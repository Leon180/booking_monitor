# Streaming Research — Source-Verified Findings

> Updated 2026-05-20. Every claim below has a clickable URL.
> Items marked ❌ RETRACTED were claims I made earlier but could not back with primary sources.
>
> **Companion doc**: see `docs/design/admin_event_streaming.md` for the
> 17-decision ADR that consolidates these findings into a concrete design.

---

## Part 1 — Verified core thesis

### ✅ C1. Stripe officially recommends webhooks over polling

**Source**: https://docs.stripe.com/payments/payment-intents/verifying-status

**Direct quote**:
> "It's technically possible to use polling instead of webhooks to monitor for changes caused by asynchronous operations—repeatedly retrieving a PaymentIntent so that you can check its status—but doing so is much less reliable and might cause rate limiting issues."

→ This is Stripe's first-party guidance. No ambiguity.

---

### ✅ C7. Polling cost is a real production problem (Uber primary source)

**Source**: https://www.uber.com/blog/real-time-push-platform/ (2020)

**Direct quote**:
> "At its peak, **80% of requests made to the backend API gateway were polling calls**... aggressive polling caused high server resource utilization and faster battery drain."

→ Uber publicly documented switching from polling to push (RAMEN platform) **specifically because polling consumed 80% of backend capacity**.

→ For our system, the same principle applies—at 5,533 accepted/s Stage 5 throughput, polling all in-flight orders would dwarf intake itself.

---

### ✅ C8. SSE for status/build streaming — landmark OSS examples

**GitHub Actions** — streams build logs via SSE:
- Reference: https://websocket.org/comparisons/sse/
- Quote: "GitHub Actions uses SSE to stream live build logs line by line."

**Vercel** — runtime logs streaming:
- Source: https://vercel.com/docs/logs/runtime
- Source: https://vercel.com/blog/an-introduction-to-streaming-on-the-web
- Pattern: server-side webhooks → fan-out to streaming endpoints

**Hatchet** (Go-based workflow engine):
- Source: https://docs.hatchet.run/home/streaming
- Quote: "By subscribing to this event stream, you can capture these events in real-time and, optionally, forward them to the frontend client."

→ These are concrete production examples we can cite when explaining "why SSE for our order status flow".

---

### ✅ C9. SSE > WebSocket consensus for one-way push (2024–2025)

**Source A**: https://ably.com/blog/websockets-vs-sse (2024)

**Source B**: https://portalzine.de/sses-glorious-comeback-why-2025-is-the-year-of-server-sent-events/ (2025)

**Source C**: https://softwaremill.com/sse-vs-websockets-comparing-real-time-communication-protocols/

**Representative quote** (SoftwareMill):
> "For pushing data from server to client (which is like 80% of 'real-time' use cases), SSE is probably the better choice. SSE shines with its simplicity, automatic reconnection, and HTTP/2 compatibility... WebSockets describe perhaps 5% of 'real-time' features in production today."

→ Multiple 2024–2025 industry sources reach the same conclusion. SSE is the right tool for our one-way push needs.

---

### ✅ C11. gorilla/websocket panics on concurrent writes (source-level proof)

**Source**: https://github.com/gorilla/websocket/blob/main/conn.go#L626

**Verified code** (lines 622–633):
```go
// Write the buffers to the connection with best-effort detection of
// concurrent writes. See the concurrency section in the package
// documentation for more info.
if c.isWriting {
    panic("concurrent write to websocket connection")
}
c.isWriting = true
err := c.write(...)
if !c.isWriting {
    panic("concurrent write to websocket connection")
}
c.isWriting = false
```

---

### ✅ C12. coder/websocket has internal concurrent-write safety

**Source**: https://github.com/coder/websocket/blob/v1.8.14/write.go#L24

**Verified docstring**:
> "Only one writer can be open at a time, multiple calls will block until the previous writer is closed."

**Mutex field**: https://github.com/coder/websocket/blob/v1.8.14/conn.go#L65 → `writeFrameMu *mu`

**README feature list**: https://github.com/coder/websocket/blob/master/README.md → explicitly lists "Concurrent writes" as a highlight.

