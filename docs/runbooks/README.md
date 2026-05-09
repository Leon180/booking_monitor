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

### Saga compensator (D12.4 — Kafka-driven hot path)
- [SagaCompensatorErrorRate](#sagacompensatorerrorrate)
- [SagaConsumerLagHigh](#sagaconsumerlaghigh)
- [SagaCompensatorClassifierDrift](#sagacompensatorclassifierdrift)
- [SagaCompensatorRedisInventoryLeak](#sagacompensatorredisinventoryleak)

### Correctness controls
- [IdempotencyCacheGetErrors](#idempotencycachegeterrors)
- [DBRollbackFailures](#dbrollbackfailures)

### Redis / Kafka infra
- [RedisXAckFailures](#redisxackfailures)
- [RedisRevertFailures](#redisrevertfailures)
- [RedisXAddFailures](#redisxaddfailures)
- [ConsumerGroupRecreated](#consumergrouprecreated)
- [InventoryDriftDetected](#inventorydriftdetected)
- [InventoryDriftListEventsErrors](#inventorydriftlisteventserrors)
- [InventoryDriftCacheReadErrors](#inventorydriftcachereaderrors)
- [SweepGoroutinePanic](#sweepgoroutinepanic)
- [KafkaConsumerStuck](#kafkaconsumerstuck)

### Outbox (Kafka publish bridge)
- [OutboxPendingBacklog](#outboxpendingbacklog)
- [OutboxPendingCollectorDown](#outboxpendingcollectordown)

### Payment webhook (D5)
- [PaymentWebhookSignatureFailing](#paymentwebhooksignaturefailing)
- [PaymentWebhookUnknownIntentSurging](#paymentwebhookunknownintentsurging)
- [PaymentWebhookLateSuccessAfterExpiry](#paymentwebhooklatesuccessafterexpiry)
- [PaymentWebhookIntentMismatch](#paymentwebhookintentmismatch)

### Reservation expiry sweeper (D6)
- [ExpiryOldestOverdueAge](#expiryoldestoverdueage)
- [ExpiryProcessingErrors](#expiryprocessingerrors)
- [ExpiryFindErrors](#expiryfinderrors)
- [ExpiryMaxAgeExceeded](#expirymaxageexceeded)

### Meta (scrape health)
- [TargetDown](#targetdown)

### Deployment / cutover
- [D7 cutover — `order.created` outbox drain](#d7-cutover-note--ordercreated-outbox-drain)

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
- `docker compose restart app` if the in-process worker has hung.
- If worker logs show DB / Redis errors → fix the dependency first.
- If steady-state load has grown → scale out worker replicas (see `WORKER_*` env config).

**Escalation.** Critical if it crosses 200K (Red) — Redis OOM is then ~minutes away.

---

## OrdersStreamBacklogRed

**Symptom.** `orders:stream` length > 200K for 1m. Severity `critical`. Approaching Redis OOM territory.

**Action.**
1. **Drop new traffic at nginx** if user-visible 5xx is acceptable temporarily (`location /api/v1/book { return 503; }` or rate-limit zone tightening).
2. Manually scale `app` replicas: `docker compose up -d --scale app=3` (the worker runs in-process inside `app`).
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

**Diagnosis.** `redis-cli -a "$REDIS_PASSWORD" XRANGE orders:dlq - +` to read the entries. Each carries a `reason` label (`malformed_reverted_legacy` / `malformed_unrecoverable` / `malformed_classified` / `exhausted_retries`).

**Action.**
- `malformed_reverted_legacy` → expected rolling-upgrade taper. The worker extracted legacy hints, reverted Redis inventory, then DLQ'd the message. Track the rate; it should decay toward zero after old PEL entries drain.
- `malformed_unrecoverable` → inventory leak. The worker could not parse revert hints or `RevertInventory` itself failed. Page immediately and reconcile Redis `ticket_type_qty:{id}` against Postgres `event_ticket_types.available_tickets`.
- `malformed_classified` → producer bug. Message parsed but failed deterministic domain invariants. Investigate the payload + git blame on the producer side.
- `exhausted_retries` → downstream was degraded for the retry budget window. Spot-check DB / Redis / payment-worker health.
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

**Diagnosis.** `psql ... -c "SELECT * FROM order_status_history WHERE from_status='charging' AND to_status='failed' AND occurred_at > now() - interval '1 hour'"`.

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

**Why this is different from `ReconMaxAgeExceeded`.** Recon force-fails. Watchdog DOES NOT auto-transition (moving any failure-terminal status → Compensated without verifying inventory was reverted is unsafe). Operator decides manually.

**Scope (post-D2 / Pattern A).** The watchdog's `FindStuckFailed` query covers all three failure-terminal statuses that compensation accepts: `failed` (legacy A4 worker / charging-fail path), `expired` (Pattern A reservation TTL), `payment_failed` (Pattern A webhook failure). All three reach `Compensated` via the same compensator, so the alert and triage flow are unified.

**Action.**
- `psql ... -c "SELECT id, user_id, event_id, status, updated_at FROM orders WHERE status IN ('failed', 'expired', 'payment_failed') AND updated_at < NOW() - interval '24 hours'"`.

  Query the `orders` table directly, NOT `order_status_history` — the alert intent is "currently stuck in a failure-terminal status", and the history table records every transition including ones that have since been Compensated. Querying history would give false positives for orders the saga consumer eventually compensated.

- For each order returned: verify Redis inventory state matches DB, then either manually `MarkCompensated` + revert inventory or escalate to engineering. The remediation step is the same regardless of which failure-terminal status the order is stuck in (the compensator widens accordingly in D2).

---

## SagaCompensatorErrorRate

**Symptom.** `saga_compensator_events_processed_total` non-success ratio > 5% for 5m. Severity `warning`. Distinct from `SagaCompensatorErrors` above (that watches the watchdog re-drive path; this watches the Kafka consumer hot path).

**Triage by dominant outcome label.** Run:

```promql
topk(5, sum by (outcome) (rate(saga_compensator_events_processed_total{outcome!~"compensated|already_compensated"}[5m])))
```

Then map the dominant label to its handler:

| Outcome | What broke | Action |
|---|---|---|
| `redis_revert_error` | Redis revert failed on FRESH compensation | This SHOULD have already paged via `SagaCompensatorRedisInventoryLeak` (critical). Inventory is leaked — see that runbook. |
| `already_compensated_redis_error` | Idempotent re-drive Redis blip | Usually benign (SETNX guard means Redis was already correct from first delivery). Sustained > 0 means Redis is unhealthy. Check Redis MEMORY USAGE + slowlog. |
| `unmarshal_error` | Poison `order.failed` payload | Check producer-side schema regression. `kafka-console-consumer --topic order.failed --offset latest --max-messages 5` to inspect payload. DLQ is at `order.failed.dlq`. |
| `getbyid_error` / `markcompensated_error` | DB outage upstream of Redis | Check `up{job="postgres-exporter"}`, pg_stat_activity for lock contention. |
| `incrementticket_error` | DB constraint violation OR contention | Check `event_ticket_types.available_tickets` for any rows where `available_tickets > total_tickets` (over-increment regression). |
| `list_ticket_type_error` | DB outage during legacy-event Path B fallback | Same DB-outage class as above; isolated label so triage scopes to the rolling-upgrade fallback path. |
| `path_c_skipped` | Rolling-upgrade artifact | Pre-v3 multi-ticket-type events. Inventory was NOT reverted but this is structurally impossible during the upgrade window. Check deploy timing — should taper to 0 within 1h after rolling upgrade completes. **If sustained > 1h after deploy is done, escalate.** |
| `context_error` | Saga consumer context cancellation | Usually shutdown noise during deploys. If sustained outside deploy windows, check for upstream timeout configs. |
| `uow_infra_error` | DB connection pool / tx-begin failure | Check pg pool stats, `db_pool_*` metrics, connection limits. |
| `unknown` | Code-path missing `record()` call | This SHOULD have already paged via `SagaCompensatorClassifierDrift`. See that runbook. |

**False-positive note.** If `path_c_skipped` is dominant during the first hour after a rolling upgrade, that's expected — pre-v3 messages flushing through. If `already_compensated_redis_error` is dominant during a Kafka redelivery burst (consumer recovering from lag), that's also expected — SETNX means Redis state is correct.

---

## SagaConsumerLagHigh

**Symptom.** `saga_compensation_consumer_lag_seconds > 30 for 2m` AND `up{job="booking-service"} == 1` (gated to avoid stuck-gauge false-positives during consumer crash). Severity `warning`.

**What this measures.** The lag-since-write of the most-recently-processed `order.failed` Kafka message. `msg.Time` was set by the outbox relay to `events_outbox.created_at` (PR-D12.4 Slice 0 data path), so the lag includes:
1. Outbox relay polling delay (default 500ms tick)
2. Kafka write + broker round-trip
3. Consumer FetchMessage queue wait
4. Compensator processing time (UoW + Redis revert)

**Why up==1 gating matters.** This metric is a PERFORMANCE indicator, not a LIVENESS indicator. A crashed consumer leaves the gauge stale at the last value until Prometheus marks the target down (`scrape_interval × 3`). Without the gate, a dead consumer would false-fire this alert for minutes before the actual liveness signal kicks in. Liveness is a separate concern handled by Prometheus's default `up == 0` paging.

**Action.**
1. **Check Kafka backlog**: `kafka-consumer-groups --bootstrap-server kafka:9092 --describe --group saga-consumer`. If `LAG` is high and growing, Kafka producers (webhook handler, expiry sweeper, recon force-fail) are outpacing the consumer.
2. **Check compensator latency**: `histogram_quantile(0.99, sum by (le)(rate(saga_compensation_loop_duration_seconds_bucket[5m])))`. p99 above 5s with sustained lag means compensator is the bottleneck — check Redis revert latency + DB lock contention on `orders` and `event_ticket_types`.
3. **Check consumer thread count**: single-instance saga consumer today. If sustained lag with a backlog, consider scaling. **NOTE for k8s**: in multi-pod deployments verify `saga_compensation_consumer_lag_seconds` and `up{job='booking-service'}` share identical `instance` label values, or replace `on(instance)` in the alert with `on(job)` if all instances scrape under one job.

**Forward-compat NOTE for D12.5**: when PR-D12.5 lands per-stage scrape jobs and may rename `job="booking-service"` → `job="d12-stage4"`, this alert's gate expression must be updated.

---

## SagaCompensatorClassifierDrift

**Symptom.** `increase(saga_compensator_events_processed_total{outcome="unknown"}[5m]) > 0`. Severity `warning` (single-occurrence alert; `for: 0m`).

**This is a code regression alert, not an infra alert.** The `unknown` outcome label is the deferred-sentinel value — emitted ONLY when a return path in `compensator.HandleOrderFailed` fails to call `RecordEventProcessed(...)` explicitly. Distinct from `uow_infra_error` and `context_error` which ARE recorded explicitly by their respective branches. Should be 0 in production forever.

**Action.**
1. **Identify the offending code path.** Diff the latest deploy's compensator.go changes against the previous version: `git log -p --since="$(date -v-7d -u +%Y-%m-%dT%H:%M:%SZ)" -- internal/application/saga/compensator.go`. Look for new `return` statements without a preceding `record(<outcome>)` call.
2. **Add the missing `record()` call.** Pick the appropriate outcome label or add a new one (and pre-warm in `metrics_init.go`'s SagaCompensatorEventsTotal block + document in monitoring.md §2).
3. **Ship a build.** Until then, the metric loses granularity for whatever traffic hits that path — but the system is functionally correct (the order is still compensated; only the metric label is wrong).

**Re-notification cadence.** Alertmanager will re-page every `repeat_interval` (default 4h for warning) until the regression is deployed away.

---

## SagaCompensatorRedisInventoryLeak

**Symptom.** `increase(saga_compensator_events_processed_total{outcome="redis_revert_error"}[5m]) > 0`. Severity `critical` (single-occurrence alert; `for: 0m`).

**This is a real inventory leak.** Unlike `already_compensated_redis_error` (idempotent re-drive blip — usually benign because SETNX guard means Redis state was already correct from a previous delivery), `redis_revert_error` fires on the FRESH compensation path: PG `MarkCompensated` committed, but the subsequent `RevertInventory` call failed. The order is `compensated` in PG, but the Redis qty key is permanently short by the order's quantity until manual intervention.

**Action.**
1. **Identify the affected order(s).** Check the saga compensator logs around the alert timestamp:
   ```bash
   docker logs booking_app 2>&1 | grep "failed to rollback Redis inventory" | tail -20
   ```
   Each log line carries `order_id` + `error` for triage.
2. **Verify Redis health.** `redis-cli INFO replication` + `redis-cli MEMORY USAGE saga:reverted:order:<id>` for the affected orders. If Redis is down → restore Redis first, then proceed.
3. **Manually revert the inventory.** For each affected order:
   ```bash
   # Get the ticket_type_id and quantity from the order
   psql ... -c "SELECT ticket_type_id, quantity FROM orders WHERE id = '<order_id>'"
   # INCRBY the Redis qty key (CAREFULLY — verify the SETNX guard isn't already set,
   # otherwise you'd double-count if a future re-drive succeeds)
   redis-cli EXISTS "saga:reverted:order:<order_id>"  # should be 0; if 1, skip — already reverted
   redis-cli INCRBY "ticket_type_qty:<ticket_type_id>" <quantity>
   redis-cli SET "saga:reverted:order:<order_id>" "1" EX 604800  # arm SETNX guard
   ```
4. **Alternative — full rehydrate.** `booking-cli rehydrate` walks PG and rewrites Redis qty keys via SETNX. This is safe but slower and recovers inventory for ALL ticket types, not just the affected one.

**Why no automatic retry.** The compensator already retried via the saga consumer's retry budget. The persistent failure means Redis was unavailable for the full retry window. By the time this alert fires, the message has been DLQ'd (`order.failed.dlq`). The DLQ is the operator-review path; auto-retry would re-attempt against the same broken Redis and fail again.

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

## ConsumerGroupRecreated

**Symptom.** `increase(consumer_group_recreated_total[5m]) > 0`. Severity **critical**, fires immediately on the first occurrence (no soak). The worker's NOGROUP self-heal triggered, meaning the Redis Streams consumer group `orders:group` was destroyed and recreated. **Messages enqueued in the destruction → recreation window have been silently dropped** — the recreation uses `XGROUP CREATE ... $` which starts from the current end of stream.

**Why this is critical-severity (and `for: 0s`).** In healthy production this counter MUST stay 0. The consumer group is created at startup and Redis Streams never auto-deletes it. Any `> 0` means SOMETHING destroyed it — possible causes ranked by likelihood:

1. Operator ran `FLUSHALL` on production Redis (don't — `make reset-db` was fixed in PR #73 to use precise DEL specifically for this reason)
2. Operator ran `XGROUP DESTROY orders:stream orders:group`
3. Redis crashed without AOF (we have AOF off by design — see `docs/architectural_backlog.md` § Cache-truth architecture). The rehydrate path covers inventory state; in-flight stream messages have NO DB analog and ARE lost on a Redis crash.

**Diagnosis (in order):**

1. **Cross-check the booking funnel for missing orders.** Run in Prometheus:
   ```promql
   # Bookings that reached the API + got 202 in last 1h
   sum(increase(bookings_total{status="success"}[1h]))
   ```
   Then count DB orders created in the same window:
   ```sql
   SELECT COUNT(*) FROM orders WHERE created_at > NOW() - INTERVAL '1 hour';
   ```
   If `bookings_total - DB_orders_count > 0`, that's the silent-drop count. Under normal operation the two should match within seconds (worker drains the stream).

2. **Identify the trigger.** Check Redis logs:
   ```
   docker compose logs redis | grep -E "FLUSHALL|XGROUP|shutdown"
   ```
   And app logs around the alert time:
   ```
   docker compose logs --tail=500 app | grep -E "NOGROUP|ConsumerGroup"
   ```

3. **Confirm the recovery worked.** The worker should have logged a WARN line: `XReadGroup Error: NOGROUP. Attempting to recreate group — messages enqueued before recovery may have been silently skipped`. After that log, the worker should be back to normal `info` logs of order processing.

**Action:**

- **If silent-drop count > 0:** affected users have HTTP 202 responses with `order_id`s that aren't in the DB. They'll never get a `confirmed` or `failed` status — `GET /orders/:id` returns 404 indefinitely. Reach out via business channels OR provide a self-service retry path (out of scope for this runbook; product decision).
- **If silent-drop count == 0:** the NOGROUP fired but no messages were in flight at the moment. Acknowledge the alert and document the trigger so it doesn't recur (e.g., update operator runbook to never run FLUSHALL on prod).

**Escalation.** If silent-drop count is in 4+ digits OR the trigger is unclear (no FLUSHALL / XGROUP DESTROY in logs), escalate to whoever owns Redis infrastructure — there may be a Redis bug, network partition, or unauthorized access.

**Background.** This alert was added in PR-C of the cache-truth architecture roadmap. The 411-of-1000 silent-message-loss case that drove it is documented in `docs/architectural_backlog.md` § "Cache-truth architecture". The metric fires on **both** the main Subscribe loop AND the PEL recovery path (`processPending`), so this alert covers all entry points.

---

## InventoryDriftDetected

**Symptom.** `increase(inventory_drift_detected_total[5m]) > 0 for 5m`. Severity **warning**. The drift detector running in the `recon` process found at least one event with a ticket type whose Redis cache disagrees with the Postgres source-of-truth (`event_ticket_types.available_tickets`) beyond the configured `INVENTORY_DRIFT_ABSOLUTE_TOLERANCE` (default 100). The `direction` label tells you WHICH failure mode and routes you to the right diagnosis branch.

**Why this is warning-severity (not critical).** Drift between Redis and DB is RECOVERABLE — the rehydrate path (`cache.RehydrateInventory`) re-runs at app startup and SETNX-guards in-flight values, so a manual restart is the worst-case remediation. The risk is sustained drift left undetected: customers see "sold out" while DB still has inventory (`cache_low_excess`), or successful 202s with no DB row (`cache_high`). The `for: 5m` window discriminates "transient in-flight blip" (one busy moment crosses tolerance briefly) from "sustained corruption worth paging".

**Diagnosis (branches by `direction` label):**

### `direction="cache_missing"`

DB has inventory, Redis returns 0 (key absent or value zero). Means rehydrate didn't run or was incomplete.

1. **Check if the app actually rehydrated at boot.** Search the app logs for the `RehydrateInventory` line:
   ```
   docker compose logs app | grep -E "rehydrate|RehydrateInventory"
   ```
   You should see one line per available event at startup. If the count is 0 OR the rehydrate completed with `events_skipped > 0`, that's your culprit.

2. **Verify the affected event_id from the alert's logs.** The drift detector logs `component=inventory_drift` with the event_id at WARN level:
   ```
   docker compose logs recon | grep "drift: cache key absent"
   ```

3. **Force rehydrate via app restart.** `docker compose restart app` will re-run the OnStart rehydrate hook for all `available_tickets > 0` events.

### `direction="cache_high"`

Redis > DB. Anomalous regardless of magnitude — only saga compensation desync OR manual `SetInventory` produces this state.

1. **Did someone run `make reset-db` or manual SetInventory recently?** Check git log + operator's terminal history. The reset path was hardened in PR #73 (precise DEL, no FLUSHALL) but a manual `SET event:{uuid}:qty 999` would still trigger this.

2. **Check saga compensator outcomes.** `cache_high` after a flurry of saga compensations means revert.lua bumped Redis but DB didn't roll back the order. Search:
   ```promql
   rate(saga_watchdog_resolved_total{outcome="compensator_error"}[10m])
   ```
   If non-zero, the compensator is partially succeeding (Redis revert happens before the DB transition; failure between them leaves Redis ahead).

3. **Reconcile manually.** `UPDATE events SET available_tickets = <redis_qty> WHERE id = '<event_id>'` aligns DB to Redis IF the customer was charged and confirmed. **Verify by joining `orders` first** before any UPDATE — getting this wrong creates duplicate/missing inventory.

### `direction="cache_low_excess"`

Redis < DB by more than tolerance. Steady-state drift is positive (in-flight bookings between Lua deduct and worker DB-commit); excess means the worker is failing to commit OR the reconciler force-fail path is leaking inventory.

1. **Check worker consumer lag.** `worker_consumer_lag_seconds` p99 should be sub-second. A persistent lag > 60s means the worker is unable to keep up — the booking funnel is decrementing Redis but DB writes are queueing.
   ```promql
   histogram_quantile(0.99, rate(worker_processing_duration_seconds_bucket[5m]))
   ```

2. **Check recon force-fail rate.** The DEF-CRIT path (recon force-failing without emitting outbox) was supposedly closed in PR #45, but this counter is the canary for any regression:
   ```promql
   rate(recon_resolved_total{outcome="max_age_exceeded"}[15m])
   ```
   Sustained > 0 means recon is force-failing orders without saga compensation → Redis stays decremented but DB rolls forward to `failed` → exactly this drift pattern.

**Action (general):**

- **Single sweep flagged once and cleared:** acknowledge and move on. The detector's `for: 5m` window already filtered transients.
- **Persistent for > 30m:** start with the diagnosis branch matching the `direction` label. If unclear, dump current state for the affected ticket type:
  ```bash
  docker exec booking_redis redis-cli GET ticket_type_qty:<ticket-type-uuid>
  docker exec booking_db psql -U user -d booking -c \
    "SELECT id, event_id, available_tickets FROM event_ticket_types WHERE id = '<ticket-type-uuid>';"
  ```

**Escalation.** If drift exceeds 1000 tickets for any single event OR multiple events drift simultaneously across direction labels, that suggests systemic corruption (Redis or DB). Bring in whoever owns the storage layer.

**Background.** This alert was added in PR-D of the cache-truth architecture roadmap — the final piece of the 4-PR plan that started with PR-A (Makefile precise reset), PR-B (rehydrate-on-startup), PR-C (NOGROUP self-heal alert). See `docs/architectural_backlog.md` § "Cache-truth architecture" for the full sequence.

---

## KafkaConsumerStuck

**Symptom.** `kafka_consumer_retry_total` rate > 0 for a topic over 2m. Severity `warning`. Consumer is "stuck but not dead" — leaving messages uncommitted for rebalance retry.

**Post-D7 (2026-05-08) scope.** The only Kafka consumer in the system is the in-process **saga consumer** for `order.failed` (lives inside the `app` container; see `internal/infrastructure/messaging/saga_consumer.go`). Pre-D7 the legacy `payment_worker` was a second consumer for `order.created` and shared this alert; that binary is gone. The `kafka_consumer_retry_total` metric label set narrowed to `topic=order.failed` only.

**Diagnosis.** Check the downstream the saga consumer depends on:
- **Postgres** — saga compensator's UoW (`MarkCompensated` + `IncrementTicket`) — `pg_pool_*` saturation, `pg_stat_activity` long-running tx?
- **Redis** — `revert.lua` increments inventory + `saga:reverted:order:<id>` SETNX — Redis reachable, not OOM, not auth-rotated?
- **Kafka** — broker reachable from `app` container? Consumer-group rebalance storms?

**Action.** This alert intentionally does NOT trigger DLQ routing on transient errors (would silently mask a downstream outage and lose compensation events). Operator's job is to find and fix the downstream; once recovered, the saga consumer drains naturally. If the in-process consumer goroutine itself has died (panic), `app`'s `/livez` would still return 200 (process is up) but `/metrics` would show zero `saga_*` activity — restart `app` (`docker compose restart app`) as a last resort.

---

## OutboxPendingBacklog

**Symptom.** `outbox_pending_count > 100 for 5m`. Severity `warning`. The transactional outbox is the bridge between "DB committed an `order.failed` event" and "the saga consumer sees it" — when this fires the bridge is broken.

**Post-D7 scope.** Only `event_type='order.failed'` rows flow through the outbox now (D7 deleted the legacy `events_outbox(order.created)` write from the booking UoW). Three production emitters write `order.failed`: D5 webhook on `payment_failed`, D6 expiry sweeper on `expired`, recon's `failOrder` on stuck-charging force-fail (rare).

**Why this is warning, not critical.** Customers can still book + pay. The Redis hot path + the `/pay` synchronous handler are unaffected. What's broken is the post-commit fan-out from D5/D6/recon to the in-process saga consumer — failed-payment compensations stall, expired-reservation compensations stall, the `failed` orders sit in `failed` status instead of advancing to `compensated`. The customer-visible damage takes minutes to surface (a declined Stripe Elements confirmation that doesn't release inventory); page-worthy via Slack/email but not phone.

**Diagnosis (in order):**

1. **Is the relay container actually alive?** The relay runs inside the `app` container today (will be split out under k8s). Check:
   ```
   docker compose logs app | grep "outbox relay"
   docker compose ps app
   ```
   A healthy relay logs poll-cycle entries; a wedged one stops logging entirely.

2. **Is a zombie tx holding the advisory lock?** The relay uses `pg_try_advisory_lock(1001)` for leader election. A crashed or hung tx still holding the lock blocks every other replica from taking over:
   ```sql
   SELECT pid, usename, state, query, query_start
   FROM pg_locks l
   JOIN pg_stat_activity a USING (pid)
   WHERE l.locktype = 'advisory' AND l.objid = 1001;
   ```
   If you see a stuck PID with `state = 'idle in transaction'` for minutes — that's the culprit. `SELECT pg_terminate_backend(<pid>)` releases the lock; the next relay poll grabs it.

   **Verify `state` BEFORE terminating.** `pg_terminate_backend` cancels the session immediately with no rollback grace period. `idle in transaction` = safe to terminate (no in-flight DML; the lock is the only state). `active` = mid-query, in-flight DML will roll back — for the relay this is benign (its `UPDATE events_outbox SET processed_at = ...` rows just re-appear as pending and get re-processed on the next poll), but for any other backend that happens to hold a different advisory lock you'd be killing legitimate work. Always read `state` from the query above before pasting the PID.

3. **Is Kafka reachable?** Even if the relay is alive and holding the lock, broker unreachability blocks publish:
   ```
   docker compose logs app | grep -i "kafka.*error\|broker"
   docker compose exec kafka kafka-broker-api-versions --bootstrap-server localhost:9092
   ```

4. **Is `events_outbox` actually growing or just bouncing on the threshold?** Run:
   ```sql
   SELECT MIN(created_at), MAX(created_at), COUNT(*)
   FROM events_outbox WHERE processed_at IS NULL;
   ```
   `MAX - MIN` should be small (relay processes oldest-first). If `MIN` is hours old, the relay is wedged on a specific row.

**Action.**

- **Stuck advisory lock:** terminate the holder (above).
- **Stuck on a poison row:** find the `MIN(created_at)` row's id and inspect its payload. If malformed, manually `UPDATE events_outbox SET processed_at = NOW(), status = 'POISON_SKIPPED' WHERE id = '<id>'` and file a bug for whoever produced it.
- **Relay container hung:** `docker compose restart app` (or in k8s, `kubectl rollout restart deployment/app`).

**Escalation.** Critical if backlog > 1000 OR sustained > 30m — at that scale customer 202s are going to start visibly stalling and product / support need a heads-up.

**Background.** This alert was added as part of the cache-truth roadmap follow-up. The PR-D drift detector caught Redis↔DB inventory drift, but the outbox-relay-broken failure mode (saga compensator goes quiet → Redis inventory leaked → drift detector eventually fires `cache_low_excess`) was previously detectable only via downstream effects, minutes-to-hours after the relay actually broke. This alert closes that gap.

---

## OutboxPendingCollectorDown

**Symptom.** `rate(outbox_pending_collector_errors_total[5m]) > 0 for 2m`. Severity `critical`. Mirrors `RedisStreamCollectorDown` discipline: while this fires, `outbox_pending_count` is silent or stale, and `OutboxPendingBacklog` cannot fire — operators see "no backlog alert" and assume health when the collector is actually blind.

**Diagnosis.**

1. **Is the DB reachable?** `docker compose logs app | grep -i "postgres\|db.*error"` — a sustained connectivity failure produces this counter at high rate.
2. **Is the partial index on `events_outbox` present?** Migration 000007 added `CREATE INDEX ... ON events_outbox(id) WHERE processed_at IS NULL`. Without it, the COUNT query fans out to the full table and may exceed the 1s scrape budget under load:
   ```sql
   SELECT indexname FROM pg_indexes
   WHERE tablename = 'events_outbox' AND indexdef LIKE '%processed_at IS NULL%';
   ```
   If empty, re-run `make migrate-up`.
3. **Query timeout under load?** If the gauge collector is timing out specifically during high-load windows, `pg_pool_in_use` will be pinned. Cross-check `pg_pool_*` gauges.

**Action.**

- DB outage → fix the DB (separate ticket); alert clears within one scrape after recovery.
- Missing migration → `make migrate-up`. Migration 000007 uses `CREATE INDEX CONCURRENTLY` and is tagged `-- golang-migrate: no-transaction`, so it does NOT block writes when the migrate runner honours the pragma. **Verify your migrate toolchain handles the `no-transaction` pragma correctly before running against a live primary** — if not, drop in via the manual `psql` route outside a transaction window. The migration file itself documents the fall-back path.
- Query timeout → bump `outboxPendingScrapeBudget` (currently 1s) only after confirming the partial index exists. The right fix is the index, not the budget.

**Escalation.** Pages with `OutboxPendingCollectorDown` should always be cross-checked against `TargetDown` — if both fire, the entire scrape job is degraded, not just this collector.

---

## InventoryDriftListEventsErrors

**Symptom.** `rate(inventory_drift_list_events_errors_total[5m]) > 0 for 2m`. Severity `critical`. The drift detector's per-sweep `EventRepository.ListAvailable` query is failing — the detector is **blind on the DB side** until this clears. While this fires, `inventory_drifted_events_count` is held at 0 (intentional — see metric godoc) and `InventoryDriftDetected` cannot fire because there is nothing to detect.

**Diagnosis.**

1. **Is the DB reachable from the recon container?** `docker compose logs recon | grep -i "list events\|postgres"`. The drift detector logs `component=inventory_drift` at WARN/ERROR for these failures.
2. **Is the recon container's DB pool saturated?** Cross-check `pg_pool_in_use` — if pinned at the configured max, the COUNT/LIST queries are queued behind hot-path traffic.
3. **Is migration 000007's partial index missing?** Drift detector uses `ListAvailable` (full event scan), NOT the outbox partial index, so the `events` table indexes are what matter here. Verify `idx_events_available` etc. exist via `\d events`.

**Action.**

- DB outage: fix the DB (separate ticket); alert clears within one scrape after recovery.
- Pool saturated: bump `DB_MAX_OPEN_CONNS` for the recon process (it shares the same env as the API but a separate pool).
- Specific timeout pattern: check the `events` table's row count and existing indexes; ListAvailable should be fast on any healthy schema.

**Escalation.** Always pages alongside `TargetDown` if the DB outage is total — if both fire, focus on `TargetDown` first.

---

## InventoryDriftCacheReadErrors

**Symptom.** `rate(inventory_drift_cache_read_errors_total[5m]) > 0 for 5m`. Severity `warning`. Per-event Redis GET failures during drift sweeps. **Distinct from `InventoryDriftListEventsErrors`**: there the WHOLE sweep aborts; here the sweep continues with partial visibility (some events skipped, others checked). The structured log line `events_skipped` per sweep is the diagnostic field.

**Diagnosis.**

1. **Is the Redis client pool exhausted?** `redis_client_pool_timeouts_total` rate > 0 → bump `REDIS_POOL_SIZE` for the recon container.
2. **Is Redis CPU saturated?** `redis_cpu_*` rates close to 1.0 sustained → the booking hot path is competing for the same Redis with the drift detector. Either accept the partial visibility or shard.
3. **Specific keys timing out?** `redis_slowlog_length` > 0 + the affected event_ids in the `component=inventory_drift` log lines.

**Action.**

- Pool exhaustion: bump pool size; restart recon container.
- Redis CPU saturation: this alert is the SECOND-order signal; the booking hot path will be visibly slow first. Address that root cause.
- Specific slow keys: use the event_ids from logs to inspect Redis directly.

**Escalation.** Doesn't usually page on its own (warning severity). If it persists past one Redis restart cycle, escalate to whoever owns Redis infrastructure.

---

## SweepGoroutinePanic

**Symptom.** `increase(sweep_goroutine_panics_total[5m]) > 0 for 0s`. Severity `critical`. A periodic-sweeper goroutine (Reconciler / InventoryDriftDetector / SagaWatchdog) panicked and was rescued by the loop's `defer recover()`. Process is still running, but a deterministic bug surfaced.

**Why this is critical (no soak window).** In healthy production this counter MUST stay 0. Without this alert, a recovered panic would be invisible — process stays up, /metrics keeps serving, `up{}` stays 1, `TargetDown` does NOT fire. The panic itself is the architectural signal; it doesn't need to recur to be page-worthy.

**Diagnosis.**

1. **Pull the panic value + stack trace.** The recovery path logs at ERROR level with the panic value and tag.Error. Filter:
   ```
   docker compose logs recon | grep -A 30 "recovered from panic"
   ```
   The grep window of 30 lines should cover the stack trace zap printed alongside the recover error. If you don't see a stack, check `docker compose logs --tail=200 recon` for the runtime's stderr panic dump (it prints separately from zap when the goroutine is mid-frame).

2. **Identify which sweeper.** The alert's `sweeper` label is also in the log line's `error` field. `recon` / `inventory_drift` / `saga_watchdog` correspond to the loop sweepers; `once_recon` / `once_drift` correspond to the `--once` mode (k8s CronJob host).

3. **Reproduce locally.** The cause is by definition deterministic if it panicked once. Stand up the same DB / Redis state, run `booking-cli recon --once`, observe the panic.

**Action.**

- File a bug with the panic value, stack trace, and sweeper label. The recovery is automatic; this alert exists so the panic doesn't go unnoticed.
- Until fixed, the loop continues running but every Sweep with the same trigger will re-panic and re-bump the counter. If the panic is on a SPECIFIC event / order (not deterministic on every Sweep), the counter rises slowly; if it's on every Sweep, the counter rises at the sweep cadence rate.
- **Do NOT remove the recover()**. It is the safety net; without it the loop dies silently and you'd discover the problem hours later via stale gauges.

**Escalation.** Always page; this is a "we have a bug" signal, not a "dependency is down" signal.

**Background.** This alert was added as part of the cache-truth roadmap follow-up. The silent-failure-hunter agent flagged the loop-without-recover scenario: a panic in `runSweepLoop` would kill the goroutine entirely, the metrics listener would keep serving stale values, and operators would see "recon container is healthy" while every sweep had stopped firing. The fix is twofold: defer recover() in the loop body (so the loop survives) AND a dedicated counter (so the recover isn't itself silent).

---

## TargetDown

**Symptom.** Prometheus's `up` metric is `0` for some `{job, instance}` for 2m+. Severity `critical`. **This is the meta-alert that catches the silent-worker-death class** — every rate-based alert depending on metrics from the down target is now silently inert.

**Diagnosis.**
1. Identify the missing target: `up == 0` in Prometheus → note `{job, instance}`.
2. `docker ps --filter name=booking_<job>` → is the container running?
3. If yes: `docker logs booking_<job> --tail 100` — look for crash / panic / context-canceled signatures.
4. If no: the container died. `docker inspect` → exit code + reason.

**Action.**
- Container down → `docker compose up -d <service>`.
- Container up but unreachable → check `WORKER_METRICS_ADDR` env (the worker subcommands expose `/metrics` on `:9091` by default; empty disables, which is intentional only for k8s CronJob-hosted modes).
- Network split between Prometheus and the target → standard Docker network triage (`docker network inspect booking_monitor_default`).

**Escalation.** Page immediately. While this fires, you have NO visibility into the affected job's correctness alerts.

**Planned maintenance.** A `docker compose restart <service>` typically completes in <30s and won't fire TargetDown. A longer-than-2m planned outage (e.g. image rebuild, database migration that blocks worker startup) WILL fire. Silence proactively:

```bash
# Silence TargetDown for 10 minutes (override during planned downtime).
docker exec booking_alertmanager amtool silence add \
  alertname=TargetDown \
  --duration=10m \
  --comment="planned maintenance: $REASON"

# List active silences:
docker exec booking_alertmanager amtool silence query

# Expire a silence early:
docker exec booking_alertmanager amtool silence expire <silence-id>
```

**Partial-stack local development.** Running only a subset of services (e.g. `docker compose up app postgres redis kafka prometheus grafana` without the worker services) makes Prometheus's static scrape targets for `payment-worker` / `recon` / `saga-watchdog` perpetually fail → TargetDown fires for each. Either run the full stack (`docker compose up -d`) during alert-aware development, or silence TargetDown for the dev session:

```bash
docker exec booking_alertmanager amtool silence add \
  alertname=TargetDown \
  --duration=8h \
  --comment="dev: partial stack, workers intentionally absent"
```

---

## Forcing TargetDown to fire (testing)

Stop a worker container and wait 2m+:

```bash
docker stop booking_recon
# … wait 2m+ …
# Verify: open Prometheus UI → Alerts → TargetDown firing for job=recon
docker start booking_recon  # to recover
```

---

## PaymentWebhookSignatureFailing

**Symptom.** `payment_webhook_signature_invalid_total` rate > 0.1/s sustained 5m. Severity warning. Dashboard: *Payment Webhook* row → "Signature failures by reason" panel.

**Diagnosis.**
1. Look at the `reason` label split:
   - `mismatch` dominant → secret rotation drift (we hold the old secret while the provider signs with the new one, or vice versa)
   - `skew_exceeded` dominant → clock issue at the provider edge (or our pod)
   - `missing` / `malformed` dominant → probing or misrouted traffic, NOT a real provider
2. Compare with the most recent `PAYMENT_WEBHOOK_SECRET` rotation timestamp (deploy log, k8s secret history).
3. Loki query: `{component="payment_webhook_handler"} |= "signature invalid"` — examines the rejected `Stripe-Signature` headers for forensic detail.

**Action.**
- `mismatch` after a recent rotation → finish the rotation (deploy the new secret to all nodes). If the rotation is recent, expect the alert to clear within the deploy window.
- `mismatch` with no rotation → potential incident; check provider dashboard for unauthorised webhook endpoints or compromised api keys, rotate immediately.
- `skew_exceeded` → check NTP sync on app pods (`timedatectl status` inside container); raise `PAYMENT_WEBHOOK_REPLAY_TOLERANCE` only as a last resort (a wide tolerance is a replay-attack window).
- `missing` / `malformed` → likely benign probing; tighten ingress CIDR allowlist if persistent.

**Escalation.** Page on-call only if `mismatch` rate > 1/s OR sustained > 30m without operator response — by then the provider is auto-disabling the endpoint per their retry policy.

---

## PaymentWebhookUnknownIntentSurging

**Symptom.** `payment_webhook_unknown_intent_total{reason="not_found"}` rate > 0.05/s sustained 5m. Severity critical. Every count is potentially "user paid but has no resolved order".

**Diagnosis.**
1. The most likely cause is the `SetPaymentIntentID` race in `/pay`: gateway intent created successfully, but the DB write that persists `payment_intent_id` failed → webhook arrives with no metadata or matching intent_id row. App log: `CreatePaymentIntent: SetPaymentIntentID failed AFTER gateway succeeded`.
2. Cross-check provider dashboard: filter PaymentIntents by status=`succeeded` AND `metadata.order_id` set AND no matching `orders.payment_intent_id` row in our DB.
3. Loki: `{component="payment_webhook"} |= "unknown intent — neither metadata nor lookup matched"` — extracts the `intent_id` for each orphan.

**Action.**
- For each orphan: read `metadata.order_id` from the provider's intent → query `SELECT * FROM orders WHERE id = $1`. If the order is still in `awaiting_payment`, manually `UPDATE orders SET payment_intent_id = $1 WHERE id = $2` so the next webhook redelivery resolves cleanly.
- If `/pay` Error logs are spiking concurrent with this alert, treat as a DB outage incident — fix root cause before backfilling intent ids.
- After resolving, the provider will retry the webhook (we returned 500); the row will resolve on retry naturally.

**Escalation.** Critical — page on-call immediately. Money has moved without a paid order in our DB; the customer experience is "paid but no ticket". Resolution is manual + per-event.

---

## PaymentWebhookLateSuccessAfterExpiry

**Symptom.** `payment_webhook_late_success_total` increase > 0 in last 5m. Severity critical. Single-event paging — every count is one ticket needing a manual provider-side refund.

**Diagnosis.**
1. Loki: `{component="payment_webhook"} |= "late success on expired reservation — refund required"` — extracts `order_id`, `intent_id`, `reserved_until`, and the `detected_at` label (`service_check` vs `sql_predicate`).
2. Confirm the order is now in `expired` status (the handler walked it there + emitted `order.failed` for saga to revert inventory): `SELECT id, status FROM orders WHERE id = $ORDER_ID`.
3. Check the `detected_at` label split on the dashboard panel:
   - `service_check` dominant → reservation TTL is too short for the median Stripe Elements latency; consider raising `BOOKING_RESERVATION_WINDOW`
   - `sql_predicate` dominant → narrow race between service-level check and the UoW; usually ms-level and fine

**Action.**
- For each occurrence, initiate a refund on the provider side. Stripe: `POST /v1/refunds {payment_intent: <intent_id>}`; mock provider: no-op (refund is provider-side).
- Add an audit-log note on the order: `INSERT INTO order_status_history (order_id, from_status, to_status, note) VALUES ($1, 'expired', 'refunded_by_ops', 'late_success after expiry — manual refund issued')` (replace `note` column name with whatever you've added; D5 doesn't add a column).
- If the rate is sustained (multiple per hour), raise `BOOKING_RESERVATION_WINDOW` until the typical `detected_at=service_check` rate falls back to 0.

**Escalation.** Critical — single-event paging. Customer-impacting and money-touching; never auto-resolve.

---

## PaymentWebhookIntentMismatch

**Symptom.** `payment_webhook_intent_mismatch_total` increase > 0 in last 5m. Severity critical. Single-event paging.

**Diagnosis.**
1. Loki: `{component="payment_webhook"} |= "intent id mismatch on metadata-resolved order"` — extracts `envelope_id`, `order_id`, `order_intent_id`, and `webhook_intent_id`.
2. The webhook handler refused to flip the order. Three plausible causes:
   - **Forged metadata** — attacker discovered our webhook secret AND knows a valid order id. Treat as security incident; rotate `PAYMENT_WEBHOOK_SECRET` immediately.
   - **Test fixture leak** — a dev/integration env's webhook envelope drifted into prod (cross-env routing bug). Check provider dashboard's webhook delivery log against the envelope_id; if it came from a non-prod endpoint, fix the routing config.
   - **Provider bug** — vanishingly rare but possible. Open a provider support ticket with the envelope id.
3. Verify the order's persisted `payment_intent_id` against the provider dashboard's actual intent for that order: `SELECT payment_intent_id FROM orders WHERE id = $ORDER_ID`. If it matches an actual provider intent that the customer paid against, the webhook arrived for a DIFFERENT intent — confirms forgery / fixture leak.

**Action.**
- DO NOT auto-resolve. The handler already refused to flip; a human must investigate.
- If forgery confirmed → rotate `PAYMENT_WEBHOOK_SECRET`, audit other recent webhooks for the same envelope shape.
- If fixture leak confirmed → fix routing config; the webhook will not retry (we returned 500 but the upstream issue is no longer reproducible).
- Document in incident log; this should be vanishingly rare in steady state.

**Escalation.** Critical — single-event paging. Potential security incident; loop in security on-call.

---

## ExpiryOldestOverdueAge

**Symptom.** `expiry_oldest_overdue_age_seconds > 300` sustained 5m. Severity warning. Dashboard: *Reservation Expiry* row → "Oldest overdue age" panel.

**Diagnosis.**
1. Cross-check `ExpiryProcessingErrors` first — if it's also firing, the sweeper IS running but per-row resolves are erroring; that's the root cause and this alert is a downstream symptom.
2. `expiry_backlog_after_sweep` — if non-zero in steady state, BatchSize is too small for current eligibility rate (the sweeper drains 100 per tick by default; if 200+ rows hit `reserved_until` per 30s, backlog grows).
3. Dashboard: *Reservation Expiry* → "Backlog vs sweep cadence" panel. Sweep duration (`expiry_sweep_duration_seconds`) approaching SweepInterval = sweeper-time-bound, NOT batch-bound.
4. `up{job="expiry-sweeper"} == 0` would surface as `TargetDown` first; if that's not firing, the sweeper IS being scraped.

**Action.**
- If `ExpiryProcessingErrors` is the upstream cause → triage that alert's runbook first.
- If pure throughput problem → bump `EXPIRY_BATCH_SIZE` (default 100) to 1000+ transiently. Sweeper picks up the new value on its next pod restart.
- If sweeper has been off for a long time and is now catching up → expect the gauge to drain over `(backlog × SweepInterval / BatchSize)`. At default cadence: 12k overdue rows drains in 1h.
- For sustained high traffic, lower `EXPIRY_SWEEP_INTERVAL` from 30s to 15s (more frequent ticks, smaller batches) — but check Postgres index scan cost first.

**Escalation.** Warning. Critical only if backlog is growing AND `ExpiryProcessingErrors` / `ExpiryFindErrors` is also firing (compound failure mode).

---

## ExpiryProcessingErrors

**Symptom.** Per-row resolve failure rate > 0/s sustained 5m. Severity warning. The label split distinguishes the runbook:

- `getbyid_error` → DB read trouble after the find query
- `outbox_error` / `transition_error` → DB write trouble inside the UoW
- `marshal_error` → code regression (`json.Marshal(OrderFailedEvent)` failed; theoretical for fixed-shape struct today)

**Diagnosis.**
1. Loki: `{component="expiry_sweeper"} |= "UoW failed"` — extracts per-row Order ID + the underlying SQL error.
2. `db_pool_wait_count` — sustained pool exhaustion produces transient `transition_error`s under load.
3. `pg_locks` for advisory lock 2001 (rehydrate) or row-level locks on `orders` — saga compensator running concurrently can serialise on the same rows.
4. `outbox_pending_collector_errors_total` — independent signal that the outbox layer is unhealthy in general.

**Action.**
- `getbyid_error` dominant → check Postgres connection pool + replica lag (we read from the primary, but a misconfigured pool could still throttle).
- `outbox_error` / `transition_error` dominant → likely lock contention with the saga consumer or saga watchdog. Stagger sweep cadences (`EXPIRY_SWEEP_INTERVAL` 30s vs `SAGA_WATCHDOG_INTERVAL` 60s already mostly does this).
- `marshal_error` > 0 → CODE BUG. Roll back the most recent deploy; file an incident.
- All branches retry next sweep automatically. The `expiry_oldest_overdue_age_seconds` gauge is the SLO signal — if it's stable while errors are firing, the sweeper is at the noise floor.

**Escalation.** Warning. Critical if `marshal_error` > 0 (code regression) OR if the gauge climbs alongside the error rate.

---

## ExpiryFindErrors

**Symptom.** `expiry_find_expired_errors_total` rate > 0/s sustained 2m. Severity critical. Covers BOTH the find query (`FindExpiredReservations`) AND the post-sweep count query (`CountOverdueAfterCutoff`) failures — the operator response is identical (DB blind).

**Critical caveat.** When this fires, `expiry_oldest_overdue_age_seconds` and `expiry_backlog_after_sweep` are **held at last-known-good** (NOT zeroed). Reading those gauges during this alert is misleading — they pre-date the failure.

**Diagnosis.**
1. Postgres up? `up{job=~"postgres-exporter"} == 0` would also fire; if not, Postgres process is alive but the query specifically is failing.
2. Migration 000012 partial index `idx_orders_awaiting_payment_reserved_until` exists? `\d orders` in psql — if missing, the find query likely Seq Scan'd into a timeout.
3. Connection pool: `db_pool_wait_count` against `expiry_sweeper` — sustained pool exhaustion under load presents as Find timeouts.
4. Loki: `{component="expiry_sweeper"} |= "find expired"` OR `|= "count overdue"` — the WARN log includes the underlying SQL error.

**Action.**
- Missing index → re-run `make migrate-up`; if the migration tooling is wedged, the manual `CREATE INDEX` statement is in `deploy/postgres/migrations/000012_pattern_a_schema.up.sql:104-106`.
- Postgres health → standard DB triage; expiry sweeper recovers automatically once the query succeeds.
- Sustained > 30m with no DB-side cause → check sweeper's connection limit / pool config in `internal/infrastructure/persistence/postgres`.

**Escalation.** Critical — DB blind affects backlog accounting. Page on-call.

---

## ExpiryMaxAgeExceeded

**Symptom.** `expiry_max_age_total` increment > 0 in last 1h. Severity critical. **Single-event paging**, but the alert is informational, not remedial — D6 has already expired the row by the time the alert fires.

**Why this is informational, not actionable on the row.** D6 plan v4 §B (round-1 P1 contract): D6 always expires eligible rows; MaxAge is a labeling/alerting threshold only. The transition has fired, the saga compensator is reverting Redis, the row's audit trail in `order_status_history` shows `awaiting_payment → expired`. The question this alert asks is **"why did the row sit overdue for > 24h before the sweeper caught it?"** — the answer drives operational improvements, not row repair.

**Diagnosis.**
1. Sweeper uptime: `count_over_time(up{job="expiry-sweeper"}[24h])` — was the sweeper running for the past day? Gaps point at deploys, container crashes, k8s CronJob mis-fires.
2. `kubectl get pods -l app=expiry-sweeper` (or `docker ps | grep expiry`) — is the sweeper currently alive?
3. `expiry_oldest_overdue_age_seconds` history — did it climb gradually (sustained throughput problem) or spike suddenly (sweeper outage)?
4. Loki: `{component="expiry_sweeper"} |= "exited"` — last clean shutdown vs OOMKill.

**Action.**
- Confirm the row IS now `expired` (`SELECT status FROM orders WHERE id = $ORDER_ID`).
- Confirm saga compensator picked it up (`SELECT status FROM orders WHERE id = $ORDER_ID` should eventually become `compensated`).
- Identify the sweeper outage cause; file an SRE ticket if not obvious.
- If a fleet-wide deploy stuck the sweeper for 24h+, verify NO other long-overdue rows are in the pipeline (`expiry_backlog_after_sweep`).

**Escalation.** Critical informational — page so the operational gap gets visibility, but the row itself doesn't need direct intervention.

---

## D7 cutover note — `order.created` outbox drain

D7 (2026-05-08) deleted the legacy `order.created` Kafka topic + `payment_worker` consumer. Before deploying the post-D7 binary in any environment with live traffic, check that the outbox relay has drained any pending `order.created` rows.

**Why this matters.** If the outbox relay has accumulated `events_outbox` rows with `event_type='order.created'` (i.e. it fell behind right before the D7 deploy), the post-D7 relay will still publish those rows once each to a Kafka topic that no longer has a consumer. Not a correctness risk — the rows get marked `processed_at = NOW()` and stop firing — but it produces transient observability noise (Kafka topic gets writes, no consumer commits offsets, DLQ stays empty).

**Pre-deploy check.**

```bash
docker exec booking_db psql -U booking -d booking -c \
  "SELECT COUNT(*) FROM events_outbox
   WHERE processed_at IS NULL AND event_type = 'order.created';"
```

If the count is 0, deploy is safe. If non-zero:

1. **Drain first** (recommended for production) — wait for the relay to finish, re-check.
2. **Mark-as-processed at deploy time** (faster, dev-only): `UPDATE events_outbox SET processed_at = NOW() WHERE processed_at IS NULL AND event_type = 'order.created'` immediately before / after the binary swap. Skips the dead-topic publishes entirely.

Local-dev / smoke environments can ignore this; the topic-as-sink behavior is benign.
