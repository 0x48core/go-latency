package locking

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type LockedCache struct {
	rdb     *redis.Client
	lockTTL time.Duration
}

func (c *LockedCache) GetWithLock(ctx context.Context, key string, ttl time.Duration, compute func() (string, error)) (string, error) {
	// Fast path: cache hit
	val, err := c.rdb.Get(ctx, key).Result()
	if err == nil {
		return val, nil
	}

	lockKey := "lock:" + key
	lockID := uuid.NewString()

	acquired, err := c.rdb.SetNX(ctx, lockKey, lockID, ttl).Result()
	if err != nil {
		return "", err
	}

	if acquired {
		defer func() {
			// Only release your own lock - use a Lua script to atomically check and delete it
			script := redis.NewScript(`
				if redis.call("GET", KEYS[1]) == ARGV[1] then
                    return redis.call("DEL", KEYS[1])
                end
                return 0
			`)
			script.Run(ctx, c.rdb, []string{lockKey}, lockID)
		}()

		fresh, err := compute()
		if err != nil {
			return "", err
		}

		c.rdb.Set(ctx, key, fresh, ttl)
		return fresh, nil
	}

	// Spin-wait with exponential backoff
	const maxRetries = 20
	wait := 50 * time.Millisecond
	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}

		val, err := c.rdb.Get(ctx, key).Result()
		if err == nil {
			return val, nil
		}
		wait = min(wait*2, 500*time.Millisecond)
	}

	return "", ErrLockTimeout
}
