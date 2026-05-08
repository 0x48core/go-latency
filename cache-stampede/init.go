package cache_stampede

import (
	"context"
	"sync"

	"github.com/redis/go-redis/v9"
)

var (
	defaultCache *Cache
	defaultOnce  sync.Once
)

// Init initialises the package-level default cache. Call once at startup.
func Init(rdb *redis.Client) {
	defaultOnce.Do(func() {
		defaultCache = NewCache(rdb)
	})
}

// Get is a convenience wrapper around GetWithLock using the default cache.
func Get[T any](ctx context.Context, key string, fetchFn FetchFunc[T], opts CacheOptions) (T, error) {
	if defaultCache == nil {
		panic("cache: Init() must be called before Get()")
	}
	// TODO:
}
