# Review: Observability, Config & Deploy

**Branch:** `review/observability-deploy` **Agent:** `code-reviewer` **Date:** 2026-04-11
**Scope:** `internal/infrastructure/observability/**`, `internal/infrastructure/config/**`, `deploy/**`, `docker-compose.yml`, `Dockerfile`, `Makefile`, `cmd/verify-redis/**`

---

## Summary

The observability layer has solid foundations — metrics cardinality is well-controlled, tracing decorators follow the decorator pattern correctly, and all spans are closed via `defer span.End()`. The primary concerns are in the deploy stack: five images use `:latest` tags (causing non-reproducible builds), the Grafana admin password and Postgres credentials are hardcoded in `docker-compose.yml`, the `InventorySoldOut` alert references a non-existent metric name, and the OTel tracer silently degrades on init failure rather than fatal-failing. Config loading also lacks startup validation for required fields like `DATABASE_URL`.

---

## Findings (severity-ranked)

### [CRITICAL] Hardcoded Credentials in docker-compose.yml

**File:** [docker-compose.yml:17](../../docker-compose.yml#L17), [docker-compose.yml:41](../../docker-compose.yml#L41), [docker-compose.yml:51-54](../../docker-compose.yml#L51-L54), [docker-compose.yml:90](../../docker-compose.yml#L90)

**Issue:** Four separate credential values are hardcoded as plain text:
- `DATABASE_URL=postgres://user:password@postgres:5432/booking` (lines 17, 41)
- `POSTGRES_USER: user`, `POSTGRES_PASSWORD: password` (lines 52-53)
- `GF_SECURITY_ADMIN_PASSWORD=admin` (line 90)

**Impact:** Anyone with repository access has the database password and Grafana admin password. If this repo is ever pushed to a public host or cloned by a contractor, credentials leak immediately. The Postgres password is symmetrically hardcoded in the Makefile's `DB_URL` and in `reset-db` target too ([Makefile:124](../../Makefile#L124), [Makefile:106](../../Makefile#L106)).

**Fix:** Use a `.env` file (gitignored) with `${VAR}` substitution in Compose:
```yaml
# docker-compose.yml
environment:
  - DATABASE_URL=postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}
  - GF_SECURITY_ADMIN_PASSWORD=${GF_ADMIN_PASSWORD}
```
Provide a `.env.example` with placeholder values. Add `.env` to `.gitignore`. Reference env vars in the Makefile `DB_URL` too.

---

### [CRITICAL] Broken Alert Rule References Non-Existent Metric

**File:** [deploy/prometheus/alerts.yml:27](../../deploy/prometheus/alerts.yml#L27)

**Issue:** The `InventorySoldOut` alert fires on `booking_sold_out_total > 0`. This metric name does not exist anywhere in the codebase. A grep across all Go files finds no registration or increment of `booking_sold_out_total`. The actual sold-out signal lives in `bookings_total{status="sold_out"}`.

**Impact:** This alert never fires. Any real inventory-exhaustion event goes undetected silently. The alert gives a false sense of coverage.

**Fix:**
```yaml
- alert: InventorySoldOut
  expr: increase(bookings_total{status="sold_out"}[5m]) > 0
  labels:
    severity: info
  annotations:
    summary: "Event Sold Out"
    description: "Sold-out bookings detected in last 5m (count: {{ $value }})"
```

---

### [HIGH] OTel Tracer Init Errors Are Silently Swallowed — Tracing Degrades Without Warning

**File:** [cmd/booking-cli/main.go:155-163](../../cmd/booking-cli/main.go#L155-L163)

**Issue:** `initTracer()` calls `log.Printf` on both `resource.New` and `otlptracegrpc.New` failures and then continues. If the exporter fails to connect (Jaeger is down or the endpoint env var is wrong), the TracerProvider is set with a nil or broken exporter. All subsequent spans are silently dropped with no error surfaced.

Additionally, `initTracer()` is called inside the `fx.Invoke` callback at server startup (line 219), not at the `fx` lifecycle `OnStart` hook boundary — meaning tracer shutdown ordering is entangled manually via a returned `*trace.TracerProvider` rather than through `fx.Lifecycle`.

**Impact:** Tracing silently disappears in any environment where Jaeger is unreachable (CI, staging, cold starts before Jaeger is healthy). The `depends_on: jaeger` in Compose lacks a `condition: service_healthy` so there is no Jaeger healthcheck defined, compounding this.

**Fix:** Fatal on tracer init failure in production, or at minimum log at `WARN` level and record a structured error. Add a healthcheck to the `jaeger` service in Compose and set `condition: service_healthy` on the `app` dependency. Wrap tracer init in the `fx.Lifecycle` `OnStart` hook so `fx` manages shutdown ordering cleanly.

---

### [HIGH] No Image Pinning — Five Services Use `:latest`

**File:** [docker-compose.yml:23](../../docker-compose.yml#L23), [docker-compose.yml:75](../../docker-compose.yml#L75), [docker-compose.yml:84](../../docker-compose.yml#L84), [docker-compose.yml:94](../../docker-compose.yml#L94)

**Issue:** The following images are unpinned:
- `nginx:alpine` (line 23)
- `prom/prometheus:latest` (line 75)
- `grafana/grafana:latest` (line 84)
- `jaegertracing/all-in-one:latest` (line 94)

The Dockerfile builder stage also uses `golang:alpine` and runner stage uses `alpine:latest` ([Dockerfile:4](../../Dockerfile#L4), [Dockerfile:25](../../Dockerfile#L25)).

**Impact:** Builds are not reproducible. A Prometheus minor release changing a PromQL function or a Grafana breaking change in provisioning format can silently break dashboards or alerts on the next `docker-compose pull`.

**Fix:** Pin to specific digest or semver tags:
```yaml
image: prom/prometheus:v2.51.2
image: grafana/grafana:10.4.2
image: jaegertracing/all-in-one:1.57
image: nginx:1.25-alpine
```
In the Dockerfile use `golang:1.24-alpine` and `alpine:3.20`.

---

### [HIGH] Dockerfile Runs as root — No Non-Root USER

**File:** [Dockerfile:25-42](../../Dockerfile#L25-L42)

**Issue:** The runner stage never creates a non-root user or sets a `USER` directive. The binary runs as UID 0 inside the container.

**Impact:** A container escape or RCE vulnerability gives the attacker root on the host depending on Docker daemon configuration. This violates the principle of least privilege and fails common container security benchmarks (CIS Docker, Docker Scout).

**Fix:**
```dockerfile
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
COPY --from=builder /app/bin/booking-cli /app/booking-cli
USER appuser
```

---

### [HIGH] Config Loading Has No Validation for Required Fields

**File:** [internal/infrastructure/config/config.go:58-66](../../internal/infrastructure/config/config.go#L58-L66)

**Issue:** `LoadConfig` calls `cleanenv.ReadConfig` and returns. `PostgresConfig.DSN` has no `env-default` and no `env-required` tag. If `DATABASE_URL` is absent (e.g., misconfigured deploy), `DSN` is an empty string, the app starts, and the database connection fails at query time with a cryptic error — not at startup.

Other fields that should be required in production have soft defaults:
- `REDIS_ADDR` defaults to `localhost:6379` — correct for dev, wrong for Docker where the container is `booking_redis`.
- `KAFKA_BROKERS` defaults to `localhost:9092` — same concern.

**Impact:** Silent misconfiguration failures that surface as runtime errors rather than clear startup validation failures.

**Fix:** Add `env-required:"true"` to `DATABASE_URL`. Consider a post-load validation function:
```go
func (c *Config) Validate() error {
    if c.Postgres.DSN == "" {
        return fmt.Errorf("DATABASE_URL is required")
    }
    return nil
}
```
Call it after `LoadConfig` in `main.go`.

---

### [MEDIUM] Prometheus Scrape Has Duplicate / Unreachable Target

**File:** [deploy/prometheus/prometheus.yml:14-15](../../deploy/prometheus/prometheus.yml#L14-L15)

**Issue:** The `booking-service` job scrapes both `host.docker.internal:8080` and `app:8080`. Inside a Docker Compose network, `host.docker.internal` resolves to the host machine. If the app is only running in Docker (not also on the host), this target is permanently `DOWN`, polluting the Prometheus target list and generating spurious `up == 0` conditions.

**Impact:** False `up` metric failures; confusing target state in the Prometheus UI.

**Fix:** Remove `host.docker.internal:8080` when running fully containerized. If dev-host scraping is needed, use a separate `dev` scrape config guarded by a file-based service discovery or comment.

---

### [MEDIUM] Nginx /metrics Route Blocks Prometheus Scrape

**File:** [deploy/nginx/nginx.conf:35-38](../../deploy/nginx/nginx.conf#L35-L38)

**Issue:** The `location /metrics { deny all; }` block prevents any traffic from reaching `/metrics`, including Prometheus. But Prometheus is configured to scrape `app:8080` directly (not via Nginx). In practice this works today because Prometheus bypasses Nginx. However, if Nginx is placed in front of the metrics endpoint in future, scraping silently breaks with a 403.

The commented-out `allow 172.16.0.0/12;` line suggests this was intended to allow Docker-internal Prometheus access. Currently the allow directive is commented out and the deny is active — meaning if Nginx is ever made the single ingress, metrics are unreachable.

**Fix:** Either keep the direct `app:8080` scrape (clearly document that metrics are not routed through Nginx), or enable the subnet allow + deny with the Docker bridge range:
```nginx
location /metrics {
    allow 172.16.0.0/12;
    allow 10.0.0.0/8;
    deny all;
    proxy_pass http://ticket_service;
}
```

---

### [MEDIUM] Nginx Has No proxy_read_timeout / proxy_connect_timeout

**File:** [deploy/nginx/nginx.conf:25-31](../../deploy/nginx/nginx.conf#L25-L31)

**Issue:** No `proxy_read_timeout`, `proxy_connect_timeout`, or `proxy_send_timeout` directives are set on either location block. Nginx defaults to 60s read/send timeouts. Under high load (flash sale), if upstream Go handlers are slow, connections pile up silently until the 60s default fires.

No `keepalive` directive is set on the upstream block, meaning each proxy request opens a new TCP connection to the app container.

**Impact:** Connection overhead under flash-sale load; Nginx may exhaust its worker connections before the rate limit kicks in.

**Fix:**
```nginx
upstream ticket_service {
    server app:8080;
    keepalive 64;
}
# In the location block:
proxy_read_timeout 30s;
proxy_connect_timeout 5s;
proxy_http_version 1.1;
proxy_set_header Connection "";
```

---

### [MEDIUM] AlwaysSample in Production — No Sampling Configuration

**File:** [cmd/booking-cli/main.go:167](../../cmd/booking-cli/main.go#L167)

**Issue:** `trace.AlwaysSample()` is hardcoded. During a flash-sale stress test at 500+ VUS, every request generates a full trace, which floods Jaeger's in-memory store (the `all-in-one` image uses in-memory storage by default) and adds non-trivial latency to trace export.

**Impact:** Under load tests, Jaeger memory grows unboundedly. Trace export adds per-request overhead. In-memory storage means traces are lost on container restart.

**Fix:** Make the sampler configurable via an env var:
```go
samplerRatio := 0.1 // default 10%
if v := os.Getenv("OTEL_TRACES_SAMPLER_RATIO"); v != "" {
    samplerRatio, _ = strconv.ParseFloat(v, 64)
}
tp := trace.NewTracerProvider(
    trace.WithSampler(trace.ParentBased(trace.TraceIDRatioBased(samplerRatio))),
    ...
)
```
For Jaeger in production, configure a persistent backend (Elasticsearch/Cassandra) or use the Jaeger operator.

---

### [MEDIUM] Redis Has No Password — Unauthenticated Access

**File:** [deploy/redis/redis.conf](../../deploy/redis/redis.conf), [docker-compose.yml:57-65](../../docker-compose.yml#L57-L65)

**Issue:** `redis.conf` has no `requirepass` directive. Redis is bound to `0.0.0.0` and the port `6379` is exposed on the host (`ports: - "6379:6379"`). Any process on the host machine can connect without authentication.

**Impact:** On a shared dev machine or a misconfigured server, Redis data (inventory, buyer sets, stream events) is readable and writable by anyone. A malicious actor can flush inventory or inject fake stream events.

**Fix:** Add `requirepass ${REDIS_PASSWORD}` to `redis.conf` (templated or overridden via command arg). The `REDIS_PASSWORD` env var is already supported in `RedisConfig` ([config.go:34](../../internal/infrastructure/config/config.go#L34)) — it just needs to be wired to the Redis container.

---

### [MEDIUM] Prometheus Scrape Interval is 5s — Too Aggressive for Production

**File:** [deploy/prometheus/prometheus.yml:2](../../deploy/prometheus/prometheus.yml#L2)

**Issue:** `scrape_interval: 5s` globally means Prometheus scrapes every service every 5 seconds. The default is 15s. At 5s, counter rates computed over `[1m]` windows only have 12 samples, which can produce noisy rate() results.

**Impact:** Increased CPU and network overhead on the app (HTTP /metrics calls 12x/min), noisier short-window rate calculations, and larger TSDB write volume.

**Fix:** Set global `scrape_interval: 15s` for production and `evaluation_interval: 15s`. Keep 5s only for dev/stress profiling environments.

---

### [LOW] Grafana Dashboard Uses Deprecated `graph` Panel Type

**File:** [deploy/grafana/provisioning/dashboards/dashboard.json:L12](../../deploy/grafana/provisioning/dashboards/dashboard.json#L12), [L37](../../deploy/grafana/provisioning/dashboards/dashboard.json#L37)

**Issue:** All panels use `"type": "graph"`, which was deprecated in Grafana 8.0 and removed in Grafana 10. Since `grafana/grafana:latest` now resolves to Grafana 10+, these panels may render as "Panel plugin not found: graph" or fall back silently.

**Impact:** Dashboard is likely broken or degraded in the current pulled image.

**Fix:** Migrate panels to `"type": "timeseries"` (the replacement for `graph` in Grafana 8+). Update `schemaVersion` from 27 to 36.

---

### [LOW] Grafana Provisioning Has disableDeletion: false

**File:** [deploy/grafana/provisioning/dashboards/dashboard.yml:8](../../deploy/grafana/provisioning/dashboards/dashboard.yml#L8)

**Issue:** `disableDeletion: false` means dashboard changes made in the Grafana UI can be deleted by users even when provisioned from file. Combined with `updateIntervalSeconds: 10`, any UI edit is overwritten every 10 seconds.

**Impact:** Confusing developer experience — UI edits silently revert. If a developer iterates on the dashboard via the UI, they lose changes.

**Fix:** Set `disableDeletion: true` and `editable: false` if the dashboard is owned by code. Document that dashboard changes must be made in the JSON file.

---

### [LOW] verify-redis CLI Hardcodes localhost:6379

**File:** [cmd/verify-redis/main.go:17](../../cmd/verify-redis/main.go#L17)

**Issue:** `redis.NewClient(&redis.Options{Addr: "localhost:6379"})` ignores `REDIS_ADDR` and any config. The tool cannot be run against a non-local Redis without recompiling.

**Impact:** The tool cannot be used in CI or Docker environments where Redis is not on localhost.

**Fix:** Read address from `REDIS_ADDR` env var with `localhost:6379` as fallback:
```go
addr := os.Getenv("REDIS_ADDR")
if addr == "" {
    addr = "localhost:6379"
}
r := redis.NewClient(&redis.Options{Addr: addr})
```

---

### [LOW] Makefile DB_URL Contains Hardcoded Credentials

**File:** [Makefile:124](../../Makefile#L124)

**Issue:** `DB_URL=postgres://user:password@localhost:5433/booking?sslmode=disable` hardcodes the same credentials as `docker-compose.yml`. These appear in shell history and process lists when `migrate-up` is run.

**Fix:** Use `$(DATABASE_URL)` from environment or a `.env` sourced file, consistent with the Compose fix above.

---

### [LOW] reset-db Uses FLUSHALL with || true

**File:** [Makefile:110](../../Makefile#L110)

**Issue:** `@docker exec booking_redis redis-cli FLUSHALL || true` silently ignores Redis flush failures. If the Redis container is not running, the reset appears to succeed but Redis state is stale.

**Impact:** Misleading dev reset experience — a developer may run stress tests against stale inventory counts.

**Fix:** Remove `|| true` and let the target fail explicitly, or add an explicit check:
```makefile
@docker exec booking_redis redis-cli PING > /dev/null || (echo "Redis not running"; exit 1)
@docker exec booking_redis redis-cli FLUSHALL
```

---

### [NIT] Span Status Not Set on 4xx in Tracing Handler

**File:** [internal/infrastructure/api/handler_tracing.go:29](../../internal/infrastructure/api/handler_tracing.go#L29)

**Issue:** `span.SetStatus(codes.Error, ...)` is only set for `status >= 500`. Client errors (4xx) do not set span status. This is technically correct per OpenTelemetry HTTP semantic conventions (4xx is a client error, not a server error), but `span.RecordError` for 400/422 validation failures (e.g., sold-out or duplicate booking) would aid debugging.

**Recommendation:** Optionally call `span.AddEvent("client_error", ...)` for 4xx to capture these in traces without marking the span as errored.

---

### [NIT] Missing `migrate-status` Target in Makefile

**File:** [Makefile](../../Makefile)

**Issue:** The Makefile has `migrate-up`, `migrate-down`, `migrate-create`, and `migrate-force` but no `migrate-status` target to show the current migration version. Developers must run the CLI manually to check state.

---

### [NIT] Prometheus Alert `HighErrorRate` Has No `for` Alignment with Scrape Interval

**File:** [deploy/prometheus/alerts.yml:9](../../deploy/prometheus/alerts.yml#L9)

**Issue:** The `HighErrorRate` alert uses a 1-minute `rate()` window with `for: 1m`. At a 5s scrape interval this is fine, but if the scrape interval is raised to 15s (per the recommendation above), the `[1m]` window only covers 4 samples. Consider using `[5m]` for a more stable signal with `for: 2m`.

---

## Test-Coverage Gaps (in-scope only)

- `internal/infrastructure/observability/metrics.go` — No unit tests for `MetricsMiddleware`. The middleware logic (label selection via `c.FullPath()`, duration measurement) is untested. A table-driven test using `httptest.NewRecorder` + Gin's test mode would cover status label correctness.
- `internal/infrastructure/observability/worker_metrics.go` — No tests. The `prometheusWorkerMetrics` adapter has trivial delegation but the `init()` pre-initialization in `metrics.go` is untested; a test ensuring all expected label values are pre-seeded would prevent cardinality regressions.
- `internal/infrastructure/config/config.go` — No tests for `LoadConfig`. Missing-field behavior (empty `DATABASE_URL`) is unverified. A test with a temp YAML + missing env var would catch the absence of startup validation.
- `cmd/verify-redis/main.go` — The script is a manual tool, not part of `go test ./...`. It should either become a proper `_test.go` integration test or be moved to `scripts/` with clear documentation that it requires a running Redis.

---

## Follow-Up Questions

1. Is `jaegertracing/all-in-one` intended for production use? It uses in-memory trace storage and loses all data on restart. Is there a plan to move to a persistent Jaeger backend (e.g., with Elasticsearch) before going to real production?
2. The `booking-service` Prometheus scrape target includes `host.docker.internal:8080`. Is there a hybrid dev mode where the app runs on the host and only infra is in Docker? If so, document this explicitly — otherwise remove the host target.
3. `GF_SECURITY_ADMIN_PASSWORD=admin` — Is Grafana intentionally left with the default admin password? Is it accessible on a public interface in any environment?
4. Are there plans to add TLS termination at Nginx? Currently everything runs over plain HTTP, including the Prometheus scrape of `/metrics`, which exposes internal counters unencrypted.
5. The `config/config.yml` file is `COPY`'d into the Docker image ([Dockerfile:39](../../Dockerfile#L39)). Does this file contain any environment-specific values that should not be baked into the image?

---

## Out of Scope / Deferred

- Kafka consumer/producer error handling — reviewed in PR #4 (messaging-saga).
- Redis Lua script correctness (`deduct.lua`, `revert.lua`) — reviewed in PR #3 (concurrency-cache).
- API handler idempotency key validation — reviewed in PR #5 (api-payment).
- Postgres query correctness and index coverage — reviewed in PR #2 (persistence).
- Cross-cutting: The `order_id` and `user_id` values appear as span *attributes* in tracing decorators ([repositories_tracing.go:119](../../internal/infrastructure/persistence/postgres/repositories_tracing.go#L119)). This is fine for tracing (low-cardinality spans), but if similar values were ever added as Prometheus *labels*, that would cause a cardinality explosion. No such pattern was found in scope, but the distinction is worth noting in future metric additions.
