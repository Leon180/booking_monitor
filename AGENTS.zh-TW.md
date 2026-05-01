# Booking Monitor - Codex 指令

> English version: [AGENTS.md](AGENTS.md)

> **工具備註。** 本檔是 [`.claude/CLAUDE.md`](.claude/CLAUDE.md) 的 Codex 對應版本。兩份檔案內容覆蓋同一套專案規則,各自存在的原因是 Codex 從專案根目錄讀取 `AGENTS.md`,而 Claude Code 讀取 `.claude/CLAUDE.md`。當專案規則異動時,兩份檔案(以及它們各自的 zh-TW 對應版本)都要更新 — 下方的雙語契約把它們視為一個 4-檔的單位。

## ⚠️ 雙語文件契約(必須遵守)

本專案維護多份**英文 + 台灣繁體中文(zh-TW)** 的成對文件。這些檔案視為**單一邏輯單元**。當你編輯其中任何一份時,你必須在**同一個回應**中同時更新兩個語言版本,維持結構完全一致(相同章節、相同表格、相同順序)。

**成對檔案(相對於專案根目錄):**
| English | Chinese |
|---------|---------|
| `AGENTS.md` | `AGENTS.zh-TW.md` |
| `.claude/CLAUDE.md` | `.claude/CLAUDE.zh-TW.md` |
| `README.md` | `README.zh-TW.md` |
| `docs/PROJECT_SPEC.md` | `docs/PROJECT_SPEC.zh-TW.md` |
| `docs/monitoring.md` | `docs/monitoring.zh-TW.md` |

**規則:**
1. **絕不**只更新成對檔案的一方。如果無法翻譯,跟使用者確認,而不是跳過。
2. **結構對齊**:章節標題、表格列、順序在兩個版本之間必須 1:1 對應。
3. **程式碼/指令/檔名保留英文**(兩個版本都是)— 只翻譯散文內容。
4. **新增章節時**,在同一個回應中把章節加進兩個版本,放在相同位置。
5. **使用者要求更新其中一份檔案時**,先說明會一起更新成對檔案,然後在標記任務完成前完成兩邊的編輯。
6. **翻譯風格**:zh-TW 使用台灣繁體中文用詞慣例(例如 資料庫 而非 数据库、介面 而非 接口、物件 而非 对象)。

**工具發現備註。** Codex 自動載入的入口是專案根目錄的 `AGENTS.md`。Claude Code 自動載入 `.claude/CLAUDE.md`。兩份檔案刻意做成幾乎相同的副本,讓每個工具都能看到完整的規則集,不必跨工具邊界讀檔;見上方的跨成對契約。PostToolUse hooks ([`.codex/hooks/check_bilingual_docs.sh`](.codex/hooks/check_bilingual_docs.sh) 與 [`.claude/hooks/check_bilingual_docs.sh`](.claude/hooks/check_bilingual_docs.sh)) 在編輯時強制執行此契約。

## ⚠️ 監控文件契約

日常維運的監控指南放在 [docs/monitoring.md](docs/monitoring.md)(以及成對的 zh-TW)。它記錄指標清單、Prometheus / Grafana 工作流、告警目錄,以及讓告警觸發的測試腳本。

第二個 PostToolUse hook(Codex 在 [`.codex/hooks/check_monitoring_docs.sh`](.codex/hooks/check_monitoring_docs.sh);Claude Code 在 `.claude/hooks/check_monitoring_docs.sh`)在動到任何 observability 表面時就會觸發 — 任何 `internal/infrastructure/observability/metrics_*.go`、任何 `*_collector.go`、`deploy/prometheus/alerts.yml`、`deploy/prometheus/prometheus.yml`、或 `deploy/grafana/provisioning/dashboards/*.json` — 並提醒 agent 在結束本回合前更新指南。新增一個指標卻沒更新 §2、新增一個告警卻沒更新 §5,hook 都會抓到。

