// loadtest/main.go
//
// Drives real traffic through every cache layer so all Grafana panels
// show live data. Run while docker-compose is up:
//
//	go run ./loadtest/main.go
//
// Design
// ──────
//  • A background goroutine generates ~20 req/s of continuous hit traffic so
//    Prometheus always has data in every scrape interval.
//  • Seven named phases run in a loop; each exercises a different metric group.
//  • Phase 3 (stampede) uses BypassSingleFlight so all 50 goroutines go through
//    the Redis lock path independently — making lock_contention visible.
//  • Phase 5 (xfetch) uses Delta=2s so the early-refresh probability is ~60 %
//    when 1 second of TTL remains (prevents the near-zero probability bug).

package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	cache_stampede "github.com/0x48core/go-latency/cache-stampede"
	"github.com/0x48core/go-latency/cache-stampede/coalescing"
	"github.com/0x48core/go-latency/cache-stampede/locking"
	"github.com/0x48core/go-latency/cache-stampede/monitoring"
	"github.com/0x48core/go-latency/cache-stampede/xfetch"
)

// ── Config ────────────────────────────────────────────────────────────────────

const (
	redisAddr   = "localhost:6379"
	metricsAddr = ":9092" // separate port so it doesn't clash with the app
)

// slowDB simulates a database query that takes between 80–150 ms.
func slowDB(ctx context.Context, key string) (string, error) {
	delay := time.Duration(80+rand.Intn(70)) * time.Millisecond
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(delay):
	}
	return fmt.Sprintf("db-value-for-%s-%d", key, time.Now().Unix()), nil
}

// flakyDB fails ~40 % of the time — used to trigger the circuit breaker.
func flakyDB(ctx context.Context, key string) (string, error) {
	if rand.Float64() < 0.4 {
		return "", fmt.Errorf("db error: connection refused")
	}
	return slowDB(ctx, key)
}

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)

	// ── Redis ──────────────────────────────────────────────────────────────
	rdb := redis.NewClient(&redis.Options{
		Addr:        redisAddr,
		DialTimeout: 3 * time.Second,
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("[FATAL] cannot connect to Redis at %s: %v", redisAddr, err)
	}
	log.Printf("[INFO]  connected to Redis at %s", redisAddr)

	// ── Monitoring ─────────────────────────────────────────────────────────
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	m := monitoring.NewMetrics(reg)

	inner := cache_stampede.NewCache(rdb)
	ic := monitoring.NewInstrumentedCache(inner, m)

	xfetchCache := xfetch.NewXFetchCache(rdb)
	monitoring.InstrumentXFetch(xfetchCache, m)

	coalescingCache := coalescing.NewCoalescingCache()
	monitoring.InstrumentCoalescing(coalescingCache, m)

	cb := locking.NewCircuitBreaker(5, 15*time.Second)
	monitoring.InstrumentCircuitBreaker(cb, m)

	// Start metrics server so Prometheus scrapes this process too.
	metricsSrv := monitoring.NewMetricsServer(metricsAddr, reg)
	go func() {
		log.Printf("[INFO]  loadtest metrics on %s/metrics", metricsAddr)
		_ = metricsSrv.ListenAndServe()
	}()

	// ── Background: steady ~20 req/s of cache hits ─────────────────────────
	// This ensures Prometheus always sees active series between burst phases,
	// preventing "No data" caused by gaps wider than the rate window.
	bgCtx, bgCancel := context.WithCancel(ctx)
	defer bgCancel()
	go runBackground(bgCtx, ic)

	// ── Run phases in a loop forever ───────────────────────────────────────
	log.Println("[INFO]  starting load test — Ctrl+C to stop")
	log.Println("[INFO]  open Grafana at http://localhost:3000")

	for {
		runAllPhases(ctx, ic, xfetchCache, coalescingCache, cb, rdb)
	}
}

