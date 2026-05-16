package redis_json

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type User struct {
	Status    string
	Balance   float64
	LastLogin time.Time
}

// Strategy 1: Optimistic Locking with WATCH
func strategy1(ctx context.Context, rdb *redis.Client) {
	err := rdb.Watch(ctx, func(tx *redis.Tx) error {
		// Read current value
		val, err := tx.Get(ctx, "user:1001").Result()
		if err != nil {
			return err
		}

		var user User
		json.Unmarshal([]byte(val), &user)
		user.Balance += 100

		updated, _ := json.Marshal(user)

		// Only write when key isn't changed
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, "user:1001", updated, 0)
			return nil
		})

		return err
	}, "user:1001")

	if err == redis.TxFailedErr {
		// Retry when conflict
		slog.Error(err.Error())
	}
}

// Strategy 2: Use Hash to completely avoid conflicts
func strategy2(ctx context.Context, rdb *redis.Client) {
	// Auth Service
	rdb.HSet(ctx, "user:1001", "last_login", time.Now().Unix())

	// Payment service
	rdb.HSet(ctx, "user:1001", "balance", 500)
}

// Strategy 3: Lua Script for atomic read-modify-write
func strategy3(ctx context.Context, rdb *redis.Client) {
	luaScript := redis.NewScript(`
    local val = redis.call('GET', KEYS[1])
    local obj = cjson.decode(val)
    obj[ARGV[1]] = ARGV[2]
    redis.call('SET', KEYS[1], cjson.encode(obj))
    return 1
`)

	luaScript.Run(ctx, rdb,
		[]string{"user:1001"},
		"status", "online",
	)
}
