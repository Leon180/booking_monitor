# GCP Bootstrap Runbook

Step-by-step setup for the booking_monitor production-grade CI/CD infrastructure on GCP. End state: Workload Identity Federation wired to GitHub Actions, Artifact Registry ready to receive signed images, Secret Manager containing live secret values, and an e2-micro VM ready for the PR 4 deploy mechanism.

**Expected wall-clock**: 60–90 minutes if it's your first time. The Workload Identity Pool creation step has propagation delays (~30s); plan around them.

**Prerequisites**:
- GCP account with billing enabled
- `gcloud` CLI installed and authenticated: `gcloud auth login` + `gcloud auth application-default login`
- `terraform` >= 1.11.0 (1.11 added write-only attributes; the module uses them — see `versions.tf`)
- `gh` CLI installed and authenticated (`gh auth login`) — needed to look up GitHub immutable IDs in Step 1.5
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

## Step 1.5 — Look up immutable GitHub IDs

The WIF trust binds on `repository_owner_id` and `repository_id` — the IMMUTABLE numeric ids, not the mutable names. This means a future repo rename or owner transfer can't silently change which workflows can impersonate the CI service accounts.

Look up once and paste into `terraform.tfvars`:

```bash
# Owner id (user or org)
gh api /users/Leon180 --jq .id
# Example output: 12345678

# Repo id
gh api /repos/Leon180/booking_monitor --jq .id
# Example output: 87654321
```

In `terraform.tfvars`:

```hcl
github_owner    = "Leon180"             # human-readable, informational
github_owner_id = "12345678"            # paste output above
github_repo     = "booking_monitor"     # human-readable, informational
github_repo_id  = "87654321"            # paste output above
```

---

## Step 2 — Bootstrap the state bucket (manual, one-time)

Terraform stores state in GCS, but the bucket itself can't be created by the very Terraform that needs it (chicken/egg). So bootstrap it manually using the modern `gcloud storage` CLI (the older `gsutil` is deprecated as of mid-2025 — works for now but emits warnings).

```bash
PROJECT_ID="booking-monitor-prod"          # match what you set in tfvars
REGION="us-central1"

# Set active project (gcloud needs this for the bucket create call)
gcloud config set project "$PROJECT_ID"

# Create the bucket with uniform IAM + public-access prevention + versioning
gcloud storage buckets create "gs://${PROJECT_ID}-tfstate" \
  --location="$REGION" \
  --uniform-bucket-level-access \
  --public-access-prevention

gcloud storage buckets update "gs://${PROJECT_ID}-tfstate" \
  --versioning
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

## Step 7 — SSH to the VM (via IAP) + smoke-test hardening

```bash
gcloud compute ssh booking-app \
  --zone="${REGION}-a" \
  --tunnel-through-iap
```

Once inside the VM, run these checks. **Each one verifies one specific hardening claim from the fixup #2 PR** — if any prints unexpected output, the corresponding hardening DID NOT take effect.

```bash
# (a) Bootstrap completed marker
cat /var/lib/booking-monitor/bootstrap_completed_at
# Expected: an ISO-8601 timestamp.

# (b) Docker installed + version pin took effect
docker --version
docker compose version
apt-cache policy docker-ce
# Expected: the `Candidate:` line shows 5:28.* and the highest-priority
# entry has `priority 1001` (our pin file). If priority shows 500, the
# pin file was not applied — re-run bootstrap.sh manually.

# (c) Docker package on hold (belt-and-suspenders for the pin file)
apt-mark showhold | grep docker-ce
# Expected: `docker-ce` printed. If empty, future `apt upgrade` could
# walk Docker to 29.x.

# (d) unattended-upgrades pattern matches Debian-Security
sudo unattended-upgrades --dry-run -d 2>&1 | grep -E "Allowed origins|origin=Debian"
# Expected: shows `origin=Debian,codename=bookworm,label=Debian-Security`
# in the matched origins. If empty, the heredoc-templated ${distro_codename}
# didn't expand — check /etc/apt/apt.conf.d/50unattended-upgrades.

# (e) chrony serving NTP + auditd running
systemctl is-active chrony
systemctl is-active auditd
# Expected: `active` for both.

# (f) ufw deny-all-inbound enforced + SSH allowed
sudo ufw status verbose | head -10
# Expected: `Status: active`, `Default: deny (incoming) allow (outgoing)`,
# rule list contains `22/tcp ALLOW IN`.

