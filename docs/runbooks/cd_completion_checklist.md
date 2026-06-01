# CD completion checklist

> Recipe for finishing the CD setup once a **tier decision** is made (resize VM or migrate to GKE). Today, `deploy.yml` is in **soft-skip mode** because `PUBLIC_HOST` is unset and the e2-micro tier can't fit the compose stack (see [docs/runbooks/deploy.md § Tier ceiling](deploy.md)).
>
> This document captures the operator steps so future-you (or a new teammate) can finish the work in ~30–60 minutes without re-discovery.

## Pre-flight

- [ ] PR #149 + PR #150 are both merged on `main`
- [ ] Tier decision made:
  - **Option A**: resize VM to `e2-small` (~$13/month, 2 GB RAM)
  - **Option B**: apply `deploy/k8s-terraform/` to provision GKE Autopilot (~$30-35/month, see [k8s-migration-plan.md](../k8s-migration-plan.md))
- [ ] A domain available — either:
  - Cloudflare-managed `.com` from Cloudflare Registrar (~$10.44/year) for a stable named tunnel
  - OR accept ephemeral `*.trycloudflare.com` URL (need to update `PUBLIC_HOST` after every cloudflared restart)

## Option A — Resize to e2-small + finish CD on VM

```bash
# 1. Stop VM (resize requires stopped state)
gcloud compute instances stop booking-app --zone=us-central1-a

# 2. Resize
gcloud compute instances set-machine-type booking-app \
  --zone=us-central1-a \
  --machine-type=e2-small

# 3. Start
gcloud compute instances start booking-app --zone=us-central1-a
```

Then continue from step "First deploy on VM" below.

## Option B — Apply GKE Autopilot

See [docs/k8s-migration-plan.md](../k8s-migration-plan.md) + [docs/runbooks/k8s_migration.md](k8s_migration.md). After GKE apply, the CD workflow needs to be replaced from "SSH deploy.sh" to "ArgoCD ApplicationSet" — a separate piece of work tracked in the plan.

For this checklist's scope, Option A (resize VM) is the smaller leap.

## First deploy on VM (after resize)

Once `gcloud compute instances describe booking-app --format='value(machineType)'` shows e2-small:

```bash
# 1. SSH to the VM
gcloud compute ssh booking-app --zone=us-central1-a --tunnel-through-iap

# 2. (on VM) update + start the stack
cd /opt/booking-monitor
sudo git pull --ff-only
sudo bash scripts/secrets_sync.sh           # PR #150's overlay+template path

DIGEST="sha256:<latest from `gcloud artifacts docker images describe ...`>"
IMAGE_REF="us-central1-docker.pkg.dev/booking-monitor-sandbox/booking/booking-monitor@${DIGEST}"
sudo gcloud auth configure-docker us-central1-docker.pkg.dev --quiet
sudo docker pull "$IMAGE_REF"
sudo BOOKING_IMAGE="$IMAGE_REF" docker compose up -d --wait --wait-timeout 300

# 3. verify stack is healthy
sudo docker compose ps                       # all "Up X (healthy)"
curl -fsS http://localhost/livez && curl -fsS http://localhost/readyz
```

If any container shows `unhealthy` for more than 5 minutes:
- `sudo docker logs <container>` — check what's failing
- e2-small still tight on RAM; check `free -h` → if <100 MB free, may need e2-medium (~$25/month)

## Cloudflare Tunnel setup

### Named tunnel (if you have a Cloudflare-managed domain — preferred)

```bash
# on VM
sudo cloudflared tunnel login                            # opens browser; pick your CF account
sudo cloudflared tunnel create booking-monitor-prod      # outputs tunnel UUID
sudo cloudflared tunnel route dns booking-monitor-prod booking.<your-domain>

# write config
sudo tee /etc/cloudflared/config.yml > /dev/null <<'YML'
tunnel: <UUID-from-create>
credentials-file: /etc/cloudflared/<UUID>.json
ingress:
  - hostname: booking.<your-domain>
    service: http://localhost:80
  - service: http_status:404
YML

sudo systemctl restart cloudflared            # or `cloudflared service install` if first time

# verify
curl -fsS https://booking.<your-domain>/readyz
```

