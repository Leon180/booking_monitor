# GCP Infrastructure (Terraform)

Bootstrap module for the booking_monitor production-grade CI/CD chain.

## What this module provisions

| Resource | Purpose | First used by |
| --- | --- | --- |
| Workload Identity Pool + Provider | GitHub Actions OIDC federation (no long-lived keys) | PR 3 (CI build + push) |
| `sa-ci-readonly` service account | Impersonated by PR / branch builds (read-only AR) | PR 3 |
| `sa-ci-deploy` service account | Impersonated by main / tag builds (push AR + read secrets) | PR 3, 5 |
| `sa-app-runtime` service account | VM-side SA (read secrets + write logs) | PR 4 |
| Artifact Registry `booking` repo | Container image storage with cleanup policies | PR 3 |
| Secret Manager containers (6) | Empty containers; values populated out-of-band | PR 5 |
| `booking-app` e2-micro VM | Always-Free tier, hardened defaults, IAP-only SSH | PR 4 |
| IAP SSH firewall rule | Only IAP source range can reach port 22 | (now) |

See [variables.tf](variables.tf) for inputs, [outputs.tf](outputs.tf) for handoffs to downstream PRs.

## Quick start

Full instructions: [`docs/runbooks/gcp_bootstrap.md`](../../docs/runbooks/gcp_bootstrap.md).

```bash
cp terraform.tfvars.example terraform.tfvars
# edit terraform.tfvars

# Bootstrap state bucket (manual, one-time)
gsutil mb -l us-central1 -b on gs://${PROJECT_ID}-tfstate
gsutil versioning set on gs://${PROJECT_ID}-tfstate

# Init + apply
terraform init -backend-config="bucket=${PROJECT_ID}-tfstate"
terraform plan -var-file=terraform.tfvars
terraform apply -var-file=terraform.tfvars

# Populate secret values
for s in stripe-api-key stripe-webhook-secret payment-webhook-secret \
         database-url redis-password grafana-admin-password; do
  read -srp "$s: " v && echo
  echo -n "$v" | gcloud secrets versions add "$s" --data-file=-
done
```

## Cost ceiling

Targeting **$0 / month** within the GCP Always-Free tier. Watchpoints:

- e2-micro VM is free only in us-central1 / us-east1 / us-west1 (`variables.tf` validation enforces this).
- 30 GB pd-standard disk is at the ceiling — don't enlarge.
- 1 GB outbound egress/month is the realistic constraint at any non-trivial traffic. Cloudflare Tunnel terminates traffic at CF edge, so the VM's outbound is mostly metrics + small webhook payloads.
- Secret Manager: 6 active versions/month free. We have 6 secrets × 1 version each = exactly at ceiling. Rotating any one secret = 1 version added; under 6 rotations/month stays free.
- Artifact Registry: 0.5 GB storage free. Cleanup policies in [`artifact_registry.tf`](artifact_registry.tf) keep last-10 versions to fit.

## Security posture

- **No long-lived service account keys**: WIF + OIDC. See PR description for threat model.
- **Split CI service accounts**: `sa-ci-readonly` for PRs, `sa-ci-deploy` gated to `refs/heads/main` + `refs/tags/v*`.
- **No public ingress except via Cloudflare Tunnel** (added in PR 4). VM's external IP is unreachable from the public internet for HTTP.
- **SSH via IAP only**: `35.235.240.0/20` source filter; OS Login for IAM-managed authn.
- **Shielded VM**: secure boot + vTPM + integrity monitoring enabled.

See [`docs/runbooks/gcp_bootstrap.md`](../../docs/runbooks/gcp_bootstrap.md) for the threat model walkthrough.
