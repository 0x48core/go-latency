package readthrough

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type UserService struct {
	db    *sql.DB
	cache *ReadThroughCache
}

type User struct {
	ID    string
	Name  string
	Email string
}

// ReadThroughCache wrap Redis + loader function
type ReadThroughCache struct {
	cache  *redis.Client
	loader func(ctx context.Context, key string) ([]byte, error)
	ttl    time.Duration
}

func (c *ReadThroughCache) Get(ctx context.Context, key string) ([]byte, error) {
	// Step 1: cache lookup
	val, err := c.cache.Get(ctx, key).Bytes()
	if err == nil {
		return val, nil // HIT
	}

	if !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("redis error: %w", err)
	}

	// Step 2: MISS → cache call loader (query DB)
	data, err := c.loader(ctx, key)
	if err != nil {
		return nil, err
	}

	// Step 3: cache tự SET
	c.cache.Set(ctx, key, data, c.ttl)

	return data, nil
}

func NewUserService(db *sql.DB, redisClient *redis.Client) *UserService {
	cache := &ReadThroughCache{
		cache: redisClient,
		ttl:   5 * time.Minute,
		// Loader: cache layer tự biết cách fetch từ DB
		loader: func(ctx context.Context, key string) ([]byte, error) {
			userID := strings.TrimPrefix(key, "user:")
			var u User
			err := db.QueryRowContext(ctx,
				"SELECT id, name, email FROM users WHERE id = $1", userID,
			).Scan(&u.ID, &u.Name, &u.Email)
			if err != nil {
				return nil, err
			}
			return json.Marshal(u)
		},
	}
	return &UserService{
		db:    db,
		cache: cache,
	}
}

func (s *UserService) GetUser(ctx context.Context, userID string) (*User, error) {
	data, err := s.cache.Get(ctx, fmt.Sprintf("user:%s", userID))
	if err != nil {
		return nil, err
	}
	var user User
	return &user, json.Unmarshal(data, &user)
}
