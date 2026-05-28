terraform {
  # Pin Terraform itself. New minor releases occasionally change resource
  # schemas (e.g. 1.6 deprecated some defaults); locking the floor keeps
  # CI + developer machines on a known-good codepath. Bump deliberately.
  required_version = ">= 1.7.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
    # google-beta is required for a handful of resources that haven't
    # graduated to GA yet (e.g. some Cloud Run + WIF combinations).
    # Keeping the alias available avoids a disruptive add later.
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 6.0"
    }
  }

  # Remote state in GCS. The bucket must exist before `terraform init`;
  # the chicken-and-egg is unavoidable for the very first apply, so the
  # bucket is created out-of-band via the runbook (docs/runbooks/gcp_bootstrap.md
  # Step 2).
  #
  # `bucket` is intentionally NOT hardcoded here so this file is project-
  # agnostic and reusable. Pass it via:
  #   terraform init -backend-config="bucket=${PROJECT_ID}-tfstate"
  backend "gcs" {
    prefix = "booking-monitor/terraform"
  }
}
