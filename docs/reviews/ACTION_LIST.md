# Consolidated Remediation Backlog

Generated from the 6 review PRs (#1–#6) after second-pass verification (2026-04-11).
Severity reflects post-verification judgement — downgrades applied where the verifier
found the original severity inflated.

**Fix PR plan:**
- **PR-A** (`fix/review-critical`) — all CRITICAL findings (C1–C6). Ship blocker.
- **PR-B** (`fix/review-high`) — all HIGH findings (H1–H8). Correctness.
- **PR-C** (`fix/review-backlog`) — MEDIUM/LOW/NIT backlog. Optional, draft.

## CRITICAL (ship-blockers — fix in PR-A)

| ID | Area | File | Finding | Source |
|----|------|------|---------|--------|
| C1 | saga | [internal/infrastructure/messaging/saga_consumer.go:L63-L72](../../internal/infrastructure/messaging/saga_consumer.go#L63-L72) | Poison messages committed after 3 in-memory retries. No DLQ write, no metric, no alert. Retry counter is local to `Start()` — restarts reset it, so the "3 retries" is effectively unbounded in practice. | [#4](https://github.com/Leon180/booking_monitor/pull/4) |
| C2 | payment | [internal/application/payment/service.go:L40-L47](../../internal/application/payment/service.go#L40-L47) | `OrderID<=0` or `Amount<0` returns `nil`; KafkaConsumer (`internal/infrastructure/messaging/kafka_consumer.go:L66-L73`) commits the offset and permanently loses the event. | [#4](https://github.com/Leon180/booking_monitor/pull/4) |
| C3 | api | [cmd/booking-cli/main.go:L248](../../cmd/booking-cli/main.go#L248) | `r.Run(...)` wraps a default `http.Server`; configured `ReadTimeout`/`WriteTimeout` never apply. Slow-loris trivial. | [#5](https://github.com/Leon180/booking_monitor/pull/5) |
| C4 | api | [internal/infrastructure/api/handler.go:L58,L76,L95,L127,L161](../../internal/infrastructure/api/handler.go) | Raw `err.Error()` returned in JSON body — leaks PG constraint names, driver errors, internal paths. | [#5](https://github.com/Leon180/booking_monitor/pull/5) |
| C5 | deploy | [docker-compose.yml](../../docker-compose.yml), [Makefile](../../Makefile) | Postgres password and Grafana admin password hardcoded in plaintext (docker-compose.yml L17,L41,L52-53,L90; Makefile L124,L106-110). | [#6](https://github.com/Leon180/booking_monitor/pull/6) |
| C6 | ops | [deploy/prometheus/alerts.yml:L27](../../deploy/prometheus/alerts.yml#L27) | `InventorySoldOut` rule references `booking_sold_out_total` — metric does not exist in code. Alert permanently silent. Actual metric is `bookings_total{status="sold_out"}`. | [#6](https://github.com/Leon180/booking_monitor/pull/6) |

## HIGH (correctness — fix in PR-B)

| ID | Area | File | Finding | Source |
|----|------|------|---------|--------|
| H1 | application | [internal/application/event_service.go:L38-L47](../../internal/application/event_service.go#L38-L47) | `CreateEvent` is a non-atomic dual-write: Postgres commits before Redis `SetInventory`. Failure window leaves events permanently unsellable. Inline comment at L45 already flags the gap ("Just log warning? Or fail?"). | [#1](https://github.com/Leon180/booking_monitor/pull/1) |
| H2 | persistence | [internal/infrastructure/persistence/postgres/repositories.go:L39,L106](../../internal/infrastructure/persistence/postgres/repositories.go#L39) | `err == sql.ErrNoRows` identity compare at two sites — any wrapping breaks NotFound → 404 mapping. Use `errors.Is(...)`. | [#2](https://github.com/Leon180/booking_monitor/pull/2) |
| H3 | persistence | [internal/infrastructure/persistence/postgres/repositories.go](../../internal/infrastructure/persistence/postgres/repositories.go) | 19 bare `return err` sites (lines 42,57,65,70,96,109,122,130,138,146,152,161,169,174,190,195,230,238,248) lack `fmt.Errorf("…: %w", err)`. Kills `errors.Is/As` upstream. | [#2](https://github.com/Leon180/booking_monitor/pull/2) |
| H4 | cache | [internal/infrastructure/cache/redis_queue.go:L191-L193](../../internal/infrastructure/cache/redis_queue.go#L191-L193) | `fmt.Sscanf` return discarded → non-numeric stream fields become `eventID=0` / `userID=0` silently. Replace with `strconv.Atoi`. | [#3](https://github.com/Leon180/booking_monitor/pull/3) |
| H5 | cache | [internal/infrastructure/cache/redis_queue.go:L105,L115,L150,L164,L234,L243](../../internal/infrastructure/cache/redis_queue.go) | All 6 `XAck`/`XAdd` (DLQ) results dropped — failed ACK silently re-queues into PEL; failed DLQ write loses the failure trace. | [#3](https://github.com/Leon180/booking_monitor/pull/3) |
| H6 | saga | [internal/infrastructure/cache/lua/revert.lua:L5-L13](../../internal/infrastructure/cache/lua/revert.lua#L5-L13) | `SETNX` idempotency key set BEFORE `INCRBY` — a crash between them means the key is permanently set and no compensation ever runs again. Swap order: INCRBY first, then SETNX. | [#4](https://github.com/Leon180/booking_monitor/pull/4) |
| H7 | messaging | [internal/infrastructure/messaging/kafka_consumer.go:L71-L73](../../internal/infrastructure/messaging/kafka_consumer.go#L71-L73), [internal/infrastructure/messaging/saga_consumer.go:L78-L80](../../internal/infrastructure/messaging/saga_consumer.go#L78-L80) | Log-and-forget on `CommitMessages` error. No retry, no backoff, loop continues with uncommitted offset. | [#4](https://github.com/Leon180/booking_monitor/pull/4) |
| H8 | relay | [internal/application/outbox_relay.go:L51-L53](../../internal/application/outbox_relay.go#L51-L53) | Advisory lock release only runs on `ctx.Done()` branch. No defer, no panic recovery. Any other exit path leaks the lock. Related: `outbox_relay_tracing.go:L26-L32` never records `span.RecordError`/`SetStatus`. | [#4](https://github.com/Leon180/booking_monitor/pull/4) |
| H9 | api | [internal/infrastructure/api/handler.go:L244](../../internal/infrastructure/api/handler.go#L244) | Legacy `POST /book` route registered outside any group — falls into nginx `location /` fallback with **no rate limit**. `/api/v1/*` IS protected at nginx:L8,L22, but legacy route is exposed. | [#5](https://github.com/Leon180/booking_monitor/pull/5) |
| H10 | observability | [cmd/booking-cli/main.go:L155-L163](../../cmd/booking-cli/main.go#L155-L163) | OTel init errors only `log.Printf` and continue. If `otlptracegrpc.New` fails, `traceExporter` is nil → `trace.WithBatcher(nil)` crashes on first span export. Wire into `fx.Lifecycle.OnStart` and fail-fast, or add Jaeger healthcheck. | [#6](https://github.com/Leon180/booking_monitor/pull/6) |
| H11 | deploy | [docker-compose.yml](../../docker-compose.yml), [Dockerfile](../../Dockerfile) | Six unpinned images: `prom/prometheus:latest`, `grafana/grafana:latest`, `jaegertracing/all-in-one:latest`, `nginx:alpine`, `golang:alpine`, `alpine:latest`. Pin all to concrete tags. | [#6](https://github.com/Leon180/booking_monitor/pull/6) |
| H12 | deploy | [Dockerfile:L25-L42](../../Dockerfile#L25-L42) | No `addgroup`/`adduser`/`USER` directive — container runs as UID 0. Add non-root user in runner stage. | [#6](https://github.com/Leon180/booking_monitor/pull/6) |
| H13 | config | [internal/infrastructure/config/config.go:L42-L66](../../internal/infrastructure/config/config.go#L42-L66) | `DATABASE_URL` has no `env-required`; `LoadConfig` has no `Validate()` method. Defaults like `REDIS_ADDR=localhost:6379`, `KAFKA_BROKERS=localhost:9092` silently mask missing env vars. | [#6](https://github.com/Leon180/booking_monitor/pull/6) |

## MEDIUM (robustness — PR-C backlog)

| ID | Area | File | Finding | Source |
|----|------|------|---------|--------|
| M1 | application | [internal/application/worker_service.go:L105](../../internal/application/worker_service.go#L105) | `payload, _ := json.Marshal(order)` — theoretical nil payload in outbox tx. **Downgraded from HIGH**: current `*domain.Order` fields cannot fail to marshal. | #1 |
| M2 | application | [internal/application/worker_service.go:L62](../../internal/application/worker_service.go#L62) | Use `errors.Is(err, context.Canceled)` rather than identity compare. | #1 |
| M3 | application | [internal/application/event_service.go:L27-L29](../../internal/application/event_service.go#L27-L29) | `CreateEvent` validates only `totalTickets <= 0`; empty `name` passes through. | #1 |
| M4 | application | [internal/application/outbox_relay.go:L53,L57](../../internal/application/outbox_relay.go#L53) | Hardcoded advisory lockID `1001` in two places. Two magic numbers that must stay in sync. | #1 |
| M5 | persistence | [internal/infrastructure/persistence/postgres/repositories.go:L33](../../internal/infrastructure/persistence/postgres/repositories.go#L33) | `FOR UPDATE` on `eventRepo.GetByID` outside tx. **Downgraded from HIGH**: zero production callers; latent footgun. Separate into `GetByIDForUpdate`. | #2 |
| M6 | persistence | [deploy/postgres/migrations/](../../deploy/postgres/migrations/) | Missing partial index `events_outbox(processed_at) WHERE processed_at IS NULL` for relay poll query. | #2 |
| M7 | persistence | [cmd/booking-cli/main.go:L282-L300](../../cmd/booking-cli/main.go#L282-L300), [internal/infrastructure/config/config.go:L44-L46](../../internal/infrastructure/config/config.go#L44-L46) | Pool setters called AFTER the ping loop; no `SetConnMaxLifetime`. Long-running conns accumulate staleness. | #2 |
| M8 | cache | [internal/infrastructure/cache/redis_queue.go:L132](../../internal/infrastructure/cache/redis_queue.go#L132) | `time.Sleep` ignores ctx cancellation. **Downgraded from HIGH**: max 600ms per msg (100+200+300). | #3 |
| M9 | cache | [internal/infrastructure/cache/redis_queue.go:L214](../../internal/infrastructure/cache/redis_queue.go#L214) | `processPending` uses `Block: 0` — XREADGROUP blocks until transport timeout on wedged conn. | #3 |
| M10 | messaging | [internal/infrastructure/messaging/kafka_consumer.go:L27-L29](../../internal/infrastructure/messaging/kafka_consumer.go#L27-L29) | Hardcoded `GroupID: "payment-service-group-test"` (note `-test` suffix) and `Topic: "order.created"`. `KafkaConfig` lacks both fields. | #4 |
| M11 | messaging | [internal/infrastructure/messaging/saga_consumer.go:L43](../../internal/infrastructure/messaging/saga_consumer.go#L43) | `retries := make(map[int64]int)` is local to `Start()` — resets on every consumer (re)start. Should be Redis-backed. | #4 |
| M12 | deploy | [deploy/prometheus/prometheus.yml:L15](../../deploy/prometheus/prometheus.yml#L15) | Scrape duplicate target `['host.docker.internal:8080', 'app:8080']`. | #6 |
| M13 | deploy | [deploy/nginx/nginx.conf:L35-L38](../../deploy/nginx/nginx.conf#L35-L38) | `/metrics` deny-all; Prometheus scrapes `app:8080` directly so this is moot but fragile. | #6 |
| M14 | deploy | [deploy/nginx/nginx.conf:L10-L13](../../deploy/nginx/nginx.conf#L10-L13) | No `proxy_read_timeout`, `proxy_connect_timeout`, `proxy_send_timeout`, `keepalive`, `proxy_http_version 1.1`, or `Connection ""`. | #6 |
| M15 | observability | [cmd/booking-cli/main.go:L167](../../cmd/booking-cli/main.go#L167) | `trace.WithSampler(trace.AlwaysSample())` hardcoded. Make configurable via `OTEL_TRACES_SAMPLER_RATIO`. | #6 |
| M16 | deploy | [deploy/redis/redis.conf:L4](../../deploy/redis/redis.conf#L4), [docker-compose.yml:L60-L61](../../docker-compose.yml#L60-L61) | Redis `bind 0.0.0.0`, no `requirepass`, exposed on host `6379:6379`. `RedisConfig.Password` exists but isn't wired to the container. | #6 |
| M17 | deploy | [deploy/prometheus/prometheus.yml:L2](../../deploy/prometheus/prometheus.yml#L2) | `scrape_interval: 5s` too aggressive for prod. | #6 |

## LOW & NIT (style / hardening — PR-C backlog, optional)

| ID | File | Finding | Source |
|----|------|---------|--------|
| L1 | [internal/infrastructure/cache/lua/revert.lua:L5-L7](../../internal/infrastructure/cache/lua/revert.lua#L5-L7) | Combine `SETNX` + `EXPIRE` into one `SET NX EX 604800`. **Downgraded from MED**: Lua atomic; only crash/SCRIPT KILL exposes gap. | #3 |
| L2 | [internal/infrastructure/cache/redis_queue.go:L40,L82](../../internal/infrastructure/cache/redis_queue.go) | String-equality check on `BUSYGROUP`/`NOGROUP` errors — brittle across Redis versions. Use `strings.Contains`. | #3 |
| L3 | [internal/infrastructure/cache/redis.go:L80](../../internal/infrastructure/cache/redis.go#L80) | `SetInventory` TTL=0 → key never expires. | #3 |
| L4 | [deploy/postgres/migrations/000001_create_events_table.up.sql:L9](../../deploy/postgres/migrations/000001_create_events_table.up.sql#L9) | Migration seeds application data (should be separate seed script). | #2 |
| L5 | [internal/infrastructure/persistence/postgres/repositories.go:L140,L232](../../internal/infrastructure/persistence/postgres/repositories.go#L140) | `_ = rows.Close()` silently ignores close error. | #2 |
| L6 | [internal/domain/event.go:L29-L38](../../internal/domain/event.go#L29-L38) | `Event.Deduct` mutates receiver. Violates project immutability rule. | #1 |
| L7 | [internal/application/saga_compensator.go:L36](../../internal/application/saga_compensator.go#L36) | Reads global `zap.S()`. Inconsistent with `workerService`'s injected logger. | #1 |
| L8 | [internal/domain/event.go:L44](../../internal/domain/event.go#L44) | `EventRepository.DeductInventory` marked Deprecated but retained. | #1 |
| L9 | [internal/application/saga_compensator.go:L44,L73,L84](../../internal/application/saga_compensator.go) | Errors returned unwrapped. | #4 |
| L10 | [internal/infrastructure/messaging/kafka_publisher.go:L38-L40](../../internal/infrastructure/messaging/kafka_publisher.go#L38), [internal/infrastructure/messaging/module.go:L25-L27](../../internal/infrastructure/messaging/module.go#L25) | `Close()` blocks on flush with no timeout wrapper. | #4 |
| L11 | [deploy/grafana/provisioning/dashboards/dashboard.json:L14,L33](../../deploy/grafana/provisioning/dashboards/dashboard.json) | Uses deprecated `graph` panel (removed in Grafana 10+). Migrate to `timeseries`; bump `schemaVersion` to 36+. | #6 |
| L12 | [deploy/grafana/provisioning/dashboards/dashboard.yml:L8](../../deploy/grafana/provisioning/dashboards/dashboard.yml#L8) | `disableDeletion: false`. Should be `true` in prod. | #6 |
| L13 | [cmd/verify-redis/main.go:L17](../../cmd/verify-redis/main.go#L17) | Hardcodes `Addr: "localhost:6379"`; no env var. | #6 |
| L14 | [Makefile:L110](../../Makefile#L110) | `reset-db` uses `FLUSHALL \|\| true` — silently swallows failure. | #6 |

### NIT

| ID | File | Finding | Source |
|----|------|---------|--------|
| N1 | [internal/application/event_service.go:L28](../../internal/application/event_service.go#L28) | `fmt.Errorf` without `%w`; could be `errors.New`. | #1 |
| N2 | [internal/application/module.go:L32](../../internal/application/module.go#L32) | OutboxRelay uses detached `context.Background()`. Fragile if OnStop gets a timeout. | #1 |
| N3 | [internal/infrastructure/messaging/kafka_consumer.go:L47](../../internal/infrastructure/messaging/kafka_consumer.go#L47) | `Start` returns `nil` on ctx cancel — no structured acknowledgement. | #4 |
| N4 | [internal/infrastructure/api/handler_tracing.go:L29,L45,L61,L77](../../internal/infrastructure/api/handler_tracing.go) | `span.SetStatus(codes.Error, ...)` gated on `status >= 500` only — 4xx never reflected. | #6 |
| N5 | [Makefile](../../Makefile) | Missing `migrate-status` target (has up/down/create/force only). | #6 |
| N6 | [deploy/prometheus/alerts.yml:L4-L9](../../deploy/prometheus/alerts.yml#L4) | `HighErrorRate` uses `rate([1m])` with `for: 1m` — window and dwell should not be equal. | #6 |

## Sources

| PR | Branch | Review file | Verification |
|----|--------|-------------|--------------|
| [#1](https://github.com/Leon180/booking_monitor/pull/1) | `review/domain-application` | [docs/reviews/domain-application.md](domain-application.md) | 11/11 verified |
| [#2](https://github.com/Leon180/booking_monitor/pull/2) | `review/persistence` | [docs/reviews/persistence.md](persistence.md) | 8 verified + 1 partial |
| [#3](https://github.com/Leon180/booking_monitor/pull/3) | `review/concurrency-cache` | [docs/reviews/concurrency-cache.md](concurrency-cache.md) | 8 verified, 2 severity downgrades |
| [#4](https://github.com/Leon180/booking_monitor/pull/4) | `review/messaging-saga` | [docs/reviews/messaging-saga.md](messaging-saga.md) | 12/12 verified |
| [#5](https://github.com/Leon180/booking_monitor/pull/5) | `review/api-payment` | [docs/reviews/api-payment.md](api-payment.md) | 15 verified + 1 partial |
| [#6](https://github.com/Leon180/booking_monitor/pull/6) | `review/observability-deploy` | [docs/reviews/observability-deploy.md](observability-deploy.md) | 20/20 verified |

## Metrics

- **Total findings:** 66 across 6 reviews
- **CRITICAL:** 6 (all ship-blockers)
- **HIGH:** 13
- **MEDIUM:** 17 (4 downgraded from HIGH during verification)
- **LOW:** 14 (1 downgraded from MEDIUM)
- **NIT:** 6
- **False positives:** 0
