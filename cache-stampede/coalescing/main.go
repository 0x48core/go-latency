package coalescing

import (
	"github.com/redis/go-redis/v9"
)

// NewInProcessCache returns a CoalescingCache for single-instance deduplication.
// Use this when your service runs as a single process or when cross-instance
// coordination is not required.
func NewInProcessCache() *CoalescingCache {
	return NewCoalescingCache()
}

// NewDistributedCache returns a DistributedCoalescingCache that layers
// in-process coalescing (singleflight) on top of a Redis distributed lock.
// Use this in a multi-pod / horizontally scaled deployment.
func NewDistributedCache(rdb *redis.Client) *DistributedCoalescingCache {
	return NewDistributedCoalescingCache(rdb)
}
