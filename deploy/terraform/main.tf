provider "google" {
  project = local.project_id
  region  = var.region
}

provider "google-beta" {
  project = local.project_id
  region  = var.region
}

# Conditional project creation. Two paths supported:
#
#   create_project=false → reference an existing project by ID.
#                          Auth via `gcloud auth application-default login`
#                          must already be valid for that project.
#
#   create_project=true  → Terraform creates the project, links billing,
#                          and owns its lifecycle. Useful for fully
#                          reproducible bootstrap from a fresh GCP org.
#
# We branch via count rather than a separate module to keep the
# blast radius of "wrong setting" small — count=0 simply skips the
# resource, no side effects.

resource "google_project" "main" {
  count = var.create_project ? 1 : 0

  project_id      = var.project_id
  name            = var.project_id
  billing_account = var.billing_account_id

  # Allow `terraform destroy` to clean up. Default is PREVENT, which
  # is safer for prod but mismatched for a bootstrap module that's
  # meant to be tearable-downable in dev. Caller takes responsibility.
  deletion_policy = "DELETE"
}

data "google_project" "existing" {
  count      = var.create_project ? 0 : 1
  project_id = var.project_id
}

locals {
  project_id     = var.create_project ? google_project.main[0].project_id : data.google_project.existing[0].project_id
  project_number = var.create_project ? google_project.main[0].number : data.google_project.existing[0].number
}
