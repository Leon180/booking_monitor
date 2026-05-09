#!/usr/bin/env bash
# scripts/run_4stage_comparison.sh — D12.5 4-stage comparison harness
#
# Runs the same k6 two-step flow scenario sequentially against each
# of the 4 stage binaries (Stage 1 sync SELECT FOR UPDATE → Stage 4
# Pattern A + saga compensator) and archives the per-stage outputs
# into a timestamped directory under `docs/benchmarks/comparisons/`.
#
# Usage:
#   ./scripts/run_4stage_comparison.sh [VUS] [DURATION] [--keep-stack]
#
# Defaults: VUS=500, DURATION=60s
# `--keep-stack` (last arg, optional): leave the bench stack up after
# the run for manual inspection. Default behavior tears it down.
#
# This script is the production-execution path for the comparison.
# `make bench-smoke` is a faster CI-friendly variant (VUS=1, DURATION
# implicit ~10s, 1 booking per stage). Use `bench-smoke` for rot
# detection; use this script for the apples-to-apples run that
# populates `comparison.md`.
#
# ────────────────────────────────────────────────────────────────
# Failure-mode guards (all from D12.5 plan-review):
#   - set -euo pipefail            top of file
#   - free-port pre-flight check   abort if 8091-8094 / 5434 / 6380 / 9091 / 19092 are in use
#   - post-/livez smoke request    confirm DB is reachable under a real query, not just /livez
#   - ticket_type_id UUID assert   catch silent shape-drift in the events response
#   - --summary-export=json        parse k6 metrics from structured JSON, not regex over stdout
#   - post-k6 http_reqs > 0 assert catch the "k6 exited 0 with 0 RPS" silent failure
#   - inter-stage drain check      poll pg_stat_activity instead of magic-constant sleep
#   - run_conditions.txt capture   auto-record toolchain versions for reproducibility
# ────────────────────────────────────────────────────────────────

set -euo pipefail

# ─── arg parse + defaults ───────────────────────────────────────
VUS="${1:-500}"
DURATION="${2:-60s}"
KEEP_STACK="${3:-}"

# Sanity: VUS must be a positive integer; DURATION must end in s/m/h.
if [[ ! "$VUS" =~ ^[0-9]+$ ]] || [[ "$VUS" -lt 1 ]]; then
    echo "FATAL: VUS must be a positive integer, got '$VUS'" >&2
    exit 2
fi
if [[ ! "$DURATION" =~ ^([0-9]+[smh])+$ ]]; then
    echo "FATAL: DURATION must be a k6-compatible duration (e.g. 60s, 5m, 1m30s), got '$DURATION'" >&2
    exit 2
fi

TS=$(date +%Y%m%d_%H%M%S)
OUTDIR="docs/benchmarks/comparisons/${TS}_4stage_c${VUS}_d${DURATION}"
mkdir -p "$OUTDIR"

# ─── Step 1: pre-flight + auto-capture run_conditions.txt ───────
echo "[1/8] Pre-flight checks + capturing run conditions..."
{
    echo "═══ D12.5 4-stage comparison run conditions ═══"
    echo "captured_at_utc: $(date -u +%FT%TZ)"
    echo "vus: $VUS"
    echo "duration: $DURATION"
    echo ""
    echo "── host ──"
    uname -a
    echo "uptime: $(uptime)"
    echo ""
    echo "── toolchain versions ──"
    docker --version 2>&1 || echo "docker: MISSING"
    docker compose version 2>&1 | head -1 || echo "docker-compose: MISSING"
    if command -v k6 >/dev/null; then k6 version 2>&1 | head -1; else echo "k6: MISSING"; fi
    if command -v psql >/dev/null; then psql --version; else echo "psql: MISSING (optional — only needed for manual debugging)"; fi
    if command -v go >/dev/null; then go version; else echo "go: MISSING"; fi
    echo ""
    echo "── git state ──"
    echo "branch: $(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
    echo "commit: $(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
    echo "dirty: $(git status --short | head -5; git status --short | wc -l | tr -d ' ') changed files"
} > "$OUTDIR/run_conditions.txt" 2>&1