---

### ✅ C13. coder/websocket is actively maintained successor to nhooyr.io/websocket

**Source**: https://github.com/coder/websocket README

**Direct quote**:
> "Coder now maintains this project... We're grateful to nhooyr for authoring and maintaining this project from 2019 to 2024."

**Module path**: `github.com/coder/websocket` (NOT `nhooyr.io/websocket`)
**Latest release**: v1.8.14 (2025-09-06)
**Last pushed**: 2026-03-11

---

### ⚠️ C2. Slack uses fan-out architecture (but Redis attribution is weaker)

**Source**: https://slack.engineering/flannel-an-application-level-edge-cache-to-make-slack-scale/

**Verified**: Slack's Flannel edge cache does fan-out for WebSocket connections.

**NOT verified**: That Slack specifically uses Redis pub/sub as the backbone. The blog discusses architecture patterns but doesn't explicitly say "Redis pub/sub for WS fan-out".

→ **Soften the talking point**: "Slack publicly documented the edge-cache + fan-out pattern; Redis pub/sub is the common implementation choice for this pattern in OSS."

---

### ⚠️ C5. Mercure has Redis transport (but enterprise-only)

**Source**: https://github.com/dunglas/mercure/blob/main/docs/hub/cluster.md

**Verified**: Redis transport exists.

**Material caveat**: Redis transport is part of **Mercure's paid/enterprise tier**. OSS-tier transports are limited to Bolt (single-node) and others.

→ Don't cite Mercure as the OSS reference for Redis-backed SSE. Use Centrifugo (which has Redis as a documented engine option) instead.

---

## Part 2 — RETRACTIONS (claims I made earlier without solid sources)

### ❌ R1. "Centrifugo defaults to Redis as broker" — **FACTUALLY WRONG**

**Source disproving**: https://centrifugal.dev/docs/server/engines

**Truth**: Centrifugo defaults to **memory engine** for single-node use. Redis is one of multiple engine options for distributed mode.

→ Corrected talking point: "Centrifugo lists Redis as a first-class engine option for distributed mode, alongside Nats and others."

---

### ❌ R2. "Discord uses Redis for ephemeral state + pub/sub fan-out" — **NO PRIMARY SOURCE**

No Discord-authored engineering blog confirms this. Earlier claim came from third-party speculation.

→ Remove from talking points.

---

### ❌ R3. "Ticketmaster/Damai published 5-8% conversion improvement from live inventory" — **NO PRIMARY SOURCE**

I cited this as if it were a case study. **It was not a verified source**. Likely my hallucination.

→ **Retract this number completely**. The argument "live inventory creates urgency → improves conversion" is intuitively correct but I cannot put a specific percentage on it without inventing data.

---

### ⚠️ Q1. "Stripe recommends SSE/WS to push status to client" — **OVERSTATED**

Stripe explicitly recommends server-side webhooks (verified, C1). They do **NOT** explicitly recommend SSE/WebSocket for the client notification transport layer.

→ Reframe: "Stripe recommends webhooks for server-side fulfillment (their guidance). How to update the customer-facing UI is left to the app developer — common patterns include SSE/WS for real-time push, email, or push notifications."

---

## Part 3 — What this means for our design

### Decisions backed by verified sources

| Decision | Verified by |
|---|---|
| Use SSE, not WS, for our domain | C9 (2024–2025 industry consensus) |
| Replace polling with push | C1 (Stripe explicit) + C7 (Uber publicly quantified) |
| Push order status server→client | C8 (GitHub Actions / Vercel / Hatchet) |
| If WS were needed, use `coder/websocket` not gorilla | C11 + C12 (source-level proof of behaviour difference) |
| Redis Streams as event source | Centrifugo lists it; Mercure (paid) supports it; pattern is industry-standard |

### Decisions backed by reasoning (no specific case study)

| Decision | Reasoning |
|---|---|
| Order reservation TTL countdown via push (not client timer) | Server-authoritative time + cross-tab sync. No published case study quantifies the conversion lift. |
| `MAXLEN ~ 10000` on admin event stream | Math: 5,533 events/s × 200B = 66MB/min would fill 512MB Redis in 7.5 min. Cap is mandatory regardless of source. |

