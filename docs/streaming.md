# Streaming Production Gotchas

> Companion to [`docs/design/admin_event_streaming.md`](design/admin_event_streaming.md).
> This document records the production gotchas encountered while
> implementing the admin event SSE pipeline (PR #121). Each section
> is short on purpose: it's a checklist for future engineers (and
> interview talking points), not a tutorial.

---

## 1. Nginx response buffering

**Symptom**: SSE client receives nothing until the connection times out, then everything at once.

**Cause**: `proxy_buffering on` (the default) tells nginx to accumulate the upstream response before forwarding. For streaming responses this batches every event into the buffer.

**Fix**: Two layers (see [§Q14](design/admin_event_streaming.md)):
- `deploy/nginx/nginx.conf` location block: `proxy_buffering off; gzip off; proxy_cache off;`
- App response header: `X-Accel-Buffering: no` (nginx honours per-response override)

**Source**: nginx official docs — https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffering

---

## 2. HTTP/1.1 6-connection-per-origin browser limit

**Symptom**: Open admin tab in 7+ browser windows on the same origin — the 7th hangs.

**Cause**: Browsers cap concurrent HTTP/1.1 connections to the same origin at 6. SSE is one connection per tab. Most ops won't hit this, but worth knowing.

**Mitigation options** (none implemented in PR #121):
- Run nginx on HTTP/2 to the client (no 6-connection limit)
- Use SharedWorker so multiple tabs share one SSE connection
- Document the constraint

---

## 3. Idle timeout from intermediate proxies

**Symptom**: SSE connection drops every N seconds even though events should keep it alive.

**Cause**: Intermediate proxies (nginx, AWS ALB, GCP LB, Cloudflare) close idle connections to free resources. Default timeouts vary:
- nginx: 60s
- AWS ALB: 60s
- GCP HTTP LB: **30s** (strictest)
- Cloudflare: 900s

**Fix**: 30-second comment-line heartbeats from the server. WHATWG SSE spec § 9.2.7 explicitly recommends this pattern.

**Code**: [`internal/infrastructure/api/sse/handler.go`](../internal/infrastructure/api/sse/handler.go) — `time.NewTicker(30 * time.Second)` writing `: heartbeat\n\n`.

**Limitation**: 30s matches GCP HTTP LB exactly with no margin. Deploy with `ADMIN_STREAM_HEARTBEAT_INTERVAL=25s` if your LB has a stricter timeout.

---

## 4. `Last-Event-ID` resumption protocol

**Implementation choice**: Redis Stream ID on the wire (see [§Q6](design/admin_event_streaming.md)).

**Why not UUIDv7**: Stream ID is transport-native (no mapping table needed). For an admin-only endpoint with low migration cost, this is the right trade-off. End-user-facing SSE endpoints, if added later, should use UUIDv7 from day 1.

**Replay window**: bounded by `events:admin:stream` MAXLEN ~ 100,000 (see [`internal/infrastructure/cache/admin_event_bus.go`](../internal/infrastructure/cache/admin_event_bus.go) `DefaultAdminStreamMaxLen`). At the Stage 5 ceiling of ~5,500 events/s, the replay window is ~18 seconds of burst history. At idle rates it covers days-to-weeks. A client that drops for longer than the window will NOT get a full replay — see edge case below.

**Edge case**: Last-Event-ID older than stream's first entry (because of MAXLEN trim) → server emits synthetic `event: stream_truncated` and resumes from live tail. Client JS should display "you missed N events; resumed from live" to the operator.

**Metric**: `admin_sse_events_truncated_total` — non-zero during flash sales is expected; sustained non-zero outside flash periods means clients are disconnecting for longer than the replay window can cover. Cross-check `admin_event_bus_published_total` rate against the implied seconds-of-history budget if this happens.

---

## 5. Reconnection storm on server restart

**Symptom**: Deploy → all N admin tabs reconnect synchronously at the default 3s SSE retry → burst of XRANGE replays and JWT validations hits the new pod simultaneously.

**Fix**: Graceful shutdown sends jittered `retry: <2000-8000>` per client (Q15). Each client gets a different reconnect delay → reconnect distribution across 6 seconds instead of synchronous.

**Code**: [`Handler.BroadcastRetryHints`](../internal/infrastructure/api/sse/handler.go) called from `installAdminStreamLifecycle` OnStop.

**Beyond N=500**: Cortex pattern — add explicit jitter to heartbeat phase. Not implemented for < 100 admin clients.

---

## 6. Goroutine leak on client disconnect

**Risk**: SSE handler spawns one writer goroutine per connection. If the goroutine doesn't exit on client disconnect, you leak ~80KB stack per connection → memory grows unbounded.

**Defence**:
- `c.Request.Context().Done()` selectable case in writer loop — Gin cancels this when client TCP closes
- Hub.Run closes every `client.Send` on shutdown — writers see closed channel and exit
- 1000-iteration connect/disconnect test in [`test/integration/sse/`](../test/integration/sse/) verifies no leak

**Library**: `go.uber.org/goleak` runs in TestMain — fails the test suite if any goroutine stays after tests complete.

---

## 7. Auth refresh during long sessions

**Limitation**: JWT TTL is capped server-side at 1h (configurable). After expiry, the EventSource connection becomes invalid and clients receive 401 on reconnect.

**Workaround**: Admin dashboard JS should detect 401 on reconnect, prompt operator to re-mint a token (or implement a refresh endpoint — out of PR scope).

**Why not cookie session**: Would require login flow + session store + CSRF — out of scope. JWT is the right level for an internal admin endpoint.

---

## 8. Backpressure: slow consumer drop policy

**Choice**: Drop the client, not the message (see [§Q10](design/admin_event_streaming.md)).

**Rationale**: Admin contract is "see all events". Silent message-drop diverges client state from server state with no recovery. Closing the connection forces EventSource auto-reconnect with `Last-Event-ID` → XRANGE replay restores full state, bounded by the MAXLEN window (see § 4 — at peak rate this is ~18 s of history; longer disconnects fall back to `stream_truncated` + resume-from-live).

**Trade-off**: ~50–100ms reconnect cost vs silent state divergence. Worth it.

**Metric**: `admin_sse_clients_dropped_total{reason="slow_consumer"}` — non-zero indicates a client whose TCP write-buffer is full (likely network issue or dev-tools tab idle).

---

## 9. `gorilla/websocket` panics on concurrent writes (avoided here)

We use SSE not WebSocket, but documenting the lesson because it informed library choice:

```go
// gorilla/websocket/conn.go line 626
panic("concurrent write to websocket connection")
```

If we ever add WebSocket, use `coder/websocket` (formerly `nhooyr.io/websocket`) which has internal concurrent-write safety. See [`internal/infrastructure/api/sse/`](../internal/infrastructure/api/sse/) design rationale.

---

## 10. TCP keepalive vs application heartbeat — different jobs

**Heartbeat (30s comment line)**: prevents proxy idle-timeout. Does NOT detect dead clients reliably (see Go issue #20553 — `Conn.Write` can buffer to a stale socket).

**TCP keepalive** (Linux default 15s in Go 1.21+): kernel-level dead-peer detection. Active by default in Go's net package.

**Gin's `c.Request.Context().Done()`**: HTTP-level cancellation when client TCP closes. This is the actual dead-client signal the writer loop relies on.

→ **Heartbeat = keep proxy alive; ctx cancellation = detect dead client.** Different mechanisms, different jobs.

---

## 11. Prometheus cardinality discipline

**Don't**: Label by `order_id`, `ticket_type_id`, `client_id`, or any high-cardinality value. We almost did with `inventory_low_alerts_total{ticket_type_id=...}` — would create thousands of time series.

**Do**: Label by enum (event_type with 8 values, reason with 1-3 values). Keep total cardinality under ~100 per metric.

**Code**: [`internal/infrastructure/observability/metrics_inventory_low.go`](../internal/infrastructure/observability/metrics_inventory_low.go) — unlabeled counter with explicit doc explaining why.

---

## 12. Graceful shutdown order: hub first, HTTP last

**Wrong order**: `srv.Shutdown(ctx)` first blocks until all in-flight requests return — but long-lived SSE handlers never return naturally → deadlock.

**Right order** (see [§Q15](design/admin_event_streaming.md)):
1. Set `shuttingDown=true` (reject new connections with 503)
2. Broadcast jittered `retry:` hint
3. Cancel hub context (subscriber stops, hub drains, closes client.Send)
4. Writer goroutines see `c.Send` closed → exit → in-flight handlers return
5. `srv.Shutdown(ctx)` now drains cleanly

---

## 13. SSE/Prometheus/PG: three sources of truth — by design

**Don't**: Try to reconcile SSE delivery with Prometheus counters or DB rows.

**Do**: Trust them at their respective levels:
- **PG audit tables** = canonical individual records (`orders`, `order_status_history`, `saga_compensations`)
- **Prometheus counters** = authoritative aggregate counts (in-memory atomic, never drops)
- **SSE stream** = best-effort real-time feed (at-most-once delivery)

Dashboard panels reading from Prometheus are accurate; SSE timeline may miss events under backpressure. Both are useful at their level.

**Why this works**: The metric and the bus.Publish() are emitted at the same code path. The metric uses atomic add (cannot fail). The bus uses bounded async drop (can lose under backpressure). PG audit is the SSOT for compliance/post-mortem queries.

---

## 14. Multi-pod deployment requires zero coordination

**Setup**: N booking-cli pods behind k8s Service, sharing one Redis.

**Why it works without sticky session**:
- Each pod's subscriber XREADs the same `events:admin:stream` independently (NOT XREADGROUP — no consumer group, every pod sees every event)
- Each pod's hub broadcasts to ITS clients
- Client load-balanced randomly across pods → all pods see all events → all clients receive all events
- Reconnect lands on a different pod → that pod uses Last-Event-ID to XRANGE-replay → no missed events (within the MAXLEN ~ 100k replay window; § 4)

**Set in k8s**: `sessionAffinity: None` on the Service. Confirmed via § Q9 + Centrifugo Redis engine pattern.

---

## 15. Configuration knobs you should know about

| Env var | Default | Purpose |
|---|---|---|
| `ADMIN_STREAM_JWT_SECRET` | (empty) | HS256 signing key. Empty → endpoint 503s all requests (fail-closed). |
| `ADMIN_STREAM_JWT_MAX_TTL` | 1h | Server-enforced TTL cap. Token requesting longer is rejected. |

Tuning the bus / hub / subscriber / handler is via code constants today (see `internal/infrastructure/cache/admin_event_bus.go`, `internal/infrastructure/api/sse/*.go`). Promoting these to env vars is a follow-up PR.

---

## 16. Why not WebSocket / Mercure / Centrifugo?

Documented in [`docs/design/admin_event_streaming.md`](design/admin_event_streaming.md) — short version:

- **WebSocket**: Our admin endpoint is one-way push, no bidirectional commands needed. SSE is simpler and proxy-friendly. (See § Q9 grill discussion.)
- **Mercure**: A standalone hub server (Caddy plugin). Adopting it would mean adding another service to deploy/monitor. Too much abstraction for one internal endpoint.
- **Centrifugo**: Multi-tenant scale broker (~10k+ connections). Over-engineered for < 100 admin clients.

Our implementation is ~1,500 LOC end-to-end. Mercure/Centrifugo would be operational overhead for less code in our `internal/`.

---

## 17. Reading references

Production-grade Go streaming code worth reading:
- **Mercure hub**: https://github.com/dunglas/mercure/blob/main/subscribe.go (Last-Event-ID replay logic)
- **Gorilla chat hub**: https://github.com/gorilla/websocket/blob/master/examples/chat/hub.go (canonical channel-based Run loop, ~50 LOC)
- **OpenTelemetry BatchSpanProcessor**: https://github.com/open-telemetry/opentelemetry-go/blob/main/sdk/trace/batch_span_processor.go (bounded queue + drop pattern)
- **go.uber.org/goleak source**: https://github.com/uber-go/goleak/blob/master/leaks.go (~500 LOC — readable in one sitting)

Standards:
- WHATWG SSE spec: https://html.spec.whatwg.org/multipage/server-sent-events.html
- RFC 9562 (UUIDv7): https://datatracker.ietf.org/doc/rfc9562/

Industry case studies:
- Uber RAMEN push platform: https://www.uber.com/blog/real-time-push-platform/ — quantifies "80% of API gateway requests were polling"
- Stripe webhooks vs polling: https://docs.stripe.com/payments/payment-intents/verifying-status
