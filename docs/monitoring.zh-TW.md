# 監控指南

> English version: [monitoring.md](monitoring.md)

這份文件是日常運維者的參考 — **stack 跑起來後要怎麼觀察 `booking_monitor`**。它**不是**架構規格(那部分看 [PROJECT_SPEC.zh-TW.md](PROJECT_SPEC.zh-TW.md))— 它要回答的是具體的問題:「現在系統健康嗎」、「這個指標定義在哪」、「我要怎麼故意把這條告警觸發起來測試」。

三個觀測性介面:

| 介面 | URL | 提供什麼 |
| :-- | :-- | :-- |
| **原始 `/metrics`** | http://localhost:80/metrics(經 nginx)或 http://localhost:8080/metrics(直接打 app) | Prometheus exposition format。便宜、可被腳本化、適合用 `grep` / `curl` 做正確性檢查。 |
| **Prometheus UI** | http://localhost:9090 | 臨時 PromQL 查詢、target 健康度、告警狀態。 |
| **Grafana** | http://localhost:3000(帳密 `admin` / `admin`) | 預先配置好的儀表板。「現在有沒有東西在閃紅燈」的最佳介面。 |

`/livez` 和 `/readyz` 也分別在 http://localhost:80/livez 與 `/readyz` 提供,給 Kubernetes 風格的健康探測使用 — protocol 合約見 [PROJECT_SPEC.zh-TW.md §6](PROJECT_SPEC.zh-TW.md)。

---

## 1. 快速健康檢查(60 秒迴圈)

要快速知道「現在系統健康嗎」:

```bash
# 1. App process 還活著嗎?
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:80/livez            # → 200

# 2. App 看得到所有依賴嗎?
curl -s http://localhost:80/readyz | jq                                       # → status: ok

# 3. 有沒有正在 firing 的告警?
curl -s http://localhost:9090/api/v1/alerts | jq '.data.alerts[] | {name: .labels.alertname, state, value}'

# 4. 訂票熱路徑有流量嗎?有成功嗎?
curl -s http://localhost:80/metrics | grep -E '^bookings_total\{' | head

# 5. 從 id 一路追單筆訂單到最終狀態(PR #47)
ORDER_ID="<從 POST /book 的 response 貼進來>"
curl -s "http://localhost:80/api/v1/orders/$ORDER_ID" | jq
# → {"id":"...", "status":"confirmed", ...}
# 在短暫的非同步處理視窗會回 404 — 帶 backoff 重試即可。
```

五個任何一個吐出非預期結果,就往下面對應的更深層介面鑽。

---

## 2. 指標清單

權威來源是 `internal/infrastructure/observability/metrics.go`,加上兩個 collector(`db_pool_collector.go`、`streams_collector.go`)。完整有註解的列表在 [PROJECT_SPEC.zh-TW.md §7](PROJECT_SPEC.zh-TW.md)。下面是按「你會問什麼問題」做的實用分組。

### 每個 request — RED 方法(Rate / Errors / Duration)

| 問題 | 指標 | 範例查詢 |
| :-- | :-- | :-- |
| 流量多大? | `http_requests_total{method,path,status}` | `sum by (status) (rate(http_requests_total[1m]))` |
| 多快? | `http_request_duration_seconds_bucket` | `histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket[5m])) by (le))` |
| 多少 % 失敗? | 同上,過濾 `status=~"5.."` | `sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))` |

### 每個資源 — USE 方法(Utilization / Saturation / Errors)

| 資源 | 指標前綴 | 備註 |
| :-- | :-- | :-- |
| Go runtime | `go_*`、`process_*` | goroutines、GC pause、heap inuse — 透過 `collectors.NewGoCollector` 註冊 |
| DB pool | `db_pool_*` | `db_pool_in_use`、`db_pool_idle`、`db_pool_wait_count`、`db_pool_wait_duration_seconds` |
| Redis 快取 | `cache_hits_total{cache}`、`cache_misses_total{cache}` | 每個快取名稱獨立的 hit/miss |
| Redis streams | `redis_stream_length{stream}`、`redis_stream_pending_entries{stream,group}`、`redis_stream_consumer_lag_seconds{stream,group}` | scrape 時由 `StreamsCollector` 即時讀取 |

