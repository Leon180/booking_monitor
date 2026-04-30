# Checkpoint Review: <date> — <trigger label>

> Copy this template to `YYYYMMDD-<phase|prN>-review.md` and fill in. The full procedure lives in [`.claude/skills/project-review-checkpoint/SKILL.md`](../../.claude/skills/project-review-checkpoint/SKILL.md).

## Pre-check snapshot

- **Trigger**: <phase boundary / every-10-PRs / pre-share / refactor / incident>
- **PRs since last checkpoint**: <count + range #N..#M>
- **Test coverage**: <package-level summary or full table>
- **Lint state**: <golangci-lint output, vet, govulncheck>
- **Stack health**: <docker compose ps; livez/readyz status>
- **Reviewers dispatched**: <list of agents launched in parallel>

## Findings by dimension

### 1. Architecture coherence

**Critical**:
**Important**:
**Nice-to-have**:

### 2. Test surface health

**Critical**:
**Important**:
**Nice-to-have**:

### 3. Documentation drift

**Critical**:
**Important**:
**Nice-to-have**:

### 4. Operational maturity

**Critical**:
**Important**:
**Nice-to-have**:

### 5. Security posture

**Critical**:
**Important**:
**Nice-to-have**:

### 6. Performance regressions

**Critical**:
**Important**:
**Nice-to-have**:

### 7. Tech debt inventory

**Critical**:
**Important**:
**Nice-to-have**:

### 8. Senior-grade defensibility

> What would an interviewer challenge? Sorted by likelihood-of-being-asked. Be honest.

## Action plan

| # | Finding | Severity | Estimated effort | Target PR |
| :-- | :-- | :-- | :-- | :-- |
| 1 | ... | Critical | S/M/L | cleanup PR |

## Defensibility self-critique summary

<3-5 sentences on the strongest portfolio narrative + the weakest spots an interviewer would probe>

## Outcome

<after the checkpoint cleanup PR ships, link to it here and note resolution>
