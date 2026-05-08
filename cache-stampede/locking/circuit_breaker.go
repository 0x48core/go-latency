package locking

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// Circuit breaker event type constants — passed to CircuitBreaker.OnEvent.
const (
	CBEventFailure  = "failure"   // RecordFailure called
	CBEventSuccess  = "success"   // RecordSuccess called
	CBEventOpen     = "open"      // breaker tripped: failures >= threshold
	CBEventClosed   = "closed"    // breaker recovered: RecordSuccess after open/half-open
	CBEventHalfOpen = "half_open" // breaker allowing a probe after resetTimeout
)

type CircuitState int32

const (
	StateClosed   CircuitState = iota // normal operation
	StateOpen                         // rejecting requests fast
	StateHalfOpen                     // allowing one probe to test recovery
)

// CircuitBreaker is a lock-free three-state breaker (Closed → Open → Half-Open).
// Set OnEvent to receive state-change events for monitoring.
type CircuitBreaker struct {
	state        atomic.Int32
	failures     atomic.Int32
	lastFailure  atomic.Int64 // unix nanoseconds
	threshold    int32
	resetTimeout time.Duration
	OnEvent      func(eventType string) // optional; nil = no-op
}

func NewCircuitBreaker(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	cb := &CircuitBreaker{
		threshold:    int32(threshold),
		resetTimeout: resetTimeout,
	}
	cb.state.Store(int32(StateClosed))
	return cb
}

// emit fires cb.OnEvent if set.
func (cb *CircuitBreaker) emit(eventType string) {
	if cb.OnEvent != nil {
		cb.OnEvent(eventType)
	}
}

// Allow returns true if a request should proceed.
// Closed   → always allow.
// Open     → allow only after resetTimeout has elapsed (transitions to HalfOpen).
// HalfOpen → allow one probe through.
func (cb *CircuitBreaker) Allow() bool {
	state := CircuitState(cb.state.Load())
	switch state {
	case StateClosed:
		return true

	case StateOpen:
		last := time.Unix(0, cb.lastFailure.Load())
		if time.Since(last) > cb.resetTimeout {
			// Transition to HalfOpen to allow one probe
			if cb.state.CompareAndSwap(int32(StateOpen), int32(StateHalfOpen)) {
				cb.emit(CBEventHalfOpen)
			}
			return true
		}
		return false

	case StateHalfOpen:
		return true
	}
	return false
}

// RecordSuccess resets the failure count and closes the circuit.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.failures.Store(0)
	cb.state.Store(int32(StateClosed))
	cb.emit(CBEventSuccess)
	cb.emit(CBEventClosed)
}

// RecordFailure increments the failure counter and opens the circuit
// once the threshold is reached.
func (cb *CircuitBreaker) RecordFailure() {
	cb.lastFailure.Store(time.Now().UnixNano())
	cb.emit(CBEventFailure)

	failures := cb.failures.Add(1)
	if failures >= cb.threshold {
		cb.state.Store(int32(StateOpen))
		cb.emit(CBEventOpen)
	}
}

// ResilientCache combines SWR + Circuit Breaker for defense-in-depth.
type ResilientCache struct {
	swr *SWRCache
	cb  *CircuitBreaker
}

var ErrCircuitOpen = errors.New("cache: circuit breaker open, DB unavailable")

// Get retrieves the value, routing through the circuit breaker first.
// When the breaker is open, stale data is returned or ErrCircuitOpen is emitted.
func (c *ResilientCache) Get(ctx context.Context, key string, ttl time.Duration, compute func() (string, error)) (string, error) {
	if !c.cb.Allow() {
		// Circuit is open — only return stale data or fail fast; never hit the DB
		stale, err := c.swr.rdb.Get(ctx, "stale:"+key).Result()
		if err == nil {
			return stale, nil
		}
		return "", ErrCircuitOpen
	}

	// Wrap compute so the circuit breaker tracks real DB success/failure
	tracked := func() (string, error) {
		val, err := compute()
		if err != nil {
			c.cb.RecordFailure()
			return "", err
		}
		c.cb.RecordSuccess()
		return val, nil
	}

	return c.swr.Get(ctx, key, ttl, tracked)
}