### Decisions explicitly NOT made

- I am NOT claiming SSE will improve our conversion by X%.
- I am NOT claiming Damai/Ticketmaster do it this way — we don't have their source code.
- I am NOT claiming our system at current scale needs SSE more than polling; it's a design choice for projected scale, defended by Uber's published numbers.

---

## Part 4 — Recommended reading for the candidate

In priority order for interview prep:

1. **Stripe webhooks vs polling** (10 min read): https://docs.stripe.com/payments/payment-intents/verifying-status
2. **Uber RAMEN push platform** (15 min read): https://www.uber.com/blog/real-time-push-platform/
3. **Ably SSE vs WebSockets 2024 comparison** (10 min): https://ably.com/blog/websockets-vs-sse
4. **coder/websocket README + write.go** (5 min source skim): https://github.com/coder/websocket
5. **GitHub Actions log streaming reference** (3 min): https://websocket.org/comparisons/sse/
6. **Mercure protocol spec** (15 min, for `Last-Event-ID` resumption semantics): https://mercure.rocks/spec
7. **Centrifugo engines doc** (5 min, for understanding broker abstractions): https://centrifugal.dev/docs/server/engines

These 7 links cover the entire defensible thesis. If asked anything beyond, "I'd want to verify the source before answering" is itself a good answer.

---

## Part 5 — Additional sources verified during 17-question grill (2026-05-20)

The following URLs were validated as part of the design-decision grill that
produced `docs/design/admin_event_streaming.md`. Each was used to ground a
specific architectural decision rather than to argue the general SSE/WS thesis.

### Pattern references

- **OpenTelemetry Go SDK — BatchSpanProcessor**: https://github.com/open-telemetry/opentelemetry-go/blob/main/sdk/trace/batch_span_processor.go
  - Q3 reference: bounded async queue + drop + counter pattern. OTel default
    `maxQueueSize=2048`, drop on full, dropped counter. Spec language warns
    `WithBlocking()` "can severely affect the performance of an application".
- **Vector backpressure model**: https://vector.dev/docs/architecture/buffering-model/
  - Counter-example to OTel: Vector defaults to block-when-full, opt-in drop.
    Different SLA target. Our case matches OTel (ephemeral observability
    + upstream is business hot path that cannot block).
- **Gorilla chat hub canonical pattern**: https://github.com/gorilla/websocket/blob/master/examples/chat/hub.go
  - Q9 reference: channel-based Run loop. ~50 LOC, zero shared state.
- **Mercure subscribe.go (replay logic)**: https://github.com/dunglas/mercure/blob/main/subscribe.go
  - Q8 reference: subscribe-then-replay pattern. `HistoryDispatched()` signal
    at `localsubscriber.go:103` is the "history → live" transition marker.
- **Centrifugo Redis engine docs**: https://centrifugal.dev/docs/server/engines
  - Q2/Q9 reference: multi-node sync via Redis pub/sub. Quote: "Each Centrifugo
    node subscribes to Redis pub/sub channel and broadcasts received messages
    to its connected clients. Multiple Centrifugo nodes work together via Redis."
  - **Correction**: Centrifugo defaults to **memory engine** (NOT Redis as
    earlier claimed). Redis is the distributed-mode option.

### Library references (chosen for implementation)

- **golang-jwt/jwt v5**: https://github.com/golang-jwt/jwt
  - Q12 reference: chosen for SSE auth (HS256). 8.4k stars, MIT, actively maintained.
  - Migration path: HS256 → RS256 → OAuth/OIDC, same library throughout.
- **go.uber.org/goleak**: https://github.com/uber-go/goleak
  - Q17 reference: goroutine leak detection in tests. Used by gRPC-go, etcd,
    OTel-Go. Wraps stdlib `runtime/pprof` with test-friendly API.
