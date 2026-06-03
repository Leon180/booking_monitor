# GKE Autopilot cluster for booking_monitor.
#
# Key choices:
#   * enable_autopilot = true → Google manages nodes; you only manage
#     workloads. ~50% smaller "surface area to learn" vs Standard mode.
#   * release_channel = STABLE → portfolio default; minimum surprise
#     from k8s minor upgrades.
#   * workload_identity_config wired → required for the
#     iam.gke.io/gcp-service-account annotation in
#     deploy/k8s/manifests/serviceaccount.yaml.
#   * deletion_protection = false → portfolio sandbox; we want to be
#     able to `terraform destroy` cleanly between active job-hunt
#     periods. Flip to true for any real production workload.
#
# Cost: $73/month cluster management fee — OFFSET by Google's
# $74.40/month free credit for the FIRST cluster in the billing
# account. Effective fee: $0/month for one cluster. Per-pod cost is
# separate; see docs/k8s-migration-plan.md cost table.
resource "google_container_cluster" "booking_monitor" {
  name     = var.cluster_name
  location = var.region

  enable_autopilot = true

  release_channel {
    channel = var.release_channel
  }

  # Workload Identity Pool — this is what makes the k8s ServiceAccount
  # → GCP Service Account mapping work via the
  # iam.gke.io/gcp-service-account annotation. Without this enabled,
  # the booking-monitor SA in deploy/k8s/manifests/serviceaccount.yaml
  # is just a normal k8s SA with no GCP identity.
  workload_identity_config {
    workload_pool = local.workload_identity_pool
  }

  # We don't define node_pool / node_config — Autopilot manages those.
  # See https://cloud.google.com/kubernetes-engine/docs/concepts/autopilot-overview.

  deletion_protection = false # portfolio sandbox; flip for prod

  depends_on = [
    google_project_service.container,
  ]
}

# Bind the GCP `sa-app-runtime` (created in PR 1) to the k8s SA
# `booking-monitor` in namespace `booking-monitor`. After this,
# the k8s SA's pods inherit `sa-app-runtime`'s IAM (including Secret
# Manager + Cloud SQL Client) automatically.
#
# CRITICAL: this resource REFERENCES the sa-app-runtime SA from PR 1's
# state. For terraform to see it, either:
#   (a) Both stacks share the same state backend (current default), OR
#   (b) PR 1 exports `app_runtime_sa_email` and this stack reads it
#       via `data.terraform_remote_state`.
# We use approach (b) — explicit cross-stack remote state read.
data "terraform_remote_state" "pr1" {
  backend = "gcs"
  config = {
    bucket = "${var.project_id}-tfstate"
    prefix = "booking-monitor/terraform"
  }
}

resource "google_service_account_iam_member" "k8s_sa_to_gcp_sa" {
  service_account_id = "projects/${var.project_id}/serviceAccounts/sa-app-runtime@${var.project_id}.iam.gserviceaccount.com"
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${local.workload_identity_pool}[booking-monitor/booking-monitor]"

  depends_on = [
    google_container_cluster.booking_monitor,
  ]
}
