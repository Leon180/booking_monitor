# PR #121 — Interview Talking Points

> One-page cheat sheet for demoing the admin SSE event stream feature.
> Read time: ~5 min. Demo time: 15-20 min including dashboard.

---

## 30-second elevator pitch

> 「我做了一個 SSE admin event stream，把 booking 系統的事件（order.created, order.paid, saga.triggered, dlq.received...）即時推送到 ops 的戰情室 dashboard。從 design grill 到 production-ready 跑了 17 個架構決策、4 輪 multi-agent review、smoke test 跟 burst load test，總共 18 個 commits。架構模仿 OpenTelemetry 的 BatchSpanProcessor + Mercure 的 SSE protocol，driven by 業界 verified sources。」

---

## Top 5 architecture decisions (面試官最可能追問)

### 1. SSE vs WebSocket — 為什麼選 SSE？
**One-liner**: Server → client 單向，admin 沒有 bidirectional 需求，WebSocket 的 concurrent-write panic 跟 single-writer pattern 都是不必要的複雜度。

**Source**: Ably 2024 blog: "WebSockets describe perhaps 5% of real-time features in production." 我們屬於另外 95%。

### 2. 為什麼用 Redis Stream 而不是 Kafka？
**One-liner**: Stream 有 monotonic ID 天然 fit Last-Event-ID resumption；MAXLEN ~ 100k 自動 trim；不需要 consumer group（每個 SSE client 都看全部事件，是 broadcast 不是 work-queue）。Kafka overkill for admin observability。

**Source**: Centrifugo 的 Redis engine 是 multi-pod fan-out 的事實標準參考。

### 3. Bus 為什麼採 OTel BatchSpanProcessor pattern (bounded async + drop)？
**One-liner**: Publisher 在 booking hot path，**不能阻塞**。Channel cap=10,000，滿了 drop + counter 計數。對標 OTel SDK 預設 maxQueueSize=2048，drop on full。

**Counter-argument**: Vector 預設是 block，因為他們的 upstream 可緩衝（log file）。我們上游是 business hot path → drop 是對的。

### 4. 為什麼 hub 用 channel-based Run loop 而不是 sync.Map？
**One-liner**: 單 goroutine 獨佔 clients map，零共享 state，零 mutex，零 race。對標 Gorilla chat example canonical pattern（~50 LOC）。< 100 client 規模 broadcast O(N) 不是瓶頸。

### 5. Graceful shutdown 為什麼 hub 先 HTTP 後？
**One-liner**: long-lived SSE handler 不會主動退出，srv.Shutdown 會 deadlock。所以倒過來：1) 拒新連線 2) jittered retry hint 3) cancel hub → 寫 goroutine 退出 4) HTTP shutdown 乾淨 drain。

**Source**: 這是業界通用 drain pattern — k8s `terminationGracePeriodSeconds`、Go `http.Server.Shutdown`、Erlang OTP supervisor、TCP `shutdown(2)` SHUT_WR 都是同個 idea。

---

## Engineering process — 為什麼我 invested 4 輪 review？

**Memory pattern**: PRs > 500 LOC benefit from 3-5 review rounds because each catches distinct classes:

| Round | Catches |
|---|---|
| 1 (code review) | static logic bugs, security holes, error handling |
| 2 (post-fix) | regressions in the fixes themselves |
| 3 (post-second-fix) | accumulation issues + ops/config bugs |
| **Smoke** | "is X actually called?" — review can't catch this |
| 4 (post-smoke) | edge cases (e.g., `//go:build integration` files) |
| **Benchmark** | architecture validation under load |

**真實數字**: 19 real issues + 1 false-positive across 4 rounds. Round 4 還抓到 CRIT-class bug in integration test。**Smoke 才是真正的試金石。**

---

## What the benchmark validated

k6 200 VUs × 15s → 1700 accepted bookings → 1699 events through the pipeline:

| Layer | Result | Validates |
|---|---|---|
| Bus publish | 1699/1699 ✓ | Q3 non-blocking on hot path |
| Bus drops | 0 | Q3 capacity (10k) sufficient at 113/s |
| Drainer XADD | 0 failures | Redis at-least-once durability |
| Redis stream XLEN | **1699 = 100%** | Q2 durable storage layer |
| Subscriber failures | 0 | Q11 stays healthy |
| Slow-consumer drops | 0 | Q10 backpressure design |
| SSE wire delivery | 930 / 1699 | curl-slow consumer; rest stay in stream for replay |

