# Why Redis is ephemeral, not durable — the 411-of-1000 silent message loss that drove our cache-truth refactor

> **TL;DR.** A FLUSHALL during smoke testing dropped 411 of 1000 in-flight messages and we couldn't tell. The fix wasn't to make Redis durable — it was to commit to "Redis ephemeral, Postgres source of truth, drift detected and named" as an explicit architectural contract, then build the rehydrate / NOGROUP-self-heal-alert / drift-detector layers that contract requires. Five PRs, ~6 weeks elapsed, ~1,300 LOC. Quantitative outcome: drift detector confirms zero unintended drift across multiple stress runs; consumer-group recreation counter stays at 0. The deeper outcome: a coherent answer to "what does Redis do here?" that previously had no clean phrasing.

Pairs with [v0.4.0 release](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0). For the series template, see [`docs/blog/README.md`](README.md).

## Context

Our async pipeline at the time of the incident:

```
POST /book → API → Redis Lua (DECRBY + XADD)
                 ↓                     ↓
                 (HTTP 202)            orders:stream
                                       ↓
                                       Worker (XReadGroup)
                                       ↓
                                       Postgres (INSERT order + outbox)
                                       ↓
                                       Kafka → payment service / saga compensator
```

The hot path was working. C500 benchmarks held 8,330 accepted bookings / sec. Lua deduct + XADD was atomic; the consumer group preserved exactly-once-ish processing via PEL recovery. The Phase 2 reliability sprint (CP1-CP9) had landed runbooks, Alertmanager, multi-process metrics, the integration test suite. By every measure I'd been thinking with, the system was in good shape.

Then we re-ran a smoke test that involves `make reset-db` between stress runs. The reset path called `FLUSHALL` to clean Redis. The next run produced 1000 booking attempts, all returned HTTP 202, but the worker only processed 589 of them. The other 411 were silently gone — no DLQ entry, no error log, no metric increment. The customers had been told their bookings were accepted; the orders never reached Postgres.

This wasn't a worker bug. The worker was healthy. The 411 messages weren't *processed-and-lost* — they were *never-delivered-to-the-worker*. They had been XADDed into the stream BEFORE FLUSHALL, then FLUSHALL deleted the stream, then the worker's XReadGroup hit `NOGROUP`, the worker's NOGROUP self-heal recreated the consumer group via `XGROUP CREATE ... $`, and the `$` argument explicitly means "start from the current end of the stream — skip everything before this moment." So 411 messages got skipped over, not lost in the sense of "we tried to deliver them and failed", but in the more dangerous sense of "we never even attempted to deliver them, and nothing in the system noticed."

The silence is the part that kept me up. We had alerts on every worker-side failure mode: XAck failures, parse errors, retry-budget exhaustion, DLQ writes, saga compensation failures, recon mark errors. The one failure mode we had no signal for was "the consumer group disappeared and the recreation skipped over messages we accepted from customers."

What was Redis supposed to do here? When we sat down to rephrase the architecture and answer that question carefully, we realised the answer had been *incoherent* the whole time. The 411-message loss wasn't a bug in the implementation. It was the architecture being incoherent and the implementation faithfully executing whatever interpretation had been most convenient at each commit.

## Options Considered

Three serious paths, plus a non-starter for completeness.

### Option A — Make Redis durable

Turn on AOF (append-only file) and RDB (snapshot) so Redis becomes its own source of truth that survives crashes and FLUSHALLs. The argument: if Redis loses data, nothing downstream can rebuild it; therefore Redis should be durable.

Trade-offs:

