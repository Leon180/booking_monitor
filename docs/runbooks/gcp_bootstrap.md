# GCP Bootstrap Runbook

Step-by-step setup for the booking_monitor production-grade CI/CD infrastructure on GCP. End state: Workload Identity Federation wired to GitHub Actions, Artifact Registry ready to receive signed images, Secret Manager containing live secret values, and an e2-micro VM ready for the PR 4 deploy mechanism.

**Expected wall-clock**: 60–90 minutes if it's your first time. The Workload Identity Pool creation step has propagation delays (~30s); plan around them.

**Prerequisites**:
- GCP account with billing enabled
- `gcloud` CLI installed and authenticated: `gcloud auth login` + `gcloud auth application-default login`
- `terraform` >= 1.7.0
- A Cloudflare account (for PR 4 — not needed here, but link it now so you don't context-switch later)
- Push access to the booking_monitor GitHub repo

---

## Step 1 — Choose the project mode

### Option A: use an existing GCP project

```bash
gcloud projects list
# pick an ID, e.g. booking-monitor-prod
```

In `deploy/terraform/terraform.tfvars`:

```hcl
project_id     = "your-existing-project-id"
create_project = false
```

### Option B: Terraform creates a fresh project

```bash
gcloud beta billing accounts list
# pick a billing account ID, format XXXXXX-XXXXXX-XXXXXX
```

In `deploy/terraform/terraform.tfvars`:

```hcl
project_id         = "booking-monitor-prod"
create_project     = true
billing_account_id = "0X0X0X-XXXXXX-XXXXXX"
```

You also need `roles/resourcemanager.projectCreator` on your GCP user (or organization).

---

## Step 2 — Bootstrap the state bucket (manual, one-time)

Terraform stores state in GCS, but the bucket itself can't be created by the very Terraform that needs it (chicken/egg). So bootstrap it manually:

```bash
PROJECT_ID="booking-monitor-prod"          # match what you set in tfvars
REGION="us-central1"

# Set active project (gcloud needs this for the bucket create call)
gcloud config set project "$PROJECT_ID"

# Create the bucket with uniform IAM + versioning
gsutil mb -l "$REGION" -b on "gs://${PROJECT_ID}-tfstate"
gsutil versioning set on "gs://${PROJECT_ID}-tfstate"

# Lock down public access
gcloud storage buckets update "gs://${PROJECT_ID}-tfstate" \
  --public-access-prevention
```

**Why versioning**: a corrupt or mis-applied state can be rolled back by listing object versions and copying the previous one to current. Saves you from "lost a day of state edits" scenarios.

---

## Step 3 — First Terraform apply

```bash
cd deploy/terraform

# Pass the bucket name at init time so the backend.gcs block stays
# project-agnostic (good for re-using this module across environments).
terraform init -backend-config="bucket=${PROJECT_ID}-tfstate"

# Inspect the plan carefully. The first apply will create ~20 resources.
terraform plan -var-file=terraform.tfvars

# Apply.
terraform apply -var-file=terraform.tfvars
```

If apply fails partway with `API not enabled`, wait 30 seconds and re-run — `google_project_service` triggers async API enablement and downstream resources sometimes race the enable.

---

## Step 4 — Populate Secret Manager values

Terraform creates empty containers; values must be added separately to keep them out of Terraform state.

```bash
# Interactive, secret-by-secret
for s in stripe-api-key stripe-webhook-secret payment-webhook-secret \
         database-url redis-password grafana-admin-password; do
  read -srp "Enter value for $s: " v && echo
  echo -n "$v" | gcloud secrets versions add "$s" --data-file=-
done

# Verify
gcloud secrets list
gcloud secrets versions list stripe-api-key
```

**For dev**: use Stripe test keys (`sk_test_*`) and a dummy webhook secret.
**For production**: use Stripe restricted keys (`rk_live_*`) with only the scopes the app needs (PaymentIntents read/write, Webhooks none).

---

## Step 5 — Wire GitHub Actions

The outputs from `terraform apply` give you what you need to set as GitHub Actions repository **variables** (not secrets — these are not sensitive).

```bash
terraform output workload_identity_provider
# Example: projects/123456789/locations/global/workloadIdentityPools/pool-github/providers/prov-github

terraform output ci_deploy_sa_email
# Example: sa-ci-deploy@booking-monitor-prod.iam.gserviceaccount.com

terraform output ci_readonly_sa_email
# Example: sa-ci-readonly@booking-monitor-prod.iam.gserviceaccount.com

terraform output artifact_registry_url
# Example: us-central1-docker.pkg.dev/booking-monitor-prod/booking
```

On GitHub:
1. Settings → Secrets and variables → Actions → **Variables** tab (not Secrets)
2. Add repo variables:
   - `GCP_WORKLOAD_IDENTITY_PROVIDER` ← `workload_identity_provider` output
   - `GCP_CI_DEPLOY_SA` ← `ci_deploy_sa_email` output
   - `GCP_CI_READONLY_SA` ← `ci_readonly_sa_email` output
   - `GCP_ARTIFACT_REGISTRY` ← `artifact_registry_url` output

PR 3 will reference these when authenticating from CI.

---

## Step 6 — Smoke test the OIDC chain

From your local machine, verify the SA can be impersonated (this proves IAM bindings work; it does NOT yet test the OIDC token exchange, which requires GH Actions runtime):

```bash
# Grant yourself temporary impersonation rights
gcloud iam service-accounts add-iam-policy-binding \
  "sa-ci-deploy@${PROJECT_ID}.iam.gserviceaccount.com" \
  --member="user:$(gcloud config get-value account)" \
  --role="roles/iam.serviceAccountTokenCreator"

# Impersonate and read a secret value
gcloud secrets versions access latest \
  --secret=stripe-api-key \
  --impersonate-service-account="sa-ci-deploy@${PROJECT_ID}.iam.gserviceaccount.com"

# Should print the secret value. If it errors with PERMISSION_DENIED on
# iam.serviceAccounts.getAccessToken, the role binding hasn't propagated
# yet (give it 30s).

# CLEANUP: remove your temporary impersonation grant
gcloud iam service-accounts remove-iam-policy-binding \
  "sa-ci-deploy@${PROJECT_ID}.iam.gserviceaccount.com" \
  --member="user:$(gcloud config get-value account)" \
  --role="roles/iam.serviceAccountTokenCreator"
```

The real OIDC chain test happens in PR 3 when CI exchanges its JWT for a GCP token.

---

## Step 7 — SSH to the VM (via IAP)

```bash
gcloud compute ssh booking-app \
  --zone="${REGION}-a" \
  --tunnel-through-iap

# Verify bootstrap completed
cat /var/lib/booking-monitor/bootstrap_completed_at
docker --version
docker compose version
cloudflared --version
```

If SSH hangs, common causes:
- You don't have `roles/iap.tunnelResourceAccessor` (add via `gcloud projects add-iam-policy-binding`).
- The VM is still running cloud-init bootstrap — give it 60–90 seconds after first boot, then retry.
- Your local network blocks outbound to `35.235.240.0/20` (rare; some corporate VPNs do).

---

## Threat model — why this is hardened

| Threat | Mitigation in this module |
| --- | --- |
| Stolen long-lived service account key | None to steal — WIF + OIDC, tokens issued per-run (~15 min) |
| Forked PR exfiltrates write credentials | `attribute_condition` rejects tokens for other repo owners; deploy SA bound to `refs/heads/main` only |
| Compromised dev machine pushes a malicious workflow to a feature branch | Feature branch's `sub` claim doesn't match deploy SA's principal binding; impersonation fails |
| Anyone on the public internet hits port 22 | GCP firewall only allows `35.235.240.0/20` (IAP); ufw on the VM enforces same |
| Anyone on the public internet hits the app | Cloudflare Tunnel terminates HTTP at CF edge (PR 4); VM has no public HTTP listener |
| Project-level metadata SSH key added by another user | `block-project-ssh-keys = TRUE` on the instance forces OS Login |
| Boot disk tampered with by attacker with disk access | Shielded VM: secure boot, vTPM, integrity monitoring |
| Terraform state leaks secret values | Secret values not in Terraform state; Terraform owns containers + IAM only |
| State bucket accidentally world-readable | `public-access-prevention` enforced at bucket level |

---

## Tear-down

```bash
# Reverse order: state bucket last (since terraform destroy depends on it)
cd deploy/terraform
terraform destroy -var-file=terraform.tfvars

# Then manually delete the state bucket
gsutil rm -r "gs://${PROJECT_ID}-tfstate"
```

If `create_project=true`, this also deletes the GCP project. Allow 30 days for the project ID to become available again for re-use.

---

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `Error 403: The project ... has not enabled the API` | API enable propagation race | Retry `terraform apply` after 30s |
| `Error 400: Invalid attribute condition` | CEL syntax error in `attribute_condition` | Use double quotes for strings, `==` not `=`, check `assertion.X` claim name spelling |
| `PERMISSION_DENIED on iam.serviceAccounts.getAccessToken` from CI | WIF binding propagation delay OR `permissions: id-token: write` missing in workflow yaml | Wait 60s + verify workflow permissions block |
| `Error 409: Service account ... already exists` | Re-apply after manual creation in the console | `terraform import google_service_account.ci_deploy projects/.../serviceAccounts/sa-ci-deploy@...` |
| e2-micro instance shows up as billed | Region isn't us-central1/us-east1/us-west1 | Re-apply with correct `region` variable; the validation in `variables.tf` should have caught this — check you haven't bypassed it |
