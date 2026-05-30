#!/bin/bash
# VM first-boot bootstrap. Idempotent: re-runs on every restart and must
# converge to the same end state. Test by `gcloud compute instances reset
# booking-app` after editing.
#
# Production hardening applied:
#   * Docker pinned to 28.x via apt preferences + apt-mark hold
#     (29.x containing breaking format changes won't sneak in via
#      unattended-upgrades / `apt-get upgrade`)
#   * unattended-upgrades configured to apply Debian-Security ONLY
#     (no blind feature upgrades; Docker explicitly blocklisted from
#      unattended-upgrades to keep version control deterministic)
#   * chrony for NTP — required for log correlation + TLS cert validity
#   * auditd for OS-level audit logging
#   * ufw + fail2ban as defense-in-depth on top of GCP firewall
#
# What this script does NOT do:
#   * Pull and start the app container. That's PR 4's deploy.sh.
#   * Install cloudflared CONFIG. Binary installed here; config in PR 4.
#   * Set up Prometheus host metrics. That's PR 7.
#
# Long-term note: this is Debian 12 with security hardening layered on.
# The production-grade alternative is Container-Optimized OS (COS),
# which Google maintains as a minimal locked-down container host. We
# defer the COS migration to PR 8 (k8s plan) since by then we'll be
# using GKE nodes which run COS by default — bundling the migration
# with the k8s move means we learn one OS-level abstraction at a time.

set -euo pipefail
exec > >(tee -a /var/log/bootstrap.log) 2>&1

echo "=== bootstrap start: $(date -Iseconds) ==="

export DEBIAN_FRONTEND=noninteractive

# --------------------------------------------------------------------
# 1. Refresh apt index. Notice we do NOT run `apt-get upgrade -y`.
#    That blind upgrade is a production antipattern — it pulls feature
#    + breaking changes alongside security fixes. Security-only updates
#    are delegated to unattended-upgrades configured below.
# --------------------------------------------------------------------
apt-get update

# --------------------------------------------------------------------
# 2. Base tools + the hardening trio (unattended-upgrades, chrony, auditd).
# --------------------------------------------------------------------
apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  gnupg \
  ufw \
  fail2ban \
  jq \
  unattended-upgrades \
  apt-listchanges \
  chrony \
  auditd \
  audispd-plugins

# --------------------------------------------------------------------
# 3. unattended-upgrades — Debian-Security ONLY.
#    Docker packages explicitly blocklisted because Docker is version-
#    pinned by us (Section 4); we don't want unattended-upgrades to
#    fight the pin file.
# --------------------------------------------------------------------
cat > /etc/apt/apt.conf.d/50unattended-upgrades <<'EOF'
Unattended-Upgrade::Origins-Pattern {
    "origin=Debian,codename=${distro_codename},label=Debian-Security";
    "origin=Debian,codename=${distro_codename}-security,label=Debian-Security";
};

// Packages we explicitly version-control elsewhere — do not auto-upgrade.
Unattended-Upgrade::Package-Blacklist {
    "docker-ce";
    "docker-ce-cli";
    "containerd.io";
    "docker-buildx-plugin";
    "docker-compose-plugin";
};

Unattended-Upgrade::AutoFixInterruptedDpkg "true";
Unattended-Upgrade::MinimalSteps "true";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";

// Do NOT auto-reboot — Compose stack restart needs operator awareness.
Unattended-Upgrade::Automatic-Reboot "false";

Unattended-Upgrade::SyslogEnable "true";
EOF

cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::Download-Upgradeable-Packages "1";
APT::Periodic::AutocleanInterval "7";
EOF

systemctl enable --now unattended-upgrades.service
systemctl enable --now chrony
systemctl enable --now auditd
systemctl enable --now fail2ban

# --------------------------------------------------------------------
# 4. Docker — pinned to 28.x + apt-mark hold (belt and suspenders).
# --------------------------------------------------------------------

# 4a. Docker official GPG key (verifies repo signatures).
if [ ! -f /etc/apt/keyrings/docker.gpg ]; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg \
    | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
fi

# 4b. Docker apt repo.
if [ ! -f /etc/apt/sources.list.d/docker.list ]; then
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    > /etc/apt/sources.list.d/docker.list
fi

# 4c. Pin file — Pin-Priority 1001 overrides apt's default 500, forcing
# the chosen version even when a higher one is available. Pattern
# `5:28.*` accepts any 28.x patch (security fixes welcome) but rejects
# 29.x (potentially breaking format changes blocked).
cat > /etc/apt/preferences.d/docker-ce.pref <<'EOF'
Package: docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
Pin: version 5:28.*
Pin-Priority: 1001
EOF

apt-get update

apt-get install -y --no-install-recommends \
  docker-ce \
  docker-ce-cli \
  containerd.io \
  docker-buildx-plugin \
  docker-compose-plugin

# 4d. Belt and suspenders: if someone removes the pin file later, the
# hold still prevents `apt-get upgrade docker-ce` from running. To
# upgrade Docker intentionally, run `apt-mark unhold ... && apt-get
# install docker-ce=<new-version>`.
apt-mark hold docker-ce docker-ce-cli containerd.io \
              docker-buildx-plugin docker-compose-plugin

systemctl enable --now docker

# --------------------------------------------------------------------
# 5. cloudflared (Cloudflare Tunnel daemon).
#    NOTE: binary fetched from `latest` tag, NOT pinned. PR 4 wires the
#    proper tunnel config + systemd service + verification step. Don't
#    run production traffic through cloudflared installed here without
#    completing PR 4's setup first.
# --------------------------------------------------------------------
if [ ! -x /usr/local/bin/cloudflared ]; then
  ARCH=$(dpkg --print-architecture)
  curl -fsSL -o /usr/local/bin/cloudflared \
    "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${ARCH}"
  chmod +x /usr/local/bin/cloudflared
fi
mkdir -p /etc/cloudflared

# --------------------------------------------------------------------
# 6. Host firewall — defense-in-depth on top of GCP firewall.
# --------------------------------------------------------------------
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp comment "SSH (IAP-only at GCP firewall layer)"
ufw --force enable

# --------------------------------------------------------------------
# 7. Marker for deploy.sh to verify bootstrap completed.
# --------------------------------------------------------------------
mkdir -p /var/lib/booking-monitor
date -Iseconds > /var/lib/booking-monitor/bootstrap_completed_at

echo "=== bootstrap done: $(date -Iseconds) ==="
