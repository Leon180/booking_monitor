# Review: API, Middleware & Payment

**Branch:** `review/api-payment` **Agent:** `go-reviewer` **Date:** 2026-04-11
**Scope:** `internal/infrastructure/api/**`, `internal/infrastructure/payment/**`, `internal/application/payment/**`, `cmd/booking-cli/**`

---

## Summary

The HTTP layer is structurally sound — gin.Recovery(), correlation-ID middleware, and tracing decorators are in place. However, four critical/high gaps exist: raw internal errors leak to clients on every error path, the HTTP server is launched without ReadTimeout/WriteTimeout bound to the configured values (slow-loris vector), there is no rate-limiting middleware anywhere in the chain, and the OTLP trace exporter runs over an unauthenticated plaintext gRPC connection. These should be resolved before a production deployment.

---

## Findings

### CRITICAL — Raw Internal Errors Exposed to Clients

**Files:**
- [`internal/infrastructure/api/handler.go:58`](../../internal/infrastructure/api/handler.go) — `ShouldBindQuery` error forwarded verbatim
- [`internal/infrastructure/api/handler.go:76`](../../internal/infrastructure/api/handler.go) — `GetBookingHistory` internal error forwarded
- [`internal/infrastructure/api/handler.go:95`](../../internal/infrastructure/api/handler.go) — `ShouldBindJSON` error forwarded
- [`internal/infrastructure/api/handler.go:127`](../../internal/infrastructure/api/handler.go) — catch-all `err.Error()` forwarded for 500s
- [`internal/infrastructure/api/handler.go:155,161`](../../internal/infrastructure/api/handler.go) — CreateEvent bind/service errors forwarded

On all non-sentinel error paths the raw `err.Error()` string is serialised into the response body. This can leak:
- PostgreSQL driver messages (e.g. `pq: duplicate key value violates unique constraint "orders_user_id_event_id_key"`)
- Redis/internal stack snippets on Gin's binding reflection errors
- File paths embedded in compile-time panic messages captured by Recovery

**Fix:** Map errors to a stable, opaque `{"error": "internal server error"}` for 5xx paths. Binding errors (`ShouldBindJSON`/`ShouldBindQuery`) are safe to forward as-is since they come from Gin's validator, but should be audited to confirm they never include stack traces.

---

### CRITICAL — HTTP Server Started Without Timeout Wiring (Slow-Loris / Resource Exhaustion)

**File:** [`cmd/booking-cli/main.go:248-250`](../../cmd/booking-cli/main.go)

```go
if err := r.Run(":" + cfg.Server.Port); err != nil { ...
```

`gin.Engine.Run` wraps `http.ListenAndServe` with `nil` server — it never reads `cfg.Server.ReadTimeout` or `cfg.Server.WriteTimeout`. Those values are configured and documented in [`internal/infrastructure/config/config.go:27-28`](../../internal/infrastructure/config/config.go) but are never applied. The effective timeouts are zero (unlimited), making the server trivially vulnerable to slow-loris attacks and connection exhaustion under load.

**Fix:**
```go
srv := &http.Server{
    Addr:         ":" + cfg.Server.Port,
    Handler:      r,
    ReadTimeout:  cfg.Server.ReadTimeout,
    WriteTimeout: cfg.Server.WriteTimeout,
    IdleTimeout:  cfg.Server.ReadTimeout * 2,
}
go func() {
    if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        log.Errorw("Server failed", "error", err)
    }
}()
```

Also keep a reference to `srv` in `OnStop` to call `srv.Shutdown(ctx)` for graceful drain.

---

### HIGH — No Rate Limiting on Any Endpoint

**File:** [`cmd/booking-cli/main.go:227-244`](../../cmd/booking-cli/main.go)

The middleware chain is: `gin.Recovery → LoggerMiddleware → CorrelationIDMiddleware → MetricsMiddleware`. There is no rate-limiting middleware registered globally or per-route — including the high-value `POST /api/v1/book` and the legacy `POST /book`. An unauthenticated caller can exhaust the Redis inventory slot, DB connections, or trigger unbounded Kafka message production with no throttle.

The project doc notes "Rate limiting on all public endpoints" as a security rule, and it was listed as implemented in a previous commit (`feat(infra): implement API gateway, rate limiting`). However, no `gin-contrib/limiter`, `golang.org/x/time/rate`, or equivalent is imported or applied.

**Fix:** Add a per-IP token-bucket middleware (e.g. `github.com/ulule/limiter/v3`) on the Gin group before `RegisterRoutes`, or an nginx `limit_req_zone` directive. The Nginx config should be audited separately.

---

### HIGH — Unvalidated `status` Query Parameter (Arbitrary String Cast to Domain Type)

