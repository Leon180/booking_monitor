# Workload Identity Federation — the supply-chain security foundation.
#
# See docs/runbooks/gcp_bootstrap.md §"Why split SA" for the threat-model
# rationale behind the two-SA pattern below. Short version:
#
#   sa-ci-readonly  → any workflow in this repo can impersonate.
#                     Read-only IAM. Used by PR / feature-branch builds.
#
#   sa-ci-deploy    → only main branch + version tags can impersonate.
#                     Write to Artifact Registry + read Secret Manager.
#
# Provider's attribute_condition is the FIRST gate (owner check); the
# real per-action permission split lives in the SA IAM bindings further
# down. This layering means: tightening trust is a one-line IAM change,
# not a provider reconfiguration.
#
# Verified against hashicorp/google v7.34 provider docs (MCP query
# `google_iam_workload_identity_pool_provider`, doc 12373909):
#   * `principalSet://...attribute.{name}/{value}` is EXACT EQUALITY
#     on the mapped attribute's value — NOT prefix match. The original
#     binding `attribute.ref/refs/tags/v` would only match a literal
#     `refs/tags/v` ref, which never occurs (real tags are `refs/tags/v1.0.0`
#     etc). Fixed via the CEL-derived `attribute.deploy_eligible` below.
#   * Built-in immutable claims preferred over mutable ones: GitHub's
#     `repository_owner_id` and `repository_id` survive owner/repo
#     renames. Mapped + used as the strongest first gate.

resource "google_iam_workload_identity_pool" "github" {
  project                   = local.project_id
  workload_identity_pool_id = "pool-github"
  display_name              = "GitHub Actions"
  description               = "OIDC federation pool for GitHub Actions. Tokens are issued by GitHub's OIDC IdP and exchanged for short-lived GCP tokens via STS."

  depends_on = [google_project_service.apis]
}

