package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	gocache "github.com/treussart/go-cache"

	apicache "github.com/0x48/go-latency/cache-layering/internal/cache"
	"github.com/0x48/go-latency/cache-layering/internal/weather"
)

type Handler struct {
	store         *apicache.Store
	weatherClient *weather.Client
	log           *slog.Logger
}

func New(store *apicache.Store, wc *weather.Client, log *slog.Logger) *Handler {
	return &Handler{store: store, weatherClient: wc, log: log}
}

// ---- Response helpers ------------------------------------------------------

type envelope struct {
	Data   any    `json:"data,omitempty"`
	Source string `json:"source"` // "l1", "l2", "stale", "live", "preload"
	Error  string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

// ---- GET /weather/{city} ---------------------------------------------------

// GetWeather serves cached weather data, fetching from the external API on miss.
func (h *Handler) GetWeather(w http.ResponseWriter, r *http.Request) {
	city := strings.ToLower(r.PathValue("city"))
	if city == "" {
		writeJSON(w, http.StatusBadRequest, envelope{Error: "city is required"})
		return
	}

	ctx := r.Context()

	// 1. Try cache (L1 → L2 → stale, handled internally by go-cache)
	cached, err := h.store.GetWeather(ctx, city)
	if err == nil {
		var wd weather.WeatherData
		if jsonErr := json.Unmarshal(cached, &wd); jsonErr == nil {
			source := "cache"
			if time.Since(wd.FetchedAt) > 1*time.Minute {
				source = "stale"
			}
			writeJSON(w, http.StatusOK, envelope{Data: wd, Source: source})
			return
		}
	}

	// 2. Cache miss or error → call the live weather API
	if !errors.Is(err, gocache.ErrCacheMiss) && err != nil {
		h.log.Warn("cache error, falling through to live API", "err", err, "city", city)
	}

	wd, apiErr := h.weatherClient.Fetch(ctx, city)
	if apiErr != nil {
		h.log.Error("weather API failed", "err", apiErr, "city", city)
		writeJSON(w, http.StatusBadGateway, envelope{Error: "upstream weather API unavailable"})
		return
	}

	// 3. Write to cache (best-effort)
	b, _ := json.Marshal(wd)
	if setErr := h.store.SetWeather(ctx, city, b); setErr != nil {
		h.log.Warn("failed to write weather to cache", "err", setErr, "city", city)
	}

	writeJSON(w, http.StatusOK, envelope{Data: wd, Source: "live"})
}

// ---- GET /flags/{name} -----------------------------------------------------

// GetFlags returns feature flags. Falls back to safe defaults if Redis is down.
func (h *Handler) GetFlags(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		name = "defaults"
	}

	ctx := r.Context()

	// 1. Try named flag set in cache
	data, err := h.store.GetFlags(ctx, name)
	if err == nil {
		var flags map[string]any
		if jsonErr := json.Unmarshal(data, &flags); jsonErr == nil {
			writeJSON(w, http.StatusOK, envelope{Data: flags, Source: "cache"})
			return
		}
	}

	// 2. Fall back to preloaded defaults (always in L1/stale — never a cache miss)
	h.log.Warn("flag cache miss, serving preloaded defaults", "name", name, "err", err)
	defaults, defErr := h.store.GetDefaultFlags(ctx)
	if defErr != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{Error: "flags unavailable"})
		return
	}

	var flags map[string]any
	json.Unmarshal(defaults, &flags)
	writeJSON(w, http.StatusOK, envelope{Data: flags, Source: "preload"})
}

// ---- PUT /flags/{name} -----------------------------------------------------

// SetFlags stores a feature flag set into the cache.
func (h *Handler) SetFlags(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || name == "defaults" {
		writeJSON(w, http.StatusBadRequest, envelope{Error: "cannot overwrite defaults"})
		return
	}

	var flags map[string]any
	if err := json.NewDecoder(r.Body).Decode(&flags); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{Error: "invalid JSON body"})
		return
	}

	b, _ := json.Marshal(flags)
	if err := h.store.SetFlags(context.Background(), name, b); err != nil {
		h.log.Error("failed to store flags", "name", name, "err", err)
		writeJSON(w, http.StatusInternalServerError, envelope{Error: "could not store flags"})
		return
	}

	writeJSON(w, http.StatusOK, envelope{Data: flags, Source: "written"})
}
