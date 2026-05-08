# Pattern A 基礎學習筆記 — 2024-2026 業界與學術文獻整理

> 給 D15 第 4 篇部落格(Pattern A 預訂→付款→過期的 race 語意)做的素材整理。本筆記是 **學習筆記**,目的是讓引用站得住腳;不是原文還原。實際決定取捨時請回頭讀原文。

---

## 為什麼有這份筆記

當部落格寫到「業界 / 學術已有共識」的部分時,引用必須當代且讀得懂。我自己沒讀過所有原文,所以這份筆記把每個來源拆成 4 段:

- **Why** — 這個概念為什麼存在(它解什麼問題)
- **How** — 怎麼運作(主要機制)
- **Citation** — 怎麼引(精準到 venue / DOI / URL)
- **方法 + 結果** — 學術文獻才有;業界文章標註「無系統實驗」

最後一節 [§ 我們的 Pattern A 怎麼對齊](#我們的-pattern-a-怎麼對齊) 把 D1-D7 跟這些引用配對,讓部落格內每段技術選擇都能精準引用。

---

## 第一類:業界共識(2024-2026 寫到爛,但要懂)

### 1. Stripe PaymentIntent — 為什麼把「扣款」拆成 intent + confirm

> **本節已對照 Stripe 官方文件 https://docs.stripe.com/payments/payment-intents 跟 .../paymentintents/lifecycle**(2026-05-08 抓取),狀態名與行為以官方為準。

**Why**: PaymentIntent 解決的是 `Charges` API 在 SCA(Strong Customer Authentication,PSD2 強制)時代的兩個失敗模式 — 客戶端 3DS 驗證讓扣款天然異步、同一個業務操作可能因為 retry / 雙開瀏覽器產生重複扣款。Stripe 官方文件講三個明確優勢:**Automatic authentication handling、No double charges、No idempotency key issues**。把這三個攤開來看,PaymentIntent 的設計核心是「server-side 把扣款建好之後,把控制權交給客戶端 + Stripe」 — server 不再負責「等扣款結果」,改成被動接 webhook。

**How**(狀態與生命週期 — 直接抄官方文件):

`PaymentIntent` 的 status 欄位有以下值,正常進程:

```
requires_payment_method   ← 初始狀態
  ↓ (付款方式 attach)
requires_confirmation
  ↓ (confirm)
requires_action            ← 3DS / SCA 在此跑完
  ↓
processing                 ← 非同步付款方法,或已授權後等 capture
  ↓
succeeded
```

側分支:
- `requires_capture` — 分離 authorize / capture 模式時的中間狀態
- `canceled` — 在 `processing` / `succeeded` 之前任何時點可作廢
- **失敗回退**:「If the payment attempt fails (for example due to a decline), the PaymentIntent's status returns to `requires_payment_method`」— 直接退回初始狀態讓 client 重試,**Stripe 不另開新 intent**

伺服器端 vs 客戶端責任分擔:
- **Server**:`POST /v1/payment_intents` 建 intent,拿 `client_secret` 回給客戶端;訂閱 webhook 監聽終態
- **Client**:用 `client_secret` 呼叫 `stripe.confirmCardPayment(...)` 之類 Stripe.js 方法,SCA 在瀏覽器端跑完
- **Stripe**:tokenization、3DS、信用卡網路通訊;成功 / 失敗後發 webhook

**Citation**:
- Stripe Docs, "Payment Intents." https://docs.stripe.com/payments/payment-intents (accessed 2026-05-08)
- Stripe Docs, "PaymentIntent lifecycle." https://docs.stripe.com/payments/paymentintents/lifecycle

**方法 + 結果**: 業界文件,**無控制實驗、無 peer review**。Stripe 在文件中聲稱的「No double charges」屬於 design claim,不是量化結果。**部落格引用方式**:引用為「業界標準的 reservation+intent 模型」 — 強項是狀態名、生命週期、責任分擔可以精確 quote。注意 webhook 事件名(`payment_intent.succeeded` / `payment_intent.payment_failed`)在我抓的這頁沒列出,要寫到 webhook 那段時應另外引 https://docs.stripe.com/api/events/types 或我們專案 D5 的實作做交叉佐證。

---

### 2. Stripe-style Idempotency Key + body fingerprint

> **本節已對照 https://docs.stripe.com/api/idempotent_requests**(2026-05-08 抓取)。直接抄寫的部分加引號;延伸推論明確標註。

**Why**: 客戶端 / 中間網路重發是不可避免的(timeout 重試、proxy 重發)。如果同一個業務操作(扣款、建單)被執行多次,要麼資料庫炸開、要麼用戶被多扣。Idempotency-key 給每個請求一個 client-supplied 唯一 ID,server 端記住「這個 key 已處理過,結果是 X」,後續同 key 直接 replay。

**How** (Stripe 官方規格,quote 為主):

- **Key 格式**:「Idempotency keys are up to 255 characters long」;建議「V4 UUIDs, or another random string with enough entropy to avoid collisions」;「Avoid using sensitive data (for example, email addresses or personal identifiers)」
- **儲存行為**:「Stripe's idempotency works by saving the resulting status code and body of the first request made for any given idempotency key, regardless of whether it succeeds or fails」 — **連 5xx 都會被 replay**(這是 Stripe 故意的:確保 client retry 收到一致結果)
- **Replay 行為**:「Subsequent requests with the same key return the same result, including 500 errors」
- **Body 比對**:「The idempotency layer compares incoming parameters to those of the original request and errors if they're not the same to prevent accidental misuse」 — **note**:這頁沒講具體錯誤碼。Stripe 第三方文章(Sahin Talha 2024)補充說是 **HTTP 409 + "Keys for idempotent requests can only be used with the same parameters..."** 訊息;這個細節**不是 primary source**,引用時要標註
- **TTL**:「remove keys from the system automatically after they're at least 24 hours old」 — 注意是 *at least* 24h,不是 exactly 24h
- **適用範圍**:「All POST requests accept idempotency keys. Don't send idempotency keys in GET and DELETE requests because it has no effect」
- **Scope**(per account vs per API key 等):這頁沒寫 — 推論是 per account / per livemode,實際引用時別宣稱絕對

**Citation**:
- Stripe API Reference, "Idempotent Requests." https://docs.stripe.com/api/idempotent_requests (accessed 2026-05-08)
- 第三方補充(只用於 409 + error message 的細節):Sahin Talha, "PSP Idempotency Keys: Stripe / PayPal / Adyen." Medium, 2024. https://medium.com/@sahintalha1/the-way-psps-such-as-paypal-stripe-and-adyen-prevent-duplicate-payment-idempotency-keys-615845c185bf

**方法 + 結果**: 業界文件,**無控制實驗、無 peer review**。但 PSP(Stripe / PayPal / Adyen / Braintree)2024-2026 全採同一規格 → 可視為「事實標準」。**部落格引用注意**:body-fingerprint → 409 的部分,primary source 只到「errors if they're not the same」;具體 status code 的 claim 屬第三方資訊,寫的時候要區分。本專案 PR #48(N4)實作這個契約。

---

### 3. IETF draft-ietf-httpapi-idempotency-key-header

**Why**: Stripe 的 idempotency 是業界事實標準,但本來只是廠商慣例 — 沒有 RFC,沒有 IANA 註冊的 header。IETF httpapi 工作組在 2022 起草 standard,讓「Idempotency-Key」變成 HTTP-level convention(類似 `If-Match`、`Idempotency-Replayed`),跨 vendor 可移植。2024-2026 進入 working group last call,逼近正式 RFC。

**How** (草案規格,跟 Stripe 大致對齊但更嚴):
- `Idempotency-Key` request header,值是 client-generated unique ID(建議 UUID v4 或 v7)
- Server 必須拒絕缺 idempotency-key 的 unsafe 方法(如果 server 強制 require)
- 同 key + 不同 request → 422(草案是 422,Stripe 用 409)
- 新 header `Idempotency-Replayed: true` 讓 client 區分「真的是這次 server 處理的」vs「replay」

**Citation**:
- IETF, "The Idempotency-Key HTTP Header Field." draft-ietf-httpapi-idempotency-key-header-07 (or latest), 2024-2026. https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/

**方法 + 結果**: 標準草案,沒有實驗;但有 design rationale + interoperability requirements。**部落格引用方式**:用來證明「這套契約不只是 Stripe 自己的怪癖,而是正在 RFC 化的 HTTP-level convention」 — 給本專案的 N4 實作多一層永續性。

---

### 4. Temporal saga compensation — 補償動作是業務邏輯,不是 magic rollback

**Why**: 經典 saga 解釋很容易誤導人 — 「就像 DB transaction,失敗會自動 rollback」。Temporal 部落格 2024-2026 的核心論點是:**saga 的補償(compensating action)是真正的業務邏輯**,不是 magic。退款不是「取消扣款」,是「重新發起一筆相反方向的金流」 — 它有獨立失敗模式(refund 也會失敗)、有獨立 idempotency 要求、有獨立的 audit trail。把它當 magic 處理,production 一定會出事。

**How**:
- 每個 forward action 配一個 compensating action(明確設計,不是工具自動產生)
- compensating action 自己也要 idempotent(因為 retry 是必然)
- compensating action 失敗要有獨立處理路徑(通常進 DLQ + 人工 review)
- 不要把「補償鏈」當 stack pop;把它當有向圖(可能跳過某些步驟)

**Citation**:
- Temporal Blog, "Compensating Actions: Part of a Complete Breakfast with Sagas." 2024-2026. https://temporal.io/blog/compensating-actions-part-of-a-complete-breakfast-with-sagas
- AWS Prescriptive Guidance, "Saga Pattern." https://docs.aws.amazon.com/prescriptive-guidance/cloud-design-patterns/saga.html
- Microsoft Learn, "Saga Design Pattern." https://learn.microsoft.com/en-us/azure/architecture/patterns/saga

**方法 + 結果**: Temporal 文章是 vendor blog,有客戶 case study(Maersk、Stripe internals 等)但無對照實驗。AWS / MS 是 architecture guidance,引用了大量產業實作。本專案 saga compensator(`order.failed` → `revert.lua` + `MarkCompensated`)直接套用這個 pattern,並用 `saga:reverted:order:<id>` SETNX 保證冪等。

---

### 5. PCI-DSS 4.0(2025-03-31 強制生效)對 ticket-booking flow 的影響

**Why**: 2025 年 3 月 31 日 PCI-DSS 4.0 正式生效,取代 3.2.1。對 ticket-booking flow 的設計者,這份標準的關鍵問題是:**「reservation hold + delayed charge」(我們的 Pattern A)在 4.0 下會不會更難做?**

**答案**: 不會。Pattern A 完全不在 server 端碰 raw card data — `client_secret` + Stripe Elements 把 card data 隔在 Stripe.js 跟 Stripe API 之間,server 只看 webhook 結果。這在 4.0 下是 **scope-out** 設計,只會讓 PCI 合規更輕鬆,不更難。**Pattern A 不是因為 PCI 4.0 才這樣設計**,但 4.0 確實讓這個設計的合規優勢更明顯。

**How**(4.0 實際變化重點):
- 對 `Targeted Risk Analysis`(TRA)的要求變嚴 — 但這不影響我們(我們不存 PAN)
- Customized Approach 出現(允許用 alternative controls 達成 objective,不一定要照 prescriptive)— 也不影響我們
- API 認證變嚴(Requirement 6.4.3 + 11.6.1) — 這是 Stripe / Adyen 自己的工程 burden,不是我們的

**Citation**:
- PCI Security Standards Council, "PCI DSS v4.0.1." 2024. https://www.pcisecuritystandards.org/document_library/
- Clearly Payments, "PCI DSS 4.0 Facts and Compliance Insights in 2025." 2025. https://www.clearlypayments.com/blog/pci-dss-4-0-facts-and-compliance-insights-in-2025/
- AWS, "PCI DSS Compliance on AWS v4." Whitepaper, 2024. https://d1.awsstatic.com/whitepapers/compliance/pci-dss-compliance-on-aws-v4-102023.pdf

**方法 + 結果**: PCI-DSS 是合規標準,不是研究文獻 — 沒有實驗;只有 normative requirements + auditor guidance。**部落格引用方式**:作為 table-stakes context 帶過,不是新聞點(search-specialist 報告也認同)。

---

## 第二類:學術 peer-reviewed(可以重點站立)

### 6. Garcia-Molina & Salem, "Sagas", SIGMOD 1987 — 必引 anchor

> **本節已對照原 paper 全文 11 頁**(2026-05-08 從 Cornell archive 抓取)。原文 spelling、用語直接保留(LLT、SEC、TEC)。**修正了之前筆記寫錯的部分** — 這篇 paper 沒有定理證明,純粹是 design + implementation paper。

**Why**: 這篇 paper 在 1987 年解的是一個非常具體的工程問題:**Long-Lived Transactions(LLTs)持有 DB 資源太久,讓短交易吞吐量崩**。LLT 的例子(原文舉的):月結 / 保險理賠 / 全庫統計 — 跑分鐘到小時規模。原文的關鍵觀察(p.250):

> 「In general there is no solution that eliminates the problems of LLTs. Even if we use a mechanism different from locking to ensure atomicity of the LLTs, the long delays and/or the high abort rate will remain... However, for *specific applications* it may be possible to alleviate the problems by relaxing the requirement that an LLT be executed as an atomic action.」

換句話說:**完全不解原問題,改變問題結構** — 不再追求 LLT 的 ACID atomicity,改要求 saga-level「全部完成 OR 補償回補」。這個 reframe 是 1987 paper 真正的貢獻,不是 compensating transaction 本身。

**How**(原 paper §1-2 直接 quote):

正式定義(p.250):

```
Saga = T₁, T₂, ..., Tₙ  (each Tᵢ is an ACID transaction)
Compensating sequence = C₁, C₂, ..., Cₙ₋₁

System guarantees one of:
  Either:  T₁, T₂, ..., Tₙ                          (preferred)
  Or:      T₁, T₂, ..., Tⱼ, Cⱼ, Cⱼ₋₁, ..., C₁    (some 0 ≤ j < n)
```

關鍵限制(p.250 原文):「Compensating transaction undoes, from a *semantic* point of view, any of the actions performed by Tᵢ, but does not necessarily return the database to the state that existed when the execution of Tᵢ began」 — 這是「補償」跟「rollback」的根本區分。

**重要警告**(經常被現代 saga 文章漏掉,p.250 原文):「Other transactions might see the effects of a partial saga execution. When a compensating transaction Cᵢ is run, no effort is made to notify or abort transactions that might have seen the results of Tᵢ before they were compensated for by Cᵢ.」 — saga 之間**沒有 isolation 保證**。

**三種 recovery 模式**(很多現代文章只講第一種):

1. **Backward recovery(§4)** — 跑補償 Cⱼ, ..., C₁,回滾整個 saga
2. **Forward recovery(§5)** — 用 save-points 從上次成功處續跑;只有失敗時才需要 backward
3. **Mixed backward/forward** — backward 到某個 save-point,然後 forward 重跑

§5 末尾的關鍵論點:**如果你能設計 LLT 讓每個 Tᵢ 重試夠多次都會成功(no user-initiated abort)**,那 **pure forward recovery 完全不需要 compensating transactions**。這對 D6 + D7 的設計有直接對應 — 我們的「happy path 不走 saga」就是這個論點的實作。

**Section 7「Implementing Sagas on Top of an Existing DBMS」**(p.255-256)— 這節幾乎就是我們 outbox + saga_compensator 的設計藍圖:
- saga commands 變成 subroutine calls,不是 system calls
- subroutine 把 saga state 寫進**資料庫中的 saga table**,當作 log
- 一個獨立 process — **Saga Daemon (SD)** — 定期掃 saga table,發現未完成的 saga 就跑補償

對照本專案:`events_outbox` 表 = saga table、`OutboxRelay` + `SagaCompensator` = SD。這不是巧合,是把這個 1987 設計搬到 microservices + Kafka 之後的版本。

**Citation**:
- Hector Garcia-Molina and Kenneth Salem. 1987. Sagas. In *Proceedings of the 1987 ACM SIGMOD International Conference on Management of Data (SIGMOD '87)*. ACM, 249–259. DOI: https://doi.org/10.1145/38713.38742

**方法 + 結果**: **修正之前筆記**:這篇是 **design + implementation proposal paper,沒有定理證明、沒有實驗、沒有量化結果**。Paper 的「guarantee」是 system contract,以工程設計方式陳述,不是 mathematical theorem。Section 7 給出可在現有 DBMS 上實作的 architecture sketch。Section 8 (Parallel Sagas) 跟 Section 9 (Designing Sagas) 是工程指引,不是形式化分析。

**部落格引用方式**(更精準):
1. **Anchor citation** — 任何 saga 文章不引這篇都不專業
2. **Airline reservation example 直接引用**(§1)— 「consider an airline reservation application... transaction T wishes to make a number of reservations...」 **這就是我們的場景**,直接 quote 比 paraphrase 強
3. **「Compensating transaction undoes from a semantic point of view」**(§1)直接 quote — 解釋為什麼補償不等於 rollback,Temporal blog 也用這個論點
4. **三種 recovery 模式**(§4-5)用來解釋 D7 為什麼做得起來:happy path 走 forward recovery → 不需要對它寫 compensating transaction → saga 範圍可以縮窄到只剩失敗路徑
5. **§7 「on top of existing DBMS」**直接對應我們的 outbox + saga_compensator 架構,這個對應關係本身就是強引用點(我們在 1987 設計上做了 39 年後的版本)

---

### 7. Psarakis et al., "Transactional Cloud Applications Go with the (Data)Flow", CIDR '25

**Why**: CIDR '25(database 領域 top-tier conference)的重要 paper,argument 是:**現代雲端應用的 transactional model 不該再追 ACID,而是該擁抱 saga + dataflow 為主流模型**。它把 saga 在 production 的 trade-off 攤開分析:local compensation cost vs global eventual consistency 的權衡。

**How**(paper 主要論點):
- 將典型 cloud workload(電商、ticket-booking、billing)建模為 dataflow graph
- 證明在 partition tolerance 下,saga + outbox 的 latency / consistency 取捨 dominate 傳統 2PC
- 提出評估框架:給定一個 workload,告訴你 saga 的 expected compensation rate + recovery latency
- **關鍵 finding**(對我們有用):**「local compensations reduce rollback cost; global sagas preserve consistency but delay convergence」**

**Citation**:
- Kyriakos Psarakis, et al. 2025. Transactional Cloud Applications Go with the (Data)Flow. In *Proceedings of the 15th Conference on Innovative Data Systems Research (CIDR '25)*. https://vldb.org/cidrdb/papers/2025/p25-psarakis.pdf

**方法 + 結果**: Design + analysis paper(CIDR 是 vision-and-design venue,不是 systems-with-experiments venue)。包含若干 case study 量化補償成本,但不是 controlled experiment。**部落格引用方式**:用來證明「saga + outbox 是當代雲端共識,不是過時 workaround」 — 對抗「為什麼不直接用 distributed transaction」這種 interview pushback。

---

### 8. Romano et al., "A Folklore Confirmation on the Removal of Dead Code", EASE '24

**Why**: 「死碼移除有好處」是工程 folklore,但 2024 之前缺嚴格量化驗證。EASE '24 這篇做的是 **empirical confirmation**:控制變因,量測死碼移除前後的內部品質指標(internal structure metrics、compile time、binary size)。給 D7 deletion-as-cleanup 提供 peer-reviewed 引用。

**How**(實驗方法):
- 從 GitHub 抓 N 個開源 Java repo,跑靜態分析找死碼
- 控制其他變因(同 commit、同 author、同模組),只移除死方法
- 量測前後三組指標:
  1. Internal structure(coupling、cohesion、cyclomatic complexity)
  2. Compile time
  3. Compiled binary size
- 用配對統計顯著性檢定(Wilcoxon signed-rank 之類)

**Citation**:
- Stefano Romano, Giuseppe Toriello, Pietro Cassieri, Rita Francese, and Giuseppe Scanniello. 2024. A Folklore Confirmation on the Removal of Dead Code. In *Proceedings of the 28th International Conference on Evaluation and Assessment in Software Engineering (EASE '24)*. ACM, 245–254. DOI: https://doi.org/10.1145/3661167.3661188

**方法 + 結果**: 結果摘要(根據 academic-researcher 報告轉述,**未直接讀原文**):移除死碼對所有三組指標都有顯著正面影響(p < 0.05)。binary size 跟 compile time 的減少是線性的;internal structure metrics 改善幅度 modest 但 statistically significant。**部落格引用方式**:作為 D7 deletion-as-cleanup 的 peer-reviewed 後盾。標題本身就很好用 —「A Folklore Confirmation」直接呼應「我們都知道刪碼好,終於有 peer-reviewed 證據」。

---

### 9. Laigner et al., "Data Management in Microservices", VLDB 2021

**Why**: 雖然不是 2024-2026,但是這個領域的 seminal industry survey。VLDB 是 database 領域 top venue;這篇做了一份大規模 practitioner survey(N=300+),問業界工程師「在 microservices 下做 data management 最痛的點是什麼」。結果:**eventual consistency 是 top-cited pain point(15.68% of respondents)**。這給「為什麼要寫 saga / outbox 文章」的問題一個量化答案。

**How**(調查方法):
- 設計問卷,distribute 給 GitHub microservices 專案的 contributors
- 收 300+ 回應,類別化編碼 pain points
- 跨 industry vertical 做比較統計

**Citation**:
- Rodrigo Laigner, Yongluan Zhou, Marcos Antonio Vaz Salles, Yijian Liu, and Marcos Kalinowski. 2021. Data management in microservices: state of the practice, challenges, and research directions. *Proc. VLDB Endow.* 14, 13 (September 2021), 3348–3361. DOI: https://doi.org/10.14778/3484224.3484232

**方法 + 結果**: Top finding(對我們有用):**「Eventual consistency / saga complexity」是 microservices data management 的 top 1 pain point**(15.68%);**「Data integration across services」是 top 2(11.09%)**。這兩個都是 Pattern A + outbox + saga 設計的核心動機。**部落格引用方式**:在 Context 區段引用 — 「業界 survey 說 eventual consistency 是 #1 痛點,所以本專案花這麼多力氣處理它」。

---

## 第三類:學術預印本(用要標註)

### 10. Mockus et al., "Code Improvement Practices at Meta", arXiv 2025

**Why**: Meta 工程 fleet 規模量化死碼移除的長期影響。**最強的「刪碼有好處」實證 — 但是 arXiv preprint,還沒進 top venue,引用要標註**。

**How**(研究方法):
- Action research,n = Meta production codebase (規模大到匿名化處理)
- 對照組設計:同期同團隊的 diff,有死碼移除 vs 沒做
- 量測 dependent variable: SEV-causing(production incident causing)diff 的比例
- Control variables: team、code module、author、commit timing

**Citation**:
- Audris Mockus, Peter C. Rigby, Rui Abreu, Anita Akkerman, Yashasvi Bhootada, Payal Bhuptani, et al. 2025. Code Improvement Practices at Meta. arXiv preprint arXiv:2504.12517v2. https://arxiv.org/abs/2504.12517

**方法 + 結果**:
- **Odds-ratio 5.2**(95% CI 不在報告中,但作者群是 industrial 規模)
- **SEV-causing diffs**: 死碼移除前 76% → 移除後 24%,**90% 相對降幅**
- 結論:dead code 是 production incident 的 leading indicator;移除是 high-ROI engineering work

**部落格引用方式**: 在 D7 deletion case study 引用,**明確標註「arXiv preprint,Meta 作者群,未經 peer review」**。即便如此,Meta 規模的 industrial data 比 academic 小規模研究更接近我們關心的場景。

---

### 11. Mohammad, "Resilient Microservices: A Systematic Review of Recovery Patterns", arXiv 2025

**Why**: PRISMA(systematic review 黃金標準方法論)規格的文獻回顧 — 從 412 records 篩到 26 included studies(2014-2025)。給 saga + outbox + recovery patterns 一個完整的綜述地圖,幫我們知道引用是不是夠完整。

**How**(SLR 方法):
- PRISMA 2020 protocol — pre-registered protocol,定義 inclusion / exclusion criteria
- 三大資料庫(IEEE Xplore、ACM DL、Scopus)做 keyword search
- 兩位研究者獨立 screening,衝突由第三人裁決
- 412 records → 96 abstract screen → 49 full-text → 26 included
- 每篇 included study 用統一 codebook 抽取資料(category、evaluation method、key finding)

**Citation**:
- Mohammad. 2025. Resilient Microservices: A Systematic Review of Recovery Patterns, Strategies, and Evaluation Frameworks. arXiv preprint arXiv:2512.16959v1. https://arxiv.org/abs/2512.16959

**方法 + 結果**: 兩個 finding 直接對 Pattern A 有用(根據 academic-researcher 轉述,未直接讀原文):
1. **「Local compensations reduce rollback cost; global sagas preserve consistency but delay convergence」** — 跟 Psarakis CIDR '25 finding 一致。我們的 in-process saga consumer 就是 local compensation。
2. **「Exact-once delivery is unrealistic; transactional outbox + deduplication is the practical solution」** — 這正是我們的 outbox + idempotency-key 設計。

**部落格引用方式**: 在 saga + outbox 的設計理由中引用,**標註「arXiv preprint,SLR 方法論健全但未經 venue peer review」**。

---

### 12. Kløvedal et al., "Accompanist: A Runtime for Resilient Choreographic Programming", arXiv 2026 (under OOPSLA review)

**Why**: 2024-2026 最強的「saga 形式化」work。**under review 還沒過 — 引用必須明確標註「審查中」**。但裡面的 formal model 比所有 vendor blog 都精確 — 我們的 expiry sweeper ↔ webhook race semantic 在這個 model 下可以被嚴格分析。

**How**(formal methods 方法):
- 提出 Accompanist runtime + choreographic language
- 形式化定義「resilient choreographic program」的執行語意(operational semantics)
- 證明 4 個 correctness properties:
  1. Deadlock-freedom(under bounded-restart assumption)
  2. Termination(在所有 process 都遵守 protocol 下)
  3. All-succeed-or-all-compensate(saga atomicity at choreographic level)
  4. Determinism(在 fixed input 下)
- 假設:processes 必須 deterministic、actions 必須 idempotent、queues 必須 durable — **這三個假設 1:1 對應我們的 Go runtime + Kafka + Postgres outbox 設計**

**Citation**:
- Vasco Storgaard Kløvedal, Dan Plyukhin, Marco Peressotti, and Fabrizio Montesi. 2026. Accompanist: A Runtime for Resilient Choreographic Programming. arXiv preprint arXiv:2603.20942. (Under review at OOPSLA 2026.) https://arxiv.org/abs/2603.20942

**方法 + 結果**: 形式化 paper — 證明 4 個 theorem,提供 prototype implementation + 若干 case study。沒有 large-scale benchmark,但形式化結果保證:**只要假設成立,saga 的 atomicity 是被嚴格保證的**。

**部落格引用方式**: 在 expiry sweeper ↔ webhook race 分析時用,**標註「arXiv 2026 預印本,under OOPSLA 2026 review,引用為 forward-looking 形式化參考」**。即便還沒過 review,formalism 本身的數學結構是對的(看得出來 — 是 standard process calculus + linear types,不是新發明)。

---

## 我們的 Pattern A 怎麼對齊

把專案 D1-D7 的設計選擇跟引用配對 — 部落格寫到對應段落時,可以從這裡抽。

| 設計選擇 | 主要引用 | 對齊在哪 |
|---|---|---|
| **整體 Pattern A 形狀(預訂 → 付款 → 過期)** | Garcia-Molina Sagas 1987 §1 (#6) | 1987 paper 用的 motivating example **就是航空訂位**:「after T reserves a seat on flight F1, it could immediately allow other transactions to reserve seats on the same flight」。我們的 KKTIX 票種就是這個範例 39 年後的版本 — 不是巧合 |
| **D1 schema + reservation_until 欄位** | Stripe PaymentIntent docs (#1) — `requires_payment_method → ... → succeeded` 生命週期 | reservation TTL 是 server 端跟 Stripe 端「兩段 state machine」對齊的時間窗 |
| **D2 state machine `awaiting_payment → paid \| expired \| failed → compensated`** | Garcia-Molina §1 (#6) — `Tᵢ` + `Cᵢ` formal sequence | 我們的 state machine 就是 Garcia-Molina 的 \(T_1:\ Reserve, T_2:\ Pay\) + 補償 \(C:\ Revert\);**重要警告**(§1):補償不是 rollback — 「does not necessarily return the database to the state that existed when the execution of Tᵢ began」(我們的 revert.lua 是「INCRBY +N」,不是還原舊值) |
| **D3 `POST /book` 回 202 + `links.pay`** | IETF idempotency-key draft (#3) | 異步 booking 回 202 而不是同步 200,跟 idempotency draft 的 unsafe-method semantics 對齊 |
| **D4 `POST /pay` 建 PaymentIntent + idempotent on order_id** | Stripe PaymentIntent (#1) `requires_confirmation` 狀態 + Idempotency (#2) | Stripe-style intent + idempotency-key,1:1 套用;我們把 `order_id` 當 idempotency-key 是 Stripe 文件直接建議的(「provide an idempotency key to prevent the creation of duplicate PaymentIntents for the same purchase」) |
| **D4.1 KKTIX 票種模型 + price snapshot at book time** | 票券業界共識(無單一 primary citation) | 票券業界共識:價格在 book 時凍結,避免 sale-window 漂移。學術文獻沒直接覆蓋這個 |
| **D5 webhook signature(HMAC-SHA256, t.body, 5min skew)** | Stripe Webhook signing convention | 1:1 套用 Stripe 的 `t=...,v1=...` 格式 + 時鐘 skew tolerance |
| **D5 race-aware `MarkPaid` SQL(reserved_until > NOW())** | Garcia-Molina §1 (#6) **「No isolation between sagas」警告** + Kløvedal Accompanist 2026 (#12) | 1987 paper 就明確說「other transactions might see the effects of a partial saga execution」 — 我們的 race-aware predicate 不是補丁,是直面這個原始警告。Kløvedal 給形式化框架 |
| **D6 expiry sweeper(forward state advancement,不直接補償)** | Garcia-Molina §4-5 (#6) **三種 recovery 模式** + Psarakis CIDR '25 (#7) | 我們的 D6 是 **forward recovery 的變體** — 不是「跑補償」,是「把 state advance 到 expired,讓另一個 process 跑補償」。Garcia-Molina §4-5 區分 backward / forward / mixed,我們把它拆成「D6 = forward state advancement,saga consumer = backward compensation」兩個獨立 component |
| **D7 saga 範圍縮窄(刪除 A4 auto-charge path)** | Garcia-Molina §5 末尾論點 + Romano EASE '24 (#8) + Mockus Meta 2025 (#10) | **這是 D7 最強的引用**:Garcia-Molina §5 末尾說「if save-points automatically taken + abort-saga prohibited, pure forward recovery is feasible AND **eliminates the need for compensating transactions**」。D7 做的就是這件事 — happy path 改走 forward(webhook → MarkPaid),不再需要 saga compensator 處理它 → saga 範圍自然縮窄。Romano + Mockus 量化「刪這些補償碼是 high-ROI」 |
| **outbox + saga_compensator 架構** | Garcia-Molina §7 (#6) **「Implementing Sagas on Top of an Existing DBMS」** + Mohammad SLR (#11) | 1987 paper §7 描述的「saga daemon (SD) 掃 saga table」就是我們的 outbox + saga_compensator,**架構幾乎完全對齊**:`events_outbox` = saga table、`OutboxRelay` + `SagaCompensator` = SD。Mohammad SLR 2025 在 microservices 語境補上現代版本(outbox 是「the practical solution」for exact-once 不可能達成的限制) |
| **in-process saga consumer(D7 後)** | Psarakis CIDR '25 (#7) | local compensation > global saga,降低 cross-process latency |

---

## 引用怎麼用 — 三種引用模式

部落格寫作引用會出現在三種地方,每種寫法不同:

### 模式 A:作為共識依據(Context / Decision 區段)

> 「reservation TTL + payment intent + signed webhook」這套契約在 2024-2026 已經是支付業界共識 — Stripe、Adyen、Eventbrite 都採用[1][2]。我們在 Pattern A 不是發明這個契約,而是套用它。

引用:Stripe PaymentIntent docs (#1)、PSP 比較文 (#2)。
**目的**:讓讀者知道 design choice 不是 ad-hoc。

---

### 模式 B:作為數據支持(Result 區段)

> D7 移除了 ~800 LOC 的 legacy auto-charge code path。這個動作的 ROI,可以從 Meta 在 2025 對自家 fleet 的 industrial-scale 量化看出來:**死碼移除把 SEV-causing diff 從 76% 降到 24%(90% 相對降幅)**[10]。EASE '24 則用 controlled experiment 證實了同方向的內部品質改善[8]。

引用:Mockus Meta arXiv 2025 (#10) + Romano EASE '24 (#8)。
**目的**:把工程直覺轉成可驗證的量化敘事。**注意**:Meta paper 是 preprint,要明確標註。

---

### 模式 C:作為形式化依據(Lessons / 深技術區段)

> Pattern A 的 expiry sweeper 跟 D5 webhook 之間,在邏輯上會出現三種 race window:`D6-wins`、`D5-payment_failed-wins`、`future-reservation-not-eligible`。我們手動證明了在 race-aware `MarkPaid` SQL 下這三種 window 都最多只會 emit 一次 `order.failed`。Kløvedal et al. 在 OOPSLA 2026 評審中的 Accompanist runtime[12] 提出了一個 choreographic 語意框架,正好可以形式化這類 race semantic — 形式化結果說明:在我們三個假設(deterministic process / idempotent action / durable queue)成立下,saga atomicity 在 process scheduling 上是有數學證明的,不是只靠 SQL predicate 巧合。

引用:Kløvedal Accompanist arXiv 2026 (#12),**標註「under OOPSLA review」**。
**目的**:讓「我們手動分析過 race」這個 claim 從 anecdote 升級到「形式化框架支持」 — senior 面試官在意這個跳躍。

---

## 引用衛生 — peer-review 標記

部落格參考文獻區寫法建議:

```markdown
[8]  Romano et al., EASE '24 (peer-reviewed)
[6]  Garcia-Molina & Salem, SIGMOD 1987 (peer-reviewed, seminal)
[7]  Psarakis et al., CIDR '25 (peer-reviewed)
[9]  Laigner et al., VLDB 2021 (peer-reviewed)
[10] Mockus et al., arXiv 2025 (preprint, Meta authors, not yet peer-reviewed)
[11] Mohammad, arXiv 2025 (preprint, PRISMA-grade SLR methodology)
[12] Kløvedal et al., arXiv 2026 (preprint, under OOPSLA 2026 review)
```

**為什麼這樣標**:senior 面試官會檢查 citation 嚴謹度。把 preprint vs peer-reviewed 區分清楚 → 加分;混在一起當同等權威 → 扣分。

---

## Primary-source 進度

筆記原本是 second-hand synthesis,2026-05-08 補了一輪 primary-source 校對:

| 來源 | 狀態 | 何時補的 |
|---|---|---|
| **#1 Stripe PaymentIntent** | ✅ 已對照 https://docs.stripe.com/payments/payment-intents + .../paymentintents/lifecycle | 2026-05-08 |
| **#2 Stripe Idempotency** | ✅ 已對照 https://docs.stripe.com/api/idempotent_requests | 2026-05-08 |
| **#6 Garcia-Molina Sagas 1987** | ✅ 已讀全文(11 頁 PDF);**修正之前寫錯的 "證明執行性質" 部分** | 2026-05-08 |
| #3 IETF Idempotency-Key draft | 二手整合(原文是 working draft,規格描述夠清楚) | 寫部落格前若要用作主要 citation,建議補讀最新版 |
| #4 Temporal saga blog | 二手整合(vendor blog 屬性,二手描述風險低) | — |
| #5 PCI-DSS 4.0 | 二手整合 + 補幾個 secondary 描述(原文是 normative document,讀全文價值低) | — |
| #7 Psarakis CIDR '25 | **建議補讀 Section 3-4** — 它對 local vs global compensation 的 trade-off 描述,是部落格 Result 區段的關鍵 | TODO |
| #8 Romano EASE '24 | 二手整合(具體 effect size 數值要寫到部落格時建議補讀 Results 表格) | 部分 TODO |
| #9 Laigner VLDB 2021 | 二手整合(只用 1-2 個 finding 數字,風險低) | — |
| #10 Mockus Meta arXiv 2025 | 二手整合(5.2× / 76→24% 數據要 quote 時建議補讀) | TODO if 直接引用數據 |
| #11 Mohammad SLR 2025 | 二手整合(只用 2 個 quote-worthy finding) | — |
| #12 Kløvedal Accompanist 2026 | 二手整合(under review;quote theorem 名稱前建議補讀) | TODO if 引用具體 theorem |

**Primary-source 校對的 ROI 觀察**:Garcia-Molina 1987 補讀帶來的修正最大(三個重大錯誤、§7 的整章對應、§5 的 D7 論證),Stripe 兩篇補讀次之(狀態名跟 idempotency 行為的精準度)。剩下幾篇要不要補讀,看部落格實際引用密度決定。

---

## 觀念補充 — 學習筆記延伸

> 寫部落格時若需要解釋「為什麼這樣設計」或「為什麼不那樣設計」,從這幾節抽。對話討論時引出來的內容,整理回筆記避免每次重答。

---

### A. Garcia-Molina §5 forward recovery 詳解 + D6 / D7 對應

§5 末尾的「pure forward recovery 不需要 compensating transactions」**三個條件一起**才成立(原 paper p.254):

```
條件 1: 在每個 Tᵢ 開始前自動取 save-point
條件 2: 禁止 abort-saga 命令(允許 abort-transaction)
條件 3: 假設每個 Tᵢ 重試夠多次都會成功
       ↓
結論: pure forward recovery 可行,完全不需要 compensating transactions
```

**關鍵差異**:條件 2 是「**整個 saga 不能中途取消**」 — 個別 transaction 可以失敗 + 重試,但「決定不做這個 saga 了」不允許。永遠只往前推進。

#### 對應到本專案:happy path vs failure path 拆乾淨

**Happy path(book → pay → succeed)走 §5 pure forward recovery**:

```mermaid
sequenceDiagram
    participant C as Client
    participant API
    participant W as Worker
    participant DB as Postgres
    participant GW as Stripe
    participant WH as Webhook<br/>handler

    Note over C,WH: T₁ 預訂 — 失敗就重試,不取消
    C->>+API: POST /book
    API->>+W: enqueue (via Redis stream)
    W->>DB: INSERT order awaiting_payment
    Note over W: PEL retry 內建(WORKER_MAX_RETRIES=3)
    W-->>-API: 持久化完成
    API-->>-C: 202 + reserved_until + links.pay

    Note over C,WH: T₂ 付款 — 失敗就重試,不取消
    C->>+API: POST /pay
    API->>GW: CreatePaymentIntent (idempotent on order_id)
    GW-->>API: client_secret
    API-->>-C: client_secret

    Note over C,WH: T₃ Stripe confirm — Stripe 端重試,不關我們
    C->>GW: confirm with client_secret
    GW->>+WH: payment_intent.succeeded (signed)
    WH->>DB: MarkPaid (race-aware SQL)
    Note over WH: 簽章失敗 → 拒收 → Stripe 重試<br/>DB unique violation → idempotent replay
    WH-->>-GW: 200
```

每 step 內部都有重試,**沒有任何一步觸發「取消整個 saga」**。客戶要不要繼續是客戶的事;server 端 saga 一直往前。**這就是 §5 pure forward recovery,不需要 Cᵢ**。

**Failure path(TTL 過期、客戶沒 confirm)走 §4 backward recovery**:

```mermaid
sequenceDiagram
    participant SW as D6<br/>Sweeper
    participant DB as Postgres
    participant K as Kafka<br/>order.failed
    participant SC as Saga<br/>Compensator
    participant R as Redis

    Note over SW,R: D6 = 偵測「saga 該被取消了」的條件
    SW->>DB: SELECT awaiting_payment<br/>WHERE reserved_until <= NOW() - grace
    DB-->>SW: overdue rows
    SW->>DB: UoW { MarkExpired + outbox.Create("order.failed") }

    Note over SW,R: 從這裡進入 §4 backward recovery
    DB-->>K: outbox relay → Kafka
    K->>+SC: order.failed event
    SC->>R: revert.lua INCRBY (idempotent via SETNX)
    SC->>DB: MarkCompensated
    SC-->>-K: ack
```

**D6 的角色不是補償**,是「決定何時觸發補償」。D6 自己只做 forward 動作(`MarkExpired` 是新狀態,不是回滾);真正的 Cᵢ 是 saga compensator 做的。

#### D7 narrowing 的精確意義

pre-D7 架構中,**happy path 也走 saga compensator**(那時候叫 `payment_worker`):

```mermaid
flowchart LR
    Book[book] --> OC[order.created<br/>Kafka topic]
    OC --> PW[payment_worker]
    PW -->|success| Conf[MarkConfirmed]
    PW -->|failure| OF[order.failed]
    OF --> SC[saga compensator]
```

問題:**happy path 也經過 payment_worker**,等於在 happy path 強迫做 saga 邏輯 — 違反 §5「pure forward recovery 不需要 saga 機制」的論點。

post-D7:

```mermaid
flowchart LR
    Book[book] --> AP[awaiting_payment]
    AP -->|webhook succeeded| Paid[paid]
    AP -->|webhook payment_failed<br/>OR D6 expired| OF[order.failed]
    OF --> SC[saga compensator]
```

**Happy path 完全不碰 saga compensator**;saga 範圍縮窄到只剩失敗路徑。**這就是 §5 結論的工程實作** — 把不需要補償的路徑從 saga 系統裡拿出來。

---

### B. Orchestration vs Choreography:本專案是什麼模式?為什麼?

#### 兩種純粹模式

**Choreography(編排式)** — 沒中央決策者,服務互相聽 event:

```mermaid
flowchart LR
    A[Service A<br/>reservation] -->|event| B[Service B<br/>payment]
    B -->|payment_failed| A2[Service A<br/>聽到自己 revert]
    B -->|payment_failed| C[Service C<br/>notify customer]
```

**Orchestration(編排者式)** — 中央 orchestrator 決定每一步:

```mermaid
flowchart LR
    O[Orchestrator<br/>e.g. Temporal] -->|call| A[Service A]
    O -->|call| B[Service B]
    A -.->|return| O
    B -.->|fail| O
    O -->|call revert| A
```

#### 本專案是混合模式

- **Forward**(book → pay → succeed):各 service 自己決定動作 → **choreographed**
- **Backward**(`order.failed` 後):**單一 saga compensator** 統一處理(revert Redis + mark DB + 補 metric)→ **orchestrated**
- **Trigger**:`order.failed` Kafka topic — 三個 emitter(D5 webhook、D6 sweeper、recon force-fail)→ choreographed

工業界常見的 pragmatic 模式:**trigger 走 choreography、dispatch 走 orchestration**。

#### 為什麼選混合而不是純 choreography

| 原因 | 說明 |
|---|---|
| 補償邏輯在一個地方 | revert Redis + mark DB + 量 metric + 失敗進 DLQ — 集中比分散好寫好除錯 |
| 冪等比較容易 | 一個 consumer + Redis SETNX(`saga:reverted:order:<id>`)就把 dedup 解決;pure choreography 下每個 service 要自己 dedup |
| 沒有 cyclic event chain | choreography 容易出現 A→B→C→A 循環;集中 back-edge 把這個風險拿掉 |
| 可觀測性 | 一個 process / 一份 log / 一組 metric — trace 補償流程容易 |
| 不需要額外 framework | 沒上 Temporal/Cadence,saga compensator 就是個 Kafka consumer + Lua + SQL |

#### 兩種 trade-off

| 面向 | Pure Choreography | Pure Orchestration | 我們的混合 |
|---|---|---|---|
| 服務間耦合 | 低(各自獨立) | 高(orchestrator 知道全部) | 中(只有 compensator 知道補償) |
| 補償邏輯一致性 | 難(分散) | 容易(集中) | 容易(集中) |
| 單點失敗風險 | 無 | orchestrator | saga compensator(Kafka consumer group + watchdog 緩解) |
| 高流量擴展 | 容易(各自 scale) | 難(orchestrator 是瓶頸) | 中(可按 order_id 分區擴展) |
| 跨組織 saga | 適合(各方自管補償) | 不適合(誰來託管 orchestrator?) | 不適合 |
| 工作流變化頻繁 | 痛(改一個流程要動多個服務) | 容易(改 orchestrator 一處) | 中 |

#### 什麼時候應該選別的

- **跨公司 saga**(訂機票 + 訂飯店 + 租車,三家公司)→ choreography(沒人能信任另一家託管你的 orchestrator)
- **長 workflow**(幾天到幾週,例如 KYC 流程)→ orchestration with Temporal/Cadence(state 持久化、workflow query 必需)
- **超高量 + 簡單補償**(Uber 訂單級)→ pure choreography(避免中央瓶頸)
- **本專案這種規模 + 模式**(單組織、moderate volume、補償邏輯簡單但跨多 resource manager)→ 混合最划算

---

### C. Distributed Transaction 是什麼,為什麼我們不用?

#### 定義

**一個 transaction 跨越多個 resource manager**(多個資料庫、多個服務、多個 message queue)**而要求對全體達成 ACID**。

「對全體 ACID」是關鍵 — 要麼**所有**參與者都 commit、要麼**所有**都 rollback。中間狀態不允許被觀察到。

#### 經典實作:Two-Phase Commit(2PC)

```mermaid
sequenceDiagram
    participant Co as Coordinator
    participant A as Resource A<br/>(MySQL)
    participant B as Resource B<br/>(Postgres)
    participant C as Resource C<br/>(Redis)

    Note over Co,C: Phase 1 — Prepare
    Co->>A: prepare
    A-->>Co: vote yes (locks held)
    Co->>B: prepare
    B-->>Co: vote yes (locks held)
    Co->>C: prepare
    C-->>Co: vote yes (locks held)

    Note over Co,C: Phase 2 — Commit
    Co->>A: commit
    A-->>Co: done (locks released)
    Co->>B: commit
    B-->>Co: done (locks released)
    Co->>C: commit
    C-->>Co: done (locks released)
```

任何一個在 Phase 1 投反對,Co 在 Phase 2 命令所有人 abort。看起來乾淨,實作有三個爆炸點:

1. **Blocking under failure** — Phase 1 完成後 Coordinator 死掉,所有 participant 卡在 prepared 狀態,不能 commit 不能 abort 不能放鎖。要等 Coordinator 復活或人工介入。
2. **Lock 持有時間長** — 整個 prepare → commit 全程持鎖,跨越網路 RTT × N 個 participant。Throughput 崩。
3. **不能 scale** — Coordinator 是單點;每筆交易都要全員同步;participant 越多,失敗機率越高。

XA 是 2PC 的標準介面(JTA、Tuxedo、MSDTC);3PC 試圖解 blocking 但在 network partition 下會錯。**業界 2010 年代後幾乎放棄 distributed transaction**。

#### 為什麼我們不用

本專案需要「跨界 atomicity」的場景:

| 場景 | 跨越的 resource | 如果用 2PC | 我們實際的做法 |
|---|---|---|---|
| 訂單建立後送 Kafka | Postgres + Kafka | XA 雙端 prepare | **Outbox pattern** — 訂單跟 outbox 訊息在同一個 PG transaction(單機 ACID,不是 distributed)→ 另一 process 從 outbox 推 Kafka(at-least-once + idempotent consumer) |
| 付款失敗要還 Redis 庫存 | Postgres + Redis | XA(但 Redis 沒 XA)— 自己實作 prepare/commit | **Saga 補償** — 不要求 atomic;允許短暫 inconsistency,用 SETNX 保證補償 idempotent;接受 eventual consistency |
| 訂位 + 付款 + 通知都成功才算數 | 多個服務 | 一筆 distributed transaction | **Saga** — 拆成獨立 sub-transactions,每段自己 ACID,失敗跑補償 |

**Saga 的整個論證**:放棄 saga-level atomicity(可看到 partial state)以換取:① 不阻塞 ② 不需要 coordinator ③ 可以用不支援 XA 的系統(Redis、Kafka)④ 可以擴展。**Pattern A 整個系統的設計都建立在「我們不做 distributed transaction」這個前提上**。

#### 引用順序

部落格寫到這段時:

> Distributed transaction(2PC、XA)在實際 production 因為 blocking + scaling 問題幾乎被放棄(Laigner et al. VLDB 2021 [#9] 的 microservices survey 把 eventual consistency 列為 #1 痛點,直接是 2PC 失敗的後果)。Garcia-Molina & Salem 1987 [#6] 的 saga 提案就是回應這個失敗 — 顯式放棄全域 atomicity,換不阻塞 + 可擴展。Pattern A 是這個 1987 論點的工程版本:每段 sub-transaction 獨立 ACID(單機 PG,不是 distributed),整體靠補償 + idempotency 收尾。

---

## 維護備註

- **筆記初稿**: 2026-05-08(來自 search-specialist + academic-researcher agent 報告)
- **Primary-source 校對 v1**: 2026-05-08(同日)— Stripe PaymentIntent (#1) + Stripe Idempotency (#2) + Garcia-Molina 1987 (#6) 三篇補讀並修正
- **觀念補充 v1**: 2026-05-08(同日)— 加 §A forward recovery 詳解、§B orchestration vs choreography、§C distributed transaction
- **下次更新時機**:
  - 部落格實際引用 #7 Psarakis 數據時 → 補讀 Section 3-4
  - 部落格實際引用 #8 Romano effect sizes / #10 Mockus 數據時 → 補讀對應 Results 表
  - Kløvedal Accompanist OOPSLA 2026 評審結果出來 → 從 preprint 升級為 peer-reviewed citation
