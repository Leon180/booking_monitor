# Secret containers only — values are NOT managed by Terraform.
#
# Rationale: terraform state contains every attribute of every managed
# resource in plaintext, including secret values if we manage them here.
# Even with GCS state encryption-at-rest, the operational footgun (state
# file printed to log, `terraform show` output piped to chat) is real.
#
# The pattern is: Terraform owns container + IAM; humans/CI own values
# via `gcloud secrets versions add` (one-off bootstrap) or via a future
# secret-rotation Cloud Function.
#
# When PR 5 wires the deploy workflow, it'll pull these values via
# Secret Manager API at deploy time and write them onto the VM's .env.

resource "google_secret_manager_secret" "secrets" {
  for_each = toset(var.secret_names)

  project   = local.project_id
  secret_id = each.value

  replication {
    # automatic multi-region replication — billed per active version
    # not per replica, so this is effectively free for our tiny
    # secret count and gives us regional outage resilience.
    auto {}
  }

  lifecycle {
    # Removing a name from var.secret_names would otherwise silently
    # destroy the container AND every version inside (live secret
    # values lost). Force operators to explicitly `terraform state rm`
    # the resource address first if they really mean it. Tear-down
    # procedure documented in the runbook.
    prevent_destroy = true
  }

  depends_on = [google_project_service.apis]
}

# Deploy SA reads secrets during CD (PR 5). The for_each binding gives
# per-secret bindings (vs project-level role) so least-privilege scoping
# remains possible if we ever add operator-owned secrets later.
#
# Why `_iam_member` (non-authoritative): each secret may have multiple
# legitimate readers (deploy SA + app runtime SA, both granted here;
# operator-added grants in the future). `_member` adds without wiping
# other members; `_binding` would be authoritative-for-role and wipe
# any out-of-band grants. Verified via google_service_account_iam
# provider docs (MCP query, doc 12373874).
resource "google_secret_manager_secret_iam_member" "ci_deploy_accessor" {
  for_each = google_secret_manager_secret.secrets

  project   = local.project_id
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.ci_deploy.email}"

  # API dependency made explicit — for_each iam_member resources can
  # otherwise race the API enablement on first apply since the resource
  # only depends transitively via google_secret_manager_secret.
  depends_on = [google_project_service.apis]
}

# App runtime SA also reads secrets (the app itself fetches at startup
# in production — the .env file written by CD is a dev/staging
# convenience, not the prod read path). Future-proofs the migration
# to per-pod secret fetch when we move to k8s.
resource "google_secret_manager_secret_iam_member" "app_runtime_accessor" {
  for_each = google_secret_manager_secret.secrets

  project   = local.project_id
  secret_id = each.value.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.app_runtime.email}"

  depends_on = [google_project_service.apis]
}
