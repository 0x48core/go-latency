# Snapshot Pattern

Pre-compute expensive aggregations in the background and serve results from memory.

---

## The Problem

Some queries are inherently slow — full-table aggregations, multi-join reports, cross-service fan-outs. Running them on every request is unsustainable:

```
Client → Handler → Repository.ComputeStats() → PostgreSQL   ← 100–500 ms per request
```

Under load, each concurrent request adds the same database pressure. P99 climbs fast.

---

## The Solution

A background worker runs the expensive query on a schedule and stores the result in memory behind a read-write mutex. Handlers read the in-memory snapshot atomically — no database round-trip required:

```
Background goroutine ──── every 30 s ────► Repository.ComputeStats() → PostgreSQL
                                                       │
                                              SnapshotStore (RWMutex)
                                                       │
Client → Handler → SnapshotStore.Get() ◄───────────────┘   ← < 1 ms per request
```

The trade-off is **data freshness**: clients see results that are at most `SNAPSHOT_INTERVAL` seconds old. For most dashboards and analytics endpoints this is perfectly acceptable.

---

## Project Structure

```
snapshot/
├── cmd/server/main.go              # Entry point — wires everything together
├── config/config.go                # Env-based configuration
├── internal/
│   ├── snapshot/store.go           # SnapshotStore: background refresh + RWMutex
│   ├── repository/postgres.go      # Expensive SQL aggregations
│   ├── handler/handler.go          # HTTP handlers for both endpoints
│   └── metrics/metrics.go          # Prometheus metrics
├── migrations/001_init.sql         # Products + 500 000 orders seed data
├── docker-compose.yml
├── Dockerfile
├── prometheus.yaml
└── loadtest.sh
```

---

## Hands-On

### Prerequisites

- Docker & Docker Compose
- Go 1.25+
- Optional: [`hey`](https://github.com/rakyll/hey) for load testing (`go install github.com/rakyll/hey@latest`)

---

### Step 1 — Start the Stack

```bash
cd snapshot
docker compose up --build
```

Wait until you see a log line like:
```json
{"level":"INFO","msg":"snapshot refreshed","version":1,"elapsed":"142ms"}
```

This means the first snapshot is ready.

---

### Step 2 — Compare the Two Endpoints

**Snapshot endpoint** — served from memory, no DB query:
```bash
curl -s http://localhost:8080/api/stats | jq .
```

**Live endpoint** — full aggregation on every call:
```bash
curl -s http://localhost:8080/api/stats/live | jq .
```

Both return the same JSON shape:

```json
{
  "stats": {
    "total_orders": 500000,
    "total_revenue": 250123456.78,
    "avg_order_value": 500.25,
    "unique_customers": 9999,
    "top_categories": [
      { "category": "Electronics", "revenue": 98765432.10, "orders": 200000 }
    ]
  },
  "computed_at": "2025-01-15T10:30:00Z",
  "version": 3,
  "age_seconds": 12.4,
  "source": "snapshot"
}
```

The `source` field tells you which path served the response. Notice `age_seconds` — the snapshot is at most 30 seconds stale.

---

### Step 3 — Run the Load Test

```bash
chmod +x loadtest.sh
./loadtest.sh
```

Expected results (500 000 rows, local Docker):

| Endpoint              | p50     | p99      | RPS       |
|-----------------------|---------|----------|-----------|
| `/api/stats`          | < 1 ms  | ~2 ms    | ~5 000    |
| `/api/stats/live`     | ~150 ms | ~400 ms  | ~20–50    |

The snapshot endpoint handles 100× more requests with 100× lower latency.

---

### Step 4 — Watch the Metrics

Open Prometheus: http://localhost:9090

Useful queries:

```promql
# Snapshot staleness
snapshot_age_seconds

# How long each refresh takes (p99)
histogram_quantile(0.99, rate(snapshot_refresh_duration_seconds_bucket[1m]))

# p99 request latency per endpoint
histogram_quantile(0.99,
  sum by (path, le) (
    rate(http_request_duration_seconds_bucket[1m])
  )
)

# Requests per second by endpoint
sum by (path) (rate(http_requests_total[1m]))
```

Open Grafana: http://localhost:3000 (anonymous, no login needed).
Add `http://prometheus:9090` as a Prometheus data source and paste the queries above into a new dashboard.

---

### Step 5 — Tune the Interval

The refresh interval controls the freshness vs. database load trade-off:

```bash
# Fresher data, higher DB load
SNAPSHOT_INTERVAL=5s docker compose up

# Staler data, lower DB load
SNAPSHOT_INTERVAL=120s docker compose up
```

Watch `snapshot_age_seconds` in Prometheus to verify the change takes effect.

---

### Step 6 — Observe Graceful Startup

On startup, the store performs a blocking initial refresh before the HTTP server accepts traffic:

```
{"level":"INFO","msg":"starting snapshot store","interval":"30s"}
{"level":"INFO","msg":"snapshot refreshed","version":1,"elapsed":"143ms"}
{"level":"INFO","msg":"server starting","port":"8080"}
```

The server only starts listening after the first snapshot is ready, so `/api/stats` is never cold.

---

## Key Code Paths

### `SnapshotStore.Get()` — the hot path

```go
// internal/snapshot/store.go
func (s *Store) Get() (*Snapshot, bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    if s.current == nil {
        return nil, false
    }
    return s.current, true
}
```

`RLock` allows unlimited concurrent readers with zero contention. The write lock is only held for the instant it takes to swap a pointer — not for the duration of the database query.

### `SnapshotStore.refresh()` — the background writer

```go
func (s *Store) refresh(ctx context.Context) {
    stats, err := s.repo.ComputeStats(ctx)   // expensive — DB not locked
    // ...
    s.mu.Lock()                               // lock only for the pointer swap
    s.current = &Snapshot{Stats: stats, ...}
    s.mu.Unlock()
}
```

The expensive work happens **outside** the lock. Readers are never blocked waiting for a refresh to complete.

---

## When to Use This Pattern

| Use it when | Avoid it when |
|---|---|
| Results can be seconds/minutes stale | Data must be real-time (stock prices, seat availability) |
| Computation is expensive (>10 ms) | Computation is cheap (<1 ms) |
| Many readers, infrequent writes | Each user needs personalised results |
| Traffic spikes would overwhelm the DB | Data changes faster than the refresh interval |

---

## Extensions

- **Manual invalidation** — add a `POST /api/stats/refresh` endpoint that triggers `store.refresh()` immediately when source data changes.
- **Versioned snapshots** — use the `version` field in responses to detect stale browser caches.
- **Multi-key snapshots** — parameterise the store by tenant ID or region to serve per-customer snapshots.
- **Persistence** — on restart, pre-seed the in-memory store from Redis or a DB cache to avoid a cold-start delay.
