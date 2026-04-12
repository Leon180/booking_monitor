#!/bin/bash
# gc_metrics.sh — Collects Go runtime / GC metrics from the app's
# Prometheus /metrics endpoint at regular intervals during a load test.
#
# Usage: ./scripts/gc_metrics.sh [output_file] [interval_seconds] [metrics_url]
#
# Runs until killed (Ctrl-C or kill). Designed to be started in the
# background before a k6 run and killed afterwards.

set -euo pipefail

OUTPUT="${1:-gc_metrics.csv}"
INTERVAL="${2:-5}"
METRICS_URL="${3:-http://localhost:8080/metrics}"

# If running inside Docker network, the app is at app:8080.
# From the host, use localhost:8080 via the nginx proxy (which forwards
# /metrics to the upstream). Alternatively, expose app:8080 directly.
# For benchmarks run from the host against docker-compose, we go through
# the app container directly:
if [ "$METRICS_URL" = "http://localhost:8080/metrics" ]; then
    # Try to reach app directly via docker exec
    FETCH_CMD="docker exec booking_app wget -qO- http://localhost:8080/metrics"
else
    FETCH_CMD="curl -sf $METRICS_URL"
fi

# CSV header
echo "timestamp,gc_pause_max_sec,gc_pause_p75_sec,heap_inuse_bytes,heap_alloc_bytes,goroutines,mallocs_total,frees_total,gc_cycles_total" > "$OUTPUT"

echo "[gc_metrics] Collecting every ${INTERVAL}s → $OUTPUT (Ctrl-C to stop)"

while true; do
    TS=$(date +%s)

    RAW=$($FETCH_CMD 2>/dev/null || echo "")
    if [ -z "$RAW" ]; then
        echo "[gc_metrics] WARNING: failed to fetch metrics at $TS"
        sleep "$INTERVAL"
        continue
    fi

    GC_MAX=$(echo "$RAW"   | grep 'go_gc_duration_seconds{quantile="1"}'    | awk '{print $2}')
    GC_P75=$(echo "$RAW"   | grep 'go_gc_duration_seconds{quantile="0.75"}' | awk '{print $2}')
    HEAP_INUSE=$(echo "$RAW" | grep '^go_memstats_heap_inuse_bytes '         | awk '{print $2}')
    HEAP_ALLOC=$(echo "$RAW" | grep '^go_memstats_heap_alloc_bytes '         | awk '{print $2}')
    GOROUTINES=$(echo "$RAW" | grep '^go_goroutines '                        | awk '{print $2}')
    MALLOCS=$(echo "$RAW"    | grep '^go_memstats_mallocs_total '            | awk '{print $2}')
    FREES=$(echo "$RAW"      | grep '^go_memstats_frees_total '              | awk '{print $2}')
    GC_CYCLES=$(echo "$RAW"  | grep '^go_gc_duration_seconds_count '         | awk '{print $2}')

    echo "$TS,$GC_MAX,$GC_P75,$HEAP_INUSE,$HEAP_ALLOC,$GOROUTINES,$MALLOCS,$FREES,$GC_CYCLES" >> "$OUTPUT"

    sleep "$INTERVAL"
done
