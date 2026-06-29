package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/0x48core/go-latency/waiting-room/internal/admission"
	"github.com/0x48core/go-latency/waiting-room/internal/queue"
)

type Handler struct {
	queue    queue.Queue
	admitter *admission.Admitter
	rate     int64 // admits/tick (for ETA calculation)
	tick     time.Duration
}

func New(queue queue.Queue, admitter *admission.Admitter, rate int64, tick time.Duration) *Handler {
	return &Handler{
		queue:    queue,
		admitter: admitter,
		rate:     rate,
		tick:     tick,
	}
}

// POST /queue/join

func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	sessionID := uuid.NewString()

	pos, err := h.queue.Enqueue(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "failed to join queue", http.StatusInternalServerError)
		return
	}

	admission.RecordJoin()

	admitsPerSec := float64(h.rate) / h.tick.Seconds()
	estimatedWaitMs := int64(float64(pos) / admitsPerSec * 1000)

	json.NewEncoder(w).Encode(map[string]any{
		"session_id":        sessionID,
		"position":          pos,
		"estimated_wait_ms": estimatedWaitMs,
	})
}

// GET /queue/status?session_id=<id>

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}

	if h.admitter.IsAdmitted(r.Context(), sid) {
		json.NewEncoder(w).Encode(map[string]any{"admitted": true})
		return
	}

	pos, err := h.queue.Position(r.Context(), sid)
	if err != nil || pos == -1 {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"admitted": false,
		"position": pos,
	})
}

// GET /resource?session_id=<id>  (the protected endpoint)

func (h *Handler) Resource(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")

	if !h.admitter.IsAdmitted(r.Context(), sid) {
		admission.RecordExpired()
		http.Error(w, "not admitted", http.StatusForbidden)
		return
	}

	h.admitter.Consume(r.Context(), sid) // one-time token

	json.NewEncoder(w).Encode(map[string]any{
		"message": "welcome — you are inside the protected resource",
	})
}