**File:** [`internal/infrastructure/api/handler.go:70-71`](../../internal/infrastructure/api/handler.go)

```go
orserStatus = lo.ToPtr(domain.OrderStatus(*req.Status))
```

Any string value is cast directly to `domain.OrderStatus` without checking membership against the four defined constants (`pending`, `confirmed`, `failed`, `compensated`). This means `GET /api/v1/history?status='; DROP TABLE orders;--` becomes a valid `OrderStatus` that is forwarded to the repository layer. Whether the downstream SQL is parameterised (it should be — covered by PR #2) mitigates injection, but the handler still passes garbage domain values and the DB will silently return zero rows rather than returning a 400, making the API deceptive. There is also a typo: `orserStatus` (should be `orderStatus`).

**Fix:**
```go
func validOrderStatus(s string) (domain.OrderStatus, bool) {
    switch domain.OrderStatus(s) {
    case domain.OrderStatusConfirmed, domain.OrderStatusPending,
         domain.OrderStatusFailed, domain.OrderStatusCompensated:
        return domain.OrderStatus(s), true
    }
    return "", false
}
```
Return `400 Bad Request` on unrecognised values.

---

### HIGH — `gin.Recovery()` Logs to stdout Without Stack Trace to Structured Logger

**File:** [`cmd/booking-cli/main.go:228`](../../cmd/booking-cli/main.go)

`gin.Recovery()` uses Gin's default logger (writes coloured text to os.Stderr). It does not integrate with the injected `*zap.SugaredLogger` and therefore panic information never reaches your structured log pipeline or Jaeger trace. In a containerised environment where stderr is discarded, panics are invisible.

**Fix:** Replace with `gin.RecoveryWithWriter(...)` pointing to a zap-wrapped writer, or use a custom `CustomRecovery` middleware that calls `zap.SugaredLogger.Errorw` with the stack bytes and sets `span.SetStatus(codes.Error, ...)` on the current OTel span.

---

### HIGH — OTLP Trace Exporter Uses Unauthenticated Plaintext gRPC

**File:** [`cmd/booking-cli/main.go:161`](../../cmd/booking-cli/main.go)

```go
traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
```

`WithInsecure()` disables TLS on the gRPC connection to the OTLP collector. Any traces — which include HTTP route names, correlation IDs, order IDs, and user IDs — are transmitted in plaintext. If the collector is co-located in Docker this is lower risk, but the option should be gated on an env flag (e.g. `OTEL_INSECURE=true`) rather than hardcoded, to prevent accidental use against a remote collector.

---

### HIGH — Idempotency Set Error Silently Discarded

**File:** [`internal/infrastructure/api/handler.go:138`](../../internal/infrastructure/api/handler.go)

```go
_ = h.idempotencyRepo.Set(ctx, idempotencyKey, &domain.IdempotencyResult{...})
```

A Redis write failure at this line means the idempotency record is never stored. The client receives a 200 but a retry with the same key will re-execute the booking rather than replaying the cached result — breaking the idempotency contract silently. The error should at minimum be logged at `Warn` level so operators know the idempotency store is degraded.

---

### MEDIUM — `HandleViewEvent` Returns Stub Response and No Event Lookup

**File:** [`internal/infrastructure/api/handler.go:168-172`](../../internal/infrastructure/api/handler.go)

`GET /api/v1/events/:id` returns `{"message":"View event","event_id":"<raw string>"}` without parsing or validating the path parameter as an integer, and without querying the database. The `eventService` injected into the handler is never called here. This is likely incomplete work, but the path parameter `c.Param("id")` is currently returned verbatim — an attacker can inject arbitrary strings into the JSON response (though Go's `encoding/json` escapes them, so XSS is not a direct risk).

---

### MEDIUM — Payment Amount Uses `float64` (Precision Risk)

**Files:**
- [`internal/domain/payment.go:26`](../../internal/domain/payment.go)
- [`internal/infrastructure/payment/mock_gateway.go:32`](../../internal/infrastructure/payment/mock_gateway.go)

`amount float64` is used for monetary values throughout the payment domain. IEEE-754 double precision is lossy for decimal fractions (e.g. `0.1 + 0.2 ≠ 0.3`). While the mock gateway does not actually charge, the interface contract established here will carry forward to any real gateway integration. This should be `int64` (amount in minor units / cents) or `github.com/shopspring/decimal` to prevent rounding errors on real charges.

---

### MEDIUM — Pagination `size` Parameter Has No Upper Bound

**File:** [`internal/infrastructure/api/handler.go:65-67`](../../internal/infrastructure/api/handler.go)

```go
if req.Size < 1 {
    req.Size = 10
}
```

There is no maximum cap on `size`. A request with `?size=1000000` will attempt to fetch 1 million rows from PostgreSQL in a single query, saturating DB connections and memory. Cap at a reasonable max (e.g. 100).

---

### MEDIUM — `errors.Is` Not Used for Sentinel Error Comparison

**File:** [`internal/infrastructure/api/handler.go:119-123`](../../internal/infrastructure/api/handler.go)

```go
switch err {
case domain.ErrSoldOut:
case domain.ErrUserAlreadyBought:
```

Direct equality comparison in a type switch breaks if any middleware or service layer wraps the sentinel via `fmt.Errorf("...: %w", err)`. Use `errors.Is(err, domain.ErrSoldOut)` with an if-else chain instead.

---

### LOW — `provideDB` Does Not Return Error After 10 Failed Pings

**File:** [`cmd/booking-cli/main.go:289-295`](../../cmd/booking-cli/main.go)

If all 10 ping retries fail, the function returns the non-nil `db` handle with the last `err` value discarded (the `for` loop exits without returning an error). The Fx app will proceed with a broken DB handle and fail at the first query rather than at startup. Return `err` after the loop:

```go
if err != nil {
    return nil, fmt.Errorf("database unreachable after retries: %w", err)
}
```

---

### LOW — Legacy `POST /book` Route Registered Outside the Rate-Limit Group

**File:** [`cmd/booking-cli/main.go:244`](../../cmd/booking-cli/main.go)

```go
r.POST("/book", handler.HandleBook) // Legacy
```

Even if rate limiting is added to the `/api/v1` group later, this legacy route on the root will bypass it. Either remove it or apply the same middleware group.

---

### LOW — `initTracer` Errors Are Logged but Execution Continues

**File:** [`cmd/booking-cli/main.go:155-163`](../../cmd/booking-cli/main.go)

Both `resource.New` and `otlptracegrpc.New` errors are logged at `Printf` level and then silently ignored. A nil/broken exporter produces a no-op `TracerProvider` but operators receive no signal that tracing is disabled. These should `log.Fatalf` or at least be surfaced via the fx error return channel.

---

### NIT — Typo in Variable Name

**File:** [`internal/infrastructure/api/handler.go:69`](../../internal/infrastructure/api/handler.go)

`orserStatus` should be `orderStatus`.

---

### NIT — `payment/service.go` Accepts Zero Amount

**File:** [`internal/application/payment/service.go:44-47`](../../internal/application/payment/service.go)

```go
if event.Amount < 0 {
    return nil // Drop
}
```

`amount == 0` is accepted and forwarded to the gateway. A zero-amount charge is likely invalid for a real gateway and should be rejected with an error.

---

## Test-Coverage Gaps

| Area | Gap |
|------|-----|
| `HandleBook` | No test for `Idempotency-Key` replay path (cache hit returns stored body) |
| `HandleBook` | No test for `Idempotency-Key` > 128 chars rejection |
| `HandleListBookings` | No test for unrecognised `status` values (currently silently accepted) |
| `HandleListBookings` | No test for `size` overflow / max-cap enforcement (gap: cap does not exist yet) |
| `HandleBook` — sentinel errors | No test that a wrapped `ErrSoldOut` (via `fmt.Errorf("%w", domain.ErrSoldOut)`) is handled correctly |
| `payment/service.go` | No test for `amount == 0` path |
| `provideDB` | No test / integration check for DB unreachable-after-retries returning an error |
| Server startup | No test that `ReadTimeout`/`WriteTimeout` are actually applied to the `http.Server` |

---

## Follow-Up Questions

1. Is there an Nginx `limit_req_zone` directive in the Docker config that is intended to act as the rate limiter? If so, the handler layer comment should document this, and the legacy `/book` route must be behind the same Nginx config.
2. The `ServerConfig` has `ReadTimeout`/`WriteTimeout` fields — were these always ignored, or was there previously a custom `http.Server` construction that was removed?
3. Is there a planned real payment gateway integration? If so, `float64` for `amount` must be changed before that work starts to avoid precision bugs in production charges.
4. `gin.Recovery()` — is there a requirement to return a standard error body structure (e.g. `{"error":"internal server error"}`) from panics, or is the default Gin recovery response acceptable?

---

## Out of Scope / Deferred

- Kafka consumer code for `order.created` topic — covered by PR #4 (messaging-saga).
- PostgreSQL query parameterisation — covered by PR #2 (persistence).
- Redis Lua script idempotency (`revert.lua`, `deduct.lua`) — covered by PR #3 (concurrency-cache).
- Outbox relay leader election — covered by PR #2.
- Auth/JWT: no authentication layer is present. Assumed out of scope for current phase (flash-sale simulation). Should be tracked as a future requirement if the API is exposed externally.
