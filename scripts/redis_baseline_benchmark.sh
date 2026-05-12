#!/bin/bash
# scripts/redis_baseline_benchmark.sh
#
# Compares deduct.lua throughput against Redis raw baselines on the
# SAME instance. Answers the recurring senior-interviewer question:
#
#   "Why is your deduct.lua ceiling 8,330/s when Redis single-instance
#    can do 100k+ SET/GET?"
#
# By running 5 modes back-to-back against our own Redis container,
# the per-op breakdown becomes reproducible rather than rhetorical.
#
# Modes:
#   A. SET no pipeline           — raw Redis floor (~70k expected)
#   B. SET pipelined -P 16       — pipelining lift  (~1.5M expected)
#   C. GET pipelined -P 16       — read-side baseline
#   D. XADD alone                — Streams write isolated
#   E. EVAL no-pipeline          — Lua eval overhead, single-op script
#   F. deduct.lua (real script)  — our actual hot-path
#
# Usage:
#   ./scripts/redis_baseline_benchmark.sh [REQUESTS] [CLIENTS]
#
# Defaults: REQUESTS=100000  CLIENTS=50
#
# Output:
#   docs/benchmarks/<timestamp>_redis_baseline/
#     ├── raw_output.txt   — full redis-benchmark + redis-cli output
#     └── summary.md       — side-by-side RPS table + analysis
#
# Requirements:
#   - docker compose up redis  (or running Redis on localhost:6379)
#   - REDIS_PASSWORD env var set (matches docker-compose .env)
#   - jq for JSON parsing (brew install jq / apt install jq)

set -euo pipefail

# ─── Args & env ──────────────────────────────────────────────────────────
REQUESTS=${1:-100000}
CLIENTS=${2:-50}
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
REPORT_DIR="docs/benchmarks/${TIMESTAMP}_redis_baseline"
RAW="${REPORT_DIR}/raw_output.txt"
SUMMARY="${REPORT_DIR}/summary.md"

REDIS_CONTAINER="${REDIS_CONTAINER:-booking_redis}"
REDIS_HOST="${REDIS_HOST:-localhost}"
REDIS_PORT="${REDIS_PORT:-6379}"

if [[ -z "${REDIS_PASSWORD:-}" ]]; then
    echo "ERROR: REDIS_PASSWORD env var not set (read from .env or export manually)" >&2
    exit 1
fi

mkdir -p "$REPORT_DIR"

# ─── Helpers ─────────────────────────────────────────────────────────────
# Run redis-benchmark inside the container. Inside-container avoids
# host-network NAT effects so the per-op math is clean.
run_inside() {
    docker exec -i "$REDIS_CONTAINER" redis-benchmark -h 127.0.0.1 -p 6379 \
        -a "$REDIS_PASSWORD" "$@"
}

# Extract "requests per second" from redis-benchmark -q output.
# Real output line: "SET: 284900.28 requests per second, p50=0.095 msec"
# Caveat: -q progress uses \r to overwrite, so everything appears on
# ONE mashed line in the captured stream. Match the specific tail
# pattern "<num> requests per second" to avoid picking up "rps=0.0"
# from the progress prefix.
extract_rps() {
    grep -oE '[0-9]+\.[0-9]+ requests per second' | tail -1 | awk '{ print $1 }'
}

# Section divider in raw_output.txt
section() {
    echo "" >> "$RAW"
    echo "═══════════════════════════════════════════════════════════════" >> "$RAW"
    echo "  $1" >> "$RAW"
    echo "═══════════════════════════════════════════════════════════════" >> "$RAW"
}

# ─── Pre-flight checks ───────────────────────────────────────────────────
if ! docker ps --format '{{.Names}}' | grep -q "^${REDIS_CONTAINER}$"; then
    echo "ERROR: container '${REDIS_CONTAINER}' not running. Start it with: docker compose up -d redis" >&2
    exit 1
fi

echo "→ Benchmark plan: $REQUESTS requests × $CLIENTS clients per mode"
echo "→ Target: $REDIS_CONTAINER (in-container, loopback to skip host NAT)"
echo "→ Output: $REPORT_DIR/"
echo ""

# Capture Redis INFO + version for reproducibility
section "Environment snapshot"
{
    echo "Timestamp: $TIMESTAMP"
    echo "Host: $(uname -a)"
    echo ""
    echo "Redis version + flags:"
    docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --no-auth-warning INFO server | head -20
    echo ""
    echo "Persistence config (we should see appendonly:no and rdb_changes_since_last_save):"
    docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --no-auth-warning INFO persistence | head -10
    echo ""
    echo "Memory:"
    docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --no-auth-warning INFO memory | head -6
} >> "$RAW" 2>&1