# (g) cloudflared binary present (config deployed in PR 4)
cloudflared --version
# Expected: a version banner. Missing config is fine here.
```

If SSH itself hangs, common causes:
- You don't have `roles/iap.tunnelResourceAccessor` on YOUR user. (Note: the deploy SA already has it via PR 1 fixup #2; this only blocks human operators.) Add via `gcloud projects add-iam-policy-binding $PROJECT_ID --member="user:$(gcloud config get-value account)" --role=roles/iap.tunnelResourceAccessor`.
- The VM is still running cloud-init bootstrap — give it 60–90 seconds after first boot, then retry.
- Your local network blocks outbound to `35.235.240.0/20` (rare; some corporate VPNs do).

---

## Threat model — what this hardens AND what it does NOT

Honest table — both mitigated AND known-residual risks are listed. Hand-wavy "this is secure" runbooks are worse than honest "here's exactly what we cover" ones.

### Mitigated

| Threat | Mitigation in this module |
| --- | --- |
| Stolen long-lived service account key | None to steal — WIF + OIDC, ~15 min token lifetime |
| Forked PR impersonates write credentials | `attribute_condition` rejects tokens for other repo `repository_owner_id`s. Deploy SA only impersonable from `refs/heads/main` push OR `refs/tags/v*` push via the `attribute.deploy_eligible` CEL derivation |
| Compromised dev pushes malicious workflow to a feature branch in the same repo | Feature branch produces `deploy_eligible == "no"`; readonly SA only — no write access |
| GitHub repo or owner RENAMED / TRANSFERRED | Trust binds on IMMUTABLE numeric ids (`repository_owner_id` / `repository_id`), not mutable names. Survives rename / transfer |
| Public internet hits port 22 | GCP firewall only allows `35.235.240.0/20` (IAP). VPC implicit-deny handles everything else (the previous explicit `deny_all_inbound` rule was decoration — see fixup #2 PR) |
| Public internet hits the app | Cloudflare Tunnel terminates HTTP at CF edge (PR 4); VM has no public HTTP listener |
| Project-wide SSH key added by another user | `block-project-ssh-keys = TRUE` forces OS Login |
| Boot disk tampered with by disk-access attacker | Shielded VM: secure boot + vTPM + integrity monitoring |
| Terraform state leaks secret values | Secret values not in Terraform state; Terraform owns containers + IAM only |
| State bucket accidentally world-readable | `public-access-prevention` enforced at bucket level |
| `var.secret_names` list trimmed by mistake destroying secret + versions | `prevent_destroy = true` lifecycle on secret containers; explicit `terraform state rm` required before destroy |
| Docker version walks to 29.x via unattended-upgrades and breaks the stack | apt preferences pin (`5:28.*`, priority 1001) + `apt-mark hold` + Docker packages on unattended-upgrades blocklist |
| Feature upgrades break the system overnight via apt | `unattended-upgrades` configured Debian-Security ONLY (no feature upgrades) |

### Known residual / not (yet) covered

| Threat | Status |
| --- | --- |
| OIDC bearer-token replay between `auth@v2` step and STS exchange | Real but narrow (1-hour ceiling, full audit trail). Sender-constrained tokens not yet supported by GH OIDC. See Topic 1 lesson |
| Anyone with `roles/owner` self-grants `roles/iam.serviceAccountTokenCreator` and bypasses WIF | Inherent to GCP IAM. Mitigation: minimize who has `roles/owner`; enable Cloud Audit Logs alert on IAM binding changes (deferred to PR 7) |
| Branch protection on `main` (require review + status checks) | NOT enforced by this Terraform module — must be set via GitHub repo settings. The WIF trust assumes `main` only contains reviewed code; if branch protection is off, that assumption fails |
| CI runs `terraform apply` (state bucket IAM not yet granted to deploy SA) | Today only human operators with ADC apply. PR 3 will add the IAM grant when CI needs it |
| Cloud Logging deploy markers / SLO alerting | Deferred to PR 7 (DORA dashboard) |
| Production host = Container-Optimized OS (COS) instead of Debian + hardening | Deferred to PR 8 (k8s migration) — see compute.tf comment |

---

## Tear-down

`terraform destroy` will be blocked by the `prevent_destroy = true` lifecycle on Secret Manager secret containers — that's intentional, so you don't lose live secret versions on accident. If you really want to nuke everything:

```bash
cd deploy/terraform

# 1. Drop secret containers from Terraform state WITHOUT deleting from GCP.
#    Run this for each entry in var.secret_names.
for s in stripe-api-key stripe-webhook-secret payment-webhook-secret \
         database-url redis-password grafana-admin-password; do
  terraform state rm "google_secret_manager_secret.secrets[\"${s}\"]"
done

# 2. Manually delete secrets from GCP if you really want to (this loses
#    every version forever — write them down first if relevant).
for s in stripe-api-key stripe-webhook-secret payment-webhook-secret \
         database-url redis-password grafana-admin-password; do
  gcloud secrets delete "$s" --quiet
done

# 3. Now `terraform destroy` will succeed.
terraform destroy -var-file=terraform.tfvars

# 4. Then manually delete the state bucket.
gcloud storage rm --recursive "gs://${PROJECT_ID}-tfstate"
```

If `create_project=true`, the destroy also deletes the GCP project. Allow 30 days for the project ID to become available again for re-use.

---

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `Error 403: The project ... has not enabled the API` | API enable propagation race | Retry `terraform apply` after 30s |
| `Error 400: Invalid attribute condition` | CEL syntax error in `attribute_condition` | Use double quotes for strings, `==` not `=`, check `assertion.X` claim name spelling |
| `PERMISSION_DENIED on iam.serviceAccounts.getAccessToken` from CI | WIF binding propagation delay OR `permissions: id-token: write` missing in workflow yaml | Wait 60s + verify workflow permissions block |
| `Error 409: Service account ... already exists` | Re-apply after manual creation in the console | `terraform import google_service_account.ci_deploy projects/.../serviceAccounts/sa-ci-deploy@...` |
| e2-micro instance shows up as billed | Region isn't us-central1/us-east1/us-west1 | Re-apply with correct `region` variable; the validation in `variables.tf` should have caught this — check you haven't bypassed it |