// background traffic config
const (
	bgKeyCount  = 100  // pool size — keeps singleflight collision rate low at high RPS
	bgTargetRPS = 1000 // requests per second to sustain
	bgWorkers   = 50   // max goroutines in flight at once
)

// runBackground seeds bgKeyCount keys in parallel, then drives bgTargetRPS req/s
// of cache hits using a ticker + bounded goroutine pool.
//
// Architecture
//   ticker fires every 1 ms (= 1 000 ticks/s)
//   semaphore caps concurrency at bgWorkers (prevents goroutine explosion if
//   Redis is slow — extra ticks are shed rather than queued)
//   100-key pool means singleflight only collapses ~10 req/s per key at 1k RPS
func runBackground(ctx context.Context, ic *monitoring.InstrumentedCache) {
	// ── Parallel seed: 20 concurrent goroutines, ~750 ms total ───────────────
	log.Printf("[BG]  seeding %d background keys (parallel)…", bgKeyCount)
	seedSem := make(chan struct{}, 20)
	var seedWg sync.WaitGroup
	for i := 0; i < bgKeyCount; i++ {
		seedWg.Add(1)
		seedSem <- struct{}{}
		key := fmt.Sprintf("bg:key:%d", i)
		go func(k string) {
			defer seedWg.Done()
			defer func() { <-seedSem }()
			_, _ = monitoring.InstrumentedGet(ctx, ic, k,
				func(c context.Context) (string, error) { return slowDB(c, k) },
				defaultOpts(60*time.Second),
			)
		}(key)
	}
	seedWg.Wait()
	log.Printf("[BG]  key pool ready — driving ~%d req/s", bgTargetRPS)

	// ── Rate-limited worker pool ──────────────────────────────────────────────
	tick := time.NewTicker(time.Second / bgTargetRPS) // 1 ms per tick
	defer tick.Stop()
	sem := make(chan struct{}, bgWorkers)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			select {
			case sem <- struct{}{}: // acquire worker slot
				key := fmt.Sprintf("bg:key:%d", rand.Intn(bgKeyCount))
				go func(k string) {
					defer func() { <-sem }()
					_, _ = monitoring.InstrumentedGet(ctx, ic, k,
						func(c context.Context) (string, error) { return slowDB(c, k) },
						defaultOpts(60*time.Second),
					)
				}(key)
			default:
				// All workers busy — shed this tick rather than queue unboundedly.
			}
		}
	}
}

func runAllPhases(
	ctx context.Context,
	ic *monitoring.InstrumentedCache,
	xfetchCache *xfetch.XFetchCache,
	coalescingCache *coalescing.CoalescingCache,
	cb *locking.CircuitBreaker,
	rdb *redis.Client,
) {
	phaseWarmUp(ctx, ic)
	phaseSteadyState(ctx, ic)
	phaseStampede(ctx, ic)
	phaseStaleData(ctx, ic, rdb)
	phaseSelfFetch(ctx, ic, rdb)       // Phase 4b — forces self-fetch fallback
	phaseXFetch(ctx, xfetchCache, rdb)
	phaseXFetchErrors(ctx, xfetchCache) // Phase 5b — forces background refresh errors
	phaseCoalescingDedup(ctx, coalescingCache)
	phaseCircuitBreaker(ctx, ic, cb, rdb)

	log.Println("[INFO]  cycle complete — sleeping 2s before next cycle")
	time.Sleep(2 * time.Second)
}

// ── Phase 1: Warm-up ──────────────────────────────────────────────────────────
// Cold start: 20 unique keys → all misses, lock acquired, fetches recorded.

func phaseWarmUp(ctx context.Context, ic *monitoring.InstrumentedCache) {
	log.Println("[PHASE 1] warm-up — 20 cold misses")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		key := fmt.Sprintf("warmup:key:%d", i)
		go func(k string) {
			defer wg.Done()
			_, _ = monitoring.InstrumentedGet(ctx, ic, k,
				func(ctx context.Context) (string, error) { return slowDB(ctx, k) },
				defaultOpts(10*time.Second),
			)
		}(key)
	}
	wg.Wait()
	log.Println("[PHASE 1] done")
}