### 領域指標 — 業務在意的事

| 問題 | 指標 |
| :-- | :-- |
| 成功訂票 vs 售完 vs 重複下單 | `bookings_total{status}` |
| Worker 處理結果 | `worker_orders_total{status}`、`worker_processing_duration_seconds` |
| Redis 與 DB 庫存漂移 | `inventory_conflicts_total` |
| Dead-letter 路由 | `dlq_messages_total{topic,reason}`、`redis_dlq_routed_total{reason}` |
| Saga 補償 poison 訊息 | `saga_poison_messages_total` |
| Kafka consumer 因下游短暫故障卡住 | `kafka_consumer_retry_total{topic,reason}` |
| 卡在 Charging 的訂單對帳 | `recon_stuck_charging_orders`(gauge)、`recon_resolved_total{outcome}`、`recon_gateway_errors_total`、`recon_resolve_duration_seconds`、`recon_resolve_age_seconds` |
| Streams collector 自己掛了 | `redis_stream_collector_errors_total{stream,operation}` |
| 同一個 Idempotency-Key 被重送時的處理結果(N4) | `idempotency_replays_total{outcome}` — 每次 client 帶**重複**的 Idempotency-Key 來時,server 怎麼處理。三種結果:<br>• `match` = 同 key + 同 body → 我們直接回傳之前快取的回應(這是冪等正常運作)<br>• `mismatch` = 同 key + **不同** body → 我們回 409 Conflict(client 程式有問題:把同一把 key 用在意義不一樣的請求上)<br>• `legacy_match` = 在 N4 上線之前就已經寫進快取的舊條目(沒帶 fingerprint),仍然回傳但順便補寫 fingerprint。**部署後 24 小時內應該降到 0**(舊快取會自然過期);如果一直 > 0,表示有東西還在寫舊格式 — 要查為什麼。 |
| Idempotency 查 Redis 失敗的次數(N4) | `idempotency_cache_get_errors_total` — idempotency 在查 Redis 時失敗了幾次(Redis 連線斷、回傳資料壞掉)。**這條值得 page on-call**。意思:當這條持續 > 0,booking 端點還在收單,但**冪等保護是關掉的** — 同一個請求重送會被處理兩次(僅剩 DB 的 UNIQUE 限制在最底層擋)。<br>**為什麼還繼續服務不直接拒絕?** 因為 Redis 一斷就拒絕所有訂票請求 = 整個端點掛掉,「冪等保護暫時失效」比「服務整個掛」好;這個 counter 就是讓 on-call 知道目前處在那個狀態。<br>告警設定:`rate(idempotency_cache_get_errors_total[5m]) > 0 for 1m` → page。 |
| 卡在 Failed 狀態還沒被補償的訂單數(A5) | `saga_stuck_failed_orders` — gauge,每次 saga watchdog 跑 sweep 時更新。值代表「在 Failed 狀態超過 `SAGA_STUCK_THRESHOLD`(預設 60s)的訂單數」。**單一時刻 > 0 沒問題**(下一次 sweep watchdog 會去重新觸發 compensator)。**持續 > 0 達 10 分鐘才是問題** — 表示 compensator 一直失敗,典型原因:Redis revert 卡住、DB lock 競爭、未被 map 的 compensator 錯誤。Watchdog 預設 60s sweep 一次,10 分鐘 = ~10 次重試後才 page,給足時間自然清空。 |
| Saga watchdog 處理結果(A5) | `saga_watchdog_resolved_total{outcome}` — counter,記每次 watchdog 重新觸發 compensator 的結果。**六個 outcome**,每個對應不同的 runbook,讓 on-call 一開始就找對子系統:<br>• `compensated` = watchdog 成功重新觸發 compensator(Failed → Compensated)<br>• `already_compensated` = 在 FindStuckFailed 跟重新觸發中間,saga consumer 自己跑完了(良性,不需處理)<br>• `max_age_exceeded` = 訂單超過 `SAGA_MAX_FAILED_AGE`(預設 24h);watchdog 只記錄 + 告警,**不會自動轉狀態**(沒驗證庫存是否已回復就 MarkCompensated 不安全)— 需要人工調查<br>• `getbyid_error` = `orderRepo.GetByID` 在到 compensator 之前就失敗了。Operator 先查 **資料庫** 健康度,**不是** Redis 或 compensator 程式碼<br>• `marshal_error` = 合成 OrderFailedEvent 時 `json.Marshal` 失敗。今天對固定形狀的 struct 是理論值;單獨 label 是為了讓未來欄位 regression 可被觀測<br>• `compensator_error` = `compensator.HandleOrderFailed` 回傳錯誤。Operator 查 **Redis revert path + DB lock 競爭**。下個 sweep 會重試 |
| Saga watchdog 的 DB 查詢失敗(A5) | `saga_watchdog_find_stuck_errors_total` — `FindStuckFailed` SQL 查詢失敗(DB 斷線、000011 migration 沒跑、query timeout)。**Critical 等級的 page**:這條 > 0 時 watchdog 完全看不到任何 stuck 訂單 — `saga_stuck_failed_orders` gauge 會卡在舊值看起來「健康」但其實是瞎的。對應告警在 2 分鐘內觸發。 |
| Page funnel | `page_views_total{page}` |

