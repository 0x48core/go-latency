package xfetch

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"
)

// XFetch event type constants — passed to XFetchCache.OnEvent.
const (
	EventHit          = "hit"           // value returned from cache (may trigger background refresh)
	EventMiss         = "miss"          // cache miss; computing synchronously
	EventEarlyRefresh = "early_refresh" // probabilistic early refresh triggered
)

// XFetchOptions configures the behavior of the XFetch algorithm.
type XFetchOptions struct {
	TTL   time.Duration // how long the fresh value lives in Redis
	Delta float64       // expected computation time in seconds (initial estimate)
	Beta  float64       // refresh aggressiveness: 0.5=aggressive, 1.0=balanced, 2.0=conservative
}

// CacheEntry is what gets serialised to JSON and stored in Redis.
// It carries the value plus the metadata needed to decide
// whether to trigger an early refresh on the next read.
type CacheEntry[T any] struct {
	Value  T       `json:"value"`
	Expiry float64 `json:"expiry"` // Unix timestamp (seconds) when the primary TTL expires
	Delta  float64 `json:"delta"`  // how long the last real fetch actually took (seconds)
}

// XFetchCache wraps a Redis client.
// Create one instance per application and reuse it across calls.
// Set OnEvent to receive observability events (e.g. for Prometheus metrics).
type XFetchCache struct {
	rdb     *redis.Client
	OnEvent func(key, eventType string) // optional; nil = no-op
}

func NewXFetchCache(rdb *redis.Client) *XFetchCache {
	return &XFetchCache{rdb: rdb}
}

// emitXFetch fires c.OnEvent if set.
func (c *XFetchCache) emitXFetch(key, eventType string) {
	if c.OnEvent != nil {
		c.OnEvent(key, eventType)
	}
}

// GetWithXFetch retrieves a value using the XFetch probabilistic early-recomputation algorithm.
//
// On every cache hit it evaluates:
//
//	P = exp( -beta * timeRemaining / delta )
//
// This probability is near 0 when the key is fresh and rises toward 1
// as expiry approaches — so refreshes are spread out rather than all
// happening at the exact moment the key expires (stampede).
//
// If the probability check fires, the stale value is returned immediately
// while a background goroutine silently recomputes and overwrites the entry.
//
// On a cache miss the value is computed synchronously before returning.
func GetWithXFetch[T any](
	ctx context.Context,
	c *XFetchCache,
	key string,
	fetchFn func(ctx context.Context) (T, error),
	opts XFetchOptions,
) (T, error) {
	cacheKey := "xfetch:" + key

	raw, err := c.rdb.Get(ctx, cacheKey).Result()
	if err == nil {
		// Cache hit — unmarshal the stored entry
		var entry CacheEntry[T]
		if jsonErr := json.Unmarshal([]byte(raw), &entry); jsonErr == nil {
			// Convert current time to float64 seconds to match Expiry precision
			now := float64(time.Now().UnixNano()) / 1e9
			timeRemaining := entry.Expiry - now

			// XFetch formula: probability grows exponentially as TTL shrinks.
			// When timeRemaining is large  → probability ≈ 0  (no refresh needed)
			// When timeRemaining ≈ 0       → probability ≈ 1  (refresh very likely)
			probability := math.Exp(-opts.Beta * timeRemaining / entry.Delta)

			if rand.Float64() < probability {
				// Trigger a background refresh — the caller gets the stale value
				// immediately without waiting for recomputation
				c.emitXFetch(key, EventEarlyRefresh)
				refreshInBackground(c, key, cacheKey, fetchFn, opts)
			}

			c.emitXFetch(key, EventHit)
			return entry.Value, nil
		}
	}

	// Cache miss (or corrupt entry) — must compute synchronously
	c.emitXFetch(key, EventMiss)
	return computeAndStore(ctx, c, key, cacheKey, fetchFn, opts)
}
