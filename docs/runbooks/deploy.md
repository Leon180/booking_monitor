# Deploy runbook — operator-driven release to the GCP sandbox VM

Day-to-day deploy flow after PR 4 lands. The release pipeline (PR 3, `release.yml`) has already built + signed + attested the image; this runbook turns that into a running container on the VM.

## TL;DR

```bash
# From the operator machine, in the booking_monitor repo:
make deploy IMAGE_TAG=v1.0.0
```

`deploy.sh` (called by the make target) does the full chain end-to-end:

```
verify cosign signature locally
  ↓
capture currently-deployed digest (for rollback)
  ↓
sync VM repo to the deployed commit (for migrations dir + compose files)
  ↓
sync secrets from Secret Manager → VM .env
  ↓
run schema migrations via Docker
  ↓
docker pull <image>@<digest> + compose up --wait --wait-timeout 120
  ↓
smoke test /livez + /readyz
  ↓
if smoke fails → roll back to previous digest, exit non-zero
```

## Prerequisites

One-time setup. If any of these fail, fix that before running `make deploy`.

### 1. Operator-side CLI tools

```bash
brew install cosign google-cloud-sdk
gcloud auth login
gcloud auth application-default login
gcloud config set project booking-monitor-sandbox    # or whatever your project ID is
```

### 2. IAM roles on operator account

```bash
PROJECT_ID="booking-monitor-sandbox"
USER_EMAIL="$(gcloud config get-value account)"

# IAP tunnel access (SSH-via-IAP to the VM)
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="user:$USER_EMAIL" \
  --role="roles/iap.tunnelResourceAccessor"

# OS Login + sudo on the project (PR 1's compute.tf sets enable-oslogin=TRUE)
# `osAdminLogin` (sudo) NOT `osLogin` (no sudo) — deploy.sh's docker
# commands run as the OS Login user, who is NOT in the docker group
# (cloud-init doesn't gpasswd ephemeral OS Login users). Without sudo,
# `docker pull` fails with "permission denied on /var/run/docker.sock"
# at step 5. See deploy/terraform/workload_identity.tf for the same
# rationale applied to the CI SA.
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="user:$USER_EMAIL" \
  --role="roles/compute.osAdminLogin"

# Read Artifact Registry (deploy.sh step 0 calls `gcloud artifacts docker images describe`)
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="user:$USER_EMAIL" \
  --role="roles/artifactregistry.reader"

# Permission to impersonate the deploy SA (for AR pull + Secret Manager read)
gcloud iam service-accounts add-iam-policy-binding \
  "sa-ci-deploy@$PROJECT_ID.iam.gserviceaccount.com" \
  --member="user:$USER_EMAIL" \
  --role="roles/iam.serviceAccountTokenCreator"
```

> Note: PR 4 v1 missed `roles/artifactregistry.reader` and `roles/compute.osLogin`. PR 5 upgraded `osLogin` → `osAdminLogin` because deploy.sh's docker commands need passwordless sudo (OS Login users aren't in the `docker` group). Without `osAdminLogin`, step 5 fails with "permission denied on `/var/run/docker.sock`".

### 3. Cloudflare Tunnel set up on the VM

See [`cloudflare_tunnel.md`](cloudflare_tunnel.md) for the one-time setup. The deploy script doesn't touch the tunnel — but if the tunnel is down, your `https://booking.<domain>/livez` post-deploy verification won't work.

### 4. PR 1 Terraform applied + PR 3 release workflow run at least once

- PR 1: `make apply` in `deploy/terraform/` to provision GCP infra.
- PR 3: push a `v*.*.*` tag to trigger `.github/workflows/release.yml` and produce at least one signed image in Artifact Registry. See [`release_pipeline.md`](release_pipeline.md) Step 4.

> **Heads-up**: if you followed `release_pipeline.md`'s **dry-run cleanup** (it tells you to `gcloud artifacts docker tags delete v0.0.0-rc1` after testing), there is **no signed image left in AR** — push a fresh tag (e.g. `v0.0.1`) before running `make deploy`, or the cosign-verify step in `deploy.sh` step 0 will fail with "image not found in registry".

## Day-to-day usage

### Deploy a specific version tag

```bash
make deploy IMAGE_TAG=v1.0.0
```

Or invoke `deploy.sh` directly:

```bash
./scripts/deploy.sh v1.0.0
```

### Deploy the latest main branch build

```bash
make deploy IMAGE_TAG=main
```