**Key insight**: Wire delivery rate **depends on client speed**. Events not on wire are **NOT lost** — they're in Redis Stream waiting for Last-Event-ID replay. This is the **at-most-once + durable-with-replay** semantic from design.

---

## Production gotchas I hit and fixed

| # | Gotcha | How discovered |
|---|---|---|
| 1 | gorilla/websocket panics on concurrent write | Source-code reading during library choice |
| 2 | EventSource cannot set custom headers | MDN docs, forced JWT in query string |
| 3 | nginx `proxy_buffering on` breaks SSE entirely | Mercure deployment guide |
| 4 | `redis.UniversalClient` vs `*redis.Client` fx mismatch | **Smoke test only** — review missed |
| 5 | Hub deadlock on shutdown if HTTP closed first | Q15 grill |
| 6 | Heartbeat ≠ dead-client detection (Go issue #20553) | Source-code research |
| 7 | Cardinality blowup from attacker-controlled `event_type` | Round 3 review |

---

## Standards / references I cited (面試官可能追)

| Standard | Used for |
|---|---|
| WHATWG SSE spec § 9.2.7 | Heartbeat comment-line format |
| RFC 9562 (UUIDv7) | Event IDs in payload |
| NIST SP 800-107 | JWT HS256 minimum 32-byte secret |
| Stripe webhook docs | "Use webhooks over polling" rationale |
| Uber RAMEN blog (2020) | "80% of API requests were polling" — production case study |

---

## What I'd do differently (誠實反思)

1. **Wire all 7 publish points upfront** instead of just 1 — smoke caught this. Lesson: "build" ≠ "wired".
2. **Smoke test earlier** — should have run after commit #5 (bootstrap wiring), not after commit #11.
3. **Build minimum viable demo (HTML page) in parallel with API** — would have caught the publish-wiring gap immediately.
4. **Add CI step that exercises the SSE path** — currently only unit + integration tests cover it.

---

## Demo plan (15-20 min)

```
0:00  Run docker-compose up -d
0:30  make migrate-up
0:45  curl -X POST /api/v1/events ... → ticket type created
1:00  booking-cli admin-token --user demo --ttl 30m → token
1:30  Open http://localhost/admin/ in browser
2:00  Paste token + Connect
2:15  POST /api/v1/book → see event appear LIVE on dashboard
3:00  Talk through: what just happened (the pipeline)
4:30  Show docs/design/admin_event_streaming.md (the ADR)
6:00  Pick 2-3 design decisions to discuss in depth
10:00 Show docs/streaming.md (production gotchas)
13:00 Show benchmark report (sse_smoke_20260520.md)
16:00 Q&A — be ready for "why not X" questions
```

---

## Vocabulary check (面試官會用這些字)

| English | 中文 | When to use |
|---|---|---|
| backpressure | 背壓 | when consumer can't keep up with producer |
| fan-out | 分發 | one source → many consumers |
| broadcast | 廣播 | one message → all subscribers |
| at-most-once / at-least-once | 至多一次 / 至少一次 | delivery guarantees |
| drain | 排空 | graceful shutdown phase |
| TOCTOU | 競爭條件 | time-of-check vs time-of-use |
| escalate | 升級 | fail forward to k8s restart |
| converge | 收斂 | iterative process stabilizing |
| invariant | 不變量 | what factories validate |
| factory + reconstructor | 工廠 + 重建器 | DDD codebase rule |

---

## Last-minute confidence boosters

1. **You wrote production code, not toy code.** PR #121 has 18 commits, 4 review rounds, smoke + benchmark, paired bilingual docs, source-verified ADR.
2. **You can ground every choice in evidence.** Every "why?" has a Mercure / OTel / Stripe / Uber / WHATWG citation.
3. **You caught your own mistakes.** 19 issues found and fixed by you (with agents). The smoke catching missed publish wiring is a strength: you have a process that catches review-blind bugs.
4. **You know what you DIDN'T do.** 6/7 publish points pending, multi-pod untested, no real browser test. Acknowledge gaps directly.
5. **You picked the simpler tool when both worked.** SSE not WS, channel loop not sync.Map, post-commit publish not outbox. Each rejection has reasons.

---

🎯 **One sentence to start every answer**: "我在這個專案的 PR #121 做了 X — 設計是 [pattern from industry]，因為 [trade-off]，驗證是 [smoke / benchmark / review]。"