// ── Phase 2: Steady-state ─────────────────────────────────────────────────────
// Re-read the same 20 keys → all hits.  Hit rate should reach ~100 %.

func phaseSteadyState(ctx context.Context, ic *monitoring.InstrumentedCache) {
	log.Println("[PHASE 2] steady-state — 100 cache hits")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		key := fmt.Sprintf("warmup:key:%d", rand.Intn(20))
		go func(k string) {
			defer wg.Done()
			_, _ = monitoring.InstrumentedGet(ctx, ic, k,
				func(ctx context.Context) (string, error) { return slowDB(ctx, k) },
				defaultOpts(10*time.Second),
			)
		}(key)
		time.Sleep(10 * time.Millisecond) // spread over ~1s
	}
	wg.Wait()
	log.Println("[PHASE 2] done")
}

// ── Phase 3: Stampede simulation ─────────────────────────────────────────────
// Flush one hot key, then hammer it with 50 concurrent goroutines.
//
// BypassSingleFlight is set so every goroutine enters the Redis lock path
// independently — simulating a real stampede across multiple processes.
// Without it, singleflight collapses 50 goroutines into one getInternal call,
// making lock_contention, stale, and pubsub_received completely invisible.
//
// Expected panel spikes: lock_contention, lock_acquired, stale (if stale exists).

func phaseStampede(ctx context.Context, ic *monitoring.InstrumentedCache) {
	log.Println("[PHASE 3] stampede — 50 goroutines on 1 cold key (singleflight bypassed)")

	key := fmt.Sprintf("stampede:key:%d", time.Now().Unix())

	stampedeOpts := cache_stampede.CacheOptions{
		TTL:                30 * time.Second,
		StateTTL:           5 * time.Minute,
		LockTimeout:        5 * time.Second,
		WaitTimeout:        2 * time.Second,
		BypassSingleFlight: true, // let every goroutine race to Redis
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = monitoring.InstrumentedGet(ctx, ic, key,
				func(ctx context.Context) (string, error) { return slowDB(ctx, key) },
				stampedeOpts,
			)
		}()
	}
	wg.Wait()
	log.Println("[PHASE 3] done")
}

// ── Phase 4: Stale data scenario ──────────────────────────────────────────────
// Write a stale key manually, then hold the primary lock so every hit
// returns stale.  Panel: stale counter climbs.

func phaseStaleData(ctx context.Context, ic *monitoring.InstrumentedCache, rdb *redis.Client) {
	log.Println("[PHASE 4] stale — expire primary, serve stale")

	key := "stale:demo:key"

	// Seed stale data directly.
	_ = rdb.Set(ctx, "stale:"+key, `"stale-value-from-previous-epoch"`, 5*time.Minute).Err()
	// Hold the lock so callers always take the stale path.
	_ = rdb.Set(ctx, "lock:"+key, "fake-lock-holder", 3*time.Second).Err()

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = monitoring.InstrumentedGet(ctx, ic, key,
				func(ctx context.Context) (string, error) { return slowDB(ctx, key) },
				defaultOpts(2*time.Second),
			)
		}()
		time.Sleep(20 * time.Millisecond)
	}
	wg.Wait()

	// Clean up so the phase doesn't bleed into the next cycle.
	_ = rdb.Del(ctx, "lock:"+key, "stale:"+key).Err()
	log.Println("[PHASE 4] done")
}

// ── Phase 4b: Self-fetch fallback ────────────────────────────────────────────
// Hold a fake lock with NO stale data, then send goroutines with a very short
// WaitTimeout (200 ms) so they time out waiting for pubsub and fall back to
// fetching directly.
//
// Why normal phases never trigger this:
//   WaitTimeout=2s but slowDB finishes in 80–150ms → pubsub always fires first.
//   Here we make WaitTimeout shorter than the fake lock TTL so waiters always
//   time out.
//
// Panel: self_fetch counter climbs.

