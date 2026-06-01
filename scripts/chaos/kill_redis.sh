#!/usr/bin/env bash
# Scenario B — Kill booking_redis container.
#
# Hypothesis:
#   1. /readyz returns 503 (correctly reflects dependency loss)
#   2. POST /api/v1/book returns 5xx (fails CLOSED — no silent corruption)
#   3. After Redis restart, /readyz recovers to 200
#
# Critical anti-pattern to verify it DOESN'T do:
#   - App continues accepting bookings while Redis is unreachable
#   - Result: DB has the order but Redis inventory NOT deducted
#   - Silent corruption — the worst kind of failure
#
# See docs/runbooks/chaos.md § Scenario B.

set -euo pipefail

CONTAINER="${CONTAINER:-booking_redis}"
# Probe via nginx (port 80) — see kill_app.sh comment. Same reason.
READY_URL="${READY_URL:-http://localhost/readyz}"
BOOK_URL="${BOOK_URL:-http://localhost/api/v1/book}"

printf '\033[1mScenario B — Kill Redis\033[0m\n'
printf 'Hypothesis:\n'
printf '  1. /readyz returns 503 within seconds of kill\n'
printf '  2. POST /api/v1/book returns 5xx (fails closed)\n'
printf '  3. After Redis restart, /readyz returns 200\n\n'
printf 'CRITICAL anti-pattern to verify it does NOT do:\n'
printf '  - Accept bookings while Redis is down (would be silent corruption)\n\n'

if ! curl -fsS --max-time 3 "$READY_URL" > /dev/null; then
  echo "ABORT: pre-flight $READY_URL is not 200 — system isn't in steady state."
  exit 1
fi

read -rp "Type INJECT to proceed: " confirm
[[ "$confirm" == "INJECT" ]] || { echo "aborted"; exit 1; }

T0=$(date -u +%s)
# Unlike kill_app.sh, this script EXPECTS a downtime window to verify
# fail-closed behavior. So we use `docker kill` (no auto-restart from
# `unless-stopped` policy) + manually `docker start` later. Auto-restart
# would close the window before we can observe the system's behavior
# while Redis is unreachable.
echo "t=0: killing $CONTAINER"
docker kill "$CONTAINER" > /dev/null

echo ""
echo "Verifying hypothesis 1: /readyz should return 503..."
sleep 2
READY_STATUS=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$READY_URL" || echo "000")
if [[ "$READY_STATUS" == "503" ]]; then
  printf '\033[32m  PASS: /readyz = 503\033[0m\n'
else
  printf '\033[31m  FAIL: /readyz = %s (expected 503)\033[0m\n' "$READY_STATUS"
fi

echo ""
echo "Verifying hypothesis 2: POST /api/v1/book should fail closed (NOT accept)..."
BOOK_STATUS=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 \
  -X POST -H 'Content-Type: application/json' \
  -d '{"user_id":1,"ticket_type_id":"019e7e65-67bf-7751-ac49-a32a0825fd63","quantity":1}' \
  "$BOOK_URL" || echo "000")
if [[ "$BOOK_STATUS" =~ ^5[0-9]{2}$ ]]; then
  printf '\033[32m  PASS: POST /book = %s (fails closed)\033[0m\n' "$BOOK_STATUS"
elif [[ "$BOOK_STATUS" == "202" ]]; then
  printf '\033[31m  FAIL: POST /book = 202 (silent corruption! DB updated, Redis is dead)\033[0m\n'
  printf '\033[31m  This is a serious bug — investigate the booking hot path retry-with-backoff\033[0m\n'
else
  printf '\033[33m  INCONCLUSIVE: POST /book = %s (expected 5xx or 503)\033[0m\n' "$BOOK_STATUS"
fi

echo ""
echo "Restoring Redis: docker start $CONTAINER"
docker start "$CONTAINER" > /dev/null

echo "Waiting for /readyz to return 200..."
RECOVERED_AT=""
for ((i=1; i<=60; i++)); do
  if curl -fsS --max-time 2 "$READY_URL" > /dev/null 2>&1; then
    RECOVERED_AT=$(date -u +%s)
    break
  fi
  sleep 1
done

if [[ -z "$RECOVERED_AT" ]]; then
  echo "FAIL: /readyz never recovered after Redis restart"
  exit 2
fi
ELAPSED=$((RECOVERED_AT - T0))
echo ""
printf '\033[32mPASS: /readyz back to 200, total experiment %ds\033[0m\n' "$ELAPSED"
echo "Document: docs/chaos-log/$(date -u +%Y-%m-%d)-kill-redis.md"
