#!/usr/bin/env bash
# D10-minimal — terminal walkthrough of the full Pattern A flow.
#
# Three phases (~3 minutes total when recorded with asciinema):
#   Phase 1 — happy path:     book → pay → confirm-succeeded → paid
#   Phase 2 — failure path:   book → pay → confirm-failed → compensated
#   Phase 3 — abandon path:   book → don't pay → expired → compensated
#
# Recording recipe:
#   1. brew install asciinema    # macOS, one-time
#   2. make demo-up              # starts stack with 20s reservation + 5s sweep
#   3. asciinema rec docs/demo/walkthrough.cast --command='scripts/d10_demo_walkthrough.sh'
#
# Replay:
#   asciinema play docs/demo/walkthrough.cast
#
# Default API_ORIGIN points at nginx on host port 80 (the only host-published
# surface in docker-compose.yml). The `app` service publishes pprof:6060 only,
# NOT 8080, so a walkthrough targeting :8080 from the host would fail. nginx
# forwards /api/* + /test/* + /livez verbatim to the upstream Go app, so the
# walkthrough sees identical behaviour. Override API_ORIGIN=http://localhost:8080
# only when running the Go binary directly on the host (`make run-server`).

set -euo pipefail

API_ORIGIN="${API_ORIGIN:-http://localhost}"
V1="${API_ORIGIN}/api/v1"
TEST="${API_ORIGIN}/test"
PG_CONN="${PG_CONN:-postgres://booking:smoketest_pg_local@localhost:5433/booking?sslmode=disable}"

# Recording-friendly pacing helpers. PAUSE_MS controls the dramatic pauses
# between curl calls so the recording reads naturally; override to 0 for CI.
PAUSE_MS="${PAUSE_MS:-1500}"

pause() {
    if [[ "$PAUSE_MS" -gt 0 ]]; then
        sleep "$(awk -v ms="$PAUSE_MS" 'BEGIN{print ms/1000}')"
    fi
}

step() {
    printf '\n\033[1;36m▶ %s\033[0m\n' "$*"
    pause
}

note() {
    printf '\033[90m  # %s\033[0m\n' "$*"
}

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || { echo "missing: $1"; exit 1; }
}
require_cmd curl
require_cmd jq
require_cmd psql

# Reusable helpers
create_event() {
    local name="$1"
    curl -sS -X POST "${V1}/events" \
        -H 'Content-Type: application/json' \
        -d "{\"name\":\"${name}\",\"total_tickets\":3,\"price_cents\":2000,\"currency\":\"usd\"}"
}

book() {
    local user_id="$1" ticket_type_id="$2"
    curl -sS -X POST "${V1}/book" \
        -H 'Content-Type: application/json' \
        -d "{\"user_id\":${user_id},\"ticket_type_id\":\"${ticket_type_id}\",\"quantity\":1}"
}

get_order() {
    curl -sS "${V1}/orders/$1"
}

pay() {
    curl -sS -X POST "${V1}/orders/$1/pay" -H 'Content-Type: application/json'
}

confirm() {
    local order_id="$1" outcome="$2"
    curl -sS -X POST "${TEST}/payment/confirm/${order_id}?outcome=${outcome}" \
        -H 'Content-Type: application/json' -o /dev/null -w '%{http_code}\n'
}

poll_until_terminal() {
    local order_id="$1" expected="$2" deadline=$(( $(date +%s) + ${3:-60} ))
    while (( $(date +%s) < deadline )); do
        local status
        status=$(get_order "$order_id" | jq -r '.status // empty')
        if [[ "$status" == "$expected" ]]; then
            echo "$status"
            return 0
        fi
        # Progress dots so the recording shows activity during polling.
        printf '.'
        sleep 1
    done
    echo ""
    return 1
}

clear
cat <<'EOF'
╔══════════════════════════════════════════════════════════════╗
║  Pattern A Walkthrough — book → pay → terminal              ║
║                                                              ║
║  Three paths: succeeded · payment_failed · expired           ║
║  Stack assumed up via `make demo-up`                         ║
║  (BOOKING_RESERVATION_WINDOW=20s, EXPIRY_SWEEP_INTERVAL=5s)  ║
╚══════════════════════════════════════════════════════════════╝
EOF
pause

step "Create an event with 3 tickets, $20 each"
EVENT=$(create_event "D10 Walkthrough $(date +%H%M%S)")
TT_ID=$(echo "$EVENT" | jq -r '.ticket_types[0].id')
echo "$EVENT" | jq '{id, name, ticket_types: [.ticket_types[] | {id, available_tickets, price_cents, currency}]}'

