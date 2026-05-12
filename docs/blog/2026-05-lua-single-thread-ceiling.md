# The Lua single-thread ceiling at 8,330 acc/s — how to think about the next 10×

> 中文版本(主要):[2026-05-lua-single-thread-ceiling.zh-TW.md](2026-05-lua-single-thread-ceiling.zh-TW.md)

> 🚧 **2026-05-12 update — Hypothesis 4 conclusion is under re-investigation.** A subsequent Redis baseline benchmark (`make benchmark-redis-baseline`, laptop docker) shows deduct.lua's **server-side aggregate throughput is 273k RPS**, **not** 8,330. The 8,330 acc/s is the throughput cap of the full booking_monitor HTTP pipeline — **not** the cap of Redis Lua same-key serialisation. Full data: [`docs/benchmarks/20260512_200714_redis_baseline/summary.md`](../benchmarks/20260512_200714_redis_baseline/summary.md). The real bottleneck (Gin handler / idempotency middleware / Go-redis RTT / etc.) needs a Go pprof to pinpoint. **The rest of this post is preserved as-is pending a one-shot rewrite after pprof analysis.**

> **TL;DR.** A VU scaling stress test from 500 to 5,000 VUs showed `accepted_bookings` plateauing at ~8,330/s the whole time. Redis CPU was 53%, the connection pool wasn't saturated, the PG pool wasn't waiting. The bottleneck is **single-key Lua write serialization** — a physical ceiling. But the next step isn't "don't shard"; it's the industry's two-layer sharding model: **(1) section-level (business axis) — independent inventory per price tier / section, designed in from day one; (2) hot-section sub-sharding (infra axis) — quota pre-allocation across multiple Redis instances for sections expected to be hot**. Generic hash sharding (the textbook version) is rarely used in production — real systems are business-axis primary, infra-axis as a hot-spot backup. **The order is still "infra first, Redis last", but the "Redis change" itself shifts from "single → cluster" to "per-section multiple instances + hot-section quota router".**

Pairs with the [VU scaling benchmark archive](../benchmarks/) + [saturation profile](../saturation-profile/). Series template: [`docs/blog/README.md`](README.md). Section-level sharding design captured in [`docs/architectural_backlog.md`](../architectural_backlog.md) § Section-level inventory sharding.

## Context

The trigger was a VU scaling stress test. We pushed concurrency from 500 to 5,000 to see where the booking hot path would actually break. Each setting hit the same 500k-ticket pool for 60 seconds, with a reset between runs.

| VUs | http_reqs/s | accepted_bookings/s | p95 | business_errors |
| --: | --: | --: | --: | --: |
| 500   | 46,475 | **8,330** | 14.45 ms | 0.00% |
| 1,000 | 41,919 | **8,331** | 23.81 ms | 0.00% |
| 2,500 | 36,156 | **8,332** | 31.68 ms | 0.00% |
| 5,000 | 34,264 | **8,314** | 174.36 ms | 0.00% |

A few observations:
- **`accepted_bookings/s` is flat at 8,330/s** across 500 → 5,000 VUs
- Total `http_reqs/s` actually *drops* from 46k to 34k as VUs rise (more VUs = each VU does fewer round-trips per second; the system is queuing)
- p95 has a hockey-stick between 2,500 → 5,000 VUs (32ms → 174ms)
- `business_errors` stays at 0.00% — the system isn't breaking, just queuing

This is healthy degradation, not failure. But **where does 8,330 come from?**

I ran a saturation profile to capture the hot-path state at peak:

| Signal | Measured | Meaning |
| :-- | --: | :-- |
| Redis main thread CPU | **53%** | Half the headroom still available |
| Redis client pool hits/s | 35,708 | Connection reuse working |
| Redis client pool timeouts/s | **0** | No goroutine waiting for a connection |
| Redis client pool used / total | 23 / 200 | Pool barely touched |
| PG pool in-use | **2** | Not waiting on DB |
| Go CPU (top frame) | ~33% Syscall6 | I/O dominant; writes 4× reads |
| p99 latency | 19.9 ms | Healthy |

**Every resource is idle.** The bottleneck is clearly not CPU, not the pool, not the DB, not the network thread. So where?

## Options Considered

Four hypotheses, each tested against the data.

### Hypothesis 1: Redis CPU is saturated