- **Lua deduct latency** would degrade meaningfully — every XADD now hits AOF disk fsync. Stack Overflow's published Redis benchmarks show ~30-50% throughput drop with `appendfsync everysec`, ~80% with `appendfsync always`. We'd give up the headline 8,330 acc/s for an architecture-level claim ("Redis is durable") that has no operational consequence we'd actually exercise.
- **NOGROUP recovery would still be broken**. AOF replay on Redis startup would restore the stream, but a manual `FLUSHALL` (the actual incident trigger) bypasses AOF entirely. Same for `XGROUP DESTROY`. Same for an operator running `DEBUG SLEEP` for too long while another tab executes `FLUSHALL`. Durability addresses crashes, not operator-induced state-loss.
- **Splits the truth** between Redis (now claiming durability) and Postgres (still our DB of record). Two sources of truth that disagree on edge cases is the configuration where every senior incident report begins with "and then we discovered the database and the cache had different versions of..."

This is a real option that real systems pick. It wasn't right for ours.

### Option B — Distributed locking between Redis and DB

Use a Redlock (or Redis-equivalent transactional pattern) so that "Redis decremented" and "Postgres committed" are atomic. The argument: if they can't disagree, drift can't develop.

Trade-offs:

- **Adds a coordinator** (the lock service) on the hot path. Every booking now does at least one extra round-trip to acquire the lock, do the work, release the lock. Hot-path latency rises; the load-shed-by-Lua-serialization property becomes a load-shed-by-lock-contention property, which is more complex to reason about.
- **Doesn't solve the FLUSHALL case at all.** The lock prevents simultaneous-write disagreement. It doesn't prevent "operator wiped the cache, so the cache says one thing now and the DB says another." That's a different failure mode.
- **Redlock specifically has [well-known correctness debates](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html)** when used as a primary safety mechanism rather than a performance optimisation.

I considered this for about an hour and put it down. The locking discussion was a distraction; we didn't have a concurrency bug, we had an architectural ambiguity about which store was authoritative.

### Option C — Treat Redis as ephemeral, Postgres as truth, build the layers

Commit to the contract:

> Redis is ephemeral. Postgres is the source of truth. Drift is detected and named.

Then build the layers that contract requires:
- A boot-time rehydrate that reconstructs Redis from Postgres so a fresh / FLUSHED Redis converges to correctness without operator action
- An alert on consumer-group recreation events so the silent-message-loss case is surfaced the moment it happens, not minutes-to-hours later via downstream effects
- A drift detector that compares cache vs source-of-truth on a periodic sweep so any state divergence is observable and named (not just numeric — *labeled* with the failure-mode direction)

Trade-offs:

- **More code than Option A**. Rehydrate, alert wiring, drift detector, runbooks, three new metrics. Roughly 1,300 LOC across five PRs.
- **Doesn't prevent the loss**, only makes it visible. After the rehydrate path landed, a future FLUSHALL would still drop in-flight messages between FLUSHALL-time and the next worker tick — but the alert would fire within 60 seconds and the operator would know about it.
- **Operationally cleaner**: matches how Alibaba describes their flash-sale stack (Redis hot path, MySQL truth, async reconciliation) and how the Stripe blog frames inventory storage ("the durable store, not the cache, is the inventory of record"). When the project's architectural choice matches the published industry pattern, every "why did you do it this way?" interview question becomes a citation and a trade-off discussion rather than a defense.

### Option Z — "Just don't run FLUSHALL"

Operationally tempting, intellectually a fail. The audit-from-first-principles question is "what does the system do when it sees state it shouldn't see?" Answering "the operator should never put us in that state" is the architecture telling on itself.

## Decision

Option C, executed across five PRs over six weeks. The contract was written down explicitly in [`docs/architectural_backlog.md`](../architectural_backlog.md) § Cache-truth architecture before any code landed, so every PR reviewer and every future-me had a definition to test commits against.

