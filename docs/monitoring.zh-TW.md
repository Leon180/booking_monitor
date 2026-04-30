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

目前的 panel 涵蓋:

- Request Rate (RPS)
- Global Request Latency (p99 / p95 / p50)
- Conversion Rate (%)
- Saturation — Goroutines
- Saturation — Memory Alloc Bytes

**快速加一個新 panel(暫時的 — 只用來探索):**
1. 點 **+ → Create dashboard → Add visualization**。
2. 選 **Prometheus** 資料來源。
3. 從 §3 貼一個 PromQL,並調整視覺化類型。
4. 如果 panel 值得保留,把 panel JSON 複製出來合進 `dashboard.json`,這樣下次 `down/up` 才不會掉。

---

## 5. 告警

告警定義在 [deploy/prometheus/alerts.yml](../deploy/prometheus/alerts.yml)。狀態在 Prometheus UI → **Alerts** 看得到。

目前的告警目錄:

| 告警 | severity | 症狀 |
| :-- | :-- | :-- |
| `HighErrorRate` | critical | 5xx 比率 > 5% 持續 5m |
| `HighLatency` | warning | p99 > 2s |
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
| `KafkaConsumerStuck` | warning | Consumer rebalance retry — 下游依賴退化中 |

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
