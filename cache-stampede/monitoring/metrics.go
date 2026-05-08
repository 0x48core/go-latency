package monitoring

import (
	"github.com/prometheus/client_golang/prometheus"

	cache_stampede "github.com/0x48core/go-latency/cache-stampede"
	"github.com/0x48core/go-latency/cache-stampede/locking"
)

// Metrics holds all Prometheus collectors for every layer of the cache stampede package.
// Create one instance with NewMetrics and wire it into each cache layer.
type Metrics struct {
	// --- cache layer (GetWithLock) ---

	// cacheRequestsTotal counts every GetWithLock call by result type.
	// result labels: hit | miss | stale | lock_contention | self_fetch | double_check_hit
	cacheRequestsTotal *prometheus.CounterVec

	// cacheFetchDuration observes how long fetchFn takes inside the lock holder.
	// Keyed by cache key so slow keys stand out.
	cacheFetchDuration *prometheus.HistogramVec

	// cacheLockAcquiredTotal counts how often this instance wins the distributed lock.
	cacheLockAcquiredTotal *prometheus.CounterVec

	// cacheSelfFetchTotal counts fallbacks where a waiter fetched directly after timeout.
	cacheSelfFetchTotal *prometheus.CounterVec

	// --- xfetch layer ---

	// xfetchRequestsTotal counts GetWithXFetch calls by result type.
	// result labels: hit | miss | early_refresh
	xfetchRequestsTotal *prometheus.CounterVec

	// xfetchFetchDuration observes computeAndStore latency.
	xfetchFetchDuration *prometheus.HistogramVec

	// xfetchBackgroundErrorTotal counts background refresh goroutine failures.
	xfetchBackgroundErrorTotal *prometheus.CounterVec

	// --- coalescing layer ---

	// coalescingEventsTotal counts dedup vs fetcher events per key.
	// type labels: dedup | fetcher
	coalescingEventsTotal *prometheus.CounterVec

	// --- circuit breaker layer ---

	// cbState is a gauge that tracks the current state of the circuit breaker.
	// state labels: closed | open | half_open
	// Value is 1 when the breaker is in that state, 0 otherwise.
	cbState *prometheus.GaugeVec

	// cbEventsTotal counts failure / success / open / closed / half_open transitions.
	cbEventsTotal *prometheus.CounterVec
}

// NewMetrics registers all collectors against reg and returns the Metrics.
// Pass prometheus.DefaultRegisterer for production, or prometheus.NewRegistry()
// in tests to get an isolated registry per test.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		cacheRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cache_requests_total",
				Help: "Total GetWithLock calls labelled by result type (hit/miss/stale/…)",
			},
			[]string{"result"},
		),
		cacheFetchDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cache_fetch_duration_seconds",
				Help:    "Time spent inside fetchFn while holding the distributed lock",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"key"},
		),
		cacheLockAcquiredTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cache_lock_acquired_total",
				Help: "Number of times this instance won the distributed lock",
			},
			[]string{"key"},
		),
		cacheSelfFetchTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cache_self_fetch_total",
				Help: "Number of times a waiter fell back to fetching directly after WaitTimeout",
			},
			[]string{"key"},
		),
		xfetchRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "xfetch_requests_total",
				Help: "Total GetWithXFetch calls labelled by result type (hit/miss/early_refresh)",
			},
			[]string{"key", "result"},
		),
		xfetchFetchDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "xfetch_fetch_duration_seconds",
				Help:    "Time spent inside fetchFn during xfetch recomputation",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"key"},
		),
		xfetchBackgroundErrorTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "xfetch_background_error_total",
				Help: "Number of xfetch background refresh goroutine failures",
			},
			[]string{"key"},
		),
		coalescingEventsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "coalescing_events_total",
				Help: "Coalescing events: dedup=waiter goroutine, fetcher=goroutine that executed fetchFn",
			},
			[]string{"key", "type"},
		),
		cbState: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "circuit_breaker_state",
				Help: "Current circuit breaker state (1 = active, 0 = inactive) per state label",
			},
			[]string{"state"},
		),
		cbEventsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "circuit_breaker_events_total",
				Help: "Circuit breaker state-change events (failure/success/open/closed/half_open)",
			},
			[]string{"event"},
		),
	}

	reg.MustRegister(
		m.cacheRequestsTotal,
		m.cacheFetchDuration,
		m.cacheLockAcquiredTotal,
		m.cacheSelfFetchTotal,
		m.xfetchRequestsTotal,
		m.xfetchFetchDuration,
		m.xfetchBackgroundErrorTotal,
		m.coalescingEventsTotal,
		m.cbState,
		m.cbEventsTotal,
	)

	// Initialise all three state gauges to 0 so they appear in /metrics immediately.
	m.cbState.WithLabelValues("closed").Set(1) // start closed
	m.cbState.WithLabelValues("open").Set(0)
	m.cbState.WithLabelValues("half_open").Set(0)

	return m
}

