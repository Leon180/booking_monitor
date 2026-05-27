# Observability Completeness Review

**Reviewer**: Observability Audit (Claude Opus, project audit 2026-05-27)
**Scope**: Metrics, traces, logs, dashboards, alerts, SLO
**Anchored to**: Stage 5 dashboard fixes (commits `28f934e`, `1cb00cf`)
**Codebase tag**: post-v1.0.0, branch `fix/grafana-datasource-uid`

## TL;DR

The observability surface is unusually thorough for a single-developer learning project: ~80 named series, RED at the HTTP layer, USE on both PG and Redis client pools (plus Redis-server-side via `redis_exporter`), a 50+ alert catalog with `runbook_url` annotations on every rule, an Alertmanager wired with severity-specific cadences, and per-flow span instrumentation. Histogram buckets are deliberately calibrated per metric (no copy-pasted defaults). Cardinality discipline is strong — every `WithLabelValues` interpolates a closed enum, never an id. Pre-warming of label combinations is explicit and well-documented. Where the project falls short of "industry production grade" is **(a)** no SLI/SLO document and no burn-rate alerts — every alert is symptom-OR-cause threshold based, none are multi-window error-budget; **(b)** trace sampling is head-based at 1% with no errors-always-sampled exception, so the 99% of error spans get silently dropped at the SDK before Jaeger sees them; **(c)** the main `dashboard.json` uses the legacy `"datasource": "Prometheus"` (string), inconsistent with `admin_war_room.json`'s pinned `uid: "PROMETHEUS"` — the Stage 5 fix `1cb00cf` was applied to only one of three dashboards. The recent `28f934e` fix swapping `bookings_total` for `admin_event_bus_published_total{event_type="order.created"}` on the war-room booking-rate panel is a **leaky abstraction** — see [MAJOR-4]. None of the findings are correctness-critical; they are gaps between "monitored" and "interpretable under an incident at 02:00."

---

## 1. RED / USE coverage matrix

### RED per public endpoint

| Endpoint / Worker                  | Rate                                                       | Errors                                                          | Duration                                                                                          | Notes                                                                                              |
| ---                                | ---                                                        | ---                                                             | ---                                                                                               | ---                                                                                                |
| `POST /api/v1/book`                | `http_requests_total{path,status}` + `bookings_total{status}` | `bookings_total{status="error"\|"sold_out"}` + 5xx via path/status | `http_request_duration_seconds_bucket{path}`                                                  | RED-complete. Booking outcome split is sharper than pure HTTP status.                              |
| `GET /api/v1/orders/:id`           | `http_requests_total`                                      | via `status=~"4..\|5.."`                                          | `http_request_duration_seconds_bucket`                                                            | Generic only; no per-handler success-vs-404 split (404 is normal during async window).             |
| `POST /api/v1/orders/:id/pay`      | `http_requests_total` + `stripe_api_calls_total{op="create_payment_intent"}` | `stripe_api_calls_total{outcome!="success"}`                    | `http_request_duration_seconds_bucket` + `stripe_api_duration_seconds`                            | Best-instrumented endpoint. Per-outcome Stripe classification is exemplary.                        |
| `POST /webhook/payment`            | `payment_webhook_received_total{result}`                   | `payment_webhook_signature_invalid_total{reason}` + `unknown_intent_total` + `intent_mismatch_total` + `late_success_total` | NOT instrumented (no histogram)                                                                   | Counters-only. Latency tracking absent — provider-side timeout concerns are blind.                 |
| `POST /api/v1/events`              | `http_requests_total`                                      | via status code                                                 | `http_request_duration_seconds_bucket`                                                            | RED via generic path only.                                                                         |
| `GET /api/v1/events/:id`           | `http_requests_total` + `page_views_total{page="event_detail"}`  | via status                                                      | `http_request_duration_seconds_bucket`                                                            | Plus business funnel signal.                                                                       |
| `GET /api/v1/history`              | `http_requests_total`                                      | via status                                                      | `http_request_duration_seconds_bucket`                                                            | Standard.                                                                                          |
| `/livez`, `/readyz`                | `http_requests_total`                                      | via status                                                      | `http_request_duration_seconds_bucket`                                                            | Health probes — RED is fine, no specialised metrics needed.                                        |
| `GET /api/v1/admin/events/stream` (SSE) | `admin_sse_active_connections` (gauge) + `admin_sse_reconnects_total` | `admin_sse_clients_dropped_total{reason}`                       | `admin_sse_connection_duration_seconds` + `admin_sse_message_lag_seconds` + `admin_sse_write_message_duration_seconds` | Best-of-class. 4 distinct histograms for different timing concerns.                                |

### RED per async worker

| Worker / Subsystem            | Rate                                                          | Errors                                                                  | Duration                                                                       | Notes                                                                                            |
| ---                           | ---                                                           | ---                                                                     | ---                                                                            | ---                                                                                              |
| Order stream consumer (worker) | `worker_orders_total{status}`                                 | `worker_orders_total{status=~"db_error\|malformed_message"}` + `redis_xack_failures_total` + `redis_revert_failures_total` + `dlq_messages_total` + `kafka_consumer_retry_total` | `worker_processing_duration_seconds`                                           | Solid.                                                                                           |
| Outbox relay                  | `outbox_pending_count` (gauge)                                | `outbox_pending_collector_errors_total`                                 | NOT instrumented (no per-batch histogram)                                      | **Gap**: no per-tick latency, no per-batch size histogram. Wedged-relay can only be inferred from gauge slope. |
| Saga compensator (Kafka hot path) | `saga_compensator_events_processed_total{outcome}`            | 13 distinct error-class labels                                          | `saga_compensation_loop_duration_seconds` (end-to-end) + `saga_compensation_consumer_lag_seconds` | Exemplary; "outcome" cardinality is well-bounded and runbook-mapped.                             |
| Saga watchdog (DB sweeper)    | `saga_stuck_failed_orders` (gauge) + `saga_watchdog_resolved_total{outcome}` | `saga_watchdog_find_stuck_errors_total` + `outcome=~".*_error"` labels  | `saga_watchdog_resolve_duration_seconds` + `saga_watchdog_resolve_age_seconds` | Symmetric to recon. Two histograms (operation vs row-age) is good practice.                      |
| Reconciler                    | `recon_stuck_charging_orders` (gauge) + `recon_resolved_total{outcome}` | `recon_find_stuck_errors_total` + `recon_gateway_errors_total` + `recon_mark_errors_total` + `recon_null_intent_id_skipped_total` | `recon_resolve_duration_seconds` + `recon_resolve_age_seconds` + `recon_gateway_get_status_duration_seconds` | Best-instrumented sweeper. 3 distinct latency histograms.                                        |
| Expiry sweeper                | `expiry_sweep_resolved_total{outcome}` + `expiry_backlog_after_sweep` (gauge) + `expiry_oldest_overdue_age_seconds` (gauge) | `expiry_find_expired_errors_total` + `outcome=~".*_error"` labels       | `expiry_resolve_duration_seconds` (per-row) + `expiry_sweep_duration_seconds` (full sweep) + `expiry_resolve_age_seconds` | Distinct per-row vs full-sweep histograms is the gold standard.                                  |
| Inventory drift detector      | `inventory_drifted_events_count` (gauge) + `inventory_drift_detected_total{direction}` | `inventory_drift_list_events_errors_total` + `inventory_drift_cache_read_errors_total` + `auto_rehydrate_errors_total` | `inventory_drift_sweep_duration_seconds`                                       | Solid.                                                                                           |
| Streams collector (scrape-time) | (Prometheus scrape interval is the rate)                      | `redis_stream_collector_errors_total{stream,operation}`                 | implicit via scrape latency                                                    | Scrape-time collector pattern — no separate histogram needed.                                    |
| Admin event bus drainer       | `admin_event_bus_published_total{event_type}` + `admin_event_bus_dropped_total{reason}` | `admin_event_bus_xadd_failures_total{reason}`                           | `admin_event_bus_xadd_duration_seconds`                                        | Complete.                                                                                        |
| Stage 5 Kafka intake          | (counter via bookings_total upstream)                         | `stage5_kafka_publish_failures_total{reverted}` + `stage5_intake_revert_failures_total` | NOT instrumented (no per-publish histogram)                                    | Gap: no publish-latency histogram on the durable-intake path; means tuning `STAGE5_PUBLISH_TIMEOUT` is by-feel. |

