# Booking Monitor — Alert Runbooks

> Bilingual operator guide for every alert in `deploy/prometheus/alerts.yml`.
> When an alert fires, Alertmanager delivers the notification with a `runbook_url`
> annotation pointing at one of the anchors below. Click → triage → act.

This is one consolidated runbook (Stripe / Lyft style) rather than 24 separate
files. The grain is "one alert ↔ one anchor section" — easier to keep in sync
with `alerts.yml` than 24 files that drift independently.

Each section follows the same shape:

- **Symptom** — what the on-call sees (alert text + which dashboard panel)
- **Diagnosis** — first commands / questions to triage
- **Action** — concrete remediation steps
- **Escalation** — when to wake someone else up

The Grafana panels referenced live in the **Booking Monitor Dashboard** at
http://localhost:3000 (CP9). Alertmanager UI is at http://localhost:9093 for
silence / inhibit / notification log.

## Index

### RED-method (request layer)
- [HighErrorRate](#higherrorrate)
- [HighLatency](#highlatency)
- [InventorySoldOut](#inventorysoldout)

### Streams (worker / DLQ throughput)
- [OrdersStreamBacklogYellow](#ordersstreambacklogyellow)
- [OrdersStreamBacklogOrange](#ordersstreambacklogorange)
- [OrdersStreamBacklogRed](#ordersstreambacklogred)
- [OrdersStreamConsumerLag](#ordersstreamconsumerlag)
- [OrdersDLQNonEmpty](#ordersdlqnonempty)
- [RedisStreamCollectorDown](#redisstreamcollectordown)

### Reconciler (A4 charging two-phase intent log)
- [ReconStuckCharging](#reconstuckcharging)
- [ReconFindStuckErrors](#reconfindstuckerrors)
- [ReconGatewayErrors](#recongatewayerrors)
- [ReconMaxAgeExceeded](#reconmaxageexceeded)
- [ReconMarkErrors](#reconmarkerrors)

### Saga watchdog (A5)
- [SagaStuckFailedOrders](#sagastuckfailedorders)
- [SagaCompensatorErrors](#sagacompensatorerrors)
- [SagaWatchdogFindStuckErrors](#sagawatchdogfindstuckerrors)
- [SagaMaxFailedAgeExceeded](#sagamaxfailedageexceeded)

### Correctness controls
- [IdempotencyCacheGetErrors](#idempotencycachegeterrors)
- [DBRollbackFailures](#dbrollbackfailures)

### Redis / Kafka infra
- [RedisXAckFailures](#redisxackfailures)
- [RedisRevertFailures](#redisrevertfailures)
- [RedisXAddFailures](#redisxaddfailures)
- [KafkaConsumerStuck](#kafkaconsumerstuck)

---

## HighErrorRate

**Symptom.** 5xx rate > 5 % of total request rate over 5m, sustained 2m. Dashboard: *Request Rate (RPS)* (top-left).

**Diagnosis.**
1. Check which endpoint is failing: `sum by (path, status) (rate(http_requests_total{status=~"5.."}[5m]))` in Prometheus.
2. Tail app logs: `docker logs -f booking_app | grep ERROR`.
3. Cross-check downstream alerts: are `RedisXAddFailures`, `DBRollbackFailures`, or any worker-side alert also firing?

**Action.**
- If a single path dominates → investigate that handler in `internal/infrastructure/api/`.
- If 5xx is system-wide → check DB / Redis / Kafka health (the *Saturation* panels + dependency healthchecks).
- If a recent deploy correlates → roll back via `git revert` + redeploy.

**Escalation.** Page the on-call if 5xx > 20 % or sustained > 10m. This is the user-visible signal that gates SLO error budget burn.

---

## HighLatency

**Symptom.** p99 HTTP latency > 2s over 5m, sustained 2m. Dashboard: *Global Request Latency*.

**Diagnosis.**
1. Check by endpoint: `histogram_quantile(0.99, sum by (le, path) (rate(http_request_duration_seconds_bucket[5m])))`.
2. *Saturation (Goroutines)* panel — sustained climb correlates with goroutine leak.
3. *PG pool: in-use vs idle* — if `pg_pool_in_use` is pinned at `DB_MAX_OPEN_CONNS` (default 50), the wait queue is the bottleneck.
4. *Redis stream / DLQ failure rates* — worker stalls leak back into request latency through inventory pressure.

**Action.**
- DB pool saturated → bump `DB_MAX_OPEN_CONNS` (cheap) or fix the slow query (correct). `EXPLAIN ANALYZE` against the slow path.
- GC pressure → check `go_memstats_alloc_bytes` slope, consider raising `GOMEMLIMIT`.
- Worker stalls → see [OrdersStreamBacklogOrange](#ordersstreambacklogorange) action list.

**Escalation.** Warning severity. Critical if p99 > 5s or HighErrorRate also firing.

---

## InventorySoldOut

**Symptom.** ≥ 1 booking attempt in the last 5m returned `sold_out` (HTTP 409). Severity `info`. Dashboard: *Conversion Rate*.

**Diagnosis / Action.** This is a business event, not a failure. Useful to know an event has hit its capacity ceiling. No on-call action required unless the operator expects more inventory and wants to rule out a Redis vs DB drift (see [RedisRevertFailures](#redisrevertfailures) for the drift signal).

---

## OrdersStreamBacklogYellow

**Symptom.** `orders:stream` length > 10K for 2m. Severity `info`. Worker is starting to fall behind.

**Diagnosis.**
- *Saga watchdog resolved by outcome* — is the worker dying?
- `docker logs booking_app | grep worker_service` — any panic / restart?
- `redis-cli -a "$REDIS_PASSWORD" XLEN orders:stream` for current depth.

**Action.** Watch for ~1 minute. If it climbs to 50K (orange), escalate. Common causes:
- Worker is processing slower than producers → consider scaling out.
- A single hot consumer is stuck → see [OrdersStreamConsumerLag](#ordersstreamconsumerlag).

**Escalation.** Self-resolves usually. Escalate when Orange / Red follows.

---

## OrdersStreamBacklogOrange

**Symptom.** `orders:stream` length > 50K for 2m. Severity `warning`. Page on-call now.

**Action.**
- `docker compose restart app payment_worker` if a worker has hung.
- If worker logs show DB / Redis errors → fix the dependency first.
- If steady-state load has grown → scale out worker replicas (see `WORKER_*` env config).

**Escalation.** Critical if it crosses 200K (Red) — Redis OOM is then ~minutes away.

---

## OrdersStreamBacklogRed

**Symptom.** `orders:stream` length > 200K for 1m. Severity `critical`. Approaching Redis OOM territory.

**Action.**
1. **Drop new traffic at nginx** if user-visible 5xx is acceptable temporarily (`location /api/v1/book { return 503; }` or rate-limit zone tightening).
2. Manually scale workers: `docker compose up -d --scale payment_worker=3`.
3. If Redis is OOMing → consider `XTRIM orders:stream MAXLEN 0` (data loss; only as last resort to prevent total Redis crash).

**Escalation.** Stay paged until length is back below Yellow threshold. Post-mortem required if OOM occurred.

---

## OrdersStreamConsumerLag

**Symptom.** A specific consumer's oldest pending entry is > 60s old for 2m. Severity `warning`. Length may look fine — one consumer is dragging the tail.

**Diagnosis.** `redis-cli -a "$REDIS_PASSWORD" XINFO CONSUMERS orders:stream orders:group` — find the consumer with the highest `idle` time.

**Action.**
- If the consumer is hung → restart its container.
- If multiple consumers are slow → check downstream (DB / payment gateway).

---

## OrdersDLQNonEmpty

**Symptom.** `orders:dlq` has > 0 entries for 5m. Severity `warning`. Operator review awaits.

**Diagnosis.** `redis-cli -a "$REDIS_PASSWORD" XRANGE orders:dlq - +` to read the entries. Each carries a `reason` label (malformed_parse / malformed_classified / exhausted_retries).

**Action.**
- malformed_parse / malformed_classified → producer bug. Investigate via the message payload + git blame on the producer side.
- exhausted_retries → downstream was degraded for the retry budget window. Spot-check downstream health.
- After investigation: `XACK` (still in PEL) or `XDEL` (after archiving the entry to long-term storage).

---

## RedisStreamCollectorDown

**Symptom.** `redis_stream_collector_errors_total` rate > 0 for 2m. Severity `critical`. **Stream backlog gauges are stale** — every other stream alert (Yellow / Orange / Red / ConsumerLag / DLQNonEmpty) goes silent until this resolves.

**Diagnosis.** `redis-cli -a "$REDIS_PASSWORD" PING` from the app host. Logs: `docker logs booking_app | grep stream_collector`.

**Action.**
- Restart Redis: `docker compose restart redis`.
- Check Redis AUTH credentials in `.env` haven't drifted from what the app sees.
- Network split between app and Redis → standard Redis-connectivity triage.

**Escalation.** Page immediately — when this fires, you have **no visibility** into stream depth.

---

## ReconStuckCharging

**Symptom.** `recon_stuck_charging_orders` > 0 for 5m. Dashboard: *Recon stuck-charging gauge*.

**Diagnosis.**
- Are gateway probes failing? Cross-check [ReconGatewayErrors](#recongatewayerrors).
- Is the reconciler running? `docker logs booking_recon | grep recon_sweep`.
- DB load? *PG pool: in-use vs idle*.

**Action.**
- Gateway slow → tune `RECON_GATEWAY_TIMEOUT` (default 2s) higher temporarily. Don't make permanent without root-causing.
- Reconciler dead → restart `docker compose restart recon`.
- Real backlog → bump `RECON_BATCH_SIZE` (default 100) or shorten `RECON_SWEEP_INTERVAL` (default 30s).

---

## ReconFindStuckErrors

**Symptom.** `recon_find_stuck_errors_total` rate > 0 for 2m. Severity `critical`. The reconciler's sweep query is failing — **stuck-Charging detection is silently broken**.

**Diagnosis.** Logs: `docker logs booking_recon | grep find_stuck`.

**Action.**
- Migration check: `psql ... -c "SELECT * FROM schema_migrations ORDER BY version DESC LIMIT 5"`. Migration `000010` adds the partial index FindStuckCharging needs.
- DB connectivity → standard PG triage.

**Escalation.** Critical — the reconciler is silently broken. Page the DB owner.

---

## ReconGatewayErrors

**Symptom.** `recon_gateway_errors_total` rate > 1/30s averaged over 5m. Severity `warning`.

**Diagnosis.** Gateway probe is failing for the recon path specifically. Check the payment gateway endpoint health (curl from the app host).

**Action.** Triage at the gateway. Recon-side retries every sweep, so the alert auto-resolves once the gateway recovers.

---

## ReconMaxAgeExceeded

**Symptom.** `increase(recon_resolved_total{outcome="max_age_exceeded"}[1h]) > 0`. Severity `critical`. The reconciler force-failed an order older than `RECON_MAX_CHARGING_AGE` (default 24h).

**Diagnosis.** `psql ... -c "SELECT * FROM order_status_history WHERE from_status='charging' AND to_status='failed' AND created_at > now() - interval '1 hour'"`.

**Action.**
- For each order: cross-check the gateway side (was the customer actually charged?) and reconcile manually.
- If many orders → there was a sustained gateway outage; capture the timeline for post-mortem.

**Escalation.** Page — these are real customer-facing incidents.

---

## ReconMarkErrors

**Symptom.** `recon_mark_errors_total` rate > 0 for 5m. Severity `warning`. Recon classified orders correctly but the DB transition itself is failing.

**Diagnosis.**
- `recon_resolved_total` should NOT be advancing if marks are failing — the gauge will keep climbing.
- Check DB lock state: `SELECT * FROM pg_stat_activity WHERE wait_event IS NOT NULL`.

**Action.** DB lock contention or connectivity. Standard PG triage.

---

## SagaStuckFailedOrders

**Symptom.** `saga_stuck_failed_orders` > 0 for 10m. Severity `warning`. The watchdog has been re-driving the compensator without clearing the backlog.

**Diagnosis.** Cross-check [SagaCompensatorErrors](#sagacompensatorerrors) — if both are firing, the compensator is failing on every sweep.

**Action.**
- Redis revert blocked → `redis-cli -a "$REDIS_PASSWORD" KEYS 'saga:reverted:*' | head` to inspect.
- DB lock on `orders` → `SELECT * FROM pg_stat_activity` (as above).
- Unmapped error → application logs.

---

## SagaCompensatorErrors

**Symptom.** `saga_watchdog_resolved_total{outcome="compensator_error"}` rate > 0 for 2m. Severity `warning`. Fast-path companion to [SagaStuckFailedOrders](#sagastuckfailedorders).

**Diagnosis.** Look at the specific compensator error in `docker logs booking_saga_watchdog`. Distinct labels (`compensator_error` vs `getbyid_error` vs `marshal_error`) point at different subsystems.

**Action.**
- `compensator_error` — Redis revert / DB MarkCompensated failed. Check Redis + DB.
- `getbyid_error` — DB read upstream. Check pool pressure.
- `marshal_error` — regression in `OrderFailedEvent` shape. Code defect.

---

## SagaWatchdogFindStuckErrors

**Symptom.** `saga_watchdog_find_stuck_errors_total` rate > 0 for 2m. Severity `critical`. Watchdog cannot detect stuck-Failed orders — **same silent-broken pattern as `ReconFindStuckErrors`**.

**Diagnosis / Action.** Migration `000011` adds the partial index FindStuckFailed needs. Otherwise: standard DB triage.

---

## SagaMaxFailedAgeExceeded

**Symptom.** `increase(saga_watchdog_resolved_total{outcome="max_age_exceeded"}[1h]) > 0`. Severity `critical`.

**Why this is different from `ReconMaxAgeExceeded`.** Recon force-fails. Watchdog DOES NOT auto-transition (moving Failed → Compensated without verifying inventory was reverted is unsafe). Operator decides manually.

**Action.**
- `psql ... -c "SELECT * FROM order_status_history WHERE status='failed' AND created_at < now() - interval '24 hours'"`.
- For each order: verify Redis inventory state matches DB, then either manually `MarkCompensated` + revert inventory or escalate to engineering.

---

## IdempotencyCacheGetErrors

**Symptom.** `idempotency_cache_get_errors_total` rate > 0 for 1m. Severity `warning`. **Page-worthy** — duplicate-charge protection is suspended for affected requests.

**Diagnosis.** Redis health: `PING`, AUTH, AOF state.

**Action.** Restore Redis. The handler fails open during the outage (DB UNIQUE constraints provide last-resort protection), but the contract degrades for the duration.

**Escalation.** Page immediately if sustained — financial-correctness control is offline.

---

## DBRollbackFailures

**Symptom.** `db_rollback_failures_total` rate > 0 for 5m. Severity `warning`.

**Action.** Should be effectively zero in production. Sustained = driver / connection-pool bug. Check pgx / database/sql release notes.

---

## RedisXAckFailures

**Symptom.** `redis_xack_failures_total` rate > 0 for 5m. Severity `warning`. PEL grows unbounded; consumers will redo work on rebalance.

**Diagnosis.** `redis-cli -a "$REDIS_PASSWORD" XINFO GROUPS orders:stream` — pending count climbing without bound.

**Action.** Standard Redis health triage. Consumers are idempotent so duplicate work is safe; the alert is a leading indicator that something is wrong, not an immediate-impact incident.

---

## RedisRevertFailures

**Symptom.** `redis_revert_failures_total` rate > 0 for 5m. Severity `warning`. Saga compensation is failing to revert Redis inventory — **drift vs DB will accumulate**.

**Action.** Check `revert.lua` execution logs (Redis SLOWLOG / app logs). Once Redis is healthy, the saga compensator retries idempotently — drift heals.

---

## RedisXAddFailures

**Symptom.** `redis_xadd_failures_total` rate > 0 for 5m. Severity `warning`. The booking hot path is intermittently failing to enqueue orders.

**Action.** User-visible 5xx may also be elevated. Standard Redis health triage.

---

## KafkaConsumerStuck

**Symptom.** `kafka_consumer_retry_total` rate > 0 for a topic over 2m. Severity `warning`. Consumer is "stuck but not dead" — leaving messages uncommitted for rebalance retry.

**Diagnosis.** Check the downstream the consumer depends on: payment gateway? DB? Redis?

**Action.** This alert intentionally does NOT trigger DLQ routing on transient errors (would cause overselling during DB hiccups). Operator's job is to find and fix the downstream. Once recovered, the consumer drains naturally.
