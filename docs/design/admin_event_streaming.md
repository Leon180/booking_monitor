# Admin Event Streaming (SSE) — 設計筆記

> **Status**: 設計階段（PR #1 — feat/sse-admin-events）
> **Date**: 2026-05-19
> **Decision owner**: Leon
> **Related**: `docs/architectural_backlog_streaming_research.md`、`docs/PROJECT_SPEC.md`

---

## 1. 問題陳述

戰情室（war room）需要在閃購進行時即時看到後端業務脈絡：訂單成立、付款結果、saga 補償、DLQ 訊息、庫存閾值。**目標讀者是 ops，不是終端使用者**。

要解的核心問題：

> 「如何在不破壞核心搶票 hot path 的前提下，把分散在 worker / webhook / saga / sweeper / DLQ writer / inventory monitor 的 8 種異質事件，以 at-most-once 語意推送到 admin 瀏覽器，並支援斷線重連 replay？」

---

## 2. Goals / Non-goals

### Goals

- **戰情室 dashboard 即時推送** — admin 看到事件 < 100ms 延遲
- **斷線可 resume** — `Last-Event-ID` replay 補回缺漏
- **不阻塞 booking hot path** — observability 不能拖慢 5,500 accepted/s 的 Stage 5 ceiling
- **k8s 友善** — multi-pod 部署天然 work，無 sticky session 需求
- **學習價值** — 採用 production-grade pattern（fan-out hub、bounded queue + drop policy、graceful shutdown）

### Non-goals

- **不**做 end-user 面 SSE（如 order status 推播）— 那是另一個 endpoint，scope 不同
- **不**追求 at-least-once delivery — 接受 at-most-once，PG audit 是 SSOT
- **不**為跨 region replication 設計
- **不**做完整 user / session 管理 — JWT shared secret 即可
- **不**做 WebSocket — 我們是單向 push，SSE 是正確 abstraction

---

## 3. Architecture overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                          BUSINESS PATH                              │
│  ┌────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐               │
│  │worker  │  │webhook   │  │saga      │  │sweeper   │  ... 7 pts    │
│  │UoW     │  │handlers  │  │compen.   │  │loops     │               │
│  └───┬────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘               │
│      │            │             │             │                     │
│      │ post-commit publish (Q5)                                     │
│      ▼            ▼             ▼             ▼                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  AdminEventBus  (bounded chan cap=10k + 1 drainer goroutine)│    │
│  │  Q3: OTel BatchSpanProcessor pattern                        │    │
│  │  Q4: typed envelope + 8 typed payloads                      │    │
│  └────────────────┬────────────────────────────────────────────┘    │
│                   │ XADD events:admin:stream MAXLEN ~ 100000        │
│                   ▼                                                 │
└─────────────────────────────────────────────────────────────────────┘
                    │
                    ▼ (Redis Stream, shared across pods — Q2)
┌─────────────────────────────────────────────────────────────────────┐
│                       STREAMING PATH (per pod)                      │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │ StreamSubscriber goroutine — Q11                            │    │
│  │   XREAD STREAMS events:admin:stream BLOCK 5s                │    │
│  │   consec-fail >= 30 → fx.Shutdown(ExitCode(1))              │    │
│  └────────────────┬────────────────────────────────────────────┘    │
│                   │                                                 │
│                   ▼ hub.broadcast chan                              │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │ Hub.Run() — channel-based loop (Q7, Q9)                     │    │
│  │   register / unregister / broadcast                         │    │
│  │   slow client → close c.send + drop (Q10)                   │    │
│  └────────────────┬────────────────────────────────────────────┘    │
│                   │                                                 │
│                   ▼ broadcast to each c.send (cap=100)              │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │ SSE handler per request (Q8 subscribe-then-replay)          │    │
│  │   - JWT HS256 auth (Q12)                                    │    │
│  │   - register to hub BEFORE replay                           │    │
│  │   - XRANGE replay history if Last-Event-ID present          │    │
│  │   - writer goroutine: events + heartbeat (Q13) every 30s    │    │
│  │   - nginx-friendly headers (Q14 layered defense)            │    │
│  └────────────────┬────────────────────────────────────────────┘    │
│                   │                                                 │
│                   ▼ HTTP response                                   │
└─────────────────────────────────────────────────────────────────────┘
                    │
                    ▼ (via nginx proxy_buffering off — Q14)
                  ┌───────┐
                  │admin  │
                  │tab    │  ← EventSource client
                  └───────┘
