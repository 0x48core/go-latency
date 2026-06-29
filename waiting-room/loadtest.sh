#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
TOTAL_USERS="${TOTAL_USERS:-1000}"
CONCURRENCY="${CONCURRENCY:-50}"   # max parallel workers — prevents fork bombing
POLL_INTERVAL=2
POLL_TIMEOUT=300
SESSION_FILE="/tmp/wr_sessions.txt"
ADMITTED_FILE="/tmp/wr_admitted.txt"
POLL_TMP="/tmp/wr_poll_tmp.txt"

echo "=== Waiting Room Load Test ==="
echo "Target     : $BASE_URL"
echo "Users      : $TOTAL_USERS"
echo "Concurrency: $CONCURRENCY"
echo ""

# -------------------------------------------------------
# PHASE 1: Flood join — batched parallel joins
# -------------------------------------------------------
echo "[Phase 1] Joining queue with $TOTAL_USERS users (concurrency=$CONCURRENCY)..."

> "$SESSION_FILE"

PHASE1_START=$(date +%s)

seq 1 "$TOTAL_USERS" | xargs -P "$CONCURRENCY" -I{} bash -c "
    response=\$(curl -s -X POST '$BASE_URL/queue/join')
    sid=\$(echo \"\$response\" | jq -r '.session_id')
    pos=\$(echo \"\$response\" | jq -r '.position')
    if [[ \"\$sid\" != \"null\" && -n \"\$sid\" ]]; then
        echo \"\$sid \$pos\" >> '$SESSION_FILE'
    fi
"

PHASE1_ELAPSED=$(( $(date +%s) - PHASE1_START ))
JOINED=$(wc -l < "$SESSION_FILE" | tr -d ' ')
JOIN_RATE=$(( JOINED / ( PHASE1_ELAPSED + 1 ) ))

echo "  Joined : $JOINED / $TOTAL_USERS"
echo "  Time   : ${PHASE1_ELAPSED}s (~${JOIN_RATE} joins/sec)"
echo ""

# -------------------------------------------------------
# PHASE 2: Poll status in parallel batches
# -------------------------------------------------------
echo "[Phase 2] Polling for admission (timeout: ${POLL_TIMEOUT}s)..."

> "$ADMITTED_FILE"

START=$(date +%s)

while true; do
    NOW=$(date +%s)
    ELAPSED=$(( NOW - START ))

    if [[ $ELAPSED -ge $POLL_TIMEOUT ]]; then
        echo "  Timeout reached after ${ELAPSED}s."
        break
    fi

    REMAINING_COUNT=$(wc -l < "$SESSION_FILE" | tr -d ' ')
    if [[ $REMAINING_COUNT -eq 0 ]]; then
        echo "  All users admitted!"
        break
    fi

    > "$POLL_TMP"

    # Poll all remaining sessions in parallel
    while IFS=' ' read -r sid pos; do
        echo "$sid $pos"
    done < "$SESSION_FILE" | xargs -P "$CONCURRENCY" -I{} bash -c "
        sid=\$(echo '{}' | awk '{print \$1}')
        pos=\$(echo '{}' | awk '{print \$2}')
        status=\$(curl -s '$BASE_URL/queue/status?session_id='\$sid)
        admitted=\$(echo \"\$status\" | jq -r '.admitted')
        if [[ \"\$admitted\" == \"true\" ]]; then
            echo \"ADMITTED \$sid\" >> '$POLL_TMP'
        else
            echo \"WAITING \$sid \$pos\" >> '$POLL_TMP'
        fi
    "

    # split results
    grep "^ADMITTED" "$POLL_TMP" | awk '{print $2}' >> "$ADMITTED_FILE" || true
    grep "^WAITING"  "$POLL_TMP" | awk '{print $2, $3}' > "$SESSION_FILE" || true

    ADMITTED_COUNT=$(wc -l < "$ADMITTED_FILE" | tr -d ' ')
    STILL_WAITING=$(wc -l < "$SESSION_FILE" | tr -d ' ')
    echo "  [${ELAPSED}s] Admitted: $ADMITTED_COUNT / $JOINED | Still waiting: $STILL_WAITING"

    sleep "$POLL_INTERVAL"
done

echo ""

# -------------------------------------------------------
# PHASE 3: Admitted users hit the resource in parallel
# -------------------------------------------------------
echo "[Phase 3] Admitted users accessing /resource..."

SUCCESS_FILE="/tmp/wr_success.txt"
FAIL_FILE="/tmp/wr_fail.txt"
> "$SUCCESS_FILE"
> "$FAIL_FILE"

cat "$ADMITTED_FILE" | xargs -P "$CONCURRENCY" -I{} bash -c "
    code=\$(curl -s -o /dev/null -w '%{http_code}' '$BASE_URL/resource?session_id={}')
    if [[ \"\$code\" == \"200\" ]]; then
        echo 1 >> '$SUCCESS_FILE'
    else
        echo 1 >> '$FAIL_FILE'
    fi
"

SUCCESS=$(wc -l < "$SUCCESS_FILE" | tr -d ' ')
FAIL=$(wc -l < "$FAIL_FILE" | tr -d ' ')
echo "  Success: $SUCCESS | Failed: $FAIL"
echo ""

# -------------------------------------------------------
# PHASE 4: Verify one-time token enforcement
# -------------------------------------------------------
echo "[Phase 4] Verifying one-time token enforcement..."

FIRST_SID=$(head -1 "$ADMITTED_FILE")
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/resource?session_id=$FIRST_SID")

if [[ "$CODE" == "403" ]]; then
    echo "  PASS — reused token correctly rejected (403)"
else
    echo "  FAIL — expected 403, got $CODE"
fi

echo ""

# -------------------------------------------------------
# Summary
# -------------------------------------------------------
TOTAL_ELAPSED=$(( $(date +%s) - START ))
ADMITTED_FINAL=$(wc -l < "$ADMITTED_FILE" | tr -d ' ')
ADMIT_RATE=$(( ADMITTED_FINAL / ( TOTAL_ELAPSED + 1 ) ))

echo "=== Summary ==="
echo "  Total users   : $TOTAL_USERS"
echo "  Joined        : $JOINED"
echo "  Admitted      : $ADMITTED_FINAL"
echo "  Resource hits : $SUCCESS ok / $FAIL failed"
echo "  Total time    : ${TOTAL_ELAPSED}s (~${ADMIT_RATE} admits/sec)"
echo ""
echo "Check Grafana at http://localhost:3000 for metrics."
