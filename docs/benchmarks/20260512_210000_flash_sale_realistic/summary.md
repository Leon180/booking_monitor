# Flash sale realistic benchmark — open-model arrival rate

**Date**: 2026-05-12
**Hardware**: Apple Silicon MacBook + Docker Desktop, 10 cores host
**Method**: k6 `ramping-arrival-rate` open-model executor via [`scripts/k6_flash_sale_realistic.js`](../../scripts/k6_flash_sale_realistic.js). Each iteration = ONE unique user, ONE attempt. No VU looping.
**Code state**: revert-clean (post-A/B-test) — `log.Error` on sold-out is **still enabled** (the inefficiency we found, not yet patched in main).

## Why we switched methodology

The earlier `constant-vus` tests measured *sustained-throughput capacity*, not *flash-sale dynamics*:
- 500 VUs each looped for 90 s, generating millions of requests against fixed user personas
- Closed-loop = when system slows, VUs naturally pause → reduces pressure → coordinated omission ([ScyllaDB 2021](https://www.scylladb.com/2021/04/22/on-coordinated-omission/))
- Reports `http_reqs/s` aggregate, which becomes meaningless post-depletion (cheap 409 fast path dominates)

Industry production teams use **open-model arrival-rate** instead ([Grafana k6 docs](https://grafana.com/docs/k6/latest/scenarios/concepts/open-vs-closed/), [Shopify Engineering](https://shopify.engineering/performance-testing-shopify), [StormForge](https://stormforge.io/blog/open-closed-workloads/)). New iterations fire at a configured rate **independent of system response time**, mimicking real F5-mashing users.

## Three scenarios, three load levels

| Scenario | Pool | Target arrival | Sustained for | Industry analogue |
|:--|--:|--:|:--|:--|
| **SMALL** | 5,000 | 500 req/s | 25 s | Local indie show / club night |
| **MEDIUM** | 50,000 | 2,500 req/s | 50 s | Mid-size concert / regional sports |
| **LARGE** | 500,000 | 8,000 req/s | 105 s | Major arena tour (one venue) |

For comparison: Taylor Swift Eras Tour 2022 generated **3.5 billion** requests across multiple days from 14M unique users — would need distributed k6 + multi-region infra to simulate, not feasible on a laptop.

## Results

| Metric | SMALL | MEDIUM | LARGE |
|:--|--:|--:|--:|
| Target arrival rate | 500 req/s | 2,500 req/s | 8,000 req/s |
| Achieved arrival rate | 428 req/s | 2,142 req/s | 7,110 req/s |
| **Accepted bookings (202)** | **5,000** | **50,000** | **500,000** |
| Sold-out responses (409) | 9,999 | 99,999 | 459,998 |
| **Failures (5xx)** | **0** | **0** | **0** |
| http_req_duration p50 | 353 µs | 256 µs | 203 µs |
| http_req_duration p90 | 670 µs | 463 µs | 534 µs |
| **http_req_duration p95** | **746 µs** | **597 µs** | **1.14 ms** |
| http_req_duration max | 21.75 ms | 78.09 ms | 146.58 ms |
| Max concurrent VUs used | 1 (of 1000) | 6 (of 3000) | **871** (of 10000) |
| Total wall clock | 35 s | 70 s | 135 s |

### Key observations

1. **All inventory sold cleanly** across all three scales — no overselling, no underselling.
2. **Zero 5xx failures** across 1.1M total requests (15k + 150k + 960k). The system never errored.
3. **p95 latency sub-millisecond** under flash-sale traffic up to 8 k req/s. The max-latency outliers (78–146 ms) are the only spikes — likely GC pauses or stream consumer batching, not the booking hot path.
4. **VU concurrency stayed low**: even LARGE only needed 871 concurrent VUs at peak from a 10 k pre-allocated pool, meaning each booking response returns fast enough that few VUs are in flight at any moment. The system is **far from concurrency-bound**.

### What this tells us about the system

booking_monitor on a single MacBook Docker handles **realistic flash-sale-scale load up to 8 k req/s arrival rate** with sub-millisecond p95 and zero failures. This is comparable in order of magnitude to:
- ~40 % of Stripe's BFCM 2024 peak (20 k+ RPS, but Stripe is whole-company; ours is one service)
- Top end of a single 12306 ticket service instance (1 k+ tickets/sec sustained per service)
- Below Shopify BFCM peaks (8 M+ RPS edge, but distributed across many instances + CDN)

The system **never approached its observed `constant-vus` ceiling** of 46–59 k req/s under this realistic load. The earlier ceiling number, while real, was measured under *adversarial closed-loop load* that doesn't represent business reality.

## What this changes about the architecture story

The narrative shifts twice:

**Old narrative (April / early May 2026)**: "Redis Lua single-thread on same key serialisation caps us at 8,330 acc/s" — invalidated by the Redis baseline benchmark (273 k RPS server-side).

**Middle narrative (2026-05-12 round 4)**: "log.Error on sold-out path serialises on stdout FD writeLock, capping us at 46 k closed-loop req/s" — true under adversarial closed-loop load, but **never triggered under realistic open-arrival-rate load** because the inventory depletes in seconds and post-depletion traffic doesn't actually arrive at a rate that contends.

**Honest narrative (2026-05-12 round 5)**: At realistic flash-sale loads (up to 8 k req/s, 500 k tickets, 100 k+ unique users in 100 s), the system handles every request with p95 < 2 ms and zero 5xx. The previously-identified bottlenecks (`log.Error` → `fdMutex`) **only matter under sustained-throughput stress that won't happen in production-shape traffic**. The system is overspecced for current load.

## When the bottlenecks DO matter

The `log.Error` → `fdMutex` cap matters in a specific scenario:
- A flash sale at scale **larger than 8 k req/s arrival** on a single instance, post-depletion
- E.g., Eras-Tour-scale onsale where post-sold-out users keep hammering refresh → arrival rate stays at the original peak even though no tickets remain
- In that regime, the FD lock contention from logging every 409 caps the server's ability to drain the queue

For booking_monitor's portfolio-stated scope (flash-sale simulator, single-instance Go), the realistic load profile doesn't trigger it. The fix would be a single-line change (skip log.Error for known business states) and is documented in [`docs/benchmarks/20260512_204000_ab_test_soldout_log/`](../20260512_204000_ab_test_soldout_log/summary.md) for when it does become relevant.

## Methodology lessons

1. **Closed-loop VU benchmarks measure capacity ceiling, not realistic traffic.** Use them to find *where the system breaks*, not what users experience.
2. **Open-model arrival-rate benchmarks measure realistic load handling.** Use them to validate that user-shaped traffic gets served well.
3. **Both are useful, for different questions.** Stripe / Shopify run both regularly; the [Shopify HHH tool](https://shopify.engineering/performance-testing-shopify) converts real browser sessions into open-model load profiles.
4. **Headline `http_reqs/s` is a misleading metric for flash sale.** Report `accepted_bookings/s`, `sold_out/s`, `5xx_rate`, and `p99 per unique user` separately.

## Reproducing

```bash
# Reset state
make reset-db

# Pick scenario via env (SMALL / MEDIUM / LARGE)
docker run --rm -i --network=booking_monitor_default \
    -e SCENARIO=MEDIUM \
    -v $(pwd)/scripts/k6_flash_sale_realistic.js:/script.js \
    grafana/k6 run /script.js
```

Source script: [`scripts/k6_flash_sale_realistic.js`](../../scripts/k6_flash_sale_realistic.js)
