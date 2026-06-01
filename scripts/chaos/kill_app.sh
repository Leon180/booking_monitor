#!/usr/bin/env bash
# Scenario A — Kill booking_app container.
#
# Hypothesis: docker compose restart policy brings it back within 30s;
# /readyz returns 200 again. Brief window (~30s) of /livez 5xx is
# acceptable — fits within 30d availability SLO's 216 min budget.
#
# Pre-experiment: SLO budget healthy, Grafana dashboard open, stopwatch ready.
# See docs/runbooks/chaos.md § Scenario A.

set -euo pipefail

CONTAINER="${CONTAINER:-booking_app}"
# Probe via nginx (port 80), NOT app:8080 directly — the compose `app`
# service only publishes :6060 (pprof). External health-checks must go
# through nginx, matching the production reachability path.
HEALTH_URL="${HEALTH_URL:-http://localhost/readyz}"
RECOVERY_TARGET="${RECOVERY_TARGET:-30}" # seconds

printf '\033[1mScenario A — Kill app\033[0m\n'
printf 'Hypothesis: %s recovers within %ds and %s returns 200.\n' \
  "$CONTAINER" "$RECOVERY_TARGET" "$HEALTH_URL"
printf 'Watch:\n'
printf '  - Grafana: p99 latency, 5xx rate, /readyz status\n'
printf '  - booking:availability:slo_budget_remaining should dip but stay positive\n\n'

# Pre-flight: confirm steady state
if ! curl -fsS --max-time 3 "$HEALTH_URL" > /dev/null; then
  echo "ABORT: pre-flight $HEALTH_URL is not 200 — system isn't in steady state."
  exit 1
fi
printf 'Pre-flight: %s = 200 ✓\n\n' "$HEALTH_URL"

read -rp "Type INJECT to proceed: " confirm
[[ "$confirm" == "INJECT" ]] || { echo "aborted"; exit 1; }

T0=$(date -u +%s)
# `docker restart --timeout=0` instead of `docker kill`:
#   docker treats `docker kill` as an explicit-user-stop, so
#   `restart: unless-stopped` does NOT auto-restart afterwards
#   (empirically confirmed on Docker 29.4.0). `docker restart
#   --timeout=0` sends SIGKILL + brings the container back in one
#   atomic operation — same chaos effect (process dies abruptly),
#   no dependency on the daemon's restart policy.
echo "t=0 ($(date -u +%H:%M:%SZ)): restarting $CONTAINER (SIGKILL + restart)"
docker restart --timeout=0 "$CONTAINER" > /dev/null

echo "Waiting for recovery (target: ${RECOVERY_TARGET}s)..."
RECOVERED_AT=""
for ((i=1; i<=120; i++)); do
  if curl -fsS --max-time 2 "$HEALTH_URL" > /dev/null 2>&1; then
    RECOVERED_AT=$(date -u +%s)
    break
  fi
  sleep 1
done

if [[ -z "$RECOVERED_AT" ]]; then
  echo "FAIL: /readyz never recovered within 120s. Manual intervention required."
  exit 2
fi

ELAPSED=$((RECOVERED_AT - T0))
echo ""
echo "Recovery: ${ELAPSED}s elapsed"
if (( ELAPSED <= RECOVERY_TARGET )); then
  printf '\033[32mPASS\033[0m: %ds <= %ds target\n' "$ELAPSED" "$RECOVERY_TARGET"
else
  printf '\033[33mDEGRADED\033[0m: %ds > %ds target — investigate restart policy + GC pause time\n' \
    "$ELAPSED" "$RECOVERY_TARGET"
fi

echo ""
echo "Document the experiment: write a row to docs/chaos-log/$(date -u +%Y-%m-%d)-kill-app.md"
