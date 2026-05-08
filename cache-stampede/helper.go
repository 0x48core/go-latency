package cache_stampede

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func unmarshal[T any](s string) (T, error) {
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return zero[T](), fmt.Errorf("json.Unmarshal: %w", err)
	}
	return v, nil
}

func setJSON[T any](ctx context.Context, rdb *redis.Client, key string, value T, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("json.Marshal: %w", err)
	}
	return rdb.Set(ctx, key, string(raw), ttl).Err()
}

func zero[T any]() T {
	var v T
	return v
}
