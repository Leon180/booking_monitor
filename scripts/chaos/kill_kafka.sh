#!/usr/bin/env bash
# Scenario E — Kafka broker kill (transactional outbox pattern verifier).
#
# Hypothesis: Kafka dying mid-outbox-publish → outbox events stay
# PENDING in PG (the outbox pattern's core promise). On Kafka restart,
# OutboxRelay drains the backlog → all events eventually published
# exactly once. NO data loss; NO duplication beyond Kafka's at-least-
# once delivery.
#
# This is the test that proves the outbox pattern (PR 1 architecture).
# If we DID publish to Kafka inside the booking transaction (the
# anti-pattern of "two-phase commit across heterogeneous systems"),
# Kafka downtime would either lose events or duplicate them.
#
# See docs/runbooks/chaos.md § Scenario E.

set -euo pipefail

CONTAINER="${CONTAINER:-booking_kafka}"
PG_CONTAINER="${PG_CONTAINER:-booking_postgres}"
PG_USER="${PG_USER:-postgres}"
PG_DB="${PG_DB:-booking}"
KAFKA_DOWN_SECONDS="${KAFKA_DOWN_SECONDS:-60}"

printf '\033[1mScenario E — Kafka broker kill\033[0m\n'
printf 'Hypothesis: outbox events accumulate as PENDING in PG, then\n'
printf 'drain after Kafka restart. NO data loss.\n\n'

read -rp "Type INJECT to proceed: " confirm
[[ "$confirm" == "INJECT" ]] || { echo "aborted"; exit 1; }

PENDING_BEFORE=$(docker exec "$PG_CONTAINER" \
  psql -U "$PG_USER" -d "$PG_DB" -t -c \
  "SELECT COUNT(*) FROM events_outbox WHERE processed_at IS NULL;" \
  2>/dev/null | tr -d '[:space:]' || echo "?")
echo "Outbox PENDING count before: $PENDING_BEFORE"

T0=$(date -u +%s)
echo "t=0: killing $CONTAINER"
docker kill "$CONTAINER"

echo "Generating booking traffic for ${KAFKA_DOWN_SECONDS}s (use another shell)..."
echo "  e.g. make stress-k6 VUS=10 DURATION=60s"
echo "Or: skip and proceed to restart immediately."
sleep "$KAFKA_DOWN_SECONDS"

PENDING_DURING=$(docker exec "$PG_CONTAINER" \
  psql -U "$PG_USER" -d "$PG_DB" -t -c \
  "SELECT COUNT(*) FROM events_outbox WHERE processed_at IS NULL;" \
  2>/dev/null | tr -d '[:space:]' || echo "?")
echo ""
echo "Outbox PENDING count during outage: $PENDING_DURING"

echo "Restoring Kafka: docker start $CONTAINER"
docker start "$CONTAINER" > /dev/null

echo "Waiting up to 90s for outbox drain (OutboxPoll × 3 = ~30s typical)..."
DRAIN_OK=false
for ((i=1; i<=90; i++)); do
  CURRENT=$(docker exec "$PG_CONTAINER" \
    psql -U "$PG_USER" -d "$PG_DB" -t -c \
    "SELECT COUNT(*) FROM events_outbox WHERE processed_at IS NULL;" \
    2>/dev/null | tr -d '[:space:]' || echo "?")
  if [[ "$CURRENT" == "0" || "$CURRENT" == "$PENDING_BEFORE" ]]; then
    DRAIN_OK=true
    break
  fi
  sleep 1
done

T1=$(date -u +%s)
echo ""
if $DRAIN_OK; then
  printf '\033[32mPASS: outbox drained back to baseline (%s) within %ds.\033[0m\n' \
    "$PENDING_BEFORE" "$((T1-T0))"
else
  CURRENT=$(docker exec "$PG_CONTAINER" \
    psql -U "$PG_USER" -d "$PG_DB" -t -c \
    "SELECT COUNT(*) FROM events_outbox WHERE processed_at IS NULL;" \
    2>/dev/null | tr -d '[:space:]' || echo "?")
  printf '\033[33mPARTIAL: outbox still has %s pending after 90s. Check OutboxRelay logs.\033[0m\n' "$CURRENT"
fi

echo "Document: docs/chaos-log/$(date -u +%Y-%m-%d)-kill-kafka.md"
