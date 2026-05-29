terraform {
  # Floor pinned to 1.11.0 — required for `write-only attributes`
  # (added in 1.11) which let secret values pass through Terraform
  # without entering state. Earlier floors force the workaround pattern
  # of "Terraform owns containers, humans own values" (see secret_manager.tf);
  # staying current gives PR 5 (deploy workflow) the cleaner option.
  #
  # Ceiling intentionally not set — HashiCorp supports each GA release
  # for 2 years; pinning a ceiling just makes dev/CI version skew worse.
  # The `.terraform.lock.hcl` (committed) records the actual resolved
  # version at apply time for reproducibility.
  required_version = ">= 1.11.0"

  required_providers {
    # google ~> 7.0 — major bump from the initial 6.x draft. 7.0 added
    # ephemeral resources (Terraform 1.10+) AND write-only attributes
    # for secret-shaped inputs. Both directly address the "secrets in
    # state" footgun documented in secret_manager.tf. 6.x worked but
    # silently denied us the cleaner pattern.
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
    # google-beta is needed for a handful of resources that haven't
    # graduated to GA yet. Keep it on the same major as google to avoid
    # cross-provider type mismatches.
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 7.0"
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
