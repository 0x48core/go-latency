.PHONY: up down run tidy test

## Start Redis
up:
	docker compose up -d

## Stop Redis
down:
	docker compose down

## Install dependencies
tidy:
	go mod tidy

## Run the server (Redis must be up)
run:
	go run ./cmd/server

## Run with Redis intentionally stopped (tests graceful degradation)
run-degraded:
	REDIS_ADDR=localhost:9999 go run ./cmd/server

## Run tests
test:
	go test ./... -v

# ── Demo curl commands ────────────────────────────────────────────────────────
# Fetch weather (live on first call, cached on subsequent):
#   curl http://localhost:8080/weather/hanoi
#   curl http://localhost:8080/weather/london
#
# Get feature flags (returns preloaded defaults if Redis is down):
#   curl http://localhost:8080/flags
#   curl http://localhost:8080/flags/defaults
#
# Write a custom flag set:
#   curl -X PUT http://localhost:8080/flags/production \
#        -H "Content-Type: application/json" \
#        -d '{"dark_mode":true,"beta_search":true,"max_results_limit":50}'
#
# Read it back (will serve from cache):
#   curl http://localhost:8080/flags/production
#
# Simulate Redis failure: docker compose stop redis
# Then re-run the flag/weather requests — stale cache & preload kick in.