#!/bin/bash
# scripts/benchmark_gc.sh
# GC-focused benchmark: runs k6 load test while simultaneously collecting
# Go runtime/GC metrics and pprof profiles.
#
# Usage: ./scripts/benchmark_gc.sh [VUS] [DURATION] [LABEL]
#
# Produces a report directory under docs/benchmarks/ containing:
#   - comparison.md          (same format as benchmark_compare.sh)
#   - gc_metrics.csv         (Go runtime metrics sampled every 5s)
#   - pprof/heap.pb.gz       (heap profile captured mid-test)
#   - pprof/allocs.pb.gz     (allocation profile, 30s sample)
#   - pprof/goroutine.pb.gz  (goroutine dump)
#   - run_raw.txt            (raw k6 output)
#
# Prerequisites:
#   - docker-compose stack running (with ENABLE_PPROF=true in .env)
#   - migrations applied (make migrate-up)
#   - make reset-db run at least once

set -euo pipefail

VUS=${1:-500}
DURATION=${2:-60s}
LABEL=${3:-gc_baseline}
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
REPORT_DIR="docs/benchmarks/${TIMESTAMP}_${LABEL}"
mkdir -p "$REPORT_DIR/pprof"

K6_SCRIPT="$(pwd)/scripts/k6_comparison.js"
MIGRATE_DB_URL="${MIGRATE_DB_URL:-postgres://booking:smoketest_pg_local@localhost:5433/booking?sslmode=disable}"

echo "============================================"
echo "  GC Baseline Benchmark"
echo "  VUS=$VUS  DURATION=$DURATION  LABEL=$LABEL"
echo "  Report: $REPORT_DIR"
echo "============================================"

# ── Helpers ──────────────────────────────────────────────────────────────────

reset_state() {
    echo "[reset] Flushing Redis and resetting DB..."
    docker exec booking_redis redis-cli -a "${REDIS_PASSWORD:-smoketest_redis_local}" FLUSHALL > /dev/null 2>&1
    psql "$MIGRATE_DB_URL" -q -c "TRUNCATE TABLE orders; UPDATE events SET available_tickets = total_tickets;" > /dev/null
    echo "[reset] Done."
}

# ── Pre-flight checks ────────────────────────────────────────────────────────

echo "[check] Verifying pprof is accessible..."
if ! curl -sf http://localhost:6060/debug/pprof/ > /dev/null 2>&1; then
    echo "ERROR: pprof endpoint not reachable at :6060."
    echo "       Make sure ENABLE_PPROF=true in .env and docker-compose rebuild."
    exit 1
fi
echo "[check] pprof OK."

echo "[check] Verifying /metrics is accessible..."
if ! docker exec booking_app wget -qO- http://localhost:8080/metrics > /dev/null 2>&1; then
    echo "ERROR: /metrics not reachable on booking_app."
    exit 1
fi
echo "[check] /metrics OK."

# ── Record environment ────────────────────────────────────────────────────────

cat > "$REPORT_DIR/environment.md" << EOF
| Setting | Value |
|---------|-------|
| Date | $(date) |
| Go version | $(go version 2>/dev/null || echo "N/A") |
| Git commit | $(git rev-parse --short HEAD) — $(git log -1 --pretty=%s) |
| OS | $(uname -srm) |
| Docker | $(docker --version 2>/dev/null) |
| VUS | $VUS |
| Duration | $DURATION |
| GOGC | $(docker exec booking_app sh -c 'echo ${GOGC:-default}' 2>/dev/null) |
| GOMEMLIMIT | $(docker exec booking_app sh -c 'echo ${GOMEMLIMIT:-not set}' 2>/dev/null) |
EOF

# ── Reset state ───────────────────────────────────────────────────────────────
reset_state
sleep 2

# ── Start GC metrics collection (background) ─────────────────────────────────
echo "[gc_metrics] Starting background collection..."
./scripts/gc_metrics.sh "$REPORT_DIR/gc_metrics.csv" 5 &
GC_PID=$!

