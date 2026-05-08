package monitoring_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	cache_stampede "github.com/0x48core/go-latency/cache-stampede"
	"github.com/0x48core/go-latency/cache-stampede/coalescing"
	"github.com/0x48core/go-latency/cache-stampede/locking"
	"github.com/0x48core/go-latency/cache-stampede/monitoring"
	"github.com/0x48core/go-latency/cache-stampede/xfetch"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
)

// setup creates an isolated miniredis + Cache + Metrics for each test.
// Using a fresh prometheus.Registry per test prevents metric name conflicts.
func setup(t *testing.T) (*miniredis.Miniredis, *redis.Client, *monitoring.InstrumentedCache, *monitoring.Metrics) {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)

	inner := cache_stampede.NewCache(rdb)
	ic := monitoring.NewInstrumentedCache(inner, m)

	return mr, rdb, ic, m
}

// defaultOpts returns sensible CacheOptions for tests.
func defaultOpts() cache_stampede.CacheOptions {
	return cache_stampede.CacheOptions{
		TTL:         1 * time.Minute,
		StateTTL:    10 * time.Minute,
		LockTimeout: 5 * time.Second,
		WaitTimeout: 200 * time.Millisecond,
	}
}

// marshalString returns a JSON-encoded string value (e.g. `"hello"`)
// for seeding miniredis directly.
func marshalString(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 1: Fresh cache hit increments the hit counter
// ─────────────────────────────────────────────────────────────────────────────

func TestCacheHit(t *testing.T) {
	mr, _, ic, m := setup(t)

	// Seed a valid JSON string at the primary key
	mr.Set("cache:user:1", marshalString("alice"))

	val, err := monitoring.InstrumentedGet(context.Background(), ic, "user:1",
		func(ctx context.Context) (string, error) {
			t.Fatal("fetchFn must not be called on a cache hit")
			return "", nil
		},
		defaultOpts(),
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "alice" {
		t.Fatalf("want alice, got %s", val)
	}

	// hit counter must be 1; miss counter must be 0
	if got := testutil.ToFloat64(m.CacheRequestsTotal().WithLabelValues("hit")); got != 1 {
		t.Errorf("hit counter: want 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.CacheRequestsTotal().WithLabelValues("miss")); got != 0 {
		t.Errorf("miss counter: want 0, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 2: Cache miss → fetchFn called → lock_acquired + fetch_done recorded
// ─────────────────────────────────────────────────────────────────────────────

func TestCacheMiss(t *testing.T) {
	_, _, ic, m := setup(t)

	fetchCalled := 0
	val, err := monitoring.InstrumentedGet(context.Background(), ic, "user:2",
		func(ctx context.Context) (string, error) {
			fetchCalled++
			return "bob", nil
		},
		defaultOpts(),
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "bob" {
		t.Fatalf("want bob, got %s", val)
	}
	if fetchCalled != 1 {
		t.Fatalf("fetchFn call count: want 1, got %d", fetchCalled)
	}

	if got := testutil.ToFloat64(m.CacheRequestsTotal().WithLabelValues("miss")); got != 1 {
		t.Errorf("miss counter: want 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.CacheLockAcquiredTotal().WithLabelValues("user:2")); got != 1 {
		t.Errorf("lock_acquired_total counter: want 1, got %v", got)
	}
	// fetch_done is emitted → histogram must have exactly 1 observation
	// CollectAndCount returns the number of metric samples in the vec
	if got := testutil.CollectAndCount(m.CacheFetchDuration()); got == 0 {
		t.Errorf("fetch_duration histogram: want at least 1 observation, got 0")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 3: Stale data served when another instance holds the lock
// ─────────────────────────────────────────────────────────────────────────────

func TestStaleServed(t *testing.T) {
	mr, _, ic, m := setup(t)

	// Simulate: lock already held by another pod, stale data exists
	mr.Set("lock:user:3", "other-pod-token")
	mr.Set("stale:user:3", marshalString("stale-carol"))

	val, err := monitoring.InstrumentedGet(context.Background(), ic, "user:3",
		func(ctx context.Context) (string, error) {
			t.Fatal("fetchFn must not be called when stale data is available")
			return "", nil
		},
		defaultOpts(),
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "stale-carol" {
		t.Fatalf("want stale-carol, got %s", val)
	}

	if got := testutil.ToFloat64(m.CacheRequestsTotal().WithLabelValues("stale")); got != 1 {
		t.Errorf("stale counter: want 1, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 4: Self-fetch triggered when WaitTimeout expires with no stale data
// ─────────────────────────────────────────────────────────────────────────────

func TestSelfFetch(t *testing.T) {
	mr, _, ic, m := setup(t)

	// Simulate: lock held by another pod, no stale data → waiter path
	mr.Set("lock:user:4", "other-pod-token")

	opts := defaultOpts()
	opts.WaitTimeout = 20 * time.Millisecond // expire quickly so the test is fast

	val, err := monitoring.InstrumentedGet(context.Background(), ic, "user:4",
		func(ctx context.Context) (string, error) {
			return "self-fetched-dave", nil
		},
		opts,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "self-fetched-dave" {
		t.Fatalf("want self-fetched-dave, got %s", val)
	}

	if got := testutil.ToFloat64(m.CacheRequestsTotal().WithLabelValues("self_fetch")); got != 1 {
		t.Errorf("self_fetch counter: want 1, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 5: Circuit breaker state gauge reflects Open → HalfOpen → Closed
// ─────────────────────────────────────────────────────────────────────────────

func TestCircuitBreakerStateTransitions(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)

	cb := locking.NewCircuitBreaker(3, 50*time.Millisecond)
	monitoring.InstrumentCircuitBreaker(cb, m)

	// Initial state: closed
	if got := testutil.ToFloat64(m.CBState().WithLabelValues("closed")); got != 1 {
		t.Errorf("initial closed gauge: want 1, got %v", got)
	}

	// Record 3 failures → breaker opens
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()

	if got := testutil.ToFloat64(m.CBState().WithLabelValues("open")); got != 1 {
		t.Errorf("open gauge after failures: want 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.CBState().WithLabelValues("closed")); got != 0 {
		t.Errorf("closed gauge after open: want 0, got %v", got)
	}

	// Wait for resetTimeout → Allow() transitions to HalfOpen
	time.Sleep(60 * time.Millisecond)
	cb.Allow()

	if got := testutil.ToFloat64(m.CBState().WithLabelValues("half_open")); got != 1 {
		t.Errorf("half_open gauge: want 1, got %v", got)
	}

	// Record success → breaker closes
	cb.RecordSuccess()

	if got := testutil.ToFloat64(m.CBState().WithLabelValues("closed")); got != 1 {
		t.Errorf("closed gauge after success: want 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.CBState().WithLabelValues("open")); got != 0 {
		t.Errorf("open gauge after close: want 0, got %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 6: In-process coalescing — 10 goroutines produce 1 fetchFn call
// ─────────────────────────────────────────────────────────────────────────────

func TestCoalescingDedup(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)

	c := coalescing.NewCoalescingCache()
	monitoring.InstrumentCoalescing(c, m)

	var (
		callCount int
		mu        sync.Mutex
		wg        sync.WaitGroup
	)

	const goroutines = 10

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = coalescing.Get(context.Background(), c, "product:1",
				func(ctx context.Context) (string, error) {
					mu.Lock()
					callCount++
					mu.Unlock()
					time.Sleep(30 * time.Millisecond) // hold the in-flight slot open
					return "data", nil
				},
			)
		}()
	}
	wg.Wait()

	if callCount != 1 {
		t.Errorf("fetchFn call count: want 1, got %d", callCount)
	}

	// 9 goroutines were deduplicated; 1 was the fetcher
	dedupCount := testutil.ToFloat64(m.CoalescingEventsTotal().WithLabelValues("product:1", "dedup"))
	if dedupCount != float64(goroutines-1) {
		t.Errorf("dedup counter: want %d, got %v", goroutines-1, dedupCount)
	}

	fetcherCount := testutil.ToFloat64(m.CoalescingEventsTotal().WithLabelValues("product:1", "fetcher"))
	if fetcherCount != 1 {
		t.Errorf("fetcher counter: want 1, got %v", fetcherCount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test 7: XFetch early refresh fires when timeRemaining is tiny (Beta forced high)
// ─────────────────────────────────────────────────────────────────────────────

func TestXFetchEarlyRefresh(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	reg := prometheus.NewRegistry()
	m := monitoring.NewMetrics(reg)

	c := xfetch.NewXFetchCache(rdb)
	monitoring.InstrumentXFetch(c, m)

	opts := xfetch.XFetchOptions{
		TTL:   1 * time.Minute,
		Delta: 0.1,
		Beta:  1000, // extremely high beta forces probability ≈ 1 even when expiry is far
	}

	// Seed a CacheEntry with expiry ALREADY in the past.
	// timeRemaining < 0 → P = exp(-beta * negative / delta) = exp(large positive) >> 1
	// rand.Float64() < P is always true → early refresh guaranteed.
	type entry struct {
		Value  string  `json:"value"`
		Expiry float64 `json:"expiry"`
		Delta  float64 `json:"delta"`
	}
	e := entry{
		Value:  "cached-value",
		Expiry: float64(time.Now().Add(-10*time.Second).UnixNano()) / 1e9, // 10s in the past
		Delta:  0.1,
	}
	raw, _ := json.Marshal(e)
	mr.Set("xfetch:item:1", string(raw))

	val, err := xfetch.GetWithXFetch(context.Background(), c, "item:1",
		func(ctx context.Context) (string, error) {
			return "refreshed-value", nil
		},
		opts,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The stale value is returned immediately; background refresh runs later
	if val != "cached-value" {
		t.Fatalf("want cached-value, got %s", val)
	}

	// Give the background goroutine a moment to start
	time.Sleep(50 * time.Millisecond)

	earlyRefresh := testutil.ToFloat64(m.XFetchRequestsTotal().WithLabelValues("item:1", "early_refresh"))
	if earlyRefresh != 1 {
		t.Errorf("early_refresh counter: want 1, got %v", earlyRefresh)
	}
	hit := testutil.ToFloat64(m.XFetchRequestsTotal().WithLabelValues("item:1", "hit"))
	if hit != 1 {
		t.Errorf("xfetch hit counter: want 1, got %v", hit)
	}
}
