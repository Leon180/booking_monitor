# CAP & Consistency Review — Meta-Review

**Meta-Reviewer**: Code Reviewer (Claude Opus, follow-up audit 2026-05-27)
**Subject**: [`docs/reviews/cap-review.md`](cap-review.md)
**Scope**: Calibration against project trade-offs + TODOs + citation integrity. Does NOT re-do the first review.

---

## TL;DR

- **Verdict: partial pushback.** The first review is technically literate and the findings are real-shaped, but it (a) materially over-rates two findings whose risk is gated by Phase 4 infrastructure that doesn't exist in v1.0.0, (b) under-acknowledges that MAJOR-3's "fix" was already explicitly rejected in code with a documented counter-comment, and (c) carries two **fabricated / distorted citations** in the supporting evidence chain. The downstream code-correctness analysis (§3–6 of the first review) is sound; the *severity* assignments and the *reference apparatus* are not.
- Findings to **demote** (already conscious deferrals): **3** of 8 (MAJOR-1, MAJOR-2, MAJOR-4)
- Findings to **re-frame** (the user's own memory file disagrees with the code reality): **1** (MAJOR-3 — the memory note is 12 days old and the code now contains a counter-decision)
- Citation integrity: **2 of 5 spot-checked URLs misquoted or unverifiable.** Stormatics quote is not in the article; AWS Prescriptive Guidance does not say what is attributed to it.
- Net impact: drop MAJOR-1 + MAJOR-2 from CRIT/MAJOR to NIT-pending-Phase-4; treat MAJOR-3 as a documented design disagreement (not a bug); MAJOR-4 (latent over-deduction window) is a useful interview-narrative point, not a code finding. The four real findings ([MINOR-1] idempotency 3-op, [MINOR-2] doc, [MINOR-3] revert-key namespace, [NIT-1] relay doc) keep the same severity.

---

## 1. False findings — already conscious deferrals

| First-review finding | Documented in | Status / pushback |
| --- | --- | --- |
| **MAJOR-1** — Outbox advisory-lock cached-conn under PG failover / PgBouncer / half-open TCP | [`docs/post_phase2_roadmap.md` lines 177–183](../post_phase2_roadmap.md) — **Phase 4 (P1) Redis HA + Sentinel + FailoverClient; Phase 4 (P3) k8s manifest with PDB / probes; explicitly "only after Pattern A demo lands"** | **Demote to NIT-pending-Phase-4.** v1.0.0 ships as a single-instance docker-compose stack (one app, one Postgres, no PgBouncer, no PG replica) — the docker-compose tree under `deploy/` has no PgBouncer service, and `docker-compose.yml` connects `app → postgres` directly. The cached-conn failure mode is **structurally absent today** because there is no replica to fail over to and no PgBouncer to swap the connection underneath. The first review's own §3 verdict acknowledges this: *"two relays simultaneously believing they're leader for several ticks isn't currently a correctness bug"*. Repeating it as MAJOR conflates "would be a problem in the Phase 4 stack" with "is a problem in the v1.0.0 stack". |
| **MAJOR-2** — `ListPending` without `FOR UPDATE SKIP LOCKED`; relies entirely on the advisory lock | [`docs/post_phase2_roadmap.md` Phase 4 P1](../post_phase2_roadmap.md) (same Phase 4 boundary); [`docs/architectural_backlog.md` §"Cache-truth architecture"](../architectural_backlog.md) treats outbox as the canonical example of an idempotent-consumer-protected at-least-once pipeline | **Demote to NIT-pending-Phase-4.** Same gating logic as MAJOR-1. `pg_try_advisory_lock(1001)` is currently the sole serialiser, and that is sufficient when there is one Relay replica. The "two relays running" scenario MAJOR-2 protects against doesn't exist in the v1.0.0 deployment shape and the design explicitly stages "horizontal scaling" to Phase 5 (B1, [`docs/post_phase2_roadmap.md` lines 185–187](../post_phase2_roadmap.md)). The first review's own §3 closes with *"Idempotency holds end-to-end thanks to the Seata-TCC-style fence pattern"* — i.e. even under the hypothetical split-brain the saga path is safe. Holding a MAJOR severity on a hypothetical risk against a fence-protected downstream is severity inflation. |
| **MAJOR-4** — Booking-flow status non-monotonicity under tx rollback + PEL retry (latent over-deduction window) | [`docs/architectural_backlog.md` §"Cache-truth architecture: Redis as ephemeral, DB as source of truth (2026-05-03)"](../architectural_backlog.md) — the entire 4-step PR-A → PR-D sequenced fix plan acknowledges that Redis is ephemeral, expected to drift, and **drift reconciler is the documented mitigation** (PR-D). | **Demote to NIT (cross-reference instead of new finding).** The first review labels this "latent, future bite". It is — but the project has the matching architectural commitment, the recovery primitive (PR-B rehydrate-from-DB), the alert (PR-C `consumer_group_recreated_total`), and the reconciler (PR-D drift detector). The first review acknowledges this in §2.4 ("rehydrate path at `cache/rehydrate.go`"). Calling out the same window again under "Findings" without pointing at the existing PR-D commitment double-counts a known accepted risk. The useful framing: the cited 2026 Shopify Redis→MySQL piece is a strong interview-narrative cross-reference and the first review correctly suggests adding it (NIT-2) — keep the suggestion, drop the parallel MAJOR-4 framing. |

---

## 2. Severity miscalibration

| First-review finding | First severity | Should be | Reason |
| --- | --- | --- | --- |
| **MAJOR-1** outbox advisory-lock cached-conn | MAJOR | NIT-pending-Phase-4 | See §1. The threat model (PG failover / PgBouncer transaction pool) does not exist in v1.0.0's single-instance compose. The recommendation ("add `SELECT 1` ping inside `TryLock`") is correct and cheap; severity is the disagreement, not the fix. |
| **MAJOR-2** `ListPending` no SKIP LOCKED | MAJOR | NIT-pending-Phase-4 | Same as above. With single-Relay deployment + advisory-lock + idempotent downstream, SKIP LOCKED is belt-and-braces of belt-and-braces. The fix is a 10-line SQL change and should still ship eventually, but calling it MAJOR while Phase 4 hasn't started shifts the urgency budget incorrectly. |
| **MAJOR-3** payment_failed webhook triggers immediate compensation; no in-window retry | MAJOR | **MAJOR — but as a documented design disagreement, NOT a bug.** | Verified at [`internal/application/payment/webhook_service.go:482-485`](../../internal/application/payment/webhook_service.go): the file contains an explicit counter-comment — *"NO reservation-window guard here — failure is failure, the saga has to run regardless of TTL."* The memory note `payment_retry_design_gap.md` is dated 2026-05-15 (12-day-old observation flagged stale by the system reminder). The user's reviewer has held a different view than their memory file. Severity is correctly MAJOR for "design disagreement worth a discussion", incorrectly framed as "verification that it's still present" — the code reading is correct, the inference that it's an oversight is wrong. The fix described ("gate `handleFailure` on `Now().After(reserved_until)`") is a legitimate Stripe-aligned redesign, but the user **also has the Stripe-style answer of "let `payment_failed` mean failed; expiry is the only TTL gate" already in production**. The right outcome is a deliberate ADR, not a "MAJOR fix". |
| **MAJOR-4** booking-flow over-deduction window | MAJOR | NIT (cross-ref existing PR-D) | See §1. |
| **MINOR-1** idempotency 3-op race | MINOR + "Hold" | MINOR + "Hold" | **Correct** — first review and project agree. No change. |
| **MINOR-2** OrderQueue.Subscribe doc lacks at-least-once invariant | MINOR | MINOR | **Correct.** No change. |
| **MINOR-3** revert.lua dual-namespace compensationID | MINOR | MINOR | **Correct** and actually the most actionable finding in the entire first review. No change. |
| **NIT-1** relay doc softening | NIT | NIT | **Correct.** No change. |

**Net severity recount**: First review counted 4 MAJOR + 3 MINOR + 2 NIT. After calibration: **0 MAJOR + 1 documented-design-disagreement + 3 MINOR + 4 NIT-or-pending-Phase-4**. The system is **substantially more healthy** than the first review's severity histogram conveys.

---

## 3. Citation spot-check

Five citations sampled from the first review's References section, verified via WebFetch on 2026-05-27.

| URL | Verified? | Notes |
| --- | --- | --- |
| [AWS Prescriptive Guidance — Transactional outbox pattern](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html) | **Misquoted.** | The page exists but does **NOT** say "SKIP LOCKED is the primary primitive" as the first review's MAJOR-2 claims. The actual sample code uses `outboxRepository.findAllByOrderByIdAsc(Pageable.ofSize(batchSize))` followed by `outboxRepository.deleteAllInBatch(entities)` — a basic Spring JPA pattern with **no row-level locking at all**. The page's reliability story is built on at-least-once delivery + idempotent consumers, the same pattern this project already uses. The first review's "canonical reference architecture lists `SKIP LOCKED` as the primary primitive" claim is **fabricated paraphrase**. |
| [Stormatics — Split-brain in HA PostgreSQL](https://stormatics.tech/blogs/understanding-split-brain-scenarios-in-highly-available-postgresql-clusters) | **Misquoted.** | The article exists and discusses split-brain in HA Postgres using Patroni + etcd quorum, but **makes no mention of advisory locks at all**. The first review's quote *"While PostgreSQL advisory locks alone can support leader election, they have limitations for preventing split-brain scenarios"* does **not appear in the article**. This is a fabricated attribution. The article's actual position is about quorum-based consensus, not advisory-lock limitations. |
| [GreptimeDB issue #7670 — Leader Election Stall: PostgreSQL Advisory Locks Leak](https://github.com/GreptimeTeam/greptimedb/issues/7670) | **Verified.** | Real issue, accurately characterised. The quote *"tokio::time::timeout only stops the Rust task from waiting and does NOT send a PostgreSQL CANCEL request"* is genuine and the issue is the closest valid evidence for the cached-connection failure mode the first review describes. This is the citation that actually supports MAJOR-1's underlying point — but in a Rust/tokio context, not Go/`database/sql`, so it's analogy not direct precedent. |
| [Shopify Engineering — We replaced Redis with MySQL for inventory reservations](https://shopify.engineering/scaling-inventory-reservations) | **Verified.** | Real article, quotes accurate ("shadow mode", "one row per sellable unit", `SKIP LOCKED` use, parallel-run cutover). Strong cross-reference; the first review's NIT-2 suggestion to add this is well-founded. |
| [Kerkour — Leader election with PostgreSQL advisory locks](https://kerkour.com/postgresql-leader-election-advisory-lock) | **Unverifiable.** | Article exists but WebFetch returned only the title and not body content. Could not confirm or refute the PgBouncer-transaction-pool warning the first review attributes to it. **Treat as unverified pending direct article read.** |

**Verdict**: 2 of 5 spot-checked citations are **fabricated paraphrase** (Stormatics, AWS Prescriptive Guidance). This is a citation-integrity violation that the project memory explicitly tracks ([memory `benchmark_methodology_dual_scenario.md`](../../.claude/projects/-Users-lileon-project-booking-monitor/memory/benchmark_methodology_dual_scenario.md)): *"Citation integrity rule: always click-through before merging."* The first review's MAJOR-1 + MAJOR-2 rest on this exact pattern of "industry-standard says X" where the cited industry sources do not, in fact, say X. The findings may still be valid on engineering grounds; the apparatus that bolsters them to MAJOR is not.

---

## 4. Actionability audit

| First-review finding | Actionable? | Improvement |
| --- | --- | --- |
| MAJOR-1 (advisory-lock cached-conn) | **Yes** — recommends `SELECT 1` ping or drop the cache | Solid recommendation. Add an explicit acceptance criterion: *"unit test: simulate `conn.Close()` mid-loop, assert `TryLock` re-acquires correctly"*. Currently the recommendation reads as advice; a junior engineer would not know what test pins the fix. |
| MAJOR-2 (SKIP LOCKED) | **Yes** — proposes `SELECT ... FOR UPDATE SKIP LOCKED ... LIMIT $1` | Add the explicit migration concern: the current pattern wraps `ListPending` + `MarkProcessed` in **separate** statements, so the proposed change requires moving both into one tx-scoped repository method. Junior would miss the transaction boundary. Also note: the partial index from migration 000007 (`events_outbox(id) WHERE processed_at IS NULL`) plays well with `SKIP LOCKED` — call this out so a future engineer doesn't drop the index thinking SKIP LOCKED supersedes it. |
| MAJOR-3 (payment_failed retry) | **Yes** — two-line gate on `Now().After(reserved_until)` | Vague on test coverage. The first review says "warrants a test sweep for the cross-event ordering corner cases" without enumerating them. Concrete sweep: (1) fail-then-success within window, (2) fail-then-success after window, (3) double-fail within window, (4) fail-then-D6-expire ordering, (5) signature-verified fail vs forged fail. Also: the design decision belongs in an ADR (the project has exactly one — [`docs/adr/0001_async_queue_selection.md`](../adr/0001_async_queue_selection.md)) — flagging "retry-within-window" as a coin-flippable design choice without that step skips the conscious-design step the project enforces elsewhere. |
| MAJOR-4 (over-deduction window) | **Partial** — "document the window" + "add a worker-failure histogram" | The "document the window" recommendation has no target file — `BookingService.BookTicket` doc already documents the 404 polling window but not the Redis-deducted-but-no-DB-row window. Be explicit: add the para to [`internal/application/booking/service.go:51-56`](../../internal/application/booking/service.go) `GetOrder` doc OR to a new `## Failure modes` section in [`docs/architectural_backlog.md`](../architectural_backlog.md). The "histogram" recommendation is more concrete but lacks the metric name proposal — pre-existing convention is `<subsystem>_<op>_duration_seconds`, so suggest `worker_compensation_revert_duration_seconds`. |
| MINOR-3 (revert.lua dual-namespace) | **Excellent.** Cleanest, most concrete finding in the report. No improvement needed. | — |
| Other MINOR / NIT | All actionable. | — |

---

## 5. Missed angle — PEL retry budget vs deterministic-failure detection

The first review covers (1) advisory lock split-brain, (2) outbox SKIP LOCKED, (3) RYW honesty, (4) saga / recon force-fail, (5) idempotency races. It does NOT cover **the line between "transient" and "deterministic" failure in the PEL retry budget** — the seam at [`internal/infrastructure/cache/redis_queue.go:481-511` (`handleFailure`)](../../internal/infrastructure/cache/redis_queue.go) and the matching `handleParseFailure` at line 443.

**Why this matters for CAP / consistency:**

The current design has two compensation paths sharing one revert primitive:
- **Path 1**: worker exhausts `WORKER_MAX_RETRIES` (default 3) → `handleFailure` → `RevertInventory` → DLQ → XACK. Treats failure as transient-with-budget.
- **Path 2**: parse-failure / malformed-classified → `handleParseFailure` → may revert via "legacy hints" → DLQ. Treats failure as deterministic.

The classifier that decides which path is taken (`WORKER_MAX_RETRIES`-based) is a **fixed-budget timer**, not a content-based discriminator. A deterministic-failure message that always errors (e.g. malformed `quantity=-5` that slips past validation, persistent FK violation against a deleted ticket_type) burns through 3 retries × 100ms = 300ms of wasted PEL claims before reaching DLQ. At 8,330 acc/s ceiling (`docs/saturation-profile/`) this is ~2.5K wasted retries per stuck message. The first review's §2.3 mentions the budget but doesn't ask: **is the retry budget content-aware?**

This is a CAP-relevant sub-dimension because:
1. Under load shed conditions (Redis slow, DB slow), the budget burns faster on transient errors, increasing the *probability* a poison-pill message starves transient ones inside the same consumer's claim window.
2. Stripe's idempotency rationale (cited by the first review elsewhere) makes the same distinction — `card_declined` is deterministic and should not be retried; `rate_limited` is transient and should. The project's worker classifier doesn't (yet) carry that taxonomy through.
3. The Phase 2 checkpoint ([`docs/checkpoints/20260430-phase2-review.md`](../checkpoints/20260430-phase2-review.md) row A1) and [`docs/post_phase2_roadmap.md`](../post_phase2_roadmap.md) Phase 2.5 already landed a DLQ classifier (A1, PR #36) for malformed messages — but the **classification rule** is "JSON parse fails" only, not "deterministic vs transient business failure".

**Suggested follow-up (severity: NIT or MINOR depending on traffic shape)**: enumerate deterministic-error sentinels in `worker.QueuedBookingMessage` processing (`domain.ErrUserAlreadyBought`, `domain.ErrTicketTypeNotFound`, etc.) and route them to DLQ on FIRST occurrence rather than after the full retry budget. The retry budget should be **temporal-only**: transient timeouts, transient FK violations, transient Redis errors. The seam already exists at [`internal/application/worker/message_processor.go`](../../internal/application/worker/message_processor.go) — add a small `IsDeterministic(err)` helper and branch in `handleFailure` caller.

This is a sub-dimension the first review's PEL-retry analysis brushed past, and it's the one most likely to bite under Phase 4's higher-concurrency conditions where poison-pill detection matters more.

---

## 6. Verdict + sign-off

**Partial pushback.** The first review is technically literate and the §2 (dual-tier inventory consistency), §4 (saga / recon DEF-CRIT closed), and §5 (RYW honesty) analyses are accurate and well-traced. The MINOR-3 revert.lua dual-namespace finding is the most concretely-actionable observation in the report and should be picked up directly. The §3 advisory-lock analysis is structurally correct and the recommended fix is good engineering — but bundling it as MAJOR conflates "would-be-correct under Phase 4 deployment" with "is-broken-in-v1.0.0". The MAJOR-3 payment_failed framing reads the code right but reads the design wrong: the code at `webhook_service.go:482-485` contains an explicit decision-comment that the first review didn't quote, and the memory note it derives from is a 12-day-old draft worry that the live code has since explicitly rejected. The two fabricated citations (Stormatics, AWS Prescriptive Guidance) violate the project-level citation-integrity rule the user committed to in PR #109 and need to be either replaced with real sources or dropped to match the actual content. Headline: the project is in better CAP shape than the first review's severity histogram suggests, and the genuine wins (MINOR-3 + MINOR-2 + the saga-fence robustness already verified in §3) should be the part that gets surfaced upward.

---

## Net recommendations (sorted by expected value)

1. **Action MINOR-3 (revert.lua dual-namespace) verbatim from the first review.** Cheapest, most concrete, no controversy.
2. **Open an ADR** (`docs/adr/0002_payment_failed_compensation_semantics.md`) that captures the explicit "no in-window retry, expiry is the only TTL gate" decision now in `webhook_service.go:482-485`. The decision exists in code; it should exist in an ADR. This **resolves** MAJOR-3 as a deliberate position rather than leaving it as a memory-vs-code tension. Reference [`docs/architectural_backlog.md` § "Bulkhead pattern" entry shape](../architectural_backlog.md) for "what / why deferred / revisit when" formatting.
3. **Replace fabricated citations.** Drop the Stormatics and AWS Prescriptive Guidance quotes in their current form; either find real industry text that says what the first review claims, or rephrase the first review to lean only on the Shopify, Kerkour, and GreptimeDB references (which check out).
4. **Bundle MAJOR-1 + MAJOR-2 fixes into Phase 4 P1 (Redis HA + DB HA) PR scope** instead of treating them as v1.0.0 follow-ups. The fixes are correct; the timing is wrong. Add explicit acceptance tests (cached-conn close mid-loop test for MAJOR-1; row-claim concurrency test for MAJOR-2) as Phase 4 P1 entry criteria.
5. **NIT-2 (Shopify cross-reference in `redis_runtime_metadata_scaling.md`)** is a strong interview-narrative win. Action verbatim.
6. **Address the missed angle (PEL retry budget vs deterministic-failure detection)** as a small follow-up that builds on the existing DLQ classifier (A1, PR #36). Concrete: add `IsDeterministic(err) bool` helper in `internal/application/worker/`; branch in `handleFailure` caller. ~30 LOC + 3 tests.
7. **NIT-1 (relay leader-election doc softening)** — defer to the same PR that lands MAJOR-1 fix in Phase 4 P1. The doc says what the code does today; tightening the doc without tightening the code reads as overcautious.

---

## Executive summary (≤150 words) back to the parent

`docs/reviews/cap-meta-review.md` — **verdict: partial pushback.** First review is technically literate but mis-rates severity on the two outbox advisory-lock findings (MAJOR-1 + MAJOR-2): the v1.0.0 stack has neither PG replica nor PgBouncer, so the cached-conn failure mode the review describes is structurally absent today, and the project explicitly defers HA work to Phase 4 ([`docs/post_phase2_roadmap.md` lines 177-187](../post_phase2_roadmap.md)). Re-classify as NIT-pending-Phase-4. MAJOR-3 payment_failed retry: the code at [`webhook_service.go:482-485`](../../internal/application/payment/webhook_service.go) contains an explicit counter-decision (*"NO reservation-window guard here — failure is failure"*) that the first review didn't quote; the memory note it derives from is a 12-day-old draft worry. **Top push-back**: 2 of 5 spot-checked citations (Stormatics, AWS Prescriptive Guidance) are fabricated paraphrase — they say nothing of what is attributed to them. Cleanest actionable wins: MINOR-3 (revert.lua dual-namespace) + open an ADR for the payment_failed semantics decision.
