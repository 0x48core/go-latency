package config

import (
	"os"
	"time"
)

type Config struct {
	L1TTL         time.Duration
	L2TTL         time.Duration
	StaleTTL      time.Duration
	RedisAddr     string
	RedisPassword string
	WeatherAPIKey string
	Port          string
}

func Load() Config {
	return Config{
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		L1TTL:         getDuration("L1_TTL", 1*time.Minute),
		L2TTL:         getDuration("L2_TTL", 5*time.Minute),
		StaleTTL:      getDuration("STALE_TTL", 1*time.Hour),
		WeatherAPIKey: getEnv("WEATHER_API_KEY", "demo"),
		Port:          getEnv("PORT", "8080"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
