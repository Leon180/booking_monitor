# Monitoring Guide

> 中文版本: [monitoring.zh-TW.md](monitoring.zh-TW.md)

This guide is the day-to-day operator's reference for **how to observe `booking_monitor` while the stack is running**. It is *not* an architecture spec (see [PROJECT_SPEC.md](PROJECT_SPEC.md) for that) — it answers concrete questions like "is the system healthy right now", "where is this metric defined", and "how do I make this alert fire on purpose for testing".

The three observability surfaces are:

| Surface | URL | What it gives you |
| :-- | :-- | :-- |
| **Raw `/metrics`** | http://localhost:80/metrics (via nginx) or http://localhost:8080/metrics (direct) | Prometheus exposition format. Cheap, scriptable, useful for `grep` / `curl` sanity checks. |
| **Prometheus UI** | http://localhost:9090 | Ad-hoc PromQL exploration, target health, alert state. |
| **Grafana** | http://localhost:3000 (login `admin` / `admin`) | Pre-provisioned dashboards. The "is anything red right now" surface. |

`/livez` and `/readyz` are also available at http://localhost:80/livez and `/readyz` for Kubernetes-style health probes — see [PROJECT_SPEC §6](PROJECT_SPEC.md) for the protocol contract.

---

## 1. Quick health check (60-second loop)

When you want to know "is the system healthy right now":

```bash
# 1. Is the app process alive?
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:80/livez            # → 200

# 2. Are all dependencies reachable from the app?
curl -s http://localhost:80/readyz | jq                                       # → status: ok

# 3. Are there active alerts?
curl -s http://localhost:9090/api/v1/alerts | jq '.data.alerts[] | {name: .labels.alertname, state, value}'

# 4. Is the booking hot-path producing traffic + succeeding?
curl -s http://localhost:80/metrics | grep -E '^bookings_total\{' | head

# 5. Trace one specific order from id → terminal status (PR #47)
ORDER_ID="<paste from POST /book response>"
curl -s "http://localhost:80/api/v1/orders/$ORDER_ID" | jq
# → {"id":"...", "status":"confirmed", ...}
# 404 during the brief async-processing window — retry with backoff.
```

If any of those five returns something unexpected, drop into the relevant deeper layer below.

---

## 2. Metric inventory

The authoritative source is `internal/infrastructure/observability/metrics.go` plus the two collectors (`db_pool_collector.go`, `streams_collector.go`). The full annotated list is in [PROJECT_SPEC §7](PROJECT_SPEC.md). Below is a pragmatic grouping by *what question you would ask*.

### Per-request — RED method (Rate / Errors / Duration)

| Question | Metric | Example query |
| :-- | :-- | :-- |
| How much traffic? | `http_requests_total{method,path,status}` | `sum by (status) (rate(http_requests_total[1m]))` |
| How fast? | `http_request_duration_seconds_bucket` | `histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket[5m])) by (le))` |
| What % failed? | same, filter `status=~"5.."` | `sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))` |

### Per-resource — USE method (Utilization / Saturation / Errors)

| Resource | Metric prefix | Notes |
| :-- | :-- | :-- |
| Go runtime | `go_*`, `process_*` | goroutines, GC pause, heap inuse — registered via `collectors.NewGoCollector` |
| DB pool | `db_pool_*` | `db_pool_in_use`, `db_pool_idle`, `db_pool_wait_count`, `db_pool_wait_duration_seconds` |
| Redis cache (Go-client view) | `cache_hits_total{cache}`, `cache_misses_total{cache}` | per-cache-name hit/miss; what *our app* sees |
| Redis streams | `redis_stream_length{stream}`, `redis_stream_pending_entries{stream,group}`, `redis_stream_consumer_lag_seconds{stream,group}` | scraped at request time by `StreamsCollector` |
| Redis server (oliver006 exporter, scraped from `redis_exporter:9121`) | `redis_*` | The "is Redis itself saturated" view that our app-side metrics can't answer. See sub-table below. |

**Redis-server-side metrics (from `redis_exporter`)** — fills the gap that our Go-client-side `cache_*` and `redis_stream_*` metrics can't answer. Useful when triaging "why is the booking hot path slow":

