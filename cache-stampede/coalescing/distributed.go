package coalescing

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// DistributedCoalescingCache prevents duplicate fetches across multiple
// instances of the same service. It combines:
//   - In-process coalescing  (CoalescingCache) — catches duplicates within one pod
//   - Distributed Redis lock (SET NX)          — catches duplicates across pods
//   - Poll-until-fresh       (waitForResult)   — non-lock-holders wait cheaply
type DistributedCoalescingCache struct {
	rdb     *redis.Client
	local   *CoalescingCache // first line of defence: free, no network round-trip
	lockTTL time.Duration    // safety expiry for the Redis lock
}

func NewDistributedCoalescingCache(rdb *redis.Client) *DistributedCoalescingCache {
	return &DistributedCoalescingCache{
		rdb:     rdb,
		local:   NewCoalescingCache(),
		lockTTL: 10 * time.Second,
	}
}

// releaseLockScript deletes the lock only when the stored token matches,
// so a slow pod never releases a lock that another pod has re-acquired.
//
// KEYS[1] = lockKey   ARGV[1] = lockToken
var releaseLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)

// GetDistributed retrieves the value for key, guaranteeing at most one
// fetchFn call across all service instances at any given time.
//
// Flow:
//  1. In-process coalescing absorbs all duplicates inside this pod for free.
//  2. The single pod-level request then races for a Redis distributed lock.
//  3. Lock acquired  → run fetchFn, JSON-encode result to Redis, release lock.
//  4. Lock not acquired → poll Redis until the lock-holder writes the result.
func GetDistributed[T any](
	ctx context.Context,
	c *DistributedCoalescingCache,
	key string,
	fetchFn func(ctx context.Context) (T, error),
) (T, error) {
	// Layer 1 — in-process dedup (no network, pure memory)
	return Get(ctx, c.local, key, func(ctx context.Context) (T, error) {
		// Layer 2 — cross-instance dedup via Redis lock
		return fetchWithDistributedLock(ctx, c, key, fetchFn)
	})
}

// fetchWithDistributedLock handles the cross-instance coordination.
// Only one pod across the cluster will call fetchFn; the others poll.
func fetchWithDistributedLock[T any](
	ctx context.Context,
	c *DistributedCoalescingCache,
	key string,
	fetchFn func(ctx context.Context) (T, error),
) (T, error) {
	resultKey := "result:" + key
	lockKey := "lock:" + key
	// Unique token per attempt — prevents pod A from releasing pod B's lock
	// if pod A's lock expired while it was still computing
	lockToken := uuid.NewString()

	// Try to become the designated fetcher across the cluster
	acquired, err := c.rdb.SetNX(ctx, lockKey, lockToken, c.lockTTL).Result()
	if err != nil {
		var zero T
		return zero, err
	}

	if acquired {
		// --- We are the designated fetcher for this key ---
		defer func() {
			// Atomic compare-and-delete: only release if our token still matches.
			// Prevents releasing a lock that expired and was re-acquired by
			// another pod while we were still computing.
			releaseLockScript.Run(ctx, c.rdb, []string{lockKey}, lockToken)
		}()

		val, err := fetchFn(ctx)
		if err != nil {
			var zero T
			return zero, err
		}

		// JSON-encode before storing so any type T (struct, int, etc.)
		// survives the Redis round-trip and can be decoded by waiting pods
		data, err := json.Marshal(val)
		if err != nil {
			// Marshal failed — return the value to the caller but skip caching
			return val, nil
		}
		c.rdb.Set(ctx, resultKey, string(data), c.lockTTL)

		return val, nil
	}

	// --- Another instance holds the lock — poll until result is ready ---
	return waitForResult[T](ctx, c, resultKey)
}

// waitForResult polls Redis until the lock-holding instance writes the result
// or the context is canceled.
func waitForResult[T any](
	ctx context.Context,
	c *DistributedCoalescingCache,
	resultKey string,
) (T, error) {
	const (
		maxRetries = 20
		interval   = 100 * time.Millisecond
	)

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-time.After(interval):
		}

		raw, err := c.rdb.Get(ctx, resultKey).Result()
		if err != nil {
			// Not ready yet — keep polling
			continue
		}

		// redis.Get returns a concrete string, NOT an interface —
		// we cannot type-assert string directly to T.
		// JSON unmarshal is the correct way to convert back to any T.
		var result T
		if jsonErr := json.Unmarshal([]byte(raw), &result); jsonErr != nil {
			var zero T
			return zero, jsonErr
		}
		return result, nil
	}

	var zero T
	return zero, ErrCoalescingTimeout
}
