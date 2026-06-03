# Terraform + provider versions for the k8s migration skeleton.
#
# Provider versions match PR 1's deploy/terraform/ to avoid two
# pinning streams in the same repo. Once we APPLY this Terraform
# (PR 9 era), keep them aligned via Renovate / Dependabot.
terraform {
  required_version = ">= 1.11"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }

  # State for this stack lives next to PR 1's state, in the same GCS
  # bucket, different prefix. Operator runs:
  #   make init  (which sets up backend at booking-monitor/k8s-terraform)
  # before first apply.
  backend "gcs" {
    prefix = "booking-monitor/k8s-terraform"
  }
}