cat "$OUTDIR/run_conditions.txt" | tail -20

echo ""
echo "── free-port check ──"
PORTS_TO_CHECK="5434 6380 8091 8092 8093 8094 9091 19092"
if command -v lsof >/dev/null 2>&1; then
    PORT_CONFLICT=0
    for port in $PORTS_TO_CHECK; do
        if lsof -nPi ":$port" -sTCP:LISTEN -t >/dev/null 2>&1; then
            echo "  ✗ port $port already in use"
            PORT_CONFLICT=1
        fi
    done
    if [ "$PORT_CONFLICT" = "1" ]; then
        echo ""
        echo "FATAL: one or more bench ports are occupied. Run 'make bench-down-clean'" >&2
        echo "       and any non-bench process on these ports before retrying." >&2
        exit 3
    fi
    echo "  ✓ all bench ports free ($PORTS_TO_CHECK)"
else
    # Linux CI runners frequently lack lsof; don't hard-fail on it.
    # Docker compose will error loudly if a port is bound, just less
    # clearly than this pre-flight check.
    echo "  ⚠️  lsof not found; skipping port-conflict pre-flight check"
fi

# ─── Step 2-4: bring up backing services + apply migrations + start stages ───
echo ""
echo "[2/8] Bringing up bench stack (backing services → migrations → stage binaries → wait /livez × 4)..."

# Cleanup trap: if anything below this point fails (set -e propagates),
# tear the bench stack down on exit so the next invocation doesn't
# hit a port-conflict from this run's leftover containers. Cleared
# right before the planned teardown step (or before --keep-stack
# success path).
TRAP_ACTIVE=1
cleanup_on_error() {
    if [ "${TRAP_ACTIVE:-0}" = "1" ]; then
        echo ""
        echo "⚠️  Aborting — tearing down bench stack to avoid port-conflict on next run..."
        make bench-down-clean 2>/dev/null || true
    fi
}
trap cleanup_on_error EXIT

make bench-up

# ─── Step 5: post-/livez smoke per stage ────────────────────────
# /livez says "process alive". POST /api/v1/events validates the DB
# connection actually services a real query under a non-trivial
# code path (event create + ticket_type insert in same tx). If
# this fails, the orchestrator aborts BEFORE k6 wastes a 60s
# benchmark window driving load against a half-broken stage.
echo ""
echo "[3/8] Post-/livez DB-reachable smoke per stage..."
for stage in 1 2 3 4; do
    port=$((8090 + stage))
    smoke_resp=$(curl -sS -X POST "http://localhost:$port/api/v1/events" \
        -H 'Content-Type: application/json' \
        -d '{"name":"d12.5-orchestration-smoke","total_tickets":10,"price_cents":1000,"currency":"usd"}')
    tt_id=$(echo "$smoke_resp" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d['ticket_types'][0]['id'])
except Exception as e:
    sys.exit(f'parse failed: {e}')