func phaseSelfFetch(ctx context.Context, ic *monitoring.InstrumentedCache, rdb *redis.Client) {
	log.Println("[PHASE 4b] self-fetch — short WaitTimeout forces fallback")

	key := "selffetch:demo:key"

	// Hold the lock for 5s with no stale data so every goroutine ends up in
	// waitForResult and then hits the timeout branch.
	_ = rdb.Del(ctx, "cache:"+key, "stale:"+key).Err()
	_ = rdb.Set(ctx, "lock:"+key, "fake-holder", 5*time.Second).Err()

	selfFetchOpts := cache_stampede.CacheOptions{
		TTL:                30 * time.Second,
		StateTTL:           5 * time.Minute,
		LockTimeout:        5 * time.Second,
		WaitTimeout:        200 * time.Millisecond, // shorter than fake lock → timeout guaranteed
		BypassSingleFlight: true,                   // every goroutine enters waitForResult
	}

	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = monitoring.InstrumentedGet(ctx, ic, key,
				func(ctx context.Context) (string, error) { return slowDB(ctx, key) },
				selfFetchOpts,
			)
		}()
		time.Sleep(30 * time.Millisecond)
	}
	wg.Wait()

	// Clean up so the key doesn't interfere with the next cycle.
	_ = rdb.Del(ctx, "cache:"+key, "stale:"+key, "lock:"+key).Err()
	log.Println("[PHASE 4b] done")
}

// ── Phase 5: XFetch early refresh ────────────────────────────────────────────
// Entries are created with a 5s TTL.  After 4s (1s remaining) the XFetch
// probability formula evaluates to:
//
//	P = exp(-beta * remaining / delta) = exp(-1 * 1 / 2) ≈ 0.60
//
// — about 60 % of reads trigger an early background refresh.
// Delta=2.0 (2× the typical fetch latency) is deliberately large so the
// formula fires well before expiry rather than only in the last few ms.
//
// Panel: xfetch early_refresh counter climbs.

func phaseXFetch(ctx context.Context, xfetchCache *xfetch.XFetchCache, rdb *redis.Client) {
	log.Println("[PHASE 5] xfetch early refresh")

	opts := xfetch.XFetchOptions{
		TTL:   5 * time.Second,
		Delta: 2.0, // 2 s — ensures ~60 % refresh probability at 1 s remaining
		Beta:  1.0,
	}

	// 5a: cold misses — populate the xfetch keys.
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("xfetch:item:%d", i)
		_, _ = xfetch.GetWithXFetch(ctx, xfetchCache, key,
			func(ctx context.Context) (string, error) { return slowDB(ctx, key) },
			opts,
		)
	}

	// 5b: wait for entries to nearly expire, then read.
	log.Println("[PHASE 5] waiting 4s for keys to approach expiry…")
	time.Sleep(4 * time.Second)

	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("xfetch:item:%d", rand.Intn(10))
		_, _ = xfetch.GetWithXFetch(ctx, xfetchCache, key,
			func(ctx context.Context) (string, error) { return slowDB(ctx, key) },
			opts,
		)
		time.Sleep(50 * time.Millisecond)
	}

	log.Println("[PHASE 5] done")
}

// ── Phase 5b: XFetch background errors ───────────────────────────────────────
// Populate a key with a very short TTL (1 s), wait until it is near expiry,
// then read it with a fetchFn that always fails.
//
// Flow:
//   1. Key is populated (Delta=0.8 s so probability ≈ 1 at 0s remaining).
//   2. After 900 ms the key has ~100 ms left → P ≈ exp(-1*0.1/0.8) ≈ 0.88.
//   3. 10 reads hit the cache; most trigger early_refresh.
//   4. refreshInBackground calls the failing fetchFn → background_error fired.
//
// Panel: xfetch_background_error_total climbs.

