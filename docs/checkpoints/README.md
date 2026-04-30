# Project Review Checkpoints

Whole-project audit reports at defined cadence. Distinct from per-PR review (which checks a change); checkpoint review checks the **whole state** at a pause.

The full framework lives in [`.claude/skills/project-review-checkpoint/SKILL.md`](../../.claude/skills/project-review-checkpoint/SKILL.md). This index lists past reviews + outcomes so future reviewers can see what's been checked already.

## Checkpoint history

| Date | Trigger | Phase | PRs covered | Report | Outcome |
| :-- | :-- | :-- | :-- | :-- | :-- |
| _(none yet)_ | _Phase 2 boundary after A5_ | — | — | _scheduled_ | — |

## Triggers

- **Phase boundary** — after completing a roadmap Phase
- **Every 10 PRs** — drift / regression check
- **Pre-portfolio share** — defensibility audit before external review
- **Major refactor** — targeted to changed surface
- **Production incident** — reactive

## What each checkpoint covers

8 dimensions, each with a parallel reviewer:

1. Architecture coherence
2. Test surface health
3. Documentation drift
4. Operational maturity
5. Security posture
6. Performance regressions
7. Tech debt inventory
8. Senior-grade defensibility

## Adding a new checkpoint

1. Pick a date-stamped filename: `YYYYMMDD-<phase|prN>-review.md`
2. Copy `template.md` as your starting structure
3. Run the parallel-reviewer dispatch per the skill
4. Fill in the report
5. Update this README's history table
6. Triage findings into a "checkpoint cleanup" PR (Critical + Important only); defer Nice-to-have to backlog or next roadmap PR
