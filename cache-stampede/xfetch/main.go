package xfetch

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer rdb.Close()

	cache := NewXFetchCache(rdb)

	opts := XFetchOptions{
		TTL:   5 * time.Minute, // primary key lives 5 minutes
		Delta: 0.1,             // assume ~100ms fetch time as baseline
		Beta:  1.0,             // balanced: not too eager, not too lazy to refresh
	}

	ctx := context.Background()

	val, err := GetWithXFetch(ctx, cache, "user:42", func(ctx context.Context) (string, error) {
		// Simulate a slow database query
		time.Sleep(100 * time.Millisecond)
		return "user-data-from-db", nil
	}, opts)

	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	fmt.Printf("value: %s\n", val)
}
