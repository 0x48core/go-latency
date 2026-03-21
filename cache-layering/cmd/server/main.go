package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/0x48/go-latency/cache-layering/config"
	apicache "github.com/0x48/go-latency/cache-layering/internal/cache"
	"github.com/0x48/go-latency/cache-layering/internal/handler"
	"github.com/0x48/go-latency/cache-layering/internal/weather"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := config.Load()

	// ── Redis client ─────────────────────────────────────────────────────────
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		// Redis unreachable at startup — that's OK! go-cache handles this.
		// The circuit breaker + stale cache + preload will keep us alive.
		log.Warn("Redis not reachable at startup — running in degraded mode", "err", err)
	} else {
		log.Info("Redis connected", "addr", cfg.RedisAddr)
	}

	// ── Two-level cache ──────────────────────────────────────────────────────
	store, err := apicache.New(rdb, cfg.L1TTL, cfg.L2TTL, cfg.StaleTTL, log)
	if err != nil {
		log.Error("failed to init cache", "err", err)
		os.Exit(1)
	}

	// ── Dependencies ─────────────────────────────────────────────────────────
	weatherClient := weather.NewClient(cfg.WeatherAPIKey)
	h := handler.New(store, weatherClient, log)

	// ── Router ───────────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(10 * time.Second))

	r.Get("/weather/{city}", h.GetWeather)
	r.Get("/flags/{name}", h.GetFlags)
	r.Get("/flags", h.GetFlags) // returns defaults
	r.Put("/flags/{name}", h.SetFlags)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	// ── Server ───────────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("server listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// ── Graceful shutdown ────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
}
