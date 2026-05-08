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

// CacheEvent describes a single observable decision point inside GetWithLock.
// Set OnEvent in CacheOptions to receive these events for monitoring.
type CacheEvent struct {
	Key      string
	Type     string        // one of the Event* constants below
	Duration time.Duration // populated only for EventFetchDone
}

// Event type constants for CacheEvent.Type.
const (
	EventHit            = "hit"              // fresh value returned from cache
	EventMiss           = "miss"             // primary key not found in cache
	EventDoubleCheckHit = "double_check_hit" // value appeared between lock acquire and fetch
	EventLockAcquired   = "lock_acquired"    // this caller acquired the distributed lock
	EventStale          = "stale"            // stale value served while another holder refreshes
	EventLockContention = "lock_contention"  // no stale data; this caller must wait
	EventPubSubReceived = "pubsub_received"  // notification received from lock holder
	EventSelfFetch      = "self_fetch"       // wait timed out; caller fetches directly
	EventFetchDone      = "fetch_done"       // fetchFn completed; Duration is populated
	EventFetchError     = "fetch_error"      // fetchFn returned an error
)

// CacheOptions configures the behavior of the cache.
type CacheOptions struct {
	TTL         time.Duration          // How long fresh data is valid
	StateTTL    time.Duration          // How long stale data is kept as a fallback
	LockTimeout time.Duration          // How long a distributed lock is held before expiring
	WaitTimeout time.Duration          // How long a waiter blocks before self-fetching
	OnEvent     func(event CacheEvent) // optional hook; nil = no-op

	// BypassSingleFlight skips in-process request deduplication so every caller
	// goes through the full Redis lock/stale/wait path independently.
	// Use this in load tests to simulate concurrent cross-process requests that
	// exercise lock_contention and stale-while-revalidate code paths, which are
	// invisible when singleflight collapses all goroutines into one getInternal call.
	BypassSingleFlight bool
}

// FetchFunc is any function that returns fresh data of type T.
type FetchFunc[T any] func(ctx context.Context) (T, error)

// Cache holds shared state (singleflight group) so it can be reused across
// calls for the same Redis client. Create one per application and reuse it.
type Cache struct {
	rdb   *redis.Client
	group singleflight.Group
}

func NewCache(rdb *redis.Client) *Cache {
	return &Cache{rdb: rdb}
}

// emit fires opts.OnEvent if set. No-op when OnEvent is nil.
func emit(opts CacheOptions, key, eventType string) {
	if opts.OnEvent != nil {
		opts.OnEvent(CacheEvent{Key: key, Type: eventType})
	}
}