| PR | Closes | Scope |
| :-- | :-- | :-- |
| [#73](https://github.com/Leon180/booking_monitor/pull/73) (PR-A) | Reset path was the trigger | `make reset-db` switched from FLUSHALL to precise DEL on cache keys; preserves Redis stream + consumer group across resets so the test reset stops being a NOGROUP-trigger. |
| [#74](https://github.com/Leon180/booking_monitor/pull/74) (PR-B) | Recovery from a clean Redis | App-startup `RehydrateInventory` — SETNX-not-SET, advisory-lock-serialised across multi-instance startup; explicit `appendonly no` + `save ""` Redis config marking ephemeral by design. |
| [#75](https://github.com/Leon180/booking_monitor/pull/75) (PR-C) | Detection of the actual incident | `consumer_group_recreated_total` counter + `ConsumerGroupRecreated` critical alert (no soak window — single occurrence pages). Pairs with runbook so the silent-message-loss case is detectable instead of buried. |
| [#76](https://github.com/Leon180/booking_monitor/pull/76) (PR-D) | Naming the drift | `InventoryDriftDetector` co-resident with the reconciler. Compares Redis cached qty vs Postgres `events.available_tickets` every 60s; three direction labels (`cache_missing` / `cache_high` / `cache_low_excess`) route to distinct runbook branches. |
| [#77](https://github.com/Leon180/booking_monitor/pull/77) | Outbox-relay analogue | `outbox_pending_count` gauge with the same "ephemeral cache vs durable source-of-truth" framing, just for the Postgres-outbox → Kafka bridge instead. Emits 0 on DB failure (not stale), so the backlog alert can't fire on stale data during a DB outage. |

Two cross-cutting hardenings landed in the PR-D fixup commits:

- Sweep goroutine panic recovery in all three sweepers (Reconciler, InventoryDriftDetector, SagaWatchdog) via `safeSweep` helper with `runtime/debug.Stack()` capture + `sweep_goroutine_panics_total{sweeper}` counter + `SweepGoroutinePanic` critical alert. Closes the "loop dies silently while process stays up, /metrics keeps serving stale gauges" silent-failure shape that two reviewer agents flagged independently.
- Stack-trace capture in the recovery log was a HIGH finding from the second review pass — `fmt.Errorf("panic: %v", rec)` formats only the panic *value*, not the stack. The runbook had been claiming operators could "see the panic value + stack trace" while the implementation only logged the value. `debug.Stack()` is now invoked inside the deferred recover block where the goroutine is still in the panicking frame.

## Result

### Quantified

| Signal | Before | After |
| :-- | :-- | :-- |
| Smoke-test reset path | `FLUSHALL` (destroys stream + group) | precise `DEL event:* idempotency:* saga:reverted:*` (preserves stream + group) |
| `consumer_group_recreated_total` | unmonitored | alert on FIRST occurrence, no soak window |
| Boot-time rehydrate | none | runs as fx OnStart hook BEFORE HTTP listener starts; failure aborts startup |
| Drift detection | none | sweeps every 60s; three direction labels distinguish failure modes |
| Outbox backlog | only `redis_stream_length` (the wrong stream) | `outbox_pending_count` + warning alert at >100 for 5m |
| Sweeper goroutine panic | silent (loop dies, /metrics keeps serving stale data) | recovered + counter + critical alert + stack trace in log |
| Drift after reset (smoke test) | unknown — no detector | confirmed 0 across multiple runs |

### Side effects, both directions

The win we expected: the FLUSHALL incident path is now end-to-end observable. The smoke test that originally produced the 411-message loss now produces a `ConsumerGroupRecreated` alert within 60 seconds; the runbook tells the operator to cross-check `bookings_total` vs DB orders count, and the drift detector confirms no inventory drift remains after the recovery. The original failure mode is still possible, but it can no longer be silent.

The win we didn't expect: writing the contract down made every future architectural decision easier. When the recon process needed Redis access for drift detection (PR-D), the question "should this be served by a separate Redis instance?" had a one-line answer: no, Redis is ephemeral so a single instance is structurally fine — durability isn't a property we're asking Redis to provide. When the outbox backlog gauge needed to decide on stale-vs-zero-on-error semantics (#77), the contract said "the cache reading 0 means we can't tell, the truth signal is the errors counter" — same shape as the inventory drift gauge. The contract turned three independent design decisions into three applications of one rule.

The trade-off we accepted: more code. Five PRs, three new alerts, two new runbook sections, one new sweep loop, and ~1,300 LOC. The bilingual monitoring docs (EN + zh-TW) kept growing in lockstep with each PR. If we'd picked Option A (make Redis durable), the diff would have been about ten lines plus a config change. The cost of explicitness shows up in commit count.

### Industry alignment

After landing the changes, I asked a research agent to map our contract to industry practice and find anything that contradicted us. Verdict: every architectural choice came back **STANDARD** with one exception:

| Choice | Verdict | Source |
| :-- | :-- | :-- |
| Postgres as source-of-truth + Redis ephemeral hot path | STANDARD | [Alibaba flash-sale architecture](https://www.alibabacloud.com/blog/system-stability-assurance-for-large-scale-flash-sales_596968) explicitly: inventory in Redis, persistent DB authoritative. [Stripe inventory pattern](https://stripe.dev/blog/how-do-i-store-inventory-data-in-my-stripe-application) same shape. |
| `appendonly no` + `save ""` (pure ephemeral Redis) | STANDARD | [AWS ElastiCache caching patterns](https://docs.aws.amazon.com/whitepapers/latest/database-caching-strategies-using-redis/caching-patterns.html) supports this directly. Memcached's design principle is identical. |
| Boot rehydrate via SETNX (preserve in-flight values) | NOVEL but sound | No published reference for SETNX-not-SET specifically, but the underlying logic — don't clobber values in flight — is the right one. |
| Lua atomic deduct (DECRBY + XADD) | STANDARD | Alibaba calls this out by name: "transaction feature of LUA scripts" for "deducting after reading remaining inventory." [Games24x7 case study](https://medium.com/@Games24x7Tech/running-atomicity-consistency-use-case-at-scale-using-lua-scripts-in-redis-372ebc23b58e) demonstrates at scale. |
| Periodic drift reconciliation | STANDARD (rarely documented in public) | [SPIFFE incident log #4973](https://github.com/spiffe/spire/issues/4973) explicitly recommends "periodic full-db reconciliation of cache." Universally practiced; rarely written about. |
| No distributed locking (coherence via Lua + outbox) | STANDARD | [Redis project's own guidance](https://redis.io/glossary/redis-lock/) explicitly discourages SETNX-based locks in favour of fault-tolerant patterns; for our case, Lua atomicity + transactional outbox covers the consistency requirement without locking. |

The one NOVEL item is the SETNX-on-rehydrate refinement. It's a sound refinement of a standard pattern, not an anti-pattern. I'm comfortable defending it under interview questioning.

The most surprising finding of this research: the periodic-drift-reconciliation pattern is **standard but rarely documented publicly**. Every senior practitioner I asked offline ("yes of course you reconcile cache against truth") agreed it was obvious; almost no engineering blog spells it out. SPIFFE's GitHub issue is the closest thing to a public reference. This means (a) we're in good company doing it, but also (b) we wrote one of the more public references for the pattern when we documented our PR-D drift detector and runbook.

## Lessons

### The 411-message loss wasn't a bug — it was the architecture being incoherent

The implementation was faithfully executing whatever interpretation of "what does Redis do" had been most convenient at each commit. There were no broken assertions, no missing error handlers in the conventional sense; every line of the code was doing what its author had intended. The bug lived in the gap between the implementations: the authors of the deduct path, the worker's NOGROUP self-heal, the FLUSHALL-using reset script, and the saga compensator each had a slightly different mental model of which store was authoritative, and none of those mental models was written down.

The fix wasn't to add error handling. The fix was to write the contract down, then change the implementation to match. The contract is now in `docs/architectural_backlog.md` and the diff history has the receipts. Future-me, reviewing future-PRs, has a definition to test against.

### The "cache as durable storage" anti-pattern is sneaky

If you'd asked me before this incident whether our system treated Redis as durable, I'd have said no. We had Postgres. We had outbox. We had saga. We obviously knew Redis was ephemeral. Then I went looking for the assumption in the code and found that the worker's NOGROUP self-heal said "recreate the group from `$`" — which only makes sense if the *messages we're going to receive after now* are sufficient. Which is only true if Redis is durable. Which it isn't. The code was incoherent in exactly the place we'd already verbally said wasn't the problem.

The smell I'd watch for, retrospectively: anywhere the code recovers from cache loss without doing something equivalent to "rehydrate from truth", you're either claiming durability you don't have or you're losing data. Our NOGROUP self-heal was Option 2; we didn't notice until it cost us 411 messages.

### Drift detection is observability, not auto-correction

PR-D's `InventoryDriftDetector` does not auto-correct. It detects drift, names the failure mode, and pages the operator. Three different reviewers asked some variant of "shouldn't this also fix the drift it finds?" The answer is no:

- Auto-correction races with in-flight Lua deducts. Writing to a Redis key from the sweep loop while concurrent customers are decrementing it via Lua is exactly the kind of multi-writer race the original architecture was designed to avoid.
- Auto-correction without operator visibility hides the underlying failure. If saga compensation is desyncing in production, "drift detector silently fixes it" lets the bug live for months. "Drift detector pages the operator with `direction=cache_high`" surfaces the bug within minutes.
- The recovery action — re-run rehydrate from DB truth — is destructive enough (it touches the live cache) that it should be operator-gated, not autonomous.

The conservative default — detect + name + alert, don't fix — is what production observability should look like. "Self-healing" is a marketing word; in distributed systems most self-healing is silent symptom suppression.

### What I'd do differently

Three things, in order of how confident I am about each:

**1. Write the contract document first, before the first PR.** I wrote the contract during PR-A, after I'd already started coding. It was the single highest-leverage piece of writing in the project; doing it earlier would have made the PR sequence cleaner and the review discussions shorter. This is the version-of-Gall's-law I'll be teaching myself for the next ten years: an architectural decision that can't be written down in two paragraphs probably hasn't been made yet.

**2. Treat the bilingual docs contract as the architecture's executable spec.** The most valuable test of "did the change actually land?" turned out to be the bilingual `docs/monitoring.md` + `docs/monitoring.zh-TW.md` updates. If a metric or alert wasn't documented in both, the PostToolUse hook caught it; if the prose was vague, the act of translating the EN version into zh-TW forced it to be precise. The bilingual contract isn't documentation hygiene; it's a forcing function for explicit thinking.

**3. Run the multi-agent review pass earlier in PR-D's review cycle.** The two HIGH findings the reviewers caught (unsafe `errors.Is(err, ctx.Err())` pattern + `(0, nil)` ambiguity in `GetInventory`) were both subtle enough that I would have shipped them. The fixes were small (~30 LOC across two commits). The cost was a one-day delay in landing the PR; the win was avoiding two latent-bug-shaped review findings hitting an unrelated PR a month later. I now treat "did the multi-agent review say HIGH on anything?" as a merge gate, not a "nice-to-have", for any PR touching the resilience surface.

## What's next

This is post 1 of a planned five-post series ([`docs/blog/README.md`](README.md)). Post 2 will cover the Lua single-thread ceiling at 8,330 acc/s and how to think about the next 10× — pairs with the [VU stress test benchmark archive](../benchmarks/) and the 1M QPS analysis we ran in Phase 2.

The cache-truth contract continues to pay dividends in Phase 3 design work. When the Pattern A reservation flow needs to extend the booking state machine with `awaiting_payment`, the question "what's the source of truth for the reservation TTL?" has a one-line answer: Postgres `orders.reserved_until` is the source of truth, the Redis-side reflection (if any) follows the cache-truth contract — drift between them is detectable and named, not magically fixed. The work in v0.4.0 turned a class of unsolved questions into one rule we apply consistently.

---

*If something in this post is wrong or missed, please open an issue or PR against [`docs/blog/2026-05-cache-truth-architecture.md`](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-cache-truth-architecture.md). The blog is in-repo precisely so corrections go through the same review process as code.*
