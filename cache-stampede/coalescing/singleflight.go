package coalescing

import (
	"context"
	"sync"
)

// call represents a single in-flight or completed request.
// It is the Go equivalent of a Promise — multiple goroutines can
// block on the done channel and all receive the result at once
// when the fetcher closes it.
type call[T any] struct {
	val  T            // written once by the fetcher goroutine
	err  error        // written once by the fetcher goroutine
	done chan struct{} // closed (not sent to) so ALL waiters wake up simultaneously
}

// CoalescingCache deduplicates concurrent requests for the same key
// within a single process. If 100 goroutines ask for "user:42" at
// the same time, only one fetchFn call is made; the other 99 wait
// and share the result.
type CoalescingCache struct {
	mu       sync.Mutex            // protects the inflight map — maps are not goroutine-safe
	inflight map[string]*call[any] // key → in-flight request
}

func NewCoalescingCache() *CoalescingCache {
	return &CoalescingCache{
		inflight: make(map[string]*call[any]),
	}
}

// Get returns the value for key, calling fetchFn at most once per
// concurrent group of callers.
//
// Flow:
//  1. Lock and check the inflight map.
//  2. If a call is already running → unlock and wait on its done channel.
//  3. If no call is running → register one, unlock, execute fetchFn,
//     clean up the map entry, then close done to wake all waiters.
func Get[T any](
	ctx context.Context,
	c *CoalescingCache,
	key string,
	fetchFn func(ctx context.Context) (T, error),
) (T, error) {
	c.mu.Lock()

	// Case 1: request already in-flight for this key
	if existing, ok := c.inflight[key]; ok {
		c.mu.Unlock() // release lock before blocking

		select {
		case <-ctx.Done():
			// Caller gave up (timeout / cancellation).
			// The original fetcher is still running for other waiters.
			var zero T
			return zero, ctx.Err()

		case <-existing.done:
			// Fetcher finished — read the result.
			// existing.val is `any` (stored from a T), so assertion to T is safe
			// as long as all callers for this key use the same T.
			return existing.val.(T), existing.err
		}
	}

	// Case 2: no in-flight request — we become the fetcher.
	// Use a distinct variable name `newCall` to avoid shadowing the
	// receiver `c *CoalescingCache` — that was the original compile error.
	newCall := &call[any]{
		done: make(chan struct{}),
	}
	c.inflight[key] = newCall
	c.mu.Unlock() // let other goroutines find and wait on newCall

	go func() {
		defer func() {
			// Step 1: remove from map so future callers start a fresh request
			c.mu.Lock()
			delete(c.inflight, key)
			c.mu.Unlock()

			// Step 2: close done — broadcasts to ALL waiting goroutines at once.
			// Sending to a channel would only wake one; closing wakes all.
			// Must happen AFTER the map delete so a late-arriving caller
			// does not find and wait on an already-completed call.
			close(newCall.done)
		}()

		val, err := fetchFn(ctx)
		// Write val/err before close(done).
		// Readers access these fields without a lock once the channel is closed —
		// writing must complete first to avoid a data race.
		newCall.val = val
		newCall.err = err
	}()

	// Wait for our own goroutine to finish (same select path as all waiters)
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-newCall.done:
		return newCall.val.(T), newCall.err
	}
}
