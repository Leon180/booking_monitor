#!/usr/bin/env bash
# scripts/profile_saturation.sh — One-shot saturation diagnostic.
#
# The companion to scripts/benchmark_compare.sh: while benchmark_compare
# answers "did this PR change throughput?", this script answers
# "WHY does the system saturate where it does?" — the question that
# B3 (inventory sharding, withdrawn) and PR #69 closing-comment
# explicitly flagged as the prerequisite for any optimization work.
#
# Outputs to docs/saturation-profile/<YYYYMMDD_HHMMSS>/:
#   ├── README.md              — auto-generated bottleneck analysis
#   ├── cpu.pprof              — 30s CPU profile of the booking-cli app
#   ├── heap.pprof             — heap snapshot (in-use bytes)
#   ├── goroutine.pprof        — goroutine count + stacks
#   ├── commandstats_before.txt
#   ├── commandstats_during.txt — diff = where Redis spent its time
#   ├── commandstats_diff.txt   — auto-computed (during - before)
#   ├── slowlog.txt            — Redis SLOWLOG GET 100
#   ├── pool_metrics.json      — redis_client_pool_* snapshot at peak
#   ├── prom_signals.json      — RED + USE signals from Prometheus
#   └── k6_summary.txt         — load-generator output
#
# Usage:
#   make profile-saturation                       # defaults: VUS=500 DURATION=60s
#   make profile-saturation VUS=1000 DURATION=90s
#
# Requirements: docker compose stack up (the script will up missing
# services), `k6` on PATH (or k6 docker image — fallback).
#
# This script does NOT modify the codebase or Prometheus config. It
# observes the running system for the duration of one k6 saturation run.

set -euo pipefail

# ── Args + paths ─────────────────────────────────────────────────────────────

VUS="${VUS:-500}"
DURATION="${DURATION:-60s}"
WARMUP_SECONDS="${WARMUP_SECONDS:-10}"
PPROF_SECONDS="${PPROF_SECONDS:-30}"

TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
OUT_DIR="docs/saturation-profile/${TIMESTAMP}_c${VUS}"
mkdir -p "$OUT_DIR"

K6_SCRIPT="$(pwd)/scripts/k6_comparison.js"
# pprof binds to container loopback (127.0.0.1:6060 inside the app
# container — see PROJECT_SPEC: PPROF_ADDR default is loopback-only for
# security). We reach it via `docker compose exec app wget` which runs
# inside the container's network namespace; no need to make the user
# flip PPROF_ADDR=0.0.0.0:6060 just to profile.
PPROF_BASE="http://127.0.0.1:6060"
PROM_URL="${PROM_URL:-http://localhost:9090}"
APP_HOST_BASE="${APP_HOST_BASE:-http://localhost}"

# pprof_get <output_path> <pprof_path>
# Fetches a pprof endpoint from inside the app container. wget's
# BusyBox build (alpine) handles binary stdout via -O- correctly.
pprof_get() {
  local out_path="$1"
  local pprof_path="$2"
  docker compose exec -T app wget -q -O- "${PPROF_BASE}${pprof_path}" > "$out_path"
}

# Redis password — read from .env if present (matches other scripts).
REDIS_PASSWORD="${REDIS_PASSWORD:-$(grep -E '^REDIS_PASSWORD=' .env 2>/dev/null | cut -d= -f2 || echo '')}"

echo "================================================================"
echo "  Saturation profile run"
echo "  VUS=$VUS  DURATION=$DURATION  warmup=${WARMUP_SECONDS}s  pprof_window=${PPROF_SECONDS}s"
echo "  Output: $OUT_DIR"
echo "================================================================"

# ── Pre-flight ──────────────────────────────────────────────────────────────

