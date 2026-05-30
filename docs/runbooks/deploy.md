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

# Permission to impersonate the deploy SA (for AR pull + Secret Manager read)
gcloud iam service-accounts add-iam-policy-binding \
  "sa-ci-deploy@$PROJECT_ID.iam.gserviceaccount.com" \
  --member="user:$USER_EMAIL" \
  --role="roles/iam.serviceAccountTokenCreator"
```

### 3. Cloudflare Tunnel set up on the VM

See [`cloudflare_tunnel.md`](cloudflare_tunnel.md) for the one-time setup. The deploy script doesn't touch the tunnel — but if the tunnel is down, your `https://booking.<domain>/livez` post-deploy verification won't work.

### 4. PR 1 Terraform applied + PR 3 release workflow run at least once

- PR 1: `make apply` in `deploy/terraform/` to provision GCP infra.
- PR 3: push a `v*.*.*` tag to trigger `.github/workflows/release.yml` and produce at least one signed image in Artifact Registry. See [`release_pipeline.md`](release_pipeline.md) Step 4.

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
  --command="cd /opt/booking-monitor && set -a && source .env && set +a && docker run --rm --network=host -v /opt/booking-monitor/deploy/postgres/migrations:/migrations:ro migrate/migrate:v4.19.1 -path /migrations -database \"\$DATABASE_URL\" up"
```

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
| Deploy script itself compromised | Yes, fully. `deploy.sh` runs on operator machine, can be tampered with. PR 5 wraps this in `google-github-actions/ssh-compute@v2` for the CI-driven path, where the script lives in a GH-managed isolated runner. |

## What's deferred (PR 5+)

- **CI-triggered deploy**: PR 5 wires this into `.github/workflows/deploy.yml` using `google-github-actions/ssh-compute@v2`. Operator no longer SSHes; deploys happen on tag push.
- **Blue-green / canary**: docker-compose has no native support; will be a PR 8 (k8s migration) item.
- **Deploy markers in Prometheus**: PR 7 (DORA dashboard) adds annotations on the existing Grafana boards.
- **Cosign `--insecure-ignore-tlog` gotcha**: PR 3's release.yml smoke test uses this flag to dodge Rekor inclusion-proof propagation race. The operator-side `deploy.sh` does NOT — Rekor lookup is the real trust gate.
