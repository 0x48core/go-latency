# Go Latency Patterns

A collection of practical Go examples demonstrating patterns and techniques for reducing latency in backend services. Each pattern lives in its own directory with a fully working demo, Docker Compose stack, and monitoring via Prometheus + Grafana.

## Patterns

| Pattern | Description | Directory |
|---------|-------------|-----------|
| **Singleflight** | Deduplicate concurrent requests to prevent cache stampedes and protect the database from redundant load | [`singleflight/`](singleflight/) |
| **Cache Layering** | Two-level cache (TinyLFU + Redis) with circuit breaker, graceful degradation, and stale-while-error fallback | [`cache-layering/`](cache-layering/) |
| **Snapshot** | Pre-compute expensive aggregations in the background and serve results from memory to absorb traffic spikes | [`snapshot/`](snapshot/) |
| **Redis High Availability** | Redis Sentinel architecture in Kubernetes with automatic failover, persistent storage, and a Go client deployed via Helm | [`redis-high-availability/`](redis-high-availability/) |
