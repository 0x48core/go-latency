package xfetch

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"time"
)

// XFetch compute event type constants.
const (
	EventFetchDone       = "fetch_done"       // fetchFn completed successfully
	EventBackgroundError = "background_error" // background refresh goroutine failed
)

// computeAndStore calls fetchFn, measures how long it takes, builds a
// CacheEntry with the real delta and future expiry, then writes it to Redis.
//
// It returns the fresh value even if the Redis write fails, so the caller
// always gets a result.
func computeAndStore[T any](
	ctx context.Context,
	c *XFetchCache,
	key string,
	cacheKey string,
	fetchFn func(ctx context.Context) (T, error),
	opts XFetchOptions,
) (T, error) {
	// Measure how long the actual fetch takes so future reads have an
	// accurate delta for the XFetch probability formula
	start := time.Now()

	value, err := fetchFn(ctx)
	if err != nil {
		var zero T
		return zero, err
	}

	// Real elapsed time in seconds
	delta := time.Since(start).Seconds()

	// Never let delta fall below the configured estimate — a suspiciously fast
	// result (e.g. partial data, empty response) would make the probability
	// formula too conservative and delay future proactive refreshes
	delta = math.Max(delta, opts.Delta)

	c.emitXFetch(key, EventFetchDone)

	entry := CacheEntry[T]{
		Value: value,
		// Store expiry as a float64 Unix timestamp so the probability formula
		// can do sub-second arithmetic without rounding errors
		Expiry: float64(time.Now().UnixNano())/1e9 + opts.TTL.Seconds(),
		Delta:  delta,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		// Marshalling failed — return the value anyway, just uncached
		return value, nil
	}

	// Write the full entry (value + metadata) as a single JSON blob.
	// TTL matches opts.TTL so Redis evicts it at the same time as Expiry.
	c.rdb.Set(ctx, cacheKey, string(data), opts.TTL)

	return value, nil
}

// refreshInBackground fires computeAndStore in a new goroutine and logs
// any error. It is intentionally "fire and forget" — the caller has already
// received the stale value and should not be blocked.
//
// context.Background() is used instead of the caller's ctx so that the
// refresh is not cancelled if the original HTTP request ends before the
// background write completes.
func refreshInBackground[T any](
	c *XFetchCache,
	key string,
	cacheKey string,
	fetchFn func(ctx context.Context) (T, error),
	opts XFetchOptions,
) {
	go func() {
		bgCtx := context.Background()
		_, err := computeAndStore(bgCtx, c, key, cacheKey, fetchFn, opts)
		if err != nil {
			c.emitXFetch(key, EventBackgroundError)
			log.Printf("xfetch: background refresh failed for key %s: %v", key, err)
		}
	}()
}