# Compose services we need up. The stack is allowed to be partially up
# (e.g. user just brought up app + redis); we assert the rest exist.
required_services=(app redis prometheus redis_exporter)
for svc in "${required_services[@]}"; do
  if ! docker compose ps "$svc" --format '{{.Service}}' 2>/dev/null | grep -q "^${svc}$"; then
    echo "[pre] Bringing up missing service: $svc"
    docker compose up -d "$svc" >/dev/null
  fi
done

# Sanity: pprof must be enabled inside the app container. Without
# ENABLE_PPROF=true the listener is not bound and the CPU profile
# capture below would silently fail.
if ! docker compose exec -T app wget -q -O- "${PPROF_BASE}/debug/pprof/" >/dev/null 2>&1; then
  echo "[pre] ERROR: pprof not reachable inside the app container at ${PPROF_BASE}"
  echo "       Set ENABLE_PPROF=true in .env and restart the app:"
  echo "         docker compose up -d --force-recreate app"
  exit 1
fi

# k6 always runs in docker — k6_comparison.js hardcodes
# `http://app:8080` which only resolves on the compose network. This
# matches benchmark_compare.sh's invocation; do not switch to local k6
# without also fixing the script's BASE_URL.
if ! docker image ls grafana/k6 --format '{{.Repository}}' | grep -q '^grafana/k6$'; then
  echo "[pre] WARN: grafana/k6 image not present locally. Pulling..."
  docker pull grafana/k6 >/dev/null
fi

# ── Reset state (mirror benchmark_compare.sh) ───────────────────────────────

echo "[step 1/6] Reset state (FLUSHALL + truncate orders)"
docker compose exec -T redis redis-cli ${REDIS_PASSWORD:+-a "$REDIS_PASSWORD"} --no-auth-warning FLUSHALL >/dev/null
docker compose exec -T postgres psql -U booking -d booking -c \
  'TRUNCATE orders, order_status_history, events_outbox, events RESTART IDENTITY CASCADE;' >/dev/null

# Seed one big event so k6_comparison.js has inventory to deduct.
SEED_EVENT_ID=$(curl -sS -X POST "$APP_HOST_BASE/api/v1/events" \
  -H 'Content-Type: application/json' \
  -d '{"name":"saturation profile","total_tickets":500000}' | jq -r '.id')
echo "  seeded event_id=$SEED_EVENT_ID with 500k tickets"

# ── Capture BASELINE state ──────────────────────────────────────────────────

echo "[step 2/6] Capture baseline Redis commandstats (pre-load)"
docker compose exec -T redis redis-cli ${REDIS_PASSWORD:+-a "$REDIS_PASSWORD"} --no-auth-warning \
  INFO commandstats > "$OUT_DIR/commandstats_before.txt"

# ── Launch k6 in background, capture pprof + Redis state during peak ─────────

echo "[step 3/6] Launch k6 (background) — VUS=$VUS DURATION=$DURATION"
# k6 stdout+stderr → summary file. Run on the compose network so the
# hardcoded http://app:8080 in k6_comparison.js resolves.
K6_CMD=(docker run --rm -i --network booking_monitor_default
        -v "$(pwd)/scripts:/scripts:ro"
        -e VUS="$VUS"
        -e DURATION="$DURATION"
        grafana/k6 run /scripts/k6_comparison.js)

"${K6_CMD[@]}" > "$OUT_DIR/k6_summary.txt" 2>&1 &
K6_PID=$!

echo "  k6 running as pid $K6_PID, sleeping ${WARMUP_SECONDS}s for warmup..."
sleep "$WARMUP_SECONDS"

# ── During-saturation captures (parallel) ───────────────────────────────────

echo "[step 4/6] Capture during-saturation diagnostics (pprof + Redis + Prom)"

# 4a. CPU profile — blocks for $PPROF_SECONDS.
( pprof_get "$OUT_DIR/cpu.pprof" "/debug/pprof/profile?seconds=$PPROF_SECONDS" \
    || echo "[warn] CPU profile capture failed" ) &
CPU_PID=$!

