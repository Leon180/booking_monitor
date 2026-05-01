---
name: project-review-checkpoint
description: Whole-project audit at defined cadence (phase boundaries, every-N-PRs, pre-portfolio-share). 8 dimensions, parallel multi-agent execution, durable report artifact under docs/checkpoints/. Activates when user asks for "project review", "checkpoint", or signals a phase transition.
origin: booking_monitor
---

# Project Review Checkpoint

A repeatable framework for whole-project audit, distinct from per-PR review. Per-PR review checks the change; checkpoint review checks the **whole** state at the moment of pause.

## Dimension 0 — Necessity validation (BEFORE planning a PR)

This dimension runs **before** the other 8, at PR-planning time, NOT at checkpoint time. Skipping it produces "code shipped because it was on the roadmap" rather than "code shipped because production needed it." A5 (saga watchdog) merged with this gap retroactively visible — the PR was correct but planning didn't validate necessity. Future PRs use this checklist.

Before any non-trivial PR:

| # | Question | If "no" / "unsure", do this BEFORE writing code |
| :-- | :-- | :-- |
| 1 | Is the gap this PR closes **observable in production today**? (metric value, log line, incident) | Add the metric / alert that WOULD detect it. Defer code until the signal fires. |
| 2 | What's the **simplest version** that would close the same gap? (alert-only, doc-only, smaller scope) | Ship the simpler version first. Re-evaluate after it lands. |
| 3 | Is there a **planned future PR** that would close the same gap? | Distinguish: complementary (both worth shipping), duplicative (drop one), or "this is the bridge until that lands" (ship with explicit sunset criteria). |

Recording the answers in the PR body forces honest framing. Examples:

- "Production shows `dlq_messages_total{topic="order.failed.dlq"} > 0` for 24h → real bleeding → ship full fix."
- "No production data shows this gap. Adding alert + 2-week observation period before writing handler code."
- "DLQ worker is on the roadmap. This watchdog is the defense-in-depth layer; will mostly produce `already_compensated` once DLQ worker ships."

## When to Activate (post-PR audit)

Trigger a checkpoint when ANY of these is true:

| Trigger | Cadence | Scope |
| :-- | :-- | :-- |
| **Phase boundary** | After completing a roadmap Phase | All 8 dimensions, deep |
| **Every N PRs** (default N=10) | Every ~2-3 weeks at typical pace | Mid-depth; focus on regression / drift |
| **Pre-portfolio share** | Before sharing externally (interview, demo) | Defensibility audit emphasis |
| **Major refactor merged** | Same response | Targeted to refactored surface + downstream |
| **Production incident** | Reactive — same week as incident | Surface + adjacent code paths |

Skip checkpoints between routine feature PRs unless 10+ have accumulated. Per-PR review covers those.

## The 8 Audit Dimensions

Each dimension has a specific reviewer agent type and a defined deliverable. ALL run in parallel during a checkpoint.

| # | Dimension | Reviewer | Deliverable |
| :-- | :-- | :-- | :-- |
| 1 | **Architecture coherence** | code-reviewer (architecture-focused prompt) | Layer-violation report; "smelly" pattern callouts; refactor candidates |
| 2 | **Test surface health** | code-reviewer (test-focused prompt) | Pyramid analysis (unit/integration/e2e ratios); coverage gaps; flaky test detection |
| 3 | **Documentation drift** | code-reviewer (docs-focused prompt) | Bilingual EN/zh-TW parity check; skills relevance; memory currency; doc-vs-code accuracy |
| 4 | **Operational maturity** | code-reviewer (ops-focused prompt) | Probe coverage; metric inventory completeness; alert/runbook coverage matrix |
| 5 | **Security posture** | code-reviewer (security-review skill is the one-shot equivalent) | Threat model; auth gaps; secret handling; input-validation gaps; CVE check |
| 6 | **Performance regressions** | code-reviewer + benchmark run | k6 baseline vs current; pprof samples; allocation hot spots; throughput delta |
| 7 | **Tech debt inventory** | Explore + grep | TODO / FIXME / XXX scan; deferred-items reconciliation against `architectural_backlog.md`; unused code |
| 8 | **Senior-grade defensibility** | code-reviewer (portfolio-critic prompt) | "What would an interviewer challenge?" — hidden weaknesses, cut corners, missing pieces |

## Execution Procedure

### Step 1: gather pre-check inventory

Before dispatching agents, capture the current state for the report:

```bash
# Snapshot the state
git log --oneline main..HEAD                 # PRs since last checkpoint (or since main)
git log --oneline -20 main                   # recent merges
go test -cover ./... 2>&1 | grep -E "ok|coverage:"   # coverage snapshot
golangci-lint run ./... 2>&1 | tail -5       # lint cleanliness
docker compose ps --format json | jq -r '.[] | "\(.Names): \(.Status)"'   # stack health
```

### Step 2: dispatch all 8 reviewers in parallel

Send one Task tool message with 8 Agent invocations. Each agent gets a self-contained prompt with:
- Specific dimension to audit
- Repo path + branch reference
- Output format constraint ("under 1500 words; cite file:line; structure: Critical / Important / Nice-to-have")
- Anti-overlap directive ("co-reviewers cover [other dimensions] in parallel — focus on YOUR dimension")

