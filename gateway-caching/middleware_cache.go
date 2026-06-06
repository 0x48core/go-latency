package gatewaycaching

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/redis/go-redis/v9"
)

type CacheMiddleware struct {
	cache *redis.Client
}

func (m *CacheMiddleware) Handle(ctx context.Context, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only cache GET requests
		if r.Method != "GET" {
			next.ServeHTTP(w, r)
			return
		}

		// Generate cache key
		cacheKey := fmt.Sprintf("cache:%s:%s", r.URL.Path, r.URL.RawQuery)

		// Try cache first
		cached, err := m.cache.Get(ctx, cacheKey).Bytes()
		if err == nil {
			w.Header().Set("X-Cache", "HIT")
			w.Write(cached)
			return
		}

		// Cache miss: call backend
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		// Cache successful responses
		if rec.Code == 200 {
			body := rec.Body.Bytes()
			m.cache.Set(ctx, cacheKey, body, 5*time.Minute)
		}

		w.Header().Set("X-Cache", "MISS")
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	})
}
