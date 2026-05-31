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

# Identity regex for cosign verify — matches PR 3's Makefile shape.
# CRIT-C2 fix (PR 5 review round 1): `tags/v.*` → `tags/v[0-9][^/]*`
# rejects tag names containing `/` (git allows them, e.g. `v1.0.0/hot`)
# and requires the post-`v` char to be a digit (standard semver shape).
# Keep the {owner}/{repo} hardcoded for now — extracting from `git remote
# get-url origin` would make this drift-safe across rename/transfer but
# costs us PR-scope creep. Tracked: M4 in review report.
COSIGN_IDENTITY_REGEX='^https://github\.com/Leon180/booking_monitor/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9][^/]*)$'
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
# Read from the RUNNING CONTAINER (booking_app), NOT by image-tag lookup.
# Earlier draft used `docker inspect booking-monitor` which resolves to
# :latest — never matches our digest-only deploys → rollback always
# exited "none". Caught in 3-agent review.
#
# Resolve container's image ID → then look up that image's RepoDigests[0].
# Handles single + multi-tag images (we deliberately read just the FIRST
# digest; `tr -d '[]'` on `{{.RepoDigests}}` would have left a space-
# separated string that `docker pull` rejects with "invalid reference").
# shellcheck disable=SC2016
# Single quotes are intentional: $(docker inspect ...) must execute on
# the VM (inside the gcloud ssh --command body), NOT locally.
#
# `sudo` because OS Login mints an ephemeral Linux user per SSH session
# that is NOT in the `docker` group (cloud-init doesn't gpasswd it; OS
# Login users are created on first login by the Google guest agent).
# Both operators (osAdminLogin) and the CI SA (osAdminLogin, PR 5) have
# passwordless sudo, so `sudo docker ...` works for both. See
# `deploy/terraform/workload_identity.tf` for the IAM rationale.
PREVIOUS_DIGEST=$(gcloud compute ssh "$VM_NAME" \
  --zone="$ZONE" --tunnel-through-iap --quiet \
  --command='IMG_ID=$(sudo docker inspect booking_app --format="{{.Image}}" 2>/dev/null) && \
             sudo docker inspect "$IMG_ID" --format="{{index .RepoDigests 0}}" 2>/dev/null || echo none' \
  2>/dev/null || echo "none")
# Trim + normalize empty / Go-template "<no value>" to "none".
PREVIOUS_DIGEST=$(echo "$PREVIOUS_DIGEST" | tr -d '[:space:]')
case "$PREVIOUS_DIGEST" in
  ""|"<no"|*"no"*"value"*) PREVIOUS_DIGEST="none" ;;
esac
echo "  current: ${PREVIOUS_DIGEST}"

# ============================================================================
# 2. Sync repo on VM to deployed commit (needed for migrations + compose files)
# ============================================================================
echo "===> [2/6] Syncing repo on VM to deployed commit"

# Determine the source git commit from the image OCI label.
#
# Earlier draft used `--format='value(image_summary.labels)'` + `grep -oP`.
# Two problems caught in 3-agent review:
#   (1) gcloud emits labels as a Python-dict-repr string, NOT key=value
#       lines — grep would never match.
#   (2) BSD grep on macOS lacks `-P` (PCRE) — operators on macOS would
#       hit "invalid option" before getting to (1).
# Both go away with gcloud's structured field path:
#   `--format='value(image_summary.labels.org_opencontainers_image_revision)'`
# gcloud flattens dots → underscores in nested map keys.
DEPLOYED_COMMIT=$(gcloud artifacts docker images describe "$IMAGE_REF" \
  --format='value(image_summary.labels.org_opencontainers_image_revision)' \
  2>/dev/null) || true
DEPLOYED_COMMIT=$(echo "$DEPLOYED_COMMIT" | tr -d '[:space:]')

if [ -z "${DEPLOYED_COMMIT:-}" ]; then
  echo "  ✗ could not extract commit SHA from image label 'org.opencontainers.image.revision'."
  echo "    Possible causes:"
  echo "      - Image was built before PR 2 (no OCI labels)"
  echo "      - gcloud projection format changed (run: gcloud artifacts docker images describe $IMAGE_REF --format=json | jq '.image_summary.labels')"
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
# Mounts migrations dir read-only.
#
# 3-agent review CRIT: earlier draft used `set -a && source .env` to load
# secrets — but `source` is shell evaluation. A secret value containing
# \$(cmd) or backticks would execute as root on the VM. Realistic
# threat: DB passwords routinely contain \$, future automation could
# write arbitrary values to Secret Manager.
#
# Fix: pass .env to the migrate CONTAINER via `--env-file`, which uses
# docker-compose's dotenv parser (no shell eval). migrate reads
# DATABASE_URL from container env automatically (no -database flag
# needed when env is set). Forward-only convention — never run `down`.
gcloud compute ssh "$VM_NAME" \
  --zone="$ZONE" --tunnel-through-iap --quiet \
  --command="cd ${VM_PROJECT_DIR} && \
    sudo docker run --rm --network=host \
      --env-file ${VM_PROJECT_DIR}/.env \
      --entrypoint sh \
      -v ${VM_PROJECT_DIR}/deploy/postgres/migrations:/migrations:ro \
      migrate/migrate:v4.19.1 \
      -c 'migrate -path /migrations -database \"\$DATABASE_URL\" up'"
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
    sudo docker pull '${IMAGE_BY_DIGEST}' && \
    sudo BOOKING_IMAGE='${IMAGE_BY_DIGEST}' docker compose up -d --wait --wait-timeout 120"
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
      sudo docker pull '${PREVIOUS_DIGEST}' && \
      sudo BOOKING_IMAGE='${PREVIOUS_DIGEST}' docker compose up -d --wait --wait-timeout 120"
  echo "  ✓ rolled back. Investigate logs: \`gcloud compute ssh ${VM_NAME} --tunnel-through-iap -- sudo docker compose logs app\`"
  exit 6
fi

echo ""
echo "=================================================="
echo "✓ Deploy successful: ${IMAGE_BY_DIGEST}"
echo "=================================================="