# 4b. heap + goroutine — instantaneous.
pprof_get "$OUT_DIR/heap.pprof" "/debug/pprof/heap" \
  || echo "[warn] heap profile capture failed"
pprof_get "$OUT_DIR/goroutine.pprof" "/debug/pprof/goroutine" \
  || echo "[warn] goroutine profile capture failed"

# Wait for the CPU profile window to elapse before taking Redis +
# Prometheus snapshots — otherwise the [1m] rate windows in Prometheus
# only see warmup-time load (the saturation hasn't existed for a full
# minute yet) and report misleadingly low values. The CPU profile takes
# $PPROF_SECONDS by design; the other captures happen at the END of
# that window when load has been steady-state longest.
wait "$CPU_PID" 2>/dev/null || true

# 4c. Redis state during peak — INFO commandstats + SLOWLOG.
docker compose exec -T redis redis-cli ${REDIS_PASSWORD:+-a "$REDIS_PASSWORD"} --no-auth-warning \
  INFO commandstats > "$OUT_DIR/commandstats_during.txt"
docker compose exec -T redis redis-cli ${REDIS_PASSWORD:+-a "$REDIS_PASSWORD"} --no-auth-warning \
  SLOWLOG GET 100 > "$OUT_DIR/slowlog.txt"

# 4d. Prometheus snapshot — the headline RED + USE signals.
PROM_QUERIES=(
  "redis_up"
  "rate(redis_cpu_sys_seconds_total[1m])+rate(redis_cpu_user_seconds_total[1m])"
  "redis_client_pool_total_conns"
  "redis_client_pool_idle_conns"
  "rate(redis_client_pool_hits_total[1m])"
  "rate(redis_client_pool_misses_total[1m])"
  "rate(redis_client_pool_timeouts_total[1m])"
  "rate(redis_client_pool_wait_duration_seconds_total[1m])"
  "pg_pool_in_use"
  "rate(pg_pool_wait_count_total[1m])"
  "rate(pg_pool_wait_duration_seconds_total[1m])"
  "go_goroutines"
  "rate(http_requests_total[1m])"
  "histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket[1m])) by (le))"
  "rate(bookings_total{status=\"success\"}[1m])"
)
{
  echo '{'
  first=1
  for q in "${PROM_QUERIES[@]}"; do
    [ $first -eq 1 ] && first=0 || echo ','
    val=$(curl -sS --data-urlencode "query=$q" "$PROM_URL/api/v1/query" \
            | jq -c '.data.result[0].value[1] // null' 2>/dev/null || echo 'null')
    printf '  %s: %s' "\"$q\"" "$val"
  done
  echo
  echo '}'
} > "$OUT_DIR/prom_signals.json"

# ── Wait for k6 to finish ───────────────────────────────────────────────────

echo "[step 5/6] Waiting for k6 to finish..."
wait "$K6_PID" || echo "  k6 exit non-zero (continuing — partial data is still useful)"

# ── Compute commandstats diff + write README.md ─────────────────────────────

echo "[step 6/6] Computing diffs + writing analysis README"

# commandstats_diff.txt: per-cmd (calls_during - calls_before) + (usec_during - usec_before).
# Format of INFO commandstats lines:
#   cmdstat_set:calls=12345,usec=678,usec_per_call=0.054,...
# Parsed in python because awk's float arithmetic + sort-by-numeric is
# painful when several columns have wildly different magnitudes.
if command -v python3 >/dev/null 2>&1; then
  python3 scripts/_commandstats_diff.py \
    "$OUT_DIR/commandstats_before.txt" \
    "$OUT_DIR/commandstats_during.txt" \
    > "$OUT_DIR/commandstats_diff.txt"
else
  echo "python3 not available — open commandstats_before.txt and commandstats_during.txt manually." \
    > "$OUT_DIR/commandstats_diff.txt"
fi

# README — auto-generated bottleneck analysis. Reads pool_metrics.json
# and surfaces signals that point at specific causes.
cat > "$OUT_DIR/README.md" <<EOF
# Saturation profile — ${TIMESTAMP}

