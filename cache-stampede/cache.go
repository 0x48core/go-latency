package cache_stampede

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// CacheOptions configures the behavior of the cache.
type CacheOptions struct {
	TTL         time.Duration // How long fresh data is valid
	StateTTL    time.Duration // How long stale data is kept as a fallback
	LockTimeout time.Duration // How long a distributed lock is held before expiring
	WaitTimeout time.Duration // How long a waiter polls before giving up and fetching itself
}

// FetchFunc is any function that returns fresh data of type T
type FetchFunc[T any] func(ctx context.Context) (T, error)

// Cache holds shared state (singleflight group) so it can be reused across
// calls for the same Redis client. Create one per application and reuse it.
type Cache struct {
	rdb   *redis.Client
	group singleflight.Group
}

func NewCache(rdb *redis.Client) *Cache {
	return &Cache{
		rdb: rdb,
	}
}

// lockOrStaleScript atomically tries to acquire the lock; if it fails it
// returns the stale value (if any) in a single round-trip.
//
// KEYS[1] = lockKey   KEYS[2] = staleKey
// ARGV[1] = lockToken ARGV[2] = lockTTL (seconds)
//
// Returns a two-element array:
//
//	{"locked", ""}        – lock acquired, caller must fetch
//	{"stale",  "<json>"} – lock not acquired, stale data returned
//	{"wait",   ""}        – lock not acquired, no stale data
var lockOrStaleScript = redis.NewScript(`
local locked = redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2], 'NX')
if locked then return {'locked', ''} end
local stale = redis.call('GET', KEYS[2])
if stale then return {'stale', stale} end
return {'wait', ''}
`)