# ──────────────────────────────────────────────────────────────
step "Phase 1 — Happy path: book → pay → confirm-succeeded → paid"
# ──────────────────────────────────────────────────────────────

note "Step 1.1 — book (returns 202 with reserved_until + links.pay)"
HAPPY_RESP=$(book 1001 "$TT_ID")
HAPPY_ID=$(echo "$HAPPY_RESP" | jq -r '.order_id')
echo "$HAPPY_RESP" | jq '{order_id, status, reserved_until, expires_in_seconds, links}'
pause

note "Step 1.2 — poll order; worker async-persists to awaiting_payment"
sleep 1
get_order "$HAPPY_ID" | jq '{order_id, status, amount_cents, currency}'
pause

note "Step 1.3 — POST /pay creates a Stripe-shape PaymentIntent"
pay "$HAPPY_ID" | jq
pause

note "Step 1.4 — POST /test/payment/confirm fires a signed webhook (test endpoint)"
confirm "$HAPPY_ID" succeeded
pause

note "Step 1.5 — poll until paid"
HAPPY_FINAL=$(poll_until_terminal "$HAPPY_ID" paid 30 || echo "TIMEOUT")
echo "  status=$HAPPY_FINAL"
get_order "$HAPPY_ID" | jq '{order_id, status, amount_cents}'
pause

# ──────────────────────────────────────────────────────────────
step "Phase 2 — Payment failed: book → pay → confirm-failed → compensated"
# ──────────────────────────────────────────────────────────────

note "Step 2.1 — book"
FAIL_RESP=$(book 1002 "$TT_ID")
FAIL_ID=$(echo "$FAIL_RESP" | jq -r '.order_id')
echo "  order_id=$FAIL_ID"
sleep 1
pause

note "Step 2.2 — pay"
pay "$FAIL_ID" >/dev/null
echo "  PaymentIntent created"
pause

note "Step 2.3 — confirm-FAILED webhook → MarkPaymentFailed → emit order.failed"
confirm "$FAIL_ID" failed
pause

note "Step 2.4 — poll until compensated (saga reverted Redis inventory)"
FAIL_FINAL=$(poll_until_terminal "$FAIL_ID" compensated 30 || echo "TIMEOUT")
echo "  status=$FAIL_FINAL"
pause

# ──────────────────────────────────────────────────────────────
step "Phase 3 — Abandon: book → never pay → D6 expires → compensated"
# ──────────────────────────────────────────────────────────────

note "Step 3.1 — book; deliberately don't call /pay"
ABANDON_RESP=$(book 1003 "$TT_ID")
ABANDON_ID=$(echo "$ABANDON_RESP" | jq -r '.order_id')
RESERVED_UNTIL=$(echo "$ABANDON_RESP" | jq -r '.reserved_until')
echo "  order_id=$ABANDON_ID"
echo "  reserved_until=$RESERVED_UNTIL  (≈20s from now)"
pause

note "Step 3.2 — wait for D6 expiry sweeper (5s interval) + saga compensation"
echo "  expecting: ~25s for window + sweep tick + saga round-trip"
ABANDON_FINAL=$(poll_until_terminal "$ABANDON_ID" compensated 60 || echo "TIMEOUT")
echo ""
echo "  status=$ABANDON_FINAL"
pause

note "Step 3.3 — DB audit trail confirms the saga path actually fired"
psql "$PG_CONN" -c \
    "SELECT from_status, to_status, transitioned_at FROM order_status_history WHERE order_id = '$ABANDON_ID' ORDER BY transitioned_at;" 2>/dev/null \
    || echo "(psql skipped — set PG_CONN to enable audit query)"
pause

# ──────────────────────────────────────────────────────────────
step "Final inventory check — all 3 tickets accounted for"
# ──────────────────────────────────────────────────────────────

psql "$PG_CONN" -c \
    "SELECT id, available_tickets FROM event_ticket_types WHERE id = '$TT_ID';" 2>/dev/null \
    || echo "(psql skipped)"

cat <<'EOF'

╔══════════════════════════════════════════════════════════════╗
║  Done.                                                       ║
║                                                              ║
║  - Phase 1 (paid): forward recovery, no compensation needed  ║
║  - Phase 2 (compensated): backward recovery via D5 webhook   ║
║  - Phase 3 (compensated): backward recovery via D6 sweeper   ║
║                                                              ║
║  See docs/blog/2026-05-saga-pure-forward-recovery.zh-TW.md   ║
║  for the architectural rationale.                            ║
╚══════════════════════════════════════════════════════════════╝
EOF