### 為什麼 label 在啟動時就有值

程式碼會在啟動時預先註冊預期的 label 組合,避免儀表板對「目前還沒發生過事件」的指標顯示「no data」。新啟動的 stack 上會看到例如 `bookings_total{status="success"} 0` 這樣的初始列。這是有意的 — 見 [observability/metrics.go](../internal/infrastructure/observability/metrics.go) 的開頭。

---

## 3. Prometheus UI 工作流

開 http://localhost:9090。

**會用到的三個 nav:**

| Nav | 用途 |
| :-- | :-- |
| **Graph** | 打 PromQL,按 Execute,切到 Graph 分頁。是「X 有沒有在發生」最快的介面。 |
| **Status → Targets** | 確認 app process 有被 scrape 到(`app:8080/metrics` 旁邊應該是 `UP`)。這裡紅 = scrape 失敗 = 其他所有指標都是過期值。 |
| **Status → Alerts** | 全部已定義的告警,以及目前狀態(Inactive / Pending / Firing)。 |

**值得收藏的查詢:**

```promql
# 訂票漏斗 — 成功 vs 售完 vs 重複 vs 錯誤
sum by (status) (rate(bookings_total[1m]))

# Worker 吞吐
sum by (status) (rate(worker_orders_total[1m]))

# 各路徑的 p99 latency
histogram_quantile(0.99,
  sum by (le, path) (rate(http_request_duration_seconds_bucket[5m]))
)

# DB pool 飽和度 — 持續 > 0 表示 worker 在排隊等連線
db_pool_wait_count

# Stream 積壓 — 健康的 worker 應該把它排空到 ~0
redis_stream_length{stream="orders:stream"}

# 卡在 Charging 的對帳器 — 持續 > 0 = gateway 退化或對帳器跟不上
recon_stuck_charging_orders
```

---

## 4. Grafana 工作流

開 http://localhost:3000,登入 `admin` / `admin`。**Dashboards → Browse → Booking Monitor Dashboard**。

預先配置的 panel 在 [deploy/grafana/provisioning/dashboards/dashboard.json](../deploy/grafana/provisioning/dashboards/dashboard.json)。Provisioning 在 UI 上是**唯讀的** — 你在瀏覽器裡的修改在 `docker compose down` 之後不會保留。要做永久的修改,改 JSON 檔再重啟 Grafana。

Panel 以可摺疊的 row 分組。最上方放「黃金訊號」,reliability / 基礎設施的 row 在下方。

**黃金訊號(dashboard 最上方):**
- Request Rate (RPS) — 依 method/path/status 切分
- Global Request Latency (p99 / p95 / p50)
- Conversion Rate (%) — `bookings_total{status="success"}` / `page_views_total{page="event_detail"}`
- Saturation — Goroutines
- Saturation — Memory Alloc Bytes

