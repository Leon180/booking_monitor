#!/usr/bin/env bash
# Scenario C — Network latency injection (500ms) between app and Postgres.
#
# Hypothesis: 500ms artificial latency app→PG, connection pool reuse
# keeps per-request hit bounded → p99 latency on POST /api/v1/book
# stays under 1500ms. The booking-latency SLO (99% < 500ms) IS broken
# during the experiment; availability SLO is NOT broken.
#
# Requires: --cap-add NET_ADMIN on the app container (already in
# docker-compose.yml per PR 1).
#
# See docs/runbooks/chaos.md § Scenario C.

set -euo pipefail

CONTAINER="${CONTAINER:-booking_app}"
DELAY="${DELAY:-500ms}"
DURATION="${DURATION:-60}" # seconds

printf '\033[1mScenario C — Network latency injection\033[0m\n'
printf 'Hypothesis: %s delay app→PG, p99 booking latency stays < 1500ms.\n' "$DELAY"
printf 'Booking-latency SLO WILL break; availability SLO should hold.\n\n'

# Verify container has NET_ADMIN
if ! docker exec "$CONTAINER" tc qdisc show > /dev/null 2>&1; then
  echo "ABORT: $CONTAINER doesn't have NET_ADMIN capability."
  echo "Check docker-compose.yml has: cap_add: [NET_ADMIN]"
  exit 1
fi

read -rp "Type INJECT to proceed (auto-revert after ${DURATION}s): " confirm
[[ "$confirm" == "INJECT" ]] || { echo "aborted"; exit 1; }

T0=$(date -u +%s)
echo "t=0: adding tc netem delay $DELAY on $CONTAINER eth0"
docker exec "$CONTAINER" tc qdisc add dev eth0 root netem delay "$DELAY"

# Trap cleanup so Ctrl-C still removes the rule
cleanup() {
  echo ""
  echo "Removing tc netem rule..."
  docker exec "$CONTAINER" tc qdisc del dev eth0 root netem 2>/dev/null || true
  echo "Cleaned. Latency restored to baseline."
}
trap cleanup EXIT INT TERM

echo "Holding for ${DURATION}s. Watch Grafana:"
echo "  - p99 latency on POST /api/v1/book — expected < 1500ms"
echo "  - booking:book_latency:slo_budget_remaining — expected to drop"
echo "  - booking:availability:slo_budget_remaining — expected to stay > 0.5"
sleep "$DURATION"

T1=$(date -u +%s)
echo ""
printf '\033[32mEnded: %ds total latency injection window.\033[0m\n' "$((T1-T0))"
echo "Document: docs/chaos-log/$(date -u +%Y-%m-%d)-network-latency.md"
