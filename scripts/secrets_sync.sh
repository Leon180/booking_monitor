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

# Where the env file lands. Owned by root, readable by docker daemon group
# only — keeps secrets off the public Docker layer.
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

# ---- 1. Pull each secret + write to temp .env ----
# Atomic-write pattern: build .env.tmp; move on success. Partial-write
# during interrupt won't leave the live .env in a half-state.

mkdir -p "$(dirname "$ENV_FILE")"
: > "$ENV_FILE_TMP"
chmod 600 "$ENV_FILE_TMP"

echo "Pulling ${#SECRETS[@]} secrets from Secret Manager..."
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

  # Escape backslashes + double quotes; wrap in double quotes for safety
  # against spaces / special chars. .env consumers (docker compose) handle
  # double-quoted values per https://docs.docker.com/compose/environment-variables/env-file/
  escaped=$(printf '%s' "$value" | sed 's/\\/\\\\/g; s/"/\\"/g')
  echo "${env_name}=\"${escaped}\"" >> "$ENV_FILE_TMP"
  echo "  ✓ ${env_name} (${#value} bytes)"
done

# ---- 2. Atomic swap ----
mv "$ENV_FILE_TMP" "$ENV_FILE"
chmod 600 "$ENV_FILE"

echo ""
echo "✓ Wrote ${#SECRETS[@]} secrets to $ENV_FILE"
echo "  Owner: $(stat -c '%U:%G' "$ENV_FILE" 2>/dev/null || stat -f '%Su:%Sg' "$ENV_FILE")"
echo "  Mode:  $(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Mp%Lp' "$ENV_FILE")"