// releaseLockScript deletes the lock only when the stored token matches,
// preventing a process from releasing a lock it no longer owns.
//
// KEYS[1] = lockKey   ARGV[1] = lockToken
var releaseLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
end
return 0
`)

// GetWithLock retrieves the value for key, using the following strategy:
//
//  1. singleflight: deduplicate in-process concurrent calls for the same key.
//  2. Fresh cache hit  → return immediately.
//  3. Atomic lock+stale: try to acquire the distributed lock in one Lua
//     script; if lost and stale data exists, return stale immediately.
//  4. Double-checked locking: re-read the cache after acquiring the lock to
//     avoid a redundant fetch when another holder just wrote the value.
//  5. Pub/Sub wait: subscribe to the notification channel instead of polling;
//     the lock holder publishes once it has written the value.
//  6. Timeout self-fetch: if the wait times out, compute the value directly.
func GetWithLock[T any](
	ctx context.Context,
	c *Cache,
	key string,
	fetchFn FetchFunc[T],
	opts CacheOptions,
) (T, error) {
	// Collapse concurrent in-process requests for the same key into one.
	v, err, _ := c.group.Do(key, func() (any, error) {
		return getInternal[T](ctx, c.rdb, key, fetchFn, opts)
	})
	if err != nil {
		return zero[T](), err
	}
	return v.(T), nil
}

func getInternal[T any](ctx context.Context, rdb *redis.Client, key string, fetchFn FetchFunc[T], opts CacheOptions) (T, error) {
	dataKey := "cache:" + key
	lockKey := "lock:" + key
	staleKey := "stale:" + key
	notifyChannel := "notify:" + key

	// Refresh cache
	if val, err := rdb.Get(ctx, dataKey).Result(); err == nil {
		return unmarshal[T](val)
	}

	// Atomic lock acquisition + stale read (one round-trip)
	lockToken := uuid.New().String() // unique so we only release our own lock
	lockTTLSec := int(opts.LockTimeout.Seconds())

	res, err := lockOrStaleScript.Run(ctx, rdb, []string{lockKey, staleKey}, lockToken, lockTTLSec).StringSlice()
	if err != nil {
		return zero[T](), fmt.Errorf("lockOrStale script: %w", err)
	}

	switch res[0] {
	case "locked":
		// Hold the lock
		return fetchAndCache[T](ctx, rdb, dataKey, staleKey, lockKey, lockToken, notifyChannel, fetchFn, opts)
	case "stale":
		// Another folder has the lock: serve stale immediately
		return unmarshal[T](res[1])
	default: // "wait"
		// No stale data; subscribe and wait for the holder to finish
		return waitForResult[T](ctx, rdb, dataKey, notifyChannel, fetchFn, opts)
	}
}

// fetchAndCache fetches fresh data while holding the lock, writes it to Redis,
// publishes a notification, and releases the lock.
func fetchAndCache[T any](
	ctx context.Context,
	rdb *redis.Client,
	dataKey, staleKey, lockKey, lockToken, notifyChannel string,
	fetchFn FetchFunc[T],
	opts CacheOptions,
) (T, error) {
	defer releaseLock(context.Background(), rdb, lockKey, lockToken)

	// Double-check locking
	// Another process may have populated the cache between our lock acquisition
	// and now (e.g. lock expired and was re-acquired). Avoid a redundant fetch.
	if val, err := rdb.Get(ctx, dataKey).Result(); err == nil {
		return unmarshal[T](val)
	}

	data, err := fetchFn(ctx)
	if err != nil {
		return zero[T](), fmt.Errorf("fetchFn: %w", err)
	}

	// Marshal once; reuse the bytes for both cache keys and the return value.
	raw, err := json.Marshal(data)
	if err != nil {
		return zero[T](), fmt.Errorf("marshal: %w", err)
	}

	pipe := rdb.Pipeline()
	pipe.Set(ctx, dataKey, raw, opts.TTL)
	pipe.Set(ctx, staleKey, raw, opts.StateTTL)
	pipe.Publish(ctx, notifyChannel, "ready") // wake up subscribers
	if _, err := pipe.Exec(ctx); err != nil {
		// Return data even if caching fails so the caller still gets a result.
		return data, fmt.Errorf("redis pipeline exec: %w", err)
	}

	return data, nil
}

// waitForResult subscribes to the notify channel and returns as soon as the
// lock holder publishes "ready". Falls back to a self-fetch on timeout.
func waitForResult[T any](
	ctx context.Context,
	rdb *redis.Client,
	dataKey, notifyChannel string,
	fetchFn FetchFunc[T],
	opts CacheOptions,
) (T, error) {
	// Subscribe before the final cache check to avoid a race where the
	// publisher writes and notifies between our GET and Subscribe call.
	sub := rdb.Subscribe(ctx, notifyChannel)
	defer sub.Close()

	// One more check: the lock holder may have finished in the gap above.
	if val, err := rdb.Get(ctx, dataKey).Result(); err == nil {
		return unmarshal[T](val)
	}

	waitCtx, cancel := context.WithTimeout(ctx, opts.WaitTimeout)
	defer cancel()

	select {
	case <-sub.Channel():
		// Lock holder published; value should now be in the cache.
		if val, err := rdb.Get(ctx, dataKey).Result(); err == nil {
			return unmarshal[T](val)
		}
	// Cache write may have failed on the other side; fall through to self-fetch.
	case <-waitCtx.Done():
		// Timed out or parent context canceled.
	}

	// Self-fetch fallback
	data, err := fetchFn(ctx)
	if err != nil {
		return zero[T](), fmt.Errorf("fetchFn (self-fetch fallback): %w", err)
	}
	// Best-effort cache write; ignore error so the caller always gets data.
	_ = setJSON(ctx, rdb, dataKey, data, opts.TTL)
	return data, nil
}

// releaseLock releases the distributed lock only if we still own it.
// Uses a background context so a canceled parent ctx does not prevent release.
func releaseLock(ctx context.Context, rdb *redis.Client, lockKey, lockToken string) {
	_ = releaseLockScript.Run(ctx, rdb, []string{lockKey}, lockToken).Err()
}
