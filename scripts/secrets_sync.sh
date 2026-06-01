#!/usr/bin/env bash
# secrets_sync.sh — pull GCP Secret Manager values to /opt/booking-monitor/.env
#
# Designed to run ON the booking VM via `gcloud compute ssh --tunnel-through-iap`.
# Uses the VM's attached service account (sa-app-runtime, PR 1) + the metadata
# server for auth — no credentials handling here.
#
# Idempotent: rewrites .env atomically; re-running just refreshes values.
#
# Usage (from operator machine):
#   gcloud compute ssh booking-app --zone us-central1-a --tunnel-through-iap \
#     --command='sudo /opt/booking-monitor/scripts/secrets_sync.sh'
#
# Or via deploy.sh which wraps this.
#
# Designed assumptions (per PR 4 fact-check):
#   * Secret Manager free tier: 10K access ops/month — 6 secrets × 100 deploys
#     = 600 ops/mo, well under cap (16× headroom).
#   * `gcloud secrets versions access latest --secret=NAME` IS the canonical
#     CLI shape in 2026.
#   * VM SA must have `cloud-platform` scope (PR 1's compute.tf sets this).
#     We sanity-check below to fail loud if a future change restricts it.

set -euo pipefail

# Where the env file lands.
#
# Ownership: root:docker (mode 640). 3-agent PR 4 review caught the
# earlier 600-root-owned design — `docker compose` runs as the operator's
# OS Login user (NOT root). With 600 the compose process can't read .env
# → docker-compose.yml `${VAR:?}` checks fail at startup with confusing
# "env var missing" errors. Granting `docker` group read access (every
# user that can run `docker` commands is in this group) lets compose
# read .env while keeping non-docker users locked out. Root is the
# ONLY writer (this script always runs via `sudo bash`).
ENV_FILE="${ENV_FILE:-/opt/booking-monitor/.env}"
ENV_FILE_TMP="${ENV_FILE}.tmp.$$"

# Secret names — MUST match `var.secret_names` in deploy/terraform/variables.tf.
# Drift here = silent missing env var at app start. If this list grows, also
# grow the tfvars default + verify the env var name the app reads matches.
SECRETS=(
  "stripe-api-key"
  "stripe-webhook-secret"
  "payment-webhook-secret"
  "database-url"
  "redis-password"
  "grafana-admin-password"
)

# Map secret-id → ENV_VAR_NAME the app reads. Hyphenated secret IDs → SHOUTY_SNAKE.
secret_to_env() {
  case "$1" in
    stripe-api-key)         echo "STRIPE_API_KEY" ;;
    stripe-webhook-secret)  echo "STRIPE_WEBHOOK_SECRET" ;;
    payment-webhook-secret) echo "PAYMENT_WEBHOOK_SECRET" ;;
    database-url)           echo "DATABASE_URL" ;;
    redis-password)         echo "REDIS_PASSWORD" ;;
    grafana-admin-password) echo "GRAFANA_ADMIN_PASSWORD" ;;
    *) echo "" ;;
  esac
}

# ---- 0. VM scope sanity check ----
# If PR 1's compute.tf has been changed to restrict scopes, `gcloud secrets
# versions access` will 403 even with the right IAM role. Fail loud now,
# not on the actual deploy.
echo "Checking VM service-account scope..."
ATTACHED_SA=$(curl -s -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/email" 2>/dev/null) || {
  echo "ERROR: not on a GCE VM (metadata server unreachable). This script must run on the VM."
  exit 2
}

ATTACHED_SCOPES=$(curl -s -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/scopes" 2>/dev/null)

case "$ATTACHED_SCOPES" in
  *"cloud-platform"*) : ;;
  *)
    echo "ERROR: VM service account scopes do not include cloud-platform."
    echo "  attached SA: $ATTACHED_SA"
    echo "  scopes:      $ATTACHED_SCOPES"
    echo "  Fix: edit deploy/terraform/compute.tf service_account.scopes, re-apply."
    exit 3
    ;;
esac
echo "  ✓ SA: $ATTACHED_SA"
echo "  ✓ scopes include cloud-platform"

# ---- 1. Compose final .env: base template + overlay secrets ----
#
# Previous design (PR 4) wrote ONLY the 6 secret values to .env. Real
# operation surfaced the gap: docker-compose.yml requires ~40+ non-secret
# CONFIG values (POSTGRES_USER, REDIS_ADDR, KAFKA_BROKERS, OTEL_...,
# worker tunings, etc.) and the .env-only-6-secrets file caused
# compose-up to fail with "POSTGRES_USER is required" type errors.
#
# Fix: start from the committed `deploy/.env.vm.template` (VM-suitable
# CONFIG with compose-internal hostnames), then OVERLAY the 6 Secret
# Manager values on top by replacing the PLACEHOLDER_* tokens.
#
# Trade-off vs alternatives considered:
#   * Pure overlay (append secrets) — would leave PLACEHOLDER_* values
#     visible in .env, breaking compose's `${VAR:?}` validation.
#   * Read .env.example (local-dev defaults) — has localhost hostnames
#     that don't work compose-internal. Diverges from VM needs.
#   * Move all config to Secret Manager — works but Secret Manager has
#     a 64 KiB cap per secret + a per-secret pricing model; cheap, but
#     burying non-secret config in Secret Manager is poor hygiene.
# Chosen path: committed `.env.vm.template` + sed-overlay of 6 secrets.

