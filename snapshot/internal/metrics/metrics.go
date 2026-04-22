package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	SnapshotAge             prometheus.Gauge
	SnapshotRefreshDuration prometheus.Histogram
	SnapshotRefreshTotal    *prometheus.CounterVec
	RequestDuration         *prometheus.HistogramVec
	RequestTotal            *prometheus.CounterVec
}

func New() *Metrics {
	return &Metrics{
		SnapshotAge: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "snapshot_age_seconds",
			Help: "Seconds since the current snapshot was computed",
		}),
		SnapshotRefreshDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "snapshot_refresh_duration_seconds",
			Help:    "Time taken to refresh the snapshot",
			Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}),
		SnapshotRefreshTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "snapshot_refresh_total",
			Help: "Total snapshot refresh attempts by status",
		}, []string{"status"}),
		RequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency",
			Buckets: []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1},
		}, []string{"path", "method"}),
		RequestTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests",
		}, []string{"path", "method", "status"}),
	}
}
