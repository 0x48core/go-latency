package admission

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/0x48core/go-latency/waiting-room/internal/queue"
)

var (
	queueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waiting_room_queue_depth",
		Help: "Current number of sessions in the waiting queue",
	})
	admitTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waiting_room_admit_total",
		Help: "Total sessions admitted",
	})
	joinTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waiting_room_join_total",
		Help: "Total sessions that joined the queue",
	})
	expiredTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waiting_room_expired_total",
		Help: "Total admitted tokens that expired without being used",
	})
	waitDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waiting_room_wait_duration_ms",
		Help:    "Time from join to admission in milliseconds",
		Buckets: prometheus.ExponentialBuckets(100, 2, 10),
	})
)

const (
	admittedKeyPrefix = "waiting-room:admitted:"
	admittedTTL       = 5 * time.Minute
)

type Admitter struct {
	queue queue.Queue
	redis *redis.Client
	rate  int64         // how many to admit per tick
	tick  time.Duration // e.g. 500ms
}

func New(q queue.Queue, rdb *redis.Client, rate int64, tick time.Duration) *Admitter {
	return &Admitter{
		queue: q,
		redis: rdb,
		rate:  rate,
		tick:  tick,
	}
}

func init() {
	prometheus.MustRegister(queueDepth, admitTotal, joinTotal, expiredTotal, waitDuration)
}

// Run starts the admission loop; blocks until ctx is canceled.
func (a *Admitter) Run(ctx context.Context) {
	ticker := time.NewTicker(a.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.admitBatch(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (a *Admitter) admitBatch(ctx context.Context) {
	ids, err := a.queue.Admit(ctx, a.rate)
	if err != nil || len(ids) == 0 {
		return
	}

	pipe := a.redis.Pipeline()
	for _, id := range ids {
		pipe.Set(ctx, admittedKeyPrefix+id, "1", admittedTTL)
	}
	pipe.Exec(ctx)
	admitTotal.Add(float64(len(ids)))
	size, _ := a.queue.Size(ctx)
	queueDepth.Set(float64(size))
}

// IsAdmitted checks whether a session has been granted entry.
func (a *Admitter) IsAdmitted(ctx context.Context, sessionID string) bool {
	val, err := a.redis.Get(ctx, admittedKeyPrefix+sessionID).Result()
	return err == nil && val == "1"
}

// Consume removes the admitted token so the session can only enter once.
func (a *Admitter) Consume(ctx context.Context, sessionID string) {
	a.redis.Del(ctx, admittedKeyPrefix+sessionID)
}

// RecordJoin increments the join counter metric.
func RecordJoin() {
	joinTotal.Inc()
}

// RecordExpired increments the expired token counter metric.
func RecordExpired() {
	expiredTotal.Inc()
}