func phaseXFetchErrors(ctx context.Context, c *xfetch.XFetchCache) {
	log.Println("[PHASE 5b] xfetch background errors — injecting failing fetchFn")

	opts := xfetch.XFetchOptions{
		TTL:   1 * time.Second,
		Delta: 0.8, // large relative to TTL → high refresh probability near expiry
		Beta:  1.0,
	}

	key := "xfetch:error:key"

	// Seed the key with a reliable fetchFn.
	_, _ = xfetch.GetWithXFetch(ctx, c, key,
		func(ctx context.Context) (string, error) { return "seed-value", nil },
		opts,
	)

	// Wait until the key is near expiry.
	time.Sleep(900 * time.Millisecond)

	// Now read with a fetchFn that always fails.
	// When early_refresh fires, the background goroutine will call this and
	// emit EventBackgroundError.
	failingFetch := func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("injected fetch error")
	}

	for i := 0; i < 10; i++ {
		_, _ = xfetch.GetWithXFetch(ctx, c, key, failingFetch, opts)
		time.Sleep(20 * time.Millisecond)
	}

	log.Println("[PHASE 5b] done")
}

// ── Phase 6: Coalescing dedup ────────────────────────────────────────────────
// 40 goroutines hit the same 3 keys simultaneously.
// Panel: coalescing dedup counter >> fetcher counter.

func phaseCoalescingDedup(ctx context.Context, c *coalescing.CoalescingCache) {
	log.Println("[PHASE 6] coalescing — 40 goroutines × 3 keys")

	keys := []string{"coalesce:user:1", "coalesce:user:2", "coalesce:user:3"}

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		key := keys[i%len(keys)]
		go func(k string) {
			defer wg.Done()
			_, _ = coalescing.Get(ctx, c, k,
				func(ctx context.Context) (string, error) {
					time.Sleep(80 * time.Millisecond) // keep the in-flight slot open
					return "coalesced-" + k, nil
				},
			)
		}(key)
	}
	wg.Wait()
	log.Println("[PHASE 6] done")
}

// ── Phase 7: Circuit breaker ──────────────────────────────────────────────────
// Inject a flaky DB so failures accumulate and the breaker opens.
// Panel: CB state transitions Closed → Open → Half-Open → Closed.

func phaseCircuitBreaker(
	ctx context.Context,
	ic *monitoring.InstrumentedCache,
	cb *locking.CircuitBreaker,
	rdb *redis.Client,
) {
	log.Println("[PHASE 7] circuit breaker — injecting failures")

	// Flush any cached value so fetchFn is always called.
	for i := 0; i < 10; i++ {
		_ = rdb.Del(ctx, fmt.Sprintf("cache:cb:key:%d", i)).Err()
	}

	opts := defaultOpts(2 * time.Second)

	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			key := fmt.Sprintf("cb:key:%d", i)
			go func(k string) {
				defer wg.Done()
				if !cb.Allow() {
					log.Printf("[PHASE 7] circuit OPEN — fast-rejecting %s", k)
					return
				}
				_, err := monitoring.InstrumentedGet(ctx, ic, k,
					func(ctx context.Context) (string, error) { return flakyDB(ctx, k) },
					opts,
				)
				if err != nil {
					cb.RecordFailure()
				} else {
					cb.RecordSuccess()
				}
			}(key)
		}
		wg.Wait()

		log.Printf("[PHASE 7] round %d complete — sleeping 2s", round+1)
		time.Sleep(2 * time.Second)
	}

	// Let the breaker reset.
	log.Println("[PHASE 7] waiting 16s for breaker to half-open…")
	time.Sleep(16 * time.Second)
	cb.Allow()         // trigger half-open transition
	cb.RecordSuccess() // close the breaker
	log.Println("[PHASE 7] done — breaker should be closed again")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func defaultOpts(lockTimeout time.Duration) cache_stampede.CacheOptions {
	return cache_stampede.CacheOptions{
		TTL:         30 * time.Second,
		StateTTL:    5 * time.Minute,
		LockTimeout: lockTimeout,
		WaitTimeout: 2 * time.Second,
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
