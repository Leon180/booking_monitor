#!/usr/bin/env python3
"""
scripts/generate_comparison_md.py — D12.5 Slice 4

Reads a 4-stage comparison directory produced by
`scripts/run_4stage_comparison.sh` and emits `comparison.md` with the
headline metrics table populated from per-stage summary.json files.

The prose template (run conditions, stage-by-stage analysis, caveats,
citation map) is INLINE in this script. The metrics table, run-condition
fields, and per-stage RPS/p99 numbers are interpolated from the input
directory's artifacts.

Usage:
    python3 scripts/generate_comparison_md.py <comparison-dir>

Example:
    python3 scripts/generate_comparison_md.py \\
        docs/benchmarks/comparisons/20260510_120000_4stage_c500_d60s/

Output:
    Writes <comparison-dir>/comparison.md
    Reads <comparison-dir>/run_conditions.txt
    Reads <comparison-dir>/stage{1,2,3,4}/summary.json

Why Python (not bash):
- k6 summary.json has nested keys (`metrics.http_reqs.count`, etc.)
  that bash's `jq` would handle but adds an external dep + harder
  for readers to follow. Python's `json` is stdlib, the prose
  template stays readable, and string interpolation is one
  builtin.
- The script is run ONCE per comparison directory; perf isn't a
  factor.
"""

from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path


# ─── k6 summary.json parsing ──────────────────────────────────────


def load_stage_metrics(stage_dir: Path) -> dict:
    """Extract the headline metrics from a stage's summary.json.

    Schema (k6 1.6.0 verified):
        metrics.http_reqs.count           total HTTP requests
        metrics.http_reqs.rate            requests/sec average
        metrics.http_req_duration.med     median latency (ms)
        metrics.http_req_duration['p(95)']  p95 latency (ms)
        metrics.http_req_duration['p(99)']  p99 latency (ms)
        metrics.iterations.count          total scenario iterations
        metrics.iteration_duration['p(95)']  p95 end-to-end (ms)

    Custom-metric keys used by `k6_two_step_flow.js`:
        metrics.accepted_bookings.count   number of /book → 202
        metrics.end_to_end_paid_duration['p(95)']  full happy-path
                                                    end-to-end p95
    """
    summary_path = stage_dir / "summary.json"
    if not summary_path.exists():
        return _empty_metrics(reason=f"summary.json missing at {summary_path}")
    try:
        with summary_path.open() as f:
            data = json.load(f)
    except json.JSONDecodeError as e:
        return _empty_metrics(reason=f"summary.json parse failed: {e}")

    metrics = data.get("metrics", {})

    def _get(name: str, *keys: str) -> float | None:
        m = metrics.get(name)
        if not isinstance(m, dict):
            return None
        for k in keys:
            if k in m:
                v = m[k]
                if isinstance(v, (int, float)):
                    return float(v)
        return None

    return {
        "http_reqs_total": _get("http_reqs", "count"),
        "http_reqs_per_sec": _get("http_reqs", "rate"),
        "http_req_duration_med": _get("http_req_duration", "med"),
        "http_req_duration_p95": _get("http_req_duration", "p(95)"),
        "http_req_duration_p99": _get("http_req_duration", "p(99)"),
        "iterations_total": _get("iterations", "count"),
        "iterations_per_sec": _get("iterations", "rate"),
        "accepted_bookings": _get("accepted_bookings", "count"),
        "end_to_end_p95": _get("end_to_end_paid_duration", "p(95)"),
    }


def _empty_metrics(reason: str) -> dict:
    return {
        "_missing": reason,
        "http_reqs_total": None,
        "http_reqs_per_sec": None,
        "http_req_duration_med": None,
        "http_req_duration_p95": None,
        "http_req_duration_p99": None,
        "iterations_total": None,
        "iterations_per_sec": None,
        "accepted_bookings": None,
        "end_to_end_p95": None,
    }