# ── Start pprof capture (background, delayed) ────────────────────────────────
echo "[pprof] Starting background capture (30s delay)..."
./scripts/pprof_capture.sh "$REPORT_DIR/pprof" 30 &
PPROF_PID=$!

# ── Run k6 ────────────────────────────────────────────────────────────────────
echo ""
echo "=== k6 Load Test ==="
docker run --rm -i \
    --network=booking_monitor_default \
    -e VUS="$VUS" \
    -e DURATION="$DURATION" \
    -v "$K6_SCRIPT:/script.js" \
    grafana/k6 run /script.js 2>&1 | tee "$REPORT_DIR/run_raw.txt"
echo "[k6] Complete."

# ── Stop background collectors ────────────────────────────────────────────────
kill $GC_PID 2>/dev/null || true
wait $PPROF_PID 2>/dev/null || true
echo "[collectors] Stopped."

# ── Extract k6 metrics ───────────────────────────────────────────────────────
RAW="$REPORT_DIR/run_raw.txt"
RPS=$(grep "http_reqs" "$RAW" | grep -v "expected_response" | awk '{for(i=1;i<=NF;i++) if($i ~ /[0-9]+\.[0-9]+\/s$/) print $i}' | head -1)
P95=$(grep "http_req_duration" "$RAW" | grep -v "expected_response" | awk '{for(i=1;i<=NF;i++) if($i ~ /^p\(95\)=/) {sub(/p\(95\)=/,"",$i); print $i}}' | head -1)
AVG=$(grep "http_req_duration" "$RAW" | grep -v "expected_response" | awk '{for(i=1;i<=NF;i++) if($i ~ /^avg=/) {sub(/avg=/,"",$i); print $i}}' | head -1)
P99=$(grep "http_req_duration" "$RAW" | grep -v "expected_response" | awk '{for(i=1;i<=NF;i++) if($i ~ /^p\(99\)=/) {sub(/p\(99\)=/,"",$i); print $i}}' | head -1)
OK_PCT=$(grep "booking accepted" "$RAW" | awk '{for(i=1;i<=NF;i++) if($i ~ /^[0-9]+%/) print $i}' | head -1)
ERR_PCT=$(grep "business_errors" "$RAW" | awk '{for(i=1;i<=NF;i++) if($i ~ /^[0-9]+\.[0-9]+%/) print $i}' | head -1)
ITERS=$(grep "^    iterations" "$RAW" | awk '{print $2}' | head -1)

# ── Extract GC metrics summary ───────────────────────────────────────────────
GC_CSV="$REPORT_DIR/gc_metrics.csv"
if [ -f "$GC_CSV" ] && [ "$(wc -l < "$GC_CSV")" -gt 1 ]; then
    # Skip header, compute min/max/avg for each column
    GC_SUMMARY=$(awk -F',' 'NR>1 {
        for(i=2; i<=NF; i++) {
            sum[i]+=$i; count[i]++;
            if(NR==2 || $i+0 > max[i]+0) max[i]=$i;
            if(NR==2 || $i+0 < min[i]+0) min[i]=$i;
        }
    } END {
        for(i=2; i<=NF; i++) {
            avg[i] = (count[i]>0) ? sum[i]/count[i] : 0
            printf "%s,%s,%s\n", min[i], max[i], avg[i]
        }
    }' "$GC_CSV")

    GC_PAUSE_MAX_STATS=$(echo "$GC_SUMMARY" | sed -n '1p')
    GC_PAUSE_P75_STATS=$(echo "$GC_SUMMARY" | sed -n '2p')
    HEAP_INUSE_STATS=$(echo "$GC_SUMMARY" | sed -n '3p')
    HEAP_ALLOC_STATS=$(echo "$GC_SUMMARY" | sed -n '4p')
    GOROUTINE_STATS=$(echo "$GC_SUMMARY" | sed -n '5p')
    MALLOCS_STATS=$(echo "$GC_SUMMARY" | sed -n '6p')
    FREES_STATS=$(echo "$GC_SUMMARY" | sed -n '7p')
    GC_CYCLES_STATS=$(echo "$GC_SUMMARY" | sed -n '8p')
