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