// emitWithDuration fires opts.OnEvent with a Duration field (used for EventFetchDone).
func emitWithDuration(opts CacheOptions, key, eventType string, dur time.Duration) {
	if opts.OnEvent != nil {
		opts.OnEvent(CacheEvent{Key: key, Type: eventType, Duration: dur})
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

// GetWithLock retrieves the value for key using a layered strategy:
//
//  1. singleflight    — deduplicate in-process concurrent calls.
//  2. Cache hit       — return immediately if key is fresh.
//  3. Atomic lock+stale — one Lua round-trip to acquire lock or get stale data.
//  4. Double-check    — re-read after acquiring the lock (another holder may have written).
//  5. Pub/Sub wait    — subscribe and wait for the holder to publish "ready".
//  6. Self-fetch      — compute directly if the wait times out.
func GetWithLock[T any](
	ctx context.Context,
	c *Cache,
	key string,
	fetchFn FetchFunc[T],
	opts CacheOptions,
) (T, error) {
	// BypassSingleFlight lets every caller enter getInternal independently,
	// surfacing lock_contention / stale / pubsub events that would otherwise
	// be invisible because singleflight collapses concurrent in-process calls.
	if opts.BypassSingleFlight {
		return getInternal[T](ctx, c.rdb, key, fetchFn, opts)
	}

	// Normal path: collapse concurrent in-process requests for the same key.
	v, err, _ := c.group.Do(key, func() (any, error) {
		return getInternal[T](ctx, c.rdb, key, fetchFn, opts)
	})
	if err != nil {
		return zero[T](), err
	}
	return v.(T), nil
}

func getInternal[T any](
	ctx context.Context,
	rdb *redis.Client,
	key string,
	fetchFn FetchFunc[T],
	opts CacheOptions,
) (T, error) {
	dataKey      := "cache:"  + key
	lockKey      := "lock:"   + key
	staleKey     := "stale:"  + key
	notifyChannel := "notify:" + key

	// Fast path: fresh cache hit
	if val, err := rdb.Get(ctx, dataKey).Result(); err == nil {
		emit(opts, key, EventHit)
		return unmarshal[T](val)
	}

	// Primary key not found — record miss before deciding how to handle it
	emit(opts, key, EventMiss)

	// Atomic lock acquisition + stale read (one round-trip)
	lockToken    := uuid.New().String()
	lockTTLSec   := int(opts.LockTimeout.Seconds())

	res, err := lockOrStaleScript.Run(ctx, rdb, []string{lockKey, staleKey}, lockToken, lockTTLSec).StringSlice()
	if err != nil {
		return zero[T](), fmt.Errorf("lockOrStale script: %w", err)
	}

	switch res[0] {
	case "locked":
		// We hold the lock — responsible for fetching and caching
		emit(opts, key, EventLockAcquired)
		return fetchAndCache[T](ctx, rdb, key, dataKey, staleKey, lockKey, lockToken, notifyChannel, fetchFn, opts)

	case "stale":
		// Another holder has the lock: serve stale immediately without waiting
		emit(opts, key, EventStale)
		return unmarshal[T](res[1])

	default: // "wait"
		// No stale data; subscribe and wait for the holder to finish
		emit(opts, key, EventLockContention)
		return waitForResult[T](ctx, rdb, key, dataKey, notifyChannel, fetchFn, opts)
	}
}

// fetchAndCache fetches fresh data while holding the lock, writes it to Redis,
// publishes a notification, then releases the lock.
func fetchAndCache[T any](
	ctx context.Context,
	rdb *redis.Client,
	key, dataKey, staleKey, lockKey, lockToken, notifyChannel string,
	fetchFn FetchFunc[T],
	opts CacheOptions,
) (T, error) {
	defer releaseLock(context.Background(), rdb, lockKey, lockToken)

	// Double-check: another process may have populated the cache between our
	// lock acquisition and now (e.g. lock expired and was re-acquired).
	if val, err := rdb.Get(ctx, dataKey).Result(); err == nil {
		emit(opts, key, EventDoubleCheckHit)
		return unmarshal[T](val)
	}

	start := time.Now()
	data, err := fetchFn(ctx)
	if err != nil {
		emit(opts, key, EventFetchError)
		return zero[T](), fmt.Errorf("fetchFn: %w", err)
	}
	// Record actual fetchFn latency for monitoring (delta for xfetch, histogram for prometheus)
	emitWithDuration(opts, key, EventFetchDone, time.Since(start))

	// Marshal once; reuse for both cache keys and the Pub/Sub payload.
	raw, err := json.Marshal(data)
	if err != nil {
		return zero[T](), fmt.Errorf("marshal: %w", err)
	}

	pipe := rdb.Pipeline()
	pipe.Set(ctx, dataKey, raw, opts.TTL)
	pipe.Set(ctx, staleKey, raw, opts.StateTTL)
	pipe.Publish(ctx, notifyChannel, "ready") // wake up all subscribers
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
	key, dataKey, notifyChannel string,
	fetchFn FetchFunc[T],
	opts CacheOptions,
) (T, error) {
	// Subscribe BEFORE the final cache check to avoid the race where the
	// publisher writes and notifies between our GET and Subscribe calls.
	sub := rdb.Subscribe(ctx, notifyChannel)
	defer sub.Close()

	// One last check: the holder may have finished while we were subscribing.
	if val, err := rdb.Get(ctx, dataKey).Result(); err == nil {
		emit(opts, key, EventPubSubReceived)
		return unmarshal[T](val)
	}

	waitCtx, cancel := context.WithTimeout(ctx, opts.WaitTimeout)
	defer cancel()

	select {
	case <-sub.Channel():
		// Lock holder published "ready" — read the now-cached value.
		if val, err := rdb.Get(ctx, dataKey).Result(); err == nil {
			emit(opts, key, EventPubSubReceived)
			return unmarshal[T](val)
		}
		// Cache write may have failed on the holder side; fall through to self-fetch.

	case <-waitCtx.Done():
		// Timed out or parent context cancelled.
	}

	// Self-fetch fallback: compute the value ourselves rather than returning an error.
	emit(opts, key, EventSelfFetch)
	data, err := fetchFn(ctx)
	if err != nil {
		return zero[T](), fmt.Errorf("fetchFn (self-fetch fallback): %w", err)
	}
	// Best-effort write; never block the caller on a Redis error here.
	_ = setJSON(ctx, rdb, dataKey, data, opts.TTL)
	return data, nil
}

// releaseLock releases the distributed lock only if this process still owns it.
// Uses context.Background() so a cancelled parent ctx never blocks the release.
func releaseLock(ctx context.Context, rdb *redis.Client, lockKey, lockToken string) {
	_ = releaseLockScript.Run(ctx, rdb, []string{lockKey}, lockToken).Err()
}
