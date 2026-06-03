variable "project_id" {
  description = "GCP project ID. Must match PR 1's deploy/terraform/ project (we share the same Secret Manager + Artifact Registry)."
  type        = string
}

variable "region" {
  description = "GCP region for GKE Autopilot + Cloud SQL. us-central1 chosen for Always-Free e2-micro overlap (so cost-conscious users can run both)."
  type        = string
  default     = "us-central1"
}

variable "cluster_name" {
  description = "GKE Autopilot cluster name. Convention: booking-monitor-prod for the production cluster."
  type        = string
  default     = "booking-monitor-prod"
}

variable "release_channel" {
  description = "GKE release channel. RAPID for early access, REGULAR for balance, STABLE for slowest cadence. STABLE is portfolio-default — minimum surprise."
  type        = string
  default     = "STABLE"

  validation {
    condition     = contains(["RAPID", "REGULAR", "STABLE"], var.release_channel)
    error_message = "release_channel must be RAPID, REGULAR, or STABLE."
  }
}

variable "cloudsql_tier" {
  description = "Cloud SQL machine tier. db-f1-micro is the cheapest shared-core tier (~$7-10/month us-central1). Use db-g1-small or db-n1-standard-1 for any production workload — shared-core has no SLA."
  type        = string
  default     = "db-f1-micro"
}

variable "cloudsql_disk_size_gb" {
  description = "Cloud SQL disk size. 10GB is the minimum + sufficient for booking_monitor's small dataset. Storage: ~$0.22/GB-month SSD."
  type        = number
  default     = 10
}

variable "deletion_protection" {
  description = "Cloud SQL deletion protection. Set true for production. Defaults FALSE for portfolio so we can `terraform destroy` cleanly when not actively demoing."
  type        = bool
  default     = false
}

variable "vpc_network_self_link" {
  description = "(Optional) Private VPC network for Cloud SQL private IP. Leave null to use the default network with public IP + authorized networks. Default to null for portfolio simplicity; security-strict deployments should set this."
  type        = string
  default     = null
}
