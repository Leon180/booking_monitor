#!/usr/bin/env bash
# D8 smoke — verify the CORS preflight contract end-to-end so the
# browser demo (Vite dev server on :5173) can talk to the API.
#
# What this checks:
#   1. OPTIONS preflight from a whitelisted origin returns 204 with
#      Access-Control-Allow-{Origin,Methods,Headers} echoed.
#   2. Vary header lists Origin (and the request-method/headers
#      negotiation pair) so intermediate caches don't poison.
#   3. Disallowed origin gets NO Access-Control-Allow-Origin (the
#      browser then rejects the response per spec).
#   4. Same-origin / no-Origin requests still flow through (Vary set,
#      no Allow-Origin emitted).
#
# Usage: docker compose up -d (with CORS_ALLOWED_ORIGINS set in env)
#        scripts/d8_smoke.sh
#
# API_ORIGIN defaults to http://localhost (nginx on host port 80) —
# the only host-published surface in docker-compose.yml. The `app`
# service publishes pprof:6060 only, NOT 8080, so a smoke targeting
# :8080 from the host would fail to connect (Codex round-1 P2).
# nginx forwards /api/* + /livez + OPTIONS verbatim to the upstream
# Go app, so the CORS middleware runs identically. Override
# API_ORIGIN=http://localhost:8080 if you're running the Go binary
# directly on the host (`make run-server`) instead of via compose.
#
# Codex round-4 execution note: header grep is case-insensitive
# (`-i`) and CRLF-tolerant (`^Header:` anchored, `\r?$` implied
# because `grep` matches line-by-line on `curl -i` output where
# CRLF is the wire format).

set -euo pipefail

API_ORIGIN="${API_ORIGIN:-http://localhost}"
ALLOWED_ORIGIN="${ALLOWED_ORIGIN:-http://localhost:5173}"
DISALLOWED_ORIGIN="${DISALLOWED_ORIGIN:-http://evil.example}"

log() { printf '[d8_smoke] %s\n' "$*" >&2; }

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || { log "missing cmd: $1"; exit 1; }
}
require_cmd curl
require_cmd grep

# 1. Preflight from allowed origin must succeed and echo headers.
log "(1/4) preflight from allowed origin ${ALLOWED_ORIGIN}"
preflight_resp=$(curl -sS -i -X OPTIONS \
    -H "Origin: ${ALLOWED_ORIGIN}" \
    -H "Access-Control-Request-Method: POST" \
    -H "Access-Control-Request-Headers: Content-Type, Idempotency-Key" \
    "${API_ORIGIN}/api/v1/book")

# HTTP/1.1 204 — preflight short-circuits with no body
echo "${preflight_resp}" | grep -qiE '^HTTP/[0-9.]+ 204' \
    || { log "FAIL: preflight did not return 204"; echo "${preflight_resp}"; exit 1; }

echo "${preflight_resp}" | grep -qi "^Access-Control-Allow-Origin: ${ALLOWED_ORIGIN}" \
    || { log "FAIL: missing/wrong Access-Control-Allow-Origin"; echo "${preflight_resp}"; exit 1; }

# Methods echo must include POST (the demo's actual call) — Codex
# round-2 P2 raised this; spec mandates ACAM on preflight.
echo "${preflight_resp}" | grep -qi "^Access-Control-Allow-Methods:.*POST" \
    || { log "FAIL: missing POST in Access-Control-Allow-Methods"; echo "${preflight_resp}"; exit 1; }

# Headers echo must include both Content-Type AND Idempotency-Key.
# Browser rejects the actual POST otherwise (gateway sends Idempotency-Key
# from the demo client; without echo, fetch() throws CORS error).
echo "${preflight_resp}" | grep -qi "^Access-Control-Allow-Headers:.*Content-Type" \
    || { log "FAIL: missing Content-Type in Access-Control-Allow-Headers"; echo "${preflight_resp}"; exit 1; }
echo "${preflight_resp}" | grep -qi "^Access-Control-Allow-Headers:.*Idempotency-Key" \
    || { log "FAIL: missing Idempotency-Key in Access-Control-Allow-Headers"; echo "${preflight_resp}"; exit 1; }

# 2. Vary contract — needed even when request matched, otherwise
#    proxies cache one origin's preflight for another.
echo "${preflight_resp}" | grep -qi "^Vary:.*Origin" \
    || { log "FAIL: missing Vary: Origin"; echo "${preflight_resp}"; exit 1; }
log "(2/4) Vary contract ok"

# 3. Disallowed origin: NO Allow-Origin (browser will reject).
log "(3/4) preflight from disallowed origin ${DISALLOWED_ORIGIN}"
disallowed_resp=$(curl -sS -i -X OPTIONS \
    -H "Origin: ${DISALLOWED_ORIGIN}" \
    -H "Access-Control-Request-Method: POST" \
    "${API_ORIGIN}/api/v1/book")

if echo "${disallowed_resp}" | grep -qi "^Access-Control-Allow-Origin:"; then
    log "FAIL: Allow-Origin echoed for disallowed origin (allow-list bypass)"
    echo "${disallowed_resp}"
    exit 1
fi

# Vary is still emitted on every response (cache-correctness invariant).
echo "${disallowed_resp}" | grep -qi "^Vary:.*Origin" \
    || { log "FAIL: Vary missing on disallowed-origin response"; echo "${disallowed_resp}"; exit 1; }

# 4. Same-origin flow (no Origin header) — health probe must answer
#    normally and STILL emit Vary: Origin so proxy caches stay
#    correct if a same-host browser hits this URL later with an
#    Origin. This is the cache-correctness invariant the unit
#    test TestCORS_VaryOrigin_EveryResponse encodes; promoting it
#    to a hard failure here keeps the smoke honest about what it
#    actually verifies (it'd be misleading to call it an
#    "invariant" while only WARN'ing).
#
# NOTE: this check requires CORS_ALLOWED_ORIGINS to be non-empty
# in the running container (otherwise the middleware is a no-op).
# The demo's compose env sets it to localhost:5173 + 127.0.0.1:5173.
log "(4/4) same-origin GET /livez (Vary: Origin must be present)"
same_origin_resp=$(curl -sS -i "${API_ORIGIN}/livez")
echo "${same_origin_resp}" | grep -qiE '^HTTP/[0-9.]+ 200' \
    || { log "FAIL: /livez did not return 200"; echo "${same_origin_resp}"; exit 1; }
echo "${same_origin_resp}" | grep -qi "^Vary:.*Origin" \
    || { log "FAIL: Vary: Origin missing on no-Origin response (cache-correctness invariant)"; echo "${same_origin_resp}"; exit 1; }

log "PASS — D8 CORS contract verified"
