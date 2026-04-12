# Smoke Test Plan — Booking Monitor

Repeatable end-to-end verification for each PR in the remediation stack.
Run from the repo root on the **host** (your Mac), not inside a container.

## Prerequisites

```bash
# Tools required on host
which docker-compose   # Docker Compose v2+
which migrate          # brew install golang-migrate
which jq               # brew install jq
which curl             # built-in on macOS

# .env must exist (gitignored)
cp .env.example .env   # fill in real values; smoketest_pg_local etc. is fine

# Clean start
docker-compose down -v
docker-compose up -d
docker-compose ps      # all 10 containers should be Up

# Run migrations
make migrate-up
```

Wait ~30 seconds for Kafka to become healthy before hitting APIs.

---

## 1. Happy Path — API → Redis → Worker → DB → Outbox → Kafka → Payment → Confirmed

**Goal:** Verify the entire booking flow completes end-to-end.

```bash
# 1a. Create event
curl -s -X POST http://localhost/api/v1/events \
  -H 'Content-Type: application/json' \
  -d '{"name":"Smoke Test","total_tickets":5}' | jq
# Expected: {"id":N, "total_tickets":5, "available_tickets":5, ...}

EVENT_ID=<id from above>

# 1b. Book a ticket
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: smoke-001' \
  -d "{\"user_id\":1,\"event_id\":$EVENT_ID,\"quantity\":1}"
# Expected: {"message":"booking successful"}

# 1c. Wait for async worker + payment pipeline
sleep 3

# 1d. Check history — should show status=confirmed (or pending if payment still running)
curl -s "http://localhost/api/v1/history?page=1&size=10" | jq '.data[0].status'
# Expected: "confirmed"

# 1e. Idempotency replay
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: smoke-001' \
  -d "{\"user_id\":1,\"event_id\":$EVENT_ID,\"quantity\":1}" \
  -v 2>&1 | grep -iE "X-Idempotency-Replayed|< HTTP"
# Expected: HTTP/1.1 200 OK + X-Idempotency-Replayed: true

# 1f. Redis inventory matches
docker exec booking_redis redis-cli GET event:${EVENT_ID}:qty
# Expected: 4 (was 5, booked 1)
```

**Pass criteria:** Order appears in history as `confirmed`, idempotency header present, Redis qty = total - booked.

---

## 2. Sold Out — Redis gate + bookings_total metric

**Goal:** Verify Redis correctly rejects when inventory is 0, and `bookings_total{status="sold_out"}` increments.

```bash
# 2a. Drain remaining tickets
for i in $(seq 100 104); do
  curl -s -X POST http://localhost/api/v1/book \
    -H 'Content-Type: application/json' \
    -d "{\"user_id\":$i,\"event_id\":$EVENT_ID,\"quantity\":1}"
  echo
done
# Expected: first 4 → "booking successful", 5th → "sold out"

# 2b. Confirm Redis is 0
docker exec booking_redis redis-cli GET event:${EVENT_ID}:qty
# Expected: 0

# 2c. One more attempt to get a clean sold_out
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -d "{\"user_id\":999,\"event_id\":$EVENT_ID,\"quantity\":1}"
# Expected: {"error":"sold out"}

# 2d. Check metric
docker exec booking_app wget -qO- http://localhost:8080/metrics 2>/dev/null \
  | grep "^bookings_total"
# Expected: sold_out >= 1, success >= 4
```

**Pass criteria:** `bookings_total{status="sold_out"}` > 0.

---

## 3. InventorySoldOut Alert Fires (C6)

**Goal:** Prometheus alert transitions to `firing` after sold_out events.

```bash
# 3a. Verify alerts.yml is loaded
docker exec booking_prometheus ls /etc/prometheus/alerts.yml
# Expected: file exists (not "No such file")

# 3b. Check alert state (may take up to 30s for first evaluation)
curl -s "http://localhost:9090/api/v1/rules" | \
  jq '.data.groups[].rules[] | select(.name=="InventorySoldOut") | {name, state, query}'
# Expected: state="firing", query contains "bookings_total{status=\"sold_out\"}"

# 3c. If state is "inactive" or "pending", wait and retry:
sleep 30
curl -s "http://localhost:9090/api/v1/rules" | \
  jq '.data.groups[].rules[] | select(.name=="InventorySoldOut") | .state'
# Expected: "firing"
```

**Pass criteria:** `state: "firing"` with the corrected query expression.

**Known blocker if fails:**
- If `alerts.yml` is missing from container → PR #8 commit `55b9de8` fixes this (mount full prometheus dir).
- If `bookings_total` is always 0 → PR #8 commit `0284cc7` fixes this (fx.Decorate scoping).

