package config

import (
	"os"
	"time"
)

type Config struct {
	PostgresDSN      string
	Port             string
	SnapshotInterval time.Duration
}

func Load() *Config {
	interval, err := time.ParseDuration(getEnv("SNAPSHOT_INTERVAL", "30s"))
	if err != nil {
		interval = 30 * time.Second
	}
	return &Config{
		PostgresDSN:      getEnv("POSTGRES_DSN", "postgres://postgres:postgres@localhost:5432/snapshot?sslmode=disable"),
		Port:             getEnv("PORT", "8080"),
		SnapshotInterval: interval,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
