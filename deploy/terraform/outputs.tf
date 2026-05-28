output "project_id" {
  description = "Effective GCP project ID (whether existing or freshly created)."
  value       = local.project_id
}

output "project_number" {
  description = "Numeric project ID used in WIF provider URIs and IAM principal/principalSet paths."
  value       = local.project_number
}

# ----- Set these as GitHub Actions repository variables (NOT secrets) -----

output "workload_identity_provider" {
  description = "Full resource path of the WIF provider. Set as repo variable GCP_WORKLOAD_IDENTITY_PROVIDER and pass to google-github-actions/auth@v2."
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "ci_readonly_sa_email" {
  description = "Service account email impersonated by PR / branch builds. Set as repo variable GCP_CI_READONLY_SA."
  value       = google_service_account.ci_readonly.email
}

output "ci_deploy_sa_email" {
  description = "Service account email impersonated by main + tag builds. Set as repo variable GCP_CI_DEPLOY_SA."
  value       = google_service_account.ci_deploy.email
}

# ----- Used by PR 3 (registry URL for `docker push`) -----

output "artifact_registry_url" {
  description = "Base URL for the Artifact Registry repo. Tag images as ${this}/<image>:<tag>."
  value       = "${google_artifact_registry_repository.booking.location}-docker.pkg.dev/${local.project_id}/${google_artifact_registry_repository.booking.repository_id}"
}

# ----- Used by PR 4/5 (deploy target) -----

output "vm_name" {
  description = "Compute instance name for `gcloud compute ssh`."
  value       = google_compute_instance.booking_app.name
}

output "vm_zone" {
  description = "Zone the instance was created in."
  value       = google_compute_instance.booking_app.zone
}

output "vm_external_ip" {
  description = "Ephemeral public IP. Cloudflare Tunnel terminates traffic for the app; this IP is for diagnostic / SSH-via-IAP only."
  value       = google_compute_instance.booking_app.network_interface[0].access_config[0].nat_ip
}

# ----- Diagnostic -----

output "secret_ids" {
  description = "Secret Manager secret IDs created as empty containers. Populate via `gcloud secrets versions add <id> --data-file=-`."
  value       = [for s in google_secret_manager_secret.secrets : s.secret_id]
}