---

## 4. Error Response Sanitization (C4)

**Goal:** No raw DB/driver errors leak to clients.

```bash
# 4a. Invalid request body (gin validator error)
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -d '{"user_id":1}'
# Expected: {"error":"invalid request body"}
# NOT: "Key: 'bookRequest.EventID' Error:Field validation..."

# 4b. Missing body
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -d '{}'
# Expected: {"error":"invalid request body"}

# 4c. Quantity exceeds max (binding:"max=10")
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -d '{"user_id":1,"event_id":1,"quantity":999}'
# Expected: {"error":"invalid request body"}

# 4d. Empty event name
curl -s -X POST http://localhost/api/v1/events \
  -H 'Content-Type: application/json' \
  -d '{"name":"","total_tickets":10}'
# Expected: {"error":"invalid request body"}

# 4e. Sold out response (sentinel → public message)
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -d "{\"user_id\":888,\"event_id\":$EVENT_ID,\"quantity\":1}"
# Expected: {"error":"sold out"} — NOT a raw error string
```

**Pass criteria:** Every error response is a short, fixed public message. No Go struct names, no SQL, no stack traces.

---

## 5. .env Fail-Fast (C5)

**Goal:** `docker-compose up` fails immediately with a clear message if a required env var is missing.

```bash
# 5a. Backup .env and remove POSTGRES_PASSWORD
cp .env .env.bak
sed -i '' '/^POSTGRES_PASSWORD/d' .env

# 5b. Try to bring up (should fail)
docker-compose up -d postgres 2>&1 | head -20
# Expected: error containing "POSTGRES_PASSWORD is required"

# 5c. Restore
cp .env.bak .env
rm .env.bak
```

**Pass criteria:** Compose refuses to start with a human-readable error naming the missing variable.

---

## 6. http.Server Timeouts (C3)

**Goal:** The Go server uses configured `ReadTimeout`/`WriteTimeout`, not Gin's default (zero).

### 6a. Log verification (quick)

```bash
docker logs booking_app 2>&1 | grep "Starting server"
# Expected: {"msg":"Starting server","port":"8080","read_timeout":5,"write_timeout":10}
# The read_timeout / write_timeout fields only exist if http.Server{} is constructed
# explicitly. The old r.Run() path does not log these.
```

### 6b. Slow-loris simulation (thorough, requires slowhttptest)

```bash
# Install: brew install slowhttptest
# Target nginx port 80 (which proxies to app:8080)
slowhttptest -c 100 -H -g -o slow_test \
  -i 10 -r 50 -t GET -u http://localhost/api/v1/history -p 3

# Or target app directly if port 8080 is published:
# slowhttptest -c 100 -H -g -o slow_test \
#   -i 10 -r 50 -t GET -u http://localhost:8080/api/v1/history -p 3

# Expected: connections are killed within ~5 seconds (ReadTimeout).
# Check slow_test.html for the report.
```

### 6c. Minimal nc test from inside container

```bash
docker exec booking_app sh -c '
  START=$(date +%s)
  # Send incomplete HTTP header, hold connection
  (printf "GET / HTTP/1.1\r\nHost: localhost\r\n"; sleep 10) | \
    timeout 8 nc localhost 8080 2>&1 || true
  END=$(date +%s)
  echo "Connection lived $((END - START)) seconds"
'
# Expected: ~5 seconds (ReadTimeout kills it before the 10s sleep finishes)
# Note: busybox nc may not support this well; use slowhttptest for reliable results
```

**Pass criteria:** Connections with incomplete headers are severed within `ReadTimeout` (5s).

---

## 7. DLQ Metrics Pre-Initialized (C1)

**Goal:** All DLQ/saga counters appear at `/metrics` from startup, even before any failures.

```bash
docker exec booking_app wget -qO- http://localhost:8080/metrics 2>/dev/null \
  | grep -E "^(dlq_messages_total|saga_poison_messages_total)"
# Expected: 6 dlq_messages_total lines + 1 saga_poison_messages_total line, all value 0
# The labels should be pre-initialized for all known {topic, reason} combos
```

**Pass criteria:** 7 counter lines present at value 0.

---

## 8. Payment DLQ — Invalid Event (C2)

**Goal:** A payment event with `OrderID <= 0` is routed to `order.created.dlq` with provenance headers.

