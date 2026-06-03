# Enable required GCP APIs.
#
# `iamcredentials.googleapis.com` and `sts.googleapis.com` are
# easy to miss — they're what powers Service Account impersonation
# and STS token exchange respectively. Without them, WIF token exchange
# returns a confusing PERMISSION_DENIED with no actionable detail.
#
# `disable_on_destroy = false` because disabling an API is rarely what
# you want during `terraform destroy` — it forces a 30-day cooldown
# before re-enable, which is a footgun for dev iteration.

locals {
  required_apis = [
    "artifactregistry.googleapis.com",
    "secretmanager.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "sts.googleapis.com",
    "compute.googleapis.com",
    "logging.googleapis.com",
    "monitoring.googleapis.com",
    "cloudresourcemanager.googleapis.com",
  ]
}

resource "google_project_service" "apis" {
  for_each = toset(local.required_apis)

  project = local.project_id
  service = each.value

  # `disable_dependent_services = false` is the provider default; omitted
  # to reduce noise. Explicit `disable_on_destroy = false` IS load-bearing
  # — the provider default for that one is true, which forces a 30-day
  # cooldown on re-enable. False keeps `terraform destroy` cycles cheap.
  disable_on_destroy = false
}