---

## 專案概觀
高並發訂票系統(flash sale 模擬器),Go 寫的。採用 DDD + Clean Architecture 的模組化單體。雙層庫存(Redis hot path + PostgreSQL source of truth),透過 Redis Streams 做非同步處理,Kafka outbox pattern 發事件,saga-based 補償付款。

## 技術棧
Go 1.25 | Gin | PostgreSQL 15 | Redis 7 | Kafka | Prometheus | Grafana | Jaeger | Nginx

## 架構分層
```
internal/
  domain/         # Entities (Event, Order)、interfaces (repos, services)
  application/    # 內聚的流程子套件 — booking/, worker/, outbox/, event/, payment/, recon/, saga/(每個都有自己的 Service + Metrics + decorators);加上跨套件的 fx module + UnitOfWork interface + 線格式 DTOs 放在頂層
  infrastructure/ # Adapters: api/, cache/, persistence/postgres/, messaging/, observability/, payment/, config/
  mocks/          # 產生的 mocks (go.uber.org/mock)
```

## 關鍵指令
```bash
make build          # 以 -race 建置 binary
make test           # 跑單元測試含 race detection
make test-integration  # 跑 testcontainers 撐起的 Postgres 整合測試套件(CP4a;需要 Docker;約 14s)
make test-all       # 依序執行單元測試 + 整合測試
make run-server     # 啟動 API server(port 8080)
make run-stress     # 壓測(C=並發量,N=請求數)
make stress-k6      # K6 壓測 (VUS=500, DURATION=30s)
make reset-db       # 清空訂單,庫存重設為 100
make migrate-up     # 執行資料庫 migration
make mocks          # 重新產生 mock 檔
docker-compose up -d  # 啟動完整環境(app, nginx, payment_worker, postgres, redis, kafka, prometheus, grafana, jaeger)
```

## 關鍵 Patterns
- **Transactional Outbox**:Order + OutboxEvent 在同一個 DB transaction 中 -> OutboxRelay 輪詢 -> Kafka 發布
- **Saga Compensation**:付款失敗 -> order.failed topic -> SagaCompensator 回滾 DB + Redis 庫存
- **Unit of Work**:用 `PostgresUnitOfWork` 做 context 注入式 transaction
- **Advisory Locks**:PostgreSQL `pg_try_advisory_lock(1001)` 給 OutboxRelay 做 leader 選舉
- **Redis Lua Scripts**:`deduct.lua`(原子庫存扣減 + stream 發布)、`revert.lua`(idempotent 補償)
- **Idempotency**:API(Idempotency-Key header)、worker(DB unique constraint)、saga(Redis SETNX)

## API Endpoints
```
POST /api/v1/book          # 訂票(user_id, event_id, quantity)→ 202 含 order_id + 輪詢 URL
GET  /api/v1/orders/:id    # 透過 id 查訂單狀態(在短暫的非同步處理視窗中會回 404)
GET  /api/v1/history       # 分頁訂單歷史(?page=&size=&status=)
POST /api/v1/events        # 建立活動(name, total_tickets)
GET  /api/v1/events/:id    # 查看活動詳情
GET  /metrics              # Prometheus 指標
GET  /livez                # Liveness probe — process 還活著就一律回 200
GET  /readyz               # Readiness probe — PG + Redis + Kafka 都能在 1s 內回應才會 200,否則 503 帶 per-dep JSON
```