# ─── Mode A: SET no pipeline ─────────────────────────────────────────────
echo "→ Mode A: SET no pipeline"
section "Mode A: SET no pipeline (-n $REQUESTS -c $CLIENTS)"
RPS_A=$(run_inside -t set -n "$REQUESTS" -c "$CLIENTS" -q 2>>"$RAW" | tee -a "$RAW" | extract_rps)

# ─── Mode B: SET pipelined ───────────────────────────────────────────────
echo "→ Mode B: SET pipelined -P 16"
section "Mode B: SET pipelined (-P 16 -n $REQUESTS -c $CLIENTS)"
RPS_B=$(run_inside -t set -n "$REQUESTS" -c "$CLIENTS" -P 16 -q 2>>"$RAW" | tee -a "$RAW" | extract_rps)

# ─── Mode C: GET pipelined ───────────────────────────────────────────────
echo "→ Mode C: GET pipelined -P 16"
section "Mode C: GET pipelined (-P 16 -n $REQUESTS -c $CLIENTS)"
RPS_C=$(run_inside -t get -n "$REQUESTS" -c "$CLIENTS" -P 16 -q 2>>"$RAW" | tee -a "$RAW" | extract_rps)

# ─── Mode D: XADD alone ──────────────────────────────────────────────────
echo "→ Mode D: XADD alone"
section "Mode D: XADD alone (small field set, -n $REQUESTS -c $CLIENTS)"
RPS_D=$(run_inside -t xadd -n "$REQUESTS" -c "$CLIENTS" -q 2>>"$RAW" | tee -a "$RAW" | extract_rps)

# ─── Mode E: EVAL no-pipeline ────────────────────────────────────────────
# Simple Lua script — just DECR a key. Measures Lua-eval overhead
# without the multi-op cost of deduct.lua.
echo "→ Mode E: EVAL no-pipeline (single-op Lua DECR)"
section "Mode E: EVAL no-pipeline (single DECR script)"
docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --no-auth-warning SET baseline:counter 9999999999 >/dev/null 2>&1
RPS_E=$(run_inside -n "$REQUESTS" -c "$CLIENTS" -q \
    eval "return redis.call('DECR', KEYS[1])" 1 baseline:counter \
    2>>"$RAW" | tee -a "$RAW" | extract_rps)

# ─── Mode F: deduct.lua real script ──────────────────────────────────────
echo "→ Mode F: deduct.lua (real hot-path script)"
section "Mode F: deduct.lua (DECRBY + HMGET + multiply + XADD)"

# Pre-populate inventory + metadata so the Lua script can run end-to-end
docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --no-auth-warning <<'EOF' >/dev/null 2>&1
DEL ticket_type_qty:baseline
SET ticket_type_qty:baseline 999999999
HSET ticket_type_meta:baseline event_id baseline-event price_cents 1000 currency usd
EOF

# Load the script and grab its SHA so we can EVALSHA it via redis-benchmark
DEDUCT_LUA_PATH="internal/infrastructure/cache/lua/deduct.lua"
if [[ ! -f "$DEDUCT_LUA_PATH" ]]; then
    echo "ERROR: $DEDUCT_LUA_PATH not found — run from repo root" >&2
    exit 1
