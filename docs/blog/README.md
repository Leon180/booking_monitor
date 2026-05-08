# Engineering Blog Series

> 中文是主要語言、英文是輔助 — see [Bilingual policy](#bilingual-policy) below.

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

## Bilingual policy

Different from the [bilingual contract](../../.claude/CLAUDE.md) that covers AGENTS / CLAUDE / README / PROJECT_SPEC / monitoring (where the EN file is the default and the zh-TW is a paired translation), **the blog series is Taiwan-portfolio focused — zh-TW is primary, English is a companion translation**.

**File naming convention:**
- `<date>-<slug>.zh-TW.md` — the primary, canonical version. New content is drafted here first.
- `<date>-<slug>.md` — full English translation kept in sync. Posts at top of each file cross-link to the other version.

**What this means in practice:**
- The post index below lists the **zh-TW link first**.
- CHANGELOG / GitHub Releases / external citations link to the zh-TW version by default.
- Both versions stay full-length translations of each other (not "primary + summary"). If a post has new content, draft it in zh-TW first, then translate to EN before merging.
- Code blocks / commands / file paths / mermaid syntax stay English in both versions (same rule as the other bilingual files).

**Why zh-TW primary for the blog (vs the other bilingual files where EN is default):**
- The 5 paired contract files (AGENTS / CLAUDE / README / PROJECT_SPEC / monitoring) target the broader open-source / FAANG audience where English is the lingua franca.
- The blog explicitly serves Taiwan-local interview portfolio purposes (KKTIX / Pinkoi / 91APP / 街口 audiences). For that reader, zh-TW being primary is more appropriate. EN companion is kept so non-Chinese-reading reviewers (FAANG screens, recruiters) can still read the post.

## Posts

| Post (zh-TW primary) | English companion | Status | Topic | Pairs with |
| :-- | :-- | :-- | :-- | :-- |
| [2026-05-cache-truth-architecture.zh-TW.md](2026-05-cache-truth-architecture.zh-TW.md) | [EN](2026-05-cache-truth-architecture.md) | published | Why Redis is ephemeral, not durable. The FLUSHALL incident, the 411/1000 silent message loss, and the 5-PR closure. | [v0.4.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0) |
| [2026-05-lua-single-thread-ceiling.zh-TW.md](2026-05-lua-single-thread-ceiling.zh-TW.md) | [EN](2026-05-lua-single-thread-ceiling.md) | published | The Lua single-thread ceiling at 8,330 acc/s. Why Redis CPU isn't the problem; why the next 10× requires two-layer sharding (section-level + hot-section quota router); why generic hash sharding is the wrong frame. | [VU stress benchmark](../benchmarks/), [saturation profile](../saturation-profile/) |
| [2026-05-detect-but-dont-fix.zh-TW.md](2026-05-detect-but-dont-fix.zh-TW.md) | [EN](2026-05-detect-but-dont-fix.md) | published | Detect-but-don't-fix multi-layer safety. Why saga / watchdog / recon / drift detector are four non-overlapping layers; why none auto-corrects; the design rule for non-overlapping responsibility. | [PR #45](https://github.com/Leon180/booking_monitor/pull/45), [PR #49](https://github.com/Leon180/booking_monitor/pull/49), [PR #76](https://github.com/Leon180/booking_monitor/pull/76) |
| [2026-05-saga-pure-forward-recovery.zh-TW.md](2026-05-saga-pure-forward-recovery.zh-TW.md) | [EN](2026-05-saga-pure-forward-recovery.md) | published | Saga shouldn't manage the happy path. D7 narrowing as Garcia-Molina 1987 §5 forward-recovery's engineering implementation; race semantics between D6 expiry sweeper and D5 webhook; idempotency-key vs payment-success distinction. | [PR #98](https://github.com/Leon180/booking_monitor/pull/98), [v0.6.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.6.0), [foundations notes](notes/2026-05-pattern-a-foundations.zh-TW.md) |

### Planned (per [Phase 3 roadmap D15](../post_phase2_roadmap.md))

- Why Docker Desktop on Mac caps your benchmark at ~80k req/s — write AFTER O3.2 variant B has actual data

Pace target: 3 posts before Phase 3 finishes (met as of `2026-05-03`); subsequent posts land as portfolio additions. The Pattern A post above is the first post-roadmap addition.

## Reading order recommendation (for someone landing fresh on the repo)

1. [README.md](../../README.md) — entry point, including the [Architecture Evolution](../../README.md#architecture-evolution) mermaid diagrams
2. [GitHub Releases page](https://github.com/Leon180/booking_monitor/releases) — milestone timeline with v0.1.0 → v0.4.0
3. **This blog series** — drill into the architectural decisions
4. [`docs/PROJECT_SPEC.md`](../PROJECT_SPEC.md) — full operational spec
5. Specific PRs referenced in the posts — for the actual diff
