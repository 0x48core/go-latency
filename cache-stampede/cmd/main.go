package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	cache_stampede "github.com/0x48core/go-latency/cache-stampede"
	"github.com/0x48core/go-latency/cache-stampede/monitoring"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

func main() {
	// ── Config from environment ───────────────────────────────────────────
	redisAddr  := getenv("REDIS_ADDR",   "localhost:6379")
	metricsAddr := getenv("METRICS_ADDR", ":9090")
	appAddr    := getenv("APP_ADDR",     ":8080")

	// ── Redis ─────────────────────────────────────────────────────────────
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})

	// ── Monitoring ────────────────────────────────────────────────────────
	reg := prometheus.NewRegistry()
	// Also register Go runtime and process collectors
	reg.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	m := monitoring.NewMetrics(reg)

	inner := cache_stampede.NewCache(rdb)
	ic    := monitoring.NewInstrumentedCache(inner, m)

	// ── App HTTP server ───────────────────────────────────────────────────
	mux := http.NewServeMux()

	// GET /get?key=<key>  — fetch a value (demonstrates cache stampede prevention)
	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "missing key param", http.StatusBadRequest)
			return
		}

		val, err := monitoring.InstrumentedGet(r.Context(), ic, key,
			func(ctx context.Context) (string, error) {
				// Simulate a slow database query
				time.Sleep(100 * time.Millisecond)
				return "value-for-" + key, nil
			},
			cache_stampede.CacheOptions{
				TTL:         30 * time.Second,
				StateTTL:    5 * time.Minute,
				LockTimeout: 5 * time.Second,
				WaitTimeout: 3 * time.Second,
			},
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"key": key, "value": val})
	})

	appSrv := &http.Server{
		Addr:    appAddr,
		Handler: mux,
	}

	// ── Graceful shutdown ─────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start metrics server in background
	metricsSrv := monitoring.NewMetricsServer(metricsAddr, reg)
	go func() {
		log.Printf("metrics server listening on %s/metrics", metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("metrics server error: %v", err)
		}
	}()

	// Start app server in background
	go func() {
		log.Printf("app server listening on %s", appAddr)
		if err := appSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("app server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down…")

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = appSrv.Shutdown(shutCtx)
	_ = metricsSrv.Shutdown(shutCtx)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