```

---

## 4. 17 個決策（逐條 ADR 摘要）

### Q1 — Unified event bus

**Decision**: 新建 `AdminEventBus` interface，所有 8 個發布點呼叫 `bus.Publish(evt)`，集中寫入 Redis Stream `events:admin:stream`。

**Alternatives considered**:
- Mining mode：SSE handler 各自 tail 既有 streams（`bookings:stream`, Kafka `order.failed`, etc.）。**Rejected**：跨 source 時間順序 merge 是 distributed-systems-101 的坑，可測試性差。

**Consequences**:
- 7 個 publish point 各加 1 行 `bus.Publish(...)`
- 一個明確的 contract / schema 集中管理
- 未來加 audit / analytics consumer 是 add-only

### Q2 — Single stream + MAXLEN ~100k，audit 走 PG

**Decision**: 單一 Redis Stream `events:admin:stream`，`MAXLEN ~ 100000`（~20 秒 burst history 或 idle 數週）。長期 audit 由既有 PG tables 提供（`orders`、`order_status_history`、`saga_compensations`、DLQ stream）。

**Alternatives considered**:
- Per-event-type 多 stream：更細的 retention 控制。**Rejected**：consumer 多 stream merge 複雜，無 ops 場景需要。

**Rationale**: Live tail + durable store 雙層 pattern 是業界共識（Kafka topic + S3 sink、Loki hot/warm/cold、Datadog tier）。

**Consequences**:
- 戰情期 burst 60s × 5500/s = 330k events，trim 後 100k = 約 20 秒尾巴
- 對 admin live tail 接受 — 歷史查 PG

### Q3 — OTel BatchSpanProcessor pattern (bounded async + drop)

**Decision**: `AdminEventBus.Publish()` 非阻塞，內部 channel cap=10,000，滿則 drop + `admin_event_bus_dropped_total` counter +1。一個 background drainer goroutine 從 channel pull → XADD。

**Alternatives considered**:
- Sync XADD：簡單但 Redis 慢時拖垮 booking hot path。**Rejected**。
- Unbounded async：記憶體無上限。**Rejected**。
- Vector default (block when full)：適合上游可緩衝場景，我們上游是 business hot path。**Rejected**。

**Rationale**: 對標 OpenTelemetry SDK BatchSpanProcessor（規範 default maxQueueSize=2048，drop on full，dropped counter）。我們 admin event 密度比 trace 高，調為 10,000。

**Reference**: https://github.com/open-telemetry/opentelemetry-go/blob/main/sdk/trace/batch_span_processor.go

**Consequences**:
- 不 batch（OTel batch 是為攤平 RPC overhead；Redis XADD ~10μs 不需要）
- 即時送 → SSE 端 latency 最小化
- 三條 backpressure 層職責分明：publisher 不能慢，subscriber 可以等，hub broadcast 不能被 slow client 拖

### Q4 — 自訂 envelope + typed payloads + schema_version

**Decision**:
```go
type AdminEvent struct {
    EventID       uuid.UUID
    EventType     string  // discriminator
    OccurredAt    time.Time
    SchemaVersion int     // v1
    Data          json.RawMessage  // per-type payload
}
```
8 個 typed payload struct：`OrderLifecyclePayload`、`SagaTriggeredPayload`、`DLQReceivedPayload`、`InventoryLowPayload` 等。

**Alternatives considered**:
- CloudEvents 1.0：CNCF 規範。**Rejected**：對單一服務內部 admin endpoint 是 over-engineering。Stripe / GitHub webhook / K8s events 也都用自訂格式不採 CloudEvents。
- Per-type flat structs（no envelope）：discriminator 跑去 SSE wire 的 `event:` line。**Rejected**：失去統一處理能力。

**Rationale**: Match codebase precedent（[OrderFailedEvent](../../internal/application/order_events.go) 就是這個 shape）。

### Q5 — Post-commit publish + 3 disciplines

**Decision**: 在 application layer，於 state-change function return nil 之後呼叫 `bus.Publish()`。三條紀律：(1) 不在 defer 裡 publish；(2) Publish 不阻塞；(3) `saga.triggered` 是 process-start 語意的設計例外。

**Alternatives considered**:
- Pre-commit publish：tx rollback 後 phantom event。**Rejected**：admin 場景 false positive 比 false negative 嚴重。
- Outbox pattern（複用既有 `events_outbox`）：100% guarantee。**Rejected**：每個 event 多一次 PG write，5,500/s 額外負載；對 observability 是 over-engineering（已有 PG audit）。
- Tx-scoped buffer + post-commit flush：更純設計。**Rejected**：Go 生態沒 named library，自寫要 refactor UoW interface，YAGNI。

**Rationale**: 對 admin scope 漏發容忍，phantom 不容忍。Crash window 微秒級可接受。

### Q6 — Redis Stream ID as SSE wire ID

**Decision**: SSE `id:` line 用 Redis Stream ID（`1716094800123-0`）。Envelope 的 `event_id` (UUIDv7) 留在 payload，給 app-level dedup / trace。

**Alternatives considered**:
- UUIDv7 on wire：需要 UUIDv7 → Stream ID mapping table，多一層 storage + 維護。**Rejected**：admin scope migration 成本低（hot cut-over 5 分鐘解決）。

**Future-proofing note**: 若未來開放給 end-user SSE，那個 endpoint 從 day 1 用 UUIDv7，不 retrofit admin endpoint。多 region 部署需重新設計（單 region / Cluster / Sentinel 內 Stream ID 都夠用）。

### Q7 — Fan-out hub (Mercure/Centrifugo style)

**Decision**: 每個 pod 一個 hub，single subscriber goroutine 讀 Redis Stream 餵 hub.broadcast，hub broadcast 給該 pod 所有 client 的 c.send channel。

**Alternatives considered**:
- Per-client direct XREAD：簡單，但每 client 一個 Redis 連線，N=100 時用掉一半 pool。**Rejected (after grill)**：為了學習價值選 hub pattern。

**Rationale**: Mercure / Centrifugo 是 production-grade SSE hub 的參考實作。對 < 100 client 規模略 over-engineer 但學習 ROI 高。

**Multi-pod sync**: Redis Stream 自身是 sync point，pod 之間不通訊。每 pod 各自 XREAD（不是 XREADGROUP）讀全部 events，廣播給自己的 client。

### Q8 — Subscribe-then-replay

**Decision**: 新 client 進來：(1) JWT 驗證 → (2) **先 register 到 hub** 開始 buffer live events → (3) `XRANGE events:admin:stream (<Last-Event-ID> +` 讀歷史送 client → (4) Writer loop 自然 drain client.send（歷史送完後是 live events）。

**Alternatives considered**:
- Replay-then-subscribe：步驟順序顛倒。**Rejected**：register 前的 live events 會漏（race window）。
- Hub-maintained ring buffer：hub 自己存最近 N entries。**Rejected**：兩份 source of truth，inter-pod inconsistency。

**Rationale**: Mercure subscribe.go 的設計（`HistoryDispatched()` signal 切換 history → live）。Centrifugo "recovery" mechanism 同理。

### Q9 — Channel-based Run loop

**Decision**: Hub 用單一 goroutine 的 `for { select { case <-register/unregister/broadcast/ctx.Done() } }` pattern。Clients map 只在這 goroutine 內存取，**無 mutex**。

**Alternatives considered**:
- sync.RWMutex protected map：reader 平行，但容易死鎖。
- sync.Map：簡單但 SSE broadcast 高頻寫，sync.Map 對 read-heavy 場景優化。

**Reference**: Gorilla chat example 的 canonical pattern（~50 LOC）。

**Rationale**: 100 client × O(N) broadcast = 100 個非阻塞 send，CPU 可忽略。零共享狀態最易 reason about。

### Q10 — Drop client on slow consumer + force reconnect

**Decision**: Hub broadcast 時若某 `c.send <- msg` 滿，`close(c.send)` 把該 client 踢掉。Client TCP 斷 → EventSource auto-reconnect → 帶 Last-Event-ID → Q8 replay 補回。

**Alternatives considered**:
- Silent drop message：client 不知道漏了，狀態 silently 分歧。**Rejected**：對 admin contract「看到全部 event」違反。
- Block broadcast loop with timeout：拖慢所有 client，違反 fan-out 初衷。**Rejected**。

**Reference**: Gorilla chat example（同 close + delete pattern）。

### Q11 — Subscriber consec failure → fx.Shutdown

**Decision**: StreamSubscriber 連續失敗 ≥ 30 次（每次 5s block timeout，約 2.5 min 失敗窗口）→ `shutdowner.Shutdown(fx.ExitCode(1))`。Transient 失敗用指數退避（200ms base、5s cap），`redis.Nil` 不算失敗。

**Alternatives considered**:
- 永遠 retry 不 escalate：Redis auth 錯誤等 permanent failure silently 卡住。**Rejected**。
- Error classification (circuit breaker)：複雜度高，無顯著好處。**Rejected**。

**Rationale**: Match codebase 既有 [runDriftDetector](../../cmd/booking-cli-stage5/server.go) pattern。Idiomatic for k8s-native services（讓 k8s 重啟 pod）。

### Q12 — JWT HS256 + golang-jwt/jwt/v5

**Decision**: SSE endpoint 走 query parameter token：`?token=<jwt>`（瀏覽器 EventSource 不支援 custom header）。JWT 用 HS256 + shared secret，TTL 上限 1h，scope 必須是 `admin.events`。

**Alternatives considered**:
- 自寫 HMAC token：~160 LOC vs JWT ~90 LOC，且 JWT migration 到 RS256/OAuth 容易。**Rejected (after correction)**。
- Cookie session：需要 login flow + session store。**Rejected**：scope 太大。
- Pre-shared plain token：無 TTL、無 revoke、進 access log 等於外洩。**Rejected**。
- mTLS：browser 不支援 client cert。**Rejected**。

**Reference**: https://github.com/golang-jwt/jwt （8.4k stars，Go 生態事實標準）

**Migration path**: HS256 → RS256（asymmetric）→ OAuth/OIDC（JWKS endpoint），同個 library 自然演進。

### Q13 — 30s comment-line heartbeat (no jitter)

**Decision**: 每 30 秒送 `: heartbeat\n\n`（SSE comment line，browser ignore）。`X-Accel-Buffering: no` header + nginx 配置防 buffering。**不**加 jitter（client < 100 規模不需要）。

**Alternatives considered**:
- 15s：太保守，浪費 25% 流量。
- 40s（Mercure default）：在 GCP HTTP LB（30s）會 timeout。
- Named event：觸發 client `dispatchEvent`，浪費 CPU。**Rejected**。

**Rationale**: 30s 通過所有主要 cloud LB（nginx 60s、AWS ALB 60s、GCP HTTP LB 30s）。WHATWG SSE spec § 9.2.7 直接推薦 comment-line。

**重要修正**: Heartbeat **不**負責 dead-client detection（[Go issue #20553](https://github.com/golang/go/issues/20553)：`Write()` 對死掉 socket 不立即 error）。Dead-client 偵測靠 `c.Request.Context()` cancellation（Gin 在 client TCP 斷時自動 cancel）+ TCP keepalive（Go 1.21+ 預設 15s）。Heartbeat **唯一職責是 keep proxy alive**。

### Q14 — Nginx config + X-Accel-Buffering header (layered defense)

**Decision**: 雙保險：
- `deploy/nginx/nginx.conf` 加 `location /api/v1/admin/events/stream` block：`proxy_buffering off`、`proxy_cache off`、`gzip off`、`proxy_http_version 1.1`、`proxy_read_timeout 75s`
- App 端 SSE handler 設 `X-Accel-Buffering: no` response header

**Alternatives considered**:
- Nginx-only：app 不該知道 reverse proxy 存在。**Rejected**：config 漂移時失去保護。
- App-header-only：timeout / gzip 仍可能出問題。**Rejected**。

**Rationale**: Layered defense 是 Mercure 部署指引的推薦做法。

### Q15 — Graceful shutdown — retry hint + drain + cancel + close + reject new

**Decision**: fx OnStop 4 phase（順序：hub 先 → HTTP server 後）：

1. `shuttingDown.Store(true)` → 新連線 503
2. Broadcast jittered `retry: 2000-8000\n` 給所有 client → reconnect 自然分散
3. Cancel hub ctx → subscriber 退出 → hub.Run 進 cleanup path（drain broadcast channel → close 所有 c.send）
4. 等所有 writer goroutine 退出（timeout 從 fx stopCtx）

然後 `srv.Shutdown(ctx)`。

**Alternatives considered**:
- Force kill（no drain）：client 看到 TCP RST，靠 Last-Event-ID 自己補。**Rejected**：對 admin 場景 drain 成本低、體驗好。
- HTTP shutdown 先：long-lived SSE 不會主動結束，srv.Shutdown 死鎖。**Rejected**。

**Rationale**: Drain pattern 是 software engineering 通用 idiom（k8s `terminationGracePeriodSeconds`、Go `http.Server.Shutdown`、Erlang OTP supervisor、TCP `shutdown(2)` SHUT_WR）。

### Q16 — Layered metrics + war-room dashboard

**Decision**:
- **Layer A 業務 metric**（已存在於 codebase，PR 不重複加）：`bookings_total`、`PaymentWebhookReceivedTotal`、`ExpirySweepResolvedTotal`、`SagaCompensatorEventsTotal`、`RedisDLQRoutedTotal`
- **Layer A 缺失，本 PR 新加**：`inventory_low_alerts_total`
- **Layer B infrastructure metric**（本 PR 新加）：`booking_admin_event_bus_*`（3 個）、`booking_sse_*`（9 個）、`booking_admin_stream_subscriber_*`（2 個）
- **Dashboard**：4 row composition（Hero metrics 5s refresh、Trend charts 30s、SSE Live Timeline push、Health 60s）

**Consistency model**: SSE 跟 Prometheus **不 reconcile**，by design。SSE = best-effort live tail；Prometheus = atomic 計數 source of truth；DB = 個別 record source of truth。三層各司其職。

### Q17 — Tier 1 + Tier 2 tests + goleak

**Decision**: 兩層測試 ship in same PR：

**Tier 1（~1,000 LOC）**：
- Unit: Bus / Hub / JWT / SSE wire 各自 unit test
- Integration: Redis Stream replay via testcontainers
- Goroutine leak: 1000-iter connect/disconnect via `go.uber.org/goleak`
- Happy path acceptance：publish → SSE deliver

**Tier 2（~600 LOC）**：
- Slow-client isolation
- Drop-client policy
- Heartbeat 驗證
- Subscriber failure escalation
- MAXLEN trim 偵測（stream_truncated event）
- Graceful shutdown
- Auth edge cases

**Tier 3 deferred**: benchmark、multi-pod integration、chaos test。

**Reference**: `go.uber.org/goleak` https://github.com/uber-go/goleak（業界事實標準，被 gRPC-go / etcd / OTel-Go / k8s 採用）

---

## 5. 實作 roadmap（按 commit 順序）

1. **docs**: 設計 doc + research foundation（本檔 + research doc）— `feat/sse-admin-events` 初始 commit
2. **feat(domain)**: `internal/domain/admin_event.go` — envelope + 8 typed payloads + constants
3. **feat(observability)**: `internal/infrastructure/observability/admin_stream_metrics.go` — Layer B + `inventory_low_alerts_total`
4. **feat(application)**: `internal/application/admin/event_bus.go` — Bus interface + bounded async impl
5. **feat(infrastructure)**: `internal/infrastructure/api/sse/{hub,subscriber,admin_stream_handler}.go`
6. **feat(middleware)**: `internal/infrastructure/api/middleware/admin_jwt.go`
7. **feat(bootstrap)**: `internal/bootstrap/admin_stream.go` — fx wiring + OnStart/OnStop
8. **feat(cli)**: `cmd/admin-cli/main.go` — JWT mint subcommand
9. **ops**: nginx + grafana dashboard JSON + prometheus alerts.yml
10. **test(integration)**: `test/integration/sse/` 全套
11. **docs(streaming)**: `docs/streaming.md` — production gotchas + design + sources（面試 talking point doc）

預估：**~3,500 LOC（含測試），3-4 工作天**。

---

## 6. Open questions / future work

### 暫不處理但記下

- **End-user 面 SSE**（per-order status push）：另一個 endpoint，從 day 1 用 UUIDv7
- **Multi-region 部署**：Stream ID 跨 region 不可比，需重新設計（cross-region event aggregation）
- **Token rotation 機制**：v1 用單一 secret，後續加 `_NEW` / `_OLD` 雙 secret 支援
- **Sharded Redis Streams**：若 admin event rate > 10k/s（不太可能）才考慮，Centrifugo sharded engine pattern
- **CDC migration**：若全公司導入 Debezium，可從這層切換到 CDC source（Shopify pattern）

### 已知 trade-off（接受）

- **SSE / Prometheus 不一致**：by design，PG audit 是 SSOT
- **MAXLEN trim 期間漏 historical replay**：burst 60s 後超過 100k 的事件 trim 掉，重連 client 收 `stream_truncated` 通知
- **Subscriber 單點故障**：每 pod 一個 subscriber，靠 fx.Shutdown 觸發 k8s restart 處理
- **Admin scope 規模假設 < 100 client**：超出時 fan-out hub 在 broadcast loop 變 O(N) bottleneck，需 refactor 到 sharded clients map

---

## 7. References（全部 verified URLs）

### Architecture references

- **OpenTelemetry BatchSpanProcessor (Go SDK)**: https://github.com/open-telemetry/opentelemetry-go/blob/main/sdk/trace/batch_span_processor.go
- **Mercure SSE protocol spec**: https://mercure.rocks/spec
- **Mercure source code**: https://github.com/dunglas/mercure
- **Centrifugo Redis engine**: https://centrifugal.dev/docs/server/engines
- **Centrifugo SSE transport**: https://centrifugal.dev/docs/transports/sse
- **Gorilla chat hub canonical pattern**: https://github.com/gorilla/websocket/blob/master/examples/chat/hub.go
- **WHATWG SSE spec § 9.2.7 (heartbeat recommendation)**: https://html.spec.whatwg.org/multipage/server-sent-events.html

### Trade-off references

- **Stripe webhooks over polling**: https://docs.stripe.com/payments/payment-intents/verifying-status
- **Uber RAMEN push platform (80% polling cost)**: https://www.uber.com/blog/real-time-push-platform/
- **Ably SSE vs WebSocket (2024 industry consensus)**: https://ably.com/blog/websockets-vs-sse
- **Chris Richardson — Transactional Outbox**: https://microservices.io/patterns/data/transactional-outbox.html
- **Shopify Engineering — CDC migration**: https://shopify.engineering/capturing-every-change-shopify-sharded-monolith
- **Vector backpressure model**: https://vector.dev/docs/architecture/buffering-model/

### Implementation references

- **golang-jwt/jwt v5**: https://github.com/golang-jwt/jwt
- **coder/websocket (for future WS if needed)**: https://github.com/coder/websocket
- **go.uber.org/goleak**: https://github.com/uber-go/goleak
- **Go context blog (graceful shutdown idiom)**: https://go.dev/blog/context

### LB / proxy timeout 數字

- **nginx proxy_read_timeout default 60s**: https://nginx.org/en/docs/http/ngx_http_proxy_module.html
- **AWS ALB idle timeout default 60s**: https://docs.aws.amazon.com/elasticloadbalancing/latest/application/edit-load-balancer-attributes.html
- **GCP HTTP LB timeoutSec default 30s**: (Medium reference confirmed in research doc)
- **Cloudflare proxy idle 900s**: https://developers.cloudflare.com/fundamentals/reference/connection-limits/

### Pitfalls confirmed

- **gorilla/websocket panics on concurrent write**: https://github.com/gorilla/websocket/blob/main/conn.go#L626
- **coder/websocket concurrent-safe write contract**: https://github.com/coder/websocket/blob/v1.8.14/write.go#L24
- **Go `Conn.Write` does not detect dead TCP first attempt**: https://github.com/golang/go/issues/20553
- **Cortex jitter pattern for heartbeats**: https://github.com/cortexproject/cortex/issues/1534

---

## 8. Author 結語

這份設計通過 17 輪 stress-test grill 完成，每個決策都有 verified industry reference + considered alternative + explicit rationale。預計 ship 完整 PR #1 需 3-4 工作天。

設計核心哲學：

1. **Layered backpressure**: publisher 不能慢、subscriber 可以等、broadcast 不能被 slow client 拖
2. **At-most-once for observability**: 漏發容忍，phantom 不容忍；PG audit 是 SSOT
3. **Pod-local hub + shared Redis sync**: 無需 sticky session，multi-pod 天然 work
4. **Drain pattern as standard**: 上游停新 I/O、下游消化現有 in-flight，graceful shutdown 跟 OS / k8s / Erlang / Go ctx 哲學一致