```bash
# 8a. Produce a bad event
echo '{"id":-1,"status":"pending","user_id":1,"event_id":1,"quantity":1,"amount":100,"created_at":"2026-01-01T00:00:00Z"}' | \
  docker exec -i booking_kafka kafka-console-producer \
    --bootstrap-server localhost:9092 \
    --topic order.created

# 8b. Wait for consumer to process
sleep 3

# 8c. Check payment worker logs
docker logs booking_payment_worker 2>&1 | grep -E "Invalid OrderID|dead-lettering" | tail -5
# Expected:
#   "Invalid OrderID", "order_id":-1
#   "Invalid payment event — dead-lettering", "order_id":-1

# 8d. Consume from DLQ topic
docker exec booking_kafka kafka-console-consumer \
  --bootstrap-server localhost:9092 \
  --topic order.created.dlq \
  --from-beginning \
  --max-messages 5 \
  --timeout-ms 5000 \
  --property print.headers=true 2>&1 | head -5
# Expected: message with headers:
#   x-original-topic:order.created
#   x-original-partition:0
#   x-original-offset:N
#   x-dlq-reason:invalid_event
#   x-dlq-error:order_id=-1: invalid payment event
# And the full original payload in the value field

# 8e. Also test malformed JSON
echo 'this is not json' | \
  docker exec -i booking_kafka kafka-console-producer \
    --bootstrap-server localhost:9092 \
    --topic order.created
sleep 3
docker logs booking_payment_worker 2>&1 | grep "dead-lettering" | tail -3
# Expected: "Failed to unmarshal event — dead-lettering"
docker exec booking_kafka kafka-console-consumer \
  --bootstrap-server localhost:9092 \
  --topic order.created.dlq \
  --from-beginning --max-messages 10 --timeout-ms 5000 \
  --property print.headers=true 2>&1 | grep "invalid_payload"
# Expected: at least one message with x-dlq-reason:invalid_payload
```

**Pass criteria:**
- Invalid payload → DLQ with `reason=invalid_event`, headers present, original payload preserved
- Malformed JSON → DLQ with `reason=invalid_payload`
- Payment worker does NOT crash / hang

---

## 9. Payment Failure → Saga Compensation (end-to-end)

**Goal:** When the mock payment gateway fails, the saga compensator rolls back DB + Redis inventory.

The mock gateway has a configurable failure rate. To force a failure deterministically, you need to either:
- Set the mock gateway to 100% failure rate (requires code change / env var — not currently configurable)
- Run enough bookings that statistical failures occur naturally

```bash
# 9a. Create a fresh event
curl -s -X POST http://localhost/api/v1/events \
  -H 'Content-Type: application/json' \
  -d '{"name":"Saga Test","total_tickets":100}' | jq
SAGA_EVENT_ID=<id>

# 9b. Send many bookings (mock gateway has ~30% failure rate)
for i in $(seq 200 250); do
  curl -s -o /dev/null -X POST http://localhost/api/v1/book \
    -H 'Content-Type: application/json' \
    -d "{\"user_id\":$i,\"event_id\":$SAGA_EVENT_ID,\"quantity\":1}"
done

# 9c. Wait for payment + saga pipeline
sleep 10

# 9d. Check for compensated orders
curl -s "http://localhost/api/v1/history?page=1&size=100&status=compensated" | \
  jq '.meta.total'
# Expected: some number > 0 (depends on mock gateway failure rate)

# 9e. Check saga consumer logs
docker logs booking_app 2>&1 | grep "rollback successful" | tail -5
# Expected: "rollback successful", "order_id":N

# 9f. Check Redis saga revert keys
docker exec booking_redis redis-cli KEYS 'saga:reverted:*' | head -5
# Expected: at least one key like saga:reverted:order:N

# 9g. Verify inventory consistency: DB available_tickets should match
#     the number of confirmed orders
docker exec booking_db psql -U booking -d booking -c \
  "SELECT available_tickets FROM events WHERE id = $SAGA_EVENT_ID"
CONFIRMED=$(curl -s "http://localhost/api/v1/history?page=1&size=1000&status=confirmed" | \
  jq "[.data[] | select(.event_id==$SAGA_EVENT_ID)] | length")
echo "DB available = (see above), confirmed orders = $CONFIRMED"
echo "Expected: available_tickets = total_tickets - confirmed_orders"
```

**Pass criteria:**
- At least 1 compensated order exists
- `saga:reverted:*` Redis keys present
- DB `available_tickets + confirmed_count = total_tickets`

---

## 10. Redis DLQ — Worker Retry Exhaustion

**Goal:** When the worker's `processWithRetry` exhausts 3 attempts, the message goes to `orders:dlq` and Redis inventory is reverted.