else
    GC_PAUSE_MAX_STATS="N/A,N/A,N/A"
    GC_PAUSE_P75_STATS="N/A,N/A,N/A"
    HEAP_INUSE_STATS="N/A,N/A,N/A"
    HEAP_ALLOC_STATS="N/A,N/A,N/A"
    GOROUTINE_STATS="N/A,N/A,N/A"
    MALLOCS_STATS="N/A,N/A,N/A"
    FREES_STATS="N/A,N/A,N/A"
    GC_CYCLES_STATS="N/A,N/A,N/A"
fi

fmt_stat() { echo "$1" | awk -F',' "{printf \"| $2 | %s | %s | %s |\", \$1, \$2, \$3}"; }

# ── Write report ──────────────────────────────────────────────────────────────
cat > "$REPORT_DIR/report.md" << REPORT
# GC Baseline Benchmark Report

**Date**: $(date)
**Commit**: $(git rev-parse --short HEAD) — $(git log -1 --pretty=%s)
**Parameters**: VUS=${VUS}, DURATION=${DURATION}, Label=${LABEL}

## Test Conditions

| Setting | Value |
| :--- | :--- |
| Script | \`k6_comparison.js\` |
| Ticket pool | 500,000 (never sells out) |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | ${VUS} |
| Duration | ${DURATION} |

## Throughput & Latency

| Metric | Value |
| :--- | ---: |
| **Throughput (req/s)** | ${RPS:-N/A} |
| **p95 latency** | ${P95:-N/A} |
| **p99 latency** | ${P99:-N/A} |
| **avg latency** | ${AVG:-N/A} |
| **Booking accepted** | ${OK_PCT:-N/A} |
| **Business errors** | ${ERR_PCT:-N/A} |
| **Total iterations** | ${ITERS:-N/A} |

## GC Runtime Metrics (sampled every 5s during load)

| Metric | Min | Max | Avg |
|--------|-----|-----|-----|
$(fmt_stat "$GC_PAUSE_MAX_STATS" "GC pause max (seconds)")
$(fmt_stat "$GC_PAUSE_P75_STATS" "GC pause p75 (seconds)")
$(fmt_stat "$HEAP_INUSE_STATS" "Heap in-use (bytes)")
$(fmt_stat "$HEAP_ALLOC_STATS" "Heap alloc (bytes)")
$(fmt_stat "$GOROUTINE_STATS" "Goroutines")
$(fmt_stat "$MALLOCS_STATS" "Mallocs total")
$(fmt_stat "$FREES_STATS" "Frees total")
$(fmt_stat "$GC_CYCLES_STATS" "GC cycles total")

## Profiling Artifacts

| File | Description |
|------|-------------|
| [gc_metrics.csv](gc_metrics.csv) | Go runtime metrics time series |
| [pprof/heap.pb.gz](pprof/heap.pb.gz) | Heap profile (in-use objects) |
| [pprof/allocs.pb.gz](pprof/allocs.pb.gz) | Allocation profile (30s sample) |
| [pprof/goroutine.pb.gz](pprof/goroutine.pb.gz) | Goroutine stack dump |
| [run_raw.txt](run_raw.txt) | Raw k6 output |

### Analyze with:

\`\`\`bash
go tool pprof -top pprof/allocs.pb.gz     # top allocation sites
go tool pprof -top pprof/heap.pb.gz       # top heap consumers
go tool pprof -web pprof/allocs.pb.gz     # interactive graph in browser
\`\`\`

$(cat "$REPORT_DIR/environment.md")
REPORT

echo ""
echo "============================================"
echo "  Benchmark complete! Report: $REPORT_DIR/report.md"
echo "============================================"
echo ""
head -50 "$REPORT_DIR/report.md"