**Row:Reliability — Recon(A4 charging 兩階段意圖紀錄)**
- Recon resolved by outcome (rate, 5m) — `charged` / `declined` / `not_found` / `unknown` / `max_age_exceeded` / `transition_lost`
- Recon stuck-charging 計量 — `recon_stuck_charging_orders`(時點值)
- Recon resolve duration p95/p50 — `recon_resolve_duration_seconds_bucket`
- Recon 錯誤率 — find-stuck(資料庫)/ gateway 探測 / mark(資料庫+outbox)

**Row:Reliability — Saga Watchdog(A5)**
- Saga watchdog resolved by outcome (rate, 5m) — `compensated` / `already_compensated` / `max_age_exceeded` / `getbyid_error` / `marshal_error` / `compensator_error`
- Saga stuck-failed 計量 — `saga_stuck_failed_orders`
- Saga watchdog resolve duration p95/p50
- Saga watchdog find-stuck 錯誤率 + saga poison 訊息數

**Row:Dead Letter Queue 活動**
- Kafka DLQ 訊息依 topic + reason — `dlq_messages_total`
- Redis DLQ routed by reason — `redis_dlq_routed_total`
- Kafka consumer retry rate — `kafka_consumer_retry_total`(silent-retry 表面)

**Row:資料庫 — 連線池(USE)+ 正確性訊號**
- PG pool:in-use vs idle — `pg_pool_in_use` / `pg_pool_idle`
- PG pool 等待頻率 + 等待時間 + rollback 失敗 — `pg_pool_wait_count_total` / `pg_pool_wait_duration_seconds_total` / `db_rollback_failures_total`

**Row:Cache — idempotency(N4)**
- Idempotency cache 命中率 (%) — `cache_hits_total{cache="idempotency"}` /(hits + misses)
- Idempotency cache GET 錯誤 — `idempotency_cache_get_errors_total`(會 page:rate >0 持續 1m 表示 idempotency 保護已停擺)
- Idempotency replay 結果 — `match` / `mismatch` / `legacy_match`

**Row:Redis — stream / DLQ 基礎設施失敗**
- Stream/DLQ 失敗率 — `redis_xack_failures_total` / `redis_xadd_failures_total{stream}` / `redis_revert_failures_total`
- Stream collector 錯誤依 stream + operation — `redis_stream_collector_errors_total`

**快速加一個新 panel(暫時的 — 只用來探索):**
1. 點 **+ → Create dashboard → Add visualization**。
2. 選 **Prometheus** 資料來源。
3. 從 §3 貼一個 PromQL,並調整視覺化類型。
4. 如果 panel 值得保留,把 panel JSON 複製出來合進 `dashboard.json`,這樣下次 `down/up` 才不會掉。

---

## 5. 告警