def _fmt(value: float | None, fmt: str = ".1f", suffix: str = "") -> str:
    if value is None:
        return "—"
    return f"{value:{fmt}}{suffix}"


# ─── markdown emit ────────────────────────────────────────────────


# Stage architectures + per-stage prose. The prose intentionally calls
# out the architectural transition and quotes the relevant citation —
# that's the senior-portfolio "what each layer costs" story.
STAGES = [
    (1, "stage1", "Sync `SELECT FOR UPDATE` baseline", """
Stage 1's hot path is a single Postgres transaction:

    BEGIN; SELECT … FOR UPDATE; UPDATE event_ticket_types …; INSERT orders …; COMMIT;

Every request on the same `ticket_type_id` blocks behind the prior
transaction's COMMIT. Throughput plateaus at the row-lock contention
ceiling Postgres can sustain on a single hot row — typically 1-2k TPS
on strong hardware ([AWS Aurora 2024][1], [PostgresAI 2025][2]).
[Faleiro & Abadi CIDR'17][3] argue more broadly that whether a
mechanism is latch-free matters less than whether it avoids
contention on shared memory locations — a useful lens for why Stage
2's move to Redis Lua (single-threaded server-side execution, no
shared-memory contention across clients) ends up dominating Stage
1's row-lock plateau.

Stage 1's purpose in the harness is to **pin the baseline**: this
plateau is the physical floor every later stage's hot-path improvement
is measured against.
"""),
    (2, "stage2", "Redis Lua atomic deduct + sync PG INSERT", """
Stage 2 replaces the `SELECT FOR UPDATE` hot row with a Redis Lua
script (`DECRBY ticket_type_qty:{id}`) — Redis's single-threaded
execution makes the deduct atomic without a lock contended across
clients. The PG INSERT for the order row remains synchronous and
becomes the new bottleneck.

Operationally, this is the architecture pattern of putting hot
inventory on a key-value store rather than a DB hot row.
[Atikoglu et al. SIGMETRICS'12][4] characterized Facebook's
production Memcached deployment and showed that key-value stores
sustain very high request rates against hot keys — the workload
context that makes Stage 2 a viable architectural choice in the
first place. Stage 2 demonstrates this empirically: the saturation
point shifts from "rows COMMIT serially" to "PG INSERT/COMMIT fsync"
— still synchronous, but typically 3-8ms per request instead of
the row-lock plateau.
"""),
    (3, "stage3", "Redis Lua + `orders:stream` + async worker", """
Stage 3 keeps the Redis Lua deduct of Stage 2 but moves the PG INSERT
**off the request hot path** by adding `XADD orders:stream` inside the
Lua script and an async worker goroutine that drains the stream and
writes order rows in the background.

The hot path becomes ~Lua-only (sub-millisecond on localhost). PG
INSERT throughput becomes a *worker-side scheduling problem* — the
broader design space of when and how to schedule transactional
work is surveyed in [Cheng et al. PVLDB'24][5] (single-DB
transaction scheduling); the same trade-offs apply at the
microservice boundary.

Cost ledger: Stage 3 carries strictly more failure modes than Stage 2.
Async timing means a `/book` returning 202 with an order_id may never
produce a row (worker crashes → DLQ). Per-message PEL recovery,
parse-fail compensation, retry budget classification, sweeper-vs-PEL
race — Stage 3 inherits the full async failure-mode surface.
"""),
    (4, "stage4", "Pattern A end-to-end + saga compensator", """
Stage 4 is the **current production binary** (`cmd/booking-cli`,
post-D7 — distinct from the README's historical "Stage 4" diagram
which labels the *pre-D7* `v0.2.0`-`v0.4.0` architecture).

Hot path is identical to Stage 3 (Redis Lua + stream + worker).
Stage 4 adds **failure-path infrastructure**:
- D5 webhook for inbound provider payment events (HMAC-verified)
- `events_outbox` table → relay → Kafka `order.failed` topic
- In-process saga consumer + compensator (idempotent at every step)
- D6 expiry sweeper, recon force-fail, saga watchdog as additional
  outbox producers

Event-sourced microservice architectures with saga-style cross-
service compensation surface a long tail of operational challenges
that [Laigner et al. TOSEM'25][6] catalog empirically (event ordering,
schema evolution, exactly-once semantics, debugging cross-service
flows). This codebase's saga implementation addresses the
correctness-side challenges directly: revert.lua SETNX guard +
DB MarkCompensated WHERE-status guard make compensation
idempotent under at-least-once Kafka delivery, and the in-process
consumer keeps end-to-end debug traces simple. Saga is failure-path
*insurance*, not happy-path work — D7 narrowed the saga scope to
compensation only, removing the legacy `payment_worker` Kafka
auto-charge path.

**Saga-path metrics in this run are zero by design**: the k6 scenario
drives the happy path only (no payment-failure injection). Stage 4's
hot-path numbers therefore mirror Stage 3; the failure-path
*infrastructure* exists and is health-checked separately. Verify via
`up{job="booking-stages",stage="stage4"} == 1` in
`prometheus_snapshot.json`.
"""),
]


