#!/bin/bash
# pprof_capture.sh — Captures heap, allocs, and goroutine profiles from
# the pprof server during a load test.
#
# Usage: ./scripts/pprof_capture.sh [output_dir] [delay_seconds] [pprof_url]
#
# Waits [delay_seconds] (default 30) to let the load test warm up, then
# captures three profiles. The allocs profile is a 30-second CPU-style
# allocation sample.

set -euo pipefail

OUTPUT_DIR="${1:-pprof}"
DELAY="${2:-30}"
PPROF_URL="${3:-http://localhost:6060}"

mkdir -p "$OUTPUT_DIR"

echo "[pprof] Waiting ${DELAY}s for load test to warm up..."
sleep "$DELAY"

echo "[pprof] Capturing heap profile..."
curl -sf -o "$OUTPUT_DIR/heap.pb.gz" "$PPROF_URL/debug/pprof/heap" || {
    echo "[pprof] ERROR: failed to capture heap profile. Is ENABLE_PPROF=true?"
    exit 1
}

echo "[pprof] Capturing 30s allocs profile..."
curl -sf -o "$OUTPUT_DIR/allocs.pb.gz" "$PPROF_URL/debug/pprof/allocs?seconds=30" || {
    echo "[pprof] ERROR: failed to capture allocs profile"
    exit 1
}

echo "[pprof] Capturing goroutine profile..."
curl -sf -o "$OUTPUT_DIR/goroutine.pb.gz" "$PPROF_URL/debug/pprof/goroutine" || {
    echo "[pprof] ERROR: failed to capture goroutine profile"
    exit 1
}

echo "[pprof] Done. Profiles saved to $OUTPUT_DIR/"
ls -lh "$OUTPUT_DIR"/*.pb.gz

echo ""
echo "Analyze with:"
echo "  go tool pprof -top $OUTPUT_DIR/allocs.pb.gz"
echo "  go tool pprof -top $OUTPUT_DIR/heap.pb.gz"
echo "  go tool pprof -web $OUTPUT_DIR/allocs.pb.gz  # opens browser"
