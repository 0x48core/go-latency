package monitoring

import (
	"context"

	cache_stampede "github.com/0x48core/go-latency/cache-stampede"
	"github.com/0x48core/go-latency/cache-stampede/coalescing"
	"github.com/0x48core/go-latency/cache-stampede/locking"
	"github.com/0x48core/go-latency/cache-stampede/xfetch"
)

// InstrumentedCache wraps a *Cache and injects OnEvent into every call.
// Use InstrumentedGet instead of cache_stampede.GetWithLock directly.
type InstrumentedCache struct {
	inner   *cache_stampede.Cache
	metrics *Metrics
}

func NewInstrumentedCache(inner *cache_stampede.Cache, m *Metrics) *InstrumentedCache {
	return &InstrumentedCache{inner: inner, metrics: m}
}

// InstrumentedGet is the monitored replacement for cache_stampede.GetWithLock.
// It injects m.OnCacheEvent into opts.OnEvent so every decision point emits
// a Prometheus metric without changing the caller's CacheOptions.
//
// Note: Go does not support generic methods on non-generic receiver types,
// so this is a package-level function rather than a method on InstrumentedCache.
func InstrumentedGet[T any](
	ctx context.Context,
	ic *InstrumentedCache,
	key string,
	fetchFn cache_stampede.FetchFunc[T],
	opts cache_stampede.CacheOptions,
) (T, error) {
	// Inject the metrics hook — preserve any existing OnEvent the caller set.
	existing := opts.OnEvent
	opts.OnEvent = func(e cache_stampede.CacheEvent) {
		ic.metrics.OnCacheEvent(e)
		if existing != nil {
			existing(e) // forward to any caller-provided hook
		}
	}
	return cache_stampede.GetWithLock(ctx, ic.inner, key, fetchFn, opts)
}

// InstrumentXFetch wires Metrics.OnXFetchEvent into an XFetchCache.
// Call once after creating the cache — the hook is reused for every Get call.
func InstrumentXFetch(c *xfetch.XFetchCache, m *Metrics) {
	c.OnEvent = m.OnXFetchEvent
}

// InstrumentCoalescing wires Metrics.OnCoalescingEvent into a CoalescingCache.
func InstrumentCoalescing(c *coalescing.CoalescingCache, m *Metrics) {
	c.OnEvent = m.OnCoalescingEvent
}

// InstrumentCircuitBreaker wires Metrics.OnCircuitBreakerEvent into a CircuitBreaker.
func InstrumentCircuitBreaker(cb *locking.CircuitBreaker, m *Metrics) {
	cb.OnEvent = m.OnCircuitBreakerEvent
}
