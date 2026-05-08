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
# psql is OPTIONAL — only used in two informational DB-audit steps below
# (Phase 3 status_history query + final inventory check). Both already
# fall back to a "psql skipped" message if absent, so we don't gate the
# whole walkthrough on having the postgres client installed.

# Wait for the stack to pass /livez + /readyz before the first action.
# Without this, `docker compose up -d` returns before the app is healthy,
# and the first `POST /events` can hit nginx during its 502 window.
wait_for_ready() {
    local deadline=$(( $(date +%s) + 60 ))
    while (( $(date +%s) < deadline )); do
        if curl -sSf "${API_ORIGIN}/livez" >/dev/null 2>&1 \
            && curl -sSf "${API_ORIGIN}/readyz" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    echo "ERROR: stack at ${API_ORIGIN} didn't pass /livez + /readyz within 60s" >&2
    echo "Hint: run 'make demo-up' first; verify nginx is up + app is healthy." >&2
    return 1
}

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
            echo "$status"   # stdout = return value (captured by callers via $())
            return 0
        fi
        # Progress dots → stderr so they show in the recording even when
        # the call is wrapped in $(). stdout is reserved for the captured
        # return value.
        printf '.' >&2
        sleep 1
    done
    return 1
}

# Hard assertion wrapping poll_until_terminal — fails the recording
# loudly instead of swallowing the timeout into a "TIMEOUT" string and
# continuing to a misleading success banner. asciinema records the
# failure visibly so a broken stack / saga consumer / disabled test
# endpoint is obvious to anyone playing the cast back.
#
# stdout is reserved for the captured return value (the terminal status
# string). All FAIL banner + diagnostic output goes to stderr so it's
# visible in the recording even when the caller wraps the assertion in
# `$(...)` (which would otherwise capture-and-suppress it; combined
# with `set -e` the outer script would exit before the captured banner
# was ever printed).
assert_terminal() {
    local phase="$1" order_id="$2" expected="$3" timeout_s="$4"
    local result
    if ! result=$(poll_until_terminal "$order_id" "$expected" "$timeout_s"); then
        {
            echo ""
            printf '\033[1;31m❌ FAIL: %s — order %s did not reach %s within %ss\033[0m\n' \
                "$phase" "$order_id" "$expected" "$timeout_s"
            echo "    Likely causes:"
            echo "      - stack unhealthy (rerun 'make demo-up' for fresh /livez+/readyz check)"
            echo "      - ENABLE_TEST_ENDPOINTS=false / APP_ENV=production (test webhook 404)"
            echo "      - saga compensator down or backed up"
            echo "      - D6 expiry sweeper not running with EXPIRY_SWEEP_INTERVAL=5s"
        } >&2
        exit 1
    fi
    echo "$result"
}

# Hard assertion on confirm endpoint HTTP code — catches the case where
# /test/payment/confirm is disabled (production env or
# ENABLE_TEST_ENDPOINTS=false) BEFORE the script wastes 30s polling for
# a paid status that will never arrive.
#
# Same stderr discipline as assert_terminal: success message ("HTTP
# $code") to stdout for inline display; FAIL banner to stderr so a
# future caller wrapping this in `$()` still gets the visible failure.
assert_confirm() {
    local order_id="$1" outcome="$2"
    local code
    code=$(confirm "$order_id" "$outcome")
    case "$code" in
        200|202|204)
            echo "  HTTP $code"
            ;;
        *)
            {
                echo ""
                printf '\033[1;31m❌ FAIL: confirm endpoint returned %s (expected 2xx)\033[0m\n' "$code"
                echo "    Likely causes:"
                echo "      - APP_ENV=production (test endpoints rejected at startup)"
                echo "      - ENABLE_TEST_ENDPOINTS=false (default; demo-up should set true)"
            } >&2
            exit 1
            ;;
    esac
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

step "Wait for stack to be ready (livez + readyz)"
wait_for_ready
echo "  stack ready at ${API_ORIGIN}"
pause

step "Create an event with 3 tickets, \$20 each"
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
assert_confirm "$HAPPY_ID" succeeded
pause

note "Step 1.5 — poll until paid"
HAPPY_FINAL=$(assert_terminal "Phase 1 (happy)" "$HAPPY_ID" paid 30)
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
assert_confirm "$FAIL_ID" failed
pause

note "Step 2.4 — poll until compensated (saga reverted Redis inventory)"
FAIL_FINAL=$(assert_terminal "Phase 2 (payment-failed)" "$FAIL_ID" compensated 30)
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
ABANDON_FINAL=$(assert_terminal "Phase 3 (abandon)" "$ABANDON_ID" compensated 60)
echo ""
echo "  status=$ABANDON_FINAL"
pause

note "Step 3.3 — DB audit trail confirms the saga path actually fired (optional; needs psql)"
if command -v psql >/dev/null 2>&1; then
    psql "$PG_CONN" -c \
        "SELECT from_status, to_status, occurred_at FROM order_status_history WHERE order_id = '$ABANDON_ID' ORDER BY occurred_at;" 2>/dev/null \
        || echo "(psql query failed — verify PG_CONN points at the running db)"
else
    echo "(skipped — psql not installed; brew install postgresql to enable)"
fi
pause

# ──────────────────────────────────────────────────────────────
step "Final inventory check — all 3 tickets accounted for (optional; needs psql)"
# ──────────────────────────────────────────────────────────────

if command -v psql >/dev/null 2>&1; then
    psql "$PG_CONN" -c \
        "SELECT id, available_tickets FROM event_ticket_types WHERE id = '$TT_ID';" 2>/dev/null \
        || echo "(psql query failed — verify PG_CONN points at the running db)"
else
    echo "(skipped — psql not installed; brew install postgresql to enable)"
fi

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