// ── Exported accessors (used by tests via testutil.ToFloat64) ────────────────

func (m *Metrics) CacheRequestsTotal() *prometheus.CounterVec    { return m.cacheRequestsTotal }
func (m *Metrics) CacheLockAcquiredTotal() *prometheus.CounterVec { return m.cacheLockAcquiredTotal }
func (m *Metrics) CacheFetchDuration() *prometheus.HistogramVec   { return m.cacheFetchDuration }
func (m *Metrics) CBState() *prometheus.GaugeVec                  { return m.cbState }
func (m *Metrics) CBEventsTotal() *prometheus.CounterVec          { return m.cbEventsTotal }
func (m *Metrics) CoalescingEventsTotal() *prometheus.CounterVec  { return m.coalescingEventsTotal }
func (m *Metrics) XFetchRequestsTotal() *prometheus.CounterVec    { return m.xfetchRequestsTotal }

// OnCacheEvent is wired to CacheOptions.OnEvent.
// It maps each CacheEvent.Type to the appropriate Prometheus metric.
func (m *Metrics) OnCacheEvent(e cache_stampede.CacheEvent) {
	switch e.Type {
	case cache_stampede.EventHit:
		m.cacheRequestsTotal.WithLabelValues("hit").Inc()

	case cache_stampede.EventMiss:
		m.cacheRequestsTotal.WithLabelValues("miss").Inc()

	case cache_stampede.EventStale:
		m.cacheRequestsTotal.WithLabelValues("stale").Inc()

	case cache_stampede.EventLockContention:
		m.cacheRequestsTotal.WithLabelValues("lock_contention").Inc()

	case cache_stampede.EventDoubleCheckHit:
		m.cacheRequestsTotal.WithLabelValues("double_check_hit").Inc()

	case cache_stampede.EventSelfFetch:
		m.cacheRequestsTotal.WithLabelValues("self_fetch").Inc()
		m.cacheSelfFetchTotal.WithLabelValues(e.Key).Inc()

	case cache_stampede.EventLockAcquired:
		m.cacheLockAcquiredTotal.WithLabelValues(e.Key).Inc()

	case cache_stampede.EventFetchDone:
		// Duration is only set for EventFetchDone
		m.cacheFetchDuration.WithLabelValues(e.Key).Observe(e.Duration.Seconds())
	}
}

// OnXFetchEvent is wired to XFetchCache.OnEvent.
func (m *Metrics) OnXFetchEvent(key, eventType string) {
	switch eventType {
	case "hit":
		m.xfetchRequestsTotal.WithLabelValues(key, "hit").Inc()
	case "miss":
		m.xfetchRequestsTotal.WithLabelValues(key, "miss").Inc()
	case "early_refresh":
		m.xfetchRequestsTotal.WithLabelValues(key, "early_refresh").Inc()
	case "fetch_done":
		// Duration not available here — tracked separately via HistogramVec
		// if you wrap fetchFn to measure it before passing in.
		m.xfetchFetchDuration.WithLabelValues(key).Observe(0)
	case "background_error":
		m.xfetchBackgroundErrorTotal.WithLabelValues(key).Inc()
	}
}

// OnCoalescingEvent is wired to CoalescingCache.OnEvent.
func (m *Metrics) OnCoalescingEvent(key, eventType string) {
	m.coalescingEventsTotal.WithLabelValues(key, eventType).Inc()
}

// OnCircuitBreakerEvent is wired to CircuitBreaker.OnEvent.
// It updates both the event counter and the state gauge atomically.
func (m *Metrics) OnCircuitBreakerEvent(eventType string) {
	m.cbEventsTotal.WithLabelValues(eventType).Inc()

	// Keep the state gauge consistent with each transition
	switch eventType {
	case locking.CBEventOpen:
		m.cbState.WithLabelValues("closed").Set(0)
		m.cbState.WithLabelValues("half_open").Set(0)
		m.cbState.WithLabelValues("open").Set(1)

	case locking.CBEventClosed:
		m.cbState.WithLabelValues("open").Set(0)
		m.cbState.WithLabelValues("half_open").Set(0)
		m.cbState.WithLabelValues("closed").Set(1)

	case locking.CBEventHalfOpen:
		m.cbState.WithLabelValues("open").Set(0)
		m.cbState.WithLabelValues("closed").Set(0)
		m.cbState.WithLabelValues("half_open").Set(1)
	}
}