| Question | Metric | Example query |
| :-- | :-- | :-- |
| Is Redis main thread CPU-saturated? | `redis_cpu_sys_seconds_total`, `redis_cpu_user_seconds_total` | `rate(redis_cpu_sys_seconds_total[1m]) + rate(redis_cpu_user_seconds_total[1m])` (close to 1.0 sustained → saturated) |
| Which command is consuming Redis CPU? | `redis_commands_duration_seconds_total{cmd}` | `topk(5, rate(redis_commands_duration_seconds_total[5m]))` |
| Which command is hot by call rate? | `redis_commands_total{cmd}` | `topk(5, rate(redis_commands_total[5m]))` |
| Lua eval latency specifically | filter `cmd=~"eval.*"` on the duration metric | `rate(redis_commands_duration_seconds_total{cmd=~"eval.*"}[5m]) / rate(redis_commands_total{cmd=~"eval.*"}[5m])` (avg μs per call) |
| SLOWLOG length / last slow exec | `redis_slowlog_length`, `redis_last_slow_execution_duration_seconds` | `redis_slowlog_length` non-zero for sustained > 30s = something is consistently slow |
| Memory pressure | `redis_memory_used_bytes`, `redis_memory_max_bytes`, `redis_mem_fragmentation_ratio` | fragmentation > 1.5 sustained → consider `MEMORY PURGE` or restart |
| Connection back-pressure | `redis_connected_clients`, `redis_blocked_clients` | `redis_blocked_clients > 0` for sustained 1m → some client is on `BLPOP`/`XREAD` block |
| Is the exporter itself healthy? | `up{job="redis"}` | `up{job="redis"} == 0` for sustained 1m → exporter or Redis itself unreachable |

