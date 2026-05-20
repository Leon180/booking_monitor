# interview_prep_notion

# Interview Prep — booking_monitor v1.0.0 (Notion-optimized)

> **Private notes — gitignored, do not commit.** Reference document for mid-level / senior engineering interviews.
Updated 2026-05-12 — **Notion-friendly conversion**:HTML `<details>` toggle 全轉成 `##### 🔽 X` H5 heading(Notion 左邊三角符號可 collapse / expand,paste + import 兩種路徑都 work)。
**跟 `interview_prep.md` 內容 1:1 一致,只差 Notion-render 形式。**
> 

## 📱 How to use on Notion (mobile / commute review)

**Option A — Notion 原生 Markdown import(最乾淨):**
1. Notion app → New page → 右上 `···` → `Import` → 選 `Markdown & CSV`
2. Upload `interview_prep_notion.md` — Notion 把整份 render 成一頁
3. **建議**:把每個 `## Part N` 拖出去變 sub-page,mobile 導航較快

**Option B — 直接 copy-paste 整份(也 OK):**
1. 全選 `interview_prep_notion.md` 內容貼到 Notion 新頁
2. Mermaid 圖偶爾要重 paste 一次才 render(Notion paste 偶爾吃掉 fenced code 開頭)

**通勤怎麼用 collapse 功能:**
- 所有 `##### 🔽 X` H5 heading 在 Notion **左邊都有三角符號,點一下整段收合**
- 通勤滑手機:把所有 H5 收合 → 只看 30 秒口述版 → 想深入某個就展開
- Quick refs:
- Tables → render OK,mobile 寬度有限要左右滑
- Mermaid (````mermaid` blocks) → Notion 2022+ 自動 render(複雜圖偶爾失敗,paste 後 spot-check)
- `##### 🔽 X` → 可 collapse 的 H5 toggle(取代原本的 HTML details)
- Callouts (`> 💡 Tip:` style) → Notion 渲染為 quote block

**Commute workflow** (15-20 min sessions):
- Open the relevant Part page
- Read aloud 3-5 flashcards (Part 9) OR work through 1 Q&A’s full detail
- Mark anything you stumble on with `❓` for review later

**Symbol legend:**
| Symbol | Meaning |
|:-:|:–|
| 💡 | Key insight / mental model |
| ⚠️ | Common mistake / interview trap |
| 🔗 | Reference link (paper / doc / blog) |
| 📊 | Diagram / visual |
| 🎯 | The “money quote” (memorize this) |
| 🔄 | Deep-dive toggle (expandable) |

---

## 🎯 兩個 Mode — 用法分清楚

| Mode | 何時用 | 目的 | 跳到 |
| --- | --- | --- | --- |
| 🆘 **抱佛腳模式** | 面試前 **30-60 分鐘** | 不學新東西,只 refresh 已會的;避免犯低級錯誤 | **→ 見 Part 0** |
| 📚 **平常複習模式** | **通勤 / 睡前 15-30 分鐘** session | 累積深度;建立 muscle memory;補洞 | **→ 見 Part 1+ 全文** + **→ 見 W7-W8 計劃** |

⚠️ **抱佛腳時 NEVER 看 Part 5 / Part 8 / Part 10** — 內容太密,只會 panic。**Part 0 是抱佛腳唯一入口**。

---

## 🗺 Table of Contents — 緊急速查

| 面試官問什麼 | 跳到 |
| --- | --- |
| 「Walk me through your project」 | **→ 見 Part 1 elevator pitch** |
| 「Tell me about a time…」(技術 STAR) | **→ 見 Part 2** |
| 「Tell me about conflict / weakness / failure」(behavioral) | **→ 見 Part 2.5** |
| 任何技術深度 Q | **→ 見 Part 3** |
| 「production-ready 還缺什麼?」 | **→ 見 Part 4** |
| 履歷 bullet 防守 | **→ 見 Part 5** |
| LinkedIn / 學歷 / 薪資 | **→ 見 Part 6** |
| 面試前 30 分鐘 | **→ 見 Part 7** + **→ 見 Memory refresh table** |
| Stack 5 秒答案 | **→ 見 Part 8** |
| **Flashcards 主動回憶練習** | **→ 見 Part 9** |
| 通用系統設計題(rate limiter / URL shortener / chat / notification) | **→ 見 Part 10** |
| 談薪 / counter / decline | **→ 見 Part 11** |
| 「Do you have questions for me?」 | **→ 見 Part 12** |
| 答不出 / 答錯 / 表現差 | **→ 見 Part 13** |
| 面試結束後 thank-you note + 追進度 | **→ 見 Part 14** |

**面試前 30 分鐘必看 3 件事:**
1. **→ 見 Memory refresh table**(數字 + PR 對照)
2. **→ 見 Part 9 flashcards** 隨機抽 5 張口述
3. **→ 見 Part 12 closing questions** 挑 3 個

---

## Part 0 — 抱佛腳 30 分鐘速查

> ⏰ **使用時機**:面試前 **30-60 分鐘**(在咖啡廳 / 通勤車上 / 自家門口)。
💡 **規則**:**不學新東西,只 refresh 已會的**。每個 sub-section 1-3 分鐘掃過去。
⚠️ **NEVER** 在這 30 分鐘看 Part 5 / Part 8 / Part 10 全文 — 內容太密只會 panic。
🎯 **目標**:讓「**已經知道**」的東西**叫得出名字**。新內容此時學不會。
> 

### 0.A — 自我介紹 90 秒(口述版,直接背)

> 「我是 Leon。後端工程師,~2 年Go後端工程經驗。
> 
> 
> 近期主要做三件事:
> 
>  **一**, 公司系統的簽核流程模組維護，優化與開發 — 我修了一個 slow query 從 12 秒到 277 毫秒, **45 倍，另外改善原本的n+1問題以及將散亂的服務重構為狀態機模式**。
> 
> **二**, 寫了一個內部使用的 LLM agent 叫 commeet-text2sql, 讓 PM 用自然語言查 SQL。
> 
> **三**, 研究搶票系統— booking_monitor v1.0.0, 模擬秒殺訂票場景下服務如何提升吞吐量以及架構設計。
> 
> 我目標是 mid-level Backend Engineer, 希望繼續深入研究分散式系統 / LLM 整合 / production reliability。」
> 

⏱ **計時練習**:錄音念一遍,**必須在 90 秒內念完**。超過 = 太冗,壓縮。少於 60 秒 = 內容不夠厚。

---

### 0.B — 7 個 STAR 故事:Trigger → 講哪個 → 一句話 Punchline

| Interviewer 問什麼 | 講哪個故事 | 30 秒結尾 punchline |
| --- | --- | --- |
| 「Tell me about a conflict」 | 與新主管討論簽核流程重構 | 「技術說服需要雙方對技術有相同認知，在工作上需要不斷學習。」 |
| 「Biggest weakness?」 |  |  |
| 「Why leaving current job?」 | **學東西在 Commeet 路線圖之外** | 「下一個 stretch 是更大規模分散式, Commeet 接下來 6-12 個月看不到。」 |
| 「Tell me about a failure」 | **B4 Cross-company data leak**(8 個 function 漏 company_id) | 「Learning: code review 抓不到 context-aware bug — 真正需要的是 **structural protection**。」 |
| 「Greatest achievement / proud」 | 和同事一起討論完成code style guide | 「在過程中重新理解DDD以及和經驗豐富的同事學習到決策以及技術選型的方式和應該看重的點」 |
| 「Hardest tech problem」 | **故事 2 Lua 單執行緒 ceiling**(VU scaling) | 「找瓶頸要先找物理限制,不是先加機器。」 |
| 「Side project you’re proud of」 | **booking_monitor v1.0.0**(5-stage benchmark) | 「Stage 2-4 都卡 8,400/s 是 Redis 單 key Lua serialization（architectural constraint,非 Redis 容量限制）;Stage 5 Kafka acks=all 5,139/s;Stage 1 1,640/s 行鎖物理上限。」 |

---

---

### 0.D — 最高頻 Q,30 秒答案(背口述版,不要背原文)

| Q | 30 秒答案 |
| --- | --- |
| **「Walk me through your architecture」** | 「HTTP → Nginx → Gin → BookingService → Lua deduct → orders:stream → Worker → PG(同 tx 寫 outbox)→ Relay → Kafka → Saga consumer。4 個 sweeper 在獨立 process。」**[暫停等追問,不要一次講完]** |
| **「Idempotency 怎麼做?」** | 「**4 層**。API 層 `Idempotency-Key` header + body fingerprint。Worker 層 DB UNIQUE partial index。Saga 層 Redis SETNX。Gateway 層 Stripe-side key keyed on order_id。」 |
| **「Saga forward-only 是什麼?」** | 「Garcia-Molina 1987 §5 原意 — saga 只管 failure recovery,不管 happy path。我做 D7 narrowing 刪了 2,500 LOC legacy auto-charge,只剩 D5 webhook + D6 sweeper 兩個 emitter。」 |
| **「為什麼用 Outbox 不用 2PC?」** | 「Kafka **有** transactional producer(KIP-98),但**不解決 Kafka + PG 跨資料庫原子性**。實務 driver 沒好用的 XA exposure。Outbox 用 poll latency(500ms 級)換 operational simplicity + 跨 store 一致性。」**⚠️ 不要說「Kafka 沒 2PC」** |
| **「Slow query 怎麼修?」** | 「EXPLAIN ANALYZE 看 Seq Scan → 追到 GORM `.Table()` 沒套 soft-delete scope → query 漏 `delete_at IS NULL` → planner 推導不出 partial index 條件 → fallback Seq Scan。手動加條件後 12.4s → 277ms。」 |
| **「Multi-tenant 怎麼保證 isolation?」** | 「每個 user-access SQL 都要 `company_id` filter。關鍵洞察:`is_all_dept` EXISTS subquery 要 correlate to **outer** department.company_id,不然 Company A 有任一 has-all user 就會讓 EXISTS 對全世界 department true。」 |
| **「Lua single-thread ceiling 多少?」** | 「~8,330/s。Redis 單執行緒,所有 booking 都在同一個 key 上 serialize。加 worker / 加 client / 加 Redis CPU 都沒用。逃生方案是 section-level key sharding,設計已寫在 blog。」 |
| **「LLM Agent 怎麼設計?」** | 「Multi-stage pipeline:Intent classifier → Column Pruner(CHESS 5× token 縮減)→ ReAct with budget-exhaust soft-fail → cost-driven rewrite with shape-preservation guard → 4-layer table-substitution defense。3 個月 36 PRs solo build。」 |

---

### 0.E — 5 個「絕對不要說」(一句話穿幫)

> ⚠️ 這幾個 senior interviewer 一聽就抓包。背起來避開。
> 
1. ❌ **「2PC 在 Kafka 不存在」**
✅ Kafka **有**transactional producer(KIP-98 since 0.11)。要說「不用 2PC 是因為跨 store 沒好用的 XA driver + operational complexity」
2. ❌ **「我設計了 partial index」**
✅ Index 是 migration 既有的。你的貢獻是「**讓 query 能用上既有的 partial index**」
3. ❌ **「GORM 反 DDD 所以不用」**
✅ Name-calling 不是工程。要說「**評估過 GORM + sqlc,在我們 3 entity 規模手寫 Row layer 邊際效益更好**」
4. ❌ **「我熟 K8s / Pyroscope / Alertmanager」**(其實是 Tier 3)
✅ 「**Familiar with deploying to K8s** — probe pattern, helm chart updates;但**不是 cluster operator**」
5. ❌ **「全是我做的」**(過度宣稱)
✅ Solo build 是 commeet-text2sql / booking_monitor;Commeet 主系統是 team work,要說「**我主導的部份是 X,team 一起做的是 Y**」

---

### 0.F — 5 句話讓 senior interviewer 印象深刻

> 不是炫技,是「**自然冒出**」會顯示工程成熟度的 phrases。在合適時機說。
> 
1. 💡 **「Let me think out loud — 我目前思路是…」**(卡住 / 開放題用)
2. 💡 **「Can I ask a clarifying question first?」**(SD round 開頭用)
3. 💡 **「Actually,let me revise — 我想了一下應該是 X,因為…」**(self-correcting — senior 訊號)
4. 💡 **「Fail open vs fail closed 的取捨在這邊是…」**(設計判斷)
5. 💡 **「Production 上我會配 metric / alert / runbook 是…」**(ops thinking — separator from junior)

---

### 0.G — 反問 interviewer 萬用 5 個(每輪結尾必問)

> Interviewer 一定會問「Do you have questions for me?」**答 No = 大忌**。挑 3-5 個。
> 
1. ⭐ **「What are the top 2-3 challenges this team is solving in the next 6 months?」**(看未來工作 + 對方答案深度)
2. ⭐ **「How is on-call handled? Rotation? Compensation? Alert volume?」**(品質訊號 — 答不清楚 = 紅旗)
3. ⭐ **「What’s something you wish was different about working here?」**(誠實 feedback — 紅旗訊號)
4. ⭐ **「對你個人來說,你目前最 enjoy 跟最 frustrating 的工作項目分別是什麼?」**(萬用,interviewer 通常 enjoy 回答)
5. ⭐ **「What does success look like for someone in this role at 6 months / 1 year?」**(對齊期望 + 證明你想清楚)

---

### 0.H — 緊急失誤恢復腳本(背一個就夠)

> ⏱ **不知道 / 卡住 / 答錯,3 秒內進入這個 script,不要 freeze**。
> 

**情境:完全不知道答案**

```
Honestly, this is outside my experience.
Let me think out loud about how I'd approach it if I had to figure it out —
我會先 [research / consult docs / ask a senior],
然後 [寫小規模 prototype 驗證],
然後 [production rollout]。
這個我願意 1-2 週補上,但不會假裝有經驗。
```

**情境:答到一半發現答錯**

```
等等,讓我修正一下 — 我剛剛說 [X],
我想了一下其實應該是 [Y],因為 [reason]。
不好意思,腦袋熱了一下。
```

**情境:被指出答錯**

```
Got it, thanks for the correction.
So if I understand correctly, [restate correct answer].
Does that match your understanding? [pause]
I learned something — I'll go back and verify this.
```

---

### 0.I — Round-by-Round 速查(進房間前看)

| 你即將進入的 round | 30 分鐘看 0.X |
| --- | --- |
| **HR / Recruiter 第一輪** | 0.A 自我介紹 + 0.G 反問問題 + Part 11 薪資期望 |
| **Hiring Manager round** | 0.A + 0.B STAR triggers + 0.F senior-sounding phrases + 0.G |
| **Tech Lead / Peer Engineer** | 0.C 數字 + 0.D 高頻 Q + 0.E 不要說 + 0.F + 0.H 失誤腳本 |
| **System Design round** | Part 10 SD playbook + 0.F + 0.H + Part 1.5 自家架構圖 |
| **Behavioral / Culture round** | 0.A + 0.B 全 7 個 STAR triggers + Part 2.5 完整 templates |
| **CTO / Founder round** | 0.A + 0.F + Part 12 CTO-tier 反問問題 |

---

### 0.J — 進房間前 60 秒儀式

> ⏰ **面試前 60 秒**(門口 / 視訊 join 前):
> 
1. **深呼吸 3 次**(box breathing:吸 4 / 停 4 / 吐 4 / 停 4)
2. **0.A 念 1 遍**(別念出聲)
3. **0.C 數字掃 1 眼**(45 / 62 / 8,400 等)
4. **0.E 念 1 遍**(避免低級錯誤)
5. **水喝一口**(緊張口乾)
6. **微笑**(視訊也要,聲音會傳遞情緒)

---

## Part 1 — 三層深度的 elevator pitch

### 30 秒版(投履歷自介、第一輪電話、recruiter)

> 高併發 ticket booking 系統,Go 1.25。8,400 req/s 的 booking 熱路徑,Pattern A 預訂+付款解耦(Stripe Checkout 風格),saga 純前向恢復,outbox 事務性發布。從 v0.1.0 同步 baseline 一路演進到 v1.0.0,**同 codebase 跑 5 個架構 stage 做 benchmark 對比**,證明每一層 architectural choice 的價值。Portfolio / 學習專案,~111 PRs,bilingual docs,4 篇 engineering blog。
> 

### 2 分鐘版(初試 / 自我介紹)

加上:

- **問題定義**:模擬搶票 flash sale,單 event 數萬人併發。瓶頸澄清:Redis 容量不是系統瓶頸(Redis 可達 18k–33k req/s);~8,400/s 上限是單一 inventory key 的 Lua 原子序列化（architectural constraint）
- **核心架構選擇**:雙層庫存(Redis 熱路徑 sub-ms + PG SoT);Pattern A 預訂視窗 + Stripe-shape PaymentIntent + webhook 回呼;Outbox + Kafka + Saga compensator
- **D12 5-stage benchmark**:Stage 1 行鎖 1,640/s(PG 上限);Stages 2–4 Lua 序列化 ~8,400/s;Stage 5 Damai durable Kafka intake 5,139/s(Kafka acks=all durability tax −39%)
- **「為什麼 Stage 4 看似最慢實則最好」**這個 counterintuitive 賣點

### 10 分鐘版(技術主管 / staff-level 面試)

加上完整 D-roadmap:

- **D1-D6** Pattern A 核心:reservation → /pay → webhook → expiry sweeper
- **D7 Saga scope narrowing** — 把 happy-path 路徑從 saga 移除(Garcia-Molina 1987 §5 原意:saga 只處理失敗,不該設計成主要 commit path)
- **D4.2** 真實 Stripe SDK adapter — 證明 Clean Architecture port-adapter 換得乾淨
- **D12** 多版本 benchmark + dual-scenario 方法論(intake-only 量天花板,full-flow 量端到端;Ticketmaster / Stripe / Shopify 業界做法)
- **觀測完整度**:36+ metrics,24 alerts,3 runbook surfaces,Prometheus + Grafana + Jaeger
- **過程**:4 輪 multi-agent review pattern,每個非平凡 PR 跑 5-8 個 review agent;blog series 用 hybrid-STAR template

---

## Part 1.5 — 📊 架構圖速查(面試畫白板用)

> 面試常會請你「畫一下你的系統架構」。下面 4 個 mermaid diagram 是你的 3 個 killer project + 1 個關鍵概念(saga forward-only)的視覺速記。Notion 會自動 render。
> 

### Diagram A: booking_monitor 完整架構(Stage 4 / Pattern A)

```mermaid
graph TB
    Client[Browser / Mobile<br/>D8 demo: Vite + React + TS] -->|HTTPS| Nginx[Nginx<br/>Rate Limit + TLS]
    Nginx -->|HTTP| Gin[Gin Router<br/>middleware: requestid, recovery, CORS]

    Gin -->|POST /api/v1/book| BookingSvc[BookingService]
    Gin -->|POST /api/v1/orders/:id/pay| PaymentSvc[PaymentService]
    Gin -->|POST /webhook/payment| WebhookHandler[Webhook Handler]

    BookingSvc -->|Lua: DECRBY + XADD<br/>atomic single-call| Redis[(Redis 7<br/>Lua + Streams + PEL)]
    Redis -->|orders:stream| Worker[Worker<br/>XREADGROUP + Idempotency]
    Worker -->|UoW: order + outbox| PG[(PostgreSQL 15<br/>advisory lock + partial index)]
    Worker -->|XACK| Redis

    PG -.->|advisory lock 1001<br/>leader election| OutboxRelay[OutboxRelay]
    OutboxRelay -->|publish| Kafka[(Kafka<br/>order.failed topic)]
    Kafka -->|consumer group| SagaConsumer[Saga Compensator<br/>in-process inside app]
    SagaConsumer -->|revert.lua| Redis
    SagaConsumer -->|MarkCompensated| PG

    PaymentSvc -->|/v1/payment_intents| Stripe[Stripe API<br/>real adapter v82]
    Stripe -->|webhook| WebhookHandler
    WebhookHandler -->|MarkPaid + emit failed| PG

    subgraph Sweepers["4 Sweepers (independent processes)"]
        Recon[Reconciler<br/>stuck charging]
        SagaWatchdog[Saga Watchdog<br/>stuck failed]
        Expiry[Expiry Sweeper<br/>reservation TTL]
        Drift[Inventory Drift Detector]
    end

    Recon --> PG
    SagaWatchdog --> PG
    Expiry --> PG
    Drift --> PG
    Drift --> Redis

    subgraph Observability["Observability"]
        Prom[Prometheus<br/>36+ metrics]
        Grafana[Grafana<br/>18 panels]
        Jaeger[Jaeger / OTEL<br/>auto-injected IDs]
        Alert[Alertmanager<br/>24 alerts → runbook]
    end
```

### Diagram B: commeet-text2sql LLM Agent Pipeline

```mermaid
flowchart LR
    User[User Question<br/>自然語言] -->|POST /api/generate| Handler[Handler<br/>HTTP timeout 90s]

    Handler -->|SSE stream init| Stage1[Stage 1: Intent Classifier<br/>5 fixed classes<br/>fail-open → IntentOther]
    Handler -.->|parallel via errgroup| Stage2[Stage 2: RAG Retrieval<br/>schema docs from PG<br/>parallel: embed + DB load]

    Stage1 --> Stage3[Stage 3: Column Pruner<br/>CHESS: +2% BIRD, 5× token reduction<br/>guard-rails preserve id/deleted_at/company_id]
    Stage2 --> Stage3

    Stage3 --> Stage4[Stage 4: Planner<br/>Structured CoT<br/>5 numbered sub-sections]

    Stage4 --> Stage5{Stage 5: ReAct Loop<br/>MaxReActTurns = 5}
    Stage5 -->|budget exhausted| SoftFail[Soft-fail Recovery<br/>surface partial SQL<br/>single-step only]
    Stage5 -->|SQL produced| Stage6[Stage 6: Cost-driven Rewrite<br/>shape guard regex<br/>≥20% cost improvement gate]
    SoftFail --> Stage6

    Stage6 --> Stage7[Stage 7: 4-Layer Defense<br/>1 table allowlist<br/>2 RAG context check<br/>3 SQL parser walk<br/>4 EXPLAIN gate]

    Stage7 -->|valid SQL| QueryEvent[(query_event audit log<br/>intent_tag, plan_cost,<br/>attempt_count, correction_hint)]
    Stage7 -->|invalid| BuildHint[BuildCorrectionHint<br/>raw DB error verbatim]
    BuildHint --> Stage4

    QueryEvent --> User2[User receives SQL + EXPLAIN cost]
```

### Diagram C: Saga Forward-Only Recovery(Garcia-Molina 1987 §5)

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant API as API Server
    participant PG as PostgreSQL
    participant K as Kafka
    participant S as Saga Compensator
    participant R as Redis

    Note over C,R: ✅ Happy path — NO saga involvement
    C->>API: POST /book
    API->>R: Lua deduct (atomic)
    API->>PG: INSERT order (status=awaiting_payment)
    API-->>C: 202 + reserved_until + links.pay
    C->>API: POST /pay
    API->>API: Stripe PaymentIntent
    C->>API: confirm via Stripe Elements
    API->>API: webhook → MarkPaid
    Note over API: Happy path ends. Saga NEVER ran.

    Note over C,R: ❌ Failure path — Saga forward recovery
    alt Reservation expires (D6 sweeper)
        PG->>K: emit order.failed (expired)
    else Payment failed (D5 webhook)
        API->>K: emit order.failed (payment_failed)
    end
    K->>S: consume order.failed
    S->>R: revert.lua (idempotent SETNX)
    S->>PG: MarkCompensated
    Note over S: Saga ONLY handles failure recovery.<br/>D7 deleted 2,500 LOC of happy-path code from saga scope.
```

### Diagram D: 5-Stage Architecture Comparison(D12 benchmark)

```mermaid
graph LR
    subgraph Stage1["Stage 1 — sync"]
        S1API[API] --> S1PG[(PG<br/>SELECT FOR UPDATE)]
    end

    subgraph Stage2["Stage 2 — Redis Lua + sync DB"]
        S2API[API] --> S2Redis[(Redis Lua)]
        S2Redis --> S2PG[(PG sync INSERT)]
    end

    subgraph Stage3["Stage 3 — Redis Lua + async worker"]
        S3API[API] --> S3Redis[(Redis Lua)]
        S3Redis --> S3Stream[Redis Stream]
        S3Stream --> S3Worker[Worker]
        S3Worker --> S3PG[(PG)]
    end

    subgraph Stage4["Stage 4 — full Pattern A + saga"]
        S4API[API] --> S4Redis[(Redis Lua)]
        S4Redis --> S4Stream[Redis Stream]
        S4Stream --> S4Worker[Worker + Outbox]
        S4Worker --> S4PG[(PG)]
        S4PG --> S4Kafka[Kafka]
        S4Kafka --> S4Saga[Saga Compensator]
    end

    subgraph Stage5["Stage 5 — Damai durable Kafka intake"]
        S5API[API] --> S5Redis[(Redis Lua<br/>no XADD)]
        S5Redis --> S5Kafka[Kafka acks=all]
        S5Kafka --> S5Consumer[IntakeConsumer]
        S5Consumer --> S5Worker[Worker]
        S5Worker --> S5PG[(PG)]
    end

    Stage1 -.->|intake-only<br/>1,640/s<br/>row-lock plateau| Result1[行鎖物理上限]
    Stage2 -.->|intake-only<br/>8,344/s<br/>Lua single-key ceiling| Result2[Redis 單 key 序列化上限]
    Stage3 -.->|intake-only<br/>8,473/s| Result3[~ ceiling]
    Stage4 -.->|intake-only<br/>8,389/s| Result4[~ ceiling, NOT penalized by saga]
    Stage5 -.->|intake-only<br/>5,139/s<br/>−39% durability tax| Result5[Kafka acks=all overhead]
```

🎯 **money quote**:「Stage 2-4 都卡在 ~8,400/s 是 Lua 對單一 inventory key 的原子序列化——Redis 容量本身可達 18k–33k req/s,不是 Redis 瓶頸。Stage 5 付了 Kafka durability tax(acks=all)換到 5,139/s。Stage 1 才是真實的 PG 行鎖架構上限。」

---

## Part 2 — 5 個 STAR 故事(主動找機會講)

每個都有對應的 in-repo blog / PR / benchmark 證據,**面試官問「最難的東西」之類的時候用**。

### 故事 1:Cache-truth incident — 411/1000 silent message loss

- **Situation**: 一次 `make reset-db` 用 FLUSHALL,結果跑壓測時 411 of 1000 booking 從 Redis stream 消失
- **Task**: 找出為什麼大量訊息靜默消失;設計一個能讓未來類似事件**被發現**的架構(不是「修補」這次)
- **Action**: 拆出「Redis 是 ephemeral cache,PG 才是 SoT」的契約;`reset-db` 改成精準 DEL 不碰 stream;startup rehydrate 用 SETNX 不用 SET;新增 `consumer_group_recreated_total` counter + `ConsumerGroupRecreated` critical alert(NOGROUP self-heal 會丟訊息,**但至少能被觀測到**);新增 InventoryDriftDetector 跑跨層比對
- **Result**: 4 PR(#73-#77)/ v0.4.0 / 一篇 blog post `2026-05-cache-truth-architecture.md`
- **Take-away**: 「靜默失敗的事故,修補不重要,**讓未來能被發現**才重要」

### 故事 2:Lua 單執行緒物理上限 — 8,330/s

- **Situation**: 跑 VU scaling stress test,RPS 在 8,330/s 卡死,加 worker / 加 client / 加 Redis CPU 都沒用
- **Task**: 找根因
- **Action**: 分析發現 hot path 是單一 Redis key 上的 Lua DECRBY,Redis 單執行緒 → 所有 booking serialize 在那個 key 上。8,330/s 是該 key 的物理上限,不是 Redis CPU、不是網路、不是任何「加機器能解」的東西
- **Result**: 一篇 blog `2026-05-lua-single-thread-ceiling.md`(已收錄 section-level sharding 設計但未實作)。寫出來證明已找到逃生方案
- **Take-away**: 「找瓶頸要先找物理限制,而不是先加機器。`accepted_bookings/s` 平在 8,330 → 我得問是不是單執行緒問題」

### 故事 3:Saga 不該管 happy path — D7 scope narrowing

- **Situation**: D4 之前 saga 同時處理(a)付款成功的 confirm 路徑 +(b)失敗的補償 — 補償邏輯混在主路徑裡,測試困難,observability 複雜
- **Task**: 證明 saga 應該只負責失敗補償(forward-only),不該設計成 happy path 的一部分
- **Action**: 回去翻 Garcia-Molina 1987 原文,§5 確實只講「failure recovery」。把 happy path 從 saga 抽出來,**刪掉**整個 `payment.Service.ProcessOrder` 路徑 + `payment_worker` binary + `order.created` Kafka topic + consumer + `domain.PaymentCharger` interface
- **Result**: PR #98 — 刪了 ~2,500 LOC,saga 的 `order.failed` topic 只剩 D5 webhook + D6 sweeper 兩個 production emitter。一篇 blog `2026-05-saga-pure-forward-recovery.md` 講 “Garcia-Molina 1987 §5 的 39 年後實作”
- **Take-away**: 「重要的設計決策是知道**什麼不該放進系統**。刪掉 2,500 LOC 比寫 2,500 LOC 重要」

### 故事 4:Dual-scenario benchmark — 不被 http_reqs/s 騙

- **Situation**: D12.5 跑 5-stage 對比,如果只報 `http_reqs/s` 一個數字,Stage 4(saga 完整)會看起來比 Stage 2(只有 Redis Lua)慢,造成「加東西反而慢」的錯誤結論
- **Task**: 設計能正確比較架構成本的 benchmark 方法論
- **Action**: 引用 Ticketmaster Q1’23(250 票/秒峰值)+ Stripe PaymentIntent docs(intent / authorization / settlement 三層分離)+ Shopify BFCM 2024(4.7M edge req/s 跟 accepted-orders/s 是不同 metric)業界做法。**雙場景 benchmark**:`intake-only` 量「載入閘 ceiling」、`full-flow` 量「端到端業務 throughput」。Stage 1 intake-only = 1,640/s(行鎖物理上限);Stages 2/3/4 intake-only 都~8,400/s(都不是被架構限制,是測試環境網路 cap)
- **Result**: PR #109 / `docs/benchmarks/comparisons/20260509_233752_4stage_c500_d60s/comparison.md` + 9 citations(5 業界 + 4 同儕審查論文)。專案層級的方法論承諾(memory: benchmark_methodology_dual_scenario)
- **Take-away**: 「benchmark 結果取決於你問什麼問題。Aggregate http_reqs/s 從來不是架構成本的 headline」

### 故事 5:Multi-agent review pattern catching CRITs at round 4-5

- **Situation**: D4.2(real Stripe SDK adapter)PR 第 4 輪 multi-agent review 抓到 1 個 CRIT(staging mock-mode silent money void);v1.0.0 closeout PR 第 5 輪 review 抓到 CRIT factual lie(`cmd/booking-cli-stage4/` 目錄不存在 — claim 寫進 release notes 會誤導讀者)
- **Task**: 確認每輪 review 都有遞減收益但仍值得跑(以及證明這不是 “review theater”)
- **Action**: 統計 D4.2 全程 4 輪 review:1 CRIT + 1 HIGH + 11 MEDIUM + 9 LOW。把 pattern 寫成 `feedback_multi_round_review.md` 給未來自己參考
- **Result**: 在 PR body 公開記載每輪 finding 表格。下次面試官問「你怎麼做 code review」,直接拿出來
- **Take-away**: 「一次 review 不夠。Pre-merge / post-fix / post-smoke / closeout 各抓不同東西」

---

## Part 2.5 — Behavioral STAR templates(必背)

> Part 2 的 5 個故事是 **技術 STAR**(深 + 量化)。這裡是 **behavioral STAR** — 面試官「軟性」追問用,通常出現在 HR / hiring manager round。
> 
> 
> 每個 template 結構:**Situation(15 秒)→ Task(10 秒)→ Action(45 秒)→ Result + Learning(20 秒)**。總長 ~90 秒,可念可背。
> 

### B1: 「Tell me about a conflict with a teammate / stakeholder」

**Situation**:
> Commeet 主系統的「自定義簽核流程」(Customize Approval Procedure) feature 中,PM 想用 4 個 boolean flag 表示 4 種狀態 — 簡單但耦合。我認為應該用 explicit error codes (-720/-721/-722 + validation) 建模 state machine。

**Task**:
> 在不擋住 Sprint 進度的前提下,說服 PM 跟 FE 接受新設計。

**Action**:
> 我做了三件事:第一,寫一頁 design doc 比較兩種設計在「未來新增第 5 種狀態」場景下的維護成本;第二,主動找 FE 對 1-on-1,確認 4-error-code 對她的 client-side validation 更好寫(共識先到手);第三,跟 PM open meeting,**先承認**她的設計在「現在」可行,再展示 6 個月後需求成長時的 maintenance cost。

**Result + Learning**:
> 採用了 error code 設計。後來 Sprint 14 真的加了第 5 種狀態,新代碼 < 30 LOC。**Learning**:技術說服不是辯論誰對,是 **align timeframe** — 短期看大家都對,長期才看得出設計差異。

---

### B2: 「What’s your biggest weakness?」

**反原則**:不要說「我太完美主義」這種假謙虛。要說**真正的弱點 + 你正在做什麼補**。

**Template**:
> 「我的一個明顯弱點是 **public technical writing**。我寫 internal docs (commeet-text2sql wiki / booking_monitor blog) 都還順,但對 conference talk 或公開 OSS PR 這種 **for-external-audience** 的內容比較缺練習。
>
> 我在補:過去 3 個月我在 booking_monitor 寫了 4 篇 **bilingual engineering blog**(saga forward recovery / cache-truth incident / Lua single-thread ceiling / detect-but-don’t-fix),雖然還沒對外推廣,但這是我建立公開 writing portfolio 的第一步。下一步是投稿到 dev.to 或 Medium,得到真實 external reader feedback。」

**為什麼這版好**:
- 真實弱點(沒 conference talk / 沒 OSS contrib 是真的)
- 有具體補救行動(blog 已經寫了)
- 顯示 self-awareness + 行動力

**其他可用弱點選項**:
- Coding interview / LeetCode 練習度不夠(誠實,正面是「我把時間花在 production 工作 + 系統設計」)
- 在大規模團隊協作的經驗(team of 5 vs team of 50 的協作模式差別)
- 跨 team 溝通的英文流利度(TOEIC 775 是 OK 但不是 native)

---

### B3: 「Why do you want to leave your current job?」/「Why this company?」

**避免**:抱怨現公司 / 講薪水

**Template — leaving current**:
> 「Commeet 我做了快 2 年,從 backend feature delivery 一路做到主導 UserAccess 權限模型遷移、startup latency 優化、跟 solo build commeet-text2sql LLM agent。我學到很多,team 也很好。
>
> 但我接下來想學的東西在 Commeet 的下一個 6-12 個月路線圖裡看不到 — 具體是 **更大規模的分散式系統**(QPS / GB 級資料量),**或 production-grade AI/ML 整合**(超越我們目前內部工具的規模)。所以時機到了,我想找下一個 stretch。」

**Template — why this company**(投履歷前要 **針對每家公司客製**):

例如投 **Crescendo Lab(MarTech AI SaaS)**:
> 「貴公司的 AI Communication Cloud(MAAC / CAAC / DAAC)跟我在 Commeet 做的 commeet-text2sql LLM agent 在技術主題上非常接近 — 都是企業 production LLM 部署。我想討論我在 Commeet 學到的 cost-driven rewrite + shape-preservation guard,看跟貴公司的 production AI 設計怎麼對比 + 互補。
>
> 另外貴公司服務 700+ 亞洲品牌、跨國 TW/JP/TH/SG,規模比 Commeet 大一個量級,這個 stretch 對我有吸引力。」

**通用版**(來不及客製化時):
> 「我從你們的 engineering blog / 招募文 / Yourator profile 看到 3 件事讓我想加入:[列 3 件具體事]。我覺得我在 [matching skill] 可以直接貢獻,同時想學 [stretch goal]。」

**準備工具**:面試前 30 分鐘 google「[公司名] engineering blog」+「[公司名] tech stack」,找 3 個具體點。

---

### B4: 「Tell me about a time you failed / made a mistake」

**反原則**:不要假裝「失敗只是 learning」— 要承認痛 + 復原。

**Template**:
> **Situation**: Commeet 2024 年底 production 出了一個 cross-company data leak — user-access SQL queries 在 8 個 function 漏了 `company_id` filter。一個關鍵 bug 是 `is_all_dept` EXISTS subquery 沒 correlate to outer `department.company_id`,導致 Company A 的 `has_all` 使用者可以看到 Company B 的部門。
>
> **Task**: 修這個 bug + 確保不會再發生。
>
> **Action**: 我接到 alert → 30 分鐘內找到 root cause → 寫 hotfix PR(8 個 function 同步修)。**但這還不夠** — 我同時做了 3 件事:第一,寫 post-mortem 文件,**not just what we fixed but why we missed it**(code review 沒抓到、test coverage 沒覆蓋 multi-tenant 場景);第二,push 加 multi-tenant SQL lint rule;第三,提議 quarterly tenant isolation audit。
>
> **Result + Learning**: 沒有客戶 escalation(我們在 customer-facing之前就 patch 了)。但更重要的 learning 是:**code review 抓不到「context-aware bug」**(這個 bug 每個 SQL 單獨看都「正確」,只有放在 multi-tenant 上下文才錯)— 需要的不是更多 review,是 **structural protection**(lint rule + integration test fixture)。

**為什麼這版好**:
- 真實失敗(production data leak 是 senior 級的 mistake 但是真的)
- 痛(沒 escalation 是運氣不是設計)
- 復原行動具體(post-mortem + lint + audit)
- Learning 不是廢話(code review vs structural protection 是真洞察)

---

### B5: 「Where do you see yourself in 5 years?」

**避免**:「我想當 manager」/「我想當 staff」(太常見)

**Template**:
> 「短期我想往 **senior backend engineer with strong AI/distributed-systems literacy** 的方向走 — 不只會用,還能在 design phase 預測 production 上的 trade-off。中期(3-5 年)我希望累積到能 **own a service end-to-end at meaningful scale** — 從 design / build / observability / on-call / capacity planning 整套。
>
> 至於 IC vs management 我還沒堅定選邊,但我目前更被 deep IC track 吸引(Staff Engineer 那種角色),因為我享受技術深度多於 people management。」

---

### B6: 「Why should we hire you?」/「What makes you different?」

**Template**:
> 「三件事:
>
> 第一,**solo build 的真實 production 經驗** — commeet-text2sql 我 3 個月 36 PRs 一個人從 architecture 到 K8s deployment 全做完。這證明我能在最少 supervision 下交付。
>
> 第二,**跨領域 background 的系統思考** — 我以前在花旗做 KYC 合規兩年,管 regulatory + workflow design。這個經驗讓我看 backend system 時不只看 code,還看 ops trail / accountability / compliance 漏洞(像我在 Commeet 抓到的 multi-tenant SQL leak)。
>
> 第三,**自主學習速度** — booking_monitor v1.0.0 3 個月 111 PRs solo,從 Stage 1 同步 baseline 一路演進到 Pattern A + saga + 5-stage benchmark + 4 篇 bilingual blog。Hiring me 等於 hire 一個 self-directing engineer,不需要 hand-holding。」

---

## Part 3 — 常見問題清單(分主題,附 talking points)

### A. Architecture / 整體設計

**Q1: 「Walk me through the architecture.」**

- 從 HTTP 進來開始:Nginx rate limit → Gin → BookingService → Redis Lua deduct → orders:stream → Worker → PG(同 transaction 寫 events_outbox)→ OutboxRelay → Kafka → SagaCompensator
- 並行:Reconciler(stuck charging)/ SagaWatchdog(stuck failed)/ ExpirySweeper(過期預訂)/ InventoryDriftDetector — 全部 4 個 sweeper 在獨立 process
- 重點:每一段都解釋**為什麼**(不只是 what)

**Q2: 「Why this stack? Why Go? Why Redis Streams not Kafka for the worker?」**

- Go: high concurrency + 1ms-level latency + 強型別 + 部署簡單(單一 binary)
- Redis Streams 而非 Kafka 給 worker:hot path 要 sub-ms,Kafka producer-side ack 延遲 5-10ms。Redis Stream + consumer group + PEL recovery 可以做到 sub-ms 且 at-least-once
- Kafka 用在 outbox/saga 那層,需要 retention 跟 replay 能力

**Q3: 「Why DDD + Clean Architecture for a portfolio project? Isn’t it overkill?」**

- 答:**正是因為是 portfolio**。展示能 design layers 比 design feature 重要。指 D12 — 5 個 binary 共用 internal/,只有 cmd/ 不同 → 證明 port-adapter 真的能 swap
- 反面:不會在 production 上引入這個 overhead 給 5 entity 的 service,但學習價值是 demonstrable

### B. Concurrency / Consistency

**Q4: 「Redis 跟 PG 怎麼保持一致?如果 Redis 掛了?」**

- 答:Redis 是 ephemeral cache,PG 是 SoT。承諾兩件事:(1)「Redis 看不到」≠「PG 沒有」;(2)startup rehydrate from PG 用 SETNX,不會覆蓋 in-flight。InventoryDriftDetector 跑跨層比對偵測 drift(三個方向 label:`cache_missing` / `cache_high` / `cache_low_excess`)
- **InventoryDriftDetector 設計選擇:偵測但不自動修正**
  - 問題:觀測到 Redis qty ≠ PG qty 時,差值可能是「飛行中」的 revert.lua 還沒落地(非同步 compensation 在路上)。立即用 PG 值覆蓋 Redis = 把那個 in-flight revert 消滅 → 反而造成 inventory leak 或超賣
  - 設計:emit `inventory_drift_detected{event_id, redis_qty, pg_qty, delta}` metric + Grafana alert → operator 確認是否真實 drift 再手動修正
  - 參考:Meta Polaris 系統(2022 [engineering.fb.com/2022/06/08/cache-made-consistent](https://engineering.fb.com/2022/06/08/core-infra/cache-made-consistent/)) 在 exabyte 規模做同一選擇 — 用多時間窗口(1/5/10 分鐘)區分「transient lag」和「永久 inconsistency」,觀測而不自動修正。理由相同:「cache fill 跟 invalidation event race 時,你觀測到的差值本身可能是 race 的一部分。」
- 重點故事:故事 1 cache-truth incident

**Q5: 「Idempotency 怎麼做?」**

- 4 層:(1)API 層 `Idempotency-Key` header + N4 Stripe-style body fingerprint validation(同 key 不同 body → 409,不是 replay);(2)Worker 層 DB UNIQUE partial index on `(user_id, event_id) WHERE status != 'failed'`;(3)Saga 層 `saga:reverted:{order_id}` SETNX in Redis;(4)Gateway 層 Stripe-side `Idempotency-Key` keyed on order_id

**Q6: 「分散式交易怎麼處理?2PC vs Saga vs Outbox?」**

**30 秒答案**(必背口述版):
> 「用 Outbox + Saga,不用 2PC。同一個 PG transaction 寫 order row + outbox row,relay 再非同步發 Kafka。失敗用 saga 補償。」

**2 分鐘深度答案**(被追問就用):
- Kafka **有**transactional producer(KIP-98 since 0.11)— 我們**沒用**因為:
- 它解決的是「Kafka 內多 partition / topic 原子寫入」,**不解決「Kafka + PG 跨資料庫原子性」**
- 跟 PG XA 組合的 XA 2PC 在實務上沒有好用的 driver 暴露這個
- 真正不用 2PC 的理由:**operational complexity** + **performance penalty**(prepared transaction 鎖死資源)
- Outbox 取捨:用 poll latency(500ms 級)換 operational simplicity + 跨 store 一致性
- 重點故事:STAR 故事 3 saga forward-only narrowing

**⚠️ 不要說「2PC 在 Kafka 不存在」— 這是 factually wrong,senior 一聽就抓包。**

### C. Failure Modes

**Q7: 「假設 Worker 在處理訂單時掛了,怎麼辦?」**

- Redis Stream PEL 自動 recovery:新 worker 啟動時用 XPENDING 找 stuck 訊息,XCLAIM 後重試。3 retry budget + linear backoff,exhausted → DLQ
- Worker 自身 NOGROUP self-heal(`consumer_group_recreated_total` counter 記錄)— 但這條會丟訊息,**所以 alert 一觸發就 page**

**Q8: 「Saga 的補償失敗怎麼辦?」**

- SagaWatchdog 跑 DB sweep,找卡在 `failed` 狀態 > 60s 的訂單再驅動 compensator
- 超過 24h MaxAge → 不自動處理,emit `max_age_exceeded` 等人工(避免 phantom revert)
- compensator 自己 13 個 outcome label 細分,每個對應 distinct runbook

**Kafka at-least-once 夠嗎?為什麼還需要 Watchdog?**

Kafka manual commit 的確覆蓋了「compensator crash」場景:offset 沒 commit → rebalance 後 Kafka 重發。這是正確的,**不需要 watchdog**。

Watchdog 真正對應的場景是 **`sagaMaxRetries=3` 耗盡後**:

```
1. order.failed 訊息被 saga consumer 拉到
2. compensation 連續失敗 3 次(例如 Redis 持續 down)
3. 訊息寫入 order.failed.dlq → offset 被 commit
4. Kafka 認為此訊息已處理,不再重發
5. Order 狀態停在 `failed`,Redis inventory 沒回補
6. order.failed.dlq 沒有自動消費者
→ Watchdog 掃到並直接呼叫 compensator function,跳過 Kafka
```

核心設計原則(Garcia-Molina & Salem, SIGMOD 1987 [dl.acm.org/doi/10.1145/38713.38742](https://dl.acm.org/doi/10.1145/38713.38742)):saga 保證「要嘛完整執行,要嘛補償回初始狀態」,但原 paper 假設 compensation 一定最終成功 — 它沒告訴你怎麼保證這件事。Watchdog 是工程層面的答案。

Netflix Conductor([netflixtechblog.com](https://netflixtechblog.com/netflix-conductor-a-microservices-orchestrator-2e8d4771bf40)) 做了同樣的事:「If a worker crashes or a task fails, Conductor retries the task based on predefined policies, **using the persisted workflow state as the source of truth**.」DB 裡的 order status 就是我們的 persisted workflow state。

Temporal([temporal.io/blog/what-is-durable-execution](https://temporal.io/blog/what-is-durable-execution)) 把這個模式稱為 "durable execution":「if the process crashes between steps, it replays from the last completed activity.」我們沒有 Temporal,Watchdog 就是自製的 durable retry scheduler。

**Q9: 「webhook 重送 / 重複的處理?」**

- 4 個機制:(1)`payment_intent_id` partial unique index;(2)race-aware `MarkPaid` SQL 守 `reserved_until > NOW()`;(3)`payment_webhook_late_success_total{detected_at}` 三 label 分流 race window;(4)saga `saga:reverted:{order_id}` SETNX

**詳細機制拆解:**

**Layer 1 — Order 狀態機(主防線)**

webhook 進來時先讀 order status,`isTerminalForWebhook` 判斷:

```
paid / expired / compensated = terminal
→ 如果是 succeeded 打到 failure-terminal → handleLateSuccess(錢到了但預訂已死 → 人工 refund 流程)
→ 其他 terminal → log "duplicate" + 200 ACK(provider 不再 retry)
```

這一層保證同一筆 webhook 不管重送幾次,只有第一次真正執行 DB 轉換。

**Layer 2 — `MarkPaid` SQL predicate**

```sql
UPDATE orders SET status = 'paid'
WHERE id = $1
  AND status = 'awaiting_payment'
  AND reserved_until > NOW()   ← 原子守門
```

`reserved_until` 已過 → `ErrInvalidTransition` → re-read order → 確認 expired → `handleLateSuccess`。D6 expiry sweeper 和 webhook 並發時,SQL predicate 保證只有一個贏。

**Layer 3 — `payment_webhook_late_success_total{detected_at}` metric**

`detected_at` 三個 label 區分 late success 被哪一層抓到:
- `post_expiry`:MarkPaid SQL predicate 失敗
- `post_terminal`:webhook 進來時 order 已是 failure-terminal(前一次 webhook delivery 已處理)
- 任何 `late_success` 都是 Error log + critical alert → operator 去 Stripe 發 refund

**Layer 4 — Saga idempotency(compensation 層)**

實際實作是 **DB-level check**,不是 Redis SETNX:

```go
if order.Status() == domain.OrderStatusCompensated {
    // skip DB transition,但仍執行 Redis revert
    // (前一次可能 DB 成功但 Redis revert 失敗)
    wasAlreadyCompensated = true
}
```

Redis `revert.lua` 本身用 `compensationID` 做 SETNX,script 內部保冪等。

**⚠️ 已知設計缺口(backlog)**:`payment_failed` webhook 目前立即觸發 saga compensation。正確做法:應讓 order 留在 `awaiting_payment`,讓 user 在 `reserved_until` 窗口內重試 `/pay`。Stripe 自己的整合指南把 `payment_failed` 視為 soft retry signal(PaymentIntent → `requires_payment_method`),不是 terminal state。待後續 PR 修正。

### D. Observability / Operations

**Q10: 「production 出事,你的 triage 流程是?」**

- Prometheus → Alertmanager → Slack(都已 wired)。每個 alert 有 `runbook_url` 直連 docs/runbooks/README.md 對應 anchor
- 24 alerts × 1 runbook section each;Grafana dashboard 18 panels 對應 alert
- 重點:**不是有 dashboard 就好**,是 alert ↔︎ runbook ↔︎ dashboard panel 三點都對齊

**Q11: 「Metric 怎麼設計?」**

- RED(rate/errors/duration)per endpoint + USE(utilization/saturation/errors)per resource + domain metrics(bookings_total / inventory_drift_detected_total / saga_compensator_events_processed_total)
- **Pre-warm labels at boot**:不要等第一次失敗才看到 label;從 second 0 開始 alert 可評估
- Outcome label exhaustiveness — saga compensator 13 個 outcome,每個對應 distinct runbook 分流(`unknown` 是 deferred sentinel,fired = code regression)

### E. Trade-offs / 「為什麼選 X 不選 Y」

**Q12: 「為什麼不用 ORM?」**

- 評估過 GORM:anti-DDD(injection 進 entity)+ perf overhead + hooks 黑魔法 → 硬 NO
- sqlc 評估過:合理但在 3 個 entity 規模邊際效益小
- 現狀:手寫 `Row` layer 對應 domain entity,各層責任清楚
- 重點:**有評估** — `memory/orm_research.md` 是當時的 decision log

**Q13: 「Stripe Elements 直接收卡資訊,後端從不碰 — 為什麼?」**

- PCI DSS SAQ A scope 之外。後端只看到 `PaymentIntent.ID` + `client_secret` + webhook events,never raw card
- 不是「省事」是「合規 + 縮小攻擊面」
- 詳見 D4.2 PR + runbook D4.2 cutover section

**Q14: 「為什麼 saga 不用 orchestrator (像 Temporal) 而用 choreography (Kafka events)?」**

- 規模 trade-off:orchestrator 適合 10+ services 的編排,2-3 services choreography 簡單夠用
- 沒有額外 SPOF(orchestrator 本身要 HA)
- 重點:**知道兩者差別**,只是針對這個專案規模選 choreography

### F. 過程 / Code Quality

**Q15: 「你怎麼做 code review?」**

- Multi-agent review pattern(5 個 specialized agents 並行:go-reviewer、silent-failure-hunter、code-reviewer 等)
- 非平凡 PR 跑 3-5 輪 review,每輪抓不同 class 的問題(per-slice / pre-merge / post-fix / post-smoke / closeout)
- 重點故事:故事 5

**Q16: 「測試策略?」**

- testify + go.uber.org/mock + race detector mandatory
- 單元測試(internal/…)+ testcontainers Postgres 整合測試(`make test-integration`)
- Pre-PR Tier 2 verification(`go test -race ./...` AND `make test-integration` 兩者都必須跑)
- 不寫 100% coverage,寫 contract-pinning tests(例如 invariant boundary、SQL predicate 邊界、race-aware MarkPaid 三條件)

---

## Part 4 — interviewer 一定會問的最後一題

**「這個專案如果你要上 production,還缺什麼?」**

答案(節錄自 P0 / P1 清單):

- **P0 — 真擴展前的硬性前提(不做 = 每次事故都失血)**
    - PG primary + replica + 自動 failover
    - Redis HA(Sentinel 或 Cluster)
    - 備份 + DR runbook
- **P1 — 高度建議(沒做的話 scale up 就在等事故)**
    - Circuit breaker 包住 Stripe API
    - `/api/v1/events` POST + `/admin/loglevel` 加 auth
    - Per-user rate limit(現在是 per-IP)
    - Zero-downtime schema migration(`_concurrent` migration variants)
    - Kafka 3-broker cluster + replication factor 3
- **P2 — 需要驗證 / 可能已 ok**
    - OutboxRelay leader election 在 k8s rolling deploy 行不行
    - Trace sampling rate(目前 1%,高 RPS 下太低)
    - Idempotency Redis 依賴的失效模式

**這題答得好等於告訴 interviewer:「我知道我寫的不是 production-ready,但我也知道差距在哪。」** 這比 fake claim「我寫的就是 production code」高分得多。

---

## Part 5 — Cake 履歷逐條 drill(memory refresh + 必問 Q&A)

> 你的 Cake 履歷是「簡潔有力」版,每個 bullet 是一行 hook,**面試官會逐條展開問**。下面是 memory refresh + 預備答案。
> 

---

### Bullet 1: “Optimized slow SQL queries (45× improvement: 12.4s → 277ms)”

> **⭐ THIS Q&A IS FULLY EXPANDED** — use this format as the template for what other Q&As should look like after expansion.
> 
> 
> 🚨 **Verify-before-interview**:這份展開有幾個「看起來像 PR body 的具體數字 / 表名 / 索引名 / 客戶名 / 日期」是**我寫文件時為了結構完整補上的 placeholder,不是從 PR body 抓出來的**。面試前你必須打開實際 PR #5694 把以下欄位的真實值填進來,標 `⚠️ VERIFY` 的地方都要對:
> - 客戶 / company 名稱
> - EXPLAIN ANALYZE 前後的 row 數、buffer 數、cost、execution time
> - 表名、索引名、欄位名
> - Rollout plan 的實際日期
> 
> 數字頭(12.4s → 277ms,45×)有 MEMORY.md 背書是真的;其他細節要對 PR body。
> 

### 🎯 30 秒口述版本(必背)

> 「PR #5694。Production slow query alert 觸發 ⚠️VERIFY[「specific customer / specific tenant」],簽核流程查詢 12 秒。我用 EXPLAIN ANALYZE 追到 root cause: GORM 的 `.Table()` 沒自動套 soft-delete scope,所以 subquery 漏帶 `deleted_at IS NULL` → Postgres planner 推導不出 partial index 條件 → fallback Seq Scan。修法是手動加 WHERE,12.4s 變 277ms,**45×**。同時還修了一個正確性 bug — 之前已刪除的職等節點會出現在 approval routing 結果裡。」
> 

### 📊 視覺速記

```mermaid
graph TB
    A[Slow query alert<br/>12s+ execution] --> B{EXPLAIN ANALYZE}
    B --> C[發現:approval-related subquery<br/>Seq Scan dominating execution time<br/>⚠️VERIFY 具體 buffer 數]
    C --> D{Why Seq Scan?}
    D --> E[GORM `.Table` ≠ `.Model`<br/>沒自動套 soft-delete scope]
    E --> F[Subquery 漏 `deleted_at IS NULL`]
    F --> G[Planner 無法證明 query predicate<br/>蘊含 partial index 的 WHERE 條件]
    G --> H[Fallback Seq Scan]

    I[Fix: 手動加 WHERE] --> J[Planner 選用 partial index]
    J --> K[Index Scan<br/>⚠️VERIFY 真實 buffer 數]
    K --> L[277ms — 45× faster]
```

### 🔄 Deep dive(被追問時展開)

### 🔽 1. GORM `.Table()` vs `.Model()` 差異(關鍵 root cause)

```go
// ⚠️ VERIFY: 下列表名 / join key / WHERE clause 是「pattern 示意」,
// 不是 PR #5694 的逐字 code。面試前對 PR diff 確認真實 query 形狀。

// ❌ Pattern 示意 — buggy 版本(用 .Table())
err := db.Table("<main_table>").
    Joins("JOIN <joined_table> j ON ...").
    Where("<main_table>.deleted_at IS NULL").  // main table 有
    // ❌ joined_table 的 deleted_at IS NULL 沒帶 → subquery 漏
    Find(&result)

// ✅ Pattern 示意 — fix 版本
err := db.Table("<main_table>").
    Joins("JOIN <joined_table> j ON ... AND j.deleted_at IS NULL").  // 手動加
    Where("<main_table>.deleted_at IS NULL").
    Find(&result)

// 為什麼 .Table() 不會自動套?
// .Model(&Model{}) 會走 GORM 的 SoftDelete plugin,自動 inject `deleted_at IS NULL`
// .Table("name") 只把 GORM 當 query builder 用,不套 model-level scope
```

### 🔽 2. Partial Index 為什麼 planner 沒選用

```sql
-- ⚠️ VERIFY 真實 index name / table / column 對著 PR 或 migration:
-- 形狀示意:
CREATE INDEX idx_<table>_<col>
  ON <joined_table> (<lookup_col>)
  WHERE deleted_at IS NULL;
```

🔗 **Reference**: [PostgreSQL — Partial Indexes](https://www.postgresql.org/docs/current/indexes-partial.html)

**Planner 的判斷邏輯**:
- Query: `WHERE <lookup_col> = 123`(沒帶 `deleted_at IS NULL`)
- Index: `WHERE deleted_at IS NULL`
- Planner 需要證明 query predicate **蘊含** (implies) index predicate
- `<lookup_col> = 123` **不蘊含** `deleted_at IS NULL`(可能查的是 deleted row)
- 結論:planner 不敢用 partial index → fallback Seq Scan

加上 `WHERE deleted_at IS NULL` 後,planner 證明成立 → Index Scan。

### 🔽 3. EXPLAIN ANALYZE 前後對比

```
⚠️ VERIFY — 下列是「EXPLAIN ANALYZE output 形狀示意」,
   具體 row 數 / buffer hits / cost 我沒從 PR body 抓出來,需要你補上真實值

-- BEFORE
Seq Scan on <joined_table>
  (cost=??..?? rows=?? width=??)
  (actual time=??..?? rows=?? loops=1)
  Buffers: shared hit=??               -- 真實值在 PR body
  Filter: (<lookup_col> = ANY('{...}'::integer[]))

-- AFTER
Index Scan using idx_<table>_<col>
  (cost=??..?? rows=?? width=??)
  (actual time=??..?? rows=?? loops=1)
  Buffers: shared hit=??               -- 大幅下降
  Index Cond: (<lookup_col> = ANY('{...}'::integer[]))
```

|  | Before | After | Ratio |
| --- | --- | --- | --- |
| Execution time | **12,386 ms** | **277 ms** | **45×** |
| Plan node | Seq Scan | Index Scan | — |
| Shared buffer hits | ⚠️VERIFY | ⚠️VERIFY | ⚠️VERIFY |
| Subplan buffers | ⚠️VERIFY | ⚠️VERIFY | ⚠️VERIFY |

> 只有「Execution time 12,386 → 277 / 45×」是 MEMORY.md 背書的數字。其他比率要從 PR body 抓真值。
> 

### 🔽 4. Production rollout plan(3 步驟)

```mermaid
gantt
    title 3-Step Rollout Plan(示意,⚠️VERIFY 實際 timeline)
    dateFormat YYYY-MM-DD
    section Step 1
    Merge + deploy PR :a1, 2025-01-01, 1d
    Partial index 跟臨時 full index 並存 :a2, after a1, 2d
    section Step 2
    觀察 pg_stat_user_indexes :a3, after a2, 3d
    確認 partial index 真的被選用 :a4, after a3, 1d
    section Step 3
    DROP INDEX CONCURRENTLY 臨時 full index :a5, after a4, 1d
```

**為什麼分 3 步?**

🎯 「保留 full index 當 safety net,如果 PR 出問題要 rollback,full index 還在,query 不會回到 12 秒。觀察期確認後再 drop,避免 single PR rollback 同時失去兩個保護。」

🔗 **Reference**: [PostgreSQL — DROP INDEX](https://www.postgresql.org/docs/current/sql-dropindex.html) (CONCURRENTLY 不能在 transaction block 裡)

### 💡 必問 Q&A

**Q1: 「這個 slow query 是怎麼被發現的?」**

🎯 答:Production slow query alert 觸發 ⚠️VERIFY[specific tenant / company,可填真實客戶名],執行時間 12s+,DBA 或自己看 `pg_stat_statements` 找出來。

⚠️ **不要說「我自己 review code 發現」** — 大型 codebase 不可信。Production alert 是合理 trigger。

---

**Q2: 「跟我說一下 root cause」**

> 答案分兩層:
> 
> 
> **Surface**: subquery 漏了 `WHERE delete_at IS NULL`。
> 
> **Deeper**: GORM `.Table()` 跟 `.Model()` 有 semantic 差。`.Model(&X{})` 走 SoftDelete plugin auto-inject scope;`.Table("name")` 把 GORM 當 query builder,**不套** model-level scope。我們用 `.Table()` 是為了複雜 JOIN 場景,但這個 trade-off 沒人在 code review 時抓到。
> 
> 這同時造成兩個 bug:**正確性**(deleted node 出現在結果)+ **效能**(partial index 沒被選用)。
> 

🔗 **References**:
- [GORM — Soft Delete docs](https://gorm.io/docs/delete.html#Soft-Delete)
- [GORM — Raw SQL & Query Builder](https://gorm.io/docs/sql_builder.html)

---

**Q3: 「Partial Index 怎麼運作?為什麼 Postgres 不直接用?」**

> Partial index 只索引滿足 `WHERE` 條件的 row(索引更小、查詢更快)。Planner 選 index 的條件是**證明 query predicate 蘊含 index predicate**:
> 
> 
> ```
> Index:  WHERE delete_at IS NULL
> Query 1: WHERE node_id = 123                    ← 不蘊含 → ❌ 不選
> Query 2: WHERE node_id = 123 AND delete_at IS NULL ← 蘊含 → ✅ 選用
> ```
> 
> 進階:可以提到 `EXPLAIN (ANALYZE, BUFFERS)` 看 plan node,以及 `pg_stat_user_indexes.idx_scan` 確認 index 真的被用過。
> 

🔗 **References**:
- [Use The Index, Luke! — Partial Indexes](https://use-the-index-luke.com/sql/where-clause/null/partial-index)
- [Postgres EXPLAIN documentation](https://www.postgresql.org/docs/current/using-explain.html)

---

**Q4: 「修完後怎麼確認沒 regression?」**

> 三層驗證:
> 
> 1. **EXPLAIN ANALYZE 對同 query 前後對比** — buffer hits + execution time
> 2. **Production 部署後跑 `pg_stat_user_indexes`** 確認 partial index 的 `idx_scan` 數字成長
> 3. **同 endpoint p99 latency 看 Grafana** — Prometheus 應該看到斷崖下跌

```sql
-- 確認 partial index 真的被選用
SELECT indexrelname, idx_scan, idx_tup_read
FROM pg_stat_user_indexes
WHERE indexrelname = '<your real partial index name>';   -- ⚠️VERIFY from PR
```

---

**Q5: 「為什麼要寫 3 步 rollout plan?直接 drop workaround index 不行嗎?」**

🎯 「Production 已經有臨時 full index 在 cover 之前的 slow query。如果這個 PR 出問題要 rollback,**但 full index 已被 drop,就雙重失守**。保留 full index 當 safety net,觀察期確認 partial index 真的被選用後再 drop。」

⚠️ 這題顯示 senior 級的 production ops thinking — 不要跳過。

---

**Q6: 「為什麼這個 bug 之前 code review 沒抓到?」(會被深挖)**

> 老實答:**Code review 不適合抓 context-aware bug**。每個 `.Table()` 用法單獨看都「正確」(GORM 文件明確支援);**只有放在 multi-tenant + soft-delete 上下文才錯**。
> 
> 
> Learning: 後來我們加了 **SQL lint rule**(檢測 query 沒有 `deleted_at` 條件就 warning),以及 integration test fixture 開始覆蓋「query 不應該回 deleted row」場景。**Structural protection > more code review**。
> 

### ⚠️ Honest gotcha(別假裝)

- 別說「我設計了 partial index」— Index 是 migration 既有的。你的貢獻是**讓 query 能用上它**。
- 「12,386ms / 277ms / 45×」這三個是 MEMORY 紀錄背書的真實值;**其他 specifics(buffer hits / rows / cost / table name / index name / 客戶名 / 日期)我這份 doc 用 placeholder + ⚠️VERIFY 標記**,**面試前你必須打開 PR #5694 把真值填進去**。對著一個沒打過的「1.15M buffer hits」回答 senior 是 credibility 自殺。
- 別說「修完後 100% 確認沒問題」— 你保留 workaround index 就是因為知道有可能 rollback。

### 🔗 References for this Q

- 🔗 [GORM Soft Delete](https://gorm.io/docs/delete.html#Soft-Delete)
- 🔗 [PostgreSQL Partial Indexes](https://www.postgresql.org/docs/current/indexes-partial.html)
- 🔗 [Use The Index, Luke! — IS NULL in PostgreSQL](https://use-the-index-luke.com/sql/where-clause/null/partial-index)
- 🔗 [PostgreSQL EXPLAIN](https://www.postgresql.org/docs/current/using-explain.html)
- 🔗 [Postgres `pg_stat_user_indexes`](https://www.postgresql.org/docs/current/monitoring-stats.html#MONITORING-PG-STAT-ALL-INDEXES-VIEW)

---

### Bullet 2: “Refactored business logic using state machine pattern for approval-workflow”

> **⭐ FULLY EXPANDED**(平常複習用版本)— 用 Bullet 1 同樣 model
> 
> 
> 🚨 **Verify-before-interview**:這份展開的「FSM 架構 / dual-orchestrator 概念 / batch UPDATE pattern」是合理的設計推測,但**具體 helper 名稱 / 表名 / method 行數 / sibling PR # / error code / reviewer quote 我都是 placeholder**,**面試前必須對著 PR #5557 (跟相關 commit message + Gemini bot review 留言) 把以下標 `⚠️VERIFY` 的填真值**。
> 

### 🎯 30 秒口述版本(必背)

> 「PR #5557。我重構了 doc approval signing flow,從原本 entity mutation 跟 DB persistence 混在一起的狀態,改成 **dual-orchestrator FSM**:一個 orchestrator 管 entity state 的演化,另一個管 persistence。每個 state transition 是顯式的 action,可以單獨測試,出 bug 也好定位。同一個 PR 也消除了 sign flow 裡的 N+1 query — 把 per-node DB update 改成 batch update with `UPDATE ... FROM (VALUES ...)` SQL pattern。**⚠️VERIFY:reviewer 引用的話 / 用詞** — 開 PR review 截圖確認你想引用的原話。」
> 

### 📊 視覺速記:Before / After

```mermaid
flowchart LR
    subgraph Before["❌ Before — entity + DB mixed"]
        Sign1[Sign API call] -->|loop| Logic1[business logic]
        Logic1 -->|每次都寫| DB1[(DB)]
        Logic1 --> Logic2[next state logic]
        Logic2 -->|每次都寫| DB1
    end

    subgraph After["✅ After — Dual-orchestrator FSM"]
        Sign2[Sign API call] --> Entity[Entity FSM<br/>state evolution]
        Entity --> Action1[Action 1: validate]
        Action1 --> Action2[Action 2: transition]
        Action2 --> Action3[Action 3: collect final state]
        Action3 --> Persistence[Persistence Handler<br/>single commit]
        Persistence -->|batch UPDATE| DB2[(DB)]
    end
```

🎯 **money quote**:「Entity mutation 跟 persistence 分離後,**unit test 不用碰 DB**,integration test 也只需要驗 final state 一次,從 N 個 assertion 變成 1 個。」

### 🔄 Deep dive(被追問時展開)

### 🔽 1. 為什麼是 Dual-orchestrator(雙 orchestrator)?

簽核流程的 state 演化分兩個 layer:
- **Entity state**:文件本身的狀態(Pending → Signing → Approved / Rejected 等 — ⚠️VERIFY 真實 state 命名)
- **Persistence state**:DB 上儲存的 row(⚠️VERIFY:列出 PR 真實接觸的 table 名稱)

這兩個 state 不是 1-to-1。例如:
- 一個 entity transition(approve)可能造成 **多個** persistence write(更新 node order seq + sign events + notify info)
- 同一個 persistence write 可能 cover **多個** entity transitions(batch sign 一次過多個 node)

**Dual-orchestrator pattern**:

```go
// pseudo-code(實際 file 在 pkg/service/...,我不能 share)

type EntityOrchestrator interface {
    Sign(ctx, doc, userID) (*EntityFinalState, error)
}

type PersistenceOrchestrator interface {
    Commit(ctx, finalState *EntityFinalState) error
}

// Service layer 串起兩個 orchestrator
func (s *DocApprovalProcessSign) Sign(ctx, req) (*Result, error) {
    finalState, err := s.entity.Sign(ctx, doc, userID)  // pure logic, no DB
    if err != nil {
        return nil, err
    }
    if err := s.persistence.Commit(ctx, finalState); err != nil {
        return nil, err
    }
    return buildResponse(finalState), nil   // ⚠️VERIFY 真實 helper 命名
}
```

**好處**:
- Entity layer 完全可在 memory 測(no DB needed)
- Persistence 只 commit final state — 一次 batch update 完成,不是每個 transition 都 round-trip DB
- Bug isolation:邏輯錯 → entity layer;DB 錯 → persistence layer

### 🔽 2. State 跟 Action 怎麼建模(Go 慣用模式)

Go 沒有原生 enum,常見的 FSM 模式有 3 種:

**方法 A:typed int + constant + switch**(我用的應該是這個)

```go
type SignState int

const (
    StatePending SignState = iota
    StateSigning
    StateApproved
    StateRejected
    StateAskingComments
)

// Action 是 state machine 的 verb
type Action func(s *Entity) (SignState, error)

// Transition table
var transitions = map[SignState]map[ActionType]Action{
    StatePending: {
        ActionApprove: approveAction,
        ActionReject:  rejectAction,
    },
    StateSigning: {
        ActionApprove: completeSign,
        ActionAskComments: requestComments,
    },
}
```

**方法 B:interface-based**

```go
type SignState interface {
    Approve(e *Entity) (SignState, error)
    Reject(e *Entity) (SignState, error)
}

type PendingState struct{}
func (PendingState) Approve(e *Entity) (SignState, error) {
    // logic
    return SigningState{}, nil
}
```

**方法 C:函式映射(functional)**

```go
type ActionFunc func(e *Entity) (SignState, error)

actions := map[Transition]ActionFunc{
    {From: StatePending, Action: "approve"}: approveAction,
    {From: StateSigning, Action: "reject"}: rejectFromSigning,
}
```

⚠️ **不知道實際用哪個 → 老實答**:「我面試前最後查一次 PR,確認實際是 enum + switch 還是 interface-based。我記得是前者,但不 100% 確定。」

### 🔽 3. Batch update 用 GORM `VALUES` clause 怎麼運作

GORM 預設的 `db.Save(&records)` 對 slice 做的事是 **per-row UPDATE in a loop** — N 個 row 就 N 個 UPDATE statement,完全 N+1。

**Batch update with VALUES**:

```sql
-- ⚠️ VERIFY 真實表名 / 欄位 / helper 名 (下列為 pattern 示意)

-- 原本(N+1)
UPDATE <signing_table> SET sign_status = 'approved' WHERE id = 1;
UPDATE <signing_table> SET sign_status = 'approved' WHERE id = 2;
-- ... × N

-- 我抽出的 batch helper 生成:
UPDATE <signing_table> AS t
SET sign_status = v.sign_status,
    signed_at  = v.signed_at::timestamp,
    signed_by  = v.signed_by::int
FROM (VALUES
    (1, 'approved', '2025-01-01 10:00:00', 42),
    (2, 'approved', '2025-01-01 10:00:01', 42),
    (3, 'approved', '2025-01-01 10:00:02', 42)
    -- ... N 筆
) AS v(id, sign_status, signed_at, signed_by)
WHERE t.id = v.id;
```

**一個 SQL statement** 就更新 N 個 row,Postgres planner 可以 plan 成 single index scan。

🔗 **Reference**: [PostgreSQL UPDATE … FROM (VALUES …)](https://www.postgresql.org/docs/current/sql-update.html)

### 🔽 4. 抽出的 helper functions(實際 PR 留下的痕跡)

**從 `docs/resume_en.md` 抓到的真實 helper 命名**(resume line 25):

- `BatchUpdateDocApprovalProcessNodeOrderSeq` — 批次更新 node order sequence
- `BatchUpdateDocApprovalProcessNodesCollectionSignEvents` — 批次更新節點集合的 sign event
- `BatchUpdateDocApprovalProcessNodesSignEvents` — 批次更新單一節點的 sign event

⚠️ **VERIFY**:其他配套 helper(entity orchestrator / response builder)我沒有 source-of-truth,面試前 trace PR diff 確認其它 helper 命名。主 service method 從幾行變幾行也需要 ⚠️VERIFY 真實數字。

**為什麼抽 helper 重要**?
- 命名顯示**單一職責**(BatchUpdate{X}{Y}Events 顯示是「per-domain-event 中心化 batch UPDATE」)
- 抽出來後主 service method 變短,讀者能直接從 method 名稱看出責任
- Helper 可以**單獨測試**

### 🔽 5. 別把 PR #5557 跟 Customize Approval Procedure 搞混

你有**兩個**跟「state machine」相關的工作,interviewer 可能問任一個。⚠️ **下表所有 PR # / 時間 / error code 都是 placeholder,面試前必須核對你自己的 PR 紀錄填真值**:

| 工作 | PRs | 時間 | 內容 |
| --- | --- | --- | --- |
| **doc approval FSM 重構** | PR #5557 | ⚠️VERIFY 月/年 | dual-orchestrator FSM + batch update |
| **Customize Approval Procedure 多錯誤碼 state machine** | 「8+ feature PRs」(resume) — ⚠️VERIFY 具體 PR # | **Sprint 11/12, Nov 2024 – Jan 2025**(resume) | 4 個 distinct error code(`-720` / `-721` / `-722` + validation)建模 customize-approval state machine,跨 docs apply / modify / check 三個 API surface |

⚠️ **面試前確認** interviewer 問的是哪一個 — 兩個答案內容不一樣!

### 💡 必問 Q&A

**Q1: 「跟我說一下 state machine 怎麼設計」**

> 看 interviewer 想聽哪一個:
> 
> 
> **PR #5557 版本**:
> 「之前 doc approval signing flow 的 entity mutation 跟 DB persistence 混在一個大 method 裡(⚠️VERIFY 真實前/後行數),難測試也難 debug。我重構成 **dual-orchestrator FSM** — 一個 orchestrator 管 entity state 的演化,另一個管 persistence commit。每個 state transition 是顯式的 Action,可以單獨在 memory 跑 test 不碰 DB。同一個 PR 也消除了 N+1 query,抽出 `BatchUpdateDocApprovalProcessNodeOrderSeq` / `…NodesCollectionSignEvents` / `…NodesSignEvents` 三個中心化的 batch UPDATE helper,把 per-node UPDATE 改成 `UPDATE ... FROM (VALUES ...)` SQL pattern。」
> 
> **Customize Approval 版本(resume verified)**:
> 「自定義簽核流程的 input 參數跟系統設定有 4 種組合 — 不傳 + 系統設了 / 傳了 + 系統沒設 / 傳了 length=0 / 全部有效。我用 4 個 distinct error code(`-720` / `-721` / `-722` + validation)建模這個狀態空間,每個 invariant violation 對應一個 code。跨 docs apply / modify / check 三個 API surface 一致實作,coordinate FE on param propagation。Single-engineer ownership across 8+ feature PRs over 2 months(Sprint 11/12, Nov 2024 – Jan 2025)。」
> 

---

**Q2: 「為什麼選 state machine 而不是 strategy / chain of responsibility?」**

> 「簽核流程的狀態變化有**明確的合法/非法 transition** — 例如 Approved → Pending 是非法的。**State machine 把這個 explicit**;Strategy pattern 是『不同實作換一換』,沒有 transition 概念;Chain of responsibility 是『一個 request 經過多個 handler』,沒有 state evolution。
> 
> 
> 額外好處:FSM 讓 invalid transition 在 **執行期 panic**(或在 Go 用 sentinel error),比 if-else cascading 早發現 bug。」
> 

🔗 **Reference**: [Refactoring Guru — State Pattern](https://refactoring.guru/design-patterns/state)

---

**Q3: 「Entity mutation 跟 persistence 分離,為什麼這樣好?」**

> 三個好處:
1. **測試容易** — entity layer 在 memory,unit test 不需要 DB
2. **單一職責** — entity 管 business logic,persistence 管 DB write
3. **Bug 容易定位** — 邏輯錯在 entity,DB 錯在 persistence,不會混淆
> 
> 
> 進階:這就是 **DDD aggregate pattern** — aggregate 在 memory 演化,repo 只負責 load / save。Vaughn Vernon 的 IDDD 第 6 章專門講這個。」
> 

🔗 **References**:
- [DDD Aggregate Pattern (Martin Fowler)](https://martinfowler.com/bliki/DDD_Aggregate.html)
- [Implementing Domain-Driven Design (Vernon) — Ch 6](https://www.amazon.com/Implementing-Domain-Driven-Design-Vaughn-Vernon/dp/0321834577)

---

**Q4: 「Batch update 跟 state machine refactor 為什麼塞在同一個 PR?」**

> 「**誠實答**:當時 sign flow 已經要動,batch update 是 state machine 抽出 persistence layer 後的自然產物 — `BatchUpdateDocApprovalProcessNodeOrderSeq` / `…NodesCollectionSignEvents` / `…NodesSignEvents` 三個 batch UPDATE helper 就是 persistence orchestrator 內部的實作細節。分開兩個 PR 反而要兩次 review 同一片代碼。
> 
> 
> **事後看**:可能拆兩個 PR 更 reviewable(reviewer 可以分別 verify FSM correctness 跟 perf 改善),但當時是 sprint 壓力 + 結構耦合所以一起做。Learning: refactor 跟 perf 通常是兩件事,**未來拆開做** PR 比較 surgical。」
> 

⚠️ **這題會被深挖**:senior interviewer 會問「你怎麼確定沒 introduce regression」— 準備好答 test coverage 跟 staging verify 步驟。

---

**Q5: 「State 多了你怎麼 migrate?例如新增第 6 個 state」**

> 「FSM 的 transition table 是 explicit 的 — 新增 state 要做 4 件事:
1. 在 `SignState` enum 加新 constant
2. 在 transition table 加新 row + col(從哪些 state 可以進到新 state / 新 state 可以去哪)
3. 實作對應的 Action functions
4. 加 unit test 覆蓋新 transition
> 
> 
> Compile error 會引導你做完前 3 步 — 這是 FSM 比 if-else 好維護的關鍵。」
> 

---

**Q6: 「Persistence layer 怎麼處理 tx rollback?」**

> 「Persistence orchestrator 用 GORM `db.Transaction(func(tx) error {...})`,所有 batch update 在同一個 tx 內。如果任一個 batch failed → return error → GORM 自動 rollback。
> 
> 
> Entity layer 沒 side effect,所以 rollback 不影響它 — entity 只是計算 final state 的 in-memory 物件。」
> 

🔗 **Reference**: [GORM Transaction](https://gorm.io/docs/transactions.html)

### ⚠️ Honest gotcha(別假裝)

- 「**state machine pattern**」這個說法**比實際 implementation 抽象**。Interviewer 如果要看 code,你要能畫出實際的 states + transitions + actions。**面試前 1 天重看 PR #5557 的 commits + 開實際 code**,確認:
    - 是 typed int enum 還是 interface-based?
    - Transition table 用 map 還是 switch?
    - Action 是 function value 還是 method?
    - 真實 helper 命名(我這份 doc 列的是 placeholder)
    - 主 method 從幾行變幾行(我寫的「500-line」是 placeholder)
- 「Dual-orchestrator」+「entity vs persistence 分離」是**我幫你寫文件時依 PR title + N+1 改善這個事實推測的設計結構** — 實際 code 用詞可能不一樣 / 結構可能更細或更粗,**面試前你必須打開檔案 trace 一次,把這份 doc 的 placeholder 換成真實命名**
- 別宣稱「我設計的 pattern」 — pattern 是業界既有的(GoF, DDD aggregate),你的貢獻是**判斷在這個 codebase 適合用它**
- 「Gemini bot reviewer 直接引用 separating entity mutations from database persistence」 — ⚠️ 這句話我沒看過原 review,**面試前打開 PR review 截圖找實際 reviewer 留言內容,只引你真的看到的**

### 🔗 References for this Q

- 🔗 [Refactoring Guru — State Pattern](https://refactoring.guru/design-patterns/state)
- 🔗 [DDD Aggregate (Martin Fowler)](https://martinfowler.com/bliki/DDD_Aggregate.html)
- 🔗 [PostgreSQL UPDATE … FROM (VALUES …)](https://www.postgresql.org/docs/current/sql-update.html)
- 🔗 [GORM Transactions](https://gorm.io/docs/transactions.html)
- 🔗 [Go FSM implementation example (looplab/fsm)](https://github.com/looplab/fsm) — 看別人怎麼做的參考

---

### Bullet 3: “Features in expense-reimbursement, approval-workflow, user access management”

### 這個 bullet 是 catchall,涵蓋多項工作 — 都要準備

interviewer 可能挑任何一塊深問。三個主題各別記憶刷新:

### A. User access management — `UserAccess` 權限模型遷移 (2026 Q1)

**PRs**: #5413, #5445 (主) + #5683 等 follow-up

**做了什麼**:把 `is_controller` 權限模型從 `group_members` 為基礎改成專屬的 `UserAccess` / `UserDeptAccess` entity,橫跨 user CRUD 全 surface:
- Add / AddByAdmin / OpenAPI AddUser
- HR-sync v2(JINS HR 串接)
- BatchImport / Modify v1+v2 / Archive

**Cross-cutting checker**:`ApproveDocAccess` — 撤銷 access 必須驗證對 approval procedures、fee policies、travel policies 的影響(防止某些已經卡在 in-flight 簽核流程的 user 被誤刪 access)

**協調**:DB migration + Wire DI 重接 + cache 失效

**面試 Q**:「為什麼要從 group_members 改成 UserAccess?」
- 答:原本 `is_controller` flag 跟 group membership 綁在一起,但業務上「我有 access」跟「我屬於這個 group」是兩件事 — 例如 budget owner 跨部門就需要 access 但不一定要進 group。專屬 entity 讓兩個語意分離,也讓 access policy 可以獨立演化

**面試 Q**:「migration 期間怎麼處理 backwards compat?」
- 誠實答:有逐步遷移,read path 先支援新 entity,write path 同步寫新舊兩邊一段時間,確認資料一致再切讀新的(這是經典 dual-write strategy)
- ⚠️ 如果你不記得具體怎麼遷移,**回去看 PR #5445 的 commits** — 不要編

### B. Approval workflow — Customize Approval Procedure(2024 Q4)+ doc approval sign state machine(2025 April)

兩個都已在 bullet 2 詳述,這裡 cross-ref

### C. Expense reimbursement — Payment Report M1-M3(2024 Q4)+ 多銀行 EDI 整合(2025 Q4)

**Payment Report M1-M3**(PRs #2904, #3243, #3321)
- M1:設計 `PaymentStatus` Postgres model + delegator,優化 list payment report 的 SQL JOIN
- M2:報銷狀態、本幣總計、會計科目原幣金額、油資資訊整合
- M3:`batchModifyDocFinanceV2` 批次更新

**多銀行 EDI**(PRs #4856, #4861, #4921 等)
- 第一銀行 + 合作金庫(TCB)+ 中信整批付款 + 國泰
- 中信整批付款:當 conversion 失敗時提供 converted file + error detail file,讓財務人員校正後重送
- Sprint 24 網銀付款管理 - 國泰 / 中信 整批付款 + 設定多選

**面試 Q**:「整合銀行 EDI 困難在哪?」
- 答:每家銀行的 fixed-width format 規格不同;errors 是「整批送出後拿到一個 error report」不是即時的,所以要設計 error file 回饋讓人工校正後重送;有些欄位有 byte length / encoding 限制(big5 vs utf8 等)

### D. JINS HR 同步(2025 Q4)

**PRs**: #4795, #4823, #4878, #4930

- TW 正式站串接
- 延長 timeout context 處理上游 sync job 較慢
- Hotfix:job_no=tw00002 的 edge case 要 skip

**面試 Q**:「外部系統同步,你怎麼設計 reliability?」
- Timeout 加長(上游慢);特定 edge case 用 explicit skip;sync 用 batch 不阻塞主流程

---

### Bullet 4: “Built text2sql production LLM agent with Intent classifier, Column Pruner, and ReAct”

⚠️ **這是你最強也最危險的 bullet — interviewer 一定深挖。準備 20-30 分鐘對話量**

### 🎯 30 秒口述版本(必背)

> 「commeet-text2sql 是我在 Commeet 單人開發的 production LLM agent,3 個月 36 PRs。技術主軸是 **multi-stage pipeline**:Intent classifier → Column Pruner(CHESS 研究 5× token 縮減)→ Structured CoT planner → ReAct loop with budget-exhaust soft-fail → Cost-driven SQL rewrite with shape-preservation guard → 4-layer table-substitution defense。建立在 CHESS / BIRD / LITHE 等 paper 的研究基礎上,production 部署在 K8s 跑 SSE streaming 給內部 BE team。」
> 

### 📊 視覺速記(架構圖)

→ 查 **→ 見 Part 1.5 Diagram B**

### 🔗 Reference papers / docs(被深問可以引用)

| 主題 | Paper | 為什麼引用 |
| --- | --- | --- |
| Column Pruner | [CHESS — Contextual Harnessing for Efficient SQL Synthesis](https://arxiv.org/abs/2405.16755) | +2% BIRD accuracy, 5× token reduction on large schemas |
| Evidence slot / BIRD-style annotation | [BIRD benchmark](https://bird-bench.github.io/) | Schema annotation 對 NL→SQL 準確度貢獻 8-20% |
| Cost-driven SQL rewrite warning | [LITHE — Learning Optimizer Choices](https://openproceedings.org/2026/conf/edbt/paper-93.pdf) (EDBT 2026) | LLM 優化 silently drop DISTINCT / change JOIN type |
| Multi-iteration SQL refinement | MAGIC / MAC-SQL | past 2 iterations diminishing returns + linear token cost |
| Industry text-to-SQL agent | [Uber QueryGPT engineering blog](https://www.uber.com/blog/query-gpt/) | Deployment lessons,啟發 instrumentation strategy |
| Stripe API patterns | [Stripe API docs](https://stripe.com/docs/api) | Idempotency-Key convention,error code taxonomy |

### 你實際做了什麼(記憶刷新)

**Repo**: `commeet-text2sql`,2026 Feb–Apr,**solo build 36 PRs in 3 個月**

### 完整 architecture(每個元件你都要能講)

**Stage 1 — Pre-RAG**:
- **Intent Agent** (`pkg/agent/intent.go`) — Pre-Planner LLM classifier;5 個固定類別:`expense_query` / `approval_query` / `user_query` / `doc_query` / `other`;結果寫進 `query_event.intent_tag`(新加 index column);**instrumentation only,不過濾 RAG**;LLM 失敗 / parse 失敗 → `IntentOther`(fail open)

**Stage 2 — RAG**:
- Schema metadata 從 DB 自動 extract(`pg_index + pg_class`)
- Index 知識(PK / unique / secondary,含 INCLUDE list)render 成 `**Indexes:**` block
- Aliases 跟 `**Evidence:**` block(BIRD-style domain knowledge)
- Auto-embed at startup,RAG_TOP_K env override

**Stage 3 — Column Pruner**:
- 從 schema 挑哪幾個 column 進 prompt
- CHESS 研究依據:**+2% BIRD accuracy + 5× token reduction** on large schemas
- JSON output `{"table": ["col1", ...]}`
- **Guard-rails**(LLM 結果無論如何保留):
- `id` + `<table>_id`(PK convention)
- `deleted_at`(soft-delete contract)
- `company_id`(tenancy)
- 任何 FK / Ref column
- LLM error / parse error → 回傳完整 schema(fail open)

**Stage 4 — Planner with structured CoT**:
- `### Thinking` block 重構成 5 個 numbered sub-section(Tables / Joins / Filters / Aggregations / Output columns)
- 不是 free-form reasoning

**Stage 5 — ReAct loop with budget-exhaust soft-fail**:
- `MaxReActTurns = 5`(default)
- 之前:exhaust 直接 HTTP 500
- **soft-fail recovery**:exhaust 後把 last attempted SQL 拿出來,丟給 EXPLAIN gate,失敗就走 existing correction-hint retry path
- 「one extra recovery shot on questions that burn the full turn budget」
- **僅支援 single-step plan** — multi-step soft-fail 安全性不夠
- 新增 `ErrMaxReActReached` sentinel

**Stage 6 — Cost-driven SQL rewrite with shape-preservation guard**(這部份最 senior):
- **動機**:LITHE (EDBT 2026) + E3-Rewrite (2025) 警告 LLM 優化時會 silently drop DISTINCT、改 JOIN type、drop WHERE predicate
- **Shape guard**(regex-based):
- 拒絕:DISTINCT 被拿掉 / LEFT→INNER JOIN 變更 / WHERE predicate 被拿掉 / aggregation fn set 變化 / SELECT list length 變化
- 接受:`COUNT(*) → COUNT(col)`(same aggregation fn)
- **Config**:`ENABLE_COST_OPTIMIZATION` default **OFF** + `COST_OPTIMIZATION_THRESHOLD` default `1e6` postgres cost units
- **Adoption gate**(全部要過):flag on + cost > threshold + rewrite passes ValidateReadOnly + ExplainSQL + rewrite cost < 0.8 × original (≥20% improvement) + shape guard accepts
- **Budget**:single-shot(MAGIC + MAC-SQL 研究說 past 2 iterations diminishing returns)

**Stage 7 — 4-layer table-substitution defense**:
- (1) table allowlist
- (2) RAG context check(SQL 引用的 table 必須在 RAG 取回的 schema 範圍內)
- (3) SQL parser walk(實際解析 SQL AST)
- (4) EXPLAIN gate(Postgres 自己 reject 不存在的 table)
- 每層 fail closed,independent

**Stage 8 — Operational surface**:
- SSE streaming workflow trace(每個 stage 推到前端)
- **Cooperative deadline chain**:HTTP 進來 context → PostgreSQL 各 query 都帶 deadline
- `query_event` audit log:**每筆生成都寫一筆**,含 intent_tag、plan_cost、attempt_count、correction_hint、final SQL、optimized SQL(rejected 也寫)
- K8s-internal `/internal/*` admin API:BE 從 pod 直接 curl 做 KB curation(內網信任 — log aggregator 是 accountability trail)
- `kb-tool` CLI + Makefile wrapper:dev 端 workflow

### 必問 Q&A

**Q: 「為什麼選自己寫不用現成 framework(LangChain / LlamaIndex)?」**
- 答:LangChain abstractions 太多,debug 困難。我們的 use case 很 specific(text-to-SQL 對企業內部 DB),需要對 prompt + RAG + cost-aware retry 完全控制。寫 36 PRs 加起來不到 2 個月,比 fight LangChain 的時間少
- 進階:也可以說「研究文獻(CHESS、BIRD、LITHE)裡的 paper 都直接寫 prompt + LLM 直呼,我跟著做」

**Q: 「Claude API 的 token cost 怎麼算 ROI?」**
- 答:每筆 query 紀錄在 `query_event` 加上 plan_cost。我們監控 token usage per query 跟錯誤率。Column Pruner 是專門為了減 token 設計的 — CHESS 說 5× reduction
- 老實答:這個我**沒有** strict ROI metric,如果問就承認「production observability 比較專注錯誤率跟 quality,token cost 是定期 review」

**Q: 「ReAct soft-fail 的 partial SQL 你怎麼確定是安全的?」**
- 答:**Single-step plan only** — multi-step 的中間 step Result 拿出來給 user 不安全。Single-step 的話 last attempted SQL 一定是要回答 user question 的 attempt,所以拿出來丟 EXPLAIN gate 再走 correction-hint retry 是安全的
- 進階:還是有 EXPLAIN gate + ValidateReadOnly 守門,所以 partial SQL 不會直接執行

**Q: 「shape-preservation guard 用 regex 不會 false positive?」**
- 答:會。Regex 是 coarse fingerprint。我們的取捨是「**fail closed 寧可 reject 正確的 rewrite,也不要 adopt 錯誤的 rewrite**」— LLM 把 DISTINCT 拿掉的代價遠大於拒絕一個本來可行的 optimization。Guard 設計就是 conservative 的
- 進階:可以加 follow-up「未來可以換 SQL AST parser 做更精確的 shape check,但目前 regex 已經 cover 95% 場景」

**Q: 「Cost-driven rewrite 的 threshold 1e6 怎麼定的?」**
- 答:看 `query_event` 累積的 plan_cost 分佈,挑一個能覆蓋「真正慢的 query」但又不會 over-trigger 的點。1e6 大約對應 production 上 p99 的 cost。flag default OFF,production 上才開
- 進階:可以說「等 production 跑一段時間後可能會調」

**Q: 「為什麼 ReAct 跟 cost-rewrite 都用 LLM 但只 single-shot?」**
- 答:MAGIC 跟 MAC-SQL 的研究都顯示 past 2 iterations diminishing returns,但 token cost 是線性成長。Single-shot 平衡 quality 跟成本
- 進階:也可以說「ReAct loop 本身就有 budget(5 turns),如果 single-shot 跟 budget 都用完還沒對,通常是 question 本身需要 clarification,加 iteration 也救不了」

**Q: 「Intent classifier 你怎麼確認 5 個類別涵蓋的對?」**
- 答:從 `query_event.intent_tag` 看 distribution,如果 `IntentOther` 太多就要新增類別。**instrumentation only**(現階段不過濾 RAG)就是為了先觀察分佈再決定下一步
- 答:5 個類別是看公司業務面決定的(費用 / 簽核 / user / doc + other),不是憑空挑的

**Q: 「4 層 table substitution defense,如果 LLM 真的引用 RAG 範圍外的 table 是怎麼發生的?」**
- 答:LLM 有時候會「想起」訓練資料裡的 common table name(像 `users`、`orders`),即使 RAG 沒給它這些。或是 hallucinate 一個 table name 剛好真的存在
- **真實 incident**:audit 發現某些生成可以通過 EXPLAIN(因為 table 真的存在)但 reference 了使用者 RAG 範圍外的 table → 4 層 defense 是 reactive 設計

### Honest gotcha

- **這個 bullet 訊息量最大 — 面試官會花 20-30 分鐘問**。**面試前 1 天**把 `commeet-text2sql` 的 README 跟 PR list 重看一次
- 如果問到非常具體的數字(token cost / latency / accuracy)而你不記得,**直接答「我需要查一下」** — 編造數字被抓到比直接承認更糟
- **這個專案是 internal,不能 share code**。Interviewer 如果問到具體 file path 你可以說「我不能 share code,但 architecture-level 都可以聊」

---

### Side Project: “booking_monitor v1.0.0 — 5-stage architectural comparison harness with saga pattern, 40+ Prometheus metrics, distributed tracing”

### 你實際做了什麼(完整故事在 Part 2 STAR 故事 + Part 3 Q&A 已涵蓋)

**核心數字**:
- **v1.0.0** shipped 2026-05-10
- **111 PRs solo** across 6 tagged releases(v0.1.0 → v1.0.0)
- **3 個月**(2026 Feb–May)
- **Go 1.25**

**5 個 binary**(都共享 internal/ 套件,只有 cmd/ 不同):
- `cmd/booking-cli-stage1` — sync `SELECT FOR UPDATE`(no Redis / Kafka / saga)
- `cmd/booking-cli-stage2` — Redis Lua atomic deduct + sync PG INSERT(no async)
- `cmd/booking-cli-stage3` — Redis Lua + orders:stream + async worker(no Kafka / saga)
- `cmd/booking-cli` — full Pattern A + saga(就是「Stage 4」,只是沒改名)
- `cmd/booking-cli-stage5` — Damai-aligned durable Kafka intake(Lua deduct no XADD → Kafka acks=all → IntakeConsumer → worker → DB)

**Benchmark 數字**(Stages 1–4: [20260509_4stage](https://github.com/Leon180/booking_monitor/blob/main/docs/benchmarks/comparisons/20260509_233752_4stage_c500_d60s/comparison.md); Stage 5: [20260513_5stage](https://github.com/Leon180/booking_monitor/tree/feat/stage5-durable-kafka-intake/docs/benchmarks/comparisons/20260513_141854_5stage_c500_d60s)):

| Stage | Full-flow intake/s | Intake-only ceiling/s | Headroom | 瓶頸 |
| --- | --- | --- | --- | --- |
| 1 (sync FOR UPDATE) | 79.4 | 1,642.9 | 20.7× | PG 行鎖 |
| 2 (Lua + sync PG) | 78.9 | 8,344.7 | 105.8× | Redis 單 key Lua 序列化 |
| 3 (Lua + async stream) | 66.3 | 8,473.5 | 127.7× | Redis 單 key Lua 序列化 |
| 4 (Pattern A + saga) | 31.3 | 8,389.2 | 267.9× | Redis 單 key Lua 序列化 |
| 5 (Damai durable Kafka intake) | 64.5 | 5,139.3 | 79.7× | Kafka acks=all publish latency |

**Saga forward-only**:Garcia-Molina 1987 §5 — saga 只管失敗 recovery,不管 happy path。D7 刪除了 2,500 LOC 的 legacy auto-charge 路徑

**Cache-truth contract**:411-of-1000 silent message loss(FLUSHALL incident)→ 設計 NOGROUP self-heal counter + critical alert + InventoryDriftDetector

**Observability**:40+ Prometheus metrics / 24 alerts(每個都有 runbook_url)/ 18 Grafana panels / OTEL tracing 自動注入 correlation/trace/span ID

**Blog**:4 篇雙語(EN + zh-TW)— cache-truth / Lua ceiling / detect-but-don’t-fix / saga forward recovery

### 必問 Q&A

**Q: 「為什麼要做 5 個 stage 對比?」**
- 答:**證明每一層 architecture 的價值** — 不是「複雜就好」,而是「每加一層解什麼問題、付什麼代價」。Stage 1 行鎖 1,640/s;Stage 2 Lua 跳到 8,400/s(瓶頸從 PG 行鎖 → Redis 單 key 序列化);Stage 3-4 不增加吞吐但增加 scalability headroom;Stage 5 Damai durable Kafka intake 付 −39% durability tax 換取 acks=all 保證
- 進階:這是 senior interview 想看的「empirical 設計」— 不是「我聽說 Lua 比較快所以用 Lua」

**Q: 「Pattern A 是什麼?為什麼這樣設計?」**
- 答:Stripe Checkout / KKTIX 模式 — `POST /book` 變預訂(不是直接扣款),`POST /pay` 拿 PaymentIntent,client 端 confirm 後 webhook 回 server。比直接同步扣款好處:可以解耦客戶端的付款流程跟 server 的訂單邏輯,可以做 reservation expiry sweep,可以 saga 補償

**Q: 「Saga forward-only 是什麼意思?跟標準 saga 有什麼不同?」**
- 答:Garcia-Molina 原始論文 §5 saga 是「主流程 commit 一系列 Ti,有失敗就跑對應的 compensating action Ci 反沖」— 主流程被 saga 管。但實務上很多人把 saga 寫成「我控制整個 happy path」,結果 happy path 跟 compensation 邏輯混在一起難測試。我把 happy path 拿出來,只讓 saga 管 expiry / payment_failed 這兩條失敗路徑 — 刪了 2,500 LOC
- 進階:這篇 blog 有寫:[2026-05-saga-pure-forward-recovery](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-saga-pure-forward-recovery.md)

**Q: 「為什麼 Stage 2 跟 Stage 3 intake/s 差不多?Stage 3 不是更複雜嗎?」**
- 答(counterintuitive bait):**因為兩個都打到 ~8,400/s 的測試環境網路 / k6 VU CPU 上限**,不是 architecture 限制。如果在 bare metal 跑,Stage 3 預期會比 Stage 2 高(async write 不阻塞讀)
- 這個答得好 = 證明你看數字不被表象騙,知道 benchmark methodology

**Q: 「411-of-1000 silent message loss 怎麼發生的?」**
- 答:`make reset-db` 當時用 FLUSHALL,**清掉了 Redis consumer group 跟 stream**。新 worker 啟動 XREADGROUP 看到 NOGROUP 就重建,但用 `$` start position(只看「之後的」新訊息)— 重建期間進來的 411 個 booking message 就被當作「不存在」silent skip
- 修法:不用 FLUSHALL,精準 DEL 只清 cache key;加 `consumer_group_recreated_total` counter + critical alert,讓 NOGROUP self-heal 至少被看見;啟動時 `RehydrateInventory` 用 SETNX 不用 SET 避免覆蓋 in-flight

**Q: 「為什麼把 booking_monitor 寫成 portfolio,而不是用來面試 trick 題?」**
- 答(真心話):這個系統的設計 trade-off 比 LeetCode hard 題有意義 — 它讓我練習 saga / outbox / 多層 idempotency / observability 完整 stack,而不是只練「在白板上寫 quicksort」。Interview 我希望 talk shop,不是表演

### Honest gotcha

- **booking_monitor 是 portfolio**,不是 production 系統 — 別過度宣稱「我跑了真實流量」。如果問到 production-readiness,引用 `docs/interview_prep.md` Part 4 的 P0/P1 清單
- 5-stage benchmark 的數字都在 GitHub 公開的 `comparison.md` 裡 — interviewer 可以直接點開驗。**不要超出 comparison.md 寫的內容**

---

### Citibank bullets + 轉職故事

### Bullet: “Handled corporate client onboarding with Relationship Managers”

**Q: 「KYC 在做什麼?」**
- 答:企業客戶開戶時的盡職調查 — verify 公司結構、UBO(ultimate beneficial owner)、業務性質、預期交易模式,確保符合 AML / OFAC sanctions 法規。RM(Relationship Manager)是業務,我是 compliance 支援,RM 把客戶資料給我,我 verify 後核准開戶

### Bullet: “Promoted within one year with mentoring responsibilities”

**Q: 「升職怎麼來的?具體做了什麼?」**
- 答:第一年 KYC 流程跑熟後,主管讓我帶新人 + 設計新流程(例如自動化部分文件審核的 checklist)。升職本身是 analyst → senior analyst 的內部 promote
- ⚠️ **不要膨脹** — 你不是 manage 一個 team,是 mentor 新人。實話實說

### 轉職故事(最常被問的「為什麼從銀行轉工程?」)

**好的答法**:
- 「我在 Citi 做 KYC 的時候發現自己對 process 自動化 + 系統設計比合規本身更感興趣。我下班自學 Python + Go,做了一些自動化小工具給 team 用。後來決定轉職,2024 年 2 月離開 Citi,3 月進 Commeet 開始寫 production code。」

**避免說**:
- 「金融業我做不下去 / 業界很爛 / 想賺更多錢」— 太情緒
- 「我以前一直想做工程」— 跟現實不符(學歷是 management science)

**強化點**:
- 一年內升職證明你在原領域也能 high performer → 跨領域學習能力強
- 進 Commeet 後 700+ PRs / 啟動延遲 62% / LLM agent → 轉職決定是 validated
- 銀行合規背景對 fintech 公司有獨特價值(KYC / AML / 跨境合規 domain knowledge)

---

### Skills 段 — 預期會被深挖的關鍵字

### Go(主力)

**Q: 「Go 你最熟的部份?」**
- 答方向:concurrency primitives(goroutine / channel / sync.Mutex / sync.WaitGroup / errgroup) + context propagation + interface design(small interfaces / accept interfaces, return concrete types)+ error wrapping with `errors.Is/As`

**Q: 「Go 1.22 / 1.23 / 1.24 / 1.25 你用過什麼新東西?」**
- 1.22:loop var per-iteration scope(`for i := range x` 的 `i` 在每次 iteration 是新的)
- 1.23:iter package(range over function)
- 你 booking_monitor 用 Go 1.25,toolchain 是 `go1.25.9`
- ⚠️ 如果不記得就**老實說「最近寫的 1.25 但具體新特性沒全 follow」**

**Q: 「Go 的 GC?」**
- 答:concurrent mark-sweep,STW 很短(< 1ms)。GOGC 控制 trigger frequency(預設 100 = heap 翻倍時 trigger)。booking_monitor 用 GOGC=400 + GOMEMLIMIT 一起,可以在不爆 OOM 的前提下讓 GC 少跑
- 你 PR #14/#15 GC 調優的 RPS 從 7,984 → 20,552(+157%) — 這是可以拿出來講的

### PostgreSQL

**Q: 「Partial index 怎麼用?跟 normal index 差別?」**
- 答(已在 bullet 1 detail):只 index 滿足 WHERE 條件的 row,索引小、查詢快,但 planner 必須能推導 query predicate 蘊含 index 條件才會選用
- 範例:`CREATE INDEX ... WHERE deleted_at IS NULL` 只 index 沒被軟刪的 row

**Q: 「Advisory lock 你用過嗎?」**
- 答:booking_monitor 的 OutboxRelay 用 `pg_try_advisory_lock(1001)` 做 leader election,確保只有一個 instance 在 poll outbox。比起跨服務的 redis lock,advisory lock 跟 transaction 同生命週期,relay crash 自動釋放

**Q: 「EXPLAIN ANALYZE 你看什麼?」**
- 答:看 plan node 是 Index Scan / Seq Scan;看 Buffers shared hit / read;看 rows estimated vs actual(差太多代表 stats outdated);看 cost estimate vs actual time
- 你 PR #5694 就用 EXPLAIN ANALYZE 對比 before / after

### Redis

**Q: 「Lua 為什麼比 multi-call 快?」**
- 答:Lua script 在 Redis 內 atomic 執行(single-thread),省掉 client-server round-trip。booking_monitor 的 deduct.lua 在一個 script 內 DECRBY + HMGET + XADD,**單次 Redis call 完成原本 3 個 call 的工作**
- 進階:Lua 在 Redis 是 single-threaded 的物理上限 — booking_monitor 的 blog 寫過 8,330/s 的 single-key ceiling

**Q: 「Streams 跟 Pub/Sub / List 差別?」**
- 答:Streams 有 persistence + consumer group + PEL(Pending Entries List)+ at-least-once delivery。Pub/Sub 是 fire-and-forget,subscriber 沒在線就丟。List 沒 consumer group 概念

### Kafka

**Q: 「Outbox pattern 你怎麼用 Kafka?」**
- 答(已在 Part 3 cover):同一個 PG transaction 寫 order row + events_outbox row → OutboxRelay(advisory-locked leader)poll outbox → publish 到 Kafka → MarkProcessed
- 為什麼:跨 PG + Kafka 沒有 2PC,用 outbox 保證 ordering + at-least-once delivery

**Q: 「saga consumer 怎麼 handle duplicate event?」**
- 答:多層 idempotency — `saga:reverted:{order_id}` SETNX in Redis + DB-level `MarkCompensated` 看當前 status 才轉換

---

### Memory refresh — 快速 lookup table(面試前 30 分鐘 review)

| 數字 / 名詞 | 來源 / 意義 |
| --- | --- |
| **45×, 12.4s → 277ms** | PR #5694 soft-delete leak fix(approval procedure subquery 漏 `delete_at IS NULL`) |
| **62%, 546ms → 204ms** | PR #4537 member-server startup latency optimization |
| **8 functions** | PR #5769 cross-company data leak fix |
| **14 N+1 patterns** | PR #5546 GetUsersAgents batch elimination |
| **4 error codes (-720/-721/-722 + validation)** | Customize Approval Procedure state machine |
| **FSM dual-orchestrator** | PR #5557 doc approval signing state machine |
| **5 fixed classes** | text2sql Intent classifier(expense / approval / user / doc / other) |
| **5×, +2% BIRD** | text2sql Column Pruner CHESS research |
| **MaxReActTurns = 5** | text2sql ReAct default budget |
| **shape guard rejects DISTINCT/JOIN-type/WHERE/aggregation/SELECT-list changes** | text2sql cost-driven rewrite |
| **4 layers** | text2sql table-substitution defense(allowlist + RAG check + parser walk + EXPLAIN) |
| **1,642 / 8,344 / 8,473 / 8,389 / 5,139 req/s** | booking_monitor 5-stage intake-only ceiling(Stage 5 = Kafka acks=all −39%) |
| **411 of 1000** | booking_monitor cache-truth incident silent message loss |
| **2,500 LOC removed** | booking_monitor D7 saga scope narrowing |
| **40+ metrics, 24 alerts, 18 panels** | booking_monitor observability |
| **111 PRs solo, 6 releases, 3 months** | booking_monitor stats |
| **700+ PRs at Commeet** | Commeet tenure stats |
| **TOEIC 775** | English proficiency proof |

---

## Part 6 — Cake 履歷外但會被問的問題

### Q: 「Why no LinkedIn?」

**避免**:「我覺得 LinkedIn 大家很厲害我比不過」(自我矮化)

**建議**:「我把心力放在 GitHub portfolio 跟業界平台(Cake / Yourator / Meet.jobs),LinkedIn 之後會補。」

### Q: 「為什麼大學念管科卻當工程師?」

**建議**:「管科教我 systems thinking + data analytics + decision making — 這些對 BE 設計很有用。寫程式是後來工作 + 自學進來的,booking_monitor portfolio 是我 self-directed 學習的證明。」

### Q: 「TOEIC 775 — 你能用英文討論技術嗎?」

**建議**:「Tech English 工作上每天用(看 RFC / 文件 / 寫 PR description / 看 Stripe / Anthropic docs)。對話流利度比書面差一點,但跟 international team 跨時區協作沒問題。」

### Q: 「你目前薪資 + 期望薪資?」

**台灣本土**:現職薪資 ± 15-25%。例如現在月薪 65k,期望 75-85k
**台灣外商**:現職薪資 × 1.3-1.5。例如現在 65k,期望 90-110k
**Remote international**:USD 60k-90k 視公司
**Mid-level BE 的 anchor**:你 2.5 年經驗 + LLM agent 經驗 = 不要低於台灣本土月薪 70k / 外商月薪 90k

如果不知道怎麼回:「I’d love to hear the range for this role first; I want to be fair to both sides.」

---

## Part 7 — 面試前 1 天 checklist

- [ ]  **重看 Cake 履歷一次**,每個 bullet 都能口頭展開 5 分鐘
- [ ]  **重看本 doc Part 5 bullet drill** 一次,memory refresh 那個 table 至少看 2 遍
- [ ]  **重看 5 個 STAR 故事**(Part 2)
- [ ]  **準備一個近期讀過的技術文章 / book**(被問「最近在學什麼」時用)
- [ ]  **準備 3 個要問 interviewer 的問題**(team structure / tech debt / on-call rotation / engineering culture)
- [ ]  **booking_monitor 的 README + CHANGELOG v1.0.0 重看一次**
- [ ]  **commeet-text2sql 的設計筆記 / `.claude/projects` 下的相關 memory** 重看(這個專案 internal,沒 GitHub URL 給 interviewer 看,所以你要記得清楚)
- [ ]  面試當天衣著(視訊也建議襯衫)+ 安靜環境 + 提早 5 分鐘進視訊
- [ ]  **準備一杯水**(緊張會口乾)

---

## Part 7.5 — 平常複習 · 8 週 schedule(W7-W8 為主)

> 📚 **使用時機**:**通勤 / 睡前 15-30 分鐘** session,每天一塊。
💡 **規則**:**深度 > 廣度**。寧可一段 deep dive 念熟,不要每段都只掃過去。
🎯 **目標**:把 Part 5 / Part 8 / Part 10 的內容變成「**口述時不需要看 doc**」的程度。
> 

### 你目前在 8-week plan 的哪裡

你在 `docs/interview_plan_2months.md`(本地檔案,非 public) Week 6(MQ / 微服務 / API / Auth)。booking_monitor + commeet-text2sql 已 build 完(W3-W5 超前),**多出 30+ 小時可以重新分配**到 W7-W8。

### 平常複習日程(W7-W8 共 14 天)

每天 ~30 min 通勤 / 睡前。每塊有明確 target,**完成就打勾** ✅。

### Week 7(深度技術 + 系統設計)

| Day | 早上通勤 30 min | 晚上睡前 30 min | 完成 |
| --- | --- | --- | --- |
| 1 (Mon) | **Part 5 Bullet 1** 完整讀過(SQL 45×)+ EXPLAIN ANALYZE deep-dive toggle | **Part 9 Flashcards Set A** 數字念 1 輪 | ☐ |
| 2 (Tue) | **Part 5 Bullet 2** 完整讀(state machine — 我會擴寫) | **Part 9 Set B** 概念 30-sec 答案 | ☐ |
| 3 (Wed) | **Part 5 Bullet 4** 完整讀(text2sql,看 Part 1.5 Diagram B + paper references) | **Part 9 Set C** 不知道安全答案 | ☐ |
| 4 (Thu) | **Part 8 Tier 1 — Go**(GMP / errgroup / context — 我會擴寫) | **Part 9 Set D** STAR triggers | ☐ |
| 5 (Fri) | **Part 8 Tier 1 — PostgreSQL**(partial index / advisory lock / EXPLAIN — 我會擴寫) | **Part 0.D** 高頻 Q 30 秒答案口述 3 遍 | ☐ |
| 6 (Sat 1hr) | **Part 10 SD#1 Rate Limiter**:畫 mermaid 圖 + 講 60 秒 framework | **Part 10 SD#2 URL Shortener**:同上 | ☐ |
| 7 (Sun 1hr) | **Part 10 SD#3 Chat / Notification 二選一**:60 秒講出來 | **Mock 30 分鐘 booking_monitor walkthrough**(找朋友或自言自語錄音) | ☐ |

### Week 8(Behavioral + Mock + 最終潤色)

| Day | 早上通勤 30 min | 晚上睡前 30 min | 完成 |
| --- | --- | --- | --- |
| 1 (Mon) | **Part 2 5 個技術 STAR 故事**朗讀一遍(< 2 min/story) | **Part 2.5 B1-B3 behavioral STAR** 朗讀 | ☐ |
| 2 (Tue) | **Part 2.5 B4-B6 behavioral STAR** 朗讀 | **Part 13 失誤恢復腳本** 8 個情境讀熟 | ☐ |
| 3 (Wed) | **Part 11 薪資談判** templates + RSU 速記 | **Part 12 反問問題** 挑 10 個對應 round 念熟 | ☐ |
| 4 (Thu) | **Mock interview #1 with Claude** 30 分鐘 — 隨機抽 Part 5 + Part 8 + 1 behavioral | 看 mock feedback,記弱點 | ☐ |
| 5 (Fri) | **Part 8 Tier 1 — Redis + Kafka**(我會擴寫) | **Part 8 Tier 1 — DDD + Saga + Outbox**(我會擴寫) | ☐ |
| 6 (Sat 1hr) | **Mock interview #2 with Claude — 系統設計 heavy** | 整理 mock #2 弱點 | ☐ |
| 7 (Sun 1hr) | **Final 自我介紹 90 秒練 5 次**(錄音回放,計時) | **Part 0 抱佛腳區全套**過 1 遍 — 確認都記得 | ☐ |

### 每塊閱讀方式(很重要)

**❌ 壞方法**:從頭讀到尾,讀完就忘
**✅ 好方法**:

```
1. 看 30 秒口述版 → 念出聲(不看 doc)
2. 看不出來的卡頓點 → 看 doc deep-dive
3. 卡頓的概念 → 點 reference URL 看 5 分鐘 paper / blog 原文
4. 重新念出聲 → 卡頓減少?yes 過了,no 再 review 一次
```

### 平常複習的「卡頓記錄」

開一個 spreadsheet 或 Notion table,**每次念出聲卡頓的點都記**:

| 日期 | Part / Bullet | 卡頓的問題 | 補洞動作 | 已解決? |
| --- | --- | --- | --- | --- |
| 2026-05-13 | Part 8 Go GMP | 講不清楚 work stealing 怎麼運作 | 看 draveness 6.5 + 寫小程式測試 | ☐ |

**重複卡頓 = 重點補洞區**。面試前 30 分鐘抱佛腳時優先看這個。

### W7-W8 期間的「實戰練習」

不要只在腦中複習。**用 mock interview 強迫自己口述**:

| 練習形式 | 頻率 | 怎麼做 |
| --- | --- | --- |
| **錄音念 STAR 故事** | 每天 1 個 | 用手機錄音 → 回放聽自己 → 卡頓 / 冗長處標記 |
| **白板畫架構**(空中比劃也行) | 隔天 1 次 | 講 booking_monitor / text2sql / approval 任 1 個 + 邊講邊畫 |
| **跟 Claude 做 mock** | Week 8 至少 2 次 | 告訴我「mock 30 分鐘 X round」我當 interviewer |
| **真實面試**(投 Tier 3 開始) | Week 7-8 | 每場結束 30 分鐘內寫 post-mortem |

---

## Part 8 — Stack 分 tier 速查表(每個 keyword 的 5 秒答案)

> **規則**:履歷上每個 keyword 都要能 5 秒內答出兩句話 — 「X 是做什麼用的」+ 「我在哪用過」。
沒法回答 → 拿掉。回答勉強 → 列在 Tier 2/3 + 用對措辭。回答很順 → Tier 1 可重點 highlight。
> 

### 🟢 Tier 1 — 強項,深度擁有,可重點 highlight

### Go (Golang) ⭐ 深度展開

> **平常複習** · 這是 Tier 1 stack 深度展開的範本格式。後面其他 stack 會按這個結構陸續展開。
> 

### 🎯 30 秒口述版(背)

> 「Go 是 Google 設計的 statically-typed 後端語言,主打三件事:M:N goroutine scheduler(GMP)讓 concurrency 變 cheap、channel + sync 兩種同步原語讓你選 share-by-communicate 或 share-memory、單一 binary 加 cross-compile 讓 deploy 簡單。我用 2+ 年 production — Commeet 主系統 + commeet-text2sql + booking_monitor v1.0.0,後者單人寫 ~111 PRs,涵蓋 worker / outbox relay / saga / 4 種 CLI subcommand。」
> 

### 📊 GMP scheduler 心智圖(被問必畫)

```mermaid
graph LR
    G1[Goroutine 1] --> P1[P 1 LocalRunQ]
    G2[Goroutine 2] --> P1
    G3[Goroutine 3] --> P2[P 2 LocalRunQ]
    G4[Goroutine 4] --> GRQ[Global RunQ]
    P1 --> M1[M 1 OS thread]
    P2 --> M2[M 2 OS thread]
    M1 --> CPU1[CPU core]
    M2 --> CPU2[CPU core]
    P1 -.work steal.-> P2
    GRQ -.61-counter.-> P1
```

- **G** = goroutine (~2KB stack 起跳,可動態長大)
- **P** = processor,持有 local run queue(256 slot),`GOMAXPROCS` 控制數量
- **M** = OS thread(kernel-managed),數量大致等於 P,被 syscall block 時 detach P 給其他 M

### 🔄 必背深度展開(每個都點開背一次)

### 🔽 1. GMP scheduler 怎麼運作(必背 — staff 級會深問)

**為什麼是 M:N 不是 1:1?**
- 1:1(C 的 pthread)→ kernel context switch ~1µs + 8MB stack/thread → 10k 連線 = 80GB RAM,GG
- M:N(Go)→ user-space switch ~200ns + 2KB stack → 1M goroutine = 2GB RAM,可行

**P 的意義**
- 1:1 M 直接綁 CPU 沒問題,但 Go 的 GC + scheduler 要 user-space 控制
- P 是 user-space resource container:local runq + memcache + GC assist credit
- M 要拿到 P 才能跑 user-space Go code(syscall 不需要)

**Work-stealing 演算法**
1. M 跑完 P.LocalRunQ → 從 GlobalRunQ 偷一半過來
2. GlobalRunQ 空 → 從其他 P 偷一半(隨機選一個 P 開始,輪過所有 P)
3. 都空 → netpoll(查 epoll 有沒有 ready 的 fd)
4. 還是空 → M 變 spinning(不 park,留著用)有上限

**61-counter 是什麼**
- P 每跑 61 個 goroutine 從 LocalRunQ → **強制 check GlobalRunQ**
- 為什麼?防止 GlobalRunQ 餓死(如果 LocalRunQ 一直滿,沒人會主動去看 Global)
- 數字 61 是 prime,避免跟其他週期(GC、netpoll)共振

**Go 1.14+ 非合作式搶占**
- 1.13 之前 — 只能在 function prologue / channel op / syscall 切換 → for{} 死迴圈會卡死整個 P
- 1.14+ — runtime 對長跑 goroutine 發 SIGURG,signal handler 在安全點 yield
- 面試常考:「Go 怎麼處理 CPU-bound goroutine 不釋放?」答 SIGURG-based async preemption (Go 1.14+)

**Reference**: [Aceld GMP visualization](https://www.bilibili.com/video/BV19r4y1w7Nx)(中文,最清楚的圖解);[Go scheduler design doc](https://docs.google.com/document/d/1TTj4T2JO42uD5ID9e89oa0sLKhJYD0Y_kqxDv3I3XMw/)(Dmitry Vyukov 原始 doc)

### 🔽 2. Channel 五情境決策樹(口述 30 秒能講完)

`hchan` struct 內部:`mutex` + `ring buffer (qcount + dataqsiz)` + `sendq` (waiting senders) + `recvq` (waiting receivers) + `closed` flag。

**為什麼 ring buffer 不用 linked list?**
- ring buffer: 連續記憶體,prefetch friendly,no per-op allocation
- linked list: 每個 element 都 alloc + GC pressure
- Go runtime 在 hot path 上盡量 zero-allocation

**send 五情境**
| 情境 | 行為 |
|:–|:–|
| recvq 有 waiter | 直接 copy 給 recvq 第一個 G,unpark 它 |
| buffer 沒滿 | enqueue 到 buffer,直接走 |
| buffer 滿 / unbuffered 沒人收 | 自己 park,放進 sendq |
| channel = nil | 永遠 block(不會 unblock,直到 GC) |
| channel 已 close | **panic: send on closed channel** |

**receive 五情境** — 對稱類似,差別在 closed channel + buffer 空 → 收到 zero value + `ok=false`。

**Close 規則**(這題超常考)
- 「Don’t close from receive side, don’t close if there are multiple senders」— [Dave Cheney 原文](https://go101.org/article/channel-closing.html)
- 為什麼?多個 sender 你不知道誰會最後寫完;從 receiver 關會 panic on send

**nil channel 應用**
- `select` 配 `nil` channel = 把那個 case 「停用」— 動態調整 select 監聽範圍的常見技巧
- 例:graceful shutdown 把 input chan 設成 nil 後,只剩 timeout case 會醒

**Reference**: [Dmitry Vyukov hchan 解析](https://github.com/golang/go/blob/master/src/runtime/chan.go);[Go blog Pipelines](https://go.dev/blog/pipelines)

### 🔽 3. errgroup vs sync.WaitGroup(必會比較)

| 特性 | `sync.WaitGroup` | `errgroup.Group` |
| --- | --- | --- |
| 等所有 goroutine 結束 | ✅ | ✅ |
| 收集第一個 error | ❌ | ✅ |
| Context 自動取消 | ❌ | ✅(`WithContext`) |
| Bounded concurrency | ❌ | ✅(`SetLimit(N)`) |
| 適用場景 | fire-and-forget | 任一失敗都要中止其他 |

**`SetLimit(0)` 為什麼會 deadlock?**
- source code 用 channel-based semaphore,size = N
- N=0 → semaphore 沒 slot → `Go()` 永遠 acquire 失敗 → 一直 block
- 結論:`SetLimit(>0)` 必須

**`WithContext` 觸發點**
- 任一 goroutine 回 err → cancel ctx → 其他正在跑的 goroutine 用 `ctx.Done()` 自行退場
- 注意:errgroup 不會幫你殺 goroutine,只是 cancel ctx,goroutine 必須**自己 cooperative 退出**

**真實 code**(我 commeet-text2sql Phase 2 用法)

```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(min(len(jobs), maxConns*2/3))  // leave room for tx connections
results := make([]Result, len(jobs))      // pre-allocated, no mutex needed
for i, job := range jobs {
    i, job := i, job
    g.Go(func() error {
        r, err := callLLM(ctx, job)
        if err != nil { return err }
        results[i] = r  // unique index per goroutine, safe without lock
        return nil
    })
}
if err := g.Wait(); err != nil { return err }
```

**Reference**: [errgroup package doc](https://pkg.go.dev/golang.org/x/sync/errgroup);[errgroup source code](https://cs.opensource.google/go/x/sync/+/refs/tags/v0.7.0:errgroup/errgroup.go)

### 🔽 4. Error handling — Go 1.13+ 標準寫法

**`%w` wrapping 語意**

```go
return fmt.Errorf("failed to process order %s: %w", orderID, err)
```

- `%w` 包後 error chain 保留;`%v` / `%s` 包後資訊只在字串裡,`errors.Is/As` 拆不出來
- 一個 `fmt.Errorf` 只能用一個 `%w`(Go 1.20+ 才支援多個 `%w`)

**`errors.Is` vs `errors.As`**
- `errors.Is(err, target)` — 比對「同一個 sentinel」,例:`errors.Is(err, sql.ErrNoRows)`
- `errors.As(err, &targetTyped)` — 把 chain 裡某個 typed error 挖出來,例:`errors.As(err, &pgErr)` 後查 `pgErr.Code`

**Sentinel error 命名**

```go
// 公開的 sentinel
var ErrSoldOut = errors.New("event is sold out")

// 內部的 typed error
type validationError struct {
    field string
    msg   string
}
func (e *validationError) Error() string { ... }
```

- sentinel 放在 package 開頭,讓 caller 用 `errors.Is` 比對
- typed error 放在 caller 想拿欄位的場景(例:把 pg 的 23505 unique violation 變 `ErrIdempotencyConflict`)

**為什麼不用 `pkg/errors`?**
- Go 1.13+ 內建 `%w` 後,`pkg/errors` 的作者 [Dave Cheney 已經 deprecate 該套件](https://github.com/pkg/errors)
- 標準 stdlib 路線:`errors.New` / `fmt.Errorf("%w")` / `errors.Is/As`

**Reference**: [Go blog Working with Errors](https://go.dev/blog/go1.13-errors);[Dave Cheney Stack Traces post](https://dave.cheney.net/2016/06/12/stack-traces-and-the-errors-package)

### 🔽 5. Escape analysis + GC tri-color + hybrid write barrier

**Escape analysis** — compile-time 決定 alloc 在 stack 還 heap
- Stack — function 結束就還,**零 GC pressure**
- Heap — 需要 GC 回收
- 規則:lifetime 超出 function、size 不確定、被 escape 走的 pointer 都 escape 到 heap

**常見 escape source**
- `fmt.Println(x)` — fmt 家族吃 `interface{}`,任何 concrete type 都 escape
- 返回 local pointer:`return &localVar` → 必 escape
- `goroutine` 共享的變數
- `interface{}` 值 — concrete value 必 box,box 在 heap

**怎麼看?** `go build -gcflags='-m' ./...` 印出來:

```
./main.go:10:6: can inline f
./main.go:14:9: &localVar escapes to heap
```

**GC — tri-color mark + hybrid write barrier(Go 1.8+)**
- 白色 = 未掃 / 已掃完不被 reach = 待回收
- 灰色 = 待掃 worklist
- 黑色 = 掃完且 reachable

**4 phase**
1. **Sweep termination**(STW < 100µs)— 結束上一輪 sweep
2. **Mark concurrent**(no STW)— 從 root 開始走,把 white → grey → black,**user code 同時跑**
3. **Mark termination**(STW < 100µs)— 把寫 barrier 累積的 mutator 改動掃完
4. **Sweep concurrent**(no STW)— 把 white block 還給 allocator

**Hybrid write barrier 解決什麼?**
- Mark 期間 user code 改 pointer(`a.x = b`),不小心會把已掃過的 black 物件指到還沒掃的 white 物件 → white 漏掃 → 提前釋放
- Hybrid barrier:寫 pointer 前,**同時 grey 掉新指向的 object + 舊指向的 object**(Dijkstra + Yuasa 結合)
- 結果:不需要 stack write barrier(stack 寫 barrier 太貴,1.8 之前只有 heap barrier)

**GOGC vs GOMEMLIMIT**
- `GOGC=100`(預設)→ heap 用量翻一倍才 GC(100% growth trigger)
- `GOGC=400`(booking_monitor 用)→ heap 用 5x 才 GC,memory 換 CPU
- `GOMEMLIMIT=256MiB` → soft cap,接近時 GC 自動激進,**不會 OOM**(Go 1.19+)
- 兩個搭配:`GOGC=off` + `GOMEMLIMIT=256MiB` → 完全靠 mem limit 觸發 GC(常見 Kubernetes pattern)

**Reference**: [Go GC Guide](https://go.dev/doc/gc-guide);[Rick Hudson GC talk](https://blog.golang.org/ismmkeynote)

### 🔽 6. Generics(1.18+)+ Type inference

**Basic syntax**

```go
func Map[T, U any](s []T, f func(T) U) []U {
    r := make([]U, len(s))
    for i, v := range s {
        r[i] = f(v)
    }
    return r
}
```

**Constraints**(用 `comparable` / `any` / 自訂 interface)

```go
type Number interface {
    int | int64 | float32 | float64
}
func Sum[T Number](s []T) T {
    var sum T
    for _, v := range s { sum += v }
    return sum
}
```

**Type inference**
- `Map[int, string](nums, strconv.Itoa)` — 可省略到 `Map(nums, strconv.Itoa)`,Go 從 args 推
- 1.21 以前 inference 有限制(constraint type inference 不能跨 generic boundary);1.21 後放寬

**什麼時候用 generics?什麼時候用 interface?**
- Generics — 同一份 logic 套用不同 concrete type,**性能優先**(monomorphization,no boxing)
- Interface — 不同 type 但有不同 implementation,**polymorphism**(runtime dispatch)
- 80% 場景:`interface{}` 改 `any` 即可,真正用 generics 的場景在 container / utility(`Map`, `Filter`, `Set[T]`)

**真實限制**
- Generic method 不能有額外 type parameter — 例:`func (s Set[T]) Map[U any]() Set[U]` 不行,只能寫 free function
- Go team 立場:[generics 是 escape hatch,不是預設工具](https://go.dev/blog/why-generics)

**Reference**: [Go blog When To Use Generics](https://go.dev/blog/when-generics);[Generics tutorial](https://go.dev/doc/tutorial/generics)

### Q&A(必背)

### 🔽 Q: Goroutine 跟 OS thread 怎麼比?何時用哪個?

「Goroutine 是 user-space 的 lightweight thread,2KB stack 起跳可動態長,runtime 用 GMP scheduler 把 M 個 goroutine multiplex 到 N 個 OS thread,context switch 大約 200ns。OS thread 是 kernel-managed,8MB stack,switch 大約 1µs,適合 CPU-bound + 不需要大量並行。

Go 你幾乎不會用 raw OS thread — 因為 syscall 跟 cgo 已經幫你包好。但 `runtime.LockOSThread()` 是 escape hatch,某些 cgo library(像是 OpenGL context、CUDA)要求同一個 thread 上下文,這時你要 lock。我自己沒踩過這個 case。」

📚 [Goroutine vs Thread - Go Blog](https://go.dev/blog/concurrency-is-not-parallelism)

### 🔽 Q: channel 跟 sync.Mutex 你怎麼選?

「兩個答案:idiom 跟 reality。

**Idiom**:`Don't communicate by sharing memory; share memory by communicating` — channel pipe data 透過 ownership transfer。但這是大原則,不是教條。

**Reality**:protect single value 用 mutex,coordinate workflow 用 channel。

我 booking_monitor 就同時用兩個 — worker 的 stream consumer 用 channel + select pattern(因為要 multiplex shutdown signal + stream message + PEL recovery),但 idempotency cache 內部就是 sync.Map(本質上是 mutex protected map),因為那只是個 key-value 查表,套 channel 反而 over-engineer。

Dave Cheney 的判準很實用:**如果你的 mutex critical section 開始邏輯化(condvar、wait-notify、多 step coordination),那其實該換 channel + goroutine 重寫;反過來如果你的 channel pattern 退化成「只是要 protect 一個欄位」,那 mutex 才對」

📚 [Dave Cheney - Channels are not Enough](https://dave.cheney.net/2014/03/19/channel-axioms);[Go memory model](https://go.dev/ref/mem)

### 🔽 Q: 你怎麼確保 context 正確 propagate?

「三條紀律,我在 commeet-text2sql Phase 1 寫進 cooperative deadline chain:

1. **首參規則** — 任何可能 block 的 function,`ctx context.Context` 必須是第一個參數。包含 HTTP handler、DB query、HTTP client、第三方 SDK call、long-running computation。
2. **不存 ctx 在 struct** — context 是 request-scoped,存進 long-lived struct 等於 leak request lifetime。Exception:goroutine pool 結束信號可以(但通常 errgroup 已經幫你了)。
3. **deadline 嚴格遞減** — 我 commeet-text2sql 設 PG `statement_timeout=30s` < app middleware 90s < transport 120s,讓 handler 永遠有 headroom 寫 504 response,不會被 transport 提前殺掉。

實作上 — 我用 errgroup.WithContext 或自己 `ctx, cancel := context.WithTimeout(parent, budget); defer cancel()`,**defer cancel() 必須寫**,不然 ctx 漏 leak 到 GC 才回收。」

📚 [Go blog Context](https://go.dev/blog/context);[Sam Alba context anti-patterns](https://blog.golang.org/context-and-structs)

### 🔽 Q: errors.Is / errors.As 跟 type assertion 差在哪?

「Type assertion(`pgErr, ok := err.(*pgconn.PgError)`)只看「err 本身的 dynamic type」,不會走 wrap chain。如果 err 是 `fmt.Errorf("update failed: %w", pgErr)` 包過,你 type assertion 拿到的是 `*fmt.wrapError`,不是 PgError → assertion 失敗。

`errors.Is(err, sentinel)` — 沿著 wrap chain 找有沒有等於 sentinel 的,適合 `sql.ErrNoRows`、`io.EOF` 這種 singleton sentinel。

`errors.As(err, &target)` — 沿著 wrap chain 找第一個能 assignment 到 target 的 typed error,適合「我想拿 PgError 的 Code 欄位」這種 case。

兩個都比 type assertion 安全 — 因為現代 Go code 普遍 wrap error,直接 type assertion 等於賭你 caller 沒包過,賭錯就 silent fail。」

📚 [Go 1.13 errors blog](https://go.dev/blog/go1.13-errors)

### 🔽 Q: GMP 跟 GC 是兩個獨立的 system 嗎?

「不是獨立 — 兩個都跑在同一個 runtime,有 coupling。GC 跑 mark phase 時雖然 concurrent,但 user goroutine 會被加上 **mutator assist** — 你 alloc 越多 GC 跑不夠快,scheduler 會逼你的 G 自己幫忙 mark 一段,本質上是「allocate-mark balance」。所以高 alloc 的 service 在 GC pressure 下會看到 P99 latency 變糟,即使沒 STW。

另外 GC 的 STW phase 雖然短(< 100µs in Go 1.8+),但是 **per-P stop the world** — 每個 P 都要在 safe point 暫停,網路 syscall 卡在 syscall 的 M 不算 STW 但會被掃。

我的實作層面決定:booking_monitor 用 `GOGC=400` + `GOMEMLIMIT=256MiB` — 拉高 GC trigger threshold 換取低頻 GC,代價是 RSS memory 多用,但因為 booking 是 short-lived alloc + low retention,GC 跑得起來。這組設定的 env var 寫在 `.env` + docker-compose 配置,具體 latency 差異要看 `docs/benchmarks/20260412_*` 下的 GC baseline / pprof / compare 三組 run(我沒手抓 p99 從 X 變 Y 的精確數字,只能講『拉高 GOGC 後 GC 跑得頻率變低,benchmark 沒看到 regression』)。」

📚 [Go GC Guide - Mutator Assists](https://go.dev/doc/gc-guide#Mutator_assists);booking_monitor 的 PR #14 GC tuning benchmark

### 🔽 Q: 為什麼選 Go 不選 Rust / Java?

「實用主義回答:

**Vs Rust** — Rust 安全性最強(borrow checker),但 learning curve 陡 + 開發速度慢。商業 backend 80% 是 business logic + IO,Rust 的 zero-cost abstraction 邊際效益 < Go 的「寫快、好招人、有 stdlib」。Rust 適合 system programming(資料庫、proxy、embedded)。

**Vs Java** — Java 生態最完整(Spring Cloud / Kafka client 都最成熟),但是 JVM startup + heap 開銷大,GC tuning 複雜。Go 單一 binary cold start 50ms,Java Spring Boot 動輒 5-10s。Cloud-native + microservice 場景 Go 大幅勝出。我們現在開 lambda / k8s pod scale up,start time 直接影響成本。

**Go 的 sweet spot**:network-bound + concurrent 多、需要 cross-compile single binary、生態夠用(net/http + DB driver + Kafka client 都 mature)、招人不要培訓 6 個月就能上手。Commeet 主系統就是這個 profile。我 commeet-text2sql + booking_monitor 也是同樣的考量。」

📚 [Discord migrate Go → Rust 案例](https://discord.com/blog/why-discord-is-switching-from-go-to-rust);[Cloudflare Go usage post](https://blog.cloudflare.com/go-at-cloudflare/)

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我熟 Go runtime internals」** — 真話是「我會 GMP 高層概念、看過 hchan struct、看過 1.14 preemption 演化」,**沒貢獻過 runtime patch**,沒在 production 拿 pprof 抓 scheduler bug
- ❌ **「我會手寫 generics library」** — 你用過 `any`、看過 `Map[T any]` 的範例,但沒寫過 production-grade generic data structure
- ❌ **「我做過 Go GC tuning」** — 真話是「我設過 GOGC + GOMEMLIMIT 跑 benchmark 比較」,**沒手調過 pacer**,沒寫 GC 相關的 issue
- ✅ 安全 phrasing:**「I have a solid mental model of GMP scheduling and GC tri-color algorithm. I’ve tuned GOGC + GOMEMLIMIT for booking_monitor with benchmark evidence. I haven’t worked on the runtime internals themselves.」**

### 📚 References — Go 深度題備案 must-read

- [Go scheduler design doc - Dmitry Vyukov](https://docs.google.com/document/d/1TTj4T2JO42uD5ID9e89oa0sLKhJYD0Y_kqxDv3I3XMw/) — GMP 設計起點
- [Go GC Guide](https://go.dev/doc/gc-guide) — 官方 GC tuning 指南
- [Aceld GMP 中文視覺化](https://www.bilibili.com/video/BV19r4y1w7Nx) — 中文最清楚的 scheduler 圖解
- [Go memory model](https://go.dev/ref/mem) — happens-before 規則,channel/mutex 的 ordering guarantee
- [Effective Go](https://go.dev/doc/effective_go) — 經典 idioms,口試前再讀一次
- [Go FAQ](https://go.dev/doc/faq) — interviewer 問「為什麼 Go 沒 X?」答案幾乎都在這
- [Dave Cheney blog](https://dave.cheney.net/) — error handling / channel / package design 的權威來源

---

### PostgreSQL ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「PostgreSQL 是 production-ready 關聯式 DB,我用 2+ 年。我能深聊三件事:**partial index** — 我在 Commeet PR #5694 把 12s 慢查降到 277ms(45×),靠 `WHERE deleted_at IS NULL` 條件式索引讓 planner 走 Index Scan;**advisory lock** — 我在 booking_monitor 用 `pg_try_advisory_lock(1001)` 做 OutboxRelay 多實例 leader election;**MVCC + isolation** — Read Committed 是預設,看不到 dirty read 但會有 non-repeatable read,所以高敏感的 financial flow 要主動拉 Repeatable Read 或 Serializable。」
> 

### 📊 索引決策樹(被問必畫)

```mermaid
flowchart TD
    Q[Query 出來慢] --> EX[EXPLAIN ANALYZE]
    EX --> Plan{Plan Node?}
    Plan -->|Seq Scan| Sel{Selectivity?}
    Plan -->|Index Scan| Done[OK]
    Plan -->|Bitmap Heap Scan| Multi[多 index 結合]
    Sel -->|< 5% rows| Idx[加 index]
    Sel -->|> 30% rows| OK[Seq Scan 正常]
    Idx --> Cond{Predicate 有 WHERE?}
    Cond -->|Yes 且固定| Partial[Partial Index]
    Cond -->|No / 多種| Full[Full B-tree Index]
    Partial --> Verify[pg_stat_user_indexes 確認 idx_scan 增加]
    Full --> Verify
```

### 🔄 必背深度展開

### 🔽 1. Partial Index — planner 怎麼決定要不要用?

**Partial index 定義**

```sql
CREATE INDEX idx_orders_active ON orders(user_id)
    WHERE deleted_at IS NULL;
```

這個 index 只包含 `deleted_at IS NULL` 的 row,size 比 full index 小很多。

**Planner 套用規則(關鍵)**
- Query `WHERE user_id = 1 AND deleted_at IS NULL` → ✅ 用 partial(planner 推導 query predicate 蘊含 index WHERE)
- Query `WHERE user_id = 1` → ❌ 不用 partial(query 沒蘊含 index WHERE,planner 不敢用)
- 解法 1:query 改寫成包含 `AND deleted_at IS NULL`
- 解法 2:用 application-side default filter(GORM `.Where("deleted_at IS NULL")`)

**為什麼這跟我 PR #5694 有關?**
- GORM `.Model(&User{})` 自動加 `deleted_at IS NULL` → 走 partial index → 277ms
- GORM `.Table("users")` 跳過 soft delete scope → 沒 `deleted_at IS NULL` → planner 不用 partial → Seq Scan → 12s

**怎麼驗證?**

```sql
SELECT * FROM pg_stat_user_indexes WHERE indexrelname = 'idx_orders_active';
-- idx_scan 數字應該增加;否則 index 沒被用
```

**Partial vs Full size 差別(原理性說明)**
- 主表 N row 中只有比例 P 是 active(`deleted_at IS NULL`)
- Full B-tree index 包含全部 N entry → size 跟 N 成正比
- Partial 只包含 N × P entry → size 跟 active row 數成正比
- active 比例越小(P → 0),partial 的相對優勢越大;B-tree depth 也較淺,query latency 略勝
- **具體數字隨 schema 與 row 分佈大幅變化** — 我 PR #5694 沒留下 partial-only 跟 full-only 的 index size 對比;主要贏在「planner 不走 partial → 整段 Seq Scan」這個本質問題,不是 index size 本身

**Reference**: [Use The Index Luke - Partial Indexes](https://use-the-index-luke.com/sql/where-clause/null/partial-index);[PG docs Partial Indexes](https://www.postgresql.org/docs/current/indexes-partial.html)

### 🔽 2. Advisory Lock — leader election 怎麼選 lock id?

**為什麼用 advisory lock 不用 row lock?**
- Row lock 要綁定 row(`SELECT ... FOR UPDATE`)→ 但 OutboxRelay 不是針對特定 row,是整個 process scope
- Advisory lock 是 application-defined,只要全 cluster agree 用同一個 id,就達成「全 cluster 只有一個 process 拿到」

**`pg_try_advisory_lock(1001)` 行為**
- 拿到 → 回 true,該 session 持有,**session 結束自動釋放**(disconnect or session close)
- 拿不到 → 回 false(不 block),適合 leader election
- 對比 `pg_advisory_lock(1001)` → 拿不到會 block,適合單例 critical section

**`1001` 怎麼選?**
- 必須跨 cluster 唯一,且 stable(不能每次 rebuild change)
- booking_monitor 用 `1001` — 純粹 arbitrary number,但要 document
- 進階做法:`pg_try_advisory_lock(hashtext('outbox_relay_leader'))` — 用 hashtext 把 string 轉 int,更語意化

**Leader 死了怎麼辦?**
- TCP keepalive timeout(預設 PG `tcp_keepalives_idle = 7200s`)後 session 才會被清掉 → lock 才釋放
- 解法 1:把 `tcp_keepalives_idle` 設短(60s)
- 解法 2:OutboxRelay 自己加 health check,卡死自己 panic exit → session 立刻關 → lock 立刻釋放
- 我 booking_monitor 走解法 2(讓 process die 不要苟延殘喘)

**Reference**: [PG docs Advisory Locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS);booking_monitor `internal/application/outbox/relay.go`

### 🔽 3. MVCC + Isolation Level — 4 個 anomaly 跟對應 level

**3 個經典 anomaly + 1 個 SI specific**

| Anomaly | Read Uncommitted | Read Committed | Repeatable Read | Serializable |
| --- | --- | --- | --- | --- |
| Dirty Read(讀到沒 commit 的) | ❌ | ✅ | ✅ | ✅ |
| Non-repeatable Read(同 tx 讀兩次值不一樣) | ❌ | ❌ | ✅ | ✅ |
| Phantom Read(同 tx 讀 range 兩次,row 數不一樣) | ❌ | ❌ | ✅(PG 用 SI 順便擋) | ✅ |
| Serialization Anomaly(write skew) | ❌ | ❌ | ❌ | ✅ |

**PG 預設是 Read Committed**
- 標準說 RC 允許 non-repeatable read,但對 financial / counter 很危險
- 例:`SELECT balance` → 確認夠 → `UPDATE balance = balance - 100` 中間其他 tx 改了 balance,你的判斷已過時

**Repeatable Read(PG 用 Snapshot Isolation 實作)**
- 一個 tx 看到的是 tx start 時的 snapshot
- PG 的 SI 順便擋住了 phantom read(標準 RR 沒擋)
- 缺點:`UPDATE` 撞到 stale row → `ERROR: could not serialize access due to concurrent update` → 你要 retry

**Serializable(PG 用 SSI - Serializable Snapshot Isolation)**
- 完整擋四個 anomaly,效能比傳統 2PL 好
- 缺點:撞 serialization failure 機率更高,production 要寫 retry loop

**什麼時候用什麼?**
- 預設 RC + UPDATE 用 `WHERE version = $1` 樂觀鎖 → 多數場景 OK
- counter / balance 類 → 改 RR,撞 serialize fail retry
- audit / compliance 嚴格 → Serializable,接受 retry cost

**我用過嗎?** booking_monitor `event_ticket_types` 庫存扣減,我選 RC + version-based optimistic locking(WHERE clause 帶 version),沒上 RR。理由:Lua atomic in Redis 已經先擋掉,PG 是 source of truth 但不是高度競爭點。

**Reference**: [PG MVCC docs](https://www.postgresql.org/docs/current/mvcc.html);[Use The Index Luke - Isolation](https://www.postgresql.org/docs/current/transaction-iso.html)

### 🔽 4. EXPLAIN ANALYZE — 看什麼?

**範本(我 PR #5694 抓到的)**

```sql
EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) SELECT ... FROM ...;
```

**3 個關鍵欄位**
1. **Plan node type** — Seq Scan / Index Scan / Bitmap Heap Scan / Hash Join / Nested Loop
2. **cost=A..B rows=N** — A 啟動成本,B 總成本(unitless,跟 IO + CPU 比),N 是 planner 估的 row 數
3. **actual time=A..B rows=N loops=K** — 實際時間 ms,實際 row 數,loop 次數(nested loop 內層執行幾次)

**Estimate vs Actual 差距**
- 差 10× 以上 → planner 估錯,可能 stats stale(`ANALYZE table_name` 跑一次)
- 差 100× 以上 → 必有問題,通常是 join 順序錯或 missing index

**Buffers 訊息**
- `Buffers: shared hit=X read=Y` — hit 是 cache(快),read 是磁碟(慢)
- 如果 read 數量 = total 數量 → 完全 cold cache,跑第二次看穩態
- 如果 read 數量 > 0 — 該 index 或表 working set 大於 shared buffers

**Plan Node 解讀**
- **Seq Scan** — 全表掃,小表正常,大表通常要 index
- **Index Scan** — B-tree 走 index 找到 ctid 再回表,適合少量 row
- **Index Only Scan** — 走 covering index,**完全不回表**,適合 SELECT 欄位都在 index 裡的場景
- **Bitmap Heap Scan** — 多個 index 結合,先 bitmap union 再回表
- **Nested Loop** — 外層每 row 都查內層,適合外層 row 數小
- **Hash Join** — 內層先 build hash,外層 probe,適合中型 + 沒 index 的 join
- **Merge Join** — 兩邊都已 sort 過,適合大表 + 有 ordered index

**Reference**: [PG EXPLAIN docs](https://www.postgresql.org/docs/current/sql-explain.html);[Depesz EXPLAIN visualizer](https://explain.depesz.com/)

### 🔽 5. CTE — 何時用 WITH,何時不用?

**CTE 兩種形式**

```sql
-- Non-recursive
WITH active_users AS (
    SELECT * FROM users WHERE deleted_at IS NULL
)
SELECT * FROM orders o JOIN active_users u ON ...;

-- Recursive(我 commeet RBAC 跑 access control tree 用過)
WITH RECURSIVE org_tree AS (
    SELECT id, parent_id, name FROM departments WHERE id = $1
    UNION ALL
    SELECT d.id, d.parent_id, d.name
      FROM departments d JOIN org_tree t ON d.parent_id = t.id
)
SELECT * FROM org_tree;
```

**WITH 的 `MATERIALIZED` vs `NOT MATERIALIZED`(PG 12+)**
- PG 11 之前 — WITH 一律 materialize(像 temp table),會 break planner inline 機會
- PG 12+ — 預設 `NOT MATERIALIZED`,planner 可以 inline CTE 進主 query(像 subquery 一樣);你想保留 materialize 行為要明寫
- 範例:`WITH foo AS MATERIALIZED (SELECT ...)` 強制 materialize → 用 case:同 CTE 在外層用兩次,你不想算兩次

**RECURSIVE 應用**
- 樹狀資料(org chart、access control tree、留言 thread)
- 用 `UNION ALL` 不要用 `UNION`(後者去重貴 + recursive 本身不會產 dup)
- 一定要有 termination(主表 WHERE 子句),不然無限 loop

**Reference**: [PG CTE docs](https://www.postgresql.org/docs/current/queries-with.html);Markus Winand 的 [Use The Index Luke - Recursive](https://use-the-index-luke.com/)

### 🔽 6. Batch UPDATE — VALUES clause 還是 CASE WHEN?

**Bad — N 次 UPDATE**

```go
for _, item := range items {
    db.Exec("UPDATE items SET qty = ? WHERE id = ?", item.Qty, item.ID)
}
// N round-trip → N × (network + parse + plan + execute)
```

**Better — VALUES clause(我 commeet PR #5557 用法)**

```sql
UPDATE items AS i
   SET qty = u.qty,
       status = u.status
  FROM (VALUES
        ($1::uuid, $2::int, $3::text),
        ($4::uuid, $5::int, $6::text),
        ...
       ) AS u(id, qty, status)
 WHERE i.id = u.id;
```

- 一次 round-trip,N 個 row 一起更新
- planner 對 VALUES 做 hash join,O(N + M)

**Alt — CASE WHEN(legacy 寫法)**

```sql
UPDATE items SET qty = CASE id
    WHEN $1 THEN $2
    WHEN $3 THEN $4
    ...
END WHERE id IN ($1, $3, ...);
```

- 缺點:每個欄位都要寫一份 CASE,容易出錯
- 適合場景:極小批次(< 5 row)且只更新一個欄位

**Pitfall**
- 用 VALUES clause 千萬要寫 type cast(`$1::uuid`、`$2::int`)— PG 預設假設 unknown type,join 會失敗
- 一次 batch 別超 ~500 row,planner 對巨型 VALUES 會慢

**Reference**: [PG UPDATE FROM docs](https://www.postgresql.org/docs/current/sql-update.html);commeet-text2sql 的 batch repository pattern

### Q&A(必背)

### 🔽 Q: Index 為什麼用 B-tree?Hash index 不行嗎?

「B-tree 三個 winning property:**有序**(支援 range query 跟 ORDER BY)、**平衡**(高度 log N)、**多欄位 composite**。Hash index 只支援等值查找,不能 range,也不能 ORDER BY,所以 99% 的場景 B-tree 贏。

PG 有 Hash index 但只有 1) 大表 2) 純等值 lookup 3) value 是 long string 這種 case 才贏 — 因為 hash 後值都一樣大,B-tree 對長 string 會比較浪費 page。

實作面 — B-tree 的 leaf 是 doubly linked list,所以 range scan(`WHERE x BETWEEN a AND b`)是順著 leaf chain 走,不用反覆 backtrack。Hash index 沒這個結構。

特殊情境 PG 還有 GiST / GIN / BRIN — JSONB / full-text / geo 用 GIN 或 GiST,time-series 大表用 BRIN(block range)更省。」

📚 [PG Index Types](https://www.postgresql.org/docs/current/indexes-types.html);Markus Winand 的 [B-tree explained](https://use-the-index-luke.com/sql/anatomy/the-tree)

### 🔽 Q: Composite index 順序怎麼決定?

「兩個原則:**最左前綴**(leftmost prefix)+ **selectivity 高的先**(在前綴可選擇下)。

最左前綴 — composite index `(a, b, c)` 能服務 `WHERE a=?`、`WHERE a=? AND b=?`、`WHERE a=? AND b=? AND c=?` 三種 query;但 `WHERE b=?`(skip a)不能用這個 index,要另建 `(b)`。

Selectivity 高的先 — 例:`(status, user_id)` 跟 `(user_id, status)`,如果 user_id selectivity 90%,status 只有 5 個值,**user_id 先**,因為前綴 prune 更快。但若 query 永遠帶 status,且 status=‘active’ 是常用條件,partial index `WHERE status='active'` 反而更省。

特殊情境 — ORDER BY 要走 index 就要看順序:`ORDER BY a, b DESC` 要 index `(a, b DESC)`,否則 planner 會走 sort。」

📚 [Use The Index Luke - Composite](https://use-the-index-luke.com/sql/where-clause/the-equals-operator/concatenated-keys)

### 🔽 Q: 為什麼 OFFSET 大會慢?怎麼解?

「`OFFSET 100000 LIMIT 20` PG 需要實際掃 100020 row 然後丟掉前 100000,IO + CPU 全燒。Keyset pagination(seek method)解這個。

**Keyset pagination**:

```sql
SELECT * FROM orders
 WHERE (created_at, id) < ($lastCreatedAt, $lastId)
 ORDER BY created_at DESC, id DESC
 LIMIT 20;
```

- 用 `(created_at, id)` 當 cursor,每頁帶上一頁最後一筆的 cursor
- index `(created_at DESC, id DESC)` 直接 seek,不用 scan
- 唯一缺點:不能跳頁(沒有「第 5 頁」這種概念,只能往後翻)

我 commeet 主系統某些列表 API 就是這樣改的,from `LIMIT/OFFSET` to seek-based。Cloud DB 跟 ORM(GORM)現在普遍支援。」

📚 [Use The Index Luke - Pagination](https://use-the-index-luke.com/no-offset);[Markus Winand SQL Pagination talk](https://use-the-index-luke.com/sql/partial-results/fetch-next-page)

### 🔽 Q: PG 跟 MySQL 你會怎麼選?

「實用主義回答:

**PG 贏的場景**:複雜 query(CTE / window function / partial index)、JSONB 存半結構化、PostGIS 做 GIS、進階 isolation(SSI)。我 booking_monitor + Commeet 都選 PG 因為這些 feature。

**MySQL 贏的場景**:read-heavy + simple schema + 高 connection 數(InnoDB 的 row-level lock 在純 read 場景 overhead 低)、跨大型 cluster horizontal sharding(MySQL 生態的 Vitess 等)、組織既有 DBA 熟。

**個人偏好** — 我認為 PG 在 application complexity 高的 BE service 是預設選擇;MySQL 在「就是要 CRUD + 大量 read,組織已經有 MySQL 運維能力」場景仍然強。實作面我 PG 經驗多很多,MySQL 沒 production 用過。」

📚 [PostgreSQL vs MySQL - high-level comparison](https://www.postgresql.org/about/featurematrix/)

### 🔽 Q: VACUUM 跟 autovacuum 你會 tune 嗎?

「實話 — autovacuum 預設 95% 場景夠用,我 production 沒手動 tune 過。但我能講原理:

**MVCC 副作用** — PG 不是真刪 row,UPDATE 是「insert 新 row + 舊 row 標 dead」,DELETE 是標 dead。dead row 累積叫 **bloat**,讓表變大、index 變鬆。

**VACUUM** = 回收 dead tuple → 表變緊湊但不 release disk(回到 free space map)
**VACUUM FULL** = 重建表 → release disk 但 ACCESS EXCLUSIVE lock,production 不能跑(會卡所有 query)
**Autovacuum** = 跑 VACUUM 不跑 FULL,thresholds 預設「dead/live > 20% + 50 row」會觸發

**什麼時候要手 tune?** 大表(> 100M row)更新頻繁時,預設 threshold 太晚跑 → 改 `autovacuum_vacuum_scale_factor = 0.05`(5% bloat 就跑)。極大表 partition 才是真解,VACUUM 只是延緩。

我安全的回答 — 「我了解原理,production 沒手動調過 autovacuum threshold,如果遇到 bloat 我會先看 `pg_stat_user_tables.n_dead_tup` 確認,再決定 partition 還是 tune」」

📚 [PG VACUUM](https://www.postgresql.org/docs/current/routine-vacuuming.html);[Citus autovacuum tuning guide](https://www.citusdata.com/blog/2022/07/28/debugging-postgres-autovacuum-problems-13-tips/)

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我會 PG 內部 storage engine」** — 真話是「我了解 MVCC 高層、知道 heap + B-tree 結構、知道 page = 8KB」,**沒讀過 src/backend/access**
- ❌ **「我會 tune autovacuum」** — 真話是「我知道原理,production 沒手動 tune 過」
- ❌ **「我會 replication topology 設計」** — 沒做過 streaming replication / logical replication 的 production set up
- ✅ 安全 phrasing:**「I have hands-on PG experience including partial indexes, advisory locks, EXPLAIN ANALYZE tuning, MVCC isolation tradeoffs. I haven’t operated replication topologies or tuned autovacuum at scale.」**

### 📚 References — PostgreSQL must-read

- [Use The Index Luke](https://use-the-index-luke.com/) — Markus Winand 寫,SQL indexing 最好的教材,**面試前必讀**
- [PostgreSQL Docs - Indexes](https://www.postgresql.org/docs/current/indexes.html) — 官方 index 章節
- [PG MVCC docs](https://www.postgresql.org/docs/current/mvcc.html) — Isolation level 詳細表
- [Depesz EXPLAIN visualizer](https://explain.depesz.com/) — 貼 EXPLAIN ANALYZE 看 plan tree
- [Citus blog](https://www.citusdata.com/blog/) — 真實 production scaling 案例
- [PG Wiki - Don’t Do This](https://wiki.postgresql.org/wiki/Don%27t_Do_This) — 常見錯誤清單

---

### Redis ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「Redis 是 in-memory key-value store,核心特性是**單執行緒 atomic** — 同一 Redis instance 任何 command 都 serialize 跑,不會 race。我用 2 個 production 場景:**booking_monitor hot path** — Lua script(`deduct.lua`)在 Redis 內 atomic 扣庫存 + 推 stream message,benchmark 量到 ~8,330 booking/s 的物理上限,瓶頸是 Lua 跑在單核心;**Redis Streams** — consumer group + PEL recovery,worker crash 後重啟用 `XAUTOCLAIM` 重認領 pending message,實現 exactly-once delivery semantics(配合 idempotency 層)。」
> 

### 📊 booking_monitor 中 Redis 角色圖

```mermaid
graph LR
    API[API handler] -->|EVALSHA deduct.lua| LUA[Redis Lua sandbox<br/>single-thread atomic]
    LUA -->|HINCRBY -qty| INV[Inventory hash]
    LUA -->|XADD| STR[Redis Stream]
    STR -->|XREADGROUP| W[Worker]
    W -->|XACK after DB commit| STR
    W -.crash.-> XA[XAUTOCLAIM idle > 30s]
    XA --> W2[Replacement worker]
    style LUA fill:#fee
    style STR fill:#efe
```

### 🔄 必背深度展開

### 🔽 1. 單執行緒 atomic — 為什麼這是 feature 不是 limitation?

**事實**:Redis 6.0 之前 100% 單執行緒處理 command,6.0 之後 IO threading 可選但 command execution 仍單執行緒。

**Why design like this?**
- 簡化 implementation — 任何 command 跨 data structure 都 atomic,不用內部 lock
- L1 cache friendly — single core 跑,沒 cache invalidation cost
- Predictable latency — 沒有 lock contention 不確定性

**Trade-off**
- ✅ Throughput per instance ~100k QPS(simple SET/GET),~10-50k for complex ops
- ❌ 單 instance 跑不到 1M QPS → 解法:Cluster sharding(多 master)+ replica read

**對我的 booking_monitor 意義**
- Lua script 內任何 HINCRBY / XADD 都 atomic 跑完 → 不需要 distributed lock 防止 over-sell
- 但是 8,330/s 物理上限就是這個 single-thread 的副作用 — Lua 跑完才會處理下一個 client request,Lua 越複雜越慢
- 解法:把 Lua 拆得越短越好;或上 Cluster(但 booking_monitor 簡化沒做)

**Reference**: [Redis FAQ - single-threaded](https://redis.io/docs/latest/);[Redis 6 IO Threading announcement](https://antirez.com/news/126)

### 🔽 2. Lua Script — 為什麼 atomic?有什麼限制?

**真實 booking_monitor `deduct.lua` 結構**(節錄關鍵幾行,完整版見 `internal/infrastructure/cache/lua/deduct.lua`)

```lua
-- KEYS[1]: ticket_type_qty:{id}     (inventory counter — plain integer, not hash)
-- KEYS[2]: ticket_type_meta:{id}    (event_id / price_cents / currency)
-- ARGV: count, user_id, order_id, reserved_until_unix, ticket_type_id

local new_val = tonumber(redis.call("DECRBY", KEYS[1], ARGV[1]))
if new_val < 0 then
    redis.call("INCRBY", KEYS[1], ARGV[1])    -- rollback
    return {"sold_out"}
end

local meta = redis.call("HMGET", KEYS[2], "event_id", "price_cents", "currency")
-- ... (price * quantity 用 decimal-string multiply 避開 Lua double 精度)

redis.call("XADD", "orders:stream", "*",
    "order_id", ARGV[3], "user_id", ARGV[2], "event_id", event_id,
    "quantity", ARGV[1], "reserved_until", ARGV[4],
    "ticket_type_id", ARGV[5], "amount_cents", amount_cents, "currency", currency)
return {"ok", event_id, amount_cents, currency}
```

**Atomicity guarantee**
- 整段 Lua 在 Redis 內**單執行緒 sequential 跑完**,中途沒人能 interleave
- 沒有 race condition;沒有 distributed lock 需求

**限制**
- ❌ 不能用 random()(non-deterministic,replication 會分裂)→ Redis 強制 disable
- ❌ 不能 sleep / block(整個 Redis 會卡)
- ❌ 不能讀寫沒在 KEYS[] 裡的 key(Cluster mode 強制,single mode 可以但 best practice)
- Lua script 預設 timeout 5s,超過會 abort

**EVAL vs EVALSHA**
- `EVAL` — 每次都送整段 script + cache
- `EVALSHA` — 送 script 的 SHA1 hash + ARGV,Redis cache 命中時不傳 script body → 省 bandwidth
- 標準做法:啟動時 `SCRIPT LOAD` 拿到 SHA1,後續用 EVALSHA;失敗時 fall back EVAL

**為什麼 8,330/s 是物理上限?**
- per booking_monitor blog post [`2026-05-lua-single-thread-ceiling.zh-TW.md`](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-lua-single-thread-ceiling.zh-TW.md):一筆 deduct.lua(DECRBY + 條件判斷 + HMGET + XADD)跑 ~100µs
- 1s ÷ 100µs ≈ 10,000 理論上限,client/network overhead 拉低到實測 ~8,330/s
- 這是「同一 `(event_id, section_id)` 對應的 inventory key 上跑 Lua」的物理上限 — 不同 ticket_type / section 各自有自己的 key,不互相 block
- 要破:shard `(event_id, section_id)` inventory key、上 Cluster 把不同 key 分到不同 master、script 變短(目前已經幾乎最短)

⚠️ **瓶頸澄清(5-stage benchmark 後修正)**:
- **Redis 容量不是系統瓶頸** — 5-stage benchmark 顯示 Redis 本身可達 18k–33k http_reqs/s
- 8,330/s 是「單一 inventory key 的 Lua 原子序列化」的 architectural constraint,不是 Redis 硬體限制
- Redis 容量遠超此值;真正的問題是 **單 key 序列化**,解法是 key sharding
- 一旦突破 key sharding,下一個瓶頸才是 **PG worker INSERT 吞吐量**(~10-20k 單行/s,用 batch INSERT...UNNEST 可再拉 3-5×)
- Stage 5 的瓶頸轉移到 **Kafka acks=all publish latency**,ceiling 降到 5,139/s(−39% vs Stage 4)

**Reference**: [Redis Lua scripting docs](https://redis.io/docs/latest/develop/programmability/eval-intro/);booking_monitor `internal/infrastructure/cache/deduct.lua`

### 🔽 3. Redis Streams — Consumer Group + PEL Recovery

**Streams 跟 List 差別**
- List 用 `LPUSH/RPOP` 做 queue,但消費者掛了訊息會永久消失
- Streams 是 append-only log + consumer group,訊息有 ID,消費後要 explicit `XACK`

**Consumer Group 機制**
- `XGROUP CREATE my-stream worker-group $` — 創建 consumer group,從 latest 開始(`$`)
- `XREADGROUP GROUP worker-group worker-1 COUNT 10 BLOCK 2000 STREAMS my-stream >` — worker-1 拿 10 個未派發訊息,最多 block 2s
- 每筆訊息被派給一個 worker(group 內 round-robin / hashing 不保證)
- 該訊息進入 **PEL (Pending Entries List)** until `XACK`

**PEL Recovery — worker crash 怎麼救?**
- worker-1 拿了訊息 → 處理一半 crash → 訊息還在 PEL,沒人 ACK
- 重啟的 worker-1 用 `XREADGROUP ... STREAMS my-stream 0` — `0` 替代 `>`,讀 PEL 內自己的訊息
- 不是 worker-1 而是新加入的 worker-2 — 用 `XAUTOCLAIM my-stream worker-group worker-2 30000 0-0 COUNT 100` 認領 idle > 30s 的訊息
- 我 booking_monitor 兩種 path 都實作(start-time 自掃 + idle-claim)

**Idempotency 配合**
- Streams `at-least-once` — message 可能 redelivered(crash mid-process,XAUTOCLAIM 後又收到一次)
- Worker 必須 idempotent:用 DB UNIQUE constraint 或 SETNX flag 擋重複
- booking_monitor 用 `orders.id` UUID v7 + `ON CONFLICT DO NOTHING` 加 DB UNIQUE 來做這件事

**MAXLEN 跟 retention**
- `XADD ... MAXLEN ~ 10000` — 留最近 ~10000 筆(`~` 是 approximate,效率高)
- `XADD ... MINID ~ <ts>` — 用 timestamp 做 retention(我 booking_monitor 用這個)
- 沒設會無限長,記憶體用爆

**Reference**: [Redis Streams Tutorial](https://redis.io/docs/latest/develop/data-types/streams/);booking_monitor `internal/infrastructure/cache/redis_queue.go`

### 🔽 4. Distributed Lock — Martin Kleppmann 為什麼批評 Redlock?

**Single-Redis lock** — `SET key value NX EX 30`(SETNX + TTL),簡單但 Redis 掛了 lock 就丟。

**Redlock 演算法**(Salvatore Sanfilippo 提出)
1. Client 拿系統時間 T1
2. 對 5 個獨立 Redis instance 做 `SET ... NX PX <ttl>`
3. 拿到 ≥ 3 個 → 取 T2,計算花費 = T2 - T1
4. 拿到的 lock 真實有效時間 = ttl - 花費
5. 不足 majority → 釋放已拿到的 lock 重試

**Kleppmann 批評**(2016)
- **時鐘 skew 問題**:Redlock 假設多個 instance 的 clock 沒大差距,但 NTP sync 失誤 / VM live migration 後 clock 跳變,會打破 mutual exclusion
- **GC pause 問題**:client 取到 lock 後 process GC 暫停超過 TTL,lock 過期被另一個 client 拿走,但原 client GC 結束後仍以為自己持有 lock → 兩個 client 同時都認為自己有 lock
- 結論:**Redlock 不適合 correctness-critical 場景**(financial、unique resource);適合 efficiency(避免重工)場景

**Sanfilippo 反駁**
- 同樣的 clock skew / GC 問題在 ZooKeeper / etcd 也存在
- 設計上要假設 lock 不是 hard guarantee,業務層要再加 fencing token

**Fencing token 是什麼?**
- Lock service 給每個 lock 一個 monotonically increasing token
- 持有者帶 token 去操作 resource
- Resource 拒絕 token < seen 的 request → 即使 GC pause 後拿著 stale lock,token 早就 outdated

**我 booking_monitor 怎麼處理?**
- 我**沒用 distributed lock** — Redis Lua atomic 已經足夠(同 instance、單執行緒、check-then-decrement 在 Lua 內 atomic)
- Outbox relay 用 **PG advisory lock**(不是 Redis),因為 PG 是 source of truth、有完整 transaction semantics、session 死了 lock 自動釋放

**Reference**: [Kleppmann’s critique](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html);[Sanfilippo’s response](http://antirez.com/news/101)

### 🔽 5. Cache stampede / penetration / avalanche — 3 個典型問題

**1. Cache Penetration(穿透)**
- 攻擊者大量 query 不存在的 key → cache miss → DB hit → DB 燒
- 解法 1:negative caching(把「不存在」也 cache,TTL 短)
- 解法 2:Bloom filter 先擋一層(`bloom_filter.contains(key)` false → 必定不存在)
- booking_monitor 沒踩到(用 UUID id,猜不到)

**2. Cache Avalanche(雪崩)**
- 大量 cache key 同時過期 → 同時 miss → 同時打 DB
- 解法:TTL 加隨機 jitter(`base + rand(0, jitter)`),分散過期時間
- booking_monitor `IDEMPOTENCY_TTL=24h` 加 jitter

**3. Cache Stampede / Thundering Herd(擊穿)**
- 一個 hot key 過期,瞬間多個 request 都 miss + 都 fetch DB + 都寫回 cache
- 解法 1:singleflight pattern(`x/sync/singleflight` Go package)— 同一個 key 同時 fetch 只跑一次
- 解法 2:lock-on-miss(SETNX flag,持鎖者 fetch,其他 wait)
- 解法 3:proactive refresh(快過期時 background 刷)

**我 commeet 怎麼處理?**
- LLM API 有 retry + backoff,**沒上 result cache** — text2sql 的 query 多樣性高(自然語言輸入),cache hit rate 不會理想,沒投資 cache layer
- 主系統有沒有用 `singleflight` 我沒在自己 PR 改過,不在我直接負責範圍 → 面試我會老實說「主系統某些路徑可能有,但細節我沒做過」

**Reference**: [Wikipedia Cache stampede](https://en.wikipedia.org/wiki/Cache_stampede);Go `x/sync/singleflight` docs

### Q&A(必背)

### 🔽 Q: Redis 跟 Memcached 怎麼選?

「Redis 贏在 data structure 豐富(List / Hash / Set / Sorted Set / Stream / Bitmap),支援 Lua scripting、persistence(RDB / AOF)、replication、cluster。Memcached 只有 string,但 multi-threaded 在純 GET/SET 場景 latency 更低,簡單 cache layer 仍有競爭力。

實務:90% 場景 Redis 贏因為 data structure 多 + persistence 選項,Memcached 只在「我只要 byte cache、要極高 throughput、用不到任何 advanced feature」時考慮。我 booking_monitor + Commeet 全 Redis。」

📚 [Redis vs Memcached - AWS](https://aws.amazon.com/elasticache/redis-vs-memcached/)

### 🔽 Q: RDB vs AOF persistence 怎麼選?

「**RDB**:point-in-time snapshot,每 N 秒 fork child process dump 到 disk。優點:檔案小、recovery 快;缺點:兩次 snapshot 之間的寫入會丟。

**AOF (Append-Only File)**:把每個 write command append 到 log,recovery 時 replay。`fsync` 策略決定耐久度:`always`(每筆 flush,慢但 zero loss)/ `everysec`(每秒 flush,可能丟 1s)/ `no`(OS 自己刷,可能丟到分鐘)。

**Hybrid 是預設**:Redis 4.0+ 開了 `aof-use-rdb-preamble` — AOF file 開頭塞 RDB snapshot,後面 append commands,**recovery 快 + 耐久度高**,生產建議組合。

我 booking_monitor — 因為 Redis 是 cache 不是 SoT,我設 RDB only(`save 60 1000`),掛了重啟少量 inventory 從 PG rebuild。如果 Redis 是 SoT 場景就一定要 AOF + replication。」

📚 [Redis Persistence docs](https://redis.io/docs/management/persistence/)

### 🔽 Q: Redis Cluster 跟 Sentinel 差在哪?

「**Sentinel**:HA 方案,monitor + auto failover。架構是 1 master + N replica + 多 Sentinel 投票,master 掛了 Sentinel promotion 一個 replica。**沒 sharding,單 master 就是 throughput 上限**。

**Cluster**:HA + sharding。把 keyspace 切 16384 個 slot 分到多個 master(每個 master 配 replica),client 連任一節點都能 redirect 到正確 master。**throughput 隨 master 數量水平擴展**,但複雜度高(multi-key transaction 受限、Lua 內 key 必須同 slot)。

**選擇邏輯**:單 master throughput 夠 → Sentinel(簡單);要 horizontal scale + 接受複雜度 → Cluster。

我 booking_monitor 用 single-instance + 沒 HA(demo project 不需要),production 場景我會選 Sentinel 因為單 master throughput ~80k QPS 足夠,Cluster 對 Lua script 限制太多(`KEYS[]` 必須同 slot,booking flow 自然要這樣設計)。」

📚 [Redis Cluster spec](https://redis.io/docs/management/scaling/);[Sentinel docs](https://redis.io/docs/management/sentinel/)

### 🔽 Q: 你的 booking_monitor 為什麼用 Redis Streams 不用 Kafka?

「兩個原因:**簡化** + **co-location**。

**簡化** — booking_monitor 是 portfolio demo,引一個 Kafka 等於再加 Zookeeper / KRaft / 3-broker cluster,docker-compose 重得多。Streams 用既有的 Redis 就可以;消費者 group + PEL 也夠用。

**Co-location** — booking_monitor 的 hot path 是 Lua 在 Redis 內 atomic deduct 庫存 + 推 stream message,這兩個動作必須 atomic(否則扣了庫存 message 推丟 = 庫存假性消失)。Streams 跟 inventory 在同一個 Redis 內,Lua 直接 atomic 處理。如果用 Kafka,我要做 outbox pattern 把 Redis-side 寫入跟 Kafka-side 推 message 解 coupling,代價更高。

實際 production 場景(大流量 + 多 service consume 同 stream + 長 retention)我會選 Kafka;booking_monitor 規模選 Streams 是 trade-off — 我自己面試會主動講這個 trade-off 不會假裝是『Streams 比 Kafka 好』。」

📚 booking_monitor 的 `internal/infrastructure/cache/redis_queue.go` + `deduct.lua`

### 🔽 Q: 8,330/s 那個物理上限,怎麼破?

「四條路:

1. **Lua 變短** — 從 6 個 op 縮到 3 個(把 audit logging 移出 Lua,改 stream consumer 寫)
2. **Shard inventory key** — `inventory:{event_id}:{shard_id}` 分 N 個 shard,client 隨機選一個扣,sum 起來才是總庫存。代價:庫存「破碎」可能某個 shard 早於其他 sold out
3. **Redis Cluster** — 多 master 各自跑 Lua,但要保證 `KEYS[]` 都同 slot(用 `{tag}` 強制 hash 到同 slot)
4. **預先 reserve** — 在 Redis 切 token bucket,每 token 代表 1 張票,庫存發空了 deny,扣的是 token 而不是 Lua check 然後 decrement

我 booking_monitor v1.0.0 沒做這些 — 我選擇 ship 一個有完整 saga + outbox + observability 的 baseline,scaling 改造留給後續 PR。面試講這題時我會強調『8,330/s 是我量出來的物理上限,我知道四條破解路,但 portfolio scope 上我選了更該證明的 — completion + 工程紀律』」

📚 booking_monitor `docs/benchmarks/` 5-stage architectural comparison

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我會 Redis Cluster 運維」** — 沒實際跑過 Cluster,只看過 docs
- ❌ **「我會調 Redis kernel parameter」** — 沒手調過 `vm.overcommit_memory`、`transparent_hugepage`,只看過建議
- ❌ **「我會深入 Redis source code」** — 看過 hash table 跟 stream 的 high-level 結構,沒貢獻過 patch
- ✅ 安全 phrasing:**「I have production-grade Redis usage with Lua scripting + Streams + PEL recovery in booking_monitor; I have a solid mental model of single-threaded execution + Redlock critique + cache stampede patterns. I haven’t run Cluster in production or tuned kernel parameters at depth.」**

### 📚 References — Redis must-read

- [Redis Docs - main hub](https://redis.io/docs/) — Stream / Lua / Cluster 章節
- [Kleppmann’s Redlock critique](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html) — 必讀,顯示你了解分散式 lock 的真實限制
- [Antirez (Sanfilippo) blog](http://antirez.com/) — 創始者本人的設計理由與權衡
- [xiaolincoding Redis 篇](https://xiaolincoding.com/redis/) — 中文最系統的 Redis 教材
- [Redis Streams Tutorial](https://redis.io/docs/latest/develop/data-types/streams/) — Consumer group + PEL 最權威
- [Redis Persistence Demystified](http://antirez.com/post/redis-persistence-demystified.html) — RDB vs AOF trade-off

---

### Kafka ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「Kafka 是 distributed log-based message broker,核心是 **partition + offset + consumer group** 三件事。Partition 是 ordered append-only log,同 partition 內 message 嚴格有序;consumer group 內每個 partition 只給一個 consumer 拿,所以 group 內可以 parallel 但不會 dup。**At-least-once** 是預設語意,真正的 exactly-once 要靠 idempotent producer + consumer side idempotency 配合。我用在 booking_monitor outbox relay → Kafka → saga compensator,topic `order.failed` + DLQ `order.failed.dlq`,group `booking-saga-group`。」
> 

### 📊 booking_monitor 中 Kafka 角色圖

```mermaid
graph LR
    DB[(PG)] -->|outbox row| RELAY[Outbox Relay<br/>pg_advisory_lock leader]
    RELAY -->|publish| K[Kafka topic<br/>order.failed]
    K -->|partition consumer| SAGA[Saga Compensator<br/>booking-saga-group]
    SAGA -->|compensate| REDIS[Redis revert.lua]
    SAGA -->|max_retries exceeded| DLQ[order.failed.dlq]
    DLQ -->|manual review| OPS[Operator]
    style K fill:#fef
    style DLQ fill:#fee
```

### 🔄 必背深度展開

### 🔽 1. Partition + Offset + Consumer Group — Kafka 三件事

**Partition** = ordered append-only log
- 每個 topic 有 N 個 partition,訊息進來會 hash key 決定落哪個 partition
- 同 partition 內 strictly ordered(offset 單調遞增)
- **跨 partition 無 ordering guarantee** — 這是 Kafka throughput 的關鍵

**Offset** = partition 內 message 的 monotonic index
- consumer 自己記住「我讀到 offset N」(commit 到 `__consumer_offsets` topic)
- replay 就是把 offset 倒回去再讀

**Consumer Group**
- group 內 N 個 consumer,partition 平均分配給 consumer(N consumer ≤ M partition 才有意義)
- 同 partition 只給 group 內一個 consumer 拿 → group 內不 dup
- 跨 group 互相獨立 → 同 topic 多 group 都能收(用於 fan-out)

**Rebalance 何時觸發?**
- consumer 加入 / 離開 / heartbeat 超時
- partition 數量改變(rare)
- rebalance 期間整個 group **暫停消費**(stop-the-world),production 痛點

**怎麼減少 rebalance?**
- 用 sticky assignor(`PartitionAssignor=sticky`)→ rebalance 後盡量沿用原本分配
- Cooperative rebalance(Kafka 2.4+)→ incremental rebalance,只動受影響的 partition
- Heartbeat 拉短到 < session timeout / 3,讓死掉的 consumer 快被踢

**Reference**: [Kafka docs - Consumer Group](https://kafka.apache.org/documentation/#design_consumerposition);[Confluent Cooperative Rebalance](https://www.confluent.io/blog/cooperative-rebalancing-in-kafka-streams-consumer-ksqldb/)

### 🔽 2. At-least-once vs Exactly-once Semantics

**At-least-once(預設)**
- producer 寫成功但 ack 丟 → retry → 同 message 寫兩次
- consumer commit offset 前 crash → 重啟後從上次 commit 再讀 → 重複處理
- 結論:不 dedup 一定會看到 dup

**Exactly-once(EOS)**
Kafka 2.x+ 透過兩個機制達成:
1. **Idempotent producer**(`enable.idempotence=true`)→ 每個 message 帶 producer ID + sequence number,broker 端 dedup(同一 producer 重複 send 同 seq 只寫一次)
2. **Transactional API**(`transactional.id=...` + `initTransactions()` + `beginTransaction()` + `commitTransaction()`)→ producer 跨 partition + consumer offset 都在同一個 transaction,atomic commit

**真實 exactly-once 限制**
- 只在「Kafka → Kafka」場景成立(producer 寫到 Kafka + consumer 從 Kafka 讀)
- **跨外部系統(DB / 第三方 API)無法 EOS** → 還是要 idempotent consumer + outbox / saga

**我 booking_monitor 怎麼處理?**
- producer:outbox relay 用 idempotent producer(enable.idempotence + acks=all)
- consumer:saga compensator 不依賴 EOS,而是用 **idempotent compensating action**(Redis revert.lua 設計成可重複跑不會多扣)+ `processed_at` flag 擋 dup
- 不用 transactional API — 因為 saga 跨 Redis + DB,Kafka transaction 不解決

**Reference**: [Confluent EOS deep dive](https://www.confluent.io/blog/exactly-once-semantics-are-possible-heres-how-apache-kafka-does-it/);[Kafka transactions JIRA](https://issues.apache.org/jira/browse/KAFKA-4815)

### 🔽 3. Producer ack levels — acks=0 / 1 / all 取捨

**`acks=0`** — fire-and-forget
- producer 不等任何 ack,送出去就忘
- 最高 throughput,**訊息可能丟**(network fail / broker crash)
- 用例:metrics ingest、log shipping(可丟的場景)

**`acks=1`** — leader ack
- leader 寫進 page cache 就 ack(沒等 replica)
- leader crash + replica 還沒同步 → 訊息丟
- 用例:預設,大多數場景

**`acks=all`(或 `-1`)** — ISR ack
- 等 ISR (In-Sync Replicas) 都寫進去才 ack
- 配 `min.insync.replicas=2` 才有真實 redundancy(只剩 1 個 replica 寫成功 → reject)
- 最高耐久,latency 最高
- 用例:financial、order、saga event(我 booking_monitor 走這個)

**`enable.idempotence=true` 隱含 `acks=all`**(Kafka 強制),用 EOS 等於選 all。

**Reference**: [Kafka Producer Configs](https://kafka.apache.org/documentation/#producerconfigs)

### 🔽 4. Retention policy — saga replay 場景怎麼想

**兩種 retention**
- **time-based**(`retention.ms`)— 例:7 天 → 超過 7 天 segment 被刪
- **size-based**(`retention.bytes`)— 例:10GB → 超過就刪舊的
- 兩者取先到的

**compaction(log compaction)**
- 同 key 只保留最新 message(舊版本逐步 GC)
- 用例:`__consumer_offsets` 本身就是 compacted topic(同 partition 同 group 只留最新 offset);Materialized view source
- 不是「retention」的替代,可同時開:時間 + compaction

**Saga replay 場景**
- saga 要在 `order.failed` topic 重跑歷史 → retention 必須夠長覆蓋你想 replay 的時間窗
- booking_monitor 我設 `retention.ms=604800000`(7 天),理論上可 replay 一週內所有 failed order
- 但 saga 不應該依賴 replay 解 bug,**Kafka 是 transport 不是 source of truth**,SoT 是 PG 的 outbox table + `orders.status`

**Reference**: [Kafka Topic Config](https://kafka.apache.org/documentation/#topicconfigs);[Compaction docs](https://kafka.apache.org/documentation/#compaction)

### 🔽 5. DLQ pattern — booking_monitor 怎麼設?

**為什麼要 DLQ?**
- consumer 處理失敗 → retry → 還是失敗 → 不能無限 retry(會卡死 partition,後面 message 全 block)
- 解法:重試 N 次後 publish 到 `topic.dlq`,主 topic ACK,讓人/監控處理

**booking_monitor 實作**
- `order.failed` topic + 3 次 retry(`sagaMaxRetries`)
- 超過 → publish 到 `order.failed.dlq`(同 partition key,方便 replay 時保序)
- DLQ 不自動消費,operator 手動處理 + 監控告警

**DLQ message 應該帶什麼?**
- 原 message body
- error chain(string)+ retry 次數
- 第一次失敗時間 + 最後一次失敗時間
- 失敗的 stage(便於 root cause)

**DLQ vs retry topic chain**
- 進階 pattern:`retry.5s` / `retry.30s` / `retry.5min` / `dlq` 多層 retry topic,exponential backoff
- booking_monitor 沒做,但我知道 Uber / Stripe / Confluent 都這樣設計

**Reference**: [Uber’s Kafka DLQ pattern](https://www.uber.com/blog/reliable-reprocessing/);[Confluent DLQ best practices](https://docs.confluent.io/platform/current/connect/concepts.html#dead-letter-queue)

### Q&A(必背)

### 🔽 Q: 為什麼選 Kafka 不選 RabbitMQ / NATS?

「**Kafka 贏的場景**:high throughput(每秒百萬 msg)、long retention(時間或 size)、需要 replay、ordered partition、stream processing(KStreams)。

**RabbitMQ 贏的場景**:複雜 routing(topic exchange / direct exchange / fanout)、per-message TTL、priority queue、低 throughput(< 50k msg/s)。

**NATS 贏的場景**:超低 latency(microsecond level)、cloud-native protocol(NATS Streaming / JetStream 對標 Kafka subset)、簡單 pub/sub。

**我 booking_monitor 選 Kafka 因為**:transactional outbox 配 Kafka 是業界標準 pattern(Debezium / Eventuate 都用);長 retention 讓 saga replay 變可能;ordered partition 對 saga event 順序很重要。RabbitMQ 在 ordering 場景吃力(per-queue ordering ok 但 throughput 跟不上),NATS JetStream 比較新生態還不夠 mature。」

📚 [Kafka vs RabbitMQ - Confluent](https://www.confluent.io/learn/rabbitmq-vs-apache-kafka/)

### 🔽 Q: Consumer 在 partition 重複消費怎麼擋?

「Kafka 預設 at-least-once,所以 consumer 必須自己 idempotent。三條路:

1. **Database UNIQUE constraint** — 用 message 內某個 unique id(我 booking_monitor 用 `event_id` + `order_id`)當 DB unique key,重複 insert 直接 `ON CONFLICT DO NOTHING`。最簡單可靠。
2. **External idempotency store**(Redis SETNX)— 處理前 `SET key NX EX 24h`,拿不到表示已處理過,直接 skip。優點:不污染主表;缺點:Redis 掛了就退化。
3. **Compensating action 本身 idempotent** — saga 的 revert action 我設計成可重複跑(`HINCRBY +qty` 用條件式 check 而非無條件加),配 `processed_at` flag。

我 booking_monitor saga 同時用 1 + 3 — DB 層擋,業務 action 本身也 safe。Defense in depth。」

📚 booking_monitor `internal/application/saga/compensator.go`

### 🔽 Q: Kafka 怎麼擴展?加 partition 就好嗎?

「加 partition 只解 throughput,有兩個 catch:

1. **partition 不能減** — 一旦 partition 從 4 變 8,沒辦法縮回 4(資料分佈會錯)。所以一開始要規劃過量,別事後狂加。
2. **加 partition 會打亂 hash key 分佈** — 例:partition 從 4 變 8,原本 hash key % 4 的 message 跟新進來 hash key % 8 的不在同一個 partition → 同 key 的 ordering 在 transition 期間有問題。

業界做法:initial partition 數量設足夠 headroom(預估 throughput × 3),consumer 數 ≤ partition 數,以後加 consumer 比加 partition 更安全。

我 booking_monitor `order.failed` topic 用 3 partition + 3 replica,單實例 throughput 不是瓶頸所以 partition 沒擴張過。production 大流量 LinkedIn 規模會看到 1000+ partition / topic。」

📚 [Confluent Choosing Partition Count](https://www.confluent.io/blog/how-to-choose-the-number-of-topicspartitions-in-a-kafka-cluster/)

### 🔽 Q: 你有用過 Kafka transactional producer 嗎?

「實話 — 沒在 production 用過。我 booking_monitor 用 idempotent producer (`enable.idempotence=true`),沒上 transactional API。理由:

- transactional 主要解「producer 同時寫 N 個 topic + commit consumer offset 要 atomic」的 EOS 場景
- 我的 outbox relay 只寫一個 topic + 不需要跟 consumer offset 綁,idempotent producer 已經夠
- transactional 有性能 cost(coordinator round-trip + transaction marker overhead)

我能講原理 — transactional ID + epoch + 2PC-like commit / abort marker — 但 production hands-on 沒做。面試我會誠實講『熟原理,沒實作過』」

📚 [KIP-98 - Exactly Once Delivery and Transactional Messaging](https://cwiki.apache.org/confluence/display/KAFKA/KIP-98+-+Exactly+Once+Delivery+and+Transactional+Messaging)

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我深調過 Kafka cluster」** — 沒手調過 `num.network.threads` / `num.io.threads` / `socket.send.buffer.bytes`
- ❌ **「我會 Kafka Connect / Streams」** — 沒用過 KConnect connector,沒寫過 KStreams 應用
- ❌ **「我做過 multi-DC replication(MirrorMaker)」** — 沒做過
- ✅ 安全 phrasing:**「I have hands-on Kafka producer + consumer experience including idempotent producer, consumer group + offsets, DLQ pattern, transactional outbox integration. I haven’t operated Kafka clusters at scale or used KConnect / KStreams in production.」**

### 📚 References — Kafka must-read

- [Apache Kafka Documentation](https://kafka.apache.org/documentation/) — 官方,先讀 Design 章節
- [Confluent Developer Course](https://developer.confluent.io/learn-kafka/) — 免費完整教材
- [KIP-98 EOS](https://cwiki.apache.org/confluence/display/KAFKA/KIP-98+-+Exactly+Once+Delivery+and+Transactional+Messaging) — Transactional producer 設計文件
- [Designing Data-Intensive Applications - Ch. 11](https://dataintensive.net/) — Kleppmann 寫,Kafka + Stream processing 最好的章節
- [Confluent Blog](https://www.confluent.io/blog/) — production-grade tuning + pattern

---

### DDD / Clean Architecture ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「DDD 是 Eric Evans 2003 提出的方法論,Clean Architecture 是 Uncle Bob 的分層實作。核心是**依賴方向**:business rule(entity / aggregate)在最內層,application use case 在中間,infrastructure adapter 在最外。所有依賴方向都指向內 — domain 不知道 DB 存在,而是定義 interface 給 infrastructure 實作。我 booking_monitor 全套這樣寫:`internal/domain/` 純 business rule + interface,`internal/application/` use case,`internal/infrastructure/` 是 Gin handler / PG repo / Kafka producer 等 adapter。」
> 

### 📊 booking_monitor 分層圖

```mermaid
graph TB
    subgraph "infrastructure (outer)"
        API[api/booking handler]
        REPO[postgres repositories]
        KAFKA[kafka producer]
        REDIS[redis lua script]
    end
    subgraph "application (middle)"
        BS[BookingService]
        WS[WorkerService]
        SAGA[SagaCompensator]
    end
    subgraph "domain (inner)"
        ENT[Entity: Order / Event]
        REPO_IF[Interface: OrderRepository]
        DOM_SVC[Domain Service]
    end
    API -->|calls| BS
    BS -->|calls| ENT
    BS -->|via interface| REPO_IF
    REPO -.implements.-> REPO_IF
    KAFKA -.implements.-> REPO_IF
    style ENT fill:#fcf
    style REPO_IF fill:#fcf
    style BS fill:#cff
```

依賴方向永遠是 outer → inner,inner 從不知道 outer 存在。

### 🔄 必背深度展開

### 🔽 1. Entity / Value Object / Aggregate / Aggregate Root

**Entity** — 有 identity(ID),lifetime 跨越多個 state 變化
- 例:`Order { ID, Status, Quantity, ... }`,兩個 Order 即使所有欄位一樣,只要 ID 不同就是不同 entity

**Value Object** — 沒有 identity,以「值」為等價基準
- 例:`Money { Amount, Currency }`,兩個 Money 同 amount + currency 就是相等
- 通常 immutable,改一個欄位要 new 一個新的 Value Object

**Aggregate** — 一組「一起變」的 entity + value object,有一個 root entity 統管邊界
- 例:`Order` aggregate 包含 `Order` (root) + 多個 `OrderItem`(內部 entity)
- 對外只能透過 root 操作 — 不能直接拿 internal entity 改

**Aggregate Root** — aggregate 的對外入口
- 持有 aggregate 內所有 entity 的 reference
- 所有 invariant(不變式)在 root 內檢查
- Repository 只 expose root,不 expose internal entity

**為什麼這樣設計?**
- 對外只 expose root → 業務邏輯不會被繞過(例:`order.AddItem()` 內可以檢查 quantity 上限,外面直接 add item 進去就繞過了)
- Transaction boundary = aggregate boundary → 一個 tx 只改一個 aggregate(若要改多個,用 eventual consistency + event publishing)

**我 booking_monitor 怎麼用?**
- `Order` 是 root,封裝 state machine transitions(`MarkPaid()` / `MarkExpired()` 等)— caller 不能直接 `order.status = "paid"`,只能 `order.MarkPaid()`,內部會驗證 transition + emit event
- `Event` 也是 root,提供 `CreateWithDefaultTicketType()`(D4.1 KKTIX 對齊),內部 atomic 建 event + ticket_type

**Reference**: [Eric Evans - DDD Book](https://www.domainlanguage.com/ddd/);[Martin Fowler - DDD Aggregate](https://martinfowler.com/bliki/DDD_Aggregate.html)

### 🔽 2. Repository pattern — interface 該放哪?

**錯誤做法** — interface 跟實作放一起

```go
// internal/infrastructure/persistence/order_repository.go
type OrderRepository interface { ... }     // ❌ interface 在 infrastructure
type postgresOrderRepository struct { ... }
```

**正確做法** — interface 在 domain,實作在 infrastructure(Dependency Inversion)

```go
// internal/domain/order.go
type OrderRepository interface {           // ✅ interface 在 domain
    Save(ctx context.Context, o *Order) error
    FindByID(ctx context.Context, id uuid.UUID) (*Order, error)
}

// internal/infrastructure/persistence/postgres/repositories.go
type postgresOrderRepository struct { ... }  // 實作在 infrastructure
func (r *postgresOrderRepository) Save(...) error { ... }
```

**為什麼差別重要?**
- 第一種 — domain 用 repository 時要 import infrastructure,**依賴方向違反 inner → outer 不能向外**
- 第二種 — domain 只 import 自己;infrastructure import domain 來實作 interface;依賴方向正確

**好處實際是什麼?**
- 換 DB(PG → MongoDB)只改 infrastructure 一個檔案,domain 跟 application 不動
- testing 時用 mock OrderRepository,不用起真 DB(雖然我 booking_monitor 為了 race condition 安全還是 testcontainers)
- 你的 domain 不會被 ORM annotation / sql tag 污染 — `Order` 是純粹的 business object

**Reference**: [Uncle Bob - Clean Architecture](https://blog.cleancoder.com/uncle-bob/2012/08/13/the-clean-architecture.html);booking_monitor `internal/domain/order.go` vs `internal/infrastructure/persistence/postgres/repositories.go`

### 🔽 3. Hexagonal / Onion / Clean — 差別到底是什麼?

**Hexagonal (Ports & Adapters)** — Alistair Cockburn 2005
- 強調「對稱性」:核心邏輯被 ports 包圍,每個 port 是一個 interface
- 兩種 port:driving(被動接受 — HTTP handler / CLI)+ driven(主動呼叫 — DB / Kafka)
- 每個 port 有一個或多個 adapter 實作

**Onion** — Jeffrey Palermo 2008
- 強調「依賴方向」+ 「層數明確」
- Domain → Application Service → Domain Service → Infrastructure 四層,依賴向內
- 比 Hexagonal 更層級化

**Clean Architecture** — Uncle Bob 2012
- 集大成 — Entity / Use Case / Interface Adapter / Frameworks & Drivers 四層
- 加 Dependency Rule(依賴方向向內)+ DTO crossing layer boundary
- 比 Onion 更具體,實作 guide 更詳細

**實作上差別微小**
- 三者都強調依賴向內、core 不知 framework 存在、可測試
- 名詞不同 (Hex 叫 port、Onion 叫 layer、Clean 叫 boundary),思想一樣
- 真實 codebase 通常混用 — Clean 的命名 + Hex 的 driving/driven port 概念

**我 booking_monitor 用哪個?**
- 命名上接近 Clean(`domain` / `application` / `infrastructure`)
- 概念上有 Hex 的 driving/driven 意識(`api/` 是 driving adapter,`persistence/` + `cache/` + `messaging/` 是 driven adapter)
- 不會強求純粹 — practical 的取捨,不照搬

**Reference**: [Hexagonal architecture - Alistair Cockburn](https://alistair.cockburn.us/hexagonal-architecture);[Onion Architecture - Jeffrey Palermo](https://jeffreypalermo.com/2008/07/the-onion-architecture-part-1/);[Clean Architecture - Uncle Bob](https://blog.cleancoder.com/uncle-bob/2012/08/13/the-clean-architecture.html)

### 🔽 4. Bounded Context — DDD 的「真正關鍵概念」

**為什麼這是 DDD 最重要概念?**
- 大系統不可能用一個 ubiquitous language 蓋全部
- 不同 sub-team(billing / inventory / shipping)對「Order」的定義不同 — billing 看到 `amount + tax`,inventory 看到 `items + qty`,shipping 看到 `address + status`
- 強行統一 model → god class,改一處壞十處
- DDD 解法:**切 bounded context**,每個 context 內 model + ubiquitous language 一致,context 之間用 anti-corruption layer + translation

**Bounded Context 怎麼切?**
- 沿 business sub-domain(billing / inventory / payment)
- 沿 team boundary(Conway’s law — system 結構 mirror 組織結構)
- 沿 data lifecycle(write-heavy vs read-heavy 可拆 CQRS)

**Context Map**(context 之間的關係)
- Shared Kernel — 共用 model(慎用,coupling 強)
- Customer/Supplier — 上下游關係
- Conformist — 下游全盤接受上游 model
- Anti-Corruption Layer — 下游不接受上游 model,中間有 translation

**我 booking_monitor 算 single context**
- 規模小,只有一個 booking context
- 但我有 anti-corruption awareness — 例:接 Stripe 時,我自己定義 `domain.PaymentIntent`,不直接用 stripe-go 的 `stripe.PaymentIntent`(後者是 stripe-specific concept,不應該污染 domain)

**Reference**: [DDD Bounded Context - Fowler](https://martinfowler.com/bliki/BoundedContext.html);[Vaughn Vernon - Implementing DDD](https://kalele.io/books/)

### 🔽 5. CQRS / Event Sourcing — DDD 高階配套

**CQRS (Command Query Responsibility Segregation)**
- Write model 跟 Read model 分離
- Write 走 entity / aggregate(正規化 + business rule check)
- Read 走 dedicated query model(denormalized + 快)
- 中間用 event 同步(可以同 DB 不同 table,或不同 DB)

**Event Sourcing**
- 不存 current state,只存 event sequence
- current state 從 event replay 得來
- 好處:audit log 天然就有、time travel(過去 state)、replay 可重建任意時間 snapshot
- 壞處:read 要 replay 或 materialized view、學習曲線陡

**兩個常一起講**
- ES 寫端是 event log;CQRS 讀端是 materialized view from events
- 但**不是必須一起用**:單獨 CQRS 也可以(write/read 不同 model,event 用 outbox 同步)

**我 booking_monitor 沒用 ES**
- 我用 transactional outbox(write 端是 entity state + outbox event)
- 不需要完整 ES — booking 場景 read pattern 跟 write 沒差太多,不值得 ES 的複雜度
- 但 `order_status_history` 表算是 mini ES(audit log)— 每次狀態變化都 append 一筆

**Reference**: [Fowler - CQRS](https://martinfowler.com/bliki/CQRS.html);[Greg Young - ES](https://www.youtube.com/watch?v=8JKjvY4etTY)

### Q&A(必背)

### 🔽 Q: DDD 不是 over-engineer 嗎?小專案有必要嗎?

「實話 — DDD 對 < 5 entity / < 6 month lifecycle 的專案是 over-engineer。但 DDD 不是「全有或全無」,核心三件事任何規模都該做:

1. **Entity 不要直接擺 ORM tag** — `Order` 是 business object,不該被 `gorm:\"column:status\"` 污染
2. **Repository interface 在 domain** — 寫 `domain.OrderRepository` interface,實作放 infrastructure
3. **Aggregate root 封裝 transition** — `order.MarkPaid()` 不是 `order.status = \"paid\"`

這三件事即使 3-entity 專案都有 ROI(testing 容易、business rule 集中、ORM swap 簡單)。其他 DDD 進階概念(bounded context / context map / domain event)是 5-team scale 才需要。

我 booking_monitor 規模算小但全套都上 — 因為這是 portfolio,展示我**會做 DDD** 而不是僅熟術語。Commeet 我們選擇實用 layered,DDD 思想有但沒用全套術語。」

📚 [Vaughn Vernon - DDD Distilled](https://www.amazon.com/Domain-Driven-Design-Distilled-Vaughn-Vernon/dp/0134434420)

### 🔽 Q: 你的 domain 真的「乾淨」嗎?無 framework 依賴?

「老實說 — 90% 乾淨,10% 妥協。

**乾淨**:`internal/domain/order.go` 不 import gorm / gin / stripe-go / 任何 framework。只 import stdlib + `github.com/google/uuid`。Entity 是純粹的 struct + method,沒任何 annotation。

**妥協** — uuid package。理論上 `uuid.UUID` 也是個 external dependency,完全純粹的 DDD 會說 domain 應該自己定義 `OrderID` type。但實務上 uuid 是 widespread 的 type,我選妥協接受 leak。

**Repository interface 細節**:我 `domain.OrderRepository` 用 `context.Context` 當第一個參數 — context 也算 framework concept。Pure DDD 派可能會包成 `domain.DeadlineContext`,但太 noise。我選妥協。

實際生產 codebase 普遍 mix — 沒有人真寫 100% 純粹的 domain。重要的是 awareness:**你知道哪些是純粹哪些是妥協,妥協有理由,不要無意識被 framework 滲透**。」

📚 booking_monitor `internal/domain/` 目錄

### 🔽 Q: 你的 application service 跟 domain service 怎麼分?

「規則 — **business logic 屬於哪個 entity 就放哪個 entity 的 method;不屬於任一 entity 的跨多 entity logic 放 Domain Service;orchestration 跟 transaction / repository / external call 放 Application Service**。

實例:
- `order.MarkPaid()` — 屬於 Order 自身的 state transition → Order entity method
- `inventoryService.ReserveAcrossEvents(events, qty)` — 跨多個 Event 的庫存協調 → Domain Service
- `BookingService.BookTicket(ctx, req)` — orchestrate Redis check + DB insert + outbox emit → Application Service

**Application Service 不該包 business rule** — 它只是把 transaction、call sequence 編排好,真正的 rule 都 delegate 到 entity / domain service。

我 booking_monitor `BookingService.BookTicket()` 就是這樣 — open tx → call `domain.NewReservation()`(business rule)→ `repo.Order.Save()` → `outbox.Emit()`,沒在 application 層判斷『quantity 不能負數』,那是 `domain.NewReservation()` 自己的事。」

📚 booking_monitor `internal/application/booking/service.go`

### 🔽 Q: Bounded context 怎麼跟 microservice 對應?

「**Bounded context 跟 microservice 不是 1:1 對等**,但有強相關。

- bounded context = 概念邊界(model + ubiquitous language 一致)
- microservice = 部署邊界(獨立 process / DB / lifecycle)
- 一個 bounded context **可以**對應一個 microservice,但**未必**

實務常見:
- 1 個 bounded context → 1 個 microservice(理想,team independent)
- 多個 bounded context → 1 個 monolith(早期或小團隊,context 用 package 邊界區隔)
- 1 個 bounded context → 多個 microservice(垂直拆,例:read-side 跟 write-side 分開,但同 model)

**「沒拆 microservice 不代表沒 DDD」** — modular monolith 就是「concept 邊界清楚,但 deploy 在一起」的健康架構,Shopify / DHH 都倡導。

我 booking_monitor 是 modular monolith — 一個 binary 但 `internal/{domain,application,infrastructure}` 邊界清楚。Future 拆 microservice 是低成本(沿邊界切就行),但現在沒這個業務需求所以不拆。」

📚 [Shopify Modular Monolith](https://shopify.engineering/shopify-monolith);booking_monitor 整體結構

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我設計過 bounded context」** — 你 booking_monitor 是 single context,沒切過跨 context 邊界
- ❌ **「我會 event storming」** — 沒實際跑過 event storming workshop
- ❌ **「我熟 DDD strategic design」** — strategic 是 context map / sub-domain / domain expert collaboration,你 solo 寫的專案沒這個鍛鍊
- ✅ 安全 phrasing:**「I apply DDD tactical patterns — entity / value object / aggregate / repository interface in domain — across booking_monitor and commeet-text2sql Phase 3 refactor. I’m familiar with strategic design concepts (bounded context, context map) but haven’t had the chance to apply them in a multi-team setting yet.」**

### 📚 References — DDD / Clean Architecture must-read

- [Eric Evans - DDD: Tackling Complexity](https://www.domainlanguage.com/ddd/) — 原典,難啃但必讀
- [Vaughn Vernon - Implementing DDD](https://kalele.io/books/) — 比 Evans 容易上手,有完整 code example
- [Uncle Bob - Clean Architecture](https://blog.cleancoder.com/uncle-bob/2012/08/13/the-clean-architecture.html) — 經典 blog post,實作 guide
- [Fowler bliki - DDD related](https://martinfowler.com/tags/domain%20driven%20design.html) — 精短 reference
- [Microsoft - .NET Microservices DDD Guide](https://learn.microsoft.com/en-us/dotnet/architecture/microservices/microservice-ddd-cqrs-patterns/) — 跨技術但 DDD 解釋清楚

---

### Saga (Forward-only Recovery) ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「Saga 是 Garcia-Molina 1987 提出來的 long-running transaction 模式 — 一系列 T1, T2, …, Tn,任一失敗就用對應的 compensating action Ci 反沖前面已 commit 的。**Forward-only** 是我的設計選擇:saga 只管 failure path 的 compensation,不管 happy path 的 commit。我 booking_monitor 從 D2 全 saga 寫到 D7 narrowing(PR #98),**淨刪 ~450 LOC**(實際 stats: 1193 insertions / 1640 deletions)的 legacy auto-charge consumer 路徑,只留兩個失敗 emitter — D5 webhook 收到 `payment_failed` + D6 expiry sweeper 過期。Saga 不該管 happy path 是這個專案最重要的設計教訓。」
> 

### 📊 booking_monitor saga 範圍對比圖

```mermaid
graph LR
    subgraph "D2-D6 (錯誤設計)"
        OLD_OK[Order Paid] -->|saga handle| OLD_K[Kafka order.created]
        OLD_K -->|saga charge| OLD_PAY[Payment Service]
        OLD_PAY -->|success| OLD_SAGA[Saga commit]
        OLD_FAIL[Payment Failed] -->|saga compensate| OLD_REV[Revert]
    end
    subgraph "D7 narrowing (正確設計)"
        OK[Order Paid] -->|sync| DB[(DB)]
        DB --> DONE[Done]
        FAIL1[D5 Webhook payment_failed] -->|emit| OF[order.failed]
        FAIL2[D6 Expiry sweep] -->|emit| OF
        OF -->|saga compensate| REV[Revert inventory + DB]
    end
    style OLD_OK fill:#fee
    style OLD_K fill:#fee
    style OLD_PAY fill:#fee
    style OLD_SAGA fill:#fee
    style FAIL1 fill:#efe
    style FAIL2 fill:#efe
```

### 🔄 必背深度展開

### 🔽 1. Garcia-Molina §5 原論文 — Saga 的真實定義

**論文出處** — [Sagas (Hector Garcia-Molina, Kenneth Salem, 1987)](https://www.cs.cornell.edu/andru/cs711/2002fa/reading/sagas.pdf)

**§5 完整定義**:Saga = sequence of T1, T2, …, Tn 而每個 Ti 都有對應的 compensating transaction Ci
- 任一 Ti 失敗 → 跑 Ci-1, Ci-2, …, C1 (反向 compensate)
- compensating transaction 必須:**semantically undo** Ti(不一定是字面 rollback,可能是「issue refund」這種正向動作)

**論文的關鍵 insight(常被忽略)**
- Saga 是 **long-lived transaction 的替代方案**(超過幾秒鐘的 transaction 在 DB 是 anti-pattern)
- 重點在「不持有 long-lived lock」— 每個 Ti 各自 commit,中間其他人能讀到
- 代價是 **lost atomicity**:中間時刻可能讀到 partial state(Ti 跑完但 Ti+1 還沒)

**§5 沒講的(現代解讀)**
- choreography vs orchestration — 論文沒區分,後人加的
- exactly-once event delivery — 論文假設 perfect messaging,現實要 idempotent consumer
- distributed transaction coordinator(saga manager / process manager)— 後續 pattern

**為什麼要讀原論文?**
- 多數教材講 saga 只講 2-phase choreography,忽略 §5 的真正 insight
- 面試講你讀過原論文 + 講對「saga 不是 distributed transaction、是 long-lived 替代」credibility 很高
- 我自己 booking_monitor 設計 D7 narrowing 的時候就回去 reread §5

**Reference**: [原論文 PDF](https://www.cs.cornell.edu/andru/cs711/2002fa/reading/sagas.pdf);[Microservices.io - Saga Pattern](https://microservices.io/patterns/data/saga.html)

### 🔽 2. Choreography vs Orchestration

**Choreography**(去中心化)
- 每個 service 自己訂閱相關 event,自己決定下一步
- 沒有 central coordinator
- 例:Service A 完成 → publish `event.A_done` → Service B 訂閱 → 自己跑 → publish `event.B_done` → Service C 訂閱…

**Orchestration**(中央集權)
- 有一個 Saga Orchestrator / Process Manager 統籌
- Orchestrator 知道整個 saga flow,call 各 service + 收結果 + 決定下一步
- 例:Temporal / Cadence / Camunda

| 特性 | Choreography | Orchestration |
| --- | --- | --- |
| 中心化 | ❌ 去中心 | ✅ 有 orchestrator |
| Coupling | ✅ 低(service 間只透過 event) | ❌ orchestrator coupling 重 |
| 可觀測 | ❌ 流程散在各處,難追蹤 | ✅ 一個地方看完整 flow |
| 修改流程 | ❌ 改多個 service | ✅ 改 orchestrator 一處 |
| 適合 | 簡單 saga (3-5 step) | 複雜 saga (10+ step) |

**我 booking_monitor 選 choreography**
- saga 只有 1-2 個 compensate step,choreography 簡單夠用
- 不想引入 Temporal/Cadence 增加 ops 負擔
- Kafka topic `order.failed` + saga consumer 已經是 minimal choreography

**什麼時候要 orchestration?**
- saga 有 5+ step + 複雜 conditional branching
- 不同 step timeout 不一樣(有些要等 1 小時,有些 5 秒)
- 需要 retry policy 細控制 + visibility into saga state

**Reference**: [Microservices.io Choreography vs Orchestration](https://microservices.io/patterns/data/saga.html);[Temporal Workflow docs](https://docs.temporal.io/workflows)

### 🔽 3. D7 narrowing — 「Saga 不該管 happy path」

**Pre-D7 設計(錯誤)**

```
Order created → emit order.created → saga charge → emit order.paid → done
                                  ↓ fail
                                  saga compensate → revert
```

- happy path 走 saga(emit `order.created` + Kafka consumer 自動扣款)
- 問題:legacy auto-charge consumer 邏輯複雜,kafka 重複消費風險,容易跟 D4 PaymentIntent flow 衝突

**D7 後設計(正確)**

```
Order created → sync write DB → done                          (happy path 不走 saga)
Webhook payment_failed → emit order.failed → saga compensate  (failure path)
Expiry sweep            → emit order.failed → saga compensate
```

- happy path:同步寫 DB + outbox + 等 webhook 回來 mark paid。**完全不經 saga**
- failure path:**只有 2 個 emitter**(D5 webhook + D6 sweeper),都 publish 到 `order.failed` topic,saga consumer compensate

**為什麼這樣設計?**
- happy path 簡單就用簡單 — 同步 DB write + idempotent webhook 已經足夠
- saga 引入的複雜度(Kafka consumer + retry + DLQ + idempotency)應該只在「真的需要 cross-service compensation」場景值得
- 結果:**淨刪 ~450 LOC** + 更易 debug(只剩兩個 saga emit 入口)+ 更少 race condition

**這個轉變我哪裡學的?**
- 重讀 Garcia-Molina §5 — 論文沒講 saga 要 cover happy path
- Eventuate / Axon 等 framework 預設 cover happy path 是 framework 偏見,不是 saga 本義
- Stripe / Shopify 公開設計都不對 happy path 用 saga

**Reference**: booking_monitor D7 narrowing PR(in repo blog);[Stripe Engineering Idempotency post](https://stripe.com/blog/idempotency)

### 🔽 4. Compensating action 的 idempotency 要求

**為什麼 saga 必須 idempotent?**
- Kafka at-least-once → 同一個 `order.failed` event 可能 redelivered
- saga consumer 重複處理 → compensate 跑兩次
- 不 idempotent → 過度補償(例:Redis qty +2 而非 +1)

**3 種 idempotency 設計**

1. **State-check guard**

```go
order, _ := repo.FindByID(ctx, msg.OrderID)
if order.Status == OrderStatusCompensated {
    return nil  // 已 compensate 過,skip
}
// 真實 compensate logic
```

1. **External idempotency store**

```go
// Saga 拿到 event id 先 SETNX,拿不到 = 已處理
ok, _ := redis.SetNX(ctx, "saga:"+msg.EventID, "1", 24*time.Hour)
if !ok { return nil }
// 真實 compensate logic
```

1. **Compensating action 本身設計成 idempotent** — 真實 booking_monitor `revert.lua`(節錄):

```lua
-- KEYS[1]: ticket_type_qty:{id}              (inventory)
-- KEYS[2]: saga:reverted:{compensation_id}   (idempotency marker)

if redis.call("EXISTS", KEYS[2]) == 1 then
    return 0   -- already reverted
end
local current = redis.call("GET", KEYS[1])
if current then
    redis.call("INCRBY", KEYS[1], ARGV[1])
end
redis.call("SET", KEYS[2], "1", "EX", 604800)   -- 7d TTL
return 1
```

- **設計理由(comment 原文)**:用 `EXISTS` guard 而非 `SETNX` — 因為整段 Lua 在 Redis 內 atomic,EXISTS+INCRBY+SET 不會被打斷;而當 Redis 用 `appendfsync=always` 在 INCRBY 之後 crash 時,SETNX 路徑會留下「key 已 set 但 inventory 沒回沖」的 **silent under-revert**,EXISTS 路徑則會在 retry 時觸發第二次 INCRBY → over-revert 被 inventory > total 監控抓到 → **「loud over-revert beats silent under-revert」**

**我 booking_monitor 同時用 1 + 2 + 3**(defense in depth)
- State-check 在 application 層:`order.MarkCompensated()` 內檢查 state
- External SETNX 在 saga 入口:`saga:event_id` 24h TTL
- revert.lua 本身用 EXISTS guard,即使極端 crash 場景也是「loud over-revert」

**Reference**: booking_monitor `internal/application/saga/compensator.go`;Stripe 的 idempotency design

### 🔽 5. Why Kafka choreography over Temporal orchestrator?

**Temporal / Cadence 賣點**
- Workflow as code(用 Go / Java 寫 long-running workflow)
- Replay-based state persistence(workflow code 是 deterministic,可從 history replay)
- 內建 retry / timeout / signal / version migration
- Visibility into running workflows

**為什麼我 booking_monitor 沒用 Temporal?**
- 我的 saga 簡單(1-2 step compensation),Temporal 殺雞用牛刀
- 引入 Temporal cluster 等於再加一個 stateful service ops 負擔(server + worker + UI)
- Kafka 已經有,saga 寫成 Kafka consumer 自然
- Portfolio scope:展示「自己會寫 saga 配 Kafka」比「整合 Temporal SDK」更能展示我懂分散式 transaction 原理

**什麼時候真的該用 Temporal?**
- workflow 5+ step + 有複雜 conditional / loop
- 需要 long-running activity(human approval、wait 7 天)
- Multi-team 共享 workflow 平台,UI 可給 PM 看
- 已經有 Temporal team / infra 投資

**我的誠實 position**
- 知道 Temporal 存在 + 為什麼 production 大廠都用
- booking_monitor 規模不值得引入
- Production 規模我會推 Temporal,而不是自己 Kafka choreography 越寫越複雜

**Reference**: [Temporal blog - Why Workflows](https://temporal.io/blog/long-running);[Cadence (Uber’s predecessor)](https://cadenceworkflow.io/)

### Q&A(必背)

### 🔽 Q: 為什麼 saga 不是 distributed transaction?

「最常見的誤解。Distributed transaction(2PC / XA)強調 **atomicity across services** — 所有 service 一起 commit 或一起 rollback,中間有 prepare phase 投票。Saga 完全不是這個概念。

Saga 的關鍵 insight:**放棄 atomicity 換取 availability** — 每個 step 各自 commit,中間時刻可能讀到 partial state,但用 compensating action 達到最終一致(eventual consistency)。

2PC 失敗的場景:prepare phase 收到 vote 後 coordinator 掛了 → 所有 participant 卡在 prepared 狀態 → 拿著 lock 等不到 commit/abort 指令 → 死鎖。Saga 不會卡 — 任一 step 失敗就反向 compensate,沒有 prepared 狀態。

代價:saga 期間其他人可能讀到 partial state(我 booking 已 deduct 但 payment 還沒 confirm)。所以 saga 適合「temporary inconsistency acceptable」場景。financial settlement 嚴格的 case 還是要 2PC 或先 design 成不需要 cross-service atomicity。」

📚 [Garcia-Molina 原論文 §5](https://www.cs.cornell.edu/andru/cs711/2002fa/reading/sagas.pdf);[2PC vs Saga - Microservices.io](https://microservices.io/patterns/data/saga.html)

### 🔽 Q: saga compensate 跑到一半失敗怎麼辦?

「Saga 的 dirty little secret — compensate 本身也可能失敗,然後你就有 partially-compensated state。三條應對策略:

1. **Retry compensate**(infinite or with budget)— 假設 transient failure(network blip),retry 通常會過。但要小心 stale data / divergence。
2. **Forward fix(continue forward)**— 既然 compensate 跑到一半,有時 forward 比 backward 簡單。例:已 issue refund 一半,你補另一半比把 refund 撤回容易。
3. **Manual intervention**(DLQ + alert)— 設 retry 上限,超過就放 DLQ + page operator。booking_monitor 設 3 次,超過 → `order.failed.dlq`。

我 booking_monitor 同時用 1 + 3 — `sagaMaxRetries=3` retry,超過丟 DLQ。Saga watchdog (D6 PR #49) 還會 background scan stuck `failed` order,定期 re-drive。

**重要的是接受 saga 不是 100% — 你選了 eventual consistency + 自動 compensation,代價是某些 edge case 需要人介入。** 比較好的 production design 是把這條 manual 流程 well-documented + 設好 alerting,而不是假裝 saga 永遠完美。」

📚 booking_monitor saga watchdog (PR #49);[Pat Helland - Idempotence Is Not a Medical Condition](https://queue.acm.org/detail.cfm?id=2187821)

### 🔽 Q: 為什麼 saga 需要 outbox?直接 publish Kafka 不行嗎?

「直接 publish Kafka 你會踩到 **dual-write problem**:你要 (1) update DB + (2) publish Kafka,兩者不能 atomic。三種失敗 scenario:

- 寫 DB 成功 + publish 失敗 → 業務 state 變了但其他人不知道(downstream miss event)
- publish 成功 + 寫 DB 失敗 → 其他人收到 event 但 DB 沒對應 state(downstream 看到鬼)
- 中間 crash → 看你重啟邏輯怎麼處理

**Outbox 解這個** — 同一個 DB transaction 內寫 entity + outbox row,DB 自己保證 atomic。然後 outbox relay(獨立 process)輪詢 outbox table 把未發送 event 推到 Kafka,推完 mark `processed_at`。

**Outbox relay 的失敗也 OK**:
- 推 Kafka 成功 + 更新 processed_at 失敗 → 下次再推一次(at-least-once,consumer idempotent 就好)
- 推 Kafka 失敗 → 不更新 processed_at,下次重推
- 不論哪種失敗,**Kafka side 不會少 event**(at-most-miss-once 變 at-least-deliver-once)

**為什麼這 pattern 業界普及?** Debezium / Eventuate / 自己寫 relay 都這個思路。dual-write 問題在 microservices + event-driven 場景太普遍,outbox 是業界標準解。」

📚 [Microservices.io Transactional Outbox](https://microservices.io/patterns/data/transactional-outbox.html);[Debezium Outbox docs](https://debezium.io/documentation/reference/stable/transformations/outbox-event-router.html)

### 🔽 Q: 你 saga 的 transition 怎麼確保不會 double-compensate?

「**Explicit state machine + atomic CTE transition**。我 booking_monitor 設計:

`Order` 有明確 status enum(awaiting_payment / charging / paid / failed / expired / compensated),每個 transition 用 atomic SQL:

```sql
UPDATE orders SET status = 'compensated', compensated_at = NOW()
 WHERE id = $1 AND status = 'failed'
 RETURNING status;
```

如果 RowsAffected = 0,代表前置條件不滿足(可能已被別人 compensate 過)→ 不 panic,而是回 `domain.ErrInvalidTransition`。Saga consumer 看到這個 error 就知道是 dup → 直接 ACK 不重試。

`order_status_history` 表還會 append 每次 transition,雙保險:
- 主表 `orders.status` = 當前狀態
- `order_status_history` = audit log,每個 transition 一筆

兩種 dup 都擋:
1. Saga consumer 重複收到 event → atomic CTE 把第二次 reject 掉
2. Saga retry → 同樣被 atomic CTE 擋掉

我 PR #40 (C1) 加進去這個 CTE-based transition logging,就是為了徹底擋這類 race。」

📚 booking_monitor `internal/domain/order.go` state machine;PR #40 C1 commit

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我熟 Temporal / Cadence」** — 沒在 production 寫過 Temporal workflow
- ❌ **「我做過 5+ step saga」** — 你 booking_monitor 只有 1-2 step compensation,沒做過超複雜 multi-step
- ❌ **「我會 microservice scale saga」** — 你 saga 是 modular monolith 內的事,跨 service 沒做過
- ✅ 安全 phrasing:**「I designed a forward-only saga in booking_monitor with explicit state machine + atomic CTE transition + DLQ + watchdog. I have a solid mental model of choreography vs orchestration tradeoffs and know when Temporal/Cadence becomes appropriate, though I haven’t operated those in production.」**

### 📚 References — Saga must-read

- [Garcia-Molina 原論文](https://www.cs.cornell.edu/andru/cs711/2002fa/reading/sagas.pdf) — 必讀,§5 是 saga 的根
- [Microservices.io Saga Pattern](https://microservices.io/patterns/data/saga.html) — Chris Richardson 寫,最完整 pattern catalog
- [Stripe Engineering - Idempotency](https://stripe.com/blog/idempotency) — saga compensate 必備的 idempotency 設計
- [Temporal blog](https://temporal.io/blog) — workflow orchestration 看當前思想
- [Designing Data-Intensive Applications - Ch. 9](https://dataintensive.net/) — Kleppmann 寫 consistency / consensus,包括 saga 在內

---

### Transactional Outbox ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「Transactional Outbox 解決 **dual-write problem** — 你要同時 update DB + publish event 到 message bus,兩個沒辦法 atomic。Outbox 的解法:同一個 DB transaction 內,把 event row 寫進 `events_outbox` table,DB 自己保證 atomic。獨立 process 叫 outbox relay 輪詢 outbox table 把未發送的 event 推到 Kafka,推完 mark `processed_at`。我 booking_monitor 用 PG advisory lock (`pg_try_advisory_lock(1001)`) 做 relay leader election,確保多實例只有一個 relay 在跑,避免重複 publish。Partial index `ON events_outbox(id) WHERE processed_at IS NULL` 讓 relay 輪詢只掃未處理的 row,DB 不用全表 scan。」
> 

### 📊 Outbox flow 完整圖

```mermaid
sequenceDiagram
    participant API
    participant DB as PostgreSQL
    participant Relay as Outbox Relay
    participant K as Kafka

    API->>DB: BEGIN
    API->>DB: INSERT orders ...
    API->>DB: INSERT events_outbox (event_type, payload)
    API->>DB: COMMIT  (atomic)
    Note over DB: orders + events_outbox 一起 commit

    Relay->>DB: pg_try_advisory_lock(1001)
    Note over Relay: 拿到 lock = leader
    loop poll
        Relay->>DB: SELECT FROM events_outbox WHERE processed_at IS NULL LIMIT 100
        Relay->>K: produce(event)
        K-->>Relay: ack
        Relay->>DB: UPDATE events_outbox SET processed_at = NOW() WHERE id = ?
    end
```

### 🔄 必背深度展開

### 🔽 1. Dual-write problem — 為什麼一定要 outbox?

**Dual-write 是什麼?**
你要做兩件事:(A) update entity in DB + (B) publish event to message bus。
這兩件事**不在同一個 transaction**,任何一邊先做都有 fail mode。

**Scenario 1 — 先寫 DB 再 publish**

```go
db.Save(order)        // ✅
kafka.Publish(event)  // ❌ network blip → event 沒送出
```

結果:order state 變了,但 downstream 收不到 → **data leak / lost event**

**Scenario 2 — 先 publish 再寫 DB**

```go
kafka.Publish(event)  // ✅
db.Save(order)        // ❌ DB connection 斷 → order 沒寫進去
```

結果:downstream 收到 event,但 source of truth 沒對應 state → **phantom event / inconsistency**

**Scenario 3 — Try-catch + 補償**

```go
db.Save(order)
defer func() {
    if err := kafka.Publish(event); err != nil {
        db.Undo(order)  // 試圖 rollback
    }
}()
```

看起來 ok,但 undo 自己可能失敗(網路斷、process crash 在 undo 前)→ 無限套娃,沒有完美解

**Outbox 解這個** — 不再嘗試「同時做兩件事」,而是把 publish 任務寫進 DB(同 tx atomic),由獨立 process 後續處理。**唯一的 atomic 寫入只在 DB**,Kafka publish 可以 retry / 失敗不影響 source of truth

**Reference**: [Microservices.io Transactional Outbox](https://microservices.io/patterns/data/transactional-outbox.html);[Confluent Outbox Pattern](https://developer.confluent.io/courses/microservices/the-transactional-outbox-pattern/)

### 🔽 2. Outbox table schema — 三種 schema 設計

**Schema 1 — Generic event store**(我 booking_monitor 用這個)

```sql
CREATE TABLE events_outbox (
    id           UUID PRIMARY KEY,
    aggregate_id UUID NOT NULL,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    processed_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_pending
    ON events_outbox(id)
    WHERE processed_at IS NULL;  -- partial index
```

優點:任意 event type 都能塞;payload 用 JSONB 彈性
缺點:type-safe 弱(payload 是 JSONB)

**Schema 2 — Per-topic table**

```sql
CREATE TABLE order_failed_outbox (
    order_id    UUID,
    reason      TEXT,
    failed_at   TIMESTAMPTZ,
    processed_at TIMESTAMPTZ
);
```

優點:type-safe,schema-on-write
缺點:每個 event type 一個表,scale 不好

**Schema 3 — Inline in entity**

```sql
ALTER TABLE orders ADD COLUMN pending_events JSONB;
```

優點:不另開表;簡單
缺點:relay 要掃整個 orders 表,不能用 partial index 加速;orders 表寫入量大

**Partial index 的關鍵**
- `WHERE processed_at IS NULL` 條件式 index 只 index 未處理 row
- 一旦 processed_at 寫上,row 自動從 index 移除 → index size 永遠很小
- relay 輪詢 `SELECT WHERE processed_at IS NULL` 直接走 partial index,O(N pending) 而非 O(N total)
- booking_monitor migration 0007 就是加這個 partial index

**Reference**: booking_monitor `deploy/postgres/migrations/000007_*.sql`;Stripe-side 設計

### 🔽 3. Relay leader election — 為什麼用 advisory lock?

**問題**:relay 要保證**只有一個實例在跑**(不然多實例同時撈 outbox row → 重複 publish)。

**選項 A — Row-level lock**

```sql
SELECT * FROM events_outbox
 WHERE processed_at IS NULL
 LIMIT 100
 FOR UPDATE SKIP LOCKED;
```

優點:多 worker 可以平行處理(每個 worker 拿不同 row)
缺點:每次 query 都要拿 lock,overhead 大;skip locked 可能 starvation

**選項 B — Distributed lock(Redis Redlock / ZK)**
- 引入額外 lock service,ops 負擔
- Kleppmann 批評過 Redlock 不適合 correctness-critical

**選項 C — PG advisory lock**(我 booking_monitor 選這個)

```go
acquired, _ := db.QueryRow("SELECT pg_try_advisory_lock(1001)").Scan(&acquired)
if !acquired { return }  // 不是 leader,睡覺
// 是 leader,輪詢 outbox
```

優點:
- PG 內建,**session 死了 lock 自動釋放**
- `try_*` variant 不 block,適合 leader election(拿不到立刻知道)
- 只有 leader 在跑,不需要 row lock contention

**Lock ID 怎麼選?**
- `1001` 是 arbitrary number,但要全 application 唯一
- 進階:`pg_try_advisory_lock(hashtext('outbox_relay_leader'))` 用 hashtext 把 string 轉 int

**Leader 死了怎麼辦?**
- PG TCP keepalive 預設 7200s 才偵測 session 死 → 太久
- 解法 1:`tcp_keepalives_idle = 60s` 設短
- 解法 2:relay 自己加 health check + panic exit(我 booking_monitor 走這個)

**Reference**: booking_monitor `internal/application/outbox/relay.go`;[PG Advisory Locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS)

### 🔽 4. CDC (Change Data Capture) — Debezium 為何是替代方案?

**CDC 是什麼?**
- 不用 application 寫 outbox,直接讀 DB 的 **transaction log**(PG WAL / MySQL binlog)
- 把每筆 INSERT/UPDATE/DELETE 變成 event 推到 Kafka
- Debezium 是業界標準(open-source,Red Hat 維護)

**CDC vs Outbox 比較**

| 特性 | Outbox | CDC (Debezium) |
| --- | --- | --- |
| 設定複雜度 | 簡單(自己寫 relay) | 中(要架 Debezium connector + Kafka Connect) |
| Event schema | Application 控制(乾淨 event) | Database row 直接 dump(raw row + metadata) |
| Downstream coupling | 低(event 是 application-defined) | 高(downstream 看 raw row schema) |
| 適合場景 | Application 改寫得起 | Legacy DB 無法改 application |

**為什麼我 booking_monitor 選 outbox 不選 CDC?**
- 我有 application 控制權,outbox 寫 clean event 比 dump raw row 對 downstream 友好
- Debezium 引入 Kafka Connect 依賴,demo project 太重
- Production 大廠(Stripe、Shopify)通常**兩個 pattern 一起用** — application 控制的 event 走 outbox,legacy DB 變化走 CDC

**CDC 的 dirty truth**
- DB schema 變了 → downstream consumer 全壞掉(因為他們依賴 raw column name)
- 比 outbox event(application 定義 schema)耦合度高很多
- 所以業界轉向 outbox-first,CDC 只用於 legacy / cross-team integration

**Reference**: [Debezium docs](https://debezium.io/documentation/);[Kafka Connect docs](https://kafka.apache.org/documentation/#connect)

### 🔽 5. Effectively-once delivery — outbox + idempotent consumer

**完美 exactly-once 不存在**(分散式系統理論證明 — Two Generals’ Problem)。Outbox + idempotent consumer = **effectively-once**,實務上等同 exactly-once。

**為什麼會 at-least-once?**
- Relay 推 Kafka 成功但更新 processed_at 失敗 → 下次 relay 再推一次
- Kafka 端 redelivery(consumer crash before ACK)

**Idempotent consumer 設計**
1. **DB UNIQUE constraint** + `ON CONFLICT DO NOTHING` — 我 booking_monitor 用 order_id 作 unique
2. **Processed event log** — consumer 自己有 `processed_events` table,每處理一個 event id 寫進去,重複收到先 check
3. **State-based idempotency** — compensating action 設計成可重複跑(`HINCRBY +qty` 配條件 check)

**TTL 上的取捨**
- Processed event log 不能無限保留(會爆),設 TTL(7-30 天)
- 但 Kafka retention 比 TTL 長 → 超出 TTL 後又收到 event → dup
- 解法:對齊 — TTL 跟 retention 一致 + 超期 event 視為 expired(DLQ)

**為什麼業界叫「effectively-once」?**
- 從 producer 端 — at-least-once delivery
- 從 consumer 端 — idempotent processing
- 兩者結合 — 業務 outcome 等同 exactly-once

**Reference**: [Effectively-Once Semantics in Pulsar / Kafka](https://flink.apache.org/2018/02/28/an-overview-of-end-to-end-exactly-once-processing-in-apache-flink-with-apache-kafka-too/);Stripe 的 idempotency post

### Q&A(必背)

### 🔽 Q: Outbox relay 怎麼決定 polling interval?

「Trade-off 三個維度:**latency vs DB load vs throughput**。

- 太短(50ms)— event 推送即時,但 DB 一直被 SELECT 騷擾,partial index 有救但 LSM cost 還是有
- 太長(10s)— DB 友好,但 saga / downstream 看到 event 延遲 10s
- 通常選 100ms - 1s

**Adaptive polling**(我 booking_monitor 想做但沒做):
- 有 event 時拉短到 50ms
- 連續空 N 次拉到 500ms / 1s
- Avoid hammering DB when idle

**Push 替代方案**:
- PG NOTIFY/LISTEN — INSERT 時 DB 自己通知 relay,zero polling
- 但要 wire 進 application,複雜度上去

我 booking_monitor 用固定 100ms polling — 簡單夠用,DB load 不大,改 adaptive 屬於 future optimization。」

📚 booking_monitor `internal/application/outbox/relay.go` `outboxPollInterval`

### 🔽 Q: 為什麼不直接用 PG NOTIFY/LISTEN 取代 polling?

「NOTIFY/LISTEN 是 PG 內建的 pub-sub,理論上零延遲。但有 4 個 production catch:

1. **每個 listener 占用一個 PG connection** — connection pool 緊張
2. **Payload 8KB 限制** — 通常只能傳 row id,還是要 SELECT 拿 detail
3. **Notifications 不持久化** — listener 暫時斷線就丟,跟 outbox「不丟 event」精神衝突
4. **跨 replica 不傳遞** — primary NOTIFY 不會傳到 read replica,scale 困難

實務的做法是 **NOTIFY + polling hybrid**:平常 polling(safety net),有 NOTIFY 時拉短 interval。我 booking_monitor 純 polling 因為:
- Relay 不是 latency-critical(saga 不在 user-facing path)
- 100ms polling 已經夠用
- 多一個 NOTIFY layer 增加複雜度沒明顯回報

但如果 user-facing 需要 sub-100ms event delivery,我會評估 hybrid。」

📚 [PG NOTIFY/LISTEN docs](https://www.postgresql.org/docs/current/sql-notify.html)

### 🔽 Q: Outbox 跟 Saga 怎麼配合?

「**Outbox 是 transport,Saga 是 protocol**。

- Outbox 解決「entity state 變化 + publish event 怎麼 atomic」— 這是 transport-layer concern
- Saga 解決「跨 service 的 long-lived transaction 怎麼 compensate」— 這是 protocol-layer concern

具體 flow(booking_monitor):
1. Payment fail webhook 進來
2. Application `MarkPaymentFailed()` — 同 tx 更新 `orders.status = 'failed'` + INSERT `events_outbox` (event_type=`order.failed`, payload={…})
3. Outbox relay 看到 unprocessed event → publish 到 Kafka `order.failed` topic
4. Saga compensator consume `order.failed` event → 反沖 Redis inventory + DB

Outbox 是 Step 2-3 機制,Saga 是 Step 4 邏輯。兩者解耦清楚:Saga 不知道 outbox 存在(它只 consume Kafka),Outbox 不知道 Saga 存在(它只負責 publish)。」

📚 booking_monitor `internal/application/outbox/` + `internal/application/saga/`

### 🔽 Q: 大流量場景,outbox 表會爆嗎?

「會。Outbox 每筆 entity 變化都新增一筆 row,高流量(10k+ TPS)幾天就幾億 row。三條應對:

1. **Aggressive cleanup** — 已 processed 的 row 定期 DELETE / 或 partition by month 然後 DROP old partition
2. **Time-series storage** — 把 outbox 從 PG 移到 ClickHouse / TimescaleDB(append-only friendly)
3. **CDC 替代** — 直接 read WAL,完全 skip outbox table

我 booking_monitor demo scale 沒踩到,但 production 我會選 partition + drop old(最簡單)。Stripe / Shopify 公開設計都用 partitioning。

**Cleanup 注意**:不能 `DELETE WHERE processed_at < N` 然後就完事 — 還要看 saga / downstream 是否真的處理完,可能要保留 N+M 期窗口應對 saga replay。」

📚 [PG Partitioning docs](https://www.postgresql.org/docs/current/ddl-partitioning.html);Stripe blog 的 outbox cleanup discussion

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我熟 Debezium」** — 沒架過 Kafka Connect + Debezium connector
- ❌ **「我會 outbox partition」** — booking_monitor 沒做過 partition,只是 conceptually 懂
- ❌ **「我做過 multi-region outbox replication」** — 沒做過跨 region scale 的 outbox
- ✅ 安全 phrasing:**「I built a transactional outbox with PG advisory lock for leader election + partial index for efficient polling in booking_monitor. I understand the dual-write problem deeply and know when CDC (Debezium) becomes preferable, though I haven’t operated Debezium in production.」**

### 📚 References — Transactional Outbox must-read

- [Microservices.io Transactional Outbox](https://microservices.io/patterns/data/transactional-outbox.html) — Chris Richardson 寫,最完整 pattern 介紹
- [Confluent Outbox Pattern blog](https://developer.confluent.io/courses/microservices/the-transactional-outbox-pattern/) — 業界 reference
- [Debezium Outbox Event Router](https://debezium.io/documentation/reference/stable/transformations/outbox-event-router.html) — CDC 替代方案完整 docs
- [Yves Reynhout - Outbox Pattern](https://www.cqrs.nu/) — CQRS context 下的 outbox
- booking_monitor `internal/application/outbox/relay.go` + migration 0007 partial index

---

### Multi-layer Idempotency ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「Idempotency 是『同 request 重複呼叫,結果一致』。我 booking_monitor 做了**四層 idempotency**,defense in depth:**Layer 1 API** — client 帶 `Idempotency-Key` header,N4 我加 Stripe-style fingerprint validation(同 key 不同 body → 409 conflict);**Layer 2 Worker** — `orders.id` UUID v7 唯一 + DB UNIQUE constraint,`ON CONFLICT DO NOTHING` 擋 Kafka redelivery;**Layer 3 Saga** — Redis `SETNX saga:event_id` 24h TTL,擋 compensate redelivery;**Layer 4 Gateway** — 對 Stripe API 用 `order_id` 當 idempotency key,Stripe-side 同 key 返回同 PaymentIntent。每一層獨立,任一層失靈其他層擋。」
> 

### 📊 booking_monitor 四層 idempotency 圖

```mermaid
graph LR
    C[Client] -->|Idempotency-Key: abc| L1[Layer 1 API<br/>fingerprint validation]
    L1 -->|same key + same body| OK1[return cached response]
    L1 -->|same key + diff body| C409[409 conflict]
    L1 -->|new key| W[Worker]
    W -->|order_id UUIDv7| L2[Layer 2 DB<br/>UNIQUE ON CONFLICT]
    L2 -->|already exists| Skip[skip]
    L2 -->|insert ok| OK[order created]
    OK -.failure.-> L3[Layer 3 Saga<br/>Redis SETNX saga:event_id]
    L3 -->|first time| Comp[compensate]
    L3 -->|already processed| Ack[ACK skip]
    OK -.pay.-> L4[Layer 4 Stripe<br/>Idempotency-Key: order_id]
    L4 -->|same order_id| Same[return same PaymentIntent]
```

### 🔄 必背深度展開

### 🔽 1. Stripe-style fingerprint validation — 同 key 不同 body 怎麼處理?

**問題**:單純 `Idempotency-Key` cache 容易被誤用 — client 拿同一個 key 但 payload 不同重發(bug 或 attack),naive 實作會回傳 cached response = wrong outcome

**Stripe 設計** ([engineering blog](https://stripe.com/blog/idempotency))
1. 第一次 POST 帶 `Idempotency-Key: abc-123` + body `{...}`
2. Server 算 body 的 fingerprint(SHA256 hash of canonicalized body)
3. 寫進 cache:`abc-123 → {fingerprint, response, status}`
4. 第二次 POST 帶同 key:
- body fingerprint 一致 → return cached response ✅
- body fingerprint 不同 → **409 Conflict**(Stripe 用 `idempotency_error` code)
5. TTL 過後 cache 清,同 key + 同 body 也會重跑(refresh)

**為什麼這設計重要?**
- 擋 client bug — bug 改了 body 但忘了換 key
- 擋 attack — 重放 attack 帶不同 body 想 confuse server
- 符合 RFC 7231 idempotent 定義 — 同 request 同結果

**Body canonicalization 細節**
- JSON key 要排序(`{"a":1,"b":2}` 跟 `{"b":2,"a":1}` 算同 body)
- 空白要正規化(`{ "a" : 1 }` 跟 `{"a":1}` 同)
- 否則 hash 不一致

**Migration to fingerprint**(N4 PR #48 我做的)
- 已 cached 的 legacy entry 沒 fingerprint → lazy migration(下次命中 + 算 fingerprint + 寫回)
- 不需要 schema migration,慢慢汰換

**Reference**: [Stripe Idempotency Doc](https://docs.stripe.com/api/idempotent_requests);booking_monitor `internal/infrastructure/api/middleware/idempotency.go`(N4 PR #48)

### 🔽 2. UUID v7 — 為什麼 booking_monitor 從 v4 換 v7?

**UUID v4** — 完全 random,128 bits 都隨機
- ✅ collision-free
- ❌ B-tree index insert 是 random position → page split 頻繁、cache locality 差

**UUID v7** — 前 48 bits 是 unix timestamp(ms 精度),後 80 bits random
- ✅ 時序性 — 新 UUID > 舊 UUID,B-tree 永遠 append to right
- ✅ 仍 collision-free(80 bits random space ~10^24,生日攻擊機率天文)
- ✅ 帶時間資訊(從 UUID 反解 → 知道大概什麼時候 mint 的)

**為什麼 B-tree append 比 random insert 快很多?**
- Random insert 觸發中間 page split,page split 要 lock + 重排
- Append 永遠是 rightmost leaf,**hot page 在 cache 內**,zero page split
- **Reference benchmark**:Ardent Performance Computing 1M row INSERT 測試 — v4 ~2,670 TPS / v7 ~3,420 TPS / bigint ~3,480 TPS([source](https://ardentperf.com/2024/02/03/uuid-benchmark-war/))。**我 booking_monitor 沒做專屬 v4 vs v7 對比 benchmark**,而是直接以 v7 設計 schema(decision 2026-04-25,參考 Ardent + RFC 9562)

**Library 選擇** ([googleuuid](https://github.com/google/uuid))

```go
import "github.com/google/uuid"
id, _ := uuid.NewV7()  // 直接內建,Go 1.21+
```

**履歷怎麼老實講?**
- 「Designed booking_monitor’s entity schema with UUID v7 primary keys based on RFC 9562 + Ardent Performance Computing benchmark research (v7 matches bigint within 2% on INSERT throughput, v4 ~28% slower). Embedded ms-timestamp aligns naturally with `ORDER BY created_at` pagination.」
- 注意:**借 third-party benchmark**(誠實標注),不是自己測的

**Reference**: [RFC 9562 UUID v7](https://datatracker.ietf.org/doc/html/rfc9562);[Buildkite UUID v7 explanation](https://buildkite.com/blog/goodbye-integers-hello-uuids)

### 🔽 3. DB UNIQUE + ON CONFLICT — 最強的 idempotency layer

**為什麼 DB UNIQUE 是最終防線?**
- Application 層的 idempotency 都可能失效(cache 過期、Redis 掛、process 重啟丟記憶體)
- DB UNIQUE constraint 是 **persistent + atomic + race-free** — 即使 N 個 goroutine 同時 insert 同 key,只有一個成功
- 走 ACID,不需要相信任何 application code

**ON CONFLICT 兩種寫法**

```sql
-- 1. DO NOTHING — 撞到就 skip,適合「重複 insert 不算錯」
INSERT INTO orders (id, ...) VALUES ($1, ...)
ON CONFLICT (id) DO NOTHING;

-- 2. DO UPDATE — 撞到就 update,適合「我要 upsert」
INSERT INTO orders (id, status) VALUES ($1, 'paid')
ON CONFLICT (id) DO UPDATE
   SET status = EXCLUDED.status,
       updated_at = NOW()
 WHERE orders.status != 'paid';  -- 加 WHERE 確保不會 downgrade state
```

**RETURNING 用法 — 知道有沒有真插**

```sql
INSERT INTO orders ... ON CONFLICT (id) DO NOTHING RETURNING id;
```

- 有返回 id → 真插入了(新 order)
- 沒返回 row → conflict,skip 了(idempotent miss)

**Composite UNIQUE 處理「多欄位」**
- 例:`UNIQUE (user_id, event_id, idempotency_key)` 確保同 user 同 event 同 key 只成功一次
- 更靈活,適合 fine-grained idempotency

**Pitfall**:UNIQUE constraint 過大會減 insert 速度,因為 index 維護 cost。**只在必要欄位上加**,不要無腦多欄位 unique。

**Reference**: [PG INSERT ON CONFLICT docs](https://www.postgresql.org/docs/current/sql-insert.html#SQL-ON-CONFLICT);booking_monitor `internal/infrastructure/persistence/postgres/repositories.go`

### 🔽 4. 為什麼 saga 需要獨立 idempotency layer?

**錯誤思考** — 「我 DB 已經 UNIQUE 了,saga 不需要再做 idempotency」

**為什麼錯?** Saga compensation 的 idempotency 不是「DB write 層的 dup」,而是「**business action 層的 dup**」。

**舉例 booking_monitor compensate**
1. Saga 收到 `order.failed` event
2. Compensate:`order.MarkCompensated()` + Redis `revert.lua` (inventory +qty)
3. ACK Kafka

**fail mode**
- Step 2 成功 → Step 3 fail(network blip 沒 ACK)
- Kafka redelivery → 再來一次
- 沒擋的話 → Redis +qty 跑兩次 = 多退一張票

**Layer 解法**
- DB 層擋不了 — `MarkCompensated()` 是 idempotent SQL transition(`WHERE status = 'failed'`),第二次撞到 `ErrInvalidTransition`,但 **Redis revert.lua 還是會被呼叫**
- Saga 層 SETNX `saga:event_id` 24h:第一次拿到 set,跑 compensate;第二次拿不到,直接 ACK skip

**TTL 怎麼定?**
- 太短(5min)— Kafka 長 retention 期間還可能 redeliver,擋不住
- 太長(7 天)— Redis 浪費記憶體
- 我設 24h — 涵蓋 Kafka redelivery 高頻區間,又不太佔記憶體

**Reference**: booking_monitor `internal/application/saga/compensator.go`

### 🔽 5. Gateway-side idempotency — 跟 Stripe 配合

**Stripe API 提供 Idempotency-Key header**
- POST `/v1/payment_intents` 帶 `Idempotency-Key: order_id`
- Stripe-side cache 24h:同 key + 同 body → 返回 cached 同 PaymentIntent
- 同 key + 不同 body → 409 conflict(跟我們自己 API 一致)

**為什麼 client(我)還要做這層?**
- 我 retry Stripe call 時可能因為 network blip 不知道 first call 成功了
- 不帶 idempotency key → Stripe 真的會幫我建第二個 PaymentIntent(收兩次錢)
- 帶 `order_id` 當 idempotency key → Stripe 自己擋

**Key 怎麼選?**
- 用 business id(`order_id`)而不是 random — random key 每次 retry 都不一樣,擋不住
- 用 `order_id` → 同 order 不論 retry 幾次,Stripe 都返回同 PaymentIntent

**TTL alignment**
- Stripe 預設 24h idempotency cache
- 我 booking_monitor 內部 retry budget 設 ≤ 24h
- 對齊很重要 — 超過 Stripe TTL 後 retry,Stripe 真的會 mint 新 PaymentIntent

**真實事故案例(Stripe blog)**
- 客戶端不帶 idempotency key,網路 blip 後 retry → 重複扣款
- Stripe engineering 公開分析:idempotency key 是 client 的責任,不是 server 的義務
- 我 booking_monitor D4 design 就把這個寫在程式 invariant

**Reference**: [Stripe API Idempotent Requests](https://docs.stripe.com/api/idempotent_requests);booking_monitor `internal/infrastructure/payment/stripe.go`

### Q&A(必背)

### 🔽 Q: 一個 layer 夠了嗎為什麼要四層?

「Defense in depth。每一層擋不同 attack surface:

- **Layer 1 (API)** — 擋 client 重複 POST(network retry / user 連點)
- **Layer 2 (DB UNIQUE)** — 擋 Kafka redelivery / worker crash recovery
- **Layer 3 (Saga Redis SETNX)** — 擋 saga compensate redelivery(business action 層的 dup)
- **Layer 4 (Gateway)** — 擋 client 對 Stripe API 重複 POST

任一層失靈,其他層還在。例:Redis 掛了 → Layer 3 失效,但 Layer 2 DB UNIQUE 還在;DB schema 部署過程中 UNIQUE 沒生效 → Layer 1 + 3 還在。

**反過來:單一層就夠嗎?**
- 只有 Layer 1 → API restart 後 cache 沒了,client retry 看到 dup
- 只有 Layer 2 → API 沒擋,client 看到不同 response code(409 vs 200),體驗差
- 只有 Layer 3 → compensate 之前已經 DB write 了,擋不住主流程 dup

**Production rule**:idempotency 是 system property,不是 component property。每個 entry point 都該擋。」

📚 [Stripe Engineering Idempotency](https://stripe.com/blog/idempotency)

### 🔽 Q: Idempotency key TTL 應該設多久?

「Trade-off — 太短會擋不住長距離 retry,太長浪費資源。

**業界 reference**:
- Stripe — 24h(idempotency cache)
- AWS API Gateway — 5min(同 method 重複)
- Square — 60s(payment API)
- Adyen — 7 days

**選擇邏輯**:
1. Client retry 最長窗口 — 至少蓋到這個時間
2. Storage budget — Redis 容量 vs key 數量
3. Business safety net — 太短會錯擋,太長浪費

**我 booking_monitor**:
- Layer 1 (API) — 24h(對齊 Stripe 慣例)
- Layer 3 (Saga) — 24h(Kafka retention 7 天但實際 redelivery 集中在前 24h)

**Anti-pattern**:設 infinite TTL — Redis 永遠記住,記憶體不夠;就算夠,client bug 重用同 key 永遠擋住 = 業務 stuck。一定要有 TTL。」

📚 [Stripe idempotency docs - TTL section](https://docs.stripe.com/api/idempotent_requests)

### 🔽 Q: 如果同 key 但 body 不同的 request 進來,要怎麼回?

「Stripe 設計 — **409 Conflict** 加上 specific error code(`idempotency_error`)。我 booking_monitor N4 PR #48 也走這個。

具體 response:

```json
{
    "error": "idempotency_conflict",
    "message": "Idempotency-Key reused with different request body. Use a new key or send the original body."
}
```

**為什麼 409 不是其他 code?**
- 400 (Bad Request) — 太 vague,client 不知道是 idempotency 問題
- 422 (Unprocessable Entity) — body 本身沒問題,只是 conflict
- 409 (Conflict) — 語意最準確 — 「server-side state conflicts with this request」

**Logging level**:Error(這是 client side bug 或 attack,需要 alert)。

**Recovery 建議**:client 應該用新 key 重發,或如果 retry 是 intent → 確認 body 跟 first call 完全一致。」

📚 booking_monitor `internal/infrastructure/api/middleware/idempotency.go`

### 🔽 Q: 你 outbox 跟 idempotency 怎麼配?

「Outbox 解決『 entity state + event publish atomic』,idempotency 解決『重複呼叫 outcome 一致』,兩者互補。

具體 flow:
1. Client POST `/api/v1/book` 帶 `Idempotency-Key: abc`
2. Layer 1 idempotency 擋住已處理 → return cached response
3. 沒擋(new key)→ 進 BookingService
4. BookingService 在 tx 內 INSERT orders + INSERT events_outbox
5. Tx commit
6. Outbox relay 撈 event → publish Kafka
7. Saga 端 Layer 3 idempotency 擋 redelivery

**注意**:Layer 1 cache 要在 BookingService **成功 commit 後**才寫,失敗不寫。否則 client retry 看到 cached error response 永遠走不通。

**N4 fingerprint 細節**:Layer 1 cache 寫入時:`{idempotency_key, body_fingerprint, response, status}`,撞到要驗 fingerprint 一致才返回 cache。」

📚 booking_monitor `internal/application/booking/service.go` + `internal/infrastructure/api/middleware/idempotency.go`

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我設計過 distributed idempotency service」** — 你的 idempotency 在 single Redis,沒跨 region / multi-instance
- ❌ **「我會 idempotency key migration 跨 schema 版本」** — 沒做過 major version 變更
- ✅ 安全 phrasing:**「I designed multi-layer idempotency in booking_monitor (API fingerprint + DB UNIQUE + Saga Redis SETNX + Gateway Stripe key). I understand the patterns from Stripe’s engineering blog and applied them with TTL alignment.」**

### 📚 References — Idempotency must-read

- [Stripe Engineering Idempotency](https://stripe.com/blog/idempotency) — 最權威 reference
- [Stripe API Idempotent Requests](https://docs.stripe.com/api/idempotent_requests) — 實作細節
- [RFC 9562 UUID v7](https://datatracker.ietf.org/doc/html/rfc9562) — 時序 UUID 設計
- [PG INSERT ON CONFLICT](https://www.postgresql.org/docs/current/sql-insert.html#SQL-ON-CONFLICT) — DB 層 idempotency 必備
- [Square Idempotency Guide](https://developer.squareup.com/docs/working-with-apis/idempotency) — 另一個 reference 實作

---

### LLM Agent Engineering(text2sql) ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「commeet-text2sql 是我單人 build 的 production LLM agent for natural-language → SQL,3 個月內 36 個 PR,base 在 research-grounded foundation:**CHESS / BIRD / MAGIC / MAC-SQL / LITHE EDBT’26 / E3-Rewrite**。Pipeline 是多 stage:**Intent classifier → Column Pruner → ReAct loop → EXPLAIN-gated validation**,stages 跑 parallel via `errgroup`,每個 stage 採 fails-open(失敗回 fallback 路徑而非 hard error)讓 happy path 維持 resilient。最值得提的兩個設計是:**cost-driven SQL rewrite + shape-preservation guard**(regex fingerprint 擋 DISTINCT drop / JOIN type change / WHERE drop / aggregation change,gate 在 ≥20% cost improvement + shape match 才採用)+ **4-layer silent table-substitution defense**(table allowlist + RAG context check + SQL parser walk + EXPLAIN gate),audit 發現有 generation 能通過初步 validation 但 reference 到 RAG scope 外的 table 之後加上去的。」
> 

### 📊 text2sql Pipeline

```mermaid
graph LR
    UQ[User Query] --> IC[Intent classifier]
    IC --> CP[Column Pruner<br/>CHESS-inspired]
    CP --> RA[ReAct loop<br/>budget-exhaust soft-fail]
    RA --> CG[Cost-driven rewrite<br/>+ shape guard]
    CG --> EG[EXPLAIN gate]
    EG --> SQL[Validated SQL]
    EG -.raw DB error.-> RA
    CP -.fails open.-> RA
    style CP fill:#fef
    style CG fill:#fef
    style EG fill:#fef
```

每個 stage 失敗都 fall back 到下一個合理路徑,不是 hard error。

### 🔄 必背深度展開(verified from resume_en.md + research papers)

### 🔽 1. Multi-stage pipeline — 為什麼這樣切?

**Intent classifier** — first gate
- 分類 user query 屬於哪類(expense / approval / user-access / doc / other 等)
- 不同類用不同 RAG retrieval scope + prompt template
- ⚠️VERIFY 真實有幾個 class(resume 沒明寫;若你要在面試講具體類別 + 數量,先 trace 你 commeet-text2sql repo)

**Column Pruner** — CHESS-grounded
- CHESS 是 Stanford / Carnegie Mellon 等 2024 的 research(arxiv.org/abs/2405.16755),提出在 schema linking 階段做 column-level pruning 而非 table-level
- Resume verified:**+2% BIRD accuracy / 5× token reduction on large schemas**
- 為什麼?large enterprise schema 動輒幾百個 column,直接全餵進 LLM 會稀釋 attention + 燒 token

**ReAct loop**
- Reasoning + Acting (Yao et al., 2022, [arxiv.org/abs/2210.03629](https://arxiv.org/abs/2210.03629))
- LLM 在 reasoning trace + tool call 之間交替,每輪可 call tool(查 schema、跑 EXPLAIN、retrieve example)
- **Budget-exhaust soft-fail** — 超過 max turn 後不 hard fail,而是返回「best guess so far」+ raw error to caller,user 看到 graceful degradation 而非 500

**EXPLAIN-gated validation**
- 拿 generated SQL 直接 EXPLAIN(不執行)
- planner 報錯 → 把 raw DB error 餵回 ReAct loop 重試
- Auto-retry 是有 budget 的,連續失敗才放棄

**Reference**: [CHESS paper](https://arxiv.org/abs/2405.16755);[ReAct paper](https://arxiv.org/abs/2210.03629);[BIRD benchmark](https://bird-bench.github.io/)

### 🔽 2. Cost-driven SQL rewrite + shape-preservation guard(最值得講)

**Cost-driven rewrite 是什麼?**
- LLM 生 SQL 後,我再 prompt 一次 LLM「請 rewrite 這條 SQL 讓它更便宜(less IO / less buffer scan)」
- 拿 rewrite 後的 SQL 跟原 SQL 各跑 EXPLAIN 取 cost 估算
- **只有 ≥20% improvement 才採用 rewrite 版**(避免微小改動冒風險)

**Shape-preservation guard — 為什麼需要?**
- LITHE EDBT’26 + E3-Rewrite 論文都 warn:LLM rewriting 過程**可能 silently drop correctness**(例:把 DISTINCT 拿掉、把 LEFT JOIN 改 INNER JOIN、把 WHERE 條件丟掉、改 aggregation set)
- 表面 query 看起來「更便宜」,但**結果集不一樣** — 是 hidden correctness bug
- 我用 **regex-based fingerprint** 抽 SQL 的 structural shape(DISTINCT 數、JOIN type 序列、WHERE predicate 列表、GROUP BY column 集合),rewrite 前後 fingerprint 必須一致才 pass

**為什麼用 regex 不用 SQL parser?**
- Regex 是 **conservative fail-closed** — match 不到結構就拒絕,寧可錯擋(false positive)也不要 silent correctness drop
- SQL parser 路徑會把「奇怪但其實 OK 的 case」也 parse 過 → false negative(該擋的沒擋)
- 在 correctness > recall 的場景,regex fail-closed 是對的

**4 個 shape diff 規則(resume verified)**
1. DISTINCT drop — 原有 DISTINCT 不能拿掉
2. JOIN type change — INNER ↔︎ LEFT/RIGHT/FULL 不能換
3. WHERE-predicate drop — 任一 predicate 不能消失
4. Aggregation-set change — GROUP BY column / agg function 不能改

**Reference**: [LITHE paper EDBT’26](https://openproceedings.org/2026/conf/edbt/paper-93.pdf);[E3-Rewrite paper](https://arxiv.org/abs/2404.16487);commeet-text2sql repo(internal)

### 🔽 3. 4-layer silent table-substitution defense(這題深入面試官會印象深刻)

**問題場景** — Audit 發現 LLM generate 的 SQL 可能 reference 到 user RAG-retrieved scope 外的 table
- 例:user 問 expense 相關但 LLM 生成 query 摻到 audit_log 表(因為某個 column name 同名)
- 是 multi-tenant + sensitive data 場景下的 cross-scope leak vector
- 一層 validation 漏網就會被 production 用到

**4-layer defense(resume verified)**

1. **Table allowlist** — generation prompt 直接附帶「the only tables you’re allowed to reference are X / Y / Z」,LLM 還是會偶爾跨界,但勝率減少
2. **RAG context check** — 取 RAG retrieved schema,把 generated SQL 的 table mention 跟 retrieved scope 對照,不在 scope 內直接 reject
3. **SQL parser walk** — 用 SQL parser 抽出真實 table reference(AST walk),不靠 regex(regex 會被 alias / subquery 騙)
4. **EXPLAIN gate** — 即使前 3 層 pass 了,EXPLAIN plan 顯示走某張表,該表如果不在 expected list,reject

**每層解決不同 attack vector**
- Layer 1 — soft 防守(LLM 行為層)
- Layer 2 — semantic 防守(intent vs scope mismatch)
- Layer 3 — syntactic 防守(string-level 看到的 table 名)
- Layer 4 — runtime 防守(planner 實際打哪張表)

**面試 punchline**:「Audit 發現有 generation 能通過初步 validation 但 reference 到 RAG retrieved scope 外的 table 之後,我加了這 4 層 — 設計理由是 defense in depth,任一層失靈其他層擋」

**Reference**: commeet-text2sql 4-layer defense 是 resume line 46 verified

### 🔽 4. SSE streaming + cooperative deadline chain(D7 跟 Cooperative Deadline Chain stack 互補)

**SSE streaming workflow trace**
- 每個 agent stage 進行中時即時 push progress 到 frontend(用 Server-Sent Events)
- User 看到 「Intent classifier ✓ → Column Pruner ✓ → ReAct turn 1 …」 real-time
- 比 long-poll / WebSocket 簡單(unidirectional + HTTP-based)

**Cooperative deadline chain — 從 HTTP 一路傳到 PostgreSQL**
- HTTP middleware 起 deadline → orchestrator 收 ctx → GORM call 帶 ctx → pgx 帶 ctx → PG `statement_timeout` 用 libpq options 連線時設
- Deadline hierarchy 嚴格遞減,讓最 outer handler 永遠有 headroom 寫 504 response

**詳細 deep-dive 看 Part 8 → Cooperative Deadline Chain 那條**(避免重複,獨立一段)

### 🔽 5. Operational surface(commit log / audit / KB curation)

Resume verified 的 operational pieces:
- **`query_event` audit log** — 每筆 generation 都寫一筆 audit row(user / query / generated SQL / outcome / latency)
- **`/internal/*` admin API** — K8s-internal,log-aggregator accountability,KB curation 用
- **`kb-tool` CLI** + Makefile wrappers — 給 PM / admin 跑 KB 維護

⚠️ VERIFY:具體 audit row schema、admin API endpoint 列表、kb-tool subcommand 列表 — 你的 commeet-text2sql repo 知道實際內容,面試前 trace 一次

### Q&A(必背)

### 🔽 Q: 為什麼選 ReAct 不選 plan-and-execute 或 multi-agent?

「ReAct 的核心是 **reasoning + acting 交替**(Yao 2022),適合 query 需要動態探索 schema 的場景。Plan-and-execute(Wang 2023)先全 plan 再執行,適合**步驟可預先列出**的場景。Multi-agent(MAGIC、MAC-SQL)是把不同責任拆給專業 sub-agent。

我的選擇是 **ReAct + 局部 sub-step 結構** — 不是 pure ReAct 不是純 plan-and-execute。理由:
- text2sql 的 happy path 步驟可以前置(schema link → SQL gen → validate),但 error recovery 需要動態(planner 回 error 後重新 reason)
- Plan-and-execute 在 error recovery 場景重新 plan 成本高
- ReAct soft-fail 配 budget exhaustion 後 fall back,user 看到 graceful degradation

我**不**用 multi-agent — 因為 single Go process 拆 sub-agent 增加 IPC complexity,沒有單一 LLM 拆任務拿不到的能力差。」

📚 [ReAct paper](https://arxiv.org/abs/2210.03629);[Plan-and-Execute paper](https://blog.langchain.dev/planning-agents/);[MAGIC paper](https://arxiv.org/abs/2406.12692)

### 🔽 Q: Shape guard 用 regex 不用 SQL parser 不會出問題?

「會,但**選 regex 是 conservative trade-off**。SQL parser 路徑會把『奇怪但其實 OK 的 case』parse 過 → false negative(該擋的沒擋);regex fail-closed 會擋掉一些其實 safe 的 rewrite → false positive(該採用的沒採用)。

在 text2sql 跨多 tenant + multi-language SQL syntax 的場景,**correctness > recall**:寧可少採用幾個有效 rewrite,也不能放過一個 silent correctness drop。Production 客戶看到「query 結果跟之前不一樣」會比「LLM 沒給我最快版本」嚴重很多。

實作上 — regex 抽 SQL fingerprint(DISTINCT count / JOIN type sequence / WHERE predicate list / GROUP BY column set),rewrite 前後 fingerprint mismatch 就 reject。這個 fingerprint design 我覺得有討論空間,LITHE / E3-Rewrite 兩篇 paper 也 warn 過 LLM rewriting 的 correctness 風險,我這個是更保守的應對。」

📚 [LITHE EDBT’26](https://openproceedings.org/2026/conf/edbt/paper-93.pdf);[E3-Rewrite paper](https://arxiv.org/abs/2404.16487)

### 🔽 Q: 你 RAG retrieval 用什麼 embedding + vector store?

「embedding 用 OpenAI text-embedding API(我加了 selective retry: 429/5xx + exponential backoff + ctx-aware sleep)。vector store 用 pgvector(PostgreSQL extension)— 因為我們已經有 PG infrastructure,引入新 vector DB 是 ops 負擔。

pgvector trade-off:
- ✅ 不引入新 stateful service,直接 leverage 既有 PG
- ✅ vector + business data join 容易(WHERE 條件可帶 company_id filter)
- ❌ 純粹 vector workload 效能不如 Pinecone / Weaviate / Milvus
- ⚠️ 但對 < 1M vector + multi-tenant filter 重的場景,pgvector 是甜蜜點

我 commeet-text2sql 場景:schema embedding 數百 row 規模 + 多 company tenant filter → pgvector 完美 fit。」

📚 [pgvector docs](https://github.com/pgvector/pgvector);[OpenAI Embeddings](https://platform.openai.com/docs/guides/embeddings)

### 🔽 Q: text2sql 怎麼處理 hallucination?

「四條防線:

1. **Schema-grounded prompting** — RAG retrieve 真實 schema 餵進 prompt,LLM 不能憑空編 column / table
2. **EXPLAIN gate** — generated SQL 直接走 planner,不存在的 identifier 立刻被擋
3. **4-layer table-substitution defense**(resume verified)— 即使 EXPLAIN pass 也要驗 table 在 user retrieved scope 內
4. **Cost + shape guard** — rewrite 階段不能 silently drop correctness

整體哲學:**LLM is non-deterministic, validation must be deterministic**。我把 LLM 當 candidate generator,真正 ship 給 user 的 SQL 都過了 deterministic validation gate。」

📚 [Stanford CHESS paper](https://arxiv.org/abs/2405.16755) — schema-grounded approach 起源

### 🔽 Q: 你怎麼測這個 agent 的 production quality?

「**Offline eval** — BIRD benchmark + 我們自己客戶 ground truth(real queries from analytics request)。BIRD accuracy 我看 Column Pruner 加 +2%(resume verified)。

**Online eval** — `query_event` audit log 紀錄每筆 generation outcome,定期手抽 sample 看 generation quality + manually verify(PM 偶爾 verify 一批 queries,反向 update knowledge base)。

⚠️ VERIFY:你 commeet-text2sql 有沒有 hourly / daily 自動 eval pipeline?有沒有 production accuracy 數字?我不知道你具體生產 monitor 形狀,**面試前 trace 你 repo 確認 eval methodology**。」

📚 [BIRD benchmark](https://bird-bench.github.io/);commeet-text2sql `query_event` audit table(resume verified)

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我跑過 BIRD eval 拿到 X% accuracy」** — Column Pruner +2% BIRD 是 CHESS 論文的 finding(我引用),不是我自己 benchmark 過 commeet 內 corpus
- ❌ **「我 4-layer defense 在 production 擋了 N 次」** — resume 寫「audit 發現後加」,沒寫擋到多少;不要 inflate 成 「N 次 production 攔截」
- ❌ **「我有 fine-tune 自己的 LLM」** — 沒做過,我是 prompt engineering + tooling layer,base model 是用 Claude / OpenAI API
- ❌ **「query_event log 我每天看」** — ⚠️ VERIFY 真實 cadence;沒 hands-on production monitor 別吹
- ✅ 安全 phrasing:**「I architected commeet-text2sql with multi-stage pipeline grounded in CHESS / BIRD / LITHE / E3-Rewrite research. 36 PRs in 3 months solo. Production-grade with 4-layer table-substitution defense + shape-preservation guard. I leverage research findings (CHESS +2% BIRD on Column Pruner) but haven’t independently benchmarked my variant on BIRD.」**

### 📚 References — text2sql / LLM Agent must-read

- [CHESS paper](https://arxiv.org/abs/2405.16755) — Column Pruner 起源
- [ReAct paper](https://arxiv.org/abs/2210.03629) — Yao 2022 ReAct reasoning + acting
- [BIRD benchmark](https://bird-bench.github.io/) — text2sql 業界 benchmark
- [MAGIC paper](https://arxiv.org/abs/2406.12692) — Multi-agent text2sql
- [MAC-SQL paper](https://arxiv.org/abs/2312.11242) — Multi-agent collaborative
- [LITHE EDBT’26](https://openproceedings.org/2026/conf/edbt/paper-93.pdf) — SQL rewrite correctness warning
- [E3-Rewrite paper](https://arxiv.org/abs/2404.16487) — SQL rewrite framework + correctness considerations
- [pgvector](https://github.com/pgvector/pgvector) — Postgres vector extension

---

### Cooperative Deadline Chain ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「Cooperative deadline chain 是把 ctx-based deadline 從最 outer 的 HTTP layer 一路嚴格遞減傳到 PostgreSQL `statement_timeout`,讓每一層都看到自己被分配的 budget,任何一層 deadline 到期前 cancel 信號往內傳得很乾淨。Resume verified — 我 commeet-text2sql 把 SSE streaming workflow trace 跟這條 deadline chain 串起來(HTTP → PostgreSQL)。**設計直覺**:最 outer handler 永遠要有 headroom 寫 504 response,所以 DB-side timeout 必須早於 app middleware,app middleware 必須早於 transport layer。」
> 

### 🔄 必背深度展開

### 🔽 1. Deadline hierarchy — 為什麼必須嚴格遞減?

**反例(初學者寫法)**:不設 hierarchy,全靠 ctx propagation
- 結果:DB query 可能跑到 transport layer 已經被 client kill,handler 還在等
- handler 看到 ctx.Err()=DeadlineExceeded 但已經沒時間做 graceful cleanup + 寫 response

**對的 hierarchy(示意,⚠️VERIFY 你的真實數字)**

```
transport layer timeout   >  app middleware budget    >  DB statement_timeout
  e.g. 120s                  e.g. 90s                   e.g. 30s
```

- 用意:DB-side 最早 timeout → query 被 PG kill → app middleware 仍有時間收結果寫 graceful 504 → transport 也來得及 flush response

**為什麼是 cooperative(不是 forceful)?**
- Go 沒有 force-kill goroutine 的 API,只能用 ctx 「請對方自己退場」
- 每一層 SQL / HTTP call / LLM call 都要 honor ctx — `db.WithContext(ctx)` / `http.NewRequestWithContext` / openai SDK 配 ctx
- 一旦中間某層忘了 propagate ctx → 那層之後變 zombie,deadline 失效

⚠️ **VERIFY**:你 commeet-text2sql 實際設多少 timeout 在哪一層 — 上面是教科書示意,你要對你自己 codebase

**Reference**: [Go blog Context](https://go.dev/blog/context);[PG statement_timeout docs](https://www.postgresql.org/docs/current/runtime-config-client.html#GUC-STATEMENT-TIMEOUT)

### 🔽 2. PostgreSQL statement_timeout — 怎麼設?

**3 種設法**(由 short-lived 到 long-lived)

1. **Per-statement**(在 query 前 SET)

```sql
SET LOCAL statement_timeout = '30s';
SELECT ...;
```

- LOCAL 限制 timeout 只在當前 transaction 有效
- 每個 query 都要 SET 一次,容易漏
1. **Per-connection options**(在 libpq connect string)

```
postgres://user:pass@host/db?statement_timeout=30000
```

或在 connection pool 配置中設`SET options`。
- pgx / GORM 連線時自動帶,後續 query 都 inherit
- 我比較推這個 — 不容易漏

1. **Per-role / per-database**(server-side default)

```sql
ALTER ROLE app_user SET statement_timeout = '30s';
```

- 操作上負擔最小,但跨應用共享 role 時不靈活

**注意:這只 cover query 的 server-side 執行時間,不 cover network round-trip**
- 你 ctx WithTimeout 在 client 端是 transport-level
- statement_timeout 是 server-side
- 兩者通常都要設,client 設稍大避免 client timeout 比 server 早

**Reference**: [PG statement_timeout](https://www.postgresql.org/docs/current/runtime-config-client.html);[pgx connect string options](https://pkg.go.dev/github.com/jackc/pgx/v5#hdr-Establishing_a_Connection)

### 🔽 3. context.Canceled vs context.DeadlineExceeded — HTTP code 怎麼回?

**Canceled** — client 主動 disconnect / 上層 cancel
- HTTP code 對應 **499 Client Closed Request**(nginx convention,非 RFC standard)
- 業務含義:user 還沒等到結果就走了
- handler 通常不需要做 cleanup(client 已不在乎)

**DeadlineExceeded** — server-side deadline 到期
- HTTP code 對應 **504 Gateway Timeout**
- 業務含義:你給自己設的 budget 用完
- handler 要 graceful 寫 504 response + audit log + 啟動 fallback(若有)

**Go code 分辨**

```go
if errors.Is(ctx.Err(), context.Canceled) {
    // 499 — client gone
} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
    // 504 — server-side timeout
}
```

⚠️ **VERIFY**:你 commeet-text2sql 是否真的分辨 499 vs 504?有些 framework 直接全回 504。

**Reference**: [Go context docs](https://pkg.go.dev/context);[nginx 499 explained](https://www.nginx.com/blog/nginx-noteworthy-features-supported-by-version-1-9-15/)

### Q&A(必背)

### 🔽 Q: 為什麼要 cooperative 而不直接 force-kill?

「Go 沒有 force-kill goroutine 的 API — `runtime` 故意設計成這樣,因為 force-kill 會 leak resource(open file / DB conn / mutex 持有狀態)。

替代設計:cooperative。每個可能 long-running 的 call 都接受 ctx,自己定期檢查 ctx.Done()。例:`db.QueryContext(ctx, ...)` 內部會 send query 後輪詢 ctx,timeout 到就 send PG cancel request。

實作代價:每一層都要 honor ctx,中間有一層忘了傳 → leak。所以 codebase scale 後要有 lint rule 防止 forget。我 commeet-text2sql 過 review 流程把 ctx-less call 都改掉。」

📚 [Go context blog](https://go.dev/blog/context)

### 🔽 Q: SSE streaming + deadline chain 怎麼 work?

「SSE 是 HTTP-based unidirectional streaming(server → client),用 `Content-Type: text/event-stream` + 每個 event 用 `data: ...\n\n` 分隔。對 LLM agent 場景:每個 stage 進度即時推到 client,user 看 real-time progress 而不是等十幾秒一次性回應。

跟 deadline chain 配合:
- HTTP handler 起 deadline → 寫到 ctx
- Agent pipeline 各 stage 收 ctx → ctx canceled 立刻 stop
- SSE 本身的 connection 保持機制 — Go `http.Flusher` 每寫一個 event 就 flush,client 立刻看到
- ctx canceled 後 handler 寫一個 final event(`event: error\ndata: timeout\n\n`)再關 connection

我 commeet-text2sql 這條 pipeline 端到端 ctx propagation 是 design decision 也是 review checklist — resume line 47 提到的就是這個結構。」

📚 [MDN SSE docs](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「30s / 90s / 120s 是我 production 設的值」** — 我這份 doc 是示意值,⚠️VERIFY 你 commeet-text2sql 真實設多少
- ❌ **「我發現了 cooperative deadline 這個 pattern」** — 是 Go context package 內建設計概念,我做的是把它端到端串好,不是發明
- ✅ 安全 phrasing:**「I designed an end-to-end cooperative deadline chain in commeet-text2sql from HTTP middleware → orchestrator → GORM → pgx → PostgreSQL statement_timeout. Resume-verified part: HTTP-to-PG deadline chain wired with SSE streaming workflow trace.」**

### 📚 References — Cooperative Deadline must-read

- [Go context blog](https://go.dev/blog/context) — official deadline propagation
- [PG statement_timeout docs](https://www.postgresql.org/docs/current/runtime-config-client.html) — server-side timeout
- [pgx connection options](https://pkg.go.dev/github.com/jackc/pgx/v5)
- [MDN SSE](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events)

---

### Wave-based DAG Scheduler + errgroup ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「Wave-DAG scheduler 是把 multi-stage pipeline 從 sequential 改成 dependency-aware parallel — 從 plan 建 dependency graph(誰要先完成才解鎖誰),同一 wave 內互不依賴的 stage 用 `errgroup.WithContext` 跑 parallel。Resume verified:commeet-text2sql stages run in parallel via `errgroup`,each fails open to keep the happy path resilient。**⚠️VERIFY**:resume 沒寫具體幾個 stage / 幾個 wave / latency 改善多少 %,面試前 trace 你 repo 抓真實數字。」
> 

### 🔄 必背深度展開

### 🔽 1. errgroup vs sync.WaitGroup(必會比較)

| 特性 | `sync.WaitGroup` | `errgroup.Group` |
| --- | --- | --- |
| 等所有 goroutine 結束 | ✅ | ✅ |
| 收集第一個 error | ❌ | ✅ |
| Context 自動取消 | ❌ | ✅(`WithContext`) |
| Bounded concurrency | ❌ | ✅(`SetLimit(N)`) |
| 適用場景 | fire-and-forget | 任一失敗都要中止其他 |

**`SetLimit(0)` 為什麼會 deadlock?**(從 source code 驗證)
- `SetLimit(n)` 內部 `g.sem = make(chan token, n)` — n=0 是 unbuffered channel
- `Go(f)` 內部 `g.sem <- token{}` 在 unbuffered chan 上 send 沒人收 → block 永遠
- 結論:**`SetLimit(0)` = deadlock 第一個 `Go()`**,要設 `> 0` 才有意義

**`SetLimit(-1)` 行為**
- source code: `if n < 0 { g.sem = nil; return }`
- 結果:no limit,跟沒呼叫 SetLimit 一樣

**`WithContext` 觸發點**
- 任一 goroutine 回 err → cancel ctx → 其他正在跑的 goroutine 用 `ctx.Done()` 自行退場
- errgroup 不會幫你殺 goroutine,只是 cancel ctx,goroutine 必須自己 cooperative 退出

**Reference**: [errgroup package doc](https://pkg.go.dev/golang.org/x/sync/errgroup);[errgroup source code](https://cs.opensource.google/go/x/sync/+/refs/tags/v0.10.0:errgroup/errgroup.go)

### 🔽 2. Wave-DAG 概念 — 從 sequential 到 wave-scheduled

**Sequential pipeline**(初始實作)

```
Stage 1 → Stage 2 → Stage 3 → Stage 4 → Stage 5
```

總 latency = sum(Stage_i)

**Wave-DAG pipeline**(優化後)

```
Wave 1: [Stage 1, Stage 2]   parallel
Wave 2: [Stage 3, Stage 4]   parallel, depends on Wave 1
Wave 3: [Stage 5]           depends on Wave 2
```

總 latency = max(Wave 1) + max(Wave 2) + max(Wave 3)

**前提**:你必須**明確定義 stage 之間的 dependency** — 不能有 hidden coupling(共享 mutable state、隱性 ordering)

**真實 commeet-text2sql 形狀**(resume verified)
- Resume 寫 「Stages run in parallel via `errgroup`」— 但具體 wave 結構、stage 數量、latency 改善 % 沒寫
- ⚠️ VERIFY:你 repo 真實 wave 結構 — 面試前 trace 你 orchestrator code

### 🔽 3. SetLimit 對 DB pool 的影響

**問題**:errgroup 跑 N 個 parallel goroutine,每個都可能 acquire DB connection — 如果 N > pool size,後面的 goroutine 等 DB conn → 等於 self-block

**安全做法**:**SetLimit ≤ pool size 的某個比例**
- 太貪心(SetLimit = MaxOpenConns)— pool 完全被 parallel goroutine 占用,其他路徑(audit log / metrics)沒 conn 用
- 保守(SetLimit ≤ 2/3 × MaxOpenConns)— 留 1/3 給其他路徑用
- 這個 ratio 是我心目中的 rule of thumb,不是 errgroup 文件規定 — ⚠️VERIFY 你 commeet-text2sql 實際配多少

**為什麼說 2/3?**
- 1/2 太保守,parallelism 折半
- 3/4 太貪心,其他路徑容易被擠
- 2/3 是我自己選的 trade-off,有 production 場景驗證 — but I’d verify the actual setting before claiming in interview

**Reference**: [errgroup SetLimit docs](https://pkg.go.dev/golang.org/x/sync/errgroup#Group.SetLimit);DB pool sizing 是個獨立大話題

### Q&A(必背)

### 🔽 Q: 為什麼 errgroup 而不自己寫 channel pool?

「errgroup 三個 winning property:
1. **First-error capture** — 任一 goroutine 失敗,Wait() 回那個 error;手寫要自己 sync.Once + atomic
2. **Context propagation** — `WithContext()` 自動把 cancellation 串到所有 goroutine
3. **Bounded concurrency** — `SetLimit(N)` 內建 semaphore-based;手寫要自己 channel-based

手寫沒贏面 — errgroup 是 `golang.org/x/sync` 官方 maintained,source 很短(~100 行)讀過就懂。

唯一場景手寫贏 — 你需要**收集所有 error**(不只 first),那 errgroup 不夠,要自己包 wrapper。」

📚 [errgroup source](https://cs.opensource.google/go/x/sync/+/refs/tags/v0.10.0:errgroup/errgroup.go)

### 🔽 Q: errgroup 跟 cooperative deadline 怎麼配?

「`errgroup.WithContext(parentCtx)` 拿到的 ctx 已經帶 parent deadline。任一 goroutine 看到 ctx.Done() — 兩種來源:(1) parent deadline 到 (2) sibling goroutine 失敗觸發 cancel。Goroutine 內部要禮貌退場:DB call 帶 ctx、HTTP call 帶 ctx、LLM SDK call 帶 ctx。

我 commeet-text2sql 的 stage 都 `func(ctx) error` 形式,所有 IO call 一定帶 ctx,不留 zombie goroutine。」

📚 Resume line 44 + 47 的合奏 — errgroup parallel + cooperative deadline chain 串起來

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「5 sequential → 3 waves → 40% latency reduction」** — 我之前文件寫過這個具體數字,但 resume 沒背書,⚠️VERIFY 你 repo 真實 wave 結構 + latency 改善 %
- ❌ **「SetLimit(2/3 × MaxOpenConns) 是 best practice」** — 這是我個人 rule of thumb,不是 docs / paper backed
- ❌ **「我設計 wave-DAG scheduler」** — 你做的是「把 sequential pipeline 改成 errgroup parallel + 標 dependency」,不是發明 DAG scheduler
- ✅ 安全 phrasing:**「I designed parallel stage execution in commeet-text2sql using `errgroup.WithContext` with cooperative deadline propagation. Each stage fails open to maintain happy-path resilience. The actual wave structure + latency improvement numbers are in the repo (verify before quoting).」**

### 📚 References — Wave-DAG / errgroup must-read

- [errgroup package doc](https://pkg.go.dev/golang.org/x/sync/errgroup) — 必讀,API 介紹
- [errgroup source code](https://cs.opensource.google/go/x/sync/+/refs/tags/v0.10.0:errgroup/errgroup.go) — 看 SetLimit / Go / Wait 怎麼實作
- [Go blog Pipelines](https://go.dev/blog/pipelines) — channel-based pipeline 經典範例
- [Go blog Context](https://go.dev/blog/context) — context propagation 基礎

---

### uber/fx + Wire

### uber/fx + Wire ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「Wire 跟 uber/fx 是 Go 兩大 DI library,**兩個都用過**:Commeet 主系統用 Wire(line 29 resume verified: ‘Wire DI rewiring’ 在 is_controller migration);commeet-text2sql + booking_monitor 用 uber/fx(line 76 resume verified: ‘4 fx-wired `cmd/` binaries’)。**關鍵差別**:Wire 是 **compile-time** DI — 透過 `go generate` 在 build time 產生 wiring code,零 runtime reflection 成本;uber/fx 是 **runtime** DI + lifecycle hooks(OnStart / OnStop),更彈性但啟動時有 reflection overhead。選擇邏輯:critical-path service / 啟動時間敏感 → Wire;需要 lifecycle hooks / graceful shutdown / 動態 wiring → fx。」
> 

### 🔄 必背深度展開

### 🔽 1. Compile-time vs Runtime DI 取捨

| 特性 | Wire (compile-time) | uber/fx (runtime) |
| --- | --- | --- |
| Wiring 何時解析 | `go generate` 跑 wire 產生 code | application 啟動時用 reflection |
| Reflection cost | ✅ 零 | ❌ 啟動時有 reflection |
| Startup overhead | ✅ 零 — 就是 hand-written code | ❌ 數十 ms - 數百 ms reflection |
| Compile error 找錯 | ✅ build time | ❌ runtime panic |
| Lifecycle hooks | ❌ 沒有 | ✅ OnStart / OnStop |
| Dynamic wiring | ❌ 不能 | ✅ 可以(fx.Provide + Module) |
| Codegen 維護 | ❌ 每次 wire diagram 變要重跑 | ✅ 直接改 Provider 即可 |

**為什麼 Commeet 用 Wire?**
- 主系統啟動時間敏感(我的另一個 PR #4537 「62% startup latency reduction」就是 attack startup time)
- Compile-time error 對大 codebase 更安全
- 但要 lifecycle hooks 自己管理(close DB / Redis 連線等)

**為什麼 booking_monitor 用 fx?**
- Portfolio demo 多個 subcommand(server / recon / saga-watchdog / expiry-sweeper),fx Module 讓共用 dependency 容易組裝
- OnStop hook 幫忙 graceful shutdown(關 Kafka producer / consumer / DB pool)
- 啟動 overhead 對 demo 不敏感

**Reference**: [Wire docs](https://github.com/google/wire);[uber/fx docs](https://uber-go.github.io/fx/);booking_monitor `cmd/booking-cli/` 看 fx 實際用法

### 🔽 2. fx Lifecycle hooks — 為什麼 graceful shutdown 需要

**沒 lifecycle 怎麼 graceful shutdown?**
- 用 signal handler `os.Signal{SIGTERM, SIGINT}` 自己 close 各 resource
- 多個 resource 互相依賴時(Kafka producer 要先 close 再 close DB pool),order 自己 管
- 容易漏 close,resource leak

**fx lifecycle 做了什麼?**

```go
fx.Invoke(func(lc fx.Lifecycle, db *DB, kafka *KafkaProducer) {
    lc.Append(fx.Hook{
        OnStart: func(ctx context.Context) error {
            return db.Ping(ctx)
        },
        OnStop: func(ctx context.Context) error {
            return db.Close()
        },
    })
})
```

- fx 自己管 lifecycle order(reverse dependency order shutdown)
- SIGTERM 進來 → fx.App.Stop() → 倒序執行所有 OnStop
- ctx 帶 grace period(`fx.App.StopTimeout`)— 超過就 force

**真實 booking_monitor 案例**
- ⚠️ VERIFY:具體看 `cmd/booking-cli/main.go` 跟 `internal/bootstrap/` 的 fx Module 結構
- 大致 lifecycle hook 用在:HTTP server(graceful shutdown)、Kafka producer/consumer(flush)、DB pool(close)、outbox relay(advisory lock release)

**Reference**: [fx Lifecycle docs](https://pkg.go.dev/go.uber.org/fx#Lifecycle);booking_monitor `cmd/booking-cli/main.go`

### 🔽 3. fx.As + Module — narrow interface 暴露給 graph

**問題**:`postgresOrderRepository` 是 concrete 實作,但其他 service 該 depend 在 `OrderRepository` interface 上(DDD 規矩)。

**解法**:`fx.As` 把 concrete 註冊成 interface

```go
var Module = fx.Module("persistence",
    fx.Provide(
        fx.Annotate(
            NewPostgresOrderRepository,           // returns *postgresOrderRepository
            fx.As(new(domain.OrderRepository)),   // expose as interface
        ),
    ),
)
```

- 之後 graph 內任何 `func(repo domain.OrderRepository)` 都收到 `postgresOrderRepository` instance
- Service layer 看不到 concrete type,Clean Architecture / DDD 依賴方向 enforced

**Module pattern**
- 一個 Module 一份 cohesive provider set(e.g., persistence module / cache module / messaging module)
- 主程式 `fx.New(persistence.Module, cache.Module, ...)` 組裝
- ⚠️ VERIFY:booking_monitor 具體 Module 切法 — 看 `internal/bootstrap/`

**Reference**: [fx.Annotate docs](https://pkg.go.dev/go.uber.org/fx#Annotate);[fx.Module pattern](https://uber-go.github.io/fx/modules.html)

### Q&A(必背)

### 🔽 Q: 為什麼要 DI,直接 New 不行嗎?

「`New`-based 在小專案 OK,但會逐漸長成:

```go
// 開始很簡單
db, _ := NewDB(cfg)
redis, _ := NewRedis(cfg)
repo := NewRepository(db)
service := NewService(repo, redis)

// 加 30 個 service / repo / cache 之後...
// main.go 變成 500 行 manual wiring
// 加新 dependency 要改 main.go + 每個 caller
```

**DI 三個 winning property**:
1. **Wiring 從 main.go 移到 declarative** — 每個 service 自帶 Provider,main 只 import module
2. **Testing 容易** — Provider 接收 interface,test 換 mock
3. **Graph validation** — fx 啟動時驗證 graph 沒 missing dependency / cycle

**反論**:DI 增加 indirection — debugging stack trace 跑進 fx framework code,新人看不懂。**所以 DI 適合中大型專案(>20 entity),小專案 manual wiring 仍然 OK**。」

📚 [Why DI in Go - Uber blog](https://github.com/uber-go/fx)

### 🔽 Q: Wire 跟 fx 你怎麼選?

「**Wire** — when 啟動時間敏感、build-time error 重要、graph 大致靜態:
- Commeet 主系統(我熟 — Wire DI rewiring 在 `is_controller` migration PR)
- Production service 跑 N 個 instance,啟動 overhead × N 累積

**fx** — when 需要 lifecycle hooks、graph 動態(多 subcommand 共享 module)、有 graceful shutdown 需求:
- booking_monitor 5 個 binary 共用 internal/ packages,fx Module 容易組
- LLM agent 場景(commeet-text2sql)stage 動態 wire 進來

**實務**:大 codebase 兩個都用過,各有取捨;一個 codebase 不要混用,選一個堅持。」

📚 [Wire docs](https://github.com/google/wire);[fx docs](https://uber-go.github.io/fx/)

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「Wire 100% zero overhead」** — `go generate` 跑 wire 是 build time cost(秒級),runtime 才是 zero
- ❌ **「fx reflection cost 很大」** — 啟動數十 ms 不算大,production 啟動時的 fx overhead 通常被其他 IO(DB ping / cache warmup)overshadowed
- ✅ 安全 phrasing:**「I’ve used both Wire (Commeet) and uber/fx (booking_monitor + commeet-text2sql), choose based on whether the project needs lifecycle hooks (fx) or maximum startup speed + compile-time guarantees (Wire).」**

### 📚 References — DI must-read

- [Wire docs + tutorial](https://github.com/google/wire) — compile-time DI
- [uber/fx docs](https://uber-go.github.io/fx/) — runtime DI + lifecycle
- [Uber blog announcing fx](https://github.com/uber-go/fx) — design motivation
- booking_monitor `cmd/booking-cli/main.go` + `internal/bootstrap/` — fx 實際用法 case study

---

### GORM ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「GORM 是 Go 最廣用的 ORM,我在 Commeet 主系統每天用 2+ 年,resume 有 4 個 PR 直接 ground:**PR #5694**(`.Table` vs `.Model` soft-delete scope leak,12.4s → 277ms 45×)、**PR #5557**(batch update with `VALUES` clause — `BatchUpdateDocApprovalProcessNodeOrderSeq` 等 3 個 helper)、**PR #5546**(eliminate N+1 in `GetDocApprovalProceduresV3` API chain)、**`transaction.GetTx(ctx)` + `WithContext` fallback dual-style pattern**(resume line 30 verified)。我對 GORM 的態度:會用,但**知道 ORM 的踩點**,不會無腦相信。」
> 

### 🔄 必背深度展開

### 🔽 1. `.Model` vs `.Table` — 我 PR #5694 親身踩到

**`.Model(&User{})`** — 走 GORM 的 model-driven path
- 自動套用 model 註冊的 plugin scope(SoftDelete `deleted_at IS NULL` / Timestamping `created_at` `updated_at` 等)
- 生成 query 帶 model-aware logic
- 但需要 model struct 定義在 hand

**`.Table("users")`** — 走 query-builder path
- 把 GORM 純當 SQL builder 用,**跳過 model-level scope**
- 適合複雜 JOIN / dynamic table 場景,但要**手動處理 soft delete**
- 不熟的開發者最容易踩

**踩點本質**:同一個 codebase 內 mix `.Model` + `.Table` 容易出 inconsistency — 某些 query 自動套 soft delete,某些沒套。Code review 看單一行很正確,放上下文才錯。

**結構性防護** — 比靠 code review 抓更可靠:
1. **SQL lint rule** — 偵測 query 沒帶 `deleted_at` 條件就 warning
2. **Integration test fixture** — 主動 cover「query 不應該回 deleted row」場景

**Reference**: [GORM Soft Delete docs](https://gorm.io/docs/delete.html#Soft-Delete);[GORM Raw SQL & Query Builder](https://gorm.io/docs/sql_builder.html);PR #5694 detail 在 Bullet 1

### 🔽 2. Batch UPDATE with VALUES clause — PR #5557 verified

GORM 預設 `db.Save(&records)` 對 slice 做 **per-row UPDATE in loop** — N 個 row → N 個 UPDATE statement。

**真實 PR #5557 改善**(resume line 25 verified helper 名):
- `BatchUpdateDocApprovalProcessNodeOrderSeq` — 批次更新 node order sequence
- `BatchUpdateDocApprovalProcessNodesCollectionSignEvents` — 批次更新節點集合 sign event
- `BatchUpdateDocApprovalProcessNodesSignEvents` — 批次更新單 node sign event

SQL pattern(per Postgres docs):

```sql
UPDATE table_name AS t
SET col1 = v.col1, col2 = v.col2
FROM (VALUES (id1, val1a, val1b), (id2, val2a, val2b), ...) AS v(id, col1, col2)
WHERE t.id = v.id;
```

- 一個 statement update N row,planner hash join 處理 → O(N + M)
- 配合 `ON CONFLICT` 還能做 upsert

**注意點**:每個 VALUES tuple 必須 type cast(`$1::uuid` 等),否則 PG 跑不過

**Reference**: [PG UPDATE FROM VALUES docs](https://www.postgresql.org/docs/current/sql-update.html);PR #5557

### 🔽 3. Transaction + ctx 整合 — resume line 30 verified pattern

**問題**:repository method 要支援兩種 caller style:
- **Caller A**:傳 root DB,自己在 ctx 帶 active transaction
- **Caller B**:傳 tx-bound DB session(已開 tx),ctx 沒帶

讓單一 repo method 兩種都能用是 codebase 一致性必要,但容易寫得醜。

**Resume 寫的解法**(line 30):
> “Pattern A (DI-injected root DB + ctx-carried tx) and Pattern B (tx-bound session, ctx-empty) — both via `transaction.GetTx(ctx)` with `WithContext` fallback so a single repo method supports either caller style. Aligned 13 service-layer error-handling sites to backend-guide-DDD conventions in a single sweep.”

**結構大致是**:

```go
// Repository method
func (r *repo) Save(ctx context.Context, o *Order) error {
    db := transaction.GetTx(ctx)        // Pattern A: 拿 ctx 內的 tx
    if db == nil {
        db = r.db.WithContext(ctx)       // Pattern B fallback: 用 root + ctx
    }
    return db.Save(o).Error
}
```

- Service 層用 Pattern A(顯式 begin tx + 注入 ctx)
- Worker 場景如果已經是 tx-bound session,Pattern B 直接 fall through
- 對 caller 透明,repo 不在乎是誰來

**Reference**: [GORM Transactions](https://gorm.io/docs/transactions.html);Resume line 30

### 🔽 4. GORM hook 為何要慎用

GORM 提供 `BeforeCreate` / `AfterUpdate` 等 hook,看起來很方便,但有 3 個 production 痛點:

1. **隱式邏輯** — Hook 跑了什麼從 model struct 看不出來,新人 trace 困難
2. **跨層依賴** — hook 內部呼叫 DB / cache 容易讓 model 知道 infrastructure(violates DDD)
3. **Test 困難** — 要嘛 disable hook 跑單測,要嘛起完整 DB

我的 Commeet 經驗 — 早期 codebase 有用 hook,但逐漸往 explicit service-layer transition 移動,跟 DDD aggregate pattern 比較貼。

**面試這題可以體現「ORM 紀律」** — 不是 ORM 越用越好,而是用得越克制越好。

**Reference**: [GORM Hooks](https://gorm.io/docs/hooks.html);DDD aggregate pattern

### Q&A(必背)

### 🔽 Q: 為什麼選 GORM 而不 sqlx / sqlc?

「實話:Commeet 選 GORM 早於我加入,慣性使然。如果是 greenfield 我會傾向 **sqlc** — compile-time type safety + raw SQL control,適合 enterprise + 多 table 場景。

**GORM 強處**:
- API 直覺(association / preload / soft delete 開箱即用)
- 中小型 codebase 開發速度快

**GORM 弱處**:
- `.Model` vs `.Table` 這種隱式 scope 容易出 bug(我 PR #5694 踩到)
- ORM 思維讓人忘記 SQL 細節,partial index / planner behavior 不深入
- hook + plugin 增加 implicit behavior

**sqlc 強處**:
- 寫真實 SQL,query 行為跟生產 SQL 一致
- compile-time type check parameter + result
- 沒有 ORM hidden scope

**sqlc 弱處**:
- Codegen 多一道工
- 動態 query / dynamic WHERE 場景 sqlc 較不靈活,要 raw SQL fallback

我的 booking_monitor 沒用 ORM(直接 pgx + 手寫 SQL),也是為了避開 ORM hidden behavior。」

📚 [sqlc docs](https://sqlc.dev/);[GORM docs](https://gorm.io/)

### 🔽 Q: GORM N+1 怎麼避免?Preload 夠用嗎?

「Preload 對 simple association 夠用(`db.Preload(\"Items\").Find(&orders)` → 2 query),但有 N+1 形式 Preload 不解:

1. **Nested loop in business logic** — Service 取一批 order,for each order 又 query 其他 entity → preload 不知道你 loop 裡會做什麼
2. **Conditional preload** — 只在某些 order 才需要 preload sub-entity → preload 不支援條件式
3. **跨 service call N+1** — Service A 取 order list,Service B per-order call → preload 解不到跨 boundary

我 PR #5546 的解法是:
- 把 service-layer iteration 拆出來,改成 batch fetch with `IN (?)`
- Dedup userIDs 之後再一次 `IN (?)` 拿
- 結構 O(N) → O(1) round trip(resume line 24 verified)

具體 case 看 Bullet 3 的 N+1 sub-section。」

📚 Resume line 24;[GORM Preload docs](https://gorm.io/docs/preload.html)

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我是 GORM 內部 expert」** — 沒貢獻過 GORM patch,只是 production 用戶
- ❌ **「我會所有 GORM hook 細節」** — Hook 我寫過幾次但故意減少使用,不是 deep dive
- ❌ **「sqlc 比 GORM 全面強」** — Trade-off,看 codebase + team 經驗
- ✅ 安全 phrasing:**「I have 2+ years GORM in Commeet production. I’ve ridden the .Table vs .Model footgun (PR #5694), implemented batch UPDATE pattern (PR #5557), and standardized a ctx-aware tx pattern (resume line 30). I prefer explicit-SQL approaches (sqlc / raw pgx) for greenfield projects.」**

### 📚 References — GORM must-read

- [GORM docs](https://gorm.io/) — 官方,先讀 Hooks / Soft Delete / Transactions 三章
- [GORM Soft Delete + .Model vs .Table](https://gorm.io/docs/delete.html#Soft-Delete) — PR #5694 root cause 來源
- [sqlc - alternative](https://sqlc.dev/) — type-safe SQL 替代方案
- [Effective Go - ORM thoughts](https://dave.cheney.net/) — Dave Cheney 對 ORM 觀點

---

### N+1 Elimination ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「N+1 是 ORM 場景最常見的效能病灶 — loop 內 per-iteration 多打 DB,N 個 entity 就 N + 1 個 query。Resume line 24 verified:**PR #5546** 我消除了 `GetDocApprovalProceduresV3` API call chain 的 N+1 — batch-fetch on node collections / approval agents / notified users;removed duplicate `GetDocApprovalProcedurePolicyActionV2` + `GetDoc` calls(2× → 1× per request);deduplicated agent user IDs before batch `IN (?)` queries。**Per-request DB round-trip count reduced from O(N) to O(1)**。」
> 

### 🔄 必背深度展開

### 🔽 1. N+1 的 5 種型態

**1. Classic loop-fetch**

```go
orders, _ := db.Find(&orders)              // 1 query
for _, o := range orders {
    user, _ := db.First(&User{}, o.UserID)  // N queries
}
```

解法:`IN (?)` batch 取 user 後 map lookup

**2. Nested association 各層 loop**

```go
for _, doc := range docs {
    for _, node := range doc.Nodes {
        agents, _ := db.Find(&agents, "node_id = ?", node.ID)  // N×M queries
    }
}
```

解法:把所有 node id 蒐集起來一次 `IN (?)`,Go-side group by node_id

**3. Conditional fetch in business logic**

```go
for _, order := range orders {
    if order.Status == "paid" {
        invoice, _ := db.First(&Invoice{}, order.ID)  // 只在某些 case fetch
    }
}
```

解法:filter orders 後 batch fetch invoice

**4. Cross-service N+1**

```go
orders, _ := orderService.List(ctx, userID)
for _, o := range orders {
    detail, _ := paymentService.Get(ctx, o.PaymentID)  // 跨 service
}
```

解法:讓 paymentService 提供 `GetBatch([]ID)` API

**5. Duplicate call(隱性 N+1 變體 — PR #5546 case verified)**

```go
// resume line 24 verified pattern
GetDocApprovalProcedurePolicyActionV2(...)   // 第一次
// ... 中間別的 logic
GetDocApprovalProcedurePolicyActionV2(...)   // 第二次同 args
```

- 同 request 內同一個 expensive call 跑兩次
- 解法:**caching within request lifetime** 或 重排 logic 讓 call 只發一次

### 🔽 2. PR #5546 — `GetDocApprovalProceduresV3` 怎麼改

Resume line 24 verified scope:
1. **Batch-fetch on node collections** — 一次 `IN (?)` 取所有 node
2. **Batch-fetch approval agents** — 一次取所有 agents
3. **Batch-fetch notified users** — 一次取所有被通知用戶
4. **Removed duplicate `GetDocApprovalProcedurePolicyActionV2`** — 2× → 1× per request
5. **Removed duplicate `GetDoc`** — 同上
6. **Deduplicated agent user IDs before batch IN (?)** — 同個 user 出現在多 agent 不要重複查

⚠️ **VERIFY**:resume 沒寫具體 query count 減少多少。你 PR body 應該有 benchmark 數據(p99 改善 / DB hit / latency),面試前抓出來。

**典型 Go-side dedup pattern**

```go
seen := make(map[int]struct{}, len(items))
ids := make([]int, 0, len(items))
for _, item := range items {
    if _, ok := seen[item.UserID]; ok {
        continue
    }
    seen[item.UserID] = struct{}{}
    ids = append(ids, item.UserID)
}
users, _ := repo.GetByIDs(ctx, ids)   // batch IN (?)
```

**Reference**: PR #5546 (resume line 24);GORM batch query patterns

### 🔽 3. 「14 個 N+1 pattern」這個說法來自哪

⚠️ **VERIFY**:這個 “14” 數字我之前 doc 寫過(來自 MEMORY 紀錄),但 resume 只說 PR #5546 / #5557 改過 N+1,沒列舉「14 個 pattern」總數。如果你要在面試講「我消除了 14 個 N+1 pattern」,**面試前 trace 真實 PR description / commit log 確認數字**,或改成 resume 寫的 “Per-request DB round-trip count reduced from O(N) to O(1)” 更安全。

### 🔽 4. 通用偵測技巧

**Production 偵測**:
- pg_stat_statements 看 same query 同 calls 數量爆炸的 row
- APM(Datadog / New Relic)trace 看單一 request 內 DB span 數量
- Custom GORM plugin log slow path

**開發期偵測**:
- Integration test 跑單一 endpoint,assert SQL 執行次數 ≤ N
- `gorm.SkipDefaultTransaction = true` + assertion 在 caller 內 manual count

**Reference**: [pg_stat_statements docs](https://www.postgresql.org/docs/current/pgstatstatements.html);[Datadog APM N+1 detection](https://docs.datadoghq.com/)

### Q&A(必背)

### 🔽 Q: 你怎麼一開始發現 N+1 的?

「兩個 trigger:
1. **Production slow endpoint alert** — p99 latency 飆高,看 APM trace 發現 DB span 數量爆炸
2. **Manual review during refactor** — 同 PR 別的目的修改 `GetDocApprovalProceduresV3`,順手看到 loop 內 query 結構

我 PR #5546 是後者 — 改完一個相關 feature 後順道把 round-trip count 從 O(N) 降到 O(1)。」

📚 PR #5546 (resume line 24)

### 🔽 Q: dedup userIDs 為什麼用 map 不用 sort+unique?

「兩個都可以,選 map 是 O(N) 而非 sort 的 O(N log N) — 當 N 大或 dedup ratio 高時 map 較快。但 sort 在 cache locality 上比 hash map 好,小 N 時 sort 可能反贏。

實務上 — Go 沒有 stdlib 的 `slice.Unique`,自己寫 sort+compact 比 map 多幾行,所以多數人用 map dedup。我 PR #5546 也是 map dedup。

如果是 reusable utility(企業裡常用),寫一個 generic `Dedup[T comparable]` 比較好,用 map 實作:

```go
func Dedup[T comparable](s []T) []T {
    seen := make(map[T]struct{}, len(s))
    out := make([]T, 0, len(s))
    for _, v := range s {
        if _, ok := seen[v]; !ok {
            seen[v] = struct{}{}
            out = append(out, v)
        }
    }
    return out
}
```」

📚 Generic dedup pattern;PR #5546

##### ⚠️ Honest gotcha — 別亂掰

- ❌ **「14 個 N+1 pattern」** — 之前文件寫的,resume 沒背書,⚠️VERIFY 真實數字
- ❌ **「每個 endpoint p99 都從 X 變 Y」** — 沒留下逐 endpoint benchmark,⚠️VERIFY PR body
- ✅ 安全 phrasing:**「PR #5546 reduced DB round-trip from O(N) to O(1) on `GetDocApprovalProceduresV3` via batch IN (?) + dedup userIDs + removing duplicate calls. Resume-verified scope; specific quantification per endpoint is in the PR body.」**

##### 📚 References — N+1 must-read

- PR #5546 (resume line 24) — own example
- [GORM Preload](https://gorm.io/docs/preload.html) — preload-based solution
- [pg_stat_statements](https://www.postgresql.org/docs/current/pgstatstatements.html) — production detection
- GitHub topic: `n-plus-1-query-problem` for community write-ups — concept primer

---

#### Multi-tenant Data Isolation ⭐ 深度展開

##### 🎯 30 秒口述版(背)

> 「Multi-tenant SaaS 的核心安全 invariant 是 **tenant A 不能看到 tenant B 的資料**。Resume line 51 verified:**我 closed a cross-company data-leak vulnerability in user-access SQL queries — production hotfix preventing tenant data from leaking to another tenant**。MEMORY 紀錄這條對應 **PR #5769,涵蓋 8 個 function**。具體 root cause(EXISTS subquery 是否正確 correlate outer company_id)的 SQL pattern ⚠️VERIFY 你 PR body,但 leak 形態是業界常見的 multi-tenant footgun。」

##### 🔄 必背深度展開

##### 🔽 1. Multi-tenant 4 種 isolation 策略(從弱到強)

| 策略 | 描述 | Trade-off |
|:--|:--|:--|
| **Shared schema, filter by `tenant_id`** | 所有 table 帶 `company_id`,每個 query WHERE 加 filter | 最便宜;footgun 多(任一漏 filter = leak) |
| **Shared schema + Row-Level Security (RLS)** | PG `CREATE POLICY`,planner 自動加 tenant filter | 安全保障強;debug 複雜;不能跨 tenant analytics |
| **Schema-per-tenant** | 每 tenant 一個 PG schema | 隔離強;migration 複雜;scale 到 100 tenant 之後 schema 爆 |
| **Database-per-tenant** | 每 tenant 一個 DB | 最強隔離;最貴;適合超大客戶 |

**Commeet 是哪個?**
- ⚠️ VERIFY:從 resume line 51 場景看(user-access SQL queries 漏 cross-company),應該是 **shared schema + manual `company_id` filter** 路線
- 我 PR #5769 修的就是「應該 filter 但沒 filter」的 footgun

**為什麼 shared schema 最危險?**
- Footgun 在「忘了寫」— application code 99% query 都帶 filter,1% 漏網
- Code review 看單一 query 都正確,放 production 才暴露
- 對策:RLS 或 SQL lint rule(detect query missing `company_id` filter)

**Reference**: [Microsoft - Multi-tenant data architecture](https://learn.microsoft.com/en-us/azure/architecture/guide/multitenant/approaches/storage-data);[PG Row-Level Security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)

##### 🔽 2. EXISTS subquery correlation 漏洞 — 業界常見 footgun

⚠️ VERIFY:下列是 multi-tenant 常見 footgun **pattern 示意**,不是 PR #5769 逐字 code。面試前打開 PR diff 對。

**錯誤 query**:
```sql
-- 假設:departments 表有 (id, company_id);users 表也有 (id, company_id)
-- 想問:user X 是否屬於「整公司」的某種 ACL group?
SELECT *
FROM users u
WHERE u.id = $userID
  AND EXISTS (
      SELECT 1 FROM departments d
      WHERE d.id = $deptID
      -- ❌ 沒 correlate `d.company_id = u.company_id`
      -- → 任一 company 都有可能有同 deptID(或 deptID 已被刪重編)
      -- → subquery 對任何 user 都 true → 整個 query 漏跨 company
  );
```

**正確 query**:

```sql
SELECT *
FROM users u
WHERE u.id = $userID
  AND EXISTS (
      SELECT 1 FROM departments d
      WHERE d.id = $deptID
        AND d.company_id = u.company_id   -- ✅ 加 outer correlation
  );
```

**為什麼這 leak 嚴重?**
- 不是 query 報錯 — 是**靜默回了不該回的 row**
- 看不到 panic 或 alert,要靠 audit / pen-test 才會發現
- Production hot-fix 通常是「加一行 outer correlation」,但要 grep 整個 codebase 找漏網的 EXISTS 用法

**MEMORY 紀錄**:PR #5769 涵蓋 **8 個 function** 的修正(grep 出所有類似 footgun 一次掃),這個量級顯示是系統性問題不是單點 fix。

**Reference**: PG docs - Subqueries;Resume line 51

### 🔽 3. CTE pattern — `user_company_id` 集中拿一次

⚠️ VERIFY 真實 pattern,以下示意:

```sql
WITH ucid AS (
    SELECT company_id FROM users WHERE id = $userID
)
SELECT *
FROM some_table t
WHERE t.company_id = (SELECT company_id FROM ucid);
```

- 把 「current user 的 company_id」一次性算好,後續所有 filter 引用同個值
- 比每個 query 寫 `(SELECT company_id FROM users WHERE id = ?)` 內嵌 cleaner
- 但 CTE 不是萬靈丹 — 仍然要記得每個 WHERE 都 reference,漏寫一個 = leak

**Application-side 進階做法**
- 從 ctx 一路傳 `tenant_id`(類似我 commeet 的 ctx propagation 設計)
- Repository 強制簽名 `func GetX(ctx, tenantID, otherArgs)`,缺 tenantID 就 compile error
- 比 SQL-side enforcement 更貼近 type safety

### 🔽 4. 哪些 query safe-by-design 不用 filter?

並不是 100% query 都要 `company_id` filter,以下 case safe:

1. **Globally-unique PK lookup**:`WHERE order_id = ?`(order_id 跨 tenant 唯一,沒 collision)
2. **Admin-only query**:給 super-admin 看跨 tenant 的 admin tooling(明確有 audit log)
3. **Aggregated analytics**:刻意跨 tenant 的 BI report(必經 admin route)

**設計原則**:**default = filter**,exception 要明確 document + 過 review。任何「我以為這 query 安全」都是 footgun 預兆。

### Q&A(必背)

### 🔽 Q: 你怎麼發現這個 cross-company leak?

「老實答 — Resume 寫『production hotfix』,所以是 production-side trigger:可能是 audit / pen-test / customer report / metrics anomaly。

⚠️ VERIFY 你 PR body 寫的真實 trigger — 是 customer report 還是 internal audit?面試前確認。

無論哪個 trigger,**重點是 response flow** — 發現 → reproduce → grep 整個 codebase 找類似 pattern(MEMORY 紀錄 PR #5769 涵蓋 8 functions,說明你做了 codebase-wide grep)→ batch fix → write test fixture → ship hotfix。」

📚 Resume line 51;PR #5769

### 🔽 Q: 為什麼不用 PG Row-Level Security?

「RLS 是強的 — 但有 3 個 production trade-off 讓很多 SaaS codebase 不採用:

1. **Debug 複雜度** — query 直接看不出 filter,planner 自動加,新人 trace 困難
2. **Migration tooling** — RLS policy 跟 schema migration 工具沒完全 integrate(GORM 沒原生支援)
3. **跨 tenant analytics** — 內部 admin / BI 想跑跨 tenant query 要 bypass RLS,policy 設計更複雜

Commeet 走 application-level filter + grep-friendly pattern,trade-off:強度比 RLS 弱,但 debug + tooling 友好。長期看如果繼續發生 footgun(像 PR #5769)我會推 RLS migration,但**安全強化要有 ROI 證明**(每年 N 次 leak vs RLS 上線後的 ops cost)。」

📚 [PG Row-Level Security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html);Resume line 51

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我設計了 multi-tenant architecture」** — 你做的是「修 existing architecture 的 leak」,不是 architect
- ❌ **「具體 SQL 是這樣寫的」** — ⚠️VERIFY 真實 SQL,我這份 doc 給的是 pattern 示意
- ❌ **「Commeet 100% safe」** — 你修了 8 個 footgun,**承認還有未發現的可能性**(這是誠實的 security posture)
- ✅ 安全 phrasing:**「I fixed a cross-company data-leak vulnerability in user-access SQL queries (PR #5769, MEMORY notes 8 functions in scope). Root cause was EXISTS subquery pattern missing outer-correlation on company_id. Production hotfix + codebase-wide grep for similar pattern. Resume line 51 verifies the high-level scope; specifics of the SQL pattern verified in PR body.」**

### 📚 References — Multi-tenant must-read

- Resume line 51;PR #5769;MEMORY entry
- [PG Row-Level Security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)
- [Microsoft Multi-tenant SaaS guide](https://learn.microsoft.com/en-us/azure/architecture/guide/multitenant/overview)
- [Stripe Engineering - Multi-tenancy](https://stripe.com/blog/online-migrations) — production migration patterns

---

### EXPLAIN ANALYZE / Partial Index ⭐ 深度展開

### 🎯 30 秒口述版(背)

> 「EXPLAIN ANALYZE 是 PG 的 query planner 工具,實際執行 query 印出 plan node + cost + actual time + buffers;partial index 是條件式索引(`CREATE INDEX ... WHERE predicate`),size 小、planner 需證明 query predicate 蘊含 index predicate 才會選用。Resume MEMORY verified 我 **PR #5694 用 EXPLAIN ANALYZE 找到 12s slow query → 277ms,45× improvement**,root cause 是 GORM `.Table()` 沒套 soft-delete scope 讓 planner 推導不出 partial index 條件。」
> 
> 
> 完整 walkthrough 在 **→ 見 Part 5 Bullet 1**;這裡是 stack-level 速答版本。
> 

### 🔄 必背深度展開

### 🔽 1. EXPLAIN ANALYZE — 看什麼?

完整 deep-dive 已在 **→ 見 PostgreSQL stack 第 4 個 toggle** 寫過,這裡只列高頻 keyword:

- **Plan node**:Seq Scan / Index Scan / Index Only Scan / Bitmap Heap Scan / Nested Loop / Hash Join / Merge Join
- **cost=A..B / rows=N / actual time=X..Y rows=N loops=K**
- **Buffers: shared hit=X read=Y**(配 `BUFFERS` option:`EXPLAIN (ANALYZE, BUFFERS) ...`)
- **estimate vs actual 差距**:差 10× 以上代表 stats stale(`ANALYZE` 跑一次)
- **Rows Removed by Filter**:Seq Scan + 大量 filter rejected = 缺 index 訊號

**Reference**: [PG EXPLAIN docs](https://www.postgresql.org/docs/current/sql-explain.html);[Depesz EXPLAIN visualizer](https://explain.depesz.com/)

### 🔽 2. Partial Index — planner 蘊含證明邏輯

完整 deep-dive 已在 PostgreSQL stack 第 1 個 toggle 寫過,核心邏輯:

- Partial index `WHERE deleted_at IS NULL` 只 index 非 soft-deleted row
- Query `WHERE x = 1` 沒帶 `deleted_at IS NULL` → planner 不敢用 partial(query 沒蘊含 index predicate)
- Query `WHERE x = 1 AND deleted_at IS NULL` → planner 證明蘊含 → Index Scan

**驗證 partial index 被用**:

```sql
SELECT indexrelname, idx_scan, idx_tup_read
FROM pg_stat_user_indexes
WHERE indexrelname = '<your partial index name>';   -- ⚠️VERIFY 真實 index name
```

idx_scan 應該成長,沒長代表 partial index 沒被用。

**Reference**: [PG Partial Indexes docs](https://www.postgresql.org/docs/current/indexes-partial.html);[Use The Index Luke](https://use-the-index-luke.com/sql/where-clause/null/partial-index)

### 🔽 3. 3-step rollout plan — 為什麼不直接 drop workaround index?

完整 deep-dive 在 **→ 見 Part 5 Bullet 1** 第 4 個 toggle,核心理由:

- Production 已有 **臨時 full index** cover 之前 slow query
- 如果 PR 出問題要 rollback,**但 full index 已被 drop,就雙重失守**
- 保留 full index 當 safety net,觀察期確認 partial index 真的被選用後再 drop
- `DROP INDEX CONCURRENTLY` 不能在 transaction block(PG 限制)

**Reference**: [PG DROP INDEX docs](https://www.postgresql.org/docs/current/sql-dropindex.html) — CONCURRENTLY option 細節

### Q&A(必背)

### 🔽 Q: 你怎麼用 EXPLAIN ANALYZE 找 root cause 的?

「步驟:
1. **跑 `EXPLAIN (ANALYZE, BUFFERS) <query>`** — analyze 真實執行,buffers 印 IO
2. **看 plan tree 最 bottom-up 哪個 node 吃時間** — actual time 最長的就是瓶頸
3. **看那個 node 的 plan type** — Seq Scan + 大量 row + 大量 buffer hit = missing index 訊號
4. **看 query predicate vs 已有 index** — 為什麼 planner 不用既有 index
5. **修 query 或加 index 後再 EXPLAIN ANALYZE 一次** — 確認 plan node 變成 Index Scan + buffer 減少

我 PR #5694 走的就是這個流程:bottom-up 看到 approval-related subquery 占大部分時間,plan 是 Seq Scan + 大量 buffer hit;追到 GORM `.Table()` 沒帶 `deleted_at IS NULL`;修完 plan 變 Index Scan,buffer 大幅減少(⚠️VERIFY 真實 buffer 數字)。」

📚 [PG EXPLAIN docs](https://www.postgresql.org/docs/current/sql-explain.html);PR #5694

### 🔽 Q: Partial index 永遠贏 full index 嗎?

「不,要看 query pattern。

**Partial 贏的場景**:
- 大多數 query 都帶 partial predicate(`WHERE deleted_at IS NULL`)
- Soft-deleted row 占大宗(例:95% deleted,5% active)
- size 顯著小於 full,B-tree 深度減少

**Full 贏的場景**:
- Query 不一致帶 partial predicate(有些帶有些沒帶)→ planner 經常退回 full
- Active row 占大宗(例:95% active,5% deleted)→ partial 跟 full 幾乎一樣大,partial 沒明顯 win
- 需要服務 deleted row 的 query(admin tool / audit)→ partial 不蓋

我 PR #5694 場景是 active 比例極低,partial 顯著贏(MEMORY 紀錄 12.4s → 277ms 45×)。」

📚 [Use The Index Luke - Partial](https://use-the-index-luke.com/);PR #5694

### ⚠️ Honest gotcha — 別亂掰

- ❌ **「我所有 EXPLAIN output 都記得清楚」** — ⚠️VERIFY 真實 row / buffer / cost 數字,只引你 PR body 寫的
- ❌ **「我做過 PG planner internal tuning」** — 沒改過 `effective_cache_size` / `random_page_cost`,只是用戶
- ✅ 安全 phrasing:**「I use EXPLAIN ANALYZE for tactical debugging — bottom-up plan tree analysis, identify cost / actual time outliers, verify index usage via pg_stat_user_indexes. PR #5694 (12.4s → 277ms 45×, MEMORY verified) is my canonical example of using EXPLAIN to find a partial-index footgun.」**

### 📚 References — EXPLAIN / Partial Index must-read

- [PG EXPLAIN docs](https://www.postgresql.org/docs/current/sql-explain.html) — 官方
- [PG Partial Indexes docs](https://www.postgresql.org/docs/current/indexes-partial.html) — partial 定義
- [Depesz EXPLAIN visualizer](https://explain.depesz.com/) — 貼 EXPLAIN ANALYZE 視覺化看 plan tree
- [Use The Index Luke](https://use-the-index-luke.com/) — Markus Winand,SQL indexing 最權威
- [Postgres pg_stat_user_indexes](https://www.postgresql.org/docs/current/monitoring-stats.html) — 確認 index 是否被用
- Resume MEMORY entries for PR #5694

---

### 🟡 Tier 2 — 正常列出,別擺最前面,別自詡專家

### Gin

- **是什麼**:Go 最流行的 HTTP framework
- **我哪用過**:commeet-text2sql Phase 3 從 net/http 遷移過去
- **5 秒答案**:「Gin 是 HTTP framework with middleware composition · I used it in commeet-text2sql for `/api/*` routes + requestid + recovery middleware」
- ⚠️ 別深問 internals(`gin.Engine` 細節)

### Cobra

- **是什麼**:Go CLI framework(spf13/cobra)
- **我哪用過**:booking-cli 主 CLI 設計
- **5 秒答案**:「Cobra 是 CLI library · I used it for `booking-cli` subcommand tree(server / recon / saga-watchdog / expiry-sweeper)」

### testcontainers-go

- **是什麼**:integration test 用 Docker 起真實 dependency 的 library
- **我哪用過**:booking_monitor `test/integration/postgres/` 套件,40 個整合測試
- **5 秒答案**:「testcontainers-go 啟動 postgres:15-alpine container 跑整合測試 · 我用在 OrderRepository / EventRepository / UoW 套件,~40 tests in 38s」

### gomock (uber/mock)

- **是什麼**:Go interface mock generator
- **我哪用過**:commeet-text2sql Phase 2 migrate 進來
- **5 秒答案**:「uber/mock 是 maintained fork of golang/mock,用 `//go:generate mockgen` 產 mock · 我在 Provider + Executor interface 上用」

### testify

- **是什麼**:Go test 輔助 library(assert, require, mock, suite)
- **我哪用過**:全部專案都用
- **5 秒答案**:「testify 提供 assert / require / mock helper · 我每個 Go 專案都用,booking_monitor + commeet-text2sql 都是」

### Docker (multi-stage)

- **是什麼**:容器化平台 + Dockerfile multi-stage build 縮小 image
- **我哪用過**:Commeet + booking_monitor 都用
- **5 秒答案**:「Multi-stage 是 build stage 編譯 → final stage 只放 binary · 我用在 booking_monitor 跟 Commeet,non-root + pinned base image」

### GitHub Actions

- **是什麼**:GitHub 原生 CI/CD
- **我哪用過**:booking_monitor `.github/workflows/ci.yml` — 4 job parallel(test+race / lint / govulncheck / docker build)
- **5 秒答案**:「booking_monitor CI 配 4-job parallel pipeline · 跑 race detector + golangci-lint + govulncheck + docker build」

### Prometheus / Grafana

- **是什麼**:monitoring stack(metrics + dashboard)
- **我哪用過**:booking_monitor 36+ metrics / 24 alerts / 18 panels;Commeet 業務 metric
- **5 秒答案**:「Prometheus 收 metric / Grafana 視覺化 · booking_monitor 我設計了 RED + USE + domain 三類 metric,每個 alert 有 runbook_url annotation」

### OpenTelemetry / Jaeger

- **是什麼**:distributed tracing standard + viewer
- **我哪用過**:booking_monitor 用 OTEL auto-injection
- **5 秒答案**:「OTEL 是 vendor-neutral tracing standard · 我把 correlation_id / trace_id / span_id 自動注入 structured log,Jaeger 看 trace」
- ⚠️ 別深問 OTEL SDK 細節

### OpenAI Embedding API

- **是什麼**:OpenAI 的 vector embedding API
- **我哪用過**:commeet-text2sql RAG pipeline
- **5 秒答案**:「OpenAI text-embedding API 把 schema document 變 vector 餵 pgvector 做 RAG · 我加了 selective retry(429/5xx)+ exponential backoff + ctx-aware sleep」

### SSE (Server-Sent Events)

- **是什麼**:HTTP-based unidirectional streaming(server → client)
- **我哪用過**:commeet-text2sql workflow trace 即時推播
- **5 秒答案**:「SSE 是單向 HTTP streaming · text2sql 用來把 agent 每個 stage 的 progress 即時推到 frontend」

---

### 🟠 Tier 3 — 用「Familiar with」措辭,別深 highlight

### Kubernetes

- **真實程度**:你 deploy service **到** K8s,不是 cluster operator
- **5 秒答案**:「Familiar with deploying Go services to K8s · `/livez` + `/readyz` probe pattern, helm chart updates · not a cluster operator」
- ⚠️ 被問 pod CrashLoopBackOff debug → 坦白「我會看 events / describe / logs,深度 cluster troubleshooting 是我想學的方向」

### Pyroscope

- **真實程度**:你 integrated 過,但沒用 flame graph 排過真實 bug
- **5 秒答案**:「Pyroscope 是 continuous profiling tool · 我在 Commeet 啟動配進來,實際 flame graph debugging 經驗有限」
- ⚠️ **可以不放履歷**,如果不確定能講

### Alertmanager

- **真實程度**:你 booking_monitor 配過 routing + inhibition,但沒手動處理過 paging incident
- **5 秒答案**:「Alertmanager 是 Prometheus 的 dedup + grouping + routing layer · 我配過 booking_monitor 的 severity-specific cadence(critical 30m / warning 4h)+ inhibition(`RedisStreamCollectorDown` 抑制其他 stream alert)」

### pprof

- **真實程度**:你 booking_monitor 開了 pprof endpoint,但沒實際拿 profile 找 bug 過(or 少數幾次)
- **5 秒答案**:「pprof 是 Go 內建 profiler · 我在 booking_monitor 開了 `/debug/pprof` endpoint(loopback only)+ 設過 `runtime.SetBlockProfileRate`,深度 profiling 場景有限」
- ⚠️ **如果不熟,可以不放**

### Python

- **真實程度**:寫過 scripts,不熟 FastAPI / asyncio / Django
- **5 秒答案**:「Python 用於 scripting + data processing · 不是日常生產語言」

### TypeScript / JavaScript

- **真實程度**:只有 booking_monitor D8 demo(Vite + React + TS),沒做過 production frontend
- **5 秒答案**:「TS / JS 用於 booking_monitor D8 browser demo (Vite + React + TS) · 不是日常生產語言」

---

### 🔴 Tier 4 — 不要列(沒實作過)

| 技術 | 為什麼不列 |
| --- | --- |
| gRPC | 你 PR 沒用過 |
| GraphQL | 同上 |
| LangChain / LlamaIndex | text2sql 是自己寫不用 framework |
| AWS / GCP cloud services | 沒看到具體服務使用證據 |
| Terraform / IaC | 沒看到 |
| Elasticsearch | 沒看到 |
| MongoDB | 沒看到 |
| Microservice mesh (Istio / Linkerd) | 沒看到 |
| Spark / Hadoop | 沒看到 |

---

### 工作流 — 怎麼用這份 Part 8 review

### 面試前 1 週(認真版)

- 每天挑 5 個 Tier 1 stack,**口述 5 秒答案 + 深度題答案 1 次**
- 念給自己聽,卡頓 = 沒準備好

### 面試前 1 天(快速版)

- 全部 Tier 1 過一遍 5 秒答案(口述,不看 doc)
- Tier 2 過一遍 5 秒答案
- Tier 3 念一次,確認措辭

### 面試前 30 分鐘

- Part 5 memory refresh table 看一次
- Tier 1 前 5 個再口述一次
- 喝水 + 深呼吸

### 平日(履歷更新時)

- 想加新 stack 之前,先想:「我能 5 秒答出嗎?」答得出 → 在這裡 tier 分類後加進履歷;答不出 → 不加,或先補實戰再加

### 約我做 mock interview

- 跟我說「quiz me on tier 1」或「quiz me on text2sql stack」或「quiz me randomly」
- 我會挑 stack 問你 5 秒答案,卡頓我會點出來

### 看到新 JD 想 tailor 履歷

- 比對 JD 要求 keyword vs 你 tier audit
- JD 要 Tier 1 你也是 Tier 1 → 直接 highlight
- JD 要 Tier 3 你也是 Tier 3 → 列出但別吹
- JD 要的你是 Tier 4(沒做過)→ **誠實面對:這個 JD 可能不適合,或你要 1-2 個 weekend 學一下再投**

---

## Part 9 — Flashcards · 主動回憶練習

> **用法**:遮住右欄,看左欄 Q,口述答案,再看右欄 verify。卡頓 = 還沒準備好。
**目標**:30 張卡每張 ≤ 10 秒答出來。
> 

### Set A — 數字 / 事實(背完才有 credibility)

| Q | A |
| --- | --- |
| 「Slow query 改善多少?」 | 12.4s → 277ms,**45×**,PR #5694(GORM `.Table()` 沒套 soft-delete scope) |
| 「Startup latency 改善多少?」 | 546ms → 204ms,**62%**,PR #4537(GORM migration pre-filter + async infra + critical-path 重構) |
| 「N+1 elimination 多少 pattern?」 | **14 個 N+1 pattern**(PR #5546)+ 4 batch queries 取代 4×N |
| 「Multi-tenant data leak 修了幾個 function?」 | **8 個 SQL function**(PR #5769)`is_all_dept` EXISTS 漏 outer correlation |
| 「commeet-text2sql 多少 PRs / 多久?」 | **36 PRs / 3 個月 / solo build** |
| 「booking_monitor 多少 PRs / 多少 release?」 | **111 PRs / 6 release(v0.1.0 → v1.0.0)/ 3 個月** |
| 「ReAct budget 多少?」 | `MaxReActTurns = 5`(default) |
| 「Intent classifier 多少類別?」 | **5 類**:expense / approval / user / doc / other |
| 「Column Pruner CHESS 研究說多少?」 | **+2% BIRD accuracy / 5× token reduction** on large schemas |
| 「booking_monitor 5-stage benchmark 數字?」 | Stage 1: 1,640/s(PG 行鎖)· Stages 2-4: ~8,400/s(Redis 單 key Lua 序列化)· Stage 5: 5,139/s(Kafka acks=all −39%) |
| 「Cache-truth incident 多少訊息消失?」 | **411 of 1000**(`make reset-db` 用了 FLUSHALL) |
| 「D7 刪了多少 LOC?」 | **2,500 LOC**(legacy auto-charge path) |
| 「booking_monitor 多少 metric / alert / panel?」 | 40+ metrics / 24 alerts / 18 Grafana panels |
| 「Commeet 任期內多少 PRs?」 | **700+ merged PRs**(across `cts-member-server` + `commeet-text2sql`) |
| 「LLM Wave-DAG scheduler 改善?」 | **5 sequential → 3 waves → 40% latency reduction** |

### Set B — 概念 / 設計(必背 30 秒答案)

| Q | A(30 秒,可念可背) |
| --- | --- |
| 「Walk me through the architecture」 | HTTP → Nginx → Gin → BookingService → Lua deduct → orders:stream → Worker → PG(同 tx 寫 outbox)→ Relay → Kafka → Saga consumer。4 個 sweeper 在獨立 process。[暫停,等追問] |
| 「Idempotency 怎麼做?」 | **4 層**。**API**: `Idempotency-Key` header + Stripe-style body fingerprint。**Worker**: DB UNIQUE partial index on `(user_id, event_id)`。**Saga**: Redis SETNX on `saga:reverted:{order_id}`。**Gateway**: Stripe-side `Idempotency-Key` keyed on order_id。 |
| 「Outbox pattern 怎麼運作?」 | 同一個 PG transaction 寫 order row + outbox event row → OutboxRelay(`pg_try_advisory_lock(1001)` leader election)poll outbox → publish Kafka → MarkProcessed。為什麼:跨 PG + Kafka 沒 2PC(實務 driver 不暴露)。 |
| 「Saga 怎麼處理失敗?」 | Garcia-Molina 1987 §5 — saga 只管 failure recovery,不管 happy path。D7 narrowing 刪了 happy path 2,500 LOC。`order.failed` 只剩 2 個 production emitter:D5 webhook + D6 expiry sweeper。 |
| 「為什麼用 Redis Lua 不直接用 INCR/DECR?」 | 因為要 **atomic** DECRBY + HMGET + XADD + amount_cents math 在 single Redis call。Lua 在 Redis 內 single-thread atomic 執行,省 round-trip 而且保證 atomicity。 |
| 「Slow query 怎麼修?」 | EXPLAIN ANALYZE 找 Seq Scan → 追到 GORM `.Table()` 沒自動套 soft-delete scope → query 沒帶 `delete_at IS NULL` → planner 推導不出 partial index 條件 → fallback Seq Scan → 加上條件後 12.4s→277ms。 |
| 「Multi-tenant 怎麼保證 isolation?」 | 每個 user-access SQL 都要 `company_id` filter。關鍵洞察:`is_all_dept` EXISTS subquery 要 correlate to **outer** `department.company_id`,不然 Company A 有任一 has-all user 就會讓 EXISTS 對全世界 department true。 |
| 「為什麼 LLM agent 用 Plan-and-Execute 不用純 ReAct?」 | Text2SQL 是 **凝聚式任務**(有固定 schema,不是開放式 reasoning)。Plan-and-Execute 穩定 + cost-friendly(plan 用大模型一次,execute 用小模型多次)。ReAct 在 single-step 內當作 self-correction loop 用。 |
| 「Pattern A 是什麼?為什麼這樣設計?」 | Stripe Checkout / KKTIX 模式。`POST /book` 變預訂(不直接扣款)→ 客戶端 `POST /pay` 拿 PaymentIntent → confirm 後 webhook 回 server。好處:client-side payment flow 解耦 + reservation expiry sweep + saga 補償。 |
| 「Cooperative deadline chain 是什麼?」 | HTTP middleware(90s)→ Orchestrator → GORM `WithContext` → pgx 取消 → PostgreSQL `statement_timeout=30s` via libpq options。Strict 30s < 90s < 120s transport hierarchy,確保 handler 有 30s headroom 寫結構化 504。 |

### Set C — 「不知道」的安全答案

| Q | 答案 |
| --- | --- |
| 「Kafka 的 ISR 機制?」(我沒深入用過) | 「我用 Kafka 偏 producer/consumer 跟 outbox pattern。ISR replica state machine 細節我**只到 high-level 知道是 in-sync replica set**,沒 operate 過 Kafka cluster。如果這個 role 需要這個,我可以 1-2 週補上,但不會假裝有。」 |
| 「gRPC streaming 你做過嗎?」 | 「沒在 production 做過。我用 REST + SSE(在 commeet-text2sql)做 streaming。如果 role 需要 gRPC,我熟 protobuf + Go server interface 概念,實作上需要 ramp-up 1-2 週。」 |
| 「K8s operator 你寫過嗎?」 | 「沒寫過 operator。我 deploy services 到 K8s,熟 deployment / service / probe 但不是 cluster operator。」 |
| 「LeetCode 你練多少?」 | 「老實說我把時間花在 production code + system design + open-source portfolio,LeetCode 練得少。如果 coding round 是 medium difficulty Go problem,我覺得我可以做;hard graph / DP 那種我會卡。」 |

### Set D — 個人 / 行為(B1-B6 速記)

| Q | 我講哪個 STAR? |
| --- | --- |
| Conflict / disagreement | B1 自定義簽核流程 4 boolean vs error codes |
| Weakness | B2 public technical writing |
| Why leave current job | B3 學東西在 Commeet 路線圖之外 |
| Why this company | B3 客製版(投履歷前 30 分鐘 google) |
| Tell me about failure | B4 cross-company data leak + post-mortem |
| 5 years from now | B5 senior IC track + production AI |
| Why hire you | B6 solo build + 跨領域 + 自主學習速度 |

---

## Part 10 — 通用系統設計題 playbook

> 你的 3 個 killer project(booking_monitor / text2sql / approval)很強但是**自己的故事**,interviewer 通常會額外要求 **generic SD problem** 確認你 transferable。下面是 4 個必準備:
> 

### SD1: Rate Limiter

**Trigger word**: 「設計一個 API rate limiter」

**60 秒框架**:
1. **Requirement**: per-user 還是 per-IP? Token rate? Burst? Distributed across multiple servers?
2. **Algorithm 選擇**: Token Bucket(允許 burst)/ Leaky Bucket(平滑)/ Sliding Window(精確)/ Fixed Window(簡單但邊界尖峰問題)
3. **儲存**: 單機 → in-memory map + mutex;分散式 → Redis(`INCR` + `EXPIRE` 或 `Lua` script atomic)
4. **Data structure**: Redis Sorted Set(score = timestamp)做 sliding window
5. **Edge case**: clock skew、Redis 掛了 fallback、被 rate limit 的 response code(429 + `Retry-After` header)

**你的真實 leverage**: booking_monitor 用 nginx + Lua rate limit,你**真的用過**,可以直接拿來講

**深度題**:
- 「為什麼選 Token Bucket 不 Sliding Window?」→ Token Bucket O(1) 計算,Sliding Window 要存歷史 events
- 「Redis 掛了怎麼辦?」→ Fail open(允許通過)還 Fail closed(全擋)?業界 SOP 是 fail open + alert

### SD2: URL Shortener

**Trigger word**: 「設計 bit.ly」

**60 秒框架**:
1. **Requirement**: read-heavy(read:write ~100:1)、custom alias 要不要、analytics 要不要、TTL 要不要
2. **Hash strategy**: 不用 MD5/SHA(太長 + collision 不可控);**用 Base62 encode auto-increment ID**(短、可逆、無 collision)
3. **DB**: PG 存 (id, long_url, created_at, user_id) — id PK auto-increment
4. **Cache**: Redis cache (short_code → long_url),read-heavy 場景命中率 > 95%
5. **Scaling**: 用 sharding by `short_code` 第一個字元(62 個 shard)
6. **Edge case**: malicious URL filter、URL 被刪除如何處理、analytics 怎麼算(不要每次 read 都寫 DB → batch / Kafka)

**你的真實 leverage**: booking_monitor 你做了 5-stage benchmark,**「sync vs async」trade-off** 可以直接拿來討論 analytics 寫入路徑

**深度題**:
- 「302 還是 301 redirect?」→ 301 cache forever,302 每次都打 server。短連結通常用 302(才能算 analytics)
- 「Custom alias 跟 hash 衝突?」→ Custom alias 進 reserved set,hash 跳過

### SD3: Chat / Messaging System(WhatsApp / Slack lite)

**Trigger word**: 「設計即時聊天」

**60 秒框架**:
1. **Requirement**: 1-on-1 還是 group?last-seen / read receipt 要不要?歷史訊息存多久?
2. **Connection**: WebSocket(long-lived bidirectional);如果跨多個 server → user-to-server mapping + Redis Pub/Sub 做 server-to-server
3. **Message delivery**:
- **Fan-out on write**(發送時就複製到每個 recipient inbox)→ read 快,write 重 → group chat 不適用
- **Fan-out on read**(發送只寫 conversation,read 時拉)→ write 輕,read 要 join → large group 適用
4. **DB**: 訊息存 Cassandra(time-series friendly)或 PG(小規模)
5. **Edge case**: offline delivery / push notification、message ordering(per-conversation strictly ordered)、duplicate delivery 防護(client-side dedup by message_id)

**你的真實 leverage**: SSE streaming(commeet-text2sql)、Redis Stream consumer group(booking_monitor)— **async message delivery** 你做過

**深度題**:
- 「訊息順序怎麼保證?」→ Per-conversation single partition(Kafka)/ per-conversation lock + sequence number
- 「離線通知怎麼做?」→ Server detect connection close → APNs / FCM push;但**不要重送 in-flight 訊息**,client 重連時用 last_message_id pull

### SD4: Notification System

**Trigger word**: 「設計通知系統」(email / SMS / push / in-app)

**60 秒框架**:
1. **Requirement**: 多 channel(email + SMS + push + in-app)、template 系統、用戶 opt-out、retry policy
2. **Architecture**:
- Trigger source(business event)→ **Kafka topic** → notification dispatcher
- Dispatcher 根據 user preference 路由到 channel-specific worker(email worker / SMS worker / push worker)
- 每個 channel worker 處理 retry + dead letter queue
3. **Idempotency**: notification_id 為 key,worker 用 SETNX 防止重複送
4. **Template engine**: 存 PG,worker 從 PG 拉 template + render
5. **Priority**: 高 priority queue(security alert)+ 低 priority queue(marketing),用不同 Kafka topic
6. **Edge case**: rate limit per-user(不要 spam)、quiet hours(timezone-aware)、bounce / unsubscribe handling

**你的真實 leverage**: booking_monitor 的 **outbox + Kafka + saga** 模式直接對應 notification dispatch + retry

**深度題**:
- 「Email 跟 push 失敗 retry 策略?」→ Email 5xx 可 retry(SMTP transient);4xx 不 retry(永久失敗,進 DLQ)。Push 看 vendor SDK feedback
- 「怎麼處理 user 短時間收到太多通知?」→ Per-user rate limit + batching window(例如「5 分鐘內 ≥ 3 個通知就改 digest」)

### SD 通用 framework — 不管什麼題目都先講

```
1. 釐清需求(2 分鐘):QPS / data scale / read-write ratio / latency SLA / consistency requirement
2. High-level architecture(3 分鐘):box-and-arrow diagram
3. Deep dive 1-2 個 component(15 分鐘):看 interviewer 引導
4. Trade-off discussion(5 分鐘):你的選擇 vs alternative + 為什麼
5. Scaling / failure mode(5 分鐘):雙倍流量怎辦?哪裡會先掛?
```

---

## Part 11 — 薪資談判 & offer handling

### 基本心法

**永遠不要先報數字。** 讓對方先報 range,你再 react。

### 對方問「你的期望薪資?」

**標準腳本**:
> 「I’d love to hear the range for this role first — 我想確保 my expectation 對齊你們的 budget,不要互相浪費時間。」

**他們不肯先報**(常見 TW 公司戰術):
> 「OK,based on 我的 ~4 年經驗 + production Go + LLM agent 工程,加上 [具體技能對 JD],我預期是 [大方向 — 不要數字] competitive with mid-level BE roles at similar-stage companies。具體數字我希望基於了解你們的整體 package 後再討論。」

**他們堅持要數字**(broken record 戰術):
> 「OK,based on 我的研究,台灣 mid-level Go BE 月薪通常 70k-110k 區間,具體看公司規模 / 等級設計 / total package。我希望落在這個 range 中上段,**但這是 starting point,not a hard requirement** — 我更關心 role fit + 成長空間。」

### Counter-offer 怎麼回

收到 offer 數字後**第一反應永遠是**:「Thank you very much. I’d like 2-3 days to consider — can I get back to you by [具體日期]?」**不要當場接受,也不要當場 counter。**

### Counter template(email,3-5 天後)

```
Subject: Re: Offer for [Position] — Thoughtful response

Dear [HR name],

Thank you again for the offer for [Position]. I've spent the past few days carefully evaluating
the opportunity and discussing with my family/mentors. I'm very excited about [specific thing about the company].

I'd like to discuss the compensation package. Based on:
- My 4 years of professional experience including 2+ years of production Go
- The technical scope of the role (especially [specific aspect])
- Market data I've gathered for similar mid-level BE roles in Taiwan / [region]
- Another opportunity I'm currently in late stages with [optional: only if true]

I would feel more confident accepting at [number 10-15% above offer] base, with [specific ask:
sign-on bonus / RSU vesting acceleration / WFH days / start date flexibility].

I want to be clear that I'm very interested in joining the team, and I'm open to discussing
the structure. What flexibility do you have?

Best,
Leon
```

### 常見談判 levers(優先序)

| Lever | TW startup 常見? | 講法 |
| --- | --- | --- |
| Base salary +10-15% | 有空間 | “I’d feel more confident at X” |
| Sign-on bonus | 中型以上常見 | “Help bridge my notice period / equity loss from current company” |
| RSU acceleration / 增加 | 上市公司 / 已 Series B+ | “I’d like to see vesting cliff reduced to 6 months” |
| WFH days / hybrid arrangement | 大多談得到 | “I work better with 2-3 WFH days based on my current setup” |
| Start date flexibility | 一定能談 | “I need 4 weeks notice to my current team” |
| 年假 days | 有限空間 | 通常法定 + 公司政策 |
| Title 升一階 | 難談 | 不要為了 title 阻礙 offer,看實質工作內容 |

### RSU 速記

| 詞 | 意義 | 注意 |
| --- | --- | --- |
| Vesting period | 通常 4 年 | 標準 |
| Cliff | 1 年(年滿才開始 vest,離職前都拿 0) | 拿 cliff 前離職 = 全沒 |
| Vesting schedule | 通常 1y cliff + monthly/quarterly thereafter | 細節要問 |
| Refresher | 入職後每年再給(top-tier 公司) | TW 中型很少給 |
| 401(k) match / 退休金 | 美商必看 | TW 公司 6% 法定 |

### 拒絕 offer 怎麼說(別燒橋)

```
Subject: Re: Offer for [Position] — Difficult decision

Dear [HR name],

Thank you so much for the offer and for the time you and the team have invested in this process.
This was a genuinely difficult decision — I have huge respect for [specific thing about company].

After careful consideration, I've decided to accept a different opportunity that aligns
more closely with [specific reason — e.g., my AI/ML interest, larger-scale distributed systems,
geographic flexibility]. I want to emphasize this is NOT a reflection on [Company] — your team
and product are excellent.

I'd love to stay in touch, and I genuinely hope our paths cross again. Thank you for considering me.

Warm regards,
Leon
```

### 同時拿到多 offer 的協調

- **早一點 offer 主動告知對方**:「我正在 final round with another company, expected decision by [date]. Would you be willing to extend your offer deadline so I can make a fully-informed decision?」
- **大多公司願意延 1-2 週**(他們不想 lose to ghosting)
- **不要用 fake offer 唬人** — 業界圈子小,被抓到 reputation 毀

---

## Part 12 — 反問 interviewer 的問題庫

> 每輪面試結束 interviewer 都會問「Do you have questions for me?」**答 No 是大忌**。每輪準備 3-5 個,挑跟當輪 interviewer role 對應的問。
> 

### Round 1: HR / Recruiter

| 問題 | 為什麼問這個 |
| --- | --- |
| 「What does the interview process look like from here?」 | 安排自己時間 |
| 「Who would I be interviewing with in the next rounds?」 | 提前 LinkedIn 看對方背景 |
| 「How would you describe the engineering culture in 3 words?」 | HR 講不出來就是壞 sign |
| 「What’s the team currently looking to improve / change?」 | 找 pain point |
| 「When would you be looking for someone to start?」 | 對齊 start date |

### Round 2: Hiring Manager / Tech Lead

| 問題 | 為什麼問這個 |
| --- | --- |
| 「What are the top 2-3 challenges this team is solving in the next 6 months?」 | **你 onboard 進去要面對什麼** |
| 「How does the team make architectural decisions? Design docs? RFC? 1-on-1?」 | 是否 disciplined engineering culture |
| 「How is on-call handled? Rotation? Compensation? Alert volume?」 | 工作品質訊號 — 上面 3 個答不清楚 = 不健康 |
| 「What does success look like for someone in this role at 6 months / 1 year?」 | 對齊期望 |
| 「What’s the most challenging technical decision the team made recently?」 | 看他答得是否 substantive |

### Round 3: Peer Engineer

| 問題 | 為什麼問這個 |
| --- | --- |
| 「Walk me through a typical day for you — meeting load, focus time split?」 | 真實工作節奏 |
| 「Last 3 things you shipped — what was the trickiest part?」 | 對方真正在做什麼 |
| 「How does code review work here? Who reviews? Turnaround time?」 | 工程紀律訊號 |
| 「What’s something you wish was different about working here?」 | **誠實 feedback,壞答案 = 救命** |
| 「What’s the dev environment like — local? Devcontainer? Cloud workspace?」 | 你會花多少時間 setup |

### Round 4: CTO / Founder / VP

| 問題 | 為什麼問這個 |
| --- | --- |
| 「Where do you see the company in 3 years?」 | Vision 清楚度 |
| 「What’s the biggest technical bet the company is making?」 | 看 leader 思考深度 |
| 「How do you think about technical debt vs new feature trade-off at the leadership level?」 | 文化訊號 |
| 「What kind of engineer thrives here — vs struggles?」 | 對齊自己 fit |
| 「If you could go back 6 months, what one engineering decision would you change?」 | 看是否 honest about mistakes |

### ⚠️ 不要問的問題

| ❌ | 為什麼不問 |
| --- | --- |
| 薪資範圍 | HR round 適合,但 hiring manager / peer round 不適合 |
| 「我做這個 role 多久可以升 senior?」 | 太早問 |
| 「公司會 IPO 嗎?」 | 沒人能答 |
| 「我每天工作幾小時?」 | 用「typical day」side-question 取代 |
| 「你們有 WFH 嗎?」 | offer round 再談 |
| 已經在 JD / 公司網站寫得很清楚的東西 | 顯示沒做功課 |

### Recovery script — 想不出問題時

> 「我準備了幾個問題剛剛在前面問完了。再想一個的話 — [挑一個 trade-off Q]:**對你個人來說,你目前最 enjoy 的工作項目跟最 frustrating 的工作項目分別是什麼?**」
> 

這個問題萬用,而且 interviewer 通常 enjoy 講自己的故事。

---

## Part 13 — 失誤恢復腳本(關鍵)

> 面試中**一定**會有不知道 / 答錯 / 卡住的時刻。差別在於 senior 候選人**有腳本可以恢復**,junior 候選人 freeze。
> 

### 情境 1: 「I don’t know」(完全沒概念)

**❌ 壞答法**:
- 沉默
- 隨便編
- 「我不知道」然後尷尬

**✅ 好答法**:
> 「Honestly,this is outside my experience. **Let me think out loud about how I’d approach it if I had to figure it out** — 我會先 [step 1: research / consult docs / ask a senior],然後 [step 2: 寫小規模 prototype],然後 [step 3: production rollout]. **這個我願意 1-2 週補上,但不會假裝有經驗。**」

**為什麼好**:
- 誠實
- 顯示 **how you learn**(senior 想看的)
- 不裝懂

### 情境 2: 答了一半發現答錯

**❌ 壞答法**:繼續硬講

**✅ 好答法**(主動承認 + 修正):
> 「**等等,讓我修正一下** — 我剛剛說 [X],我想了一下其實應該是 [Y],因為 [reason]. 不好意思,腦袋熱了一下。」

**為什麼好**:
- Self-correcting 是 senior 訊號
- 比錯下去更顯示 self-awareness

### 情境 3: 完全卡住,長時間沒講話

**❌ 壞答法**:繼續沉默

**✅ 好答法**:
> 「**Let me think out loud** — 我目前的思路是 [partial idea]. 我覺得我卡在 [specific point]. **Can I ask you a clarifying question** to narrow down: [specific question]?」

**為什麼好**:
- 把 silence 變 collaboration
- Clarifying question 是 senior 訊號(senior 知道 ambiguity 是 setup 的一部分)

### 情境 4: 被指出答錯

**❌ 壞答法**:
- 辯護
- 道歉太多
- 接著錯下去

**✅ 好答法**:
> 「Got it,thanks for the correction. So if I understand correctly, [restate correct answer]. **Does that match your understanding?** [pause]. **I learned something** — I’ll go back and verify this.」

**為什麼好**:
- 不辯護
- 確認理解
- 顯示 growth mindset

### 情境 5: 系統設計題畫到一半畫錯

**❌ 壞答法**:全擦掉重畫

**✅ 好答法**:
> 「Actually,let me revise this — I think [component X] should go here,not there,because [reason]. **我把這個 box 拉過來**。」(用箭頭 / 紅筆標)

**為什麼好**:
- Iteration 是 senior 設計訊號
- 比 erase + redraw 顯示 thinking process

### 情境 6: Coding round 寫錯一個邊界

**❌ 壞答法**:慌張改

**✅ 好答法**:
> 「I see I missed an edge case here — empty input. Let me add a guard at the top. **Are there other edges you’d want me to handle? Off-by-one in the loop bounds, perhaps?**」

**為什麼好**:
- 把 mistake 變 case analysis
- 主動邀請 interviewer feedback

### 情境 7: 整個面試自我感覺很糟

**面試中**:不要表現出 — 繼續 professional。

**面試結束**:
1. **24 小時不檢討** — 情緒會放大失誤
2. **24 小時後寫 post-mortem**:被問了什麼?哪題答好?哪題答差?**bullet 化記錄,不要 narrative 抱怨**
3. **把 bug 加進 Part 8 速查表 / Part 9 flashcards**,下次不會犯同樣錯
4. **不要因為一場差就 frozen 不投履歷** — 每場面試都是 sample,差場是 sample 中的 noise,不是 trend

### 情境 8: 整場結束後 recruiter 沒消息

**1 週後**:

```
Subject: Following up — [Position] interview

Dear [name],

Hi, hope you're doing well. I had my [round name] interview on [date] and wanted to gently
check in on next steps. I remain very interested in [Position] at [Company] and would love
to hear any updates whenever you have them.

Thanks again for your time.

Best,
Leon
```

**2 週後沒回**:再 follow up 一次(更短)。
**3 週後沒回**:認定 silently rejected,move on(但不要對人扣分)。

---

## Part 14 — 面試後協議 thank-you + follow-up

### 24 小時內 — Thank-you note

每場面試後**24 小時內**寄,**1-2 段**。EN 跟 zh-TW 範本各一:

### EN Template

```
Subject: Thank you — [Position] interview

Dear [Interviewer first name],

Thank you for taking the time to talk with me today about [Position]. I especially enjoyed
our discussion about [specific topic from interview — e.g., the saga compensation design
trade-off / your team's recent migration to event-driven architecture].

[One sentence reaffirming interest + one specific thing you bring]. For example:
"I'm excited about the prospect of contributing to [specific company initiative], particularly
given my experience with [your strong skill]."

Please let me know if there's any additional information I can provide.

Best,
Leon Li
```

### zh-TW Template

```
Subject: 感謝今日面試 — [Position]

[Interviewer 稱呼],您好,

非常感謝您今天撥冗與我面談 [Position] 的職缺。我特別享受我們關於 [面試中的具體技術討論]
的對話 — 您提到的 [某個具體點] 給了我新的思考角度。

[一句重申興趣 + 一個你帶來的具體價值]。例如:
我對於有機會貢獻在 [公司的具體 initiative] 感到興奮,我在 [你的強項] 的經驗應該可以
直接 apply。

如果您需要任何補充資料 — code sample、portfolio link、reference — 都歡迎告訴我。

謝謝,
Leon Li
```

### 1 週後 — 沒消息怎麼辦

Follow-up:見 Part 13 情境 8 template。

### 2 週後 — 拿到 offer 但想等

**禮貌延長 deadline**:

```
Subject: Re: Offer for [Position] — Decision timing

Dear [HR name],

Thank you again for the offer. I'm currently in [late stage / final rounds] with one other
opportunity, and I want to make a fully-informed decision. Would it be possible to extend
my decision deadline by [N days / to specific date]?

I really appreciate your patience and want to be transparent about my timing.

Best,
Leon
```

### 收到 rejection 怎麼回

```
Subject: Re: [Position] update

Dear [name],

Thank you for letting me know, and for the team's time. While I'm disappointed, I really
appreciated the conversations and the chance to learn about [specific thing about company].

If you have any specific feedback that would help me grow, I'd genuinely welcome it.
Either way, I'd love to stay in touch — and I hope our paths cross again.

Best,
Leon
```

**為什麼回?**
- 70% 的 rejection 不是「你不夠好」是「fit / timing 不對」
- 6 個月後 reopen / 加開新 role 時,recruiter 會記得 graceful 的候選人
- 該公司的其他 recruiter 可能 transfer 你 profile 給其他 team

### Reject decline / accept template

見 Part 11 末尾。

---

## 工作 workflow — 把 Part 9-14 用起來

| 時機 | 動作 |
| --- | --- |
| 面試前 1 週 | Part 2 + Part 2.5 + Part 5 + Part 8 — 每天念一遍,口述卡頓的記下來 |
| 面試前 1 天 | Part 9 flashcards 全套至少 1 輪 + Part 12 反問挑 5 個對應 round 的 |
| 面試前 30 分鐘 | Memory refresh table + Part 9 Set A (數字) + Part 13 失誤恢復腳本快速念一次 |
| 面試後 30 分鐘內 | 寫 Part 14 thank-you note |
| 面試後 24 小時 | 寫 post-mortem(被問什麼 / 答得怎樣 / Part 8/9 缺哪題) |
| 收到 offer | Part 11 counter-offer / RSU / 多 offer 協調 |
| 收到 rejection | Part 14 graceful decline reply |

---

## 📚 References Appendix(所有外部連結集中)

> 通勤時遇到不熟的概念,**直接點連結讀原文**比我這份 doc 更權威。下面是按主題分組的所有 reference。
> 

### Go 語言核心

| 主題 | Link |
| --- | --- |
| GMP 模型 (官方 blog) | https://go.dev/blog/scheduler |
| Concurrency Patterns: Pipelines | https://go.dev/blog/pipelines |
| Concurrency Patterns: Context | https://go.dev/blog/context |
| Working with Errors in Go 1.13 | https://go.dev/blog/go1.13-errors |
| Errors are values | https://go.dev/blog/errors-are-values |
| The Laws of Reflection | https://go.dev/blog/laws-of-reflection |
| Range over function types (1.23) | https://go.dev/blog/range-functions |
| Swiss Table maps (1.24) | https://go.dev/blog/swisstable |
| 深入 Go 語言設計與實現 (draveness) | https://draveness.me/golang/ |
| GMP 深入解析 (cyub) | https://go.cyub.vip/gmp/gmp-model.html |
| errgroup 官方文件 | https://pkg.go.dev/golang.org/x/sync/errgroup |
| Garbage Collector journey | https://go.dev/blog/ismmkeynote |

### PostgreSQL

| 主題 | Link |
| --- | --- |
| EXPLAIN 文件 | https://www.postgresql.org/docs/current/using-explain.html |
| Index types | https://www.postgresql.org/docs/current/indexes.html |
| Partial Indexes | https://www.postgresql.org/docs/current/indexes-partial.html |
| Transaction Isolation | https://www.postgresql.org/docs/current/transaction-iso.html |
| Advisory Lock | https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS |
| `pg_stat_user_indexes` | https://www.postgresql.org/docs/current/monitoring-stats.html#MONITORING-PG-STAT-ALL-INDEXES-VIEW |
| Use The Index, Luke! | https://use-the-index-luke.com/ |
| MVCC 詳解 (小林 coding) | https://xiaolincoding.com/mysql/transaction/mvcc.html |

### Redis

| 主題 | Link |
| --- | --- |
| Redis Streams | https://redis.io/docs/data-types/streams/ |
| Lua scripting | https://redis.io/docs/interact/programmability/eval-intro/ |
| Martin Kleppmann — 分散式鎖批判 | https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html |
| Redis 設計與實現 (圖解) | https://redisbook.com/ |
| 小林 coding — Redis 速記 | https://xiaolincoding.com/redis/ |
| go-redis 文件 | https://redis.uptrace.dev/guide/go-redis.html |
| redsync (分散式鎖 Go 實作) | https://github.com/go-redsync/redsync |

### Kafka / Message Queue

| 主題 | Link |
| --- | --- |
| Kafka Intro | https://kafka.apache.org/intro |
| KIP-98 — Exactly Once Delivery + Transactional Producer | https://cwiki.apache.org/confluence/display/KAFKA/KIP-98+-+Exactly+Once+Delivery+and+Transactional+Messaging |
| Confluent — Transactions in Apache Kafka | https://www.confluent.io/blog/transactions-apache-kafka/ |
| RabbitMQ vs Kafka (CloudAMQP) | https://www.cloudamqp.com/blog/when-to-use-rabbitmq-or-apache-kafka.html |
| 小林 coding — 訊息隊列面試 | https://xiaolincoding.com/ |

### 分散式系統 / 模式

| 主題 | Link |
| --- | --- |
| Microservices (Martin Fowler) | https://martinfowler.com/articles/microservices.html |
| Saga Pattern (microservices.io) | https://microservices.io/patterns/data/saga.html |
| Transactional Outbox (microservices.io) | https://microservices.io/patterns/data/transactional-outbox.html |
| Garcia-Molina & Salem — Sagas (1987 原始 paper) | https://www.cs.cornell.edu/andru/cs711/2002fa/reading/sagas.pdf |
| Idempotency keys (Stripe blog) | https://stripe.com/blog/idempotency |
| Designing Data-Intensive Applications (book) | https://dataintensive.net/ |

### LLM Agent / Text2SQL

| 主題 | Link |
| --- | --- |
| CHESS — Contextual Harnessing for SQL (2024) | https://arxiv.org/abs/2405.16755 |
| BIRD benchmark | https://bird-bench.github.io/ |
| MAGIC — Multi-Agent SQL synthesis | https://arxiv.org/abs/2406.07882 |
| MAC-SQL — Multi-Agent Collaboration | https://arxiv.org/abs/2312.11242 |
| LITHE — Learning Optimizer Choices (EDBT 2026) | https://openproceedings.org/2026/conf/edbt/paper-93.pdf |
| Uber QueryGPT — engineering blog | https://www.uber.com/blog/query-gpt/ |
| Anthropic Claude API docs | https://docs.anthropic.com/ |
| OpenAI Embeddings | https://platform.openai.com/docs/guides/embeddings |
| LangChain (反例:你 NOT 用這個的理由) | https://python.langchain.com/docs/ |
| ReAct paper (Yao et al., 2022) | https://arxiv.org/abs/2210.03629 |

### Auth / Security

| 主題 | Link |
| --- | --- |
| JWT introduction | https://jwt.io/introduction |
| OAuth 2.0 (RFC 6749) | https://datatracker.ietf.org/doc/html/rfc6749 |
| OAuth 2.0 Threat Model (RFC 6819) | https://datatracker.ietf.org/doc/html/rfc6819 |
| PCI DSS SAQ A | https://www.pcisecuritystandards.org/document_library/ |

### API 設計

| 主題 | Link |
| --- | --- |
| Microsoft REST API Guidelines | https://github.com/microsoft/api-guidelines |
| Stripe API docs (idempotency, error codes) | https://stripe.com/docs/api |
| OpenAPI Specification | https://spec.openapis.org/oas/latest.html |
| gRPC Quick Start (Go) | https://grpc.io/docs/languages/go/quickstart/ |
| gRPC Tutorial (Go) | https://grpc.io/docs/languages/go/basics/ |
| Protobuf Language Guide | https://protobuf.dev/programming-guides/proto3/ |

### 系統設計 / 面試

| 主題 | Link |
| --- | --- |
| ByteByteGo (YouTube — Alex Xu) | https://www.youtube.com/@ByteByteGo |
| System Design Primer | https://github.com/donnemartin/system-design-primer |
| Designing Data-Intensive Applications | https://dataintensive.net/ |
| Microservices.io patterns | https://microservices.io/patterns/index.html |
| High Scalability blog | http://highscalability.com/ |

### 觀測性 / SRE

| 主題 | Link |
| --- | --- |
| Prometheus best practices | https://prometheus.io/docs/practices/ |
| RED / USE method (Brendan Gregg) | https://www.brendangregg.com/usemethod.html |
| Google SRE Book | https://sre.google/sre-book/table-of-contents/ |
| OpenTelemetry Go docs | https://opentelemetry.io/docs/languages/go/ |

### Open-source tooling 你用過的

| 工具 | Link |
| --- | --- |
| testify | https://github.com/stretchr/testify |
| go.uber.org/mock | https://github.com/uber-go/mock |
| testcontainers-go | https://golang.testcontainers.org/ |
| uber/fx | https://uber-go.github.io/fx/ |
| Wire | https://github.com/google/wire |
| GORM | https://gorm.io/docs/ |
| Cobra | https://cobra.dev/ |
| Gin | https://gin-gonic.com/docs/ |

---

## 🎯 通勤閱讀建議路徑

如果只有 15 分鐘:
1. Memory refresh table(數字 + PR 對照)
2. Part 9 Set A 隨機抽 5 題

如果有 30 分鐘:
1. Part 2 + Part 2.5(STAR 故事念一遍)
2. Part 9 全套口述

如果有 1 小時:
1. 挑一個 Part 5 bullet 深讀(Bullet 1 已 fully expanded as model)
2. 點 References 的對應 paper / blog 看 5-10 分鐘
3. Part 10 挑一題 SD 在腦中 walkthrough

如果有 90 分鐘:
- Part 13 失誤恢復腳本全套
- Part 12 反問問題全套
- Part 11 談薪 template 念熟

---

> **這份 doc 用法**:不是「一次讀完」,是「面試前針對性挑章節讀」。
通勤時挑一個 Part 深讀,放在心裡反芻,比一次讀 1700 行更有效。
>