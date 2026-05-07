#!/usr/bin/env bash
# D6 smoke — exercise the full reservation-expiry path end-to-end:
# book → never confirm → wait reservation_window → D6 expires →
# saga compensates → DB + Redis inventory restored.
#
# Plan v4 §Smoke (round-3 F1/F2 fixes baked in):
#  - Override BOOKING_RESERVATION_WINDOW to 20s so the full cycle
#    finishes in ~1m. docker-compose.yml's app service forwards this
#    env via ${BOOKING_RESERVATION_WINDOW:-15m}, so a host-side
#    `export` + `--force-recreate` propagates the override into the
#    container.
#  - Polling-with-timeout instead of fixed sleep — sweep cadence
#    (30s default) + saga budget (~10s) + reservation window stack
#    up to ~62s; pad 50% for safety = 90s.
#  - Inventory conservation check uses `event_ticket_types.available_tickets`
#    (D4.1 SoT), NOT the frozen `events.available_tickets` (round-2 F2).
#  - `order_status_history` audit verifies the awaiting_payment→expired
#    edge actually fired, even if API polling skipped the brief
#    `expired` window between D6 and saga compensation.

set -euo pipefail

API_BASE="${API_BASE:-http://localhost:8080/api/v1}"
PG_CONN="${PG_CONN:-postgres://booking:smoketest_pg_local@localhost:5433/booking?sslmode=disable}"
REDIS_HOST="${REDIS_HOST:-localhost}"
REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_PASS="${REDIS_PASS:-smoketest_redis_local}"

# 20s reservation = total cycle ~62s (window + grace + sweep tick + saga).
RESERVATION_WINDOW="20s"

log() { printf '[d6_smoke] %s\n' "$*" >&2; }

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || { log "missing cmd: $1"; exit 1; }
}
require_cmd curl
require_cmd jq
require_cmd psql
require_cmd redis-cli
require_cmd docker

# 1. Override + restart relevant services so the new reservation
#    window propagates. `--force-recreate` is the explicit knob (a
#    plain `up -d` reuses the existing container with the OLD env).
log "exporting BOOKING_RESERVATION_WINDOW=${RESERVATION_WINDOW} and recreating app + worker + expiry_sweeper"
export BOOKING_RESERVATION_WINDOW="${RESERVATION_WINDOW}"
docker compose up -d --force-recreate app worker expiry_sweeper >/dev/null

# Wait for app /livez before issuing API calls.
log "waiting for app /livez (30s budget)"
TIMEOUT=30
while [ $TIMEOUT -gt 0 ]; do
    if curl -sf "${API_BASE%/*/*}/livez" >/dev/null 2>&1; then
        break
    fi
    sleep 1
    TIMEOUT=$((TIMEOUT - 1))
done
[ $TIMEOUT -gt 0 ] || { log "app never became live"; exit 1; }

# 2. Create event + capture ticket_type_id.
log "creating event"
EVENT_RESP=$(curl -sf -X POST "${API_BASE}/events" \
    -H 'Content-Type: application/json' \
    -d '{"name":"D6 smoke event","total_tickets":10,"price_cents":2000,"currency":"usd"}')
EVENT_ID=$(echo "$EVENT_RESP" | jq -r '.id')
TICKET_TYPE_ID=$(echo "$EVENT_RESP" | jq -r '.ticket_types[0].id')
log "event=${EVENT_ID} ticket_type=${TICKET_TYPE_ID}"

