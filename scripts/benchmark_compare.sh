#!/bin/bash
# scripts/benchmark_compare.sh
# Runs two back-to-back benchmarks under IDENTICAL conditions and writes a
# side-by-side comparison report.
#
# Usage: ./scripts/benchmark_compare.sh [VUS] [DURATION]
#
# Both runs use k6_comparison.js:
#   - 500,000 tickets (never sells out → pure booking throughput)
#   - user_id range: 1–9,999,999 (minimises duplicate 409s)
#   - quantity: 1 per request
#
# Run A: warm-up / first pass
# Run B: second pass (same conditions)
# This lets you see natural variance and confirm stability.

set -euo pipefail

VUS=${1:-500}
DURATION=${2:-60s}
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
REPORT_DIR="docs/benchmarks/${TIMESTAMP}_compare_c${VUS}"
mkdir -p "$REPORT_DIR"

K6_SCRIPT="$(pwd)/scripts/k6_comparison.js"
DB_URL="postgres://user:password@localhost:5433/booking?sslmode=disable"

RUN_A_FILE="$REPORT_DIR/run_a_raw.txt"
RUN_B_FILE="$REPORT_DIR/run_b_raw.txt"
SUMMARY="$REPORT_DIR/comparison.md"

echo "============================================"
echo "  Comparison Benchmark: VUS=$VUS DURATION=$DURATION"
echo "  Report: $REPORT_DIR"
echo "  Script: k6_comparison.js (500k tickets)"
echo "============================================"

# ── Helpers ──────────────────────────────────────────────────────────────────

reset_state() {
    echo "[reset] Flushing Redis and resetting DB..."
    docker exec booking_redis redis-cli FLUSHALL > /dev/null
    psql "$DB_URL" -q -c "TRUNCATE TABLE orders; UPDATE events SET available_tickets = total_tickets;" > /dev/null
    echo "[reset] Done."
}

run_k6() {
    local label=$1
    local out_file=$2
    echo ""
    echo "=== $label ==="
    docker run --rm -i \
        --network=booking_monitor_default \
        -e VUS="$VUS" \
        -e DURATION="$DURATION" \
        -v "$K6_SCRIPT:/script.js" \
        grafana/k6 run /script.js 2>&1 | tee "$out_file"
    echo "[k6] $label complete."
}

extract_p95()  { grep "http_req_duration" "$1" | grep -v "expected_response" | awk '{for(i=1;i<=NF;i++) if($i ~ /^p\(95\)=/) {sub(/p\(95\)=/,"",$i); print $i}}' | head -1; }
extract_avg()  { grep "http_req_duration" "$1" | grep -v "expected_response" | awk '{for(i=1;i<=NF;i++) if($i ~ /^avg=/) {sub(/avg=/,"",$i); print $i}}' | head -1; }
extract_rps()  { grep "http_reqs" "$1" | grep -v "expected_response" | awk '{for(i=1;i<=NF;i++) if($i ~ /[0-9]+\.[0-9]+\/s$/) print $i}' | head -1; }
extract_ok()   { grep "booking accepted" "$1" | awk '{for(i=1;i<=NF;i++) if($i ~ /^[0-9]+%/) print $i}' | head -1; }
extract_err()  { grep "business_errors" "$1" | awk '{for(i=1;i<=NF;i++) if($i ~ /^[0-9]+\.[0-9]+%/) print $i}' | head -1; }
extract_iters(){ grep "^    iterations" "$1" | awk '{print $2}' | head -1; }

# ── Run A ─────────────────────────────────────────────────────────────────────
reset_state
sleep 2
run_k6 "RUN A (first pass)" "$RUN_A_FILE"

# ── Run B ─────────────────────────────────────────────────────────────────────
reset_state
sleep 2
run_k6 "RUN B (second pass)" "$RUN_B_FILE"

# ── Extract metrics ───────────────────────────────────────────────────────────
A_RPS=$(extract_rps "$RUN_A_FILE");   B_RPS=$(extract_rps "$RUN_B_FILE")
A_P95=$(extract_p95 "$RUN_A_FILE");   B_P95=$(extract_p95 "$RUN_B_FILE")
A_AVG=$(extract_avg "$RUN_A_FILE");   B_AVG=$(extract_avg "$RUN_B_FILE")
A_OK=$(extract_ok "$RUN_A_FILE");     B_OK=$(extract_ok "$RUN_B_FILE")
A_ERR=$(extract_err "$RUN_A_FILE");   B_ERR=$(extract_err "$RUN_B_FILE")
A_IT=$(extract_iters "$RUN_A_FILE");  B_IT=$(extract_iters "$RUN_B_FILE")

# ── Write comparison report ───────────────────────────────────────────────────
cat > "$SUMMARY" << EOF
# Benchmark Comparison Report

**Date**: $(date)
**Commit**: $(git rev-parse --short HEAD) — $(git log -1 --pretty=%s)
**Parameters**: VUS=${VUS}, DURATION=${DURATION}

## Test Conditions (identical for both runs)

| Setting | Value |
| :--- | :--- |
| Script | \`k6_comparison.js\` |
| Ticket pool | 500,000 (never sells out) |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | ${VUS} |
| Duration | ${DURATION} |

## Results

| Metric | Run A | Run B | Δ |
| :--- | ---: | ---: | :--- |
| **Throughput (req/s)** | ${A_RPS:-N/A} | ${B_RPS:-N/A} | — |
| **p95 latency** | ${A_P95:-N/A} | ${B_P95:-N/A} | — |
| **avg latency** | ${A_AVG:-N/A} | ${B_AVG:-N/A} | — |
| **Booking accepted** | ${A_OK:-N/A} | ${B_OK:-N/A} | — |
| **Business errors** | ${A_ERR:-N/A} | ${B_ERR:-N/A} | — |
| **Total iterations** | ${A_IT:-N/A} | ${B_IT:-N/A} | — |

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
EOF

echo ""
echo "============================================"
echo "  Comparison complete! Report: $SUMMARY"
echo "============================================"
cat "$SUMMARY"
