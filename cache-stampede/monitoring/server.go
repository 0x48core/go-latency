package monitoring

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewMetricsServer creates an HTTP server that exposes Prometheus metrics
// at GET /metrics. Use a custom gatherer (e.g. prometheus.NewRegistry())
// in tests so metric state is isolated between test runs.
func NewMetricsServer(addr string, gatherer prometheus.Gatherer) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		EnableOpenMetrics: true, // emit OpenMetrics format when requested
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

// StartMetricsServer starts the metrics HTTP server using the default
// Prometheus registry. It blocks until the context is cancelled, then
// performs a graceful shutdown.
//
// Usage:
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
//	defer stop()
//	monitoring.StartMetricsServer(ctx, ":9090")
func StartMetricsServer(ctx context.Context, addr string) {
	srv := NewMetricsServer(addr, prometheus.DefaultGatherer)

	go func() {
		log.Printf("metrics server listening on %s/metrics", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("metrics server error: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("metrics server shutdown error: %v", err)
	}
}