This is hard to trigger deterministically without a DB outage. The easiest reliable trigger is a **duplicate purchase** (which gets caught by the UNIQUE constraint 3 times then DLQ'd).

```bash
# 10a. Book with the same user twice for the same event
#      (2nd booking goes through Redis but worker catches DB constraint)
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -d "{\"user_id\":1,\"event_id\":$SAGA_EVENT_ID,\"quantity\":1}"
sleep 1
curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -d "{\"user_id\":1,\"event_id\":$SAGA_EVENT_ID,\"quantity\":1}"
sleep 5

# 10b. Check worker logs for duplicate handling
docker logs booking_app 2>&1 | grep "Duplicate purchase blocked" | tail -5
# Expected: multiple retries for the same msg_id

# 10c. Check Redis DLQ stream
docker exec booking_redis redis-cli XLEN orders:dlq
# Expected: >= 1

# 10d. Inspect DLQ entry
docker exec booking_redis redis-cli XRANGE orders:dlq - + COUNT 3
# Expected: entries with "error" field containing the failure reason
```

**Pass criteria:** `orders:dlq` has at least 1 entry for the duplicate-blocked message.

---

## 11. Advisory Lock Leader Election

**Goal:** Only one OutboxRelay instance holds the lock at a time.

```bash
# 11a. Check current advisory locks held
docker exec booking_db psql -U booking -d booking -c \
  "SELECT pid, classid, objid, granted FROM pg_locks WHERE locktype = 'advisory'"
# Expected: exactly 1 row with objid=1001 and granted=true

# 11b. Restart app to verify lock re-acquisition
docker-compose restart app
sleep 10
docker exec booking_db psql -U booking -d booking -c \
  "SELECT pid, classid, objid, granted FROM pg_locks WHERE locktype = 'advisory'"
# Expected: 1 row with a NEW pid (the restarted app), objid=1001, granted=true
```

**Pass criteria:** Exactly 1 advisory lock for objid 1001 at any time.

---

## 12. Observability Endpoints

**Goal:** All observability endpoints are accessible and returning data.

```bash
# 12a. Prometheus targets
curl -s http://localhost:9090/api/v1/targets | jq '.data.activeTargets | length'
# Expected: >= 2 (prometheus self + booking-service)

# 12b. Prometheus metrics from app
curl -s "http://localhost:9090/api/v1/query?query=up{job='booking-service'}" | \
  jq '.data.result[0].value[1]'
# Expected: "1"

# 12c. Grafana login
curl -s -u admin:smoketest_grafana_local http://localhost:3000/api/org | jq '.name'
# Expected: "Main Org."

# 12d. Jaeger — look for traces
curl -s "http://localhost:16686/api/services" | jq '.data[]'
# Expected: "booking-service" should appear

# 12e. Jaeger — search for BookTicket spans
curl -s "http://localhost:16686/api/traces?service=booking-service&operation=BookTicket&limit=5" | \
  jq '.data | length'
# Expected: >= 1
```

**Pass criteria:** All 5 checks return expected values.

---

## Cleanup

```bash
docker-compose down -v   # remove everything including volumes
rm -f .env               # optional: remove local secrets
```

---

## Test Status Tracker

Copy this table into the PR description or a GitHub issue to track progress:

| # | Test | PR #8 | PR #9 | PR #10 | PR #11 |
|---|------|-------|-------|--------|--------|
| 1 | Happy path E2E | ✅ | | | |
| 2 | Sold out + metric | ✅ | | | |
| 3 | InventorySoldOut alert | ✅ | | | |
| 4 | Error sanitization | ✅ | | | |
| 5 | .env fail-fast | ⬜ | | | |
| 6 | http.Server timeouts | ✅ (log) / ⬜ (slowhttptest) | | | |
| 7 | DLQ metrics pre-init | ✅ | | | |
| 8 | Payment DLQ invalid event | ✅ | | | |
| 9 | Saga compensation E2E | ⬜ | | | |
| 10 | Redis DLQ worker retry | ✅ (via duplicate) | | | |
| 11 | Advisory lock leader | ⬜ | | | |
| 12 | Observability endpoints | ⬜ | | | |

Legend: ✅ = passed, ⬜ = not yet run, ❌ = failed, 🟡 = partial

---

## Known Limitations

- **payment_worker has no `/metrics` endpoint**: DLQ metrics from the payment consumer are only in-process memory. Prometheus cannot scrape them. Follow-up needed: add a metrics HTTP server to `cmd/booking-cli payment`.
- **Mock gateway failure rate is not configurable at runtime**: To force saga compensation you must rely on statistical failures or modify code. Follow-up: add `PAYMENT_FAILURE_RATE` env var.
- **Slow-loris test requires `slowhttptest`** (not installed by default): The log-verification method (Test 6a) is sufficient for most cases.
- **`bookings_total` stays 0 if fx.Decorate bug is not fixed**: PR #8 commit `0284cc7` resolves this. If testing an older commit, Task 2d/3 will fail.