The exporter polls Redis `INFO` + `commandstats` + `SLOWLOG` per scrape. Each scrape is a single round-trip, so it does not meaningfully load Redis (verified via `redis_exporter`'s own self-metrics). For the full metric list see the [oliver006/redis_exporter README](https://github.com/oliver006/redis_exporter#whats-exported).

### Domain-specific — what the business cares about

| Question | Metric |
| :-- | :-- |
| Successful bookings vs sold-out vs duplicate | `bookings_total{status}` |
| Worker outcomes | `worker_orders_total{status}`, `worker_processing_duration_seconds` |
| Inventory drift between Redis and DB | `inventory_conflicts_total` |
| Dead-letter routing | `dlq_messages_total{topic,reason}`, `redis_dlq_routed_total{reason}` |
| Saga compensation poison messages | `saga_poison_messages_total` |
| Kafka consumer stuck on transient downstream failures | `kafka_consumer_retry_total{topic,reason}` |
| Charging stuck-order reconciliation | `recon_stuck_charging_orders` (gauge), `recon_resolved_total{outcome}`, `recon_gateway_errors_total`, `recon_resolve_duration_seconds`, `recon_resolve_age_seconds` |
| Streams collector itself failing | `redis_stream_collector_errors_total{stream,operation}` |
| What happens when a client retries with the same Idempotency-Key (N4) | `idempotency_replays_total{outcome}` — counts every replay attempt by what the server did with it. Three outcomes:<br>• `match` = same key + same body → we replay the previously-cached response (this is idempotency working as designed)<br>• `mismatch` = same key + **different** body → we return 409 Conflict (client bug: they're reusing one key across logically-distinct requests)<br>• `legacy_match` = entry was already in cache from before N4 shipped (no fingerprint stored), so we replay AND lazily write the fresh fingerprint back. **Should taper to 0 within 24h after deploy** (legacy entries expire); if it stays > 0, something is still writing the old wire format — investigate. |
| How often the idempotency Redis lookup is failing (N4) | `idempotency_cache_get_errors_total` — number of times the idempotency cache GET failed (Redis unreachable, unmarshal error). **Page-worthy.** When this is sustained > 0, the booking endpoint is still accepting requests but **idempotency protection is OFF** — a duplicate request will be processed twice (only the DB UNIQUE constraint catches it at the last layer).<br>**Why we don't just reject requests when Redis is down**: refusing all bookings during a Redis outage = the whole endpoint is down. "Idempotency briefly disabled" is strictly better than "endpoint disabled"; this counter exists so on-call knows we're in that degraded mode.<br>Alert: `rate(idempotency_cache_get_errors_total[5m]) > 0 for 1m` → page. |
| How many orders are stuck in Failed state without compensation (A5) | `saga_stuck_failed_orders` — gauge of orders that have been in Failed state longer than `SAGA_STUCK_THRESHOLD` (default 60s), set by the saga watchdog on every sweep. A non-zero value at any single moment is fine (the watchdog will re-drive the compensator next sweep). **Sustained > 0 for 10m means the compensator is failing repeatedly** — typical causes: Redis revert blocked, DB lock contention, or an unmapped compensator error. Watchdog default sweep interval is 60s, so 10m gives ~10 attempts to clear before paging. |
| Saga watchdog resolution outcomes (A5) | `saga_watchdog_resolved_total{outcome}` — counts what happened on each watchdog re-drive. **Six outcomes**, each pointing at a distinct runbook so triage starts at the right subsystem:<br>• `compensated` = watchdog successfully re-drove the compensator (Failed → Compensated)<br>• `already_compensated` = race won by the saga consumer between FindStuckFailed and re-drive (benign, no action needed)<br>• `max_age_exceeded` = order older than `SAGA_MAX_FAILED_AGE` (default 24h); watchdog logs + alerts but does NOT auto-transition (unsafe to MarkCompensated without verifying inventory was reverted) — operator investigates manually<br>• `getbyid_error` = `orderRepo.GetByID` failed before reaching the compensator. Operator checks **DB** health, NOT Redis or compensator code<br>• `marshal_error` = `json.Marshal` of synthesized OrderFailedEvent failed. Theoretical for the fixed-shape struct today; isolated label so a future regression is observable<br>• `compensator_error` = `compensator.HandleOrderFailed` returned an error. Operator checks **Redis revert path + DB lock contention**. Will retry next sweep |
| Saga watchdog DB query failing (A5) | `saga_watchdog_find_stuck_errors_total` — `FindStuckFailed` SQL query failed (DB outage, missing migration 000011 partial index, query timeout). **Page-worthy critical**: when this is non-zero, the watchdog can't see stuck orders at all — `saga_stuck_failed_orders` gauge goes stale and looks "healthy" but is actually blind. Companion alert fires within 2 minutes. |
| Page funnel | `page_views_total{page}` |

### When labels are pre-initialized

The codebase pre-warms expected label combinations at startup so dashboards don't show "no data" for a metric that genuinely had zero events. You'll see initial rows like `bookings_total{status="success"} 0` on a freshly-started stack. This is intentional — see [observability/metrics.go](../internal/infrastructure/observability/metrics.go) prelude.

---

## 3. Prometheus UI workflow

Open http://localhost:9090.

**Three navs you'll use:**

| Nav | Why |
| :-- | :-- |
| **Graph** | Type a PromQL query, hit Execute, switch to Graph tab. The fastest "is X happening" surface. |
| **Status → Targets** | Verify the app process is being scraped (`UP` next to `app:8080/metrics`). Red here = scrape failing = every other metric is stale. |
| **Status → Alerts** | All defined alerts plus their current state (Inactive / Pending / Firing). |

**Useful queries to bookmark:**

```promql
# Booking funnel — successes vs sold-out vs duplicate vs error
sum by (status) (rate(bookings_total[1m]))

# Worker throughput
sum by (status) (rate(worker_orders_total[1m]))

# p99 latency by route
histogram_quantile(0.99,
  sum by (le, path) (rate(http_request_duration_seconds_bucket[5m]))
)

# DB pool saturation — sustained > 0 means workers are queued waiting for a connection
db_pool_wait_count

# Stream backlog — should drain to ~0 on a healthy worker
redis_stream_length{stream="orders:stream"}

# Stuck-charging reconciler — sustained > 0 = gateway degraded or recon falling behind
recon_stuck_charging_orders
```

---

## 4. Grafana workflow

Open http://localhost:3000, login `admin` / `admin`. **Dashboards → Browse → Booking Monitor Dashboard**.

Pre-provisioned panels live in [deploy/grafana/provisioning/dashboards/dashboard.json](../deploy/grafana/provisioning/dashboards/dashboard.json). Provisioning is **read-only** from the UI — changes you make in the browser do not persist across `docker compose down`. To make a permanent change, edit the JSON file and restart Grafana.

Panels are organised by collapsible row. Top-of-dashboard "golden signals" first; reliability / infrastructure rows below.

**Golden signals (top of dashboard):**
- Request Rate (RPS) — split by method/path/status
- Global Request Latency (p99 / p95 / p50)
- Conversion Rate (%) — `bookings_total{status="success"}` / `page_views_total{page="event_detail"}`
- Saturation — Goroutines
- Saturation — Memory Alloc Bytes

**Row: Reliability — Recon (A4 charging two-phase intent log)**
- Recon resolved by outcome (rate, 5m) — `charged` / `declined` / `not_found` / `unknown` / `max_age_exceeded` / `transition_lost`
- Recon stuck-charging gauge — `recon_stuck_charging_orders` (point-in-time)
- Recon resolve duration p95/p50 — `recon_resolve_duration_seconds_bucket`
- Recon error rates — find-stuck (DB) / gateway probe / mark (DB+outbox)

**Row: Reliability — Saga Watchdog (A5)**
- Saga watchdog resolved by outcome (rate, 5m) — `compensated` / `already_compensated` / `max_age_exceeded` / `getbyid_error` / `marshal_error` / `compensator_error`
- Saga stuck-failed gauge — `saga_stuck_failed_orders`
- Saga watchdog resolve duration p95/p50
- Saga watchdog find-stuck error rate + saga poison messages

**Row: Dead Letter Queue activity**
- Kafka DLQ messages by topic + reason — `dlq_messages_total`
- Redis DLQ routed by reason — `redis_dlq_routed_total`
- Kafka consumer retry rate — `kafka_consumer_retry_total` (silent-retry surface)

**Row: Database — pool (USE) + correctness signals**
- PG pool: in-use vs idle — `pg_pool_in_use` / `pg_pool_idle`
- PG pool wait rate + wait time + rollback failures — `pg_pool_wait_count_total` / `pg_pool_wait_duration_seconds_total` / `db_rollback_failures_total`

**Row: Cache — idempotency (N4)**
- Idempotency cache hit rate (%) — `cache_hits_total{cache="idempotency"}` / (hits + misses)
- Idempotency cache GET errors — `idempotency_cache_get_errors_total` (page-worthy: rate >0 sustained 1m = idempotency protection suspended)
- Idempotency replay outcomes — `match` / `mismatch` / `legacy_match`

**Row: Redis — stream / DLQ infra failures**
- Stream/DLQ failure rates — `redis_xack_failures_total` / `redis_xadd_failures_total{stream}` / `redis_revert_failures_total`
- Stream collector errors by stream + operation — `redis_stream_collector_errors_total`

**Row: Meta — scrape health (TargetDown)**
- Scrape target up/down — `up` per `{job, instance}` (1 = healthy, 0 = down). Pairs with the `TargetDown` alert; sustained zero means rate-based alerts depending on that job are silently inert.

**Second provisioned dashboard: Redis Exporter**

[deploy/grafana/provisioning/dashboards/redis-exporter.json](../deploy/grafana/provisioning/dashboards/redis-exporter.json) — Grafana community dashboard `#763` (oliver006's reference dashboard, vendored locally so the stack works offline). Visit it at **Dashboards → Browse → Redis Exporter (oliver006/redis_exporter)**.

Panels cover the metric families documented in §2's Redis-server-side sub-table: per-command rate + duration, CPU split (sys vs user), memory + fragmentation, connected/blocked clients, hit rate, expired/evicted keys, network I/O. Use this dashboard alongside the main Booking Monitor dashboard when triaging "is the Redis hot path itself the bottleneck?". The two dashboards are deliberately separate: the main one is the *application* view (RED + USE per app resource), this one is the *infrastructure* view (Redis-internal counters).

**To add a new panel quickly (non-persistent — for exploration only):**
1. Click **+ → Create dashboard → Add visualization**.
2. Pick the **Prometheus** data source.
3. Paste a PromQL query from §3 and adjust the visualization type.
4. If the panel is worth keeping, copy the panel JSON and merge it into `dashboard.json` so it survives the next `down/up`.

---

## 5. Alerts

Alert definitions live in [deploy/prometheus/alerts.yml](../deploy/prometheus/alerts.yml). State is visible in the Prometheus UI → **Alerts** AND in the Alertmanager UI at http://localhost:9093 (silence / inhibit / notification log).

**Alertmanager wiring (CP6, default delivery updated 2026-05-02).** Prometheus pushes firing alerts to Alertmanager (config: [deploy/alertmanager/alertmanager.yml](../deploy/alertmanager/alertmanager.yml)). Alertmanager handles dedup, grouping by `alertname + severity`, severity-specific cadences (critical: 30 m repeat, warning: 4 h, info: 24 h), and inhibition (e.g. `RedisStreamCollectorDown` suppresses every other stream-backlog alert because the gauges are stale anyway).

**Default delivery (since 2026-05-02): a webhook-logger sidecar.** Alerts post to `booking_alert_logger` (a `mendhak/http-https-echo` container) which prints the payload to stdout. Operators see fired alerts via `docker logs booking_alert_logger -f`. This replaces the prior `null` receiver default which the senior-review checkpoint flagged as an operability gap (alerts never actually left Alertmanager). Slack delivery remains opt-in: copy `deploy/alertmanager/alertmanager.slack.yml.example` over `alertmanager.yml`, paste your Incoming Webhook URL into `api_url`, and `docker compose restart alertmanager`.

**Runbook annotations (CP5).** Every alert carries a `runbook_url` annotation pointing at a section in [docs/runbooks/README.md](runbooks/README.md). Alertmanager renders the URL into Slack notifications via the template in `alertmanager.yml`. Operator workflow: alert fires → notification arrives → click runbook → matching dashboard panel + concrete remediation steps in one document.

The current alert catalog:

| Alert | Severity | Symptom |
| :-- | :-- | :-- |
| `HighErrorRate` | critical | 5xx rate > 5% over 5m |
| `HighLatency` | warning | p99 > 2s for 2m+ (5m rate window — anti-flap; was 1m/1m before Phase 2 cleanup) |
| `InventorySoldOut` | info | A booking attempt returned sold_out |
| `OrdersStreamBacklogYellow` | info | Stream length > 10K for 2m |
| `OrdersStreamBacklogOrange` | warning | Stream length > 50K for 2m |
| `OrdersStreamBacklogRed` | critical | Stream length > 200K for 1m |
| `OrdersStreamConsumerLag` | warning | Oldest pending entry > 60s for 2m |
| `OrdersDLQNonEmpty` | warning | DLQ has unreviewed entries for 5m |
| `RedisStreamCollectorDown` | critical | Streams scrape errors → other stream alerts go silent |
| `ReconStuckCharging` | warning | `recon_stuck_charging_orders` > 0 for 5m |
| `ReconFindStuckErrors` | critical | Reconciler sweep query is failing |
| `ReconGatewayErrors` | warning | Reconciler gateway error rate elevated |
| `ReconMaxAgeExceeded` | critical | Reconciler force-failed an order — manual review |
| `ReconMarkErrors` | warning | `recon_mark_errors_total` rate > 0 for 5m — DB transitions failing during resolve |
| `SagaStuckFailedOrders` | warning | `saga_stuck_failed_orders > 0 for 10m` — compensator failing repeatedly |
| `SagaCompensatorErrors` | warning | `rate(saga_watchdog_resolved_total{outcome="compensator_error"}[5m]) > 0 for 2m` — fast-path companion; catches a 100%-failing compensator before the gauge alert's 10m window elapses |
| `SagaWatchdogFindStuckErrors` | critical | Watchdog sweep query failing — gauge goes blind |
| `SagaMaxFailedAgeExceeded` | critical | Stuck Failed orders >24h — manual review needed (watchdog will NOT auto-transition) |
| `KafkaConsumerStuck` | warning | Consumer rebalance retries — downstream dependency degraded |
| `IdempotencyCacheGetErrors` | warning | `idempotency_cache_get_errors_total` rate > 0 for 1m — duplicate-charge protection suspended |
| `DBRollbackFailures` | warning | `db_rollback_failures_total` rate > 0 for 5m — UoW rollback failing (driver / connection-state bug) |
| `RedisXAckFailures` | warning | `redis_xack_failures_total` rate > 0 for 5m — PEL grows unbounded; consumers redo work on rebalance |
| `RedisRevertFailures` | warning | `redis_revert_failures_total` rate > 0 for 5m — saga compensation failing to revert Redis inventory |
| `RedisXAddFailures` | warning | `redis_xadd_failures_total` rate > 0 for 5m — booking hot path intermittently failing to enqueue |
| `TargetDown` | critical | `up == 0` for any (job, instance) for 2m+ — meta-alert; rate-based alerts for that job are silently inert until scrape recovers |
| `RedisExporterCannotReachRedis` | critical | `redis_up{job="redis"} == 0` for 1m — exporter HTTP listener is alive (so `TargetDown` won't fire) but it can't reach Redis itself. Typical causes: REDIS_PASSWORD wrong/rotated, Redis container down, docker network split. Every Redis-server metric on the dashboard goes stale until this clears. |

> **Worker-process metric scrape — closed by O3 follow-up.** The `recon_*`, `saga_watchdog_*`, `kafka_consumer_retry_total`, and saga `db_*` / `redis_*` failure counters are registered inside the `booking-cli {recon,saga-watchdog,payment}` worker processes' default Prometheus registries. Each of those binaries now starts a metrics-only HTTP listener on `:9091` (configurable via `WORKER_METRICS_ADDR`; empty disables — useful for `--once` CronJob hosting), and `prometheus.yml` has matching scrape jobs (`payment-worker`, `recon`, `saga-watchdog`). Verify with `up{job=~"payment-worker|recon|saga-watchdog"} == 1` in Prometheus → Graph; the listener also exposes `/healthz` so the compose `HEALTHCHECK` can use the same port. The new `saga_watchdog` compose service runs the watchdog in default-loop mode — `--once` mode is reserved for k8s CronJob hosting where the cluster scheduler drives cadence.

### Forcing an alert to fire (testing)

To verify the alert plumbing end-to-end, push enough volume into the watched metric to cross the threshold + outlast the `for:` window. Examples:

```bash
# OrdersStreamBacklogYellow — push > 10K entries onto orders:stream
docker exec booking_redis redis-cli eval \
  "for i=1,11000 do redis.call('XADD','orders:stream','*','probe',i) end return 1" 0
# Wait 2-3 minutes (alert has `for: 2m`), check Prometheus → Alerts.

# OrdersDLQNonEmpty — push one entry onto the DLQ
docker exec booking_redis redis-cli XADD orders:dlq '*' probe 1
# Wait 5+ minutes (alert has `for: 5m`).

# RedisStreamCollectorDown — kill Redis briefly
docker compose stop redis
# After 2m the alert fires; `docker compose start redis` clears it within one scrape.

# SagaStuckFailedOrders — backdate a Failed order so it crosses SAGA_STUCK_THRESHOLD.
# Direct UPDATE rather than waiting for the natural Failed→Compensated stall, since
# the saga consumer would otherwise compensate within ms.
docker exec booking_db psql -U user -d booking -c \
  "UPDATE orders SET status='failed', updated_at = NOW() - INTERVAL '5 minutes' WHERE id = '<some-existing-order-uuid>';"
# Watchdog default sweep is 60s + alert `for: 10m`, so wait ~11m and check Prometheus → Alerts.
# Cleanup: UPDATE the row back to its prior status, OR let the watchdog re-drive the compensator
# (it will move Failed → Compensated since the row has no actual reverted-Redis-key tracked).

# TargetDown — stop a worker, wait 2m+, watch Prometheus → Alerts → TargetDown firing.
docker stop booking_payment_worker
# Wait 2m+ (alert has `for: 2m`).
# Cleanup: docker start booking_payment_worker → up returns to 1 within one scrape (15s).
```

After testing, undo: `docker exec booking_redis redis-cli DEL orders:stream orders:dlq` (loses any in-flight production data — only safe in dev).

---

## 6. The pragmatic loop

For day-to-day senior-level usage:

1. **Grafana** — "is anything red right now?"
2. **Prometheus** — "let me write a query to investigate"
3. **Raw `/metrics`** — "is this metric being emitted at all?" (PR-time sanity check)
4. **App logs** (`docker compose logs -f app`) — for context + correlation IDs that the metric alone can't give you

Logs and metrics are linked: every structured log line includes `correlation_id` + (when sampled) `trace_id`/`span_id`. From a Grafana spike you can grab the timestamp, search Jaeger (http://localhost:16686) for traces in that window, and pull the matching `correlation_id` to grep app logs. See [internal/log/](../internal/log/) for the wiring.

---

## 7. When to update this guide

This document is **paired** with the source-of-truth observability code. Any change to the surfaces below requires updating this guide (and its zh-TW counterpart):

| Surface | File(s) |
| :-- | :-- |
| Metric registration | [internal/infrastructure/observability/metrics.go](../internal/infrastructure/observability/metrics.go) |
| Custom collectors | `internal/infrastructure/observability/*_collector.go` |
| Alert rules | [deploy/prometheus/alerts.yml](../deploy/prometheus/alerts.yml) |
| Prometheus scrape config | [deploy/prometheus/prometheus.yml](../deploy/prometheus/prometheus.yml) |
| Grafana dashboards | `deploy/grafana/provisioning/dashboards/*.json` |

A PostToolUse hook ([.claude/hooks/check_monitoring_docs.sh](../.claude/hooks/check_monitoring_docs.sh)) fires whenever any of these files are edited; it injects a reminder so Claude updates this guide before ending the turn.

If you cannot translate, ask the human author rather than skipping the zh-TW update — structural parity matters more than perfect prose.
