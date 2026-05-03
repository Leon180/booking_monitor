# Engineering Blog Series

In-repo `docs/blog/` series documenting the architectural decisions made during this project, written for senior-engineer interview audiences. Each post covers one decision that's worth a 10-minute conversation in a system-design or architectural-review interview.

## Why in-repo, not external

- Posts can reference specific commit SHAs, PR numbers, and benchmark archive paths without context-switching out of GitHub
- GitHub renders mermaid + code blocks natively; no separate site to maintain
- The blog stays version-controlled with the code it describes — when an architecture decision is later revisited or revised, the blog update lands in the same commit as the code change

External blog platforms (Medium / Hashnode / personal sites) are a deferred / not-planned path. See [`docs/post_phase2_roadmap.md`](../post_phase2_roadmap.md) D15.

## Template — hybrid-STAR

Each post uses this structure (adapted from STAR — Situation / Task / Action / Result — but expanded for engineering writing where benchmark tables, code excerpts, and citations need more room than STAR's "Result" cell allows).

```
# <Title> — concrete technical decision
                e.g. "Why Redis is ephemeral, not durable"

## Context
   Situation + Task merged. What problem triggered this decision.
   Quantified if possible ("benchmark showed 411-of-1000 messages
   silently dropped").

## Options Considered
   2-4 alternatives. Each gets 1-2 sentences of trade-off — enough
   to show you saw the option, not so much that the post becomes
   a survey paper.

## Decision
   The chosen option + why. Reference the specific PR / commit
   SHAs that implemented it.

## Result
   Quantified outcomes (benchmark numbers, p99 changes, CPU
   utilisation), second-order effects (what got harder /
   easier downstream), industry citations that confirm or
   contradict the chosen path.

## Lessons / What I'd do differently
   Honest hindsight. Senior interviews weight this section
   heavily — it's the proxy for "can this person learn from
   their work, or do they just defend it?"
```

## Why hybrid-STAR vs pure STAR or free-form

- **Pure STAR** (interview-prep convention): too compressed for engineering writing. The "Result" box can't fit a benchmark table + a citation list + a follow-up trade-off discussion. Posts written in pure STAR feel mechanical and lose the technical depth that the audience is here for.
- **Free-form** (typical engineering blog): no shape, easy to ramble, hard to revisit. The reader doesn't know whether they're in motivation, decision, or aftermath.
- **Hybrid-STAR** (this template): keeps STAR's interview-answer skeleton (so the reader can mentally pre-format an interview answer while reading) AND leaves enough room in each section for the technical detail the post needs.

## Posts

| Post | Status | Topic | Pairs with |
| :-- | :-- | :-- | :-- |
| [2026-05-cache-truth-architecture.md](2026-05-cache-truth-architecture.md) | published | Why Redis is ephemeral, not durable. The FLUSHALL incident, the 411/1000 silent message loss, and the 5-PR closure. | [v0.4.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0) |

### Planned (per [Phase 3 roadmap D15](../post_phase2_roadmap.md))

- The Lua single-thread ceiling: 8,330 acc/s and how to think about the next 10× — pairs with [`docs/benchmarks/20260502_132335_compare_c500_vu_scaling/`](../benchmarks/) and the 1M QPS analysis
- Recon + drift detection: building the safety net after the silent-loss incident — focuses on the operational shape (recovery story, runbook design)
- Why Docker Desktop on Mac caps your benchmark at ~80k req/s — write AFTER O3.2 variant B has actual data
- Saga compensation in production-shape Go: outbox + watchdog + idempotency

Pace target: write 3 posts before Phase 3 finishes; the rest land incrementally as portfolio additions.

## Reading order recommendation (for someone landing fresh on the repo)

1. [README.md](../../README.md) — entry point, including the [Architecture Evolution](../../README.md#architecture-evolution) mermaid diagrams
2. [GitHub Releases page](https://github.com/Leon180/booking_monitor/releases) — milestone timeline with v0.1.0 → v0.4.0
3. **This blog series** — drill into the architectural decisions
4. [`docs/PROJECT_SPEC.md`](../PROJECT_SPEC.md) — full operational spec
5. Specific PRs referenced in the posts — for the actual diff