告警定義在 [deploy/prometheus/alerts.yml](../deploy/prometheus/alerts.yml)。狀態在 Prometheus UI → **Alerts**,以及 Alertmanager UI(http://localhost:9093,負責 silence / inhibit / 通知紀錄)。

**Alertmanager 接線(CP6)。** Prometheus 把 firing 的告警推到 Alertmanager(設定檔:[deploy/alertmanager/alertmanager.yml](../deploy/alertmanager/alertmanager.yml))。Alertmanager 負責去重、依 `alertname + severity` 分組、依 severity 設定不同的 repeat 節奏(critical:30 m;warning:4 h;info:24 h)、以及 inhibition(例如 `RedisStreamCollectorDown` 會壓制所有其他 stream-backlog 告警,因為計量本來就已經是 stale)。預設的投遞目標是 `null` — 告警在 Alertmanager 內會去重 / 分組 / 可以 silence,但不會往外推。Slack 投遞是 opt-in 的:把 `alertmanager.yml` 中所有 `null` receiver 換成 `slack` receiver,然後把 Incoming Webhook URL 貼進 `api_url`。

**Runbook annotation(CP5)。** 每個告警都帶一條 `runbook_url` annotation,指向 [docs/runbooks/README.md](runbooks/README.md) 內的某個 section。Alertmanager 會透過 `alertmanager.yml` 中的 template 把該 URL 渲染進 Slack 通知。操作員的工作流:告警觸發 → 通知到達 → 點 runbook → 同一份文件內看到對應的 dashboard panel 與具體的處置步驟。

目前的告警目錄:

| 告警 | severity | 症狀 |
| :-- | :-- | :-- |
| `HighErrorRate` | critical | 5xx 比率 > 5% 持續 5m |
| `HighLatency` | warning | p99 > 2s 持續 2m+(5m rate 視窗 — 抗抖動;Phase 2 cleanup 之前是 1m/1m) |
| `InventorySoldOut` | info | 有訂票嘗試回 sold_out |
| `OrdersStreamBacklogYellow` | info | Stream 長度 > 10K 持續 2m |
| `OrdersStreamBacklogOrange` | warning | Stream 長度 > 50K 持續 2m |
| `OrdersStreamBacklogRed` | critical | Stream 長度 > 200K 持續 1m |
| `OrdersStreamConsumerLag` | warning | 最舊 pending 條目 > 60s 持續 2m |
| `OrdersDLQNonEmpty` | warning | DLQ 有未檢視條目持續 5m |
| `RedisStreamCollectorDown` | critical | Streams scrape 錯誤 → 其他 stream 告警會變沉默 |
| `ReconStuckCharging` | warning | `recon_stuck_charging_orders` > 0 持續 5m |
| `ReconFindStuckErrors` | critical | 對帳器 sweep 查詢失敗中 |
| `ReconGatewayErrors` | warning | 對帳器 gateway 錯誤率升高 |
| `ReconMaxAgeExceeded` | critical | 對帳器強制把訂單標 failed — 需人工檢視 |
| `ReconMarkErrors` | warning | `recon_mark_errors_total` rate > 0 持續 5m — 對帳器在 resolve 階段做 DB transition 失敗 |
| `SagaStuckFailedOrders` | warning | `saga_stuck_failed_orders > 0 for 10m` — compensator 一直在失敗 |
| `SagaCompensatorErrors` | warning | `rate(saga_watchdog_resolved_total{outcome="compensator_error"}[5m]) > 0 for 2m` — 快速路徑配對告警;在 gauge 告警的 10m 視窗到之前就抓到 100% 失敗的 compensator |
| `SagaWatchdogFindStuckErrors` | critical | Watchdog sweep 查詢失敗 — gauge 看起來健康但其實是瞎的 |
| `SagaMaxFailedAgeExceeded` | critical | 卡在 Failed 超過 24h — 需要人工調查(watchdog **不會**自動轉狀態) |
| `KafkaConsumerStuck` | warning | Consumer rebalance retry — 下游依賴退化中 |
| `IdempotencyCacheGetErrors` | warning | `idempotency_cache_get_errors_total` rate > 0 持續 1m — 重複扣款防護被暫停了 |
| `DBRollbackFailures` | warning | `db_rollback_failures_total` rate > 0 持續 5m — UoW Rollback 失敗(driver / connection-state bug) |
| `RedisXAckFailures` | warning | `redis_xack_failures_total` rate > 0 持續 5m — PEL 會無限增長;rebalance 後 consumer 會重做工作 |
| `RedisRevertFailures` | warning | `redis_revert_failures_total` rate > 0 持續 5m — saga 補償時 revert.lua 失敗,Redis 庫存沒被還原 |
| `RedisXAddFailures` | warning | `redis_xadd_failures_total` rate > 0 持續 5m — 訂票 hot path 間歇性無法 enqueue |

> **Worker process 的 metric 抓取 — 已由 O3 後續 PR 補齊。** 上面的 `recon_*`、`saga_watchdog_*`、`kafka_consumer_retry_total`,以及 saga 的 `db_*` / `redis_*` 失敗計數器,都是註冊在 `booking-cli {recon,saga-watchdog,payment}` 這些 worker process 各自的 default Prometheus registry 裡。現在每一個 binary 都會在 `:9091` 開一個 metrics-only HTTP listener(可透過 `WORKER_METRICS_ADDR` 環境變數設定;設為空字串就關掉,適用於 `--once` CronJob 模式),`prometheus.yml` 也補上對應的 scrape job(`payment-worker`、`recon`、`saga-watchdog`)。要確認可以在 Prometheus → Graph 用 `up{job=~"payment-worker|recon|saga-watchdog"} == 1` 驗證;listener 同時也提供 `/healthz`,compose 的 `HEALTHCHECK` 直接用同一個 port 即可。新的 `saga_watchdog` compose service 跑的是 default-loop 模式;`--once` 模式保留給 k8s CronJob 場景,讓 cluster scheduler 控制節奏。

### 故意把告警觸發起來(測試)

要驗證告警的整條管線是否通,把監測對象的指標推到超過門檻 + 撐過 `for:` 窗口。範例:

```bash
# OrdersStreamBacklogYellow — 把 > 10K 條目推進 orders:stream
docker exec booking_redis redis-cli eval \
  "for i=1,11000 do redis.call('XADD','orders:stream','*','probe',i) end return 1" 0
# 等 2-3 分鐘(告警的 `for: 2m`),然後看 Prometheus → Alerts。

# OrdersDLQNonEmpty — 推一條進 DLQ
docker exec booking_redis redis-cli XADD orders:dlq '*' probe 1
# 等 5 分鐘以上(告警的 `for: 5m`)。

# RedisStreamCollectorDown — 讓 Redis 暫時掛掉
docker compose stop redis
# 2m 後告警 fires;`docker compose start redis` 在下一個 scrape 內就把它清掉。

# SagaStuckFailedOrders — 把一筆 Failed 訂單的 updated_at 倒回去,讓它越過 SAGA_STUCK_THRESHOLD。
# 直接 UPDATE 比等自然 Failed→Compensated 卡住更可靠 — 因為 saga consumer 會在毫秒內補償,
# 否則沒辦法製造出 stuck 狀態。
docker exec booking_db psql -U user -d booking -c \
  "UPDATE orders SET status='failed', updated_at = NOW() - INTERVAL '5 minutes' WHERE id = '<某個既有訂單的 uuid>';"
# Watchdog 預設 60s sweep 一次 + 告警 `for: 10m`,所以等 ~11m 後去 Prometheus → Alerts 確認。
# 清理:把該列 UPDATE 回原本狀態,或讓 watchdog 自己重新觸發 compensator
#(它會把 Failed → Compensated,因為這筆資料沒有真正的 reverted Redis key 紀錄)。
```

測試完還原:`docker exec booking_redis redis-cli DEL orders:stream orders:dlq`(會把進行中的 production 資料一起殺掉 — 只能在開發環境做)。

---

## 6. 實用迴圈

日常 senior 工程師的用法:

1. **Grafana** — 「現在有沒有東西閃紅?」
2. **Prometheus** — 「我來寫個 query 調查」
3. **原始 `/metrics`** — 「這個指標到底有沒有被吐出來?」(PR 階段的正確性檢查)
4. **App 日誌**(`docker compose logs -f app`) — 拿背景脈絡 + correlation ID,光靠指標看不到的東西

日誌與指標是相連的:每一行結構化 log 都帶 `correlation_id`,以及(被取樣到時)`trace_id`/`span_id`。Grafana 上看到一個尖峰,把時間戳記下來,在 Jaeger(http://localhost:16686)搜該時段的 trace,把對應的 `correlation_id` 抓出來去 grep app 日誌。串線細節見 [internal/log/](../internal/log/)。

---

## 7. 什麼時候要更新這份指南

這份文件**配對於**真正的觀測性程式碼。對下面任一介面做修改都需要連帶更新這份指南(以及它的英文版):

| 介面 | 檔案 |
| :-- | :-- |
| 指標註冊 | [internal/infrastructure/observability/metrics.go](../internal/infrastructure/observability/metrics.go) |
| 自訂 collector | `internal/infrastructure/observability/*_collector.go` |
| 告警規則 | [deploy/prometheus/alerts.yml](../deploy/prometheus/alerts.yml) |
| Prometheus scrape config | [deploy/prometheus/prometheus.yml](../deploy/prometheus/prometheus.yml) |
| Grafana 儀表板 | `deploy/grafana/provisioning/dashboards/*.json` |

PostToolUse hook([.claude/hooks/check_monitoring_docs.sh](../.claude/hooks/check_monitoring_docs.sh))會在上述任一檔案被編輯時觸發,塞一段提醒進對話,讓 Claude 在收尾前先把這份指南更新好。

如果你不會翻譯,問人類作者,別跳過 zh-TW 那邊的更新 — 結構對齊比完美的中文文筆重要。
