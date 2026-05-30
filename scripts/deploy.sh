#!/usr/bin/env bash
# deploy.sh — operator-side deploy orchestrator.
#
# Pulls a signed image from Artifact Registry, runs schema migrations,
# rolls the docker compose stack on the VM, smoke-tests it. If smoke
# fails, rolls back to the previous image tag.
#
# This is for HUMAN operators. PR 5 will wrap similar logic in a GH
# Actions workflow using `google-github-actions/ssh-compute@v2`.
#
# Usage (from operator machine):
#   make deploy IMAGE_TAG=v1.0.0
# Or:
#   ./scripts/deploy.sh v1.0.0
#
# Prereqs (one-time, see docs/runbooks/deploy.md):
#   * gcloud authenticated, ADC + active project set
#   * roles/iap.tunnelResourceAccessor on operator user
#   * roles/iam.serviceAccountTokenCreator on operator (impersonate ci-deploy SA)
#   * Cloudflare Tunnel set up on VM per docs/runbooks/cloudflare_tunnel.md
#   * `cosign` CLI installed locally for the pre-flight verification step

set -euo pipefail

# ---- Config (overridable via env) ----
PROJECT_ID="${PROJECT_ID:-booking-monitor-sandbox}"
ZONE="${ZONE:-us-central1-a}"
VM_NAME="${VM_NAME:-booking-app}"
REGISTRY="${GCP_ARTIFACT_REGISTRY:-us-central1-docker.pkg.dev/${PROJECT_ID}/booking}"
IMAGE_NAME="${IMAGE_NAME:-booking-monitor}"
VM_PROJECT_DIR="${VM_PROJECT_DIR:-/opt/booking-monitor}"
REPO_URL="${REPO_URL:-https://github.com/Leon180/booking_monitor.git}"

# Image tag we're deploying. Required as $1.
IMAGE_TAG="${1:-}"
if [ -z "$IMAGE_TAG" ]; then
  echo "Usage: $0 <image-tag>"
  echo "Example: $0 v1.0.0   # or   $0 main   # or   $0 sha-abc1234"
  exit 1
fi

IMAGE_REF="${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"

# Identity regex for cosign verify — matches PR 3's Makefile.
COSIGN_IDENTITY_REGEX='^https://github\.com/Leon180/booking_monitor/\.github/workflows/release\.yml@refs/(heads/main|tags/v.*)$'
COSIGN_ISSUER='https://token.actions.githubusercontent.com'

# ============================================================================
# 0. Pre-flight verification (LOCAL, before touching VM)
# ============================================================================
echo "===> [0/6] Verifying image signature + SLSA L3 attestation locally"
command -v cosign >/dev/null || {
  echo "  ✗ cosign CLI not installed. brew install cosign"; exit 2
}

# Resolve tag → digest first; we deploy by digest (immutable) to avoid
# tag-mutation races (a tag is mutable; a digest is content-addressed).
DIGEST=$(gcloud artifacts docker images describe "$IMAGE_REF" \
  --format='value(image_summary.digest)' 2>/dev/null) || {
  echo "  ✗ image not found in registry: $IMAGE_REF"; exit 3
}
IMAGE_BY_DIGEST="${REGISTRY}/${IMAGE_NAME}@${DIGEST}"
echo "  → ${IMAGE_REF}"
echo "  → ${IMAGE_BY_DIGEST}"

# Verify SLSA L3 provenance attestation (PR 3 emits this).
cosign verify-attestation \
  --type slsaprovenance1 \
  --certificate-identity-regexp "$COSIGN_IDENTITY_REGEX" \
  --certificate-oidc-issuer "$COSIGN_ISSUER" \
  "$IMAGE_BY_DIGEST" > /dev/null
echo "  ✓ SLSA L3 provenance verified"

# Verify SBOM attestation.
cosign verify-attestation \
  --type spdxjson \
  --certificate-identity-regexp "$COSIGN_IDENTITY_REGEX" \
  --certificate-oidc-issuer "$COSIGN_ISSUER" \
  "$IMAGE_BY_DIGEST" > /dev/null
echo "  ✓ SBOM attestation verified"

# ============================================================================
# 1. Capture current-deployed image (for rollback)
# ============================================================================
echo "===> [1/6] Capturing currently-deployed image (for rollback if smoke fails)"
PREVIOUS_DIGEST=$(gcloud compute ssh "$VM_NAME" \
  --zone="$ZONE" --tunnel-through-iap --quiet \
  --command="docker inspect ${IMAGE_NAME} --format='{{.RepoDigests}}' 2>/dev/null | tr -d '[]' || echo none" 2>/dev/null || echo "none")
echo "  current: ${PREVIOUS_DIGEST}"

# ============================================================================
# 2. Sync repo on VM to deployed commit (needed for migrations + compose files)
# ============================================================================
echo "===> [2/6] Syncing repo on VM to deployed commit"

