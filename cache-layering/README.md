# Weather / Feature Flag API

A mini-project demonstrating all four resilience layers from the article
[Building a Resilient Two-Level Cache in Go](https://medium.com/itnext/building-a-resilient-two-level-cache-in-go-with-circuit-breaker-graceful-degradation-and-d1d9986e4865).

```
┌─────────────────────────────────────────────────────┐
│                  Resilience Layers                  │
├────────┬────────────┬──────────────┬────────────────┤
│ Layer  │ Mechanism  │ TTL (default)│ Protects from  │
├────────┼────────────┼──────────────┼────────────────┤
│ L1     │ TinyLFU    │ 1 min        │ Redis latency  │
│ L2     │ Redis      │ 5 min        │ DB hammering   │
│ CB     │ gobreaker  │ —            │ Cascading fail │
│ Stale  │ TinyLFU    │ 1 hour       │ Redis outage   │
│ Preload│ DefaultFlags│ ∞           │ Cold start     │
└────────┴────────────┴──────────────┴────────────────┘
```

## Quick Start

```bash
# 1. Start Redis
make up

# 2. Install deps
make tidy

# 3. Run the server
make run
```

## Endpoints

| Method | Path             | Description                               |
|--------|------------------|-------------------------------------------|
| GET    | /weather/{city}  | Fetch weather (cached, with fallback)     |
| GET    | /flags/{name}    | Get feature flags (preloaded defaults)    |
| GET    | /flags           | Alias for /flags/defaults                 |
| PUT    | /flags/{name}    | Store a custom feature flag set           |
| GET    | /health          | Health check                              |

## Testing the Resilience Layers

### 1. Normal operation
```bash
curl http://localhost:8080/weather/hanoi
# → source: "live"  (first call)

curl http://localhost:8080/weather/hanoi
# → source: "cache"  (subsequent calls)
```

### 2. Feature flags with safe defaults
```bash
# Write production flags
curl -X PUT http://localhost:8080/flags/production \
  -H "Content-Type: application/json" \
  -d '{"dark_mode":true,"beta_search":false,"max_results_limit":100}'

curl http://localhost:8080/flags/production
# → source: "cache"
```

### 3. Simulate Redis failure (graceful degradation)
```bash
docker compose stop redis

# Within L1 TTL (1 min): still served from L1
curl http://localhost:8080/weather/hanoi
# → source: "cache"

# After L1 expires: served from STALE cache
curl http://localhost:8080/weather/hanoi
# → source: "stale"

# Feature flags: served from PRELOADED defaults
curl http://localhost:8080/flags/unknown-flags
# → source: "preload"
```

### 4. Cold start (Redis never available)
```bash
make run-degraded
# Server starts up with warning, but /flags still returns defaults
curl http://localhost:8080/flags
# → source: "preload"   ← preloaded at construction time
```

## Environment Variables

| Variable         | Default       | Description                      |
|------------------|---------------|----------------------------------|
| REDIS_ADDR       | localhost:6379| Redis address                    |
| REDIS_PASSWORD   |               | Redis password                   |
| L1_TTL           | 1m            | In-memory cache TTL              |
| L2_TTL           | 5m            | Redis TTL                        |
| STALE_TTL        | 1h            | Stale cache TTL (0 = never)      |
| WEATHER_API_KEY  | demo          | Weather API key (unused w/ wttr) |
| PORT             | 8080          | HTTP port                        |

## Project Structure

```
.
├── cmd/server/main.go          # Entrypoint, wiring
├── config/config.go            # Env-based config
├── internal/
│   ├── cache/store.go          # go-cache wrapper (all 4 layers configured here)
│   ├── handler/handler.go      # HTTP handlers
│   └── weather/client.go       # External weather API client (wttr.in)
├── docker-compose.yml          # Redis
├── Makefile                    # Dev shortcuts
└── README.md
```

## Key Learning Points

- **`cache/store.go`** is the core — study how `WithRedisConn`, `WithLocalCacheTinyLFU`,
  `WithCBEnabled`, `WithGracefulDegradation`, and `WithPreload` are stacked.
- The `source` field in API responses tells you which layer served the data.
- Kill Redis with `docker compose stop redis` and watch the source field change:
  `live → cache → stale → preload` as each layer takes over.