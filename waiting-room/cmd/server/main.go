package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/0x48core/go-latency/waiting-room/internal/admission"
	"github.com/0x48core/go-latency/waiting-room/internal/handler"
	"github.com/0x48core/go-latency/waiting-room/internal/queue"
)

const (
	admitRate = 10 // admit 10 sessions per tick
	tick      = 500 * time.Millisecond
)

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: envOr("REDIS_ADDR", "localhost:6379"),
	})

	q := queue.NewRedisQueue(rdb)

	admitter := admission.New(q, rdb, admitRate, tick)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()

	go admitter.Run(ctx) // background admission loop

	h := handler.New(q, admitter, admitRate, tick)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /queue/join", h.Join)
	mux.HandleFunc("GET /queue/status", h.Status)
	mux.HandleFunc("GET /resource", h.Resource)
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: ":8080", Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Println("listening on :8080")
	srv.ListenAndServe()

}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
