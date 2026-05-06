package locking

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type CircuitState int32

const (
	StateClosed   CircuitState = iota // active normally
	StateOpen                         // opening, reject fast
	StateHalfOpen                     // trying to recover
)

type CircuitBreaker struct {
	state        atomic.Int32
	failures     atomic.Int32
	lastFailure  atomic.Int64 // unix nano
	threshold    int32
	resetTimeout time.Duration
}

func NewCircuitBreaker(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	cb := &CircuitBreaker{
		threshold:    int32(threshold),
		resetTimeout: resetTimeout,
	}
	cb.state.Store(int32(StateClosed))
	return cb
}

func (cb *CircuitBreaker) Allow() bool {
	state := CircuitState(cb.state.Load())
	switch state {
	case StateClosed:
		return true
	case StateOpen:
		last := time.Unix(0, cb.lastFailure.Load())
		if time.Since(last) > cb.resetTimeout {
			// Try to half-open
			cb.state.CompareAndSwap(int32(StateOpen), int32(StateHalfOpen))
			return true
		}
		return false
	case StateHalfOpen:
		return true
	}
	return false
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.failures.Store(0)
	cb.state.Store(int32(StateClosed))
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.lastFailure.Store(time.Now().UnixNano())
	failures := cb.failures.Add(1)
	if failures >= cb.threshold {
		cb.state.Store(int32(StateOpen))
	}
}

// ResilientCache combine SWR + Circuit Breaker
type ResilientCache struct {
	swr *SWRCache
	cb  *CircuitBreaker
}

var ErrCircuitOpen = errors.New("cache: circuit breaker open, DB unavailable")

func (c *ResilientCache) Get(ctx context.Context, key string, ttl time.Duration, compute func() (string, error)) (string, error) {
	if !c.cb.Allow() {
		// When the circuits open - only return stale data or an error; never hit the database
		stale, err := c.swr.rdb.Get(ctx, "stale:"+key).Result()
		if err == nil {
			return stale, nil
		}
		return "", ErrCircuitOpen
	}

	// Wrap compute to track success/failure
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
