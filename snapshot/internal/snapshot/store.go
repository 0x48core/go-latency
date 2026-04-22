package snapshot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/0x48core/go-latency/snapshot/internal/metrics"
	"github.com/0x48core/go-latency/snapshot/internal/repository"
)

type StatsProvider interface {
	ComputeStats(ctx context.Context) (*repository.OrderStats, error)
}

type Snapshot struct {
	Stats      *repository.OrderStats
	ComputedAt time.Time
	Version    int64
}

type Store struct {
	mu       sync.RWMutex
	current  *Snapshot
	repo     StatsProvider
	interval time.Duration
	m        *metrics.Metrics
	logger   *slog.Logger
}

func NewStore(repo StatsProvider, interval time.Duration, m *metrics.Metrics, logger *slog.Logger) *Store {
	return &Store{repo: repo, interval: interval, m: m, logger: logger}
}

func (s *Store) Get() (*Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return nil, false
	}
	return s.current, true
}

func (s *Store) refresh(ctx context.Context) {
	start := time.Now()
	stats, err := s.repo.ComputeStats(ctx)
	elapsed := time.Since(start)

	s.m.SnapshotRefreshDuration.Observe(elapsed.Seconds())

	if err != nil {
		s.logger.ErrorContext(ctx, "snapshot refresh failed", "error", err, "elapsed", elapsed)
		s.m.SnapshotRefreshTotal.WithLabelValues("error").Inc()
		return
	}

	s.mu.Lock()
	version := int64(1)
	if s.current != nil {
		version = s.current.Version + 1
	}
	s.current = &Snapshot{Stats: stats, ComputedAt: time.Now(), Version: version}
	s.mu.Unlock()

	s.m.SnapshotRefreshTotal.WithLabelValues("success").Inc()
	s.logger.InfoContext(ctx, "snapshot refreshed", "version", version, "elapsed", elapsed)
}

// Start performs an initial refresh then schedules periodic refreshes until ctx is cancelled.
func (s *Store) Start(ctx context.Context) {
	s.logger.InfoContext(ctx, "starting snapshot store", "interval", s.interval)
	s.refresh(ctx)

	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.refresh(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Continuously track snapshot staleness as a gauge.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if snap, ok := s.Get(); ok {
					s.m.SnapshotAge.Set(time.Since(snap.ComputedAt).Seconds())
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