### Step 3: synthesize into a checkpoint report

Output goes to `docs/checkpoints/YYYYMMDD-<phase|prN>-review.md`. Standard sections:

```markdown
# Checkpoint Review: <date> — <trigger label>

## Pre-check snapshot
- PRs since last checkpoint: ...
- Test coverage: ...
- Lint state: ...
- Stack health: ...

## Findings by dimension

### 1. Architecture coherence
[summary + critical/important findings]

### 2. Test surface health
...

(repeat for all 8)

## Action plan

| # | Finding | Severity | Estimated effort | Target PR |
| :-- | :-- | :-- | :-- | :-- |
...

## Defensibility self-critique

[The portfolio-defensibility findings — sorted to be the "what would an interviewer challenge" list]
```

### Step 4: triage into action items

After the report, propose ONE focused "checkpoint cleanup" PR that addresses the **Critical + Important** findings. Defer Nice-to-have findings either:
- Into the next regular roadmap PR's scope when adjacent
- Into a separate small PR if standalone
- Into `architectural_backlog.md` if not actionable now

Do NOT bundle dozens of unrelated fixes into one mega-PR. The cleanup PR should be reviewable.

## Checkpoint History

The history log lives at `docs/checkpoints/README.md` — index of past reviews with dates, triggers, and a one-line summary of outcomes. Update it as part of each checkpoint.

## Anti-patterns

- **Checkpoint before features land** — produces stale findings. Wait for the trigger.
- **Skip the parallel dispatch** — sequential agents take 8× longer with no quality gain.
- **Bundle Nice-to-have into the cleanup PR** — review fatigue; defer them.
- **No deliverable file** — checkpoints that don't leave a durable artifact are forgotten; future you can't see "what we already checked".
- **Same scope every time** — phase-boundary checkpoint should be deeper than every-N-PRs. Match scope to trigger.
- **Confuse with per-PR review** — per-PR review checks a change; checkpoint reviews the whole state at a pause.

## Reference: dimension prompts

When dispatching, each agent gets a focused prompt. Below is the canonical shape for each dimension; copy + customise per checkpoint.

### Dimension 1 — Architecture coherence
> "Audit `<repo path>` for DDD layer respect (domain/application/infrastructure boundaries), circular imports, leaky abstractions (e.g., infrastructure types appearing in domain interfaces), and god-types or other smelly patterns. Co-reviewers handle test/docs/ops/security/perf/debt/defensibility — focus on architecture only. Output: layer-violation report, refactor candidates with file:line refs, under 1500 words."

### Dimension 2 — Test surface health
> "Audit `<repo path>` test surface: unit/integration/e2e ratios via `find . -name '*_test.go' | xargs grep -l <build-tag-marker>`; identify packages with <60% coverage; flag flaky tests (search for `t.Skip`, time-dependent assertions, race-condition smells). Output: pyramid analysis, gap list, under 1500 words."

### Dimension 3 — Documentation drift
> "Audit `<repo path>` docs: EN/zh-TW bilingual parity (PROJECT_SPEC, README, monitoring, AGENTS.md), skills under `.Codex/skills/`, memory under `.Codex/projects/.../memory/`. Check structural identity, identify stale references, verify code↔doc accuracy on at least 5 sampled claims. Output: drift list with severity, under 1500 words."

### Dimension 4 — Operational maturity
> "Audit `<repo path>` operational surface: probe coverage (`/livez`, `/readyz`); metric inventory completeness; alert + runbook coverage matrix (every alert has a runbook? every metric has a dashboard?); SLO definitions present? Output: ops gap matrix, under 1500 words."

### Dimension 5 — Security posture
> "Audit `<repo path>`: auth gaps; input validation at boundaries; secret handling; SQL injection / XSS / CSRF surface; dependency CVEs via `govulncheck ./...`. Output: threat model + finding list (Critical / Important / Low), under 1500 words. Use the `security-review` skill if available for one-shot dispatch."

### Dimension 6 — Performance regressions
> "Audit `<repo path>` performance: run `make benchmark-compare` against the prior baseline in `docs/benchmarks/`; analyse pprof if available; identify allocation hot spots in `BookTicket` / outbox relay / saga consumer. Output: regression delta vs baseline, under 1500 words. Note: if no prior baseline exists, capture a baseline NOW."

### Dimension 7 — Tech debt inventory
> "Audit `<repo path>` debt: scan for TODO / FIXME / XXX / HACK comments; cross-reference deferred items in `architectural_backlog.md`; identify unused exports via `staticcheck`. Output: prioritized debt list, under 1500 words."

### Dimension 8 — Senior-grade defensibility
> "Audit `<repo path>` from an interviewer's perspective: assume this is presented as a senior-engineer portfolio piece. What would a senior interviewer challenge? Surface hidden weaknesses (cut corners, missing pieces, defensibility gaps in architecture decisions). Output: critique list sorted by likelihood-of-being-asked, under 1500 words. Be honest — this is the most valuable dimension for portfolio prep."

## Deliverable: checkpoint report template

See `docs/checkpoints/template.md` for the canonical structure. Each checkpoint copies that template and fills it in.
