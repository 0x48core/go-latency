package locking

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

type SWRCache struct {
	rdb         *redis.Client
	lockTTL     time.Duration
	staleFactor int // stale TTL = ttl * staleFactor
}

func (c *SWRCache) Get(ctx context.Context, key string, ttl time.Duration, compute func() (string, error)) (string, error) {
	// Fast path
	val, err := c.rdb.Get(ctx, key).Result()
	if err == nil {
		return val, nil
	}

	staleKey := "stale:" + key
	lockKey := "lock:" + key

	staleVal, _ := c.rdb.Get(ctx, staleKey).Result()

	acquired, err := c.rdb.SetNX(ctx, lockKey, "1", c.lockTTL).Result()
	if err != nil {
		return staleVal, err // graceful degrade to stale if Redis errors
	}

	if !acquired {
		// If not the lock owner, return stale immediately, don't wait
		if staleVal != "" {
			return staleVal, nil
		}
		// Only wait if there is no stale data
		return c.waitForFresh(ctx, key)
	}

	// If it is lock owner
	if staleVal != "" {
		// If stale data exists - recompute asynchronously and return the stale result immediately
		go func() {
			bgCtx := context.Background() // decouple from the original request context
			fresh, err := compute()
			if err != nil {
				log.Printf("SWR recompute error for key %s: %v", key, err)
				c.rdb.Del(bgCtx, key)
				return
			}
			staleTTL := ttl * time.Duration(c.staleFactor)
			c.rdb.Set(ctx, key, fresh, ttl)
			c.rdb.Set(bgCtx, key, fresh, staleTTL)
			c.rdb.Del(bgCtx, key)
		}()
		return staleVal, nil
	}

	// If no stale data exists - compute synchronously (cold start)
	defer c.rdb.Del(ctx, lockKey)

	fresh, err := compute()
	if err != nil {
		return "", err
	}

	staleTTL := ttl * time.Duration(c.staleFactor)
	c.rdb.Set(ctx, key, fresh, ttl)
	c.rdb.Set(ctx, staleKey, fresh, staleTTL)

	return fresh, nil
}

func (c *SWRCache) waitForFresh(ctx context.Context, key string) (string, error) {
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		val, err := c.rdb.Get(ctx, key).Result()
		if err == nil {
			return val, nil
		}
	}
	return "", ErrLockTimeout
}
