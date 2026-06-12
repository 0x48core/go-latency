package cacheaside

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

type UserService struct {
	db    *sql.DB
	cache *redis.Client
}

type User struct {
	ID    string
	Name  string
	Email string
}

func (s *UserService) GetUser(ctx context.Context, userID string) (*User, error) {
	cacheKey := fmt.Sprintf("user:%s", userID)

	// Step 1: lookup cache
	val, err := s.cache.Get(ctx, cacheKey).Result()
	if err == nil {
		// Cache HIT -> deserialize & return
		var user User
		if err := json.Unmarshal([]byte(val), &user); err != nil {
			return nil, err
		}
		return &user, nil
	}

	if !errors.Is(err, redis.Nil) {
		log.Printf("redis error: %v", err)
	}

	// Step 2: Cache MISS → App query DB
	user, err := s.fetchUserFromDB(ctx, userID)
	if err != nil {
		return nil, err
	}

	data, _ := json.Marshal(user)
	s.cache.Set(ctx, cacheKey, data, 5*time.Minute)

	return user, nil
}

func (s *UserService) fetchUserFromDB(ctx context.Context, userID string) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, email FROM users WHERE id = $1", userID,
	).Scan(&u.ID, &u.Name, &u.Email)
	return &u, err
}
