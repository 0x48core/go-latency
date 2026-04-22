package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/0x48core/go-latency/snapshot/internal/metrics"
	"github.com/0x48core/go-latency/snapshot/internal/repository"
	"github.com/0x48core/go-latency/snapshot/internal/snapshot"
)

type Handler struct {
	store   *snapshot.Store
	repo    *repository.Repository
	m       *metrics.Metrics
	logger  *slog.Logger
}

func New(store *snapshot.Store, repo *repository.Repository, m *metrics.Metrics, logger *slog.Logger) *Handler {
	return &Handler{store: store, repo: repo, m: m, logger: logger}
}

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stats", h.instrument("/api/stats", h.Stats))
	mux.HandleFunc("GET /api/stats/live", h.instrument("/api/stats/live", h.StatsLive))
	mux.HandleFunc("GET /api/health", h.Health)
	return mux
}

type statsResponse struct {
	Stats      *repository.OrderStats `json:"stats"`
	ComputedAt time.Time              `json:"computed_at"`
	Version    int64                  `json:"version,omitempty"`
	AgeSeconds float64                `json:"age_seconds"`
	Source     string                 `json:"source"`
}

// Stats serves pre-computed stats from the in-memory snapshot — typically <1ms.
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.store.Get()
	if !ok {
		http.Error(w, "snapshot not ready", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, statsResponse{
		Stats:      snap.Stats,
		ComputedAt: snap.ComputedAt,
		Version:    snap.Version,
		AgeSeconds: time.Since(snap.ComputedAt).Seconds(),
		Source:     "snapshot",
	})
}

// StatsLive computes stats directly from the database on every request — typically 100–500ms.
func (h *Handler) StatsLive(w http.ResponseWriter, r *http.Request) {
	stats, err := h.repo.ComputeStats(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "live stats query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, statsResponse{
		Stats:      stats,
		ComputedAt: time.Now(),
		AgeSeconds: 0,
		Source:     "live",
	})
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"status": "ok"}
	if snap, ok := h.store.Get(); ok {
		resp["snapshot_version"] = snap.Version
		resp["snapshot_age_seconds"] = time.Since(snap.ComputedAt).Seconds()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) instrument(path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next(rw, r)
		h.m.RequestDuration.WithLabelValues(path, r.Method).Observe(time.Since(start).Seconds())
		h.m.RequestTotal.WithLabelValues(path, r.Method, strconv.Itoa(rw.status)).Inc()
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.ResponseWriter.WriteHeader(status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
