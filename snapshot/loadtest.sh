#!/usr/bin/env bash
set -euo pipefail

BASE_URL=${BASE_URL:-"http://localhost:8080"}
DURATION=${DURATION:-"30s"}
RPS=${RPS:-"50"}
CONCURRENCY=${CONCURRENCY:-"10"}

echo "========================================"
echo "  Snapshot Pattern — Latency Comparison "
echo "========================================"
echo "Base URL  : $BASE_URL"
echo "Duration  : $DURATION"
echo "RPS target: $RPS"
echo ""

wait_ready() {
    echo "Waiting for server to be ready..."
    until curl -sf "$BASE_URL/api/health" > /dev/null; do sleep 1; done
    echo "Server ready."
    echo ""
}

run_hey() {
    local label=$1
    local url=$2
    echo "--- $label ---"
    echo "URL: $url"
    hey -z "$DURATION" -q "$RPS" -c "$CONCURRENCY" "$url"
    echo ""
}

run_ab() {
    local label=$1
    local url=$2
    echo "--- $label ---"
    echo "URL: $url"
    ab -t 30 -c "$CONCURRENCY" "$url" 2>&1 \
        | grep -E "Requests per second|Time per request|Failed requests|Percentage"
    echo ""
}

run_curl() {
    local label=$1
    local url=$2
    echo "--- $label (5 sequential requests) ---"
    echo "URL: $url"
    for i in $(seq 1 5); do
        time curl -sf "$url" > /dev/null
    done
    echo ""
}

wait_ready

if command -v hey &> /dev/null; then
    run_hey "SNAPSHOT endpoint  (pre-computed, ~<1ms)" "$BASE_URL/api/stats"
    run_hey "LIVE endpoint      (on-demand SQL, ~100–500ms)" "$BASE_URL/api/stats/live"
elif command -v ab &> /dev/null; then
    run_ab "SNAPSHOT endpoint  (pre-computed)" "$BASE_URL/api/stats"
    run_ab "LIVE endpoint      (on-demand SQL)" "$BASE_URL/api/stats/live"
else
    echo "Install 'hey' (go install github.com/rakyll/hey@latest) for a proper load test."
    echo "Falling back to sequential curl..."
    echo ""
    run_curl "SNAPSHOT endpoint" "$BASE_URL/api/stats"
    run_curl "LIVE endpoint"     "$BASE_URL/api/stats/live"
fi

echo "========================================"
echo "  Metrics: http://localhost:9090"
echo "  Grafana:  http://localhost:3000"
echo "========================================"