resource "google_iam_workload_identity_pool_provider" "github" {
  project                            = local.project_id
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "prov-github"
  display_name                       = "GitHub Actions OIDC"

  # Map JWT claims into GCP attributes. Only claims mapped here are
  # available for use in IAM bindings (`attribute.X`). Map liberally
  # at provider-creation time; adding later requires a destroy+recreate.
  #
  # Why each mapping:
  #   google.subject              — required mapping per provider docs
  #   attribute.repository        — display / debug; not used in IAM
  #   attribute.repository_id     — IMMUTABLE numeric id; survives renames
  #                                 (GitHub Actions security guidance 2024+)
  #   attribute.repository_owner_id — IMMUTABLE numeric owner id
  #   attribute.ref               — display only; do NOT bind on this
  #                                 directly (see deploy_eligible below)
  #   attribute.workflow_ref      — reserved for future supply-chain
  #                                 hardening (pin which workflow file)
  #   attribute.event_name        — push / pull_request / etc.
  #   attribute.deploy_eligible   — CEL-derived: "yes" for main push or
  #                                 v-tag push; "no" otherwise. This is
  #                                 the attribute the deploy SA binds on.
  attribute_mapping = {
    "google.subject"                = "assertion.sub"
    "attribute.repository"          = "assertion.repository"
    "attribute.repository_id"       = "assertion.repository_id"
    "attribute.repository_owner_id" = "assertion.repository_owner_id"
    "attribute.ref"                 = "assertion.ref"
    "attribute.workflow_ref"        = "assertion.workflow_ref"
    "attribute.event_name"          = "assertion.event_name"
    "attribute.deploy_eligible"     = "assertion.ref == \"refs/heads/main\" || assertion.ref.startsWith(\"refs/tags/v\") ? \"yes\" : \"no\""
  }

  # First defence: reject tokens issued for any other GitHub owner.
  # Uses the IMMUTABLE owner id (not the name) so an owner rename
  # doesn't silently change the trust boundary. The numeric id is
  # looked up via `gh api /users/<owner> --jq .id` at apply time
  # and stored in the var.github_owner_id variable.
  #
  # CEL string literals MUST use double quotes (escaped in HCL).
  attribute_condition = "assertion.repository_owner_id == \"${var.github_owner_id}\""

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

# ----- Readonly SA: PR + branch builds -----

resource "google_service_account" "ci_readonly" {
  project      = local.project_id
  account_id   = "sa-ci-readonly"
  display_name = "CI read-only"
  description  = "Impersonated by PR / feature-branch GH Actions workflows. Read-only access to Artifact Registry."
}

# Any workflow in the target repo (matched by immutable id) can
# impersonate this SA. Gating is at the IAM layer (what you can DO),
# not the auth layer (whether you CAN at all).
#
# `_iam_binding` is AUTHORITATIVE for this role on this SA — other
# members of `roles/iam.workloadIdentityUser` get replaced on apply.
# That's intentional: Terraform owns who can impersonate this SA.
resource "google_service_account_iam_binding" "ci_readonly_wif" {
  service_account_id = google_service_account.ci_readonly.name
  role               = "roles/iam.workloadIdentityUser"

  members = [
    "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository_id/${var.github_repo_id}",
  ]
}

# ----- Deploy SA: main + v-tag only -----

resource "google_service_account" "ci_deploy" {
  project      = local.project_id
  account_id   = "sa-ci-deploy"
  display_name = "CI deploy"
  description  = "Impersonated only by main-branch and v-tag workflows. Writes Artifact Registry, reads Secret Manager."
}

# Deploy gating uses the CEL-derived `attribute.deploy_eligible/yes`
# binding. The original two-binding design used a literal
# `attribute.ref/refs/tags/v` which is exact-match (verified in
# provider docs) and silently matched no tags. Collapsing to a
# single derived attribute is both correct AND clearer about
# intent ("eligible to deploy" is the semantic, not "ref equals X").
#
# Why _iam_binding (authoritative for the role): same rationale as
# readonly — Terraform is the source of truth for who can impersonate
# the deploy SA. A ClickOps-added member would be wiped on next apply.
resource "google_service_account_iam_binding" "ci_deploy_wif" {
  service_account_id = google_service_account.ci_deploy.name
  role               = "roles/iam.workloadIdentityUser"

  members = [
    "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.deploy_eligible/yes",
  ]
}

# ----- Cross-PR setup: IAP tunnel access for the deploy SA -----
#
# PR 5's CD workflow will `gcloud compute ssh --tunnel-through-iap`
# to push deploys onto the VM. Without `roles/iap.tunnelResourceAccessor`
# on the deploy SA, that SSH call fails with PERMISSION_DENIED.
# Granting it here in PR 1 keeps the trust graph declarative —
# nothing in PR 5 needs to surgically grant runtime permissions.
resource "google_project_iam_member" "ci_deploy_iap_tunnel" {
  project = local.project_id
  role    = "roles/iap.tunnelResourceAccessor"
  member  = "serviceAccount:${google_service_account.ci_deploy.email}"
}

# `roles/compute.osAdminLogin` lets the SA log into the VM via OS Login
# AND grants passwordless sudo. Without this, `gcloud compute ssh
# --tunnel-through-iap` from the deploy workflow returns
# `PERMISSION_DENIED: ... user is not in the sudoers file`. Added in
# PR 5 — IAP role alone is necessary but not sufficient for SSH; OS
# Login completes the pair (IAP opens the tunnel, OS Login authorizes
# the Linux session inside it).
#
# Why osAdminLogin (sudo) not the lower osLogin (no sudo):
# OS Login creates a fresh Linux user dynamically on first SSH; that
# user is NOT in the `docker` group on the VM, so `docker pull` and
# `docker compose up` in deploy.sh would fail with "permission denied
# on /var/run/docker.sock". Honest paths considered:
#   (a) osAdminLogin → `sudo docker ...` works directly        ← chosen
#   (b) osLogin + cloud-init PAM hook to auto-add new OS Login
#       users to `docker` group  (niche, hard to test)
#   (c) Docker rootless mode      (broad refactor, breaks PR 1+2)
#   (d) osLogin + /etc/sudoers.d/booking-deploy allowlist pinning the
#       SA to a closed set of binaries (docker, git, bash secrets_sync)
#       — TIGHTER than (a) for least-privilege; deferred for PR-scope
#       management (deploy.sh uses ~6 distinct sudo command shapes
#       today, all would need pinning + drift discipline).
# The trust boundary that matters is the WIF trust policy: only
# `attribute.deploy_eligible/yes` (main push or tag) can impersonate
# this SA. Linux sudo on the VM is downstream of that gate, so
# granting it here doesn't expand the threat model meaningfully —
# *for the startup / portfolio-project tier we're targeting*. Regulated
# / audit-bound shops should prefer (d) or move to GKE entirely so
# this whole class of role disappears.
#
# If we ever pivot to k8s (PR 8), this role goes away — the CD agent
# would auth to the cluster API instead of SSHing into a Linux box.
resource "google_project_iam_member" "ci_deploy_os_admin_login" {
  project = local.project_id
  role    = "roles/compute.osAdminLogin"
  member  = "serviceAccount:${google_service_account.ci_deploy.email}"
}

# CRIT-C4 (review round 1, terraform agent): the deploy SA SSHs into a
# VM that runs as `sa-app-runtime`. `gcloud compute ssh` to a VM with
# an attached service account requires `iam.serviceAccounts.actAs` on
# THAT runtime SA — the SSH operation is treated as "starting a process
# as the VM's SA identity" (the Google Guest Agent uses actAs to drop
# privileges to the runtime SA's context for OS Login user lookups).
# Without it, first deploy fails with
#   PERMISSION_DENIED: ... does not have actAs permission on
#   sa-app-runtime@<project>.iam.gserviceaccount.com
#
# `_iam_member` (non-authoritative) so this composes with any other
# actAs grants made later (operator user impersonating, k8s WI when
# we eventually pivot). Matches the project convention.
resource "google_service_account_iam_member" "ci_deploy_actas_app_runtime" {
  service_account_id = google_service_account.app_runtime.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.ci_deploy.email}"
}