# Determine the source git commit from the image OCI label.
DEPLOYED_COMMIT=$(gcloud artifacts docker images describe "$IMAGE_REF" \
  --format='value(image_summary.labels)' 2>/dev/null | \
  grep -oP 'org\.opencontainers\.image\.revision=\K[a-f0-9]+' | head -1) || true

if [ -z "${DEPLOYED_COMMIT:-}" ]; then
  echo "  ✗ could not extract commit SHA from image labels; check Dockerfile OCI labels (PR 2)"
  exit 4
fi
echo "  deploying commit: ${DEPLOYED_COMMIT}"

gcloud compute ssh "$VM_NAME" \
  --zone="$ZONE" --tunnel-through-iap --quiet \
  --command="sudo mkdir -p ${VM_PROJECT_DIR} && sudo chown -R \$(whoami): ${VM_PROJECT_DIR} && \
    if [ ! -d ${VM_PROJECT_DIR}/.git ]; then \
      git clone ${REPO_URL} ${VM_PROJECT_DIR}; \
    fi && \
    cd ${VM_PROJECT_DIR} && \
    git fetch --quiet origin && \
    git checkout --quiet ${DEPLOYED_COMMIT}"
echo "  ✓ VM repo at ${DEPLOYED_COMMIT}"

# ============================================================================
# 3. Sync secrets (idempotent — pulls latest version of each)
# ============================================================================
echo "===> [3/6] Syncing secrets from Secret Manager to VM .env"
gcloud compute ssh "$VM_NAME" \
  --zone="$ZONE" --tunnel-through-iap --quiet \
  --command="sudo bash ${VM_PROJECT_DIR}/scripts/secrets_sync.sh"

# ============================================================================
# 4. Run schema migrations via Docker (containerized migrate runner)
# ============================================================================
echo "===> [4/6] Running schema migrations"
# Uses migrate/migrate:v4.19.1 (PR 4 fact-check verified current stable).
# Mounts migrations dir read-only; sources DATABASE_URL from synced .env.
# Idempotent: re-running on up-to-date schema is a no-op (exit 0).
gcloud compute ssh "$VM_NAME" \
  --zone="$ZONE" --tunnel-through-iap --quiet \
  --command="cd ${VM_PROJECT_DIR} && set -a && source .env && set +a && \
    docker run --rm --network=host \
      -v ${VM_PROJECT_DIR}/deploy/postgres/migrations:/migrations:ro \
      migrate/migrate:v4.19.1 \
      -path /migrations -database \"\$DATABASE_URL\" up"
echo "  ✓ migrations applied"

# ============================================================================
# 5. Pull image + restart compose stack
# ============================================================================
echo "===> [5/6] Pulling ${IMAGE_BY_DIGEST} + rolling compose stack"
# `docker compose up --wait --wait-timeout 120` blocks until healthchecks
# pass OR 120s timeout. Per PR 4 fact-check: --wait without explicit timeout
# can hang the deploy forever on a stuck healthcheck.
gcloud compute ssh "$VM_NAME" \
  --zone="$ZONE" --tunnel-through-iap --quiet \
  --command="cd ${VM_PROJECT_DIR} && \
    export BOOKING_IMAGE='${IMAGE_BY_DIGEST}' && \
    docker pull '${IMAGE_BY_DIGEST}' && \
    docker compose up -d --wait --wait-timeout 120"
echo "  ✓ compose stack up"

# ============================================================================
# 6. Smoke test (/livez + /readyz)
# ============================================================================
echo "===> [6/6] Smoke testing /livez + /readyz"
SMOKE_OK=true
for endpoint in livez readyz; do
  if ! gcloud compute ssh "$VM_NAME" \
       --zone="$ZONE" --tunnel-through-iap --quiet \
       --command="curl -sf --max-time 5 http://localhost:8080/${endpoint} > /dev/null"; then
    echo "  ✗ /${endpoint} failed"
    SMOKE_OK=false
  else
    echo "  ✓ /${endpoint}"
  fi
done

if [ "$SMOKE_OK" = false ]; then
  echo ""
  echo "===> ROLLBACK: smoke failed; reverting to ${PREVIOUS_DIGEST}"
  if [ "$PREVIOUS_DIGEST" = "none" ] || [ -z "$PREVIOUS_DIGEST" ]; then
    echo "  ✗ no previous image recorded; manual intervention required"
    exit 5
  fi
  gcloud compute ssh "$VM_NAME" \
    --zone="$ZONE" --tunnel-through-iap --quiet \
    --command="cd ${VM_PROJECT_DIR} && \
      export BOOKING_IMAGE='${PREVIOUS_DIGEST}' && \
      docker pull '${PREVIOUS_DIGEST}' && \
      docker compose up -d --wait --wait-timeout 120"
  echo "  ✓ rolled back. Investigate logs: \`gcloud compute ssh ${VM_NAME} --tunnel-through-iap -- docker compose logs app\`"
  exit 6
fi

echo ""
echo "=================================================="
echo "✓ Deploy successful: ${IMAGE_BY_DIGEST}"
echo "=================================================="
