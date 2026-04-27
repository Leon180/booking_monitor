# Benchmark Comparison Report — PR #43 (cmd split + bootstrap promotion)

**Date**: Tue Apr 28 01:17:31 CST 2026
**Commit**: `5ec0da9` + review-fixup (chore: split main.go into per-subcommand files; flesh out bootstrap package)
**Run scope**: same-binary two-pass — establishes that the post-PR-43 build is stable; the PR is a pure file-organisation refactor with no hot-path bytes touched (handler.go, BookingService.BookTicket, Redis Lua, OrderMessageProcessor.Process tx body, outbox relay polling all byte-identical).

**Parameters**: VUS=500, DURATION=60s

## Test Conditions (identical for both runs)

| Setting | Value |
| :--- | :--- |
| Script | `k6_comparison.js` |
| Ticket pool | 500,000 (never sells out — measures pure capacity) |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | 500 |
| Duration | 60s |
| Target | `http://app:8080/api/v1` (direct, bypasses nginx rate limit) |

## Results

| Metric | Run A | Run B | Δ (A→B) |
| :--- | ---: | ---: | ---: |
| **Throughput (req/s)** | 50,788 | 50,597 | -0.4% |
| **p95 latency** | 12.37 ms | 11.81 ms | -4.5% ↓ |
| **avg latency** | 4.78 ms | 4.63 ms | -3.1% ↓ |
| **Total iterations** | 3,047,914 | 3,036,261 | -0.4% |
| **Business errors** | 0.00% | 0.00% | 0 |
| **p(95) threshold** | <500 ms ✓ | <500 ms ✓ | — |

## Comparison vs historical baselines

| Build | Throughput | p95 | Source |
| :--- | ---: | ---: | :--- |
| Post PR #37 (Apr 26) | 50,193 RPS | 12.63 ms | `20260426_*_compare_c500_pr35` |
| PR #40 confirmation B2 (Apr 27) | 49,141 RPS | 12.08 ms | `20260427_*_compare_c500_pr40` |
| **PR #43 Run A (this)** | **50,788 RPS** | **12.37 ms** | this directory |
| **PR #43 Run B (this)** | **50,597 RPS** | **11.81 ms** | this directory |

PR #43 sits at the **fast end** of the established noise floor (~3% RPS / ~6% p95 between same-binary runs). All metrics are within or better than the PR #37 baseline; the slight latency improvement is plausibly attributable to Go 1.25 stdlib (CI bumped Go via PR #42), not the refactor itself.

## Conclusion: no regression — pure refactor confirmed

This PR moved 700 lines from `main.go` into 4 sibling files and promoted `internal/bootstrap` to a real package owning shared boot wiring. **Zero hot-path bytes changed** — the handler, BookingService, Redis Lua, and outbox relay all sit at the same line numbers in the same files. The benchmark confirms what the diff already shows: no detectable RPS or latency regression, no business errors across 6M+ iterations.

## Why no measurable regression is the expected outcome

| Change | Hot-path impact (`POST /api/v1/book` → Redis Lua → 202) |
| :--- | :--- |
| `main.go` 699 → 68 lines (cobra root) | None. Bootstrap-time only. |
| New `server.go` / `payment.go` / `stress.go` / `tracer.go` | Function bodies byte-identical with pre-PR locations; only the file changed. |
| `internal/bootstrap/` (new `db.go` + `module.go`) | Same `provideDB` / `registerDBPoolCollector` running at fx boot; no per-request cost. |
| `bootstrap.CommonModule` wraps `fx.Module` | fx graph adds one named node; resolution happens once at startup. |
| `.gitignore` anchor fix | Source-control hygiene; no runtime effect. |
| Review fixups (`zap.*` → `mlog.*`, tracer ctx, bilingual docs) | Purely additive on logging surface + shutdown path; not on hot path. |

## Caveats

- This is a same-binary two-pass run, not an A/B against a different commit. The point isn't to detect a regression introduced by the diff (the diff demonstrably can't introduce one) — it's to confirm the post-refactor build is stable, runs to completion without error, and doesn't sit in a degraded latency band.
- Run-to-run variance for k6-on-Docker laptop is typically 3–5%; the 0.4% RPS / 4.5% p95 delta between A and B is well within that band.
- Future PRs that DO touch the hot path (saga watchdog A5, charging intent log A4) MUST run an A/B comparison against this PR's run as a fresh baseline.

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt) — Run A first pass
- [run_b_raw.txt](run_b_raw.txt) — Run B second pass
