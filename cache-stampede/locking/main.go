package locking

import (
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrLockTimeout = errors.New("cache: timed out waiting for lock")

func NewResilientCache(rdb *redis.Client) *ResilientCache {
	return &ResilientCache{
		swr: &SWRCache{
			rdb:         rdb,
			lockTTL:     10 * time.Second,
			staleFactor: 10, // Stale data lives 10× longer than the primary TTL
		},
		cb: NewCircuitBreaker(
			5,              // Open the circuit after 5 consecutive failures
			30*time.Second, // try covering after 30 seconds
		),
	}
}