" 2>&1) || {
        echo "  ✗ stage $stage smoke parse failed (response shape changed?)"
        echo "    response: $smoke_resp"
        exit 4
    }
    # UUID v4/v7 regex assertion (closes plan-review HIGH #3 —
    # silent capture of jq null is the failure mode if response
    # JSON shape drifts).
    if [[ ! "$tt_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]; then
        echo "  ✗ stage $stage ticket_type_id is not a valid UUID: '$tt_id'"
        echo "    response: $smoke_resp"
        exit 4
    fi
    echo "  ✓ stage $stage smoke OK (ticket_type_id $tt_id)"
done

# ─── k6 invocation helper ───────────────────────────────────────
# Used twice per stage (Slice 8): once with the full 2-step flow
# scenario (k6_two_step_flow.js) and once with the pure-intake
# scenario (k6_intake_only.js). The pure-intake run aligns with
# Ticketmaster / Stripe / Shopify operator-experience methodology
# (each layer measured separately rather than as a full-flow
# atomic unit) — see comparison.md "Why two scenarios" callout.
#
# Args: stage_num port stage_dir script_path label
#   - label is the basename for output files: <label>_summary.json,
#     <label>_run_raw.txt
#   - "full_flow" + "intake_only" are the two canonical labels.
run_k6_scenario() {
    local stage=$1 port=$2 stage_dir=$3 script=$4 label=$5
    local summary="$stage_dir/${label}_summary.json"
    local raw="$stage_dir/${label}_run_raw.txt"

    echo "  → $label (k6 $script)..."

    # Threshold breaches → exit 99 (or 1 in newer k6); other non-zero
    # codes are real failures. Tolerate non-zero here and let the
    # http_reqs > 0 assertion downstream decide if the run was usable.
    set +e
    k6 run \
        --summary-export "$summary" \
        -e API_ORIGIN="http://localhost:$port" \
        -e VUS="$VUS" \
        -e DURATION="$DURATION" \
        -e TICKET_POOL=500000 \
        "$script" \
        > "$raw" 2>&1
    local k6_exit=$?
    set -e

    if [ "$k6_exit" -ne 0 ]; then
        if grep -qE "thresholds on metrics .* have been crossed" "$raw"; then
            echo "    ⚠️  $label k6 exit=$k6_exit (threshold breach — informational, not fatal)"
        else
            echo "    ✗ $label k6 exit=$k6_exit (real failure)"
            echo "    last 20 lines of $raw:"
            tail -20 "$raw" | sed 's/^/      /'
            exit 5
        fi
    fi

    if [ ! -f "$summary" ]; then
        echo "    ✗ $label summary.json missing at $summary — k6 didn't write it"
        exit 5
    fi

    local http_reqs
    http_reqs=$(python3 -c "
import json, sys
try:
    d = json.load(open('$summary'))
    print(int(d['metrics']['http_reqs']['count']))
except Exception as e:
    sys.exit(f'parse failed: {e}')
" 2>&1) || {
        echo "    ✗ $label summary.json parse failed at $summary"
        cat "$summary" | head -40
        exit 5
    }
    if [ "$http_reqs" -lt 1 ]; then
        echo "    ✗ $label k6 produced 0 http_reqs — silent failure"
        echo "    (likely BASE_URL wrong, container DNS, or stage crashed mid-run)"
        echo "    last 30 lines of $raw:"
        tail -30 "$raw" | sed 's/^/      /'
        exit 5
    fi
    echo "    ✓ $label: $http_reqs http_reqs"
}

# ─── Step 6-7: run k6 sequentially per stage (BOTH scenarios) ───
echo ""
echo "[4/8] Running k6 sequentially per stage (full_flow + intake_only)..."
for stage in 1 2 3 4; do
    port=$((8090 + stage))
    stage_dir="$OUTDIR/stage$stage"
    mkdir -p "$stage_dir"
    echo ""
    echo "── stage $stage (port $port) ──"

    # Full-flow scenario: book → poll → pay → confirm → poll paid
    # OR abandon → poll compensated. Records the operational
    # funnel-completion view (acceptance ratio, e2e latency).
    run_k6_scenario "$stage" "$port" "$stage_dir" "scripts/k6_two_step_flow.js" "full_flow"

    # Brief drain between scenarios so intake-only's k6 doesn't
    # contend with full-flow's residual goroutines / PG connections.
    sleep 3

    # Intake-only scenario: each VU just hammers POST /book → next
    # iteration. Records the booking-layer ceiling — comparable to
    # Ticketmaster's "tickets sold per second" published metric.
    run_k6_scenario "$stage" "$port" "$stage_dir" "scripts/k6_intake_only.js" "intake_only"

    # ─── Step 9: inter-stage drain ──────────────────────────────
    # closes plan-review MED #1: replace 10s magic constant with
    # an actual drain check on this stage's PG connections. Stop
    # before stage 4 because there's no "stage 5" to wait for.
    if [ "$stage" -lt 4 ]; then
        # Sanity: postgres-bench MUST be running for the drain check
        # to be meaningful. If we silently fall back to "0" on a
        # missing container, the drain "succeeds" while PG is dead —
        # the next stage's k6 then runs against a corpse. (Slice 3
        # review HIGH #2: don't mask docker exec failure.)
        if ! docker inspect booking_bench_pg >/dev/null 2>&1; then
            echo "  ✗ postgres-bench container is GONE — abort"
            exit 6
        fi
        echo "  draining stage $stage active connections (max 30s)..."
        drained=0
        active="?"
        for i in $(seq 1 30); do
            active=$(docker exec booking_bench_pg \
                psql -U booking -tAc "SELECT count(*) FROM pg_stat_activity WHERE datname = 'booking_stage$stage' AND state = 'active'" \
                2>/dev/null) || {
                    echo "  ✗ drain query against booking_bench_pg failed mid-poll — abort"
                    exit 6
                }
            # drain target: <= 1 (the SELECT itself often shows up)
            if [ "$active" -le "1" ]; then
                echo "  ✓ drained (active=$active)"
                drained=1
                break
            fi
            sleep 1
        done
        # Slice 3 review HIGH #6: don't fall through silently. Plan
        # called for "log warning and proceed" so a stuck transaction
        # surfaces as a comparison.md caveat rather than a hidden
        # cross-stage contamination.
        if [ "$drained" = "0" ]; then
            echo "  ⚠️  WARN: stage $stage drain timed out at 30s (active=$active)" \
                 "— next stage may see residual contention; flag in comparison.md"
        fi
    fi
done

# ─── Step 10: capture Prometheus snapshot ───────────────────────
# Small archive of the queries that comparison.md will quote so a
# reader doesn't need a running Prometheus to validate the prose.
echo ""
echo "[5/8] Capturing Prometheus snapshot..."
{
    echo "{"
    echo "  \"snapshot_at_utc\": \"$(date -u +%FT%TZ)\","
    echo "  \"queries\": ["
    first=1
    for query in 'up{job="booking-stages"}' \
                 'sum by (stage) (rate(http_requests_total[60s]))' \
                 'saga_compensator_events_processed_total{stage="stage4"}' \
                 'saga_compensation_consumer_lag_seconds{stage="stage4"}' \
                 'saga_compensation_loop_duration_seconds_count{stage="stage4"}'
    do
        [ "$first" = "0" ] && echo ","
        first=0
        echo -n "    {\"query\": $(printf '%s' "$query" | python3 -c "import json,sys; print(json.dumps(sys.stdin.read()))"), \"result\": "
        curl -sS --data-urlencode "query=$query" "http://localhost:9091/api/v1/query" 2>/dev/null || echo "{}"
        echo -n "}"
    done
    echo ""
    echo "  ]"
    echo "}"
} > "$OUTDIR/prometheus_snapshot.json" 2>&1 || {
    echo "  ⚠️  prometheus snapshot failed (non-fatal — comparison.md can recompute from raw)"
}

# ─── Step 11: tear down ─────────────────────────────────────────
# Disarm the cleanup trap — we reached the success path. Without
# this, --keep-stack would still trigger the cleanup_on_error
# tear-down at EXIT (defeating the keep-stack flag).
TRAP_ACTIVE=0

echo ""
if [ "$KEEP_STACK" = "--keep-stack" ]; then
    echo "[6/8] --keep-stack passed; leaving bench stack up. Tear down with 'make bench-down-clean' when done."
else
    echo "[6/8] Tearing down bench stack..."
    make bench-down-clean
fi

# ─── Slice 4 hand-off ───────────────────────────────────────────
echo ""
echo "[7/8] Output archived:"
ls -la "$OUTDIR"
for stage in 1 2 3 4; do
    if [ -f "$OUTDIR/stage$stage/summary.json" ]; then
        ls -la "$OUTDIR/stage$stage/" 2>/dev/null | tail -3 | sed 's/^/    /'
    fi
done

echo ""
echo "[8/8] Next: Slice 4's scripts/generate_comparison_md.sh reads"
echo "      $OUTDIR/stage{1..4}/summary.json + run_conditions.txt"
echo "      and emits $OUTDIR/comparison.md"
echo ""
echo "✓ 4-stage comparison run complete: $OUTDIR/"
