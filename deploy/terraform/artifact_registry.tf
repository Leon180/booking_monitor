resource "google_artifact_registry_repository" "booking" {
  project       = local.project_id
  location      = var.region
  repository_id = "booking"
  description   = "Booking monitor container images. Signed via cosign keyless (PR 3); SBOM attached as in-toto attestation."
  format        = "DOCKER"

  # Cleanup policies — Artifact Registry charges per GB-month. Keep
  # the last 10 versioned tags + auto-delete untagged images older
  # than 7 days. Production rollback typically needs N-1 / N-2; 10 is
  # ample headroom.
  cleanup_policies {
    id     = "keep-recent-versions"
    action = "KEEP"

    most_recent_versions {
      keep_count = 10
    }
  }

  cleanup_policies {
    id     = "delete-stale-untagged"
    action = "DELETE"

    condition {
      tag_state  = "UNTAGGED"
      older_than = "604800s" # 7 days
    }
  }

  depends_on = [google_project_service.apis]
}

# Readonly SA: pull only.
resource "google_artifact_registry_repository_iam_member" "ci_readonly_reader" {
  project    = local.project_id
  location   = google_artifact_registry_repository.booking.location
  repository = google_artifact_registry_repository.booking.name
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:${google_service_account.ci_readonly.email}"
}

# Deploy SA: push (which also implies read).
resource "google_artifact_registry_repository_iam_member" "ci_deploy_writer" {
  project    = local.project_id
  location   = google_artifact_registry_repository.booking.location
  repository = google_artifact_registry_repository.booking.name
  role       = "roles/artifactregistry.writer"
  member     = "serviceAccount:${google_service_account.ci_deploy.email}"
}

# App runtime SA on the VM: pull only (it just runs the images).
resource "google_artifact_registry_repository_iam_member" "app_runtime_reader" {
  project    = local.project_id
  location   = google_artifact_registry_repository.booking.location
  repository = google_artifact_registry_repository.booking.name
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:${google_service_account.app_runtime.email}"
}