**DISPROVED.** Redis main thread is at 53% CPU under 35k cmd/s. Stack Overflow's published architecture serves 87k cmd/s at 2% Redis CPU (their workload is pure GET/SET, no Lua eval) — our Lua at 35k cmd/s and 53% CPU is consistent (Lua is 5-10× more expensive than GET/SET). **Still ~50% headroom from saturation. Redis CPU isn't the bottleneck.**

### Hypothesis 2: Go-redis client pool exhaustion

**DISPROVED.** Pool stats show 0 timeouts, 0 misses, 35k hits/s, and 23 of 200 connections in use. **The pool is barely touched.**

### Hypothesis 3: Docker Desktop on Mac NAT bottleneck

**POSSIBLE FOR REQ/S, NOT FOR ACCEPTED_BOOKINGS.** Docker Desktop on macOS has a published bridge-NAT ceiling around 650 Mbps, which translates to roughly 80k req/s for small payloads. This explains why total req/s plateaus around 34-46k (well under 80k), but **does not explain why accepted_bookings is stuck at 8,330**. Total req/s and accepted bookings/s are two different numbers.

### Hypothesis 4: Lua single-thread serialisation on the same key

**CONFIRMED.** Redis is single-threaded. Every Lua eval against the same `event:{uuid}:qty` key serialises — two concurrent bookings cannot run `deduct.lua` in parallel. Each Lua call does `DECRBY` + a conditional + `XADD`, ~100μs of work. 100μs × serialised execution = ceiling of ~10,000 ops/s. **The observed 8,330 fits, with the slight gap accounted for by client/network overhead beyond Lua.**

So the bottleneck is **single-key Lua write serialisation**. Not Redis's network, not its overall CPU, not the client. It's a physical limit: **single thread × single key × Lua execution cost per call.**

### Hypothesis 4 supplement: Per-op breakdown — why 100µs (and why this doesn't contradict the "Redis can do 100k+" claim)

A natural pushback: "[Redis's own benchmarks](https://redis.io/docs/latest/operate/oss_and_stack/management/optimization/benchmarks/) cite 100k–200k ops/s per single instance — your 8,330 looks an order of magnitude too slow." Fair, but the two numbers measure different things.