# Baseline inventory — the values D6 + saga must restore.
BASELINE_DB=$(psql "$PG_CONN" -tAc "SELECT available_tickets FROM event_ticket_types WHERE id='${TICKET_TYPE_ID}'")
BASELINE_REDIS=$(redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" -a "$REDIS_PASS" --no-auth-warning GET "ticket_type_qty:${TICKET_TYPE_ID}")
log "baseline DB=${BASELINE_DB} Redis=${BASELINE_REDIS}"

# 3. Book a ticket. Customer abandons after this — never calls /pay.
log "booking 1 ticket (abandoned — never paid)"
BOOK_RESP=$(curl -sf -X POST "${API_BASE}/book" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: d6-smoke-$(date +%s)" \
    -d "{\"user_id\":99,\"ticket_type_id\":\"${TICKET_TYPE_ID}\",\"quantity\":1}")
ORDER_ID=$(echo "$BOOK_RESP" | jq -r '.order_id')
log "order=${ORDER_ID}"

# 4. Poll until status=awaiting_payment (worker has persisted the row).
log "waiting for status=awaiting_payment"
TIMEOUT=15
while [ $TIMEOUT -gt 0 ]; do
    STATUS=$(curl -sf "${API_BASE}/orders/${ORDER_ID}" 2>/dev/null | jq -r '.status' || echo "")
    [ "$STATUS" = "awaiting_payment" ] && break
    sleep 1
    TIMEOUT=$((TIMEOUT - 1))
done
[ $TIMEOUT -gt 0 ] || { log "order never reached awaiting_payment (last status=${STATUS:-unknown})"; exit 1; }
log "order is awaiting_payment"

# 5. Poll-with-timeout for terminal state ∈ {expired, compensated}.
#    Budget = reservation_window (20s) + grace (2s) + sweep_tick (30s) + saga_budget (10s) = 62s.
#    Pad to 90s.
log "waiting up to 90s for D6 to expire (status ∈ {expired, compensated})"
TIMEOUT=90
while [ $TIMEOUT -gt 0 ]; do
    STATUS=$(curl -sf "${API_BASE}/orders/${ORDER_ID}" 2>/dev/null | jq -r '.status' || echo "")
    case "$STATUS" in
        expired|compensated) break ;;
    esac
    sleep 1
    TIMEOUT=$((TIMEOUT - 1))
done
[ $TIMEOUT -gt 0 ] || { log "FAIL: order never expired (final status=${STATUS:-unknown})"; exit 1; }
log "order reached terminal state: ${STATUS}"

# 6. Continue polling until status=compensated (final terminal).
log "waiting up to 30s for saga compensation to finish"
TIMEOUT=30
while [ $TIMEOUT -gt 0 ]; do
    STATUS=$(curl -sf "${API_BASE}/orders/${ORDER_ID}" 2>/dev/null | jq -r '.status' || echo "")
    [ "$STATUS" = "compensated" ] && break
    sleep 1
    TIMEOUT=$((TIMEOUT - 1))
done
[ $TIMEOUT -gt 0 ] || { log "FAIL: order never reached compensated (final status=${STATUS:-unknown})"; exit 1; }
log "order=compensated"

# 7. order_status_history audit edge — proves D6 ran the
#    awaiting_payment→expired transition even though the API view may
#    have skipped past `expired` between sweep and saga.
HISTORY_COUNT=$(psql "$PG_CONN" -tAc "
    SELECT count(*) FROM order_status_history
     WHERE order_id='${ORDER_ID}'
       AND from_status='awaiting_payment'
       AND to_status='expired'
")
[ "$HISTORY_COUNT" = "1" ] || { log "FAIL: expected exactly 1 awaiting_payment→expired history row, got ${HISTORY_COUNT}"; exit 1; }
log "history audit: 1 awaiting_payment→expired row ✓"

# 8. Inventory conservation — D4.1 SoT (round-2 F2).
FINAL_DB=$(psql "$PG_CONN" -tAc "SELECT available_tickets FROM event_ticket_types WHERE id='${TICKET_TYPE_ID}'")
FINAL_REDIS=$(redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" -a "$REDIS_PASS" --no-auth-warning GET "ticket_type_qty:${TICKET_TYPE_ID}")
log "final DB=${FINAL_DB} Redis=${FINAL_REDIS}"

[ "$FINAL_DB" = "$BASELINE_DB" ] || { log "FAIL: DB inventory drift (event_ticket_types.available_tickets baseline=${BASELINE_DB} final=${FINAL_DB})"; exit 1; }
[ "$FINAL_REDIS" = "$BASELINE_REDIS" ] || { log "FAIL: Redis inventory drift (ticket_type_qty baseline=${BASELINE_REDIS} final=${FINAL_REDIS})"; exit 1; }

log "PASS: D6 + saga restored DB + Redis inventory to baseline (${BASELINE_DB})"