### Quick tunnel (if no domain — ephemeral URL, gets a new URL each restart)

```bash
# on VM (already done in 2026-06-01 session, but URL is gone after OOM)
sudo systemctl restart cloudflared-quick
sleep 8
sudo grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' /var/log/cloudflared.log | head -1
# Copy this URL — it'll be your PUBLIC_HOST
```

Caveat: the quick-tunnel URL **changes** every time cloudflared restarts. Each change requires updating `PUBLIC_HOST` in the repo variables. Acceptable for "demo right now in this interview"; not for "list on resume permanently."

## Set repo variable + verify

```bash
# from your laptop
gh variable set PUBLIC_HOST --body "booking.<your-domain>"   # or the trycloudflare URL

# verify all 7 vars
gh variable list
# expect: GCP_ARTIFACT_REGISTRY, GCP_WORKLOAD_IDENTITY_PROVIDER, GCP_CI_DEPLOY_SA,
#         GCP_PROJECT_ID, GCP_VM_ZONE, GCP_VM_NAME, PUBLIC_HOST
```

## Validate the full chain via tag push

```bash
# Push an rc tag to test (won't promote to :latest)
git tag v1.3.2-rc1
git push origin v1.3.2-rc1
```

Then watch:

```bash
gh run watch                                  # follow the in-flight run
# or:
gh run list --workflow=release.yml --limit 1
gh run list --workflow=deploy.yml --limit 1
```

Expected outcome:
- ✅ `release.yml` on the tag — build + Trivy + push + attest, green
- ✅ `deploy.yml` on the tag — verify attestations + ssh-deploy + external smoke (now hitting your live PUBLIC_HOST), green

If deploy.yml fails at the smoke step:
- Check `curl -v https://<PUBLIC_HOST>/readyz` from your laptop — if 503, the VM stack has an unhealthy dependency
- Check `gcloud compute ssh booking-app -- 'sudo docker compose ps'` — services should all be healthy
- Check cloudflared: `sudo journalctl -u cloudflared -n 20`

## Post-validation cleanup

After confirming the chain works:

```bash
# Delete the rc tag (artifacts in AR can stay; cheap)
git tag -d v1.3.2-rc1
git push origin :refs/tags/v1.3.2-rc1
gcloud artifacts docker tags delete \
  "us-central1-docker.pkg.dev/booking-monitor-sandbox/booking/booking-monitor:v1.3.2-rc1" --quiet || true
```

Update [docs/runbooks/deploy.md](deploy.md) § "Tier ceiling" with the resolution ("resized to e2-small on YYYY-MM-DD" or "migrated to GKE on YYYY-MM-DD").

## Cost guardrail (if you went with Option A)

```bash
# Set a budget alert at $25/mo to catch unexpected spend
gcloud beta billing budgets create \
  --billing-account="$(gcloud beta billing accounts list --format='value(name)' | head -1)" \
  --display-name="booking-monitor sandbox" \
  --budget-amount=25USD \
  --threshold-rule=percent=0.5 \
  --threshold-rule=percent=0.9 \
  --threshold-rule=percent=1.0 \
  --filter-projects=projects/<your-project-number>
```

## What this checklist is NOT

This is a **VM-mode CD completion** recipe. If you decide to skip directly to GKE Autopilot (Option B), follow [docs/runbooks/k8s_migration.md](k8s_migration.md) instead — that path has its own bigger checklist + cost profile + replaces `deploy.yml`'s SSH path with ArgoCD.

## When to use this

The deploy.yml soft-skip in PR #151 means red CI banners are gone. You can defer CD completion indefinitely without it bothering you. Triggers to actually complete:

- Job-hunting starts → live URL becomes portfolio-critical → do this checklist
- Want to write a blog post / give a talk → screenshot of a green deploy.yml is the proof point
- Six months pass since the last touch → finish before context fades