fi
DEDUCT_SHA=$(docker exec -i "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --no-auth-warning \
    -x SCRIPT LOAD < "$DEDUCT_LUA_PATH" | tr -d '\r\n ')

echo "  loaded deduct.lua SHA: $DEDUCT_SHA"
echo "  loaded deduct.lua SHA: $DEDUCT_SHA" >> "$RAW"

# Run via EVALSHA. Args per deduct.lua header:
#   KEYS[1]=ticket_type_qty:baseline KEYS[2]=ticket_type_meta:baseline
#   ARGV: 1 1 baseline-order-id 0 baseline-tt
RPS_F=$(run_inside -n "$REQUESTS" -c "$CLIENTS" -q \
    evalsha "$DEDUCT_SHA" 2 ticket_type_qty:baseline ticket_type_meta:baseline \
        1 1 baseline-order-id 0 baseline-tt \
    2>>"$RAW" | tee -a "$RAW" | extract_rps)

# Clean up benchmark-only keys (inventory + metadata for baseline event)
# Leave orders:stream entries since deleting them mid-benchmark could
# race with a running worker. Run `make reset-db` separately if you
# want to fully wipe stream state.
docker exec "$REDIS_CONTAINER" redis-cli -a "$REDIS_PASSWORD" --no-auth-warning <<'EOF' >/dev/null 2>&1 || true
DEL ticket_type_qty:baseline ticket_type_meta:baseline baseline:counter
EOF

# ─── Summary report ──────────────────────────────────────────────────────
echo "→ Writing summary to $SUMMARY"

# Compute deduct.lua relative throughput vs each baseline.
relative() {
    if [[ -z "$1" || -z "$2" || "$2" == "0" ]]; then echo "n/a"; return; fi
    awk -v num="$1" -v den="$2" 'BEGIN { printf "%.1f×", num/den }'
}

cat > "$SUMMARY" <<EOF
# Redis baseline vs deduct.lua benchmark

**Timestamp**: $TIMESTAMP
**Conditions**: in-container redis-benchmark, $REQUESTS requests × $CLIENTS clients, loopback (no host NAT).
**Persistence**: AOF + RDB disabled (see \`deploy/redis/redis.conf\`) — ephemeral cache pattern.

## Mode-by-mode RPS

| Mode | What's measured | RPS | vs deduct.lua |
|:--|:--|--:|--:|
| A | SET no pipeline                              | ${RPS_A:-n/a} | $(relative "${RPS_A:-0}" "${RPS_F:-1}") |
| B | SET pipelined \`-P 16\`                      | ${RPS_B:-n/a} | $(relative "${RPS_B:-0}" "${RPS_F:-1}") |
| C | GET pipelined \`-P 16\`                      | ${RPS_C:-n/a} | $(relative "${RPS_C:-0}" "${RPS_F:-1}") |
| D | XADD alone (small field set)                 | ${RPS_D:-n/a} | $(relative "${RPS_D:-0}" "${RPS_F:-1}") |
| E | EVAL no-pipeline (single DECR)               | ${RPS_E:-n/a} | $(relative "${RPS_E:-0}" "${RPS_F:-1}") |
| F | **deduct.lua** (real hot path)               | **${RPS_F:-n/a}** | 1× |

## Interpretation

- **A vs B/C** quantifies pipelining lift on raw ops. SET no-pipeline → -P 16 is typically 10-20× on bare metal, somewhat less on Docker NAT setups. If the gap is small, your network path is the cap (not Redis).
- **D** isolates XADD cost. deduct.lua's XADD-with-9-fields is the most expensive single op in the script; D approximates its lower bound (real deduct.lua XADD is slightly heavier because of more fields).
- **E vs F** isolates Lua eval overhead from multi-op Lua. E is a 1-op Lua (DECR-only); F adds DECRBY + HMGET 3 fields + Lua decimal multiply + XADD 9 fields. Ratio F/E tells you how much of deduct.lua's cost is the script body vs the Lua machinery.
- **F vs A** is the headline. Senior interviewers ask "Redis can do 100k+ SET/GET, why is your Lua 8330?". The ratio F/A on this report is the data-backed answer: each deduct.lua invocation costs roughly *(A/F)* × the cost of a single SET, because it does that many sequential ops internally and can't pipeline.

## What this confirms

The blog post claim "8,330 acc/s is the same-key Lua throughput ceiling on this setup" is not a contradiction with "Redis can do 100k+ SET/GET" — these benchmarks measure different things. On a bare-metal Linux box you'd expect Mode F to climb to ~25-40k, but the relative ratios A:B:C:D:E:F should stay roughly the same (the architectural cap is about op-count per script + non-pipelineability, not hardware).

## Reproducing on different hardware

Run on bare-metal Linux / EC2 c6i.xlarge / etc. and compare the Mode F number. If F scales 3-5× but A:F ratio stays similar, that's the hardware-can-help-throughput-but-not-architecture story. If F barely moves but A jumps a lot, your bottleneck has moved from Redis to elsewhere (network NAT, client library, Go-redis pool).

## Raw output

See [\`raw_output.txt\`](raw_output.txt) for full \`redis-benchmark\` output per mode plus the Redis INFO snapshot.
EOF

echo ""
echo "✅ Done. Summary written to: $SUMMARY"
echo ""
echo "Quick view:"
echo "  Mode A (SET raw):           ${RPS_A:-n/a} RPS"
echo "  Mode B (SET -P 16):         ${RPS_B:-n/a} RPS"
echo "  Mode C (GET -P 16):         ${RPS_C:-n/a} RPS"
echo "  Mode D (XADD):              ${RPS_D:-n/a} RPS"
echo "  Mode E (EVAL single DECR):  ${RPS_E:-n/a} RPS"
echo "  Mode F (deduct.lua):        ${RPS_F:-n/a} RPS  ← real hot path"
