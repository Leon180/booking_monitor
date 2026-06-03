# Cloudflare Tunnel — operator setup runbook

One-time setup to put the booking app on the public internet via Cloudflare's edge, with **no inbound ports open on the VM** and **no DNS record exposing the VM's IP**.

## Why Cloudflare Tunnel (vs. nginx + Let's Encrypt)

| Aspect | nginx + LE | Cloudflare Tunnel (this setup) |
| --- | --- | --- |
| Inbound ports | 80, 443 open to internet | None — VM only opens outbound TCP to CF edge |
| DNS A record | Points to VM IP | CNAME to `<UUID>.cfargotunnel.com` (IP hidden) |
| TLS cert | LE renewal cron | CF edge cert; you don't manage TLS at all |
| DDoS protection | None by default | Built-in (CF WAF + L3/L4 mitigation) |
| Cost | $0 (LE) + cert renewal complexity | $0 (CF Tunnel free tier) |
| GitOps-friendly | Yes (nginx config in repo) | Yes (this runbook's `config.yml` is in repo as `.example`) |

## Why we chose **locally-managed** (not Cloudflare's 2026-recommended remote-managed)

As of 2026, Cloudflare officially recommends **remote-managed tunnels** (config lives in CF dashboard; `cloudflared` just needs a `--token`). We deliberately chose locally-managed for:

- **Config in repo** (`deploy/cloudflared/config.yml.example`) — reviewable on PR, drift detectable, replayable
- **No external dependency** on the CF dashboard for tunnel config changes
- **Audit trail** via git history

Trade-off: one extra file (`config.yml`) per VM to keep in sync with what's deployed. Acceptable for our 1-VM scope.

## One-time setup

### Step 1 — Operator machine: `cloudflared` install + login

```bash
# macOS
brew install cloudflared

# Linux (if you don't have a package manager option)
curl -L -o /usr/local/bin/cloudflared \
  https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64
chmod +x /usr/local/bin/cloudflared

# Browser-based login. Picks the Cloudflare account whose domain you'll
# use for the tunnel. Drops a cert.pem at ~/.cloudflared/cert.pem.
cloudflared tunnel login
```

The login command opens your browser → log into Cloudflare → pick the **zone** (domain) the tunnel will serve → confirm. cloudflared writes the auth cert to `~/.cloudflared/cert.pem`.

### Step 2 — Create the tunnel (gets you a UUID + credentials JSON)

```bash
TUNNEL_NAME="booking-monitor"
cloudflared tunnel create "$TUNNEL_NAME"
```

Outputs something like:
```
Tunnel credentials written to /Users/.../.cloudflared/abc12345-def6-...-7890.json
Created tunnel booking-monitor with id abc12345-def6-...-7890
```

**Save these two values** — you'll need them in Step 4:
- Tunnel UUID: `abc12345-def6-...-7890`
- Credentials JSON path: `~/.cloudflared/<UUID>.json`

### Step 3 — Route a public DNS hostname to the tunnel

```bash
# Replace with your actual hostname (must be in a zone you control on CF)
PUBLIC_HOSTNAME="booking.example.com"

cloudflared tunnel route dns "$TUNNEL_NAME" "$PUBLIC_HOSTNAME"
```

This creates a CNAME `booking.example.com` → `<UUID>.cfargotunnel.com` in your CF DNS zone. CF automatically marks it proxied (orange cloud).

### Step 4 — Prepare the VM config locally

```bash
# In your booking_monitor repo
cp deploy/cloudflared/config.yml.example deploy/cloudflared/config.yml
$EDITOR deploy/cloudflared/config.yml
```

Substitute:
- `<TUNNEL_UUID>` → the UUID from Step 2
- `<YOUR_HOSTNAME>` → `booking.example.com` (or whatever you used in Step 3)

`config.yml` is gitignored (the `.example` is the only committed version). Confirm with `git status`.

### Step 5 — Push the config + credentials to the VM

```bash
# Variables
PROJECT_ID="booking-monitor-sandbox"     # match terraform.tfvars
ZONE="us-central1-a"
TUNNEL_UUID="abc12345-def6-...-7890"     # from Step 2

# Create the cloudflared system user on the VM (one-time)
gcloud compute ssh booking-app --zone="$ZONE" --tunnel-through-iap \
  --command="sudo useradd --system --no-create-home --shell /usr/sbin/nologin cloudflared || true && sudo mkdir -p /etc/cloudflared && sudo chown -R cloudflared:cloudflared /etc/cloudflared"

# Push config + credentials
gcloud compute scp --zone="$ZONE" --tunnel-through-iap \
  deploy/cloudflared/config.yml \
  booking-app:/tmp/cloudflared-config.yml

gcloud compute scp --zone="$ZONE" --tunnel-through-iap \
  ~/.cloudflared/"${TUNNEL_UUID}.json" \
  booking-app:/tmp/cloudflared-creds.json

# Move into /etc/cloudflared (root-only writable)
gcloud compute ssh booking-app --zone="$ZONE" --tunnel-through-iap \
  --command="sudo install -m 644 -o cloudflared -g cloudflared /tmp/cloudflared-config.yml /etc/cloudflared/config.yml && sudo install -m 600 -o cloudflared -g cloudflared /tmp/cloudflared-creds.json /etc/cloudflared/${TUNNEL_UUID}.json && rm /tmp/cloudflared-config.yml /tmp/cloudflared-creds.json"
```

### Step 6 — Install + start the systemd unit

```bash
# Push the systemd unit
gcloud compute scp --zone="$ZONE" --tunnel-through-iap \
  deploy/cloudflared/cloudflared.service \
  booking-app:/tmp/cloudflared.service

# Install + start
gcloud compute ssh booking-app --zone="$ZONE" --tunnel-through-iap \
  --command="sudo install -m 644 -o root -g root /tmp/cloudflared.service /etc/systemd/system/cloudflared.service && sudo systemctl daemon-reload && sudo systemctl enable --now cloudflared && rm /tmp/cloudflared.service"
```

### Step 7 — Verify

```bash
# On VM via SSH
gcloud compute ssh booking-app --zone="$ZONE" --tunnel-through-iap --command="sudo systemctl status cloudflared --no-pager"
# Expect: Active: active (running) + logs showing "Registered tunnel connection"

# Public HTTPS check — but no app deployed YET, so 502/503 is expected
# until you run `make deploy IMAGE_TAG=...` from docs/runbooks/deploy.md.
curl -I "https://booking.example.com/livez"
# Once app is deployed:
#   200 OK + cf-ray header → tunnel works end-to-end
```

## Rotating tunnel credentials

If credentials JSON leaks:

```bash
# Operator machine: delete + recreate
cloudflared tunnel delete "$TUNNEL_NAME"    # destroys old credentials server-side
cloudflared tunnel create "$TUNNEL_NAME"    # generates fresh UUID + creds
cloudflared tunnel route dns "$TUNNEL_NAME" "$PUBLIC_HOSTNAME"

# Then re-do Steps 4-6 with the NEW UUID
```

## Teardown

```bash
# On VM
gcloud compute ssh booking-app --zone="$ZONE" --tunnel-through-iap --command="sudo systemctl disable --now cloudflared && sudo rm -rf /etc/cloudflared"

# Operator machine
cloudflared tunnel delete "$TUNNEL_NAME"

# Then go to CF dashboard → DNS → delete the CNAME for $PUBLIC_HOSTNAME
```

## Threat model — what this setup covers (and doesn't)

| Threat | Mitigated by | Notes |
| --- | --- | --- |
| Port scan finding the VM | Yes — VM has no public inbound port (port 22 is IAP-only per PR 1) | |
| DDoS at L3/L4 | Yes — CF edge absorbs | |
| DDoS at L7 (HTTP flood) | Partial — depends on CF plan + app-side rate limit (nginx in compose, PR 4 unchanged from existing) | |
| Operator's cert.pem leaks (`~/.cloudflared/cert.pem`) | Detectable — `cloudflared tunnel list` shows all tunnels the cert can manage; attacker would need to also have CF dashboard access to do meaningful damage | |
| Tunnel credentials JSON leaks (`/etc/cloudflared/<UUID>.json`) | Rotate per "Rotating credentials" section above. Until rotated, attacker can impersonate this tunnel and serve their own content on `booking.example.com`. **HIGH severity** — keep this file at mode 600, never check in | |
| Tunnel UUID leaks | No actionable risk — UUID alone is just a name | |
| App-layer compromise | NOT mitigated — once a request reaches your app via the tunnel, it's a normal HTTP request. Defense lives in the app (auth, rate-limit, idempotency-key) | |