Captured under: \`VUS=$VUS DURATION=$DURATION\` against k6_comparison.js (500k tickets).
pprof window: ${PPROF_SECONDS}s starting ${WARMUP_SECONDS}s after k6 launch.

## Files

| File | What it tells you |
| :-- | :-- |
| \`cpu.pprof\` | Where the Go app spent its CPU during peak. \`go tool pprof -top cpu.pprof\` |
| \`heap.pprof\` | In-use bytes by allocation site. \`go tool pprof -top heap.pprof\` |
| \`goroutine.pprof\` | Goroutine count + stacks. \`go tool pprof -top goroutine.pprof\` |
| \`commandstats_diff.txt\` | Top Redis commands by total μs spent during the window — the "what was Redis doing" answer |
| \`slowlog.txt\` | Any single command that took >10ms. Empty = no individual slow op |
| \`pool_metrics.json\` (n/a — see prom_signals.json) | Client-side connection-pool snapshot at peak |
| \`prom_signals.json\` | RED + USE signal snapshot (Redis CPU, pool ops/sec, p99 latency, Postgres pool) |
| \`k6_summary.txt\` | Headline throughput + latency from the load generator |

## How to read this

The decision tree for "what's the bottleneck":

1. **\`prom_signals.json\` → \`rate(redis_cpu_sys_seconds_total[1m]) + rate(redis_cpu_user_seconds_total[1m])\`**
   - Sustained > 0.8 → Redis main thread CPU is saturated. **Optimize Redis-side: io-threads, EVALSHA, pipelining.**
   - Sustained < 0.3 → Redis is NOT the bottleneck. Look elsewhere.
2. **\`prom_signals.json\` → \`rate(redis_client_pool_misses_total[1m])\` + \`rate(redis_client_pool_timeouts_total[1m])\`**
   - Misses sustained > 0 → PoolSize is too small. Bump it.
   - Timeouts > 0 → pool fully exhausted, this is a hard saturation signal.
3. **\`prom_signals.json\` → \`rate(pg_pool_wait_duration_seconds_total[1m])\`**
   - Sustained > 0 → Postgres connection pool is queueing. Check \`pg_pool_in_use\` vs configured max.
4. **\`commandstats_diff.txt\` top row**
   - \`evalsha_*\` or \`eval\` dominating → Lua scripts are the cost driver. Confirm with cpu.pprof.
   - \`xadd\` / \`xreadgroup\` dominating → stream operations dominate (worker side).
5. **\`cpu.pprof\` top samples**
   - \`runtime.gc*\` heavy → GC pressure; check heap.pprof for allocation churn.
   - \`syscall.*\` heavy → I/O bound (network or disk).
   - Application code dominating → application-level hotspot, profile it.

## Auto-generated signals

Pulled from \`prom_signals.json\` at the end of the saturation window:

EOF

# Append the actual numbers from prom_signals.json into the README so a
# reader doesn't have to context-switch to JSON.
{
  echo '```json'
  cat "$OUT_DIR/prom_signals.json"
  echo '```'
} >> "$OUT_DIR/README.md"

cat >> "$OUT_DIR/README.md" <<'EOF'

## Next-step prompt

Open `cpu.pprof` first:

```
go tool pprof -http=:0 cpu.pprof    # opens browser flame graph
go tool pprof -top cpu.pprof        # top-N text view
```

Cross-reference top samples with `commandstats_diff.txt` and the Redis/pool signals above. The conclusion belongs in this README — append a "Findings" section once you've read the profile.
EOF

echo ""
echo "================================================================"
echo "  DONE. Profile saved to: $OUT_DIR"
echo "================================================================"
echo ""
echo "Quick start:"
echo "  cat $OUT_DIR/README.md"
echo "  go tool pprof -http=:0 $OUT_DIR/cpu.pprof"
echo "  cat $OUT_DIR/commandstats_diff.txt"
