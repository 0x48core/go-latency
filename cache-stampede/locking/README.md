# Cache Stampede Prevention with Locking

A Go package that implements three layered strategies for protecting a backend (database, slow API, etc.) from a **cache stampede** — the thundering herd of concurrent requests that hits the origin when a hot cache key expires.

## What is a Cache Stampede?

When a popular cached value expires, **N concurrent requests** all miss the cache simultaneously and race to recompute it. The origin gets hammered N times for the same value.

```
Cache expires
      │
      ▼
1000 requests miss cache at once
      │
      ▼
1000 requests hit the database
      │
      ▼
Database overloaded → cascading failure
```

This package provides three composable layers to prevent that:

| Strategy | File | What it does |
|---|---|---|
| Hard Lock | `hardlock.go` | Only one request recomputes; others wait |
| Stale-While-Revalidate (SWR) | `stale_while_revalidate.go` | Serve stale data while one request refreshes in the background |
| Circuit Breaker | `circuit_breaker.go` | Stop hitting a failing origin entirely; serve stale or fail fast |

## Architecture

```
       Request
          │
          ▼
   ┌─────────────┐
   │ Cache hit?  │──── yes ──► Return value
   └─────────────┘
          │ no
          ▼
   ┌──────────────────┐
   │ Circuit breaker  │──── open ──► Serve stale or ErrCircuitOpen
   │     allow?       │
   └──────────────────┘
          │ closed/half-open
          ▼
   ┌──────────────────┐
   │ Stale value      │──── yes ──► Return stale + refresh in background
   │     exists?      │
   └──────────────────┘
          │ no
          ▼
   ┌──────────────────┐
   │ Acquire lock     │──── no ──► Wait briefly for fresh value
   │     (SetNX)      │
   └──────────────────┘
          │ yes
          ▼
   Recompute, write fresh + stale, release lock
```

## Components

### 1. Hard Lock (`LockedCache`)

The simplest protection. The first request to miss the cache acquires a Redis lock with `SETNX`; all other concurrent requests **block** with exponential backoff until the value is fresh, then read it from cache.

Key details:
- Uses a **UUID lock token** so only the owner can release the lock
- Release is done via a Lua script for atomic "compare-and-delete"
- Backoff starts at 50ms, doubles to a 500ms cap, gives up after 20 retries

```go
cache := &LockedCache{rdb: rdb, lockTTL: 10 * time.Second}
val, err := cache.GetWithLock(ctx, "user:42", 5*time.Minute, func() (string, error) {
    return queryDatabase(42)
})
```

**Trade-off:** Simple and correct, but waiting requests still pay the recompute latency.

---

### 2. Stale-While-Revalidate (`SWRCache`)

Keeps a **second copy** of the value at `stale:<key>` with a much longer TTL (`ttl × staleFactor`). When the primary key expires:

- The lock owner refreshes asynchronously in the background
- Every other caller **immediately gets the stale value** — no waiting

```go
cache := &SWRCache{rdb: rdb, lockTTL: 10 * time.Second, staleFactor: 10}
val, err := cache.Get(ctx, "user:42", 5*time.Minute, func() (string, error) {
    return queryDatabase(42)
})
```

**Trade-off:** Requests never wait, but may receive slightly stale data (up to `ttl × staleFactor` old) during refresh.

**Cold-start path:** If no stale value exists yet, the lock owner computes synchronously while others wait via `waitForFresh`.

---

### 3. Circuit Breaker (`CircuitBreaker`)

A standard three-state breaker (Closed → Open → Half-Open) tracking compute failures:

| State | Behavior |
|---|---|
| Closed | Normal — all requests proceed |
| Open | Rejects requests instantly; returns stale data or `ErrCircuitOpen` |
| Half-Open | Lets one probe through; success → Closed, failure → Open |

State transitions are lock-free (`atomic.Int32`).

---

### 4. ResilientCache — All Three Combined

`NewResilientCache` wires SWR + Circuit Breaker together:

```go
cache := locking.NewResilientCache(rdb)

val, err := cache.Get(ctx, "user:42", 5*time.Minute, func() (string, error) {
    return queryDatabase(42)
})

if errors.Is(err, locking.ErrCircuitOpen) {
    // Origin is down and no stale value available
}
```

Defaults set in `main.go`:
- Lock TTL: **10 seconds**
- Stale factor: **10×** (stale lives 10× longer than primary)
- Breaker threshold: **5 consecutive failures**
- Breaker reset: **30 seconds**

## Errors

| Error | When |
|---|---|
| `ErrLockTimeout` | Waited too long for another worker to populate the cache |
| `ErrCircuitOpen` | Breaker is open and no stale value is available |

## Dependencies

- `github.com/redis/go-redis/v9` — Redis client
- `github.com/google/uuid` — lock-token generation

## When to Use Which

| Scenario | Use |
|---|---|
| Low-traffic cache, simple needs | `LockedCache` |
| High-traffic, latency-sensitive reads | `SWRCache` |
| Origin can fail / become slow under load | `ResilientCache` (SWR + breaker) |

## Design Notes

- **Lock keys** are namespaced as `lock:<key>`; **stale keys** as `stale:<key>`
- The hard lock releases via Lua to prevent a slow worker from deleting a lock that has already been re-acquired by someone else
- SWR's background refresh uses `context.Background()` so it isn't cancelled if the original request's context expires
- The circuit breaker uses atomic operations only — no mutex contention on the hot path