**Where the redis-benchmark numbers come from** (sources: [DigitalOcean Ubuntu 18.04 benchmark](https://www.digitalocean.com/community/tutorials/how-to-perform-redis-benchmark-tests) + [Redis official benchmark docs](https://redis.io/docs/latest/operate/oss_and_stack/management/optimization/benchmarks/)):

| What's measured | Conditions | Observed |
|:--|:--|:--|
| `SET` no pipeline | 50 clients | ~72,000 RPS |
| `SET` with `-P 16` pipelining | 50 clients × 16-op pipeline | ~1.5M RPS |
| `GET` with `-P 16` | same | ~1.8M RPS |
| `XADD` alone (small field set) | bare-metal ARM | ~136k RPS ([source](https://learn.arm.com/learning-paths/servers-and-cloud-computing/redis-cobalt/redis-benchmark-and-validation/)) |
| EVALSHA 100-GET single script | bare-metal, pipelined | ~1.9M effective ops/s ([source](https://www.coreforge.com/blog/2022/02/redis-lua-benchmark/)) |

**The gap between our deduct.lua and these benchmarks isn't Redis getting slower** — they measure different things. Here's what one of our hot-path invocations actually does:

```
deduct.lua one-invocation internal cost (approx, laptop docker):
├── EVALSHA invocation overhead              (~5-10 µs)
├── DECRBY ticket_type_qty:{id} -1           (~5-10 µs)
├── HMGET ticket_type_meta:{id} (3 fields)   (~10-20 µs)
├── multiply_decimal_string_by_int (Lua CPU) (~10-30 µs, size-dependent)
├── XADD orders:stream * (9 field-value)     (~30-50 µs, radix tree maintenance)
└── client/network round-trip                (~20-50 µs, Docker veth NAT)
                                              ─────────────
                                              ~80-170 µs/invocation
```

Doing the math: 1,000,000 µs ÷ ~120 µs ≈ **8,333/s on this setup**. Matches the observed 8,330.

**How does hardware / setup change this?**

| Setup | Expected deduct.lua ceiling | Why |
|:--|:--|:--|
| **laptop docker on macOS** (our current) | ~8,000-10,000 /s | Docker veth NAT + macOS-on-Linux VM overhead + ARM/x86 binary translation |
| **same setup, Linux bare host** | ~12,000-20,000 /s | strips Docker NAT + macOS VM layer |
| **bare-metal Linux server / large cloud VM** | ~20,000-40,000 /s | pure loopback / Unix socket + high-frequency CPU (Lua loves single-core clock) |
| **Redis 7 `io-threads 4`** | still ~20,000-40,000 /s | io-threads parallelise socket I/O — **doesn't help single-key Lua** (Lua exec is still single-thread) |

**So "8,330 physical ceiling" needs precise framing**: **for the same inventory key running deduct.lua, on laptop docker, the ceiling is 8,330/s**. Bare-metal would likely see 3-5×, but **single-thread × single-key × Lua-eval serialisation is not solvable by hardware** — that's an architectural cap, only sharding (Tier 5a/5b) breaks through it.

**Why does SET/GET look so much faster in comparison?**
- SET / GET are single ops + pipelineable (`-P 16` gives a 20× factor)
- deduct.lua does 5 ops internally + is *not* pipelineable (each Lua call is one atomic round-trip)
- So one deduct.lua invocation costs roughly the same as 5 sequential ops, paying 5× the serialised cost of a pipelined SET

Full methodology in [`scripts/redis_baseline_benchmark.sh`](../../scripts/redis_baseline_benchmark.sh): runs 4 baseline modes (raw SET, SET pipelined, EVAL no-pipeline, XADD) side-by-side with our deduct.lua so the breakdown is reproducible on your setup.

> 💡 **Senior-interviewer-proof answer**: "8,330 is same-key Lua throughput on laptop docker. Bare-metal should land around 25-40k because Lua runs on higher-clock Linux CPU with no Docker NAT. But **this ceiling isn't a saturated Redis instance** — Redis main thread CPU is only 53% at 8,330/s. The bottleneck is the physical nature of same-key Lua serialisation, which is consistent with — not contradicted by — Redis SET/GET running 100k+, because SET/GET pipelines and deduct.lua can't. Scaling past this requires sharding the inventory key per (event_id, section_id), not upgrading the Redis instance."

## Decision

A two-tier decision.

### Short-term: **single-key is "currently sufficient", not "sufficient therefore good"**

Current state: single Redis instance + single inventory key, 8,330 acc/s ceiling. Under that ceiling, a 100k-ticket event sells out in 12 seconds and customers experience a smooth flow. We don't currently need sub-second response on million-ticket events, so **we don't urgently need sharding**.

But it's worth being precise: **"8,330 acc/s is enough" is determined by business goals, not architecture goals**. If the business requirements shift to:
- Million-ticket single events (Taylor Swift World Tour, KKTIX VIP standing-room releases)
- Sub-second response to "did I get a ticket?" (currently the total flow takes 12 seconds)
- Many concurrent events (dozens of small events going on sale simultaneously)

…then single-key isn't enough — not because Redis got slower, but because the business goal pushed the ceiling up.

### Medium-term: two-layer sharding, the industry consensus

After researching real flash-sale / ticketing systems (Alibaba Singles' Day, Ticketmaster Verified Fan, China Railway 12306, JD.com flash sales, KKTIX), the universal pattern is:

```
Layer 1 (business axis):
  Each event × section gets its own Redis key namespace.
  Applied to all multi-section events by default. No added complexity.

Layer 2 (infra axis, selective):
  Only sections expected to be hot get a second sub-sharding layer.
  Method: quota pre-allocation (NOT hash sub-sharding) — split 1000 tickets
  pre-allocated as 250 / 250 / 250 / 250 across 4 Redis instances.
  Router routes to whichever instance still has quota.
```

**Why quota pre-allocation beats hash sub-sharding**: hash sub-sharding requires SUM across N shards to detect sold-out, introducing cross-shard consistency concerns. Quota pre-allocation lets each shard see its own state — sold-out becomes a per-shard decision; only "the entire section is sold out" needs aggregation, and that's a binary AND across N shards (much cheaper than SUM).

**Industry alignment** (this pattern has substantially more public reference material than generic hash sharding):

| System | Layer 1 | Layer 2 (hot path) |
| :-- | :-- | :-- |
| Alibaba Singles' Day | Each SKU independent | Hot SKUs pre-allocated quotas across multiple Redis |
| Ticketmaster Verified Fan | Each event × ticket type independent | High-demand events get additional traffic splitting |
| China Railway 12306 | Train number + seat class | Hot trains' hot classes split across multiple Redis with an "inventory routing table" |
| JD.com flash sale | Product SKU | Hot SKU inventory pre-allocated to N nodes |

**All large-scale flash-sale and ticketing systems do this. No exceptions.**

### What we won't do: single-instance generic hash sharding

The earlier draft I wrote proposed `event:{uuid}:qty:0..N` hash sub-sharding. That's the textbook version of sharding; nobody uses it standalone in production. Every public ticketing/flash-sale architecture writeup goes business-axis first and considers infra sub-sharding only as a layer on top. The reason has been covered above: the SUM-on-sold-out cost in hash sub-sharding is real complexity; quota pre-allocation sidesteps it.

### Concrete implications for booking_monitor

- **Pattern A (D1-D7) schema design should add `section_id`** — even if we don't shard yet, supporting the section dimension in schema from day one makes future Layer 1 sharding "change routing, not the schema" instead of a schema migration. **Low-cost, high future-value design choice.**
- **Layer 1 (section-level Redis sharding) is a conditional PR for Phase 3+** — triggered by "we need to demo a multi-section event" or "benchmark shows section dimension reveals separation".
- **Layer 2 (hot-section quota router) is a portfolio-demo-grade PR** — implementing one hot section's quota router + N Redis instances + benchmark confirming N× linear scaling demonstrates the concrete answer to the standard "how do you handle hot keys" architecture question.

Full design captured in [`docs/architectural_backlog.md`](../architectural_backlog.md) § Section-level inventory sharding.

## Result

### Quantitative — revised tier table

| Tier | What changes | acc/s (per section) | acc/s (total) |
| --: | :-- | --: | --: |
| 1 (now) | Docker Mac, single Redis, single key | 8,330 | 8,330 |
| 2 | Bare-metal Linux | 8,330 | 8,330 |
| 3 | go-redis tuning | 8,330 | 8,330 |
| 4 | Redis 7 io-threads | 8,330 | 8,330 |
| **5a** | **Section-level Redis sharding (Layer 1)**, N=4 sections | 8,330 (per section) | **~33,000 (total)** |
| **5b** | **Hot-section quota router (Layer 2)**, M=4 sub-shards / hot section | 32,000+ (that hot section) | depends on hot-section count |
| 6 | Multi-region / federated cluster | depends on design | depends on design |

**Critical observation**: **Tiers 2 → 4 don't change `accepted_bookings/s`**. Network, client, Redis I/O threads — all of these are levers for *total throughput*, not for the Lua-on-same-key serialisation ceiling. To raise acc/s, you only have Tiers 5a/5b.

**5a is "design it in early"; 5b is "build it when business demands it"**. Together they push the ceiling to "total event tickets × per-shard 8,330 / Lua serialisation".

### What this means for ticketing scenarios

I wrote in the cache-truth post that **flash sales are latency-bounded, not throughput-bounded**. 100k tickets / 8,330 = 12 seconds to sell out. The customer-experience metric isn't "how many can we accept per second", it's "during those few seconds when tickets are selling, did I get one".

But the business goal shifts the conclusion in different directions:
- 100k tickets / "sold out in 12 seconds is fine" ⇒ single-key works
- 100k tickets / "sub-second response" ⇒ Layer 1 (section-level sharding) needed
- **1M tickets / "sub-second × selling out" ⇒ both Layer 1 and Layer 2 required**

### Industry alignment

Reviewing how real systems hit 1M+ QPS, the path matches our tier structure:

| Case | QPS | How |
| :-- | --: | :-- |
| **Stack Overflow** | 87k cmd/s @ 2% CPU | Single instance Linux, pure GET/SET, no Lua |
| **AWS ElastiCache r7g.4xlarge + Redis 7.1** | **1M+ RPS / node** | Enables I/O threads (Tier 4) |
| **Twitter** | 39M QPS / 10k instances | ~3.9k/instance (federated cluster) |
| **Alibaba Singles' Day** | Inventory peak 100k+/s | **Layer 1 + Layer 2 (SKU + hot SKU pre-allocated quota)** |
| **Ticketmaster Verified Fan** | Concert peaks at million+/s | **Layer 1 + Layer 2 (event × ticket type + hot-event traffic splitting)** |
| **China Railway 12306** | Spring Festival peak 10M+/s | **Layer 1 + Layer 2 (train × class + hot-train inventory routing table)** |

**No production case picks "make Redis durable" as the next step**. That intuitive option just isn't the path real systems take — consistent with the cache-truth post's conclusion.

## Lessons

### "Measure first, then optimise" should be on the wall

Before this work I had a hypothesis: "Redis is probably saturated; we need sharding". The saturation profile came back at 53% CPU and **directly contradicted me**. This is Knuth's "premature optimization is the root of all evil" applied concretely: **don't make architecture decisions without quantitative evidence**.

Had I trusted the intuition and done inventory sharding straight away, I'd have shipped a complexity-explosive PR that ran no faster (because Redis wasn't saturated), then spent two weeks debugging only to find Docker Mac NAT was the issue.

**The saturation profile became one of the project's most valuable tools**. It surfaces the synchronous state of 6 resources at once, making the bottleneck visible. I later wired `make profile-saturation` into the Makefile + docs as a standard step — every "I think X is slow" conversation now starts with running it.

### Intuition and reality often run in opposite directions

What I expected for the bottleneck order: Redis CPU → network → client.
What was actually true: **network (Docker NAT) → client → Redis CPU (very far from being hit on the Lua path)**.

The lesson: **discrete benchmark numbers beat continuous intuition**. Intuition says "Redis is single-threaded so it'll saturate first". The numbers say "no, Docker Mac's NAT hits the limit first". Intuition lost. **Remember this story for the next time someone says 'I think X is the bottleneck'.**

### `accepted_bookings/s` is the real metric for ticketing

This is a product-level lesson, not a technical one. Engineers easily fall into throughput-fetishism — see http_reqs/s plateau, immediately reach for scale. But for ticketing, **higher total req/s isn't business value**. How fast you can reject doesn't matter; how fast you can accept does.

Our dashboard now displays `accepted_bookings/s` and `http_reqs/s` separately, with the SLO bound to the former. Anyone reading the dashboard can tell within 5 seconds whether the system is "processing requests fast but rejecting most" or "actually selling tickets".

### Sharding isn't binary — it's "how many layers, on which business axis"

Earlier I framed sharding as a "do it / don't do it" question. I framed it wrong — implicitly assuming a generic-hash-sharding model, then concluding "complexity too high, don't do it". Wrong.

Nobody in production uses generic hash sharding standalone, because the SUM-on-sold-out cost is real. Every large flash-sale system uses a **business-axis primary, infra-axis hot-spot backup** two-layer pattern. Section-level sharding belongs in the schema from day one (low-cost, high future value); quota-router-style infra sub-sharding is reserved for hot sections only (expensive complexity, but only paid for the few paths that actually need it).

The takeaway: **the key sharding-design question isn't "should we shard"; it's "does our business model have a natural sharding axis?"**. For ticketing it's section / price tier; for e-commerce flash sales it's SKU; for messaging it's user_id or conversation_id. Find the business axis first, then decide whether to add infra sub-sharding for the hot parts.

### Write 8,330 down so future-us doesn't rediscover it

[PROJECT_SPEC](../PROJECT_SPEC.md), [monitoring docs](../monitoring.md), and this post all explicitly state "8,330 acc/s is the physical ceiling of single-key Lua serialisation". **Future-us, if someone says 'I think Redis is too slow', has a ready answer**. Without this record, in six months someone re-runs the same analysis and may reach a different (wrong) conclusion.

Technical writing's important function: **prevent successors from repeating the same investigation**.

## What's next

Post 3 will cover the recon + drift detection design — why we picked "detect but don't fix", and how saga / watchdog / drift detector layer the responsibilities. Posts 4 and 5 land when their evidence is ready: a Docker Mac NAT cap post once the O3.2 variant B bare-metal benchmark is in (Tier 1 → Tier 2 quantified), or the Tier 5a Pattern A section-level schema design as a standalone piece.

**The cache-truth contract + the two-layer sharding rule** are currently the two highest-portfolio-value architecture designs in this project — both straightforward, both aligned with industry, both backed by quantitative evidence.

---

*If anything here is wrong or missing, please open an issue or PR against [`docs/blog/2026-05-lua-single-thread-ceiling.md`](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-lua-single-thread-ceiling.md). The blog is in-repo precisely so corrections go through the same review process as code.*
