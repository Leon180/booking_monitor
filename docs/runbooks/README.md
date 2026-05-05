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

### Cache layer (perf optimisations — fail-soft)
- [TicketTypeCacheRedisErrors](#tickettypecacherediserrors)
- [TicketTypeCacheMarshalErrors](#tickettypecachemarshalerrors)

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

### Meta (scrape health)
- [TargetDown](#targetdown)

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

## IdempotencyCacheGetErrors

**Symptom.** `idempotency_cache_get_errors_total` rate > 0 for 1m. Severity `warning`. **Page-worthy** — duplicate-charge protection is suspended for affected requests.

**Diagnosis.** Redis health: `PING`, AUTH, AOF state.

**Action.** Restore Redis. The handler fails open during the outage (DB UNIQUE constraints provide last-resort protection), but the contract degrades for the duration.

**Escalation.** Page immediately if sustained — financial-correctness control is offline.

---

## TicketTypeCacheRedisErrors

**Symptom.** `cache_errors_total{cache="ticket_type",op=~"get|set"}` rate > 0 for 5m+. Severity `warning`. **NOT page-worthy at the warning threshold** — the cache is a perf optimisation, not a correctness control. Booking endpoint stays up via Postgres fallback; the +38% sold-out fast-path RPS / -21% p95 gain from PR #90 is what's silently disabled.

**Diagnosis.** This alert disambiguates "Redis down / degraded" from "cache cold". Cross-check:
- `redis_up{job="redis"}` / `RedisExporterCannotReachRedis` — is Redis itself reachable?
- `redis_client_pool_timeouts` / `redis_client_pool_misses` — is OUR client starving?
- `redis_used_memory_bytes` vs `maxmemory` — OOM eviction storms can cause `op="set"` failures specifically
- `IdempotencyCacheGetErrors` — typically fires under the same incident; if it doesn't, the cause is more localised
- `RedisXAddFailures` / `RedisXAckFailures` — same Redis, different code paths; pattern of which fire helps localise

**Action.** Restore Redis health. The cache is fail-soft so booking continues during the incident; the only impact is paying the post-D4.1 PG round-trip cost on every booking attempt (`http_req_duration` p95 will track back toward the no-cache baseline of ~28ms while the alert is active). No special operator action against the cache itself — once Redis recovers, write-through fills entries lazily on the next miss for each ticket_type.

**Escalation.** Page if sustained > 30m AND `RedisXAddFailures` is also firing — that combination means the booking hot path is approaching the Lua single-thread limit at ~5ms per attempt (vs the cache's ~1ms hit cost) AND the `accepted_bookings/s` ceiling will start dropping. Otherwise warning + on-call awareness is sufficient; the gain this PR added is recoverable via simple Redis recovery.

**Why severity = warning, not critical.** A docker-compose Redis restart or k8s pod rolling-redeploy bursts `op="get"` errors for the duration of disconnect. A 1m / critical alert would page on routine ops. The 5m soak absorbs routine restarts while still catching real outages within the SLO window.

---

## TicketTypeCacheMarshalErrors

**Symptom.** `cache_errors_total{cache="ticket_type",op="marshal"}` rate > 0 for 1m+. Severity `warning`.

**This is NOT a Redis incident.** Marshal of the fixed-shape `ticketTypeCacheEntry` JSON is a deterministic operation; non-zero rate means a code regression introduced an unmarshalable type into the cache entry struct. The booking endpoint stays up (cache write is skipped on marshal failure, the inner result still returns), but the cache layer is permanently disabled until a fix lands.

**Diagnosis.** Recent changes to:
- `internal/infrastructure/cache/ticket_type_cache.go::ticketTypeCacheEntry` (the cache DTO)
- `internal/infrastructure/cache/ticket_type_cache.go::ticketTypeCacheEntryFromDomain` (the domain → DTO mapper)
- The aggregate accessors on `domain.TicketType` if a new type was added

Pull recent commits touching these paths; the offending struct field will surface in the marshal stack trace.

**Action.** **File a bug; do NOT page Redis on-call.** The fix is a code change (revert the bad field type or fix its JSON tags). Until the fix lands, the cache layer is bypassed → total RPS regresses ~28% and p95 climbs by ~6ms on the sold-out fast path.

**Escalation.** This alert's existence is its own escalation signal. If the rate sustains > 1h, raise the severity manually.

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

**Symptom.** `increase(inventory_drift_detected_total[5m]) > 0 for 5m`. Severity **warning**. The drift detector running in the `recon` process found at least one event whose Redis cache disagrees with the Postgres source-of-truth (`events.available_tickets`) beyond the configured `INVENTORY_DRIFT_ABSOLUTE_TOLERANCE` (default 100). The `direction` label tells you WHICH failure mode and routes you to the right diagnosis branch.

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
- **Persistent for > 30m:** start with the diagnosis branch matching the `direction` label. If unclear, dump current state for the affected event:
  ```bash
  docker exec booking_redis redis-cli GET event:<uuid>:qty
  docker exec booking_db psql -U user -d booking -c \
    "SELECT id, available_tickets FROM events WHERE id = '<uuid>';"
  ```

**Escalation.** If drift exceeds 1000 tickets for any single event OR multiple events drift simultaneously across direction labels, that suggests systemic corruption (Redis or DB). Bring in whoever owns the storage layer.

**Background.** This alert was added in PR-D of the cache-truth architecture roadmap — the final piece of the 4-PR plan that started with PR-A (Makefile precise reset), PR-B (rehydrate-on-startup), PR-C (NOGROUP self-heal alert). See `docs/architectural_backlog.md` § "Cache-truth architecture" for the full sequence.

---

## KafkaConsumerStuck

**Symptom.** `kafka_consumer_retry_total` rate > 0 for a topic over 2m. Severity `warning`. Consumer is "stuck but not dead" — leaving messages uncommitted for rebalance retry.

**Diagnosis.** Check the downstream the consumer depends on: payment gateway? DB? Redis?

**Action.** This alert intentionally does NOT trigger DLQ routing on transient errors (would cause overselling during DB hiccups). Operator's job is to find and fix the downstream. Once recovered, the consumer drains naturally.

---

## OutboxPendingBacklog

**Symptom.** `outbox_pending_count > 100 for 5m`. Severity `warning`. The transactional outbox is the bridge between "DB committed an `order.created` / `order.failed` event" and "Kafka downstream consumers see it" — when this fires the bridge is broken.

**Why this is warning, not critical.** Customers can still book. The Redis hot path is unaffected. What's broken is the post-commit fan-out — saga compensator stops compensating, payment service stops processing, in-flight orders cannot advance from `processing` to `confirmed` / `failed`. The customer-visible damage takes minutes to surface (a 202 with no progress on `GET /orders/:id`); page-worthy via Slack/email but not phone.

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
docker stop booking_payment_worker
# … wait 2m+ …
# Verify: open Prometheus UI → Alerts → TargetDown firing for job=payment-worker
docker start booking_payment_worker  # to recover
```
