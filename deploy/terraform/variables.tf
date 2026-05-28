variable "project_id" {
  description = "GCP project ID. If create_project=true, this is the ID Terraform will create. Otherwise the project must already exist."
  type        = string
}

variable "create_project" {
  description = "Whether Terraform should create the GCP project. Set false when using an existing project; set true to bootstrap from scratch (requires billing_account_id)."
  type        = bool
  default     = false
}

variable "billing_account_id" {
  description = "Billing account ID (format: XXXXXX-XXXXXX-XXXXXX). Required only when create_project=true. Look up via `gcloud beta billing accounts list`."
  type        = string
  default     = ""
}

variable "region" {
  description = "Primary region. MUST be us-central1, us-east1, or us-west1 for the e2-micro Always-Free tier to apply. Other regions cost ~$7/mo for the same instance."
  type        = string
  default     = "us-central1"

  validation {
    condition     = contains(["us-central1", "us-east1", "us-west1"], var.region)
    error_message = "Region must be one of us-central1/us-east1/us-west1 to qualify for e2-micro Always-Free."
  }
}

variable "github_owner" {
  description = "GitHub owner (user or org) of the repo. Used in WIF attribute_condition as the first filter — only OIDC tokens issued for repos under this owner will be accepted by the pool."
  type        = string
}

variable "github_repo" {
  description = "GitHub repo name. Used in WIF IAM bindings to scope which repo's workflows can impersonate each SA."
  type        = string
}

variable "secret_names" {
  description = "List of Secret Manager secret IDs to create as empty containers. Values are populated out-of-band (see runbook Step 4) so they never land in Terraform state."
  type        = list(string)
  default = [
    "stripe-api-key",
    "stripe-webhook-secret",
    "payment-webhook-secret",
    "database-url",
    "redis-password",
    "grafana-admin-password",
  ]
}

variable "vm_zone_suffix" {
  description = "Zone suffix within the region (a/b/c). e2-micro Always-Free is per-project, not per-zone, so this only affects which zone hosts the instance."
  type        = string
  default     = "a"
}