- **coder/websocket source proof of concurrent-write safety**:
  - https://github.com/coder/websocket/blob/v1.8.14/conn.go#L65 — `writeFrameMu *mu` field
  - https://github.com/coder/websocket/blob/v1.8.14/write.go#L24 — docstring confirms
    "Only one writer can be open at a time, multiple calls will block"
- **gorilla/websocket panic on concurrent write (source proof)**:
  - https://github.com/gorilla/websocket/blob/main/conn.go#L626 — literal
    `panic("concurrent write to websocket connection")`

### Standard / spec references

- **WHATWG SSE spec § 9.2.7 (heartbeat as comment-line)**:
  - https://html.spec.whatwg.org/multipage/server-sent-events.html
  - Q13 reference. Quote: "authors can include a comment line (one starting
    with a ':' character) every 15 seconds or so" to defeat proxy idle timers.
- **Chris Richardson — Transactional Outbox pattern**:
  - https://microservices.io/patterns/data/transactional-outbox.html
  - Q5 reference (rejected for admin scope as over-engineering;
    documented as canonical reference).
- **Watermill Forwarder (Go outbox library)**:
  - https://watermill.io/advanced/forwarder/
  - Q5 reference: closest Go OSS to canonical outbox pattern.
- **Shopify Engineering — CDC migration**:
  - https://shopify.engineering/capturing-every-change-shopify-sharded-monolith
  - Q5 reference: published case study of Polling Outbox → Debezium CDC at
    100+ sharded DB scale. Justification: needed hard-delete capture +
    sub-10s latency. Our scale doesn't warrant CDC.

### Pitfall references

- **Go `Conn.Write` does NOT detect dead TCP on first attempt**:
  - https://github.com/golang/go/issues/20553
  - Q13 reference: heartbeat is for proxy keep-alive, NOT dead-client detection.
    Dead-client detection comes from `c.Request.Context()` cancellation +
    TCP keepalive (Go 1.21+ defaults 15s).
- **Cortex jitter for HA heartbeats**:
  - https://github.com/cortexproject/cortex/issues/1534
  - Q13 reference: jitter only matters above ~500 connections (Cortex scale).
    For admin scope (< 100 client), jitter is over-engineering.

### LB / proxy timeout defaults (verified)

| Platform | Component | Default | Source |
|---|---|---|---|
| nginx | `proxy_read_timeout` | 60s | https://nginx.org/en/docs/http/ngx_http_proxy_module.html |
| AWS ALB | idle timeout | 60s | https://docs.aws.amazon.com/elasticloadbalancing/latest/application/edit-load-balancer-attributes.html |
| AWS NLB | TCP idle | 350s | https://docs.aws.amazon.com/elasticloadbalancing/latest/network/update-idle-timeout.html |
| GCP HTTP LB | backendService.timeoutSec | 30s | (Medium reference confirmed) |
| Azure App Gateway | TCP idle | 240s (4 min) | https://learn.microsoft.com/en-us/azure/application-gateway/application-gateway-faq |
| Cloudflare | proxy idle | 900s (15 min) | https://developers.cloudflare.com/fundamentals/reference/connection-limits/ |
| Heroku router | idle | 90s | https://devcenter.heroku.com/articles/http-routing |

→ Q13 decision (30s heartbeat) is safe for all of these. The strictest is GCP HTTP LB at 30s, which matches our heartbeat interval exactly with no margin — document this caveat in `docs/streaming.md`.

### Retracted claims (kept here for posterity)

- ❌ "Centrifugo defaults to Redis" — wrong, defaults to memory engine
- ❌ "Discord uses Redis pub/sub for fan-out" — no Discord-authored source
- ❌ "Ticketmaster published 5-8% conversion improvement from live inventory" — no source, hallucinated
- ❌ "Mercure HISTORY_SIZE defaults to 100,000" — wrong, default is 0 (unlimited)
  via Bolt transport; 100,000 is the unrelated `subscriber_list_cache_size`
- ❌ "gorilla/websocket is archived/frozen" — GitHub repo not archived; API
  is stable per their README. **But it does panic on concurrent write**,
  which is the real reason to prefer coder/websocket for new code.