This pulls the `:main` tag from Artifact Registry (the latest main-branch build, signed by `release.yml`).

### Deploy by short SHA

```bash
make deploy IMAGE_TAG=sha-abc1234
```

Each release.yml run tags `:sha-<short>` in addition to `:main` or `:vX.Y.Z`. Useful for deploying a specific commit when `:main` has moved past it.

## What deploy.sh does step-by-step

### [0/6] Pre-flight cosign verification (LOCAL)

Before touching the VM, `cosign verify-attestation` runs locally against both SLSA L3 provenance + SBOM attestations. The `--certificate-identity-regexp` is the same as PR 3's release.yml smoke test — both `release.yml@refs/heads/main` and `release.yml@refs/tags/v*` accepted.

If verification fails, the deploy aborts before any change touches the VM. Common failures:
- Tag not pushed by release.yml (manually-tagged image won't have the attestation)
- Rekor inclusion proof not yet propagated (rare; retry in 2 min)

The deploy also resolves the tag → `sha256:<digest>` once, and uses the digest (immutable) for every subsequent VM-side operation. This avoids tag-mutation races between resolve and deploy.

### [1/6] Capture currently-deployed digest

For rollback. `docker inspect` on the VM gets the `RepoDigests` of the currently-running container. Stored locally for use in step 6 if smoke fails.

### [2/6] Sync VM repo to the deployed commit

The deployed image's OCI label `org.opencontainers.image.revision` tells us which git commit produced it (PR 2 baked this in). We `git fetch` + `git checkout` that commit on the VM, so:

- The migration files at `/opt/booking-monitor/deploy/postgres/migrations/` are exactly the ones that match the deployed code.
- `docker-compose.yml` + `nginx/`, `prometheus/`, etc. configs are in sync.

This is the cleanest way to keep "running image" ↔ "running config files" coherent.

### [3/6] Sync secrets from Secret Manager

Calls `scripts/secrets_sync.sh` on the VM. That script:

- Sanity-checks the VM's service-account scope includes `cloud-platform` (fact-check finding C8 — without this, the secret-fetch silently 403s)
- For each secret in Secret Manager, `gcloud secrets versions access latest`
- Maps secret-id → SHOUTY_SNAKE env var name
- Atomically writes `/opt/booking-monitor/.env` (temp file + rename)
- Sets mode 600 + correct owner

If any secret fetch fails, the temp file is removed and the live `.env` is untouched.

Note on free-tier costs (per fact-check finding C9): Secret Manager free tier is **10K access ops/month + 6 active secret versions + 3 rotation notifications**. We pull 6 secrets per deploy; 100 deploys/month uses 600 ops — well within budget.

### [4/6] Run schema migrations

```bash
docker run --rm --network=host \
  -v /opt/booking-monitor/deploy/postgres/migrations:/migrations:ro \
  migrate/migrate:v4.19.1 \
  -path /migrations -database "$DATABASE_URL" up
```

- Pinned to `migrate/migrate:v4.19.1` (per fact-check, current stable as of 2026-05).
- Idempotent: re-running on an up-to-date schema is a no-op.
- Forward-only: we do NOT run `down.sql` here. To revert a migration, write a new `up.sql` that does the reverse.
- DATABASE_URL is sourced from `.env` (synced in step 3).

If migration fails, the deploy aborts BEFORE the new image is pulled. The old container keeps running with the old schema; no rollback needed because nothing changed yet.

### [5/6] Pull image + roll the compose stack

```bash
docker pull "<registry>/booking-monitor@<digest>"
docker compose up -d --wait --wait-timeout 120
```

- `--wait` blocks until all services' healthchecks pass.
- `--wait-timeout 120` caps the wait at 2 minutes (per fact-check: default is unbounded; would hang the deploy forever on a stuck healthcheck).
- The `BOOKING_IMAGE` env var is set inline before the compose call; `docker-compose.yml`'s `app` service must reference `${BOOKING_IMAGE}` for this to land (existing project already supports this pattern).

If `--wait` times out, deploy proceeds to step 6 anyway — smoke test will catch the failure and trigger rollback.

### [6/6] Smoke test /livez + /readyz

Two HTTP checks via `curl -sf` from inside the VM to localhost:8080:

- `/livez` — process is up (returns 200 if HTTP listener bound)
- `/readyz` — process + dependencies (PG, Redis, Kafka) all answer within 1s

If either fails, deploy.sh rolls back to the previous image digest captured in step 1.

### Rollback path

Triggered automatically if smoke fails. Same `docker pull` + `compose up --wait` flow but with the previous image's digest. Smoke runs again (implicitly via `--wait` healthchecks).

If rollback ALSO fails, deploy.sh exits non-zero and surfaces:
```
✗ rolled back. Investigate logs: `gcloud compute ssh ... docker compose logs app`
```

Manual intervention required.

## Common operator tasks

### Just sync secrets (without a full deploy)

E.g., after rotating a Stripe key in Secret Manager:

```bash
make secrets-sync
# or
gcloud compute ssh booking-app --zone=us-central1-a --tunnel-through-iap \
  --command="sudo bash /opt/booking-monitor/scripts/secrets_sync.sh"
```

The app needs a restart to pick up new env values:

```bash
gcloud compute ssh booking-app --zone=us-central1-a --tunnel-through-iap \
  --command="cd /opt/booking-monitor && docker compose restart app"
```

### Just run migrations (without a deploy)

Rare — usually migrations land with their code. But if you need to:

```bash
gcloud compute ssh booking-app --zone=us-central1-a --tunnel-through-iap \
  --command="cd /opt/booking-monitor && docker run --rm --network=host --env-file /opt/booking-monitor/.env --entrypoint sh -v /opt/booking-monitor/deploy/postgres/migrations:/migrations:ro migrate/migrate:v4.19.1 -c 'migrate -path /migrations -database \"\$DATABASE_URL\" up'"
```

> **Why `--env-file` not `source .env`**: `source` is shell evaluation — a secret value containing `$(cmd)` or backticks would execute as root on the VM. `--env-file` uses docker-compose's dotenv parser (literal, no shell eval). The `sh -c '...'` inside the container then shell-expands `$DATABASE_URL` safely (variable interpolation, not value re-parse). Caught in 3-agent PR 4 review.

### Inspect what's running

```bash
gcloud compute ssh booking-app --zone=us-central1-a --tunnel-through-iap \
  --command="docker compose ps && docker inspect booking-monitor --format='{{.Config.Image}} → {{.RepoDigests}}'"
```

### View logs

```bash
gcloud compute ssh booking-app --zone=us-central1-a --tunnel-through-iap \
  --command="docker compose logs --tail=200 app"
```

### Pause + resume the app (without rolling)

```bash
# Pause (e.g., for maintenance window — Cloudflare will start returning 502s)
gcloud compute ssh booking-app --zone=us-central1-a --tunnel-through-iap \
  --command="cd /opt/booking-monitor && docker compose stop app"

# Resume
gcloud compute ssh booking-app --zone=us-central1-a --tunnel-through-iap \
  --command="cd /opt/booking-monitor && docker compose start app"
```

## Threat model — what this deploy flow protects

| Threat | Mitigation |
| --- | --- |
| Operator deploys a tampered image | `cosign verify-attestation` (step 0) fails closed. Image won't reach the VM unless its SLSA L3 provenance + SBOM are valid and signed by `release.yml@main` or a `v*` tag. |
| Operator deploys an image that was built but later modified at the registry | We deploy by digest, not tag. Digest is content-addressed; AR mutation can't swap content under us between step 0 and step 5. |
| Secret value compromised at fetch time | Logs print secret name + byte count, NOT value. `/opt/booking-monitor/.env` is mode 600 owned by root. Docker reads via `env_file:` — values never appear in image layers. |
| Migration breaks something irreversibly | Forward-only convention + we run migrations BEFORE pulling new image. If migration fails, old container with old schema keeps running. (Caveat: if migration succeeds but introduces a schema-breaking change for the OLD code, you've created an outage that rollback can't fix. Write migrations to be backward-compatible with the previous release.) |
| New image health-checks pass but app is broken in subtle ways | Out of scope for this deploy script — that's what observability (PR 7) catches. |
| Deploy script itself compromised | Operator path: `deploy.sh` runs on the operator machine; tampering possible if the operator's box is compromised. CI path (PR 5, [.github/workflows/deploy.yml](../../.github/workflows/deploy.yml)): the same script lives in a GH-managed ephemeral runner. Only `main` push + version tags can impersonate the deploy SA (PR 1 WIF trust policy), and the runner is destroyed after every job. |

## CI-driven deploy (PR 5)

The same `deploy.sh` runs in two modes:

| Mode | Runs from | Auth | Used by |
| --- | --- | --- | --- |
| **Operator** | Your laptop | `gcloud auth` ADC + osAdminLogin role | Bootstrap, manual hotfixes pre-CI, breakglass |
| **CI** ([.github/workflows/deploy.yml](../../.github/workflows/deploy.yml)) | GH-hosted ubuntu-latest runner | WIF OIDC → impersonate `sa-ci-deploy` (ephemeral 1h token) | Every `v*` tag push + `workflow_dispatch` hotfix |

### Required GitHub repo variables

The CI workflow needs these set in **Settings → Secrets and variables → Actions → Variables**:

| Variable | Example value | Source |
| --- | --- | --- |
| `GCP_ARTIFACT_REGISTRY` | `us-central1-docker.pkg.dev/booking-monitor-sandbox/booking` | `terraform output artifact_registry_url` |
| `GCP_WORKLOAD_IDENTITY_PROVIDER` | `projects/123456789/locations/global/workloadIdentityPools/github-pool/providers/github-provider` | `terraform output workload_identity_provider` |
| `GCP_CI_DEPLOY_SA` | `sa-ci-deploy@booking-monitor-sandbox.iam.gserviceaccount.com` | `terraform output ci_deploy_sa_email` |
| `GCP_PROJECT_ID` | `booking-monitor-sandbox` | Your project ID |
| `GCP_VM_ZONE` | `us-central1-a` | `terraform output vm_zone` |
| `GCP_VM_NAME` | `booking-app` | `terraform output vm_name` |
| `PUBLIC_HOST` | `booking.example.com` | Cloudflare DNS for the tunnel target |
| `VM_PROJECT_DIR` (optional) | `/opt/booking-monitor` | Override if VM uses non-default path |
| `REPO_URL` (optional) | `https://github.com/Leon180/booking_monitor.git` | Override for forks |

Set via gcloud + gh CLI in one shot after PR 1's terraform apply:

```bash
cd deploy/terraform
gh variable set GCP_ARTIFACT_REGISTRY        --body "$(terraform output -raw artifact_registry_url)"
gh variable set GCP_WORKLOAD_IDENTITY_PROVIDER --body "$(terraform output -raw workload_identity_provider)"
gh variable set GCP_CI_DEPLOY_SA              --body "$(terraform output -raw ci_deploy_sa_email)"
gh variable set GCP_PROJECT_ID                --body "$(terraform output -raw project_id)"
gh variable set GCP_VM_ZONE                   --body "$(terraform output -raw vm_zone)"
gh variable set GCP_VM_NAME                   --body "$(terraform output -raw vm_name)"
# Cloudflare hostname is NOT a terraform output — set after `cloudflared.service` is up
gh variable set PUBLIC_HOST --body "booking.your-domain.example.com"
```

### Required: configure the `production` GitHub Environment

The deploy workflow has `environment: production` at the job level. GitHub's Environment-protection settings are what gate manual-dispatch hotfixes — without them, anyone with `Write` on the repo can deploy any image digest. This is **not** an automatic side effect of the workflow; you must configure it once in the GH UI.

1. Repo **Settings → Environments → New environment** → name it `production` exactly.
2. Under **Deployment branches and tags**:
   - Switch from "All branches" to "Selected branches and tags"
   - Add rule: `v*` (matches `v1.0.0`, `v1.2.3-rc1`, etc.)
   - Optionally also add: `main` (for `workflow_dispatch` hotfixes by digest)
3. Under **Required reviewers**: add at least one reviewer (yourself + a teammate, or a security/SRE group). Without this, `workflow_dispatch` deploys execute immediately with no human approval gate.
4. Under **Wait timer**: optional, sets a delay between approval and actual deploy. Useful for production change windows.

Without these settings, the workflow still runs — it just isn't gated. If you're trying out the pipeline solo, that's fine. If you're shipping for real, the GH Environment is the auditable approval boundary.

### Triggering a deploy

- **Normal**: push a `v*` tag (e.g. `git tag v1.0.1 && git push --tags`). The workflow auto-runs after `release.yml` finishes producing the signed image.
- **Hotfix**: GitHub UI → Actions → Deploy → "Run workflow" → fill `image_tag` (e.g. `v1.0.0` to redeploy, or `sha-abc1234` to deploy a previously-built non-tag digest). Subject to `production` environment's required-reviewer gate.

### Verifying a deploy ran

- GitHub UI: **Deployments** tab on the repo home page shows every deploy with state + commit SHA + log link.
- Programmatic: `gh api /repos/Leon180/booking_monitor/deployments?environment=production | jq '.[0]'`
- PR 7's DORA dashboard reads this same API.

### Concurrency behaviour

The workflow uses `concurrency: group: deploy-production, cancel-in-progress: false`. This means:

- One deploy at a time.
- A second tag pushed mid-deploy queues behind the active one.
- A **third** tag pushed pushes the second out of the queue — only the most-recent queued tag actually deploys.

This is safe for our `golang-migrate` forward-only model: `v1.0.3` includes all `v1.0.2`'s migrations + code. Releases skipped this way still satisfy "every released change reaches prod" — they just batch. Document tag-skipped behaviour in release notes ("v1.0.3 includes v1.0.2 changes").

## Tier ceiling — e2-micro can't fit the compose stack (2026-06-01 deep-dive finding)

Empirically verified: the booking_monitor `docker-compose.yml` stack (8 services: postgres + redis + kafka + zookeeper + jaeger + nginx + app + recon/saga/expiry sweepers) **does not fit on a single e2-micro Always-Free VM (1 GB RAM)**.

What we observed during the first-deploy validation session:
- Image pulls from Artifact Registry succeed cleanly
- `secrets_sync.sh` populates `.env` correctly (after PR #150's template+overlay fix)
- `docker compose up -d` starts the lightweight services first (postgres, redis, jaeger, alert_logger, redis_exporter, sweeper sidecars)
- **Kafka + zookeeper never reach a healthy state** — JVM startup competes with everything else for memory
- After ~5 minutes, OOM killer fires; `booking_app` / `booking_kafka` / `booking_nginx` never enter `docker ps`; `cloudflared` (the tunnel) gets killed; SSH becomes unresponsive (sshd evicted)

Approximate per-service RAM budget on this stack:

| Service | RAM |
|---|---|
| kafka (JVM) | 600–800 MB |
| zookeeper (JVM) | 200 MB |
| jaeger | 200 MB |
| postgres | 100 MB |
| app (Go binary) | 50–100 MB |
| recon / saga_watchdog / expiry_sweeper × 3 | ~30 MB each |
| redis | 30 MB |
| nginx | 10 MB |
| alert_logger | 10 MB |
| **Committed** | **~1.3–1.5 GB** |
| **e2-micro usable** | **~0.6 GB (1 GB total minus OS overhead)** |

**Implications**:
- The PRs 1–7 CI/CD chain (build, sign, push, attest, deploy.yml workflow) is **architecturally complete** but lacks a viable live target on Always-Free tier.
- `deploy.yml` is in soft-skip mode (PR #151): when `PUBLIC_HOST` repo variable is empty, the workflow exits cleanly with a notice instead of failing red. The architecture is wired; only the live target is missing.

**Two paths to a working live deploy**:

1. **Resize the VM to e2-small** — `gcloud compute instances set-machine-type booking-app --machine-type=e2-small`. 2 GB RAM, fits the stack. Loses Always-Free; approximately **$14-16/month** for the VM (us-central1 on-demand; sustained-use discount may lower this). Verify current pricing at [GCE pricing](https://cloud.google.com/compute/all-pricing).
2. **Migrate to GKE Autopilot per [docs/k8s-migration-plan.md](../k8s-migration-plan.md)** — node-managed, $0/month cluster fee (Google's free credit covers one cluster). Application pod resource costs depend on requested CPU/memory; for the full Kafka-bearing compose-equivalent workload, expect **~$30-35/month** (vs ~$20/month for a lighter Go-only pod without Kafka). The "correct" answer; PR 8 already planned this.

See [docs/runbooks/cd_completion_checklist.md](cd_completion_checklist.md) for the exact steps to complete CD setup once a tier decision is made.

## What's deferred (PR 6+)

- **Blue-green / canary**: docker-compose has no native support; will be a PR 8 (k8s migration) item.
- **DORA dashboard**: PR 7 reads GitHub Deployments API + adds Grafana annotations.
- **Cosign `--insecure-ignore-tlog` gotcha**: PR 3's release.yml smoke test uses this flag to dodge Rekor inclusion-proof propagation race. Neither the operator path nor the CI path uses it — Rekor lookup is the real trust gate post-publish.
- **Live deploy validation**: blocked by the tier ceiling above. Pick option 1 or 2 to unblock.