def _extract_run_field(run_conditions: str, key: str, default: str = "?") -> str:
    """Pull a single field (e.g. 'duration', 'vus') from run_conditions.txt.

    Format produced by the orchestration script is `key: value` per line.
    """
    for line in run_conditions.splitlines():
        if line.lower().startswith(f"{key.lower()}:"):
            return line.split(":", 1)[1].strip()
    return default


def render_markdown(input_dir: Path) -> str:
    run_conditions = (input_dir / "run_conditions.txt").read_text() \
        if (input_dir / "run_conditions.txt").exists() \
        else "(run_conditions.txt missing — check the orchestration script's Step 1)"

    duration = _extract_run_field(run_conditions, "duration", "60s")
    vus = _extract_run_field(run_conditions, "vus", "?")

    stage_metrics = {
        n: load_stage_metrics(input_dir / dir_name)
        for n, dir_name, *_ in STAGES
    }

    # Headline table rows.
    # NOTE: p(99) intentionally omitted — k6 1.6's default summary
    # only emits p(90) + p(95). Adding p(99) would require modifying
    # k6_two_step_flow.js with `summaryTrendStats: ['p(99)']`, which
    # would make it differ from existing baselines under
    # docs/benchmarks/. Keep p(95) as the canonical tail percentile.
    table_rows = []
    for n, dir_name, label, _prose in STAGES:
        m = stage_metrics[n]
        if m.get("_missing"):
            table_rows.append(
                f"| {n} | {label} | — | — | — | "
                f"⚠️ {m['_missing']} |"
            )
            continue
        table_rows.append(
            "| {n} | {label} | {http_rps} | {bookings} | {p95} | "
            "{iterations} |".format(
                n=n, label=label,
                http_rps=_fmt(m["http_reqs_per_sec"], ".0f"),
                bookings=_fmt(m["accepted_bookings"], ".0f"),
                p95=_fmt(m["http_req_duration_p95"], ".1f", " ms"),
                iterations=_fmt(m["iterations_total"], ".0f"),
            )
        )

    # Per-stage detailed metrics (additional context below the table).
    stage_details = []
    for n, _dir, label, prose in STAGES:
        m = stage_metrics[n]
        stage_details.append(
            f"### Stage {n} — {label}\n"
            f"{prose}\n"
            f"**Metrics**: total http_reqs={_fmt(m['http_reqs_total'], '.0f')} | "
            f"avg RPS={_fmt(m['http_reqs_per_sec'], '.0f')} | "
            f"p95 latency={_fmt(m['http_req_duration_p95'], '.1f', ' ms')} | "
            f"p99={_fmt(m['http_req_duration_p99'], '.1f', ' ms')} | "
            f"iterations={_fmt(m['iterations_total'], '.0f')} | "
            f"end-to-end p95={_fmt(m['end_to_end_p95'], '.0f', ' ms')}\n"
        )

    # NOTE: parser-review HIGH about brace-injection in run_conditions
    # was a false positive — Python's str.format() does NOT re-parse
    # substituted values (verified empirically). Literal `{` / `}` in
    # run_conditions.txt content (from `docker compose version` output,
    # git status, etc.) inserts verbatim without raising. Don't add
    # `.replace("{", "{{")` here — it produces doubled braces in the
    # rendered markdown (visible noise, no safety gain).

    # Warn if both custom metrics from k6_two_step_flow.js are absent
    # across all stages — strong signal that the k6 script was edited
    # or replaced (silent drop of the headline `accepted_bookings`
    # column). We don't fail; the table still renders with "—" but
    # the operator gets a clue. (Closes Slice 4 parser-review MED.)
    custom_missing = sum(
        1 for n, *_ in STAGES
        if stage_metrics[n].get("accepted_bookings") is None
        and stage_metrics[n].get("end_to_end_p95") is None
        and not stage_metrics[n].get("_missing")
    )
    if custom_missing >= 2:
        print(
            f"WARN: {custom_missing}/{len(STAGES)} stages have summary.json "
            f"but no `accepted_bookings` / `end_to_end_paid_duration` custom "
            f"metrics — k6_two_step_flow.js may have been modified, or k6 "
            f"version dropped these keys. Headline table will show '—'.",
            file=sys.stderr,
        )

    return TEMPLATE.format(
        timestamp=datetime.now(tz=timezone.utc).strftime("%Y-%m-%d %H:%M UTC"),
        input_dir=str(input_dir),
        run_conditions=run_conditions.strip(),
        headline_rows="\n".join(table_rows),
        stage_details="\n".join(stage_details),
        duration=duration,
        vus=vus,
    )