舊的 `POST /book` 在 Phase 13 已移除(PR #9 H9)— 它會繞過 nginx 的 rate-limit zones。所有呼叫端必須改用 `/api/v1/book`。

**訂票 response contract(PR #47)。** `POST /api/v1/book` 回 202 Accepted,內容是 `{order_id, status: "processing", message, links: {self}}`。202 對非同步管線是誠實的:Redis 端庫存扣減成功(load-shed gate);DB 持久化 + 付款 + saga 還在 in flight。Client 輪詢 `GET /api/v1/orders/:id` 拿終態 — 該 endpoint 在 202 之後幾毫秒可能會回 404(非同步處理視窗),client 應該配 backoff 重試。`order_id` 是 `BookingService.BookTicket` 在 API 邊界用 UUIDv7 鑄出來的,一路串到 Redis stream → worker → DB → outbox → saga;PEL 重送會沿用同一個 id,不會每次重送就重鑄一個。

`/livez` + `/readyz` 遵循 k8s probe 慣例:liveness 不能依賴下游服務(Redis 抖動不能殺光所有 pod),readiness 才會真的 ping 依賴。compose 的 `app` service 用 `/livez` 做 HEALTHCHECK。

## 資料庫
- PostgreSQL 在 port 5433(user/password/booking)
- 4 張表:`events`、`orders`、`events_outbox`、`order_status_history`(PR #40 / migration 000009 加進來的審計記錄表)
- `deploy/postgres/migrations/` 中有 11 個 migration — 000008(PR #34)把 PK 從 SERIAL → UUIDv7;000010/000011 加上 `orders(status, updated_at) WHERE status IN ('charging','pending','failed')` 的 partial index,給 reconciler + saga-watchdog 的 sweep 用。完整 migration 歷史見 [docs/PROJECT_SPEC.md §4](docs/PROJECT_SPEC.md)。

## Kafka Topics
- `order.created` — 由 payment service 消費(group `payment-service-group`)
- `order.created.dlq` — 無法解析 / 無效 payment 事件的 dead letter
- `order.failed` — 由 saga compensator 消費(group `booking-saga-group`)
- `order.failed.dlq` — 超過 `sagaMaxRetries=3` 的 saga 事件 dead letter

Group IDs 跟 topic names 可透過 `KAFKA_PAYMENT_GROUP_ID`、`KAFKA_ORDER_CREATED_TOPIC`、`KAFKA_SAGA_GROUP_ID`、`KAFKA_ORDER_FAILED_TOPIC` 設定。

## CI

GitHub Actions 在 [`.github/workflows/ci.yml`](.github/workflows/ci.yml),每個 push 到 `main` 跟每個 PR 都會跑。五個 job 並行:

| Job | What | Why |
| :-- | :-- | :-- |
| `test (race)` | `go vet` + `go test -race -coverprofile ./internal/...` | Race detector 在 CI 表現最好 — 非決定性的 race 只在量大時才浮現。Coverage 上傳成 artifact(沒有 gate)。 |
| `test-integration (testcontainers)` | `go test -tags=integration -race ./test/integration/...` 對每個測試啟一個全新的 `postgres:15-alpine` | CP4 階段建立的持久層整合測試套件(40 tests / ~38s)。抓 row-mapper 單元測試摸不到的 SQL-level regression。 |
| `lint (golangci-lint)` | `golangci-lint run` 對 [`.golangci.yml`](.golangci.yml) | 保守組合:errcheck、govet、ineffassign、staticcheck、gosec、revive。風格類 linter(gocyclo、funlen、lll)刻意延後到正確性基準清乾淨之後。 |
| `govulncheck (supply chain)` | `govulncheck ./...` | 把已知 CVE 對應到實際呼叫路徑 — 只在 vulnerable symbol 從我們程式碼可達時才 fail,不會被傳遞 import 灌爆。 |
| `docker build` | 多階段 Dockerfile 建置(不 push) | 抓 `make build` 抓不到的 image stage 故障。 |

Toolchain 透過 `go.mod` 的 `toolchain go1.25.9` directive 鎖定,所以 CI 建置(以及任何用 `go install` 對這個 module 的開發者)會自動拿到 stdlib CVE patches。要升 toolchain 時跟 `Dockerfile` 的 `golang:1.25-alpine` tag 一起升。

## 開發規範
- Immutable data patterns - 建立新 object,絕不 mutate
- 檔案 < 800 行;function 預設 < 50 行。Bootstrap / DI wiring / 線性建構程式(例如 `cmd/booking-cli/main.go` 的 fx.Invoke 內容)在拆出來只會增加間接層而不會讓意圖更清楚時,可放寬到約 80 行。不要為了壓行數而強拆 helper。
- 每一層都要顯式處理錯誤
- 在系統邊界做驗證
- 沒有寫死的 secret - 都用 env vars
- 測試用 testify/assert + go.uber.org/mock

## Benchmark 規範

吞吐量 regression 在 `docs/benchmarks/` 追蹤。目錄 layout 跟內容是慣例,不是工具強制 — 請保持一致,讓歷史 run 之間能比較。

**目錄命名**:`YYYYMMDD_HHMMSS_compare_c<vus>[_<tag>]`(例如 `20260426_183530_compare_c500_pr35`)。`c<vus>` 段是 VU 數;後面選填的 `_<tag>` 描述比較對象(PR id、phase 名稱等)。

**必要 artifacts**(每個目錄):
- `comparison.md` — 參數、run-A vs run-B 指標表、結論、注意事項(以 `20260426_183530_compare_c500_pr35/comparison.md` 為標準範本)
- `run_a_raw.txt` — baseline run 的完整 k6 stdout
- `run_b_raw.txt` — 待測 run 的完整 k6 stdout

**標準 apples-to-apples 條件**(除非刻意要量測別的東西,否則用這組):
- Script:`scripts/k6_comparison.js`
- VUs:500
- Duration:60s
- 票池:500,000(符合真實 flash-sale 活動規模 — 也就是這個系統要模擬的場景)。票池會在 60s 視窗中途賣光。Headline metrics 現在會分開報兩個數字,如實呈現量測內容:總 `http_reqs/s`(load-shed gate 的容量,賣光後被便宜的 409 fast path 主導)以及 `accepted_bookings/s`(穿過 Redis Lua deduct + worker queue + DB 的訂票 hot path)。兩個數字在運維上都有意義;senior-review checkpoint 抓到的問題是把它們塞進單一 headline。
- Target:`http://app:8080/api/v1`(直連,繞過 nginx rate limit)
- 兩個 run 都對同一台 host 上等價的 Docker stack 跑

**何時記錄**:碰到訂票 hot path(handler / `BookingService.BookTicket` / Redis Lua / `OrderMessageProcessor.Process` 的 tx body / outbox relay 輪詢)的 PR **必須**留 comparison report。純 refactor 而 diff 顯然證明 hot path 是 byte-identical 的(見 PR 31→35 的案例,正是這條規則的起源)**可**省略 — 但留一份證明 "no regression" 的 report 仍然是最乾淨的證據,優先保留。

**現有工具**:`make benchmark-compare VUS=500 DURATION=60s` 會跑 `scripts/benchmark_compare.sh`,自動建立目錄與 raw 輸出;`comparison.md` 再由人撰寫,引用已存下的 raw 檔。k6-on-Docker laptop 的 run-to-run 變異通常落在 3-5%,低於這個區間的 delta 是 noise 不是信號。

**未來壓力測試的軸線**:推 VU(並發數),而不是票池。系統定位是 flash-sale 模擬器 — 真實的壓力來自「10× 的使用者打同一批稀缺庫存」,而不是「更多庫存」。預期後續測試會把 VU 從 1k → 5k → 10k 推上去,打同一個 500k 票池,找出 time-to-sold-out、p99 延遲、或 `accepted_bookings`-vs-`http_reqs` 比值開始惡化的臨界點。票池固定在真實規模上,讓跨次測試比較在訂票 hot path 維度上維持 apples-to-apples。

## 目前狀態(截至 2026-04-30,Phase 2 checkpoint 之後)

完成 15 phases + Phase 2 reliability sprint。reliability arc 涵蓋:
- **PR #36 (A1)** — DLQ classifier(malformed message 跳過 retry budget);**PR #36 (A2)** — Payment gateway idempotency contract。
- **PRs #38/#39 (A3)** — Order 顯式狀態機(typed transitions,不再用 `UpdateStatus(any)`)。
- **PR #40 (C1)** — `order_status_history` 審計記錄表 + 用 atomic CTE-based transition logging。
- **PR #41 (N1)** — k8s 風格的 `/livez` + `/readyz` probes + `db_pool_*` / Go-runtime / cache hit-miss 指標。
- **PR #42 (N2)** — GitHub Actions CI:4-job pipeline(test+race / golangci-lint v2 / govulncheck / docker build)。
- **PRs #43/#44** — `cmd/main.go` 拆成 bootstrap 套件;`api/` 拆成 `api/{booking,middleware,ops,dto}` 子套件。
- **PR #45 (A4)** — Charging two-phase intent log + reconciler subcommand(`booking-cli recon`)。
- **PR #46** — Streams observability + DLQ MINID retention + idempotency value cap。
- **PR #47** — `POST /book` response shape(`order_id` + status + self link)+ `GET /api/v1/orders/:id`。
- **PR #48 (N4)** — Stripe 風格的 idempotency-key fingerprint 驗證(body fingerprint → 不一致回 409 + 對 legacy entries 做 lazy migration)。
- **PR #49 (A5)** — Saga watchdog + project-review checkpoint framework(`.agents/skills/project-review-checkpoint/`,在 `.claude/skills/project-review-checkpoint/` 也有對應)。

**Phase 2 邊界**(2026-04-30):第一次 project-review checkpoint 跑了 8 維度的 parallel-agent audit;報告在 [`docs/checkpoints/20260430-phase2-review.md`](docs/checkpoints/20260430-phase2-review.md)。Grade A−。一個經驗證的正確性 gap(reconciler max-age force-fail 漏了 Redis inventory)+ 4 個 ops Criticals + 9 個 Important findings → 從 action plan 第 1–9 列拆出 cleanup PR。完整歷史見 [docs/PROJECT_SPEC.md](docs/PROJECT_SPEC.md)。

## Logging 慣例(post-PR #18)
- **Pattern A — 長壽 component**:透過 constructor 注入 `*log.Logger`,在建構時用 `With()` **一次** 加上 `component=<subsystem>` decoration(例如 `worker_service`、`outbox_relay`、`saga_compensator`)。用 `l.Error(ctx, "msg", tag.OrderID(id))` — ctx-aware 方法會自動補 correlation/trace ids。
- **Pattern B — call-site-local 程式**:handler、middleware、init 路徑用套件層級的 `log.Error(ctx, "msg", tag.UserID(uid))`。從 ctx 用 `FromContext` 取 logger,沒設定就 fallback 到 Nop。
- **Typed fields**:優先用 `internal/log/tag/` 的 `tag.OrderID`/`tag.Error`/`tag.UserID` 等,而非 raw `log.Int("order_id", ...)` — 編譯時就能擋打錯字。
- **Inline 一次性欄位**:用 `internal/log/` 的 `log.String`/`log.Int`/`log.Int64`/`log.ByteString`/`log.Err`/`log.NamedError`,給那些不值得做成 typed tag 的 key(`component`、`batch_size`、`payload` 等)。**不要**從 application 層 import `go.uber.org/zap` — zap 被封在 `internal/log/` 內。
- **絕不**呼叫 `zap.S()` 或 `zap.L()` 全域函式 — 在這個 codebase 沒接;logger 全部走 DI。

## 剩下的 Roadmap
- DLQ Worker(dead letter retry policy)
- Event Sourcing / CQRS
- 水平擴展測試
- 真實付款 gateway 整合

## 主要 Env Vars
完整清單見 [docs/PROJECT_SPEC.md § 7](docs/PROJECT_SPEC.md)。最常調整的旋鈕:

**Runtime / GC / Tracing**
- `GOGC`(`.env` 預設 `400`,docker-compose fallback `100`)— 越高 = GC 越不頻繁
- `GOMEMLIMIT=256MiB` — 軟記憶體上限;搭配 GOGC 讓 GC 只在接近上限時才積極
- `OTEL_TRACES_SAMPLER_RATIO`(預設 `0.01`)— 1% 取樣;`1` = 全取、`0` = 不取

**安全敏感(post-PR #21/#22)**
- `ENABLE_PPROF`(預設 `false`)— `true` 時啟動 pprof + `/admin/loglevel` listener
- `PPROF_ADDR`(預設 `127.0.0.1:6060`)— **預設 loopback**;只在真的需要遠端 pprof 才覆寫。Heap dump + log-level 控制都在這
- `TRUSTED_PROXIES`(CSV,預設 RFC1918 CIDRs)— Gin 用這個信任的 proxies 取 `ClientIP()`;在 RFC1918 外的 service mesh(GKE、某些 EKS)需要覆寫

**運維(post-PR #21/#22)**
- `CONFIG_PATH`(預設 `config/config.yml`)— config 路徑;CWD 不同時(systemd unit、k8s initContainer)需要設
- `DB_PING_ATTEMPTS` / `DB_PING_INTERVAL` / `DB_PING_PER_ATTEMPT` — DB 啟動 probe budget;依賴慢時把 attempts 加大
- `KAFKA_BROKERS`(CSV,預設 `localhost:9092`)— 經過 cleanenv 的 `env-separator:","` 解析成 `[]string`

**Worker / Cache(post-PR #37)**
- `WORKER_STREAM_READ_COUNT`(預設 `10`)/ `WORKER_STREAM_BLOCK_TIMEOUT`(預設 `2s`)— order-stream consumer 的 XReadGroup batch + block window
- `WORKER_MAX_RETRIES`(預設 `3`)/ `WORKER_RETRY_BASE_DELAY`(預設 `100ms`)— 每訊息 retry budget + 線性 backoff base;deterministic-failure 錯誤透過 application-level retry policy 跳過
- `WORKER_FAILURE_TIMEOUT`(預設 `5s`)— handleFailure 補償 ctx budget(Redis revert + DLQ XAdd)
- `WORKER_PENDING_BLOCK_TIMEOUT`(預設 `100ms`)/ `WORKER_READ_ERROR_BACKOFF`(預設 `1s`)— 啟動時 PEL sweep block + 讀錯誤時的 retry sleep
- `REDIS_INVENTORY_TTL`(預設 `720h`)/ `REDIS_IDEMPOTENCY_TTL`(預設 `24h`)— Redis cache key TTL;之前是 const 寫死
- `REDIS_MAX_CONSECUTIVE_READ_ERRORS`(預設 `30`)— worker 在退出前能容忍的壞-Redis 次數

**Reconciler — A4(post-PR #45)** — 驅動 `booking-cli recon` subcommand
- `RECON_SWEEP_INTERVAL`(預設 `30s`)— reconciler 多久掃一次 stuck-`charging` 訂單
- `RECON_CHARGING_THRESHOLD`(預設 `30s`)— 訂單要被視為 "stuck charging" 並值得 gateway 探測的最小 age
- `RECON_GATEWAY_TIMEOUT`(預設 `2s`)— sweep 中每次 `gateway.GetStatus` 呼叫的 budget
- `RECON_MAX_CHARGING_AGE`(預設 `24h`)— 超過這個 age 後 reconciler 強制標 fail(目前漏掉 outbox emit — 見 `docs/checkpoints/20260430-phase2-review.md` DEF-CRIT)
- `RECON_BATCH_SIZE`(預設 `100`)— 每次 sweep tick 處理的訂單數

**Saga watchdog — A5(post-PR #49)** — 驅動 `booking-cli saga-watchdog` subcommand
- `SAGA_WATCHDOG_INTERVAL`(預設 `60s`)— stuck-`failed` 訂單的 sweep 節奏
- `SAGA_STUCK_THRESHOLD`(預設 `60s`)— `failed` 訂單要被視為卡住、值得重新觸發 compensator 的最小 age
- `SAGA_MAX_FAILED_AGE`(預設 `24h`)— 超過這個 age 後 watchdog 停止重新觸發,改 emit `max_age_exceeded`(operator 必須人工檢視 — 自動轉狀態有 phantom-revert 風險)
- `SAGA_BATCH_SIZE`(預設 `100`)— 每次 sweep tick 處理的訂單數。`Validate()` 拒絕 `MaxFailedAge ≤ StuckThreshold`(跨欄位守門)

## 可用工具

專案出貨平行的 skill / agent / hook 目錄,讓相同的專案規則同時被 Codex 跟 Claude Code 發現:

| 用途 | Codex 路徑 | Claude Code 路徑 |
| :-- | :-- | :-- |
| Skills | `.agents/skills/` | `.claude/skills/` |
| Subagents | `.codex/agents/*.toml` | `.claude/agents/*.md` |
| Hooks | `.codex/hooks/` | `.claude/hooks/` |
| Hook 設定 | `.codex/hooks.json` | `.claude/settings.json` |

兩個 skill 目錄內容相同;新增或更新一個 skill 時,要同時鏡像到兩邊。Subagents 跟 hooks 同理。內容採用自 [affaan-m/everything-claude-code](https://github.com/affaan-m/everything-claude-code)(MIT)— 見 [.claude/ATTRIBUTIONS.md](.claude/ATTRIBUTIONS.md)。

### Subagents(`.codex/agents/` / `.claude/agents/`)
- **go-reviewer** — TRIGGER:任何 `*.go` 檔案在 PR 中被改。檢查 security(SQL/command injection、race conditions、`InsecureSkipVerify`)、error handling(wrapping、`errors.Is/As`)、concurrency(goroutine leak、channel deadlock)、程式品質。
- **go-build-resolver** — TRIGGER:`go build` 或 `go test` 失敗。診斷 import cycle、版本不匹配、module 錯誤。
- **silent-failure-hunter** — TRIGGER:檢視會回傳或吞錯誤的程式,特別是 Kafka consumer([internal/infrastructure/messaging/](internal/infrastructure/messaging/))、outbox relay([internal/application/outbox/relay.go](internal/application/outbox/relay.go))、saga compensator、worker service。獵被吞掉的 error、空 catch block、爛 fallback。

### Skills(`.agents/skills/` / `.claude/skills/`)
- **golang-patterns** — TRIGGER:寫新 Go code。Go idioms:小 interface、error wrapping、context propagation。
- **golang-testing** — TRIGGER:加測試。Table-driven tests、`testify` / `go.uber.org/mock`、race detection、coverage。
- **postgres-patterns** — TRIGGER:碰 `internal/infrastructure/persistence/postgres/` 或 migration。Transactions、advisory locks、indexes、connection pooling。
- **idempotency-patterns** — TRIGGER:碰 `Idempotency-Key` 接線或 fingerprint 邏輯。
- **tdd-workflow** — TRIGGER:開始一個新功能 / bugfix。Red-green-refactor loop,把全域 coding style 中的 TDD mandate 落到實作。
- **project-review-checkpoint** — TRIGGER:phase 邊界或每約 10 個 PR。8 維度的全專案 audit framework。

### Rules
使用者全域的 `~/.claude/rules/common/` 由 `.claude/rules/golang/{coding-style,hooks,patterns,security,testing}.md` 擴充,加上 Go 專屬規範。Codex 透過上面的專案級 skills 取得相同內容。
