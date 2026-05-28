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
  attribute_mapping = {
    "google.subject"             = "assertion.sub"
    "attribute.actor"            = "assertion.actor"
    "attribute.repository"       = "assertion.repository"
    "attribute.repository_owner" = "assertion.repository_owner"
    "attribute.ref"              = "assertion.ref"
    "attribute.workflow_ref"     = "assertion.workflow_ref"
    "attribute.event_name"       = "assertion.event_name"
  }

  # First defence: reject tokens for any other GitHub owner. This is
  # CEL syntax (NOT raw SQL). Use double quotes — CEL strings.
  attribute_condition = "assertion.repository_owner == \"${var.github_owner}\""

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

# Any workflow in the target repo can impersonate this SA — gating
# is at the IAM (what you can DO) layer, not at the auth (whether you
# CAN at all) layer.
resource "google_service_account_iam_binding" "ci_readonly_wif" {
  service_account_id = google_service_account.ci_readonly.name
  role               = "roles/iam.workloadIdentityUser"

  members = [
    "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository/${var.github_owner}/${var.github_repo}",
  ]
}

# ----- Deploy SA: main + tag only -----

resource "google_service_account" "ci_deploy" {
  project      = local.project_id
  account_id   = "sa-ci-deploy"
  display_name = "CI deploy"
  description  = "Impersonated only by main-branch and version-tag workflows. Writes Artifact Registry, reads Secret Manager."
}

# Two binding entries:
#   1. principal:// (exact subject) for main-branch push.
#   2. principalSet:// (attribute prefix) for tags starting with `v`.
#
# Why two entries instead of `assertion.ref.startsWith(...)` in the
# provider's attribute_condition? Because attribute_condition gates
# ALL impersonations through this provider; tightening it would lock
# out the readonly SA path. The right layer for "who can deploy" is
# this SA's IAM binding.
resource "google_service_account_iam_binding" "ci_deploy_wif" {
  service_account_id = google_service_account.ci_deploy.name
  role               = "roles/iam.workloadIdentityUser"

  members = [
    "principal://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/subject/repo:${var.github_owner}/${var.github_repo}:ref:refs/heads/main",
    "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.ref/refs/tags/v",
  ]
}