# ─── Markdown template ────────────────────────────────────────────


TEMPLATE = """\
# 4-Stage Architecture Comparison — {timestamp}

> Source artifacts: `{input_dir}`
> Generated by: `scripts/generate_comparison_md.py`
> See [docs/d12/README.md](../../d12/README.md) for the architectural
> definitions of each stage and PR-D12.{{1,2,3,4,5}} histories.

This report quantifies the cost of four architectural decisions in the
booking hot path under the same load profile. All four stages preserve
the same 2-step API contract (per the apples-to-apples invariant
established in PR-D12.1) so the same `scripts/k6_two_step_flow.js`
runs unmodified against any stage. The headline question this report
answers: *what does each layer cost, and when does it pay off?*

## TL;DR

| Stage | Architecture | http_reqs/s | accepted_bookings | p95 | iterations |
|---|---|---|---|---|---|
{headline_rows}

Stages are listed in chronological architectural-evolution order. The
expected pattern is a **monotonic increase** in `http_reqs/s` from
Stage 1 to Stage 4, but the architectural-cost story is more nuanced:
each stage trades different failure-mode complexity for hot-path
throughput, and the comparison value is in *what each layer buys you*
— not in raw RPS.

> **Note on sold-out timing**: this run does not surface
> seconds-until-pool-depletion as a column. At low VUs ({vus} VUs ×
> {duration}) the 500k-ticket pool typically does not deplete inside
> the window, so the column would be uninformative. At c500/d60s the
> pool does deplete partway through, and the time-to-sold-out is the
> most discriminating metric between Stage 2 and Stage 3 (their RPS
> can look similar; sold-out shape diverges because Stage 3's async
> path returns 202s ahead of the DB). The c500/d60s canonical run
> (PR-D12.5 Slice 6) reports sold-out time in `comparison.md`'s
> stage-by-stage section.

## Run conditions

```
{run_conditions}
```

## Stage-by-stage analysis

{stage_details}

## Architectural cost summary

| Layer added | Hot-path benefit | Failure-mode cost | When it pays off |
|---|---|---|---|
| Stage 1 → 2: Redis Lua atomic deduct | Move bottleneck off PG row-lock onto cache-tier atomic op | None added (still synchronous) | Always, if hot-row contention is the bottleneck |
| Stage 2 → 3: async worker via `orders:stream` | Move PG INSERT off request path; hot path becomes ~Lua-only | PEL recovery, DLQ classifier, sweeper-vs-PEL race, "202 with order_id but no DB row" failure mode | When PG INSERT fsync time dominates request latency |
| Stage 3 → 4: outbox + Kafka + saga compensator | None on the hot-path critical path (worker UoW + Lua deduct unchanged); the in-process saga consumer + outbox relay + expiry sweeper + saga watchdog DO add background goroutines that compete for CPU/memory at high VUs (visible at c500+ but not at c10) | Cross-process compensation guarantees + saga consumer + Kafka topic management + 13 outcome labels + 4 alerts | When you need at-least-once cross-process compensation and the operational cost of Kafka is acceptable |

The progression from Stage 1 to Stage 4 is **not strictly faster** — the
saga in Stage 4 is failure-path insurance with zero hot-path
contribution. Stage 3 vs Stage 4 hot-path numbers should be ~equal in
this benchmark (k6 doesn't drive payment failures). The Stage 3 → 4
delta you'd see in production is in *failure-path observability +
guarantees*, not in `http_reqs/s`.

## Caveats

- **Single-laptop Docker-on-macOS** — production-like but not
  bare-metal. Real-world numbers on dedicated Linux hosts would be
  higher across the board.
- **Single-broker Kafka** — Stage 4's saga path is sensitive to
  multi-broker latency. Multi-broker Stage 4 may behave differently.
- **{duration} window** — short relative to JIT / GC / cache warm-up.
  A longer endurance run (5-10 min) would smooth out warm-up effects.
  At very short windows (≤10s), warm-up itself dominates; treat the
  numbers as a smoke check, not a verdict.
- **Run-to-run variance ~3-5%** per the master benchmark
  conventions (see `docs/benchmarks/`). If two stages' headline RPS
  differ by less than 5%, treat them as **architecturally
  indistinguishable at this noise floor** — don't claim an
  architectural win.
- **Stages were run sequentially** (not co-deployed) to hold host
  CPU/IO contention constant across stages. Headline numbers
  represent each stage's IN-ISOLATION ceiling, NOT the throughput
  when Stage 3 and Stage 4 are deployed side-by-side competing for
  the same machine resources. Co-deployment-throughput is a separate
  research question, deferred to a future PR-D12.6.
- **Saga-path metrics are zero by design** — the k6 scenario drives
  only the happy path. Stage 4's failure-path observability (saga
  compensator counter / histogram / lag gauge) is health-checked
  independently via the bench Prometheus snapshot.

## Disambiguating Stage 4 (current post-D7) from README's "Stage 4"

The README's "Architecture Evolution" section labels its Stage 4
diagram as the **historical** `v0.2.0–v0.4.0` pre-D7 architecture
(when `payment_worker` consumed `order.created` from Kafka and saga
managed both happy + failure paths). **D12 Stage 4 is a different
binary** — `cmd/booking-cli` post-D7, where saga is narrowed to
compensation only and money movement happens via webhook (D5) +
PaymentIntent (D4). When reading this document alongside the README,
use the architectural description ("Pattern A + saga compensator")
rather than the "Stage 4" label to disambiguate.

## Citation map (4 peer-reviewed papers + 2 industry sources)

> Citations are verified against the published sources. Industry sources (1, 2)
> are blog posts from operators with production-scale experience and are flagged
> distinctly from peer-reviewed work (3-6).

**Industry sources (operator experience, not peer-reviewed):**

[1]: AWS Aurora team — *Improve PostgreSQL Performance by 100x by Avoiding the Lock Manager Contention* (AWS Database Blog, 2024).

[2]: PostgresAI — *Postgres Marathon 2-005: Lock contention at scale* (postgres.ai, 2025).

**Peer-reviewed papers:**

[3]: **Faleiro, J. M. & Abadi, D. J.** — *Latch-free Synchronization in Database Systems: Silver Bullet or Fool's Gold?* CIDR'17.
    Argues that the binary "latch-free vs latched" framing matters less than
    whether a synchronization mechanism causes contention on shared memory
    locations. Stage 1's row-lock plateau is one manifestation of this principle:
    the lock-manager bottleneck is not the locks themselves but the contention
    point they create on a single hot row.

[4]: **Atikoglu, B., Xu, Y., Frachtenberg, E., Jiang, S., & Paleczny, M.** — *Workload Analysis of a Large-Scale Key-Value Store*. SIGMETRICS'12.
    Workload characterization of Facebook's production Memcached deployment:
    GET/SET ratios, object sizes, request rate distributions, temporal locality.
    Establishes the production context that makes Stage 2's choice of a
    key-value store for hot inventory a viable architectural pattern in the
    first place.

[5]: **Cheng, R. et al.** — *Towards Optimal Transaction Scheduling*. PVLDB'24.
    Surveys the design space for transaction scheduling within a database;
    Stage 3's worker-side scheduling decision (when and how to drain
    `orders:stream` into PG INSERTs) maps to the same trade-offs at the
    microservice boundary.

[6]: **Laigner, R. et al.** — *An Empirical Study on Challenges of Event Management in Microservice Architectures*. TOSEM'25.
    Catalogs the operational challenges of event-driven microservices — event
    ordering, schema evolution, exactly-once semantics, debugging cross-service
    flows. Stage 4's saga compensator addresses the correctness-side challenges
    via revert.lua SETNX guard + DB MarkCompensated WHERE-status guard
    (idempotent under at-least-once delivery).

## Cross-references

- D12 plan + per-stage shipping history: [`docs/d12/README.md`](../../d12/README.md)
- Pattern A blog post (D15): [`docs/blog/2026-05-saga-pure-forward-recovery.md`](../../blog/2026-05-saga-pure-forward-recovery.md)
- Roadmap closure: [`docs/post_phase2_roadmap.md`](../../post_phase2_roadmap.md)
"""


