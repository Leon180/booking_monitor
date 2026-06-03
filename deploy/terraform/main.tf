provider "google" {
  # NOTE: use `var.project_id` here, not `local.project_id`.
  # `local.project_id` resolves through either `google_project.main` or
  # `data.google_project.existing`, both of which run THROUGH this same
  # provider — that creates a graph cycle (provider → local → resource →
  # provider). `terraform validate` rejects with `Error: Cycle: ...`.
  #
  # `project` here is only the provider's DEFAULT project. Every resource
  # in this module ALSO sets `project = local.project_id` explicitly, which
  # overrides the default and creates the right "wait for project to exist"
  # ordering. Per provider docs: "If another project is specified on a
  # resource, it will take precedence."
  project = var.project_id
  region  = var.region
}

provider "google-beta" {
  # Same rationale as `google` provider above — break the cycle.
  project = var.project_id
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
