package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	gocache "github.com/treussart/go-cache"
)

// FeatureFlags is the shape of our preloaded safe defaults.
type FeatureFlags struct {
	DarkMode        bool    `json:"dark_mode"`
	BetaSearch      bool    `json:"beta_search"`
	MaxResultsLimit int     `json:"max_results_limit"`
	TaxRate         float64 `json:"tax_rate"`
}

// DefaultFlags are used when Redis is unavailable AND L1 has expired.
// This is the "preload" fallback — the last line of defense.
var DefaultFlags = FeatureFlags{
	DarkMode:        false,
	BetaSearch:      false,
	MaxResultsLimit: 20,
	TaxRate:         0.1,
}

// Store wraps go-cache and exposes typed get/set methods.
type Store struct {
	c   gocache.Cacher
	log *slog.Logger
}

// New creates the two-level cache with:
//   - L1:  TinyLFU in-memory, l1TTL
//   - L2:  Redis, l2TTL
//   - CB:  circuit breaker enabled
//   - GD:  stale cache for staleTTL (0 = never expires until evicted)
//   - Pre: safe fallback defaults for feature flags
func New(
	redisClient *redis.Client,
	l1TTL, l2TTL, staleTTL time.Duration,
	logger *slog.Logger,
) (*Store, error) {
	// Preload: encode the default feature flags as bytes
	defaultBytes, err := json.Marshal(DefaultFlags)
	if err != nil {
		return nil, fmt.Errorf("marshal default flags: %w", err)
	}

	preload := map[string][]byte{
		"flags:defaults": defaultBytes,
	}

	c, err := gocache.New("weather-flag-api",
		gocache.WithRedisConn(redisClient, l2TTL),
		gocache.WithLocalCacheTinyLFU(10_000, l1TTL),
		gocache.WithCBEnabled(true),
		gocache.WithGracefulDegradation(staleTTL),
		gocache.WithPreload(preload),
	)
	if err != nil {
		return nil, fmt.Errorf("init cache: %w", err)
	}

	logger.Info("cache initialised",
		"l1_ttl", l1TTL,
		"l2_ttl", l2TTL,
		"stale_ttl", staleTTL,
	)

	return &Store{c: c, log: logger}, nil
}

// ---- Weather helpers -------------------------------------------------------

func (s *Store) GetWeather(ctx context.Context, city string) ([]byte, error) {
	key := weatherKey(city)
	data, err := s.c.Get(ctx, []byte(key))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Store) SetWeather(ctx context.Context, city string, data []byte) error {
	return s.c.Set(ctx, []byte(weatherKey(city)), data)
}

func weatherKey(city string) string {
	return "weather:" + city
}

// ---- Feature flag helpers --------------------------------------------------

func (s *Store) GetFlags(ctx context.Context, name string) ([]byte, error) {
	return s.c.Get(ctx, []byte("flags:"+name))
}

func (s *Store) SetFlags(ctx context.Context, name string, data []byte) error {
	return s.c.Set(ctx, []byte("flags:"+name), data)
}

// GetDefaultFlags returns the preloaded defaults — always available.
func (s *Store) GetDefaultFlags(ctx context.Context) ([]byte, error) {
	return s.c.Get(ctx, []byte("flags:defaults"))
}