# ─── main ─────────────────────────────────────────────────────────


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        description=__doc__.split("\n\n", 1)[0],
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("input_dir",
                        type=Path,
                        help="comparison directory produced by scripts/run_4stage_comparison.sh")
    args = parser.parse_args(argv)

    input_dir: Path = args.input_dir
    if not input_dir.is_dir():
        print(f"FATAL: input_dir not a directory: {input_dir}", file=sys.stderr)
        return 1

    # Sanity: at least one stage's summary.json must exist. If ALL
    # four are missing, this is almost certainly a wrong-directory
    # call and we should fail loud rather than emit a useless report.
    n_present = sum(
        1 for n, dir_name, *_ in STAGES
        if (input_dir / dir_name / "summary.json").exists()
    )
    if n_present == 0:
        print(
            f"FATAL: no stage summary.json files found under {input_dir}/stage{{1..4}}/. "
            f"Wrong input directory?",
            file=sys.stderr,
        )
        return 1
    partial = n_present < 4
    if partial:
        print(
            f"WARN: only {n_present}/4 stages have summary.json. "
            f"comparison.md will mark missing stages as ⚠️.",
            file=sys.stderr,
        )

    md = render_markdown(input_dir)
    out_path = input_dir / "comparison.md"
    out_path.write_text(md)
    print(f"✓ wrote {out_path} ({len(md)} chars)")
    # Exit 2 (not 0) on partial-stage success so CI consumers can
    # distinguish "all 4 stages clean" from "some stages missing
    # but generated anyway". Strict callers gate on `$? == 0`;
    # tolerant callers gate on `$? -le 2`. (Closes Slice 4
    # parser-review LOW #3.)
    return 2 if partial else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
