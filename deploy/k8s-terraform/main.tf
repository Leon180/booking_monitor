provider "google" {
  project = var.project_id
  region  = var.region
}

# Local convenience refs. Most resources read from variables directly,
# but `locals` makes the cluster's workload identity pool ID
# (`<project>.svc.id.goog`) easy to reference across files.
locals {
  workload_identity_pool = "${var.project_id}.svc.id.goog"
}

# Enable required APIs for this stack. (PR 1's terraform already enables
# Compute / Artifact Registry / Secret Manager / IAM Credentials; we
# add Container Engine + Cloud SQL here.)
resource "google_project_service" "container" {
  project            = var.project_id
  service            = "container.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "sqladmin" {
  project            = var.project_id
  service            = "sqladmin.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "secretmanager_addon" {
  # Required by the GKE Secrets Store CSI add-on (Google managed).
  project            = var.project_id
  service            = "secretmanager.googleapis.com"
  disable_on_destroy = false
}
