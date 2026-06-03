#!/usr/bin/env bash
# Scenario D — Postgres connection saturation.
#
# Hypothesis: 100 parallel pg_sleep(120) connections consume close to
# max_connections=100 → app's connection pool retry-with-backoff
# handles the temporary shortage → booking endpoints return 5xx for
# the saturation window, then auto-recover after pg_sleep finishes.
#
# Tool: psql with pg_sleep. pg_sleep holds the connection (does NOT
# release on sleep) per PG docs — verified in 2026.
#
# See docs/runbooks/chaos.md § Scenario D.

set -euo pipefail

PG_CONTAINER="${PG_CONTAINER:-booking_postgres}"
PG_USER="${PG_USER:-postgres}"
PG_DB="${PG_DB:-booking}"
NUM_CONNECTIONS="${NUM_CONNECTIONS:-95}" # 95 not 100 — leave room for our own session
SLEEP_SECONDS="${SLEEP_SECONDS:-120}"

printf '\033[1mScenario D — Postgres connection saturation\033[0m\n'
printf 'Hypothesis: %d parallel pg_sleep(%ds) connections → app retries gracefully.\n' \
  "$NUM_CONNECTIONS" "$SLEEP_SECONDS"
printf 'Recovery: automatic after %ds (pg_sleep releases connection).\n\n' "$SLEEP_SECONDS"

read -rp "Type INJECT to proceed: " confirm
[[ "$confirm" == "INJECT" ]] || { echo "aborted"; exit 1; }

T0=$(date -u +%s)
echo "t=0: spawning $NUM_CONNECTIONS hold-connection sessions..."

PIDS=()
for ((i=1; i<=NUM_CONNECTIONS; i++)); do
  docker exec "$PG_CONTAINER" \
    psql -U "$PG_USER" -d "$PG_DB" -c "SELECT pg_sleep($SLEEP_SECONDS);" \
    > /dev/null 2>&1 &
  PIDS+=($!)
done
echo "Injected ${#PIDS[@]} connections. Watching for ${SLEEP_SECONDS}s..."

cleanup() {
  echo ""
  echo "Cleaning up: killing held psql sessions..."
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  echo "Done."
}
trap cleanup EXIT INT TERM

echo ""
echo "Watch:"
echo "  - POST /api/v1/book — expected 5xx during saturation, recovers automatically"
echo "  - db_pool_wait_seconds histogram — should spike"
echo "  - booking:availability:slo_budget_remaining — expected dip"

sleep "$SLEEP_SECONDS"

T1=$(date -u +%s)
echo ""
printf '\033[32mEnded: %ds saturation window complete.\033[0m\n' "$((T1-T0))"
echo "Document: docs/chaos-log/$(date -u +%Y-%m-%d)-pg-saturation.md"