mkdir -p "$(dirname "$ENV_FILE")"

# Locate the template — bundled with the cloned repo at deploy/.env.vm.template.
# Prefer an explicit env var (test override) over auto-detection.
TEMPLATE="${SECRETS_SYNC_TEMPLATE:-}"
if [ -z "$TEMPLATE" ]; then
  # Resolve relative to this script: scripts/ → deploy/.env.vm.template
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  TEMPLATE="${SCRIPT_DIR}/../deploy/.env.vm.template"
fi
if [ ! -f "$TEMPLATE" ]; then
  echo "ERROR: VM .env template not found at: $TEMPLATE"
  echo "  Expected at deploy/.env.vm.template (relative to repo root)."
  exit 7
fi

# Copy template to temp file, then overlay secret values.
cp "$TEMPLATE" "$ENV_FILE_TMP"
chmod 600 "$ENV_FILE_TMP"

echo "Pulling ${#SECRETS[@]} secrets from Secret Manager + overlaying onto template..."
for secret_id in "${SECRETS[@]}"; do
  env_name=$(secret_to_env "$secret_id")
  if [ -z "$env_name" ]; then
    echo "  ✗ no env mapping for secret '$secret_id'; skipping"
    continue
  fi

  if ! value=$(gcloud secrets versions access latest --secret="$secret_id" 2>/dev/null); then
    echo "  ✗ failed to fetch secret '$secret_id'"
    rm -f "$ENV_FILE_TMP"
    exit 4
  fi

  # Escape for sed replacement: backslashes, ampersands, and the delimiter.
  # We use a delimiter `|` (uncommon in env values); secret values may
  # contain special URL chars so we escape conservatively.
  escaped_value=$(printf '%s' "$value" | sed 's/[\&|\\]/\\&/g')
  placeholder="PLACEHOLDER_$(echo "$env_name" | tr '[:lower:]' '[:upper:]')_FROM_SECRET_MANAGER"

  # Replace the placeholder on the matching env line. Use `sed` with
  # `|` as delimiter (URL slashes don't need escaping).
  # NOTE: we replace the WHOLE line's value to defend against secrets
  # that contain the placeholder text itself.
  # Global token replacement (not line-anchored on env_name). This
  # handles the case where the same secret is referenced by MULTIPLE
  # env vars in the template — e.g. MIGRATE_DB_URL reuses
  # PLACEHOLDER_DATABASE_URL_FROM_SECRET_MANAGER so it picks up the same
  # value as DATABASE_URL. Line-anchored replace would leave the second
  # placeholder dangling and the sanity check would fail.
  placeholder="PLACEHOLDER_${env_name}_FROM_SECRET_MANAGER"
  if ! grep -q "$placeholder" "$ENV_FILE_TMP"; then
    echo "  ✗ template has no $placeholder; check deploy/.env.vm.template"
    rm -f "$ENV_FILE_TMP"
    exit 8
  fi
  # `escaped_value` already escapes the sed delimiter `|`, backslashes,
  # and ampersand-replacement-of-match. Global token replace.
  sed -i.bak "s|${placeholder}|${escaped_value}|g" "$ENV_FILE_TMP"
  rm -f "${ENV_FILE_TMP}.bak"
  echo "  ✓ ${env_name} (${#value} bytes; $(grep -c "^${env_name}\|^MIGRATE_${env_name}\|MIGRATE_DB_URL" "$ENV_FILE_TMP" 2>/dev/null || echo "1") line(s) updated)"
done

# Sanity check: no PLACEHOLDER_* tokens should remain (any left means a
# secret wasn't pulled — fail loud).
if grep -q "PLACEHOLDER_.*_FROM_SECRET_MANAGER" "$ENV_FILE_TMP"; then
  echo "ERROR: PLACEHOLDER tokens remain in .env after overlay:"
  grep -n "PLACEHOLDER_.*_FROM_SECRET_MANAGER" "$ENV_FILE_TMP" | sed 's/^/  /'
  echo "Check that all required secrets exist in Secret Manager."
  rm -f "$ENV_FILE_TMP"
  exit 9
fi

# ---- 2. Atomic swap + finalize ownership ----
# Set ownership BEFORE the mv so the live file is never world-readable
# between mv and chown (race window).
chown root:docker "$ENV_FILE_TMP" 2>/dev/null || {
  # `docker` group missing? Fall back to root-only — better to fail
  # loud at the next step than silently 644 the secrets.
  echo "WARN: 'docker' group not found on this host. Falling back to root:root 600."
  echo "       compose-as-operator will fail to read .env. Investigate why docker isn't installed."
  chown root:root "$ENV_FILE_TMP"
  chmod 600 "$ENV_FILE_TMP"
  mv "$ENV_FILE_TMP" "$ENV_FILE"
  exit 5
}
chmod 640 "$ENV_FILE_TMP"
mv "$ENV_FILE_TMP" "$ENV_FILE"

echo ""
echo "✓ Wrote ${#SECRETS[@]} secrets to $ENV_FILE"
echo "  Owner: $(stat -c '%U:%G' "$ENV_FILE" 2>/dev/null || stat -f '%Su:%Sg' "$ENV_FILE")"
echo "  Mode:  $(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Mp%Lp' "$ENV_FILE")"
