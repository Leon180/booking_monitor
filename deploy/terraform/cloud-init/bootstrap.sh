#!/bin/bash
# VM first-boot bootstrap. Idempotent: re-runs on every restart and
# must converge to the same end state. Test by `gcloud compute instances
# reset booking-app` after editing.
#
# What this script does NOT do:
#   - Pull and start the app container. That's PR 4's deploy.sh.
#   - Install cloudflared config. That's PR 4's runbook.
#   - Set up monitoring agents. That's PR 7.
#
# What this script DOES do:
#   - Install Docker Engine + compose plugin from the official repo.
#   - Install cloudflared binary (config bootstrapped separately).
#   - Set up ufw with deny-all-inbound + fail2ban for SSH.

set -euo pipefail

# Capture both stdout + stderr to a known log for `gcloud compute ssh
# ... 'sudo cat /var/log/bootstrap.log'` post-mortem.
exec > >(tee -a /var/log/bootstrap.log) 2>&1

echo "=== bootstrap start: $(date -Iseconds) ==="

# Re-running is fine — apt is idempotent on the install step, and the
# repo-add lines below check for existing files before clobbering.

# ----- System update + base tools -----
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get upgrade -y --no-install-recommends
apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  gnupg \
  ufw \
  fail2ban \
  jq

# ----- Docker Engine (official repo) -----
if [ ! -f /etc/apt/keyrings/docker.gpg ]; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg \
    | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
fi

if [ ! -f /etc/apt/sources.list.d/docker.list ]; then
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update
fi

apt-get install -y --no-install-recommends \
  docker-ce \
  docker-ce-cli \
  containerd.io \
  docker-buildx-plugin \
  docker-compose-plugin

systemctl enable --now docker

# ----- cloudflared (Cloudflare Tunnel daemon) -----
if [ ! -x /usr/local/bin/cloudflared ]; then
  ARCH=$(dpkg --print-architecture)
  curl -fsSL -o /usr/local/bin/cloudflared \
    "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${ARCH}"
  chmod +x /usr/local/bin/cloudflared
fi

# Config dir — actual config.yml + credentials JSON deployed in PR 4
# via `gcloud compute scp` from operator's machine after they've run
# `cloudflared tunnel create` locally.
mkdir -p /etc/cloudflared

# ----- Firewall -----
# Deny everything inbound by default. GCP firewall already gates this
# at network level, but defense-in-depth: a misconfigured GCP rule
# shouldn't blow our security model.
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp comment "SSH (IAP-only at GCP firewall layer)"
ufw --force enable

# ----- fail2ban for SSH brute force protection -----
systemctl enable --now fail2ban

# ----- Marker file so deploy.sh can verify bootstrap completed -----
mkdir -p /var/lib/booking-monitor
date -Iseconds > /var/lib/booking-monitor/bootstrap_completed_at

echo "=== bootstrap done: $(date -Iseconds) ==="