### USE per dependency

| Dependency                | Utilization                                              | Saturation                                                        | Errors                                                                          | Notes                                                                                           |
| ---                       | ---                                                      | ---                                                               | ---                                                                             | ---                                                                                             |
| PG connection pool        | `pg_pool_in_use`, `pg_pool_idle`, `pg_pool_open_connections` | `pg_pool_wait_count_total`, `pg_pool_wait_duration_seconds_total` | `db_rollback_failures_total`, `pg_pool_max_idle_closed_total`, `pg_pool_max_lifetime_closed_total` | Complete USE. Counters distinguish "transient closes" from "real errors".                       |
| PG (server-side)          | Not scraped (no pg_exporter)                             | Not scraped                                                       | Not scraped                                                                     | **Gap**: no `pg_stat_statements`, no per-query latency, no replication lag. Adequate for single-node dev; deficit for prod-grade. |
| Redis client pool         | `redis_client_pool_total_conns`, `_idle_conns`, `_stale_conns` | `redis_client_pool_wait_count_total`, `_wait_duration_seconds_total`, `_misses_total` | `redis_client_pool_timeouts_total`, `_unusable_total`                           | Complete USE. Page-worthy `timeouts` is a hard-saturation signal — uncommon-but-correct.        |
| Redis server (via redis_exporter) | `redis_cpu_*`, `redis_commands_total{cmd}`, `redis_memory_used_bytes` | `redis_blocked_clients`, `redis_slowlog_length`, `redis_mem_fragmentation_ratio` | `redis_up` (binary health)                                                      | Complete USE.                                                                                   |
| Redis Streams (app view)  | `redis_stream_length{stream}`                            | `redis_stream_pending_entries{stream,group}`, `redis_stream_consumer_lag_seconds` | `redis_stream_collector_errors_total`, `redis_xack_failures_total`, `redis_xadd_failures_total`, `consumer_group_recreated_total` | Excellent. The collector + alert pair closes the silent-failure gap.                            |
| Kafka producer            | NOT instrumented                                         | NOT instrumented                                                  | `stage5_kafka_publish_failures_total{reverted}` (Stage 5 only)                  | **Gap**: no per-broker / per-topic produce-latency histogram or in-flight gauge. Outbox-relay publish failures go to a logs-only path. |
| Kafka consumer (saga)     | `saga_compensation_consumer_lag_seconds` (gauge)         | `kafka_consumer_retry_total{topic,reason}`                        | DLQ counters via `dlq_messages_total`                                           | Adequate; no broker-side scrape (no Kafka exporter wired).                                      |
| Jaeger                    | Scraped at `:14269`                                      | (relies on Jaeger's own metrics, not surfaced in alerts)          | Not alerted on                                                                  | Scraped but no alert rule defined for Jaeger backlog or dropped spans (mentioned as intent in `prometheus.yml`). |
| OS / Go runtime           | `go_gc_duration_seconds`, `go_goroutines`, `go_sched_latencies_seconds`, `go_memstats_*`, `go_cpu_classes_*` | `go_sched_pauses_total_*`                                          | `process_*` family                                                              | `RegisterRuntimeMetrics` opts into the EXTENDED Go runtime set — well above default. Excellent. |

---

## 2. Cardinality audit

I audited every `WithLabelValues(...)` call. None interpolate an unbounded id. The discipline is uniform:

| Call site                                                                                       | Labels passed                       | Cardinality bound | Risk |
| ---                                                                                             | ---                                 | --- | ---  |
| [`middleware/metrics.go:61`](../../internal/infrastructure/api/middleware/metrics.go) `httpRequestsTotal.WithLabelValues(c.Request.Method, c.FullPath(), status)` | HTTP method + Gin route pattern + status code | ~7 × ~10 × ~10 ≈ 700                                                                       | None — `c.FullPath()` returns the route template (`/api/v1/orders/:id`), not the raw URL with id baked in. Confirmed via Gin docs. |
| [`booking_metrics.go:16`](../../internal/infrastructure/observability/booking_metrics.go) `BookingsTotal.WithLabelValues(status)` | Closed enum (`success`/`sold_out`/`duplicate`/`error`) | 4 | None |
| [`worker_metrics.go:17`](../../internal/infrastructure/observability/worker_metrics.go) `WorkerOrdersTotal.WithLabelValues(status)` | Closed enum (5 values) | 5 | None |
| [`queue_metrics.go:17`](../../internal/infrastructure/observability/queue_metrics.go) `RedisXAddFailuresTotal.WithLabelValues(stream)` | Stream name (closed set: `orders:stream`, `dlq`) | ~3 | None |
| [`queue_metrics.go:25`](../../internal/infrastructure/observability/queue_metrics.go) `RedisDLQRoutedTotal.WithLabelValues(reason)` | Closed enum (5 values) | 5 | None |
| [`sweeper_adapters.go:76,125,172,205,247`](../../internal/bootstrap/sweeper_adapters.go) | Closed-enum outcome strings; all sweepers pre-warm via `metrics_init.go` | ≤16 per sweeper | None |
| [`webhook_adapter.go:25,28,31,37,40,54,57`](../../internal/bootstrap/webhook_adapter.go) | Closed enums (`result`, `priorStatus`, `reason`, `detectedAt`, `eventType`) | ~5 each | **One soft risk**: `PaymentWebhookUnsupportedTypeTotal.WithLabelValues(eventType)` — `eventType` comes from the webhook payload. A malicious or buggy provider sending novel `event_type` strings would expand cardinality. Today bounded by Stripe's published event-type catalogue (~50). Mitigation: add a `safeEventTypeLabel` collapse like the SSE handler already does (see `safeEventTypeLabel` in [`sse/handler.go:307`](../../internal/infrastructure/api/sse/handler.go)). |
| [`payment_provider.go:52,56`](../../internal/bootstrap/payment_provider.go) `StripeAPI*.WithLabelValues(op, outcome)` | `op` ∈ {create_payment_intent, get_status}; `outcome` ∈ 5 sentinels | 10 | None |
| [`saga_consumer.go:242,274`](../../internal/infrastructure/messaging/saga_consumer.go) | Topic (closed) + reason (closed) | bounded | None |
| [`cache/instrumented_idempotency.go:68,70`](../../internal/infrastructure/cache/instrumented_idempotency.go) | cache name (closed) | 1 | None |
| [`middleware/idempotency.go:229,242,253`](../../internal/infrastructure/api/middleware/idempotency.go) | outcome (closed enum: 3 values) | 3 | None |
| [`sse/hub.go:230`](../../internal/infrastructure/api/sse/hub.go) + [`handler.go:146,307`](../../internal/infrastructure/api/sse/handler.go) | Reason (closed) + event_type (collapsed via `safeEventTypeLabel`) | bounded | None — the `safeEventTypeLabel` collapse to a closed enum + `unknown` is the canonical pattern. |
| [`admin_event_bus.go:131,133,212`](../../internal/infrastructure/cache/admin_event_bus.go) | event_type (closed enum of 8 admin event constants) + reason (closed) | bounded | None |
| [`booking/handler.go:292`](../../internal/infrastructure/api/booking/handler.go) `PageViewsTotal.WithLabelValues("event_detail")` | string literal | 1 | None — but `[]string{"page"}` label is a placeholder for future pages. Acceptable. |

**Pre-warm discipline** ([`metrics_init.go`](../../internal/infrastructure/observability/metrics_init.go)): every label combination that the producer can emit is enumerated explicitly at startup so PromQL alerts like `rate(...{outcome="..."}[5m]) > 0` evaluate to 0, not "no data". This is unusual for a hobby-scale project and is a published best practice for production-grade systems (CNCF blog 2025 on label naming).

**One real bug**: [`metrics_init.go:44`](../../internal/infrastructure/observability/metrics_init.go) pre-warms `BookingsTotal.WithLabelValues("duplicate")`, but [`service_metrics.go:25-42`](../../internal/application/booking/service_metrics.go) never emits `duplicate` — the comment says "ErrUserAlreadyBought is now only returned by the worker". The label always reads 0. Minor — see [MINOR-3].

---

## 3. Histogram bucket audit

Per-metric review. Calibration is impressively bespoke; almost no defaults left in place.

| Histogram | Declared buckets | Expected p50/p99 | Fit |
| --- | --- | --- | --- |
| `http_request_duration_seconds` ([`middleware/metrics.go:38`](../../internal/infrastructure/api/middleware/metrics.go)) | `{.005, .01, .025, .05, .1, .25, .5, 1, 2.5}` | k6 baseline p99 ≈ 100-300 ms intake-only, longer for full-flow | **Good fit at the median, weak at the tail.** Buckets stop at 2.5s — anything above lands in `+Inf` and degrades p99 resolution during incidents. Recommend extending: `{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}` so timeouts have a home. |
| `worker_processing_duration_seconds` ([`metrics_worker.go:28`](../../internal/infrastructure/observability/metrics_worker.go)) | `{.005, .01, .025, .05, .1, .25, .5, 1, 2.5}` | Worker does DB INSERT + outbox + ACK; expect 10-50ms typical | **Same gap** as above — no incident-tail visibility. |
| `recon_resolve_duration_seconds` | `{.005..10}` (11 buckets) | Gateway probe + DB transition; expect 50-500ms | Good. |
| `recon_resolve_age_seconds` | `{30..86400}` (30s..24h) | Per documentation, RECON_CHARGING_THRESHOLD=30s | Excellent — purpose-fit for threshold tuning. |
| `recon_gateway_get_status_duration_seconds` | `{.001..10}` (12 buckets) | Stripe GetStatus typical 50-100ms | Good. |
| `saga_watchdog_resolve_duration_seconds` | `{.005..10}` | DB + Redis revert; expect 10-100ms | Good. |
| `saga_watchdog_resolve_age_seconds` | `{30..86400}` | SAGA_STUCK_THRESHOLD=60s | Good. |
| `saga_compensation_loop_duration_seconds` ([`metrics_saga.go:227`](../../internal/infrastructure/observability/metrics_saga.go)) | `{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}` | Outbox→Kafka→consume→commit; can degrade to minutes in incidents | **Exemplary — explicit incident-scale bucketing to 300s.** Author's docstring explicitly references the "incident-scale durations need visibility" reasoning. Use this shape as the template for the others. |
| `expiry_resolve_duration_seconds` | `{.005..10}` | Per-row DB tx | Good. |
| `expiry_sweep_duration_seconds` ([`metrics_expiry.go:150`](../../internal/infrastructure/observability/metrics_expiry.go)) | `{.05..120}` | Full sweep including batch | Good. |
| `expiry_resolve_age_seconds` | `{1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 21600, 86400}` | SLO signal; expected p95 = sweep interval + grace ≈ 35s | Good. |
| `inventory_drift_sweep_duration_seconds` | `{.001..10}` | 1ms/event × ~1k events | Good. |
| `stripe_api_duration_seconds` ([`metrics_stripe.go:84`](../../internal/infrastructure/observability/metrics_stripe.go)) | `ExponentialBuckets(0.01, 2, 14)` = 10ms..81.92s | Stripe p99 ~89ms, timeout 30s | **Exemplary — author explicitly extended to cover the 30s timeout tail (slice 2b LOW #1)**. Documented decision in code. |
| `admin_sse_connection_duration_seconds` | `{1..18000}` (1s..5h) | Ops sessions can be all-day | Good. |
| `admin_sse_message_lag_seconds` | `{.001..5}` | Healthy <100ms; degraded 1-5s | Good. |
| `admin_sse_write_message_duration_seconds` | `{.0001..0.5}` | TCP write | Good. |
| `admin_event_bus_xadd_duration_seconds` | `{.0001..2}` | XAddTimeout cap 2s | Good. |

**Native histograms (Prometheus 2.40+, stable as of v3.8.0)** are not adopted. For this scale (single instance, ~80 series, no remote_write to long-term store) classic histograms are correct — native histograms add operational complexity (scraper config, billing implications for Grafana Cloud, mixed-format gotchas) without proportionate payoff. See [Prometheus Native Histograms in Production](https://www.michal-drozd.com/en/blog/prometheus-native-histograms-production/). Note this in `docs/architectural_backlog.md` rather than refactor now.

---

## 4. Trace sampling + semconv audit

### Sampling

[`cmd/booking-cli/tracer.go:62-78`](../../cmd/booking-cli/tracer.go) implements **head-based** `TraceIDRatioBased(0.01)` via `OTEL_TRACES_SAMPLER_RATIO`. Defaults to `AlwaysSample` when unset; sampler decision is made at span creation and inherited by all children (parent-based via the default).

**Problems**:

1. **Errors get sampled at 1%**. At p99.9 booking error rate of 0.1%, in a 60s flash-sale stress test producing 30,000 booking attempts, ~30 will fail — and at 1% sampling, you'll see *zero or one* of them in Jaeger. The trace is exactly the thing operators need to triage that single failure, and it's the most likely to be dropped. Production SaaS observability (Stripe, Shopify, every major APM vendor) implements either tail-based sampling with an "always sample errors" policy, OR head-based for the happy path + override sampler to AlwaysSample on error context. See [OpenTelemetry Sampling docs](https://opentelemetry.io/docs/concepts/sampling/) and [OneUptime: How to implement OTel tail sampling](https://oneuptime.com/blog/post/2026-02-09-otel-tail-sampling-intelligent/view).
2. **No `error=true` attribute upgrade in head-based sampling decision** — the booking decorator's `span.SetStatus(codes.Error, ...)` happens AFTER the sample decision is locked at root span creation. Head-based sampling cannot route on properties of the span; only tail-based can. See [MAJOR-2].

### Semantic conventions

[`cmd/booking-cli/tracer.go:15`](../../cmd/booking-cli/tracer.go) imports `go.opentelemetry.io/otel/semconv/v1.17.0`. **HTTP and database semantic conventions were declared Stable in 2025** ([OpenTelemetry HTTP spans spec](https://opentelemetry.io/docs/specs/semconv/http/http-spans/)), with `v1.27.0+` being the canonical stable version. v1.17.0 is from 2022 and predates `http.request.method` / `url.full` / `server.address` rename — the project's span attributes are emitting the deprecated names (`http.method`, `http.url`, etc.). Operators using newer Jaeger/Tempo/Datadog UIs will see "legacy" indicators or fail to query stably. Manual span attributes in [`service_tracing.go:26-30`](../../internal/application/booking/service_tracing.go) use ad-hoc names like `user_id`, `event_id`, `quantity` — not OTel attribute namespacing (`booking.user_id` etc.). See [MINOR-1].

### Span quality

- DB ops are well-instrumented (every repo method has a span — [`repositories_tracing.go`](../../internal/infrastructure/persistence/postgres/repositories_tracing.go) has ~30 wrappers).
- Booking service spans use `RecordError` + `SetStatus(Error)` correctly — many codebases miss the `SetStatus` half.
- Messaging spans (Kafka publish/consume, Redis Stream XADD/XREAD) appear to be MISSING — no `otel.Tracer().Start` calls inside [`internal/infrastructure/messaging/`](../../internal/infrastructure/messaging/) or [`internal/infrastructure/cache/redis_queue.go`](../../internal/infrastructure/cache/redis_queue.go). A trace that crosses the outbox/Kafka boundary therefore breaks — Jaeger will show a disconnected root span on the consumer side. This is the single biggest *trace-quality* gap. See [MAJOR-3].
- No HTTP-server auto-instrumentation: `otelgin` middleware is NOT registered (verified — `grep -rn otelgin` returns nothing). HTTP spans are not auto-emitted; only the application-layer manual spans are. This means `http_request_duration_seconds` (Prometheus) is the *only* per-route latency signal and `http.server.duration` (OTel) is not emitted at all.

---

## 5. Log structure audit

`internal/log/` implements an exemplary Pattern-A/Pattern-B scheme:
- [`log.go:184-215`](../../internal/log/log.go) `enrichFields` automatically prepends `correlation_id` + OTel `trace_id` + `span_id` to every ctx-aware emit (zero-alloc when disabled).
- [`context.go:13-17`](../../internal/log/context.go) stores logger + correlation_id in a 24-byte value struct; one `context.WithValue` allocation per request.
- Zap encapsulation is enforced: `grep -rn "zap.L()\|zap.S()" internal/ cmd/` returns one match — a doc comment in `log.go:48` *prohibiting* the pattern. No code calls the globals.
- Typed tag package under [`internal/log/tag/`](../../internal/log/tag/) — compile-time field-key safety.
- Caller-skip is correctly set on both code paths (method + package shim).

**Potential gaps**:
- No sampling decoration on the `Logger` by default. `Options.Sampling != nil` is a hook but production wiring is not visible from this audit alone. In flash-sale conditions (10k+ req/s) every successful booking generating a single Info line is 10MB/s of JSON to disk — needs explicit sampling. Confirm via deployment config.
- The doc references "every structured log line includes `correlation_id` + (when sampled) `trace_id`/`span_id`" — but `enrichFields` ONLY injects `trace_id`/`span_id` when `sc.IsValid()` is true (i.e., when this request's span was sampled at head-based 1%). So 99% of logs have NO trace correlation — making "click from log to trace" impossible for 99% of requests. This is a direct consequence of the head-based 1% sampling decision. See [MAJOR-2] linkage.

---

## 6. Alert rule audit

`alerts.yml` is 1166 lines, ~50 distinct alerts. Every alert has a `runbook_url` annotation. Severity mix: 22 critical, 25 warning, 4 info. **No info-only alerts on raw CPU/memory** — the project alerts on symptoms (error rate, latency, backlog) consistently with Google SRE guidance ([Practical Alerting](https://sre.google/sre-book/practical-alerting/)).

Selected sampling (full table would duplicate `alerts.yml`):

| Alert                          | Threshold                                                                                              | Rationale (in-file comment)                                          | Symptom vs Cause | Runbook? | Likely FP rate |
| ---                            | ---                                                                                                    | ---                                                                  | ---              | ---      | ---            |
| `HighErrorRate`                | 5xx > 5% over 5m, `for: 2m`                                                                            | "5m window with 2m for prevents flap on every brief spike"          | **Symptom**      | Yes      | Low — hysteresis is calibrated                                                                                  |
| `HighLatency`                  | p99 > 2s for 2m+                                                                                       | "5m rate window — anti-flap; was 1m/1m before Phase 2"               | **Symptom**      | Yes      | Low — but threshold is arbitrary; no SLO link (see § 7)                                                         |
| `InventorySoldOut`             | `increase(bookings_total{status="sold_out"}[5m]) > 0`                                                  | "the previous expression referenced `booking_sold_out_total` which doesn't exist" | Informational | Yes      | High — fires on EVERY sold-out (info severity, intentional)                                                     |
| `OrdersStreamBacklogYellow/Orange/Red` | 10K / 50K / 200K for 2m/2m/1m                                                                          | "tiered alerts because MAXLEN would silently evict customer orders" | **Cause** (stream length, not user-visible) | Yes      | Low at the thresholds chosen                                                                                    |
| `ReconStuckCharging`           | `recon_stuck_charging_orders > 0 for 5m`                                                               | "Charging is meant to be sub-minute"                                 | Symptom (orders stuck) | Yes      | Low                                                                                                             |
| `ReconMaxAgeExceeded`          | `increase(...{outcome="max_age_exceeded"}[1h]) > 0 for 0m`                                             | "the customer's order was abandoned"                                 | **Symptom (user-impact)** | Yes      | None — each fire is a real customer impact                                                                      |
| `InventoryDriftCacheHigh`      | `increase(...{direction="cache_high"}[5m]) > 0 for 0s`                                                 | "P0 — false 202 Accepted for inventory that doesn't exist"           | **Symptom (correctness)** | Yes      | None expected after PR #114                                                                                     |
| `PaymentWebhookSignatureFailing` | `rate(...[5m]) > 0.1 for 5m`                                                                           | "5m soak absorbs deploy windows where nodes have mixed env"          | **Cause/Symptom (security)** | Yes      | Medium during rotations                                                                                         |
| `PaymentWebhookLateSuccessAfterExpiry` | `increase(...[5m]) > 0 for 0m`                                                                         | "every count = one refund ticket"                                    | **Symptom (user-impact)** | Yes      | None — single-event paging                                                                                      |
| `KafkaConsumerStuck`           | `sum by (topic)(rate(kafka_consumer_retry_total[5m])) > 0 for 2m`                                      | "we intentionally do NOT dead-letter on transient errors"            | **Cause** (downstream)        | Yes      | Low                                                                                                             |
| `SagaConsumerLagHigh`          | `lag > 30 for 2m AND on(instance) up == 1`                                                             | "gauge is PERFORMANCE not LIVENESS; gate on up==1 to prevent stuck-gauge FP" | **Symptom**      | Yes      | Low — the `up==1` gate is a textbook fix; explicit acknowledgement of multi-pod join concern is rare and excellent |
| `ConsumerGroupRecreated`       | `increase(...[5m]) > 0 for 0s`                                                                         | "single occurrence = silent data loss"                               | **Cause (architectural)** | Yes      | None in healthy ops                                                                                             |
| `TargetDown`                   | `up == 0 for 2m`                                                                                       | "meta-alert; rate-based alerts go silently inert otherwise"          | **Cause (infra)** | Yes      | Fires on planned deploys — runbook calls this out                                                               |
| `SweepGoroutinePanic`          | `increase(...[5m]) > 0 for 0s`                                                                         | "without this, recovered panic is invisible"                         | **Cause/Symptom (defects)** | Yes      | None in healthy ops                                                                                             |

Symptom/cause balance: ~40% symptom alerts (HighErrorRate, HighLatency, Sold-Out, ReconStuck, InventoryDrift, LateSuccess, ChargingMaxAge), ~50% cause alerts (BacklogYellow/Orange, ConsumerGroupRecreated, ReconFindStuckErrors, SagaWatchdogFindStuckErrors, OutboxPendingCollectorDown, TargetDown, RedisExporterCannotReachRedis, XAck/XAdd/Revert failures, DBRollback). The 50% cause-alert ratio is high but justified — each cause alert is the *direct silent-failure tripwire* for some other observability surface going blind (the recurring "without this alert, X gauge goes stale silently" comment is the right reasoning). See Google Cloud's [Why Focus on Symptoms, Not Causes](https://cloud.google.com/blog/topics/developers-practitioners/why-focus-symptoms-not-causes) — the project effectively follows this; the cause alerts are mostly *meta-symptoms* of monitoring failure, not raw resource-utilisation thresholds.

**Missing alert classes**:
1. No PG-server-side alerts (no `pg_stat_*` scrape). Replication lag, connection-saturation-from-DB-side, slow-query rate are all blind.
2. No Kafka-broker alerts (no Kafka exporter wired). Lag-by-partition, ISR shrinks, leader election all blind.
3. No idempotency-mismatch surge alert (the metric exists — `idempotency_replays_total{outcome="mismatch"}` — but no alert references it). A client reusing keys across logically-distinct requests is a business signal worth knowing.

---

## 7. SLO / SLI gap analysis

**No SLI / SLO document exists.** Grep `/Users/lileon/project/booking_monitor/docs` for `SLO`, `SLI`, `error budget`, `burn rate` returns nothing substantive. The `monitoring.md` doc explicitly does not define one.

This is the largest gap relative to "industry production grade." A KKTIX-class flash-sale system would have at minimum:

- **SLI**: `successful_booking_completion_rate` = `bookings_total{status="success"} / bookings_total`
- **SLI**: `booking_api_availability` = `1 - http_requests_total{path="/api/v1/book",status=~"5.."} / http_requests_total{path="/api/v1/book"}`
- **SLI**: `pay_endpoint_latency_p99` = `histogram_quantile(0.99, ...path="/api/v1/orders/:id/pay")`
- **SLO target candidates**: 99.9% successful booking over 28d; p99 pay latency < 500ms; webhook delivery < 5s p95.

Without SLOs, the existing `HighLatency` and `HighErrorRate` alerts are gut-feel thresholds (5% / 2s). With SLOs, they'd be **burn-rate alerts** computed per [Google SRE workbook: Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/) using the canonical 4-tier multi-window multi-burn-rate scheme (5m+1h at 14.4× / 30m+6h at 6× / 6h+1d / 3d+30d). Multi-window multi-burn-rate is the consensus 2025 approach ([99.99% SLO Alert Template](https://medium.com/@obaff/99-99-slo-alert-template-multi-window-burn-rate-35d0bb38962f) and [Google Cloud SLO burn rate docs](https://cloud.google.com/stackdriver/docs/solutions/slo-monitoring/alerting-on-budget-burn-rate)).

See [MAJOR-1].

---

## 8. Dashboard quality audit

`dashboard.json` — main "Booking Monitor Dashboard". 25 panels organised into 7 collapsible rows (Golden signals → Recon → Saga → DLQ → DB → Cache → Redis → Meta). All panels have `description` strings (rare in hobby projects — every description references the corresponding alert). Time-default `now-15m`, refresh `5s`.

| Panel                          | Useful for incidents? | Notes                                                                                                  |
| ---                            | ---                   | ---                                                                                                    |
| Request Rate (RPS) by method/path/status | Yes                   | Standard.                                                                                              |
| p99/p95/p50 Latency            | Yes                   | Buckets unit = `s`. Good.                                                                              |
| Conversion Rate %              | Useful for business, less for incidents | Should be on a different dashboard layer per Grafana best practice.                                    |
| Saturation (Goroutines)        | Yes                   | But `sum by (job)` is a thin signal — would be more useful split by `instance` and overlaid with `go_sched_latencies` p99. |
| Saturation (Memory Alloc Bytes) | Marginal              | `go_memstats_alloc_bytes` is an *instantaneous* value, not allocation *rate*. Operators want `rate(go_memstats_alloc_bytes_total[1m])` or `go_gc_heap_allocs_bytes_total`. Current panel shows sawtooth of GC cycles — looks impressive on a screenshot but doesn't help debug. **Antipattern**. |
| Recon resolved by outcome      | Yes                   | Stack of labelled rates is exactly right.                                                              |
| Recon stuck-charging gauge     | Yes                   | Point-in-time gauge, alert-aligned.                                                                    |
| Recon resolve duration p95/p50 | Yes                   | But no red-line threshold annotation despite `RECON_CHARGING_THRESHOLD=30s` being a natural one.       |
| Recon error rates              | Yes                   | 3 stacked error series — operator can spot which subsystem.                                            |
| Saga panels                    | Yes                   | Mirrors recon pattern; good.                                                                           |
| DLQ panels                     | Yes                   | Good.                                                                                                  |
| PG pool in-use vs idle         | Yes                   | But no horizontal threshold line at `MAX_OPEN_CONNS` — should be marked.                               |
| PG pool wait rate + duration + rollback | Yes                   | 3-on-one panel is dense; ideally split.                                                                |
| Idempotency hit rate %         | Yes                   | Good.                                                                                                  |
| Idempotency GET errors         | Yes                   | Alert-paired.                                                                                          |
| Redis stream/DLQ failures      | Yes                   | Good.                                                                                                  |
| Stream collector errors        | Yes                   | Meta-monitor — closes the silent-failure gap.                                                          |
| Scrape target up/down          | Yes                   | Essential.                                                                                             |

**Visible gaps**:
- No HTTP RED panel (just RPS + latency, no separate error-rate ratio panel). The `HighErrorRate` alert exists but the dashboard panel for it is implicit only.
- Latency panels do not have **red-line thresholds** drawn at the alert thresholds (2s for `HighLatency`).
- No "war room" version of the main dashboard — `admin_war_room.json` is a separate dashboard and (as flagged below) it's wired to slightly different metrics.
- No SLO panels at all (consistent with §7 finding).
- Time-default `now-15m` is fine for live ops; no "incident review" panel set defined for retrospective at `now-1d`.
- `refresh: 5s` is too fast — Grafana best practice 2025 is 30s for ops dashboards (matches Prometheus scrape interval). 5s causes scrape-Grafana-DB extra load with no incremental signal value.

**Strengths**:
- Every panel has a `description` referencing the alert, the metric source, and the operator response. Better than 95% of dashboards I have seen.
- Row organisation by reliability concern, not arbitrary grouping.

---

## 9. Admin war room audit

`admin_war_room.json` — 9 panels. Composes "business" stat panels (Bookings/s, Pay conv %, Saga events/s, SSE connections) + "anomaly overlay" timeseries + "streaming health" timeseries.

**Verified**:
- `admin_event_bus_published_total` exists and IS wired ([`metrics_admin_stream.go:31-37`](../../internal/infrastructure/observability/metrics_admin_stream.go) + emitted at [`admin_event_bus.go:131`](../../internal/infrastructure/cache/admin_event_bus.go)).
- Datasource UID pinned (`"uid": "PROMETHEUS"`) consistently in this file.

**Concerns**:
1. **Panel 1 "Bookings / sec" uses `admin_event_bus_published_total{event_type="order.created"}` rather than `bookings_total{status="success"}`**. The recent commit `28f934e` (`fix(grafana): use admin_event_bus metric for booking panels`) is exactly this change. **This is a leaky abstraction**: the war-room panel labelled "Bookings / sec" now measures *events emitted to the admin bus*, not *successful customer bookings*. If the admin event bus drops messages (which `admin_event_bus_dropped_total{reason="channel_full"}` explicitly counts and `AdminEventBusDropping` alerts on), the war-room "Bookings / sec" panel will *underreport* successful bookings — a flash-sale operator looking at war room during incident will see the wrong rate. This is the inverse of what you want from a war room dashboard. See [MAJOR-4].
2. Panel 5 "Booking outcomes (1m rate, stacked)" labelled "stacked" but the `expr` is `sum by (event_type)` over `admin_event_bus_published_total`, not over `bookings_total{status}`. So the legend will show admin event types (`order.created`, `order.paid`, etc.), NOT booking outcomes (`success/sold_out/duplicate/error`). The title is **misleading**.
3. Panel 4 "Admin SSE connections" is a single number with `mode: thresholds` + green-only steps — no thresholds actually defined. Always green. Not useful as a war-room signal.
4. Panel 2 "Pay conversion %" uses `payment_webhook_received_total{result="succeeded"} / bookings_total{status="success"}` — this is correct as a derived KPI, but it conflates two distinct latency windows (booking is sync, webhook arrives async). On a fresh deploy or after a Kafka backlog, the ratio can briefly exceed 100% or dip arbitrarily. No documentation on the dashboard panel reflects this caveat.
5. The "what's broken right now?" use case is only partially served. Strengths: streaming health, saga + DLQ overlay. Weaknesses: no "errors per route" panel, no scrape-up panel, no recon/expiry backlog rollup, no inventory drift status. A war-room operator would need to flip to the main dashboard.

---

## 10. Recent fix root-cause audit

### Commit `1cb00cf` — `fix(grafana): pin Prometheus datasource UID to "PROMETHEUS"`

Verified: `admin_war_room.json` panel datasources all use `{"type": "prometheus", "uid": "PROMETHEUS"}`.

But `dashboard.json` panels still use **the legacy form** `"datasource": "Prometheus"` (a string, no UID) in all 25 panels. So the fix was applied to one of three dashboard files. `redis-exporter.json` was not audited line-by-line but the inconsistency between `dashboard.json` (string `"Prometheus"`) and `admin_war_room.json` (`{"uid": "PROMETHEUS"}`) means **`dashboard.json` is still vulnerable to the same datasource UID drift that motivated the fix**. Grafana 10+ deprecates the string form and falls back to "default" — works today, breaks the day someone changes the provisioned datasource name. See [MAJOR-5].

### Commit `28f934e` — `fix(grafana): use admin_event_bus metric for booking panels`

Root cause inferred: the war-room dashboard was originally wired to `bookings_total{status="success"}`, but in Stage 5 / Damai (PR #113) the "bookings happen via Kafka intake" — the in-process booking handler still emits `bookings_total` but the **published-to-admin-bus event** is what the live war-room operator sees in the SSE event timeline. The fix aligns the war-room metric source with the SSE event source so both panels (and the SSE timeline) tell a consistent story.

This is **internally consistent but operationally a band-aid**. The right architecture is either:
- Have ONE business counter (`bookings_total`) that is incremented at the same point as the admin event is emitted (preferably inside the same atomic operation — they go to the same place semantically), OR
- Have the war-room dashboard explicitly *label* itself as "live SSE-derived event stream rate" rather than "Bookings / sec" — the rename would make the choice honest.

The underlying issue — two parallel counters measuring closely-related-but-not-identical things, with confusable labels — is not addressed. A future stage refactor will hit this again.

---

## 11. Monitoring docs contract audit

`docs/monitoring.md` — 522 lines, paired with `monitoring.zh-TW.md`. Hook `.claude/hooks/check_monitoring_docs.sh` enforces co-update of the docs whenever any of:
- `internal/infrastructure/observability/metrics.go`
- `*_collector.go`
- `deploy/prometheus/alerts.yml`
- `deploy/prometheus/prometheus.yml`
- `deploy/grafana/provisioning/dashboards/*.json`
is touched.

**Hook scope is correct**, but the hook references `metrics.go` (singular) while the actual files are now `metrics_*.go` (split per concern as documented in [`metrics_init.go:19-32`](../../internal/infrastructure/observability/metrics_init.go) header). **The hook may not fire on edits to `metrics_booking.go` / `metrics_worker.go` / etc.** This is a stealthy enforcement gap.

Spot-check of recent metrics:
- `admin_event_bus_*` (PR #121) — **documented in §2 "Admin event streaming metrics" table** with full type/labels/purpose breakdown. ✓
- `inventory_low_alerts_total` (PR #121) — **documented**. ✓
- `saga_compensator_events_processed_total` (PR-D12.4) — **documented** with 16 outcome labels enumerated. ✓
- `saga_mark_redis_reverted_errors_total` / `saga_was_redis_reverted_errors_total` (PR-A) — **documented**. ✓
- `stripe_api_calls_total` / `stripe_api_duration_seconds` (D4.2) — **documented** including outcome-label triage. ✓
- `expiry_*` (D6) — **documented** comprehensively. ✓
- `redis_client_pool_*` (post-O3) — **documented** with worked example queries. ✓
- `inventory_drift_*` (PR-D) — **documented**. ✓
- `outbox_pending_collector_errors_total` — **documented**. ✓
- §5 alert catalog has matching rows for every alert in `alerts.yml`. ✓
- §5 "Forcing an alert to fire" recipes cover ~15 of ~50 alerts — useful for the most commonly-tested ones; not all.

**The docs contract is observably working.** The hook scope is the only finding.

---

## Findings (severity: CRIT / MAJOR / MINOR / NIT)

### [MAJOR-1] No SLI / SLO definitions; alerts use gut-feel thresholds

**Where**: `docs/monitoring.md` §5; `deploy/prometheus/alerts.yml` — every `HighLatency` / `HighErrorRate` / latency-based alert
**What**: There is no document defining what an SLO IS for this system. `HighErrorRate` fires at 5% over 5m; `HighLatency` fires at p99 > 2s for 2m. Both thresholds are arbitrary, not tied to a user-promise.
**Why**: For "industry production grade" — which is the stated bar — multi-window multi-burn-rate alerts on declared SLOs are the consensus 2025 approach. Threshold alerts on RED metrics produce false positives during deploys and false negatives during slow-burn outages (e.g. p99 at 1.9s for 14 days steadily consumes the error budget but never trips the alert). The Google SRE workbook explicitly recommends burn-rate alerts over threshold alerts for this reason.
**Recommendation**:
1. Define 3 SLOs in `docs/monitoring.md` §0 (NEW): booking availability (5xx rate), pay-endpoint latency (p99 < threshold), webhook ack latency.
2. Replace `HighErrorRate` + `HighLatency` with the 4-tier MWMB pattern (5m/1h at 14.4×, 30m/6h at 6×, 6h/1d at 1×, 3d/30d ticket). Templates exist at every observability vendor; the Prometheus recording-rule pattern is documented in the Google SRE Workbook.
3. Keep the existing single-event-paging alerts (`InventoryDriftCacheHigh`, `PaymentWebhookLateSuccessAfterExpiry`, etc.) as-is — those are correct for irreplaceable events.
**References**: [Google SRE: Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/) (2018, still authoritative); [99.99% SLO Alert Template Multi-Window Burn Rate](https://medium.com/@obaff/99-99-slo-alert-template-multi-window-burn-rate-35d0bb38962f) (2025).

### [MAJOR-2] Head-based 1% trace sampling drops errors and breaks log-trace correlation

**Where**: [`cmd/booking-cli/tracer.go:61-79`](../../cmd/booking-cli/tracer.go); [`internal/log/log.go:184-215`](../../internal/log/log.go) `enrichFields`
**What**: `OTEL_TRACES_SAMPLER_RATIO=0.01` is implemented as `TraceIDRatioBased(0.01)`. Error spans are sampled at the same 1% as successes. `enrichFields` only injects `trace_id`/`span_id` into log lines when the span is sampled — so 99% of error logs have NO trace correlation.
**Why**: During an incident, the operator clicks from a Grafana spike to a log entry, then expects to follow the `trace_id` to Jaeger. With 99% of logs missing `trace_id`, the click-through fails 99 times out of 100 — and the 1 successful click goes to a *random* request, not the one the operator was investigating. This is the worst-case shape for trace-log correlation. Industry SOP since ~2023 is tail-based sampling at the Collector with an "always sample errors" policy.
**Recommendation**:
1. Short-term: wrap the head sampler so that any span with an error attribute or `RecordError` call is overridden to always-sample. This is a `ParentBased(WithRemoteParentSampled(AlwaysSample()))` pattern in OTel-Go, but requires the *root* span (the HTTP layer) to mark errors at start, not after the fact.
2. Long-term: deploy the OTel Collector with `tail_sampling` processor + `status_code: ERROR` policy. The project already wires `otlptracegrpc` to a Collector-like endpoint — `traceExporter, _ := otlptracegrpc.New(...)` — so collector-side tail sampling is a config-only change, not a code change.
3. Update `enrichFields` to ALWAYS inject `trace_id`/`span_id` if a valid span exists in context, regardless of the sampling decision. The Collector tail-sampler will decide what to keep; the *logs* will always be correlatable.
**References**: [OpenTelemetry tail-based sampling docs](https://opentelemetry.io/docs/concepts/sampling/); [OneUptime: How to implement OTel tail sampling](https://oneuptime.com/blog/post/2026-02-09-otel-tail-sampling-intelligent/view) (2026).

### [MAJOR-3] Messaging spans missing — traces are disconnected at every async boundary

**Where**: [`internal/infrastructure/messaging/`](../../internal/infrastructure/messaging/); [`internal/infrastructure/cache/redis_queue.go`](../../internal/infrastructure/cache/redis_queue.go)
**What**: Kafka publish/consume and Redis Stream XADD/XREAD code paths do not call `otel.Tracer().Start`. A trace that crosses the outbox→Kafka→saga-consumer boundary breaks into two unrelated traces.
**Why**: For a system whose architectural value-add is the saga-and-outbox pattern, the trace view of "an order, end-to-end" is the single most valuable observability artifact — and it's broken. The OTel messaging semantic conventions ([messaging spec](https://opentelemetry.io/docs/specs/semconv/messaging/) — still "in Development" but the producer/consumer span pattern is established) define `messaging.system="kafka"`, `messaging.destination.name="order.failed"`, etc., and inject `traceparent` into headers so the consumer can link.
**Recommendation**: Add explicit producer + consumer spans in the messaging packages with semconv-compliant attributes. Inject `traceparent` via `propagation.TextMapPropagator`. ~20 lines per producer/consumer.
**References**: [OTel semantic conventions for messaging](https://opentelemetry.io/docs/specs/semconv/messaging/) (in Development, but consensus pattern); ZenrioTech blog ["Why OpenTelemetry and Semantic Conventions are the Last Piece of the Observability Puzzle"](https://zenriotech.com/blog/opentelemetry-semantic-conventions-last-piece-observability-puzzle).

### [MAJOR-4] War-room "Bookings / sec" panel measures admin event publishes, not successful bookings

**Where**: [`deploy/grafana/provisioning/dashboards/admin_war_room.json:77`](../../deploy/grafana/provisioning/dashboards/admin_war_room.json); commit `28f934e`
**What**: The Stage 5 dashboard fix swapped `bookings_total{status="success"}` for `admin_event_bus_published_total{event_type="order.created"}`. The panel title is still "Bookings / sec" but the underlying metric is "events the admin bus drainer published" — which can underreport during admin-bus backpressure (an alert condition that ALSO fires on this dashboard).
**Why**: During an incident where the admin event bus is itself degraded (the most likely time an operator opens the war room), the "Bookings / sec" panel will diverge from the real booking rate — exactly when accurate information matters most. The two metrics differ by `admin_event_bus_dropped_total{reason="channel_full"}` exactly.
**Recommendation**: One of:
- Rename the panel to "Admin event stream order.created events / sec" — make the metric source honest.
- Or revert the metric source to `bookings_total{status="success"}` — the source-of-truth business counter — and find a different way to make the SSE-event timeline ↔ Grafana relationship consistent (perhaps a dual-line overlay on the same panel showing both rates, so the divergence is visible rather than hidden).
**References**: Internal — this is project-specific.

### [MAJOR-5] Datasource UID fix applied to only one dashboard

**Where**: [`deploy/grafana/provisioning/dashboards/dashboard.json`](../../deploy/grafana/provisioning/dashboards/dashboard.json) (every panel uses legacy `"datasource": "Prometheus"` string); commit `1cb00cf` only touched `admin_war_room.json`
**What**: `dashboard.json` still uses the deprecated string-form datasource reference. `admin_war_room.json` uses the new UID-pinned form.
**Why**: Grafana 10+ deprecates the string form; it falls back to whatever is the "default" datasource. If the provisioned datasource name changes (e.g. when adding a Loki datasource and reorganising), the main dashboard silently breaks. The Stage 5 fix `1cb00cf` was correct but partial.
**Recommendation**: Sweep `dashboard.json` and `redis-exporter.json` to the same `{"type": "prometheus", "uid": "PROMETHEUS"}` form, matching `admin_war_room.json`.
**References**: Grafana docs on datasource UIDs (each major release since 9.0).

### [MINOR-1] OTel semantic conventions are out of date

**Where**: [`cmd/booking-cli/tracer.go:15`](../../cmd/booking-cli/tracer.go) imports `semconv/v1.17.0`
**What**: HTTP and database semantic conventions are now Stable as of 2025 ([OpenTelemetry HTTP spans spec](https://opentelemetry.io/docs/specs/semconv/http/http-spans/)). The canonical stable version is `v1.27.0+`. v1.17.0 emits deprecated names (`http.method` vs `http.request.method`, etc.).
**Why**: New Jaeger/Tempo UIs and modern APM vendors expect the stable names; legacy names are dual-emitted with a deprecation marker in modern semconv libraries.
**Recommendation**: Bump to `semconv/v1.27.0` (or latest), set `OTEL_SEMCONV_STABILITY_OPT_IN=http,database` env var on the deployment, and re-name application-layer span attributes (`user_id` → `booking.user_id`, etc.) following OTel's recommendation that application attributes use a domain prefix.
**References**: [OTel HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/http-spans/); [Releases of semantic-conventions](https://github.com/open-telemetry/semantic-conventions/releases).

### [MINOR-2] Monitoring docs hook may not fire on `metrics_*.go` edits

**Where**: `.claude/hooks/check_monitoring_docs.sh` (referenced in CLAUDE.md but not read in this audit); the documented trigger file list in CLAUDE.md mentions `internal/infrastructure/observability/metrics.go`
**What**: The single `metrics.go` was split into ~25 `metrics_*.go` files (per [`metrics_init.go:19-34`](../../internal/infrastructure/observability/metrics_init.go) header). If the hook still pattern-matches the singular filename, edits to per-concern files will not trigger the doc-co-update reminder.
**Why**: The whole point of the hook is "you can't ship a new metric without documenting it" — and the most common shape of a new metric is "add a new `metrics_<concern>.go` file". If the hook misses those, the enforcement is meaningfully weaker.
**Recommendation**: Audit the hook script (out of scope for this report — file not read here) and confirm it pattern-matches `internal/infrastructure/observability/metrics_*.go` (with the wildcard).

### [MINOR-3] `BookingsTotal{status="duplicate"}` is pre-warmed but never emitted

**Where**: [`metrics_init.go:44`](../../internal/infrastructure/observability/metrics_init.go) pre-warm includes `"duplicate"`; [`service_metrics.go:25-42`](../../internal/application/booking/service_metrics.go) never emits it
**What**: The decorator's `switch` returns `success` / `sold_out` / `error`. The `duplicate` outcome is a worker-side concern only (`worker_orders_total{status="duplicate"}`).
**Why**: The pre-warm produces a permanent `bookings_total{status="duplicate"} 0` series. Confusing for operators who'd expect a non-zero value means "duplicate bookings happened at API layer" — when actually it means nothing.
**Recommendation**: Drop `"duplicate"` from the booking pre-warm list in [`metrics_init.go:44`](../../internal/infrastructure/observability/metrics_init.go).

### [MINOR-4] `payment_webhook_unsupported_type_total` label is provider-controlled

**Where**: [`webhook_adapter.go:40`](../../internal/bootstrap/webhook_adapter.go)
**What**: `PaymentWebhookUnsupportedTypeTotal.WithLabelValues(eventType)` — `eventType` is the raw payload value from Stripe. Stripe today publishes ~50 event types; the codebase only dispatches on 2. Cardinality is bounded today but provider-controlled.
**Why**: A future Stripe expansion or a misrouted webhook from a different provider could explode the cardinality.
**Recommendation**: Apply the same `safeEventTypeLabel`-style closed-enum collapse used in [`sse/handler.go:307`](../../internal/infrastructure/api/sse/handler.go). Or add `relabel_configs` in `prometheus.yml` to drop unrecognised values.

### [MINOR-5] Grafana refresh `5s` is too aggressive

**Where**: [`dashboard.json:9`](../../deploy/grafana/provisioning/dashboards/dashboard.json) `"refresh": "5s"`
**What**: Prometheus scrape interval is `15s`, so the dashboard refreshes 3× faster than new data arrives.
**Why**: Generates Prometheus query load 3× without information gain. Grafana best practice is `refresh = scrape_interval`.
**Recommendation**: Change to `"refresh": "30s"`, matching `admin_war_room.json`.

### [MINOR-6] No HTTP-server auto-instrumentation; manual spans only

**Where**: [`cmd/booking-cli/server.go`](../../cmd/booking-cli/server.go) does not register `otelgin`
**What**: HTTP request handling has no OTel span emitted by the framework. Only application-layer manual spans exist (`BookTicket`, `GetOrder`, etc.). Means trace roots start *inside* the handler, missing the wrap of the entire HTTP lifecycle.
**Why**: Standard practice is to let the framework emit a root server span with `http.*` attributes; the application then enriches with business attributes. This codebase inverts that.
**Recommendation**: Add `r.Use(otelgin.Middleware("booking-monitor"))` in [`server.go`](../../cmd/booking-cli/server.go). 1 line.
**References**: [OTel contrib otelgin](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin).

### [MINOR-7] No publish/latency histograms for outbox relay or stage5 Kafka publish

**Where**: [`internal/application/outbox/relay.go`](../../internal/application/outbox/relay.go); Stage 5 publish path
**What**: No histogram on per-batch outbox processing time or stage5 publish latency. The `OutboxPendingBacklog` alert can only fire on the *symptom* (backlog gauge climbs); operators have no way to see whether the relay is slow vs the broker is slow.
**Recommendation**: Add `outbox_relay_batch_duration_seconds` + `outbox_relay_batch_size` histograms; add `stage5_kafka_publish_duration_seconds`.

### [NIT-1] No Kafka exporter, no PG exporter wired

**Where**: `docker-compose.yml` / `prometheus.yml`
**What**: Server-side metrics for the two stateful core dependencies (PG, Kafka) are not scraped. Redis is well-covered.
**Why**: Trade-off acknowledged in the docs (Redis exporter is in, PG/Kafka are not). For a learning project this is acceptable; for production grade it leaves replication-lag, broker-side under-replicated partitions, slow-query rate, etc. unobservable.
**Recommendation**: Defer — `docs/architectural_backlog.md` candidate. Single PR each.

### [NIT-2] Latency-panel red-line thresholds missing

**Where**: All histogram-quantile panels in `dashboard.json`
**What**: Panels for p99 latency, recon resolve duration, etc. do not render the alert threshold as a horizontal red line.
**Why**: Operators have to remember thresholds from `alerts.yml` to read the panels.
**Recommendation**: Add `"thresholds"` block to each timeseries panel with `mode: absolute, steps: [{color: green, value: 0}, {color: red, value: <alert-threshold>}]`. Trivial JSON edit.

### [NIT-3] No "incident review" dashboard variant at `now-24h`

**Where**: All dashboards default to `now-15m`
**What**: Post-incident review requires the operator to manually change the time picker.
**Recommendation**: Add a dashboard variable for time window, or clone one dashboard with `now-24h` default.

---

## Recommendations summary

Priority order:
1. **[MAJOR-1]** Define SLOs + adopt multi-window multi-burn-rate alerts. Biggest gap relative to "industry production grade" bar.
2. **[MAJOR-2]** Switch trace sampling to tail-based at the Collector (errors always sampled). Fix log-trace correlation.
3. **[MAJOR-4]** Resolve war-room "Bookings / sec" panel ambiguity — either rename or use the real business counter.
4. **[MAJOR-5]** Sweep `dashboard.json` + `redis-exporter.json` to pinned datasource UID.
5. **[MAJOR-3]** Add messaging spans (Kafka producer/consumer, Redis Stream) — closes the trace continuity gap.
6. **[MINOR-1]** Bump OTel semconv to `v1.27.0`+ and add `OTEL_SEMCONV_STABILITY_OPT_IN=http,database`.
7. **[MINOR-2]** Audit and update `check_monitoring_docs.sh` hook to match the per-concern `metrics_*.go` files.
8. **[MINOR-3]** Remove orphan `bookings_total{status="duplicate"}` pre-warm.
9. **[MINOR-4]** Collapse `payment_webhook_unsupported_type_total{event_type}` to a closed enum.
10. **[MINOR-5]** Change dashboard `refresh` to `30s`.
11. **[MINOR-6]** Register `otelgin` middleware for root HTTP server spans.
12. **[MINOR-7]** Add publish-latency histograms (outbox relay + Stage 5).
13. **[NIT-1/2/3]** PG/Kafka exporters, threshold lines on latency panels, incident-review dashboard variant — backlog.

---

## References

### Cited sources (with date + one-line quote)

- [Prometheus Labels: Understanding and Best Practices | CNCF](https://www.cncf.io/blog/2025/07/22/prometheus-labels-understanding-and-best-practices/) (2025-07-22) — "Do not use labels to store dimensions with high cardinality (many different label values), such as user IDs, email addresses, or other unbounded sets of values."
- [Prometheus Native Histograms specification](https://prometheus.io/docs/specs/native_histograms/) — "Starting with v3.8.0, native histograms are supported as a stable feature."
- [Prometheus Native Histograms in Production: Rollout Plan, Budgets, and Failure Modes](https://www.michal-drozd.com/en/blog/prometheus-native-histograms-production/) (2024) — "Real-world testing has been performed on Prometheus versions 2.48–2.53 with Grafana 10.x...production deployments have revealed significant resource challenges."
- [OpenTelemetry HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/http-spans/) — "In 2025, HTTP span and database semantic conventions were officially declared 'Stable.'"
- [OpenTelemetry Sampling concepts](https://opentelemetry.io/docs/concepts/sampling/) — "Tail-based sampling waits until a trace is complete, then decides whether to keep it based on the full picture - duration, error status, specific attributes."
- [OneUptime: How to implement OTel tail sampling](https://oneuptime.com/blog/post/2026-02-09-otel-tail-sampling-intelligent/view) (2026-02-09) — "A core feature of tail-based sampling is the ability to ensure all error traces are captured."
- [Google SRE Book: Practical Alerting](https://sre.google/sre-book/practical-alerting/) — "It's better to spend much more effort on catching symptoms than causes."
- [Google Cloud Blog: Why Focus on Symptoms, Not Causes?](https://cloud.google.com/blog/topics/developers-practitioners/why-focus-symptoms-not-causes) — "Alerts should be based on end-to-end measures of customer/client experience, not based on a system's internal behavior."
- [Google SRE Workbook: Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/) — "Multi-burn-rate alerts use pairs of time windows: a short window for responsiveness and a long window to prevent false positives."
- [99.99% SLO Alert Template (Multi-Window Burn Rate)](https://medium.com/@obaff/99-99-slo-alert-template-multi-window-burn-rate-35d0bb38962f) (2025) — Worked example of the 4-tier MWMB pattern.
- [Shopify Engineering: How we prepare Shopify for BFCM 2025](https://shopify.engineering/bfcm-readiness-2025) (2025) — "thousands of engineers working for nine months, running five major scale tests."
- [Shopify's Journey to Planet-Scale Observability](https://horovits.medium.com/shopifys-journey-to-planet-scale-observability-9c0b299a04dd) — "during the BFCM peak, they processed an astounding 7TB of logs per second."

### Searches that did NOT yield a 2024+ source

- "Grafana dashboard antipatterns 2024 2025 incident debugging" — search returned Grafana product pages but no antipattern-specific writeups from 2024+. Could not cite — the dashboard quality findings in §8 are based on the official Grafana docs (current) plus the project's own existing patterns.
- "Stripe / Etsy flash sale observability engineering blog 2024 2025" — Shopify content surfaced; Stripe and Etsy did not return relevant 2024+ posts on flash sales specifically. Could not cite.

---

**Word count of TL;DR**: 281 words.
