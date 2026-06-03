# Cloud SQL for PostgreSQL — replaces compose-PG.
#
# db-f1-micro is the cheapest shared-core tier (~$7-10/month us-central1).
# Shared-core has NO SLA and is dev/test-only per Google. Acceptable
# for portfolio; flip to db-g1-small or db-n1-standard-1 for real prod.
#
# We use POSTGRES_15 to match PR 1's compose-PG version. Migration
# scripts in deploy/postgres/migrations/ work as-is.
resource "google_sql_database_instance" "booking_monitor" {
  name             = "booking-monitor-db"
  database_version = "POSTGRES_15"
  region           = var.region

  deletion_protection = var.deletion_protection

  settings {
    tier              = var.cloudsql_tier
    availability_type = "ZONAL" # REGIONAL adds HA but doubles cost; not needed for portfolio
    disk_size         = var.cloudsql_disk_size_gb
    disk_type         = "PD_SSD"
    disk_autoresize   = true

    backup_configuration {
      enabled                        = true
      start_time                     = "03:00" # UTC; low-traffic window
      point_in_time_recovery_enabled = false   # Not available on shared-core tiers
      transaction_log_retention_days = 7
      backup_retention_settings {
        retained_backups = 7
        retention_unit   = "COUNT"
      }
    }

    # ip_configuration depends on whether we want private IP + VPC peering
    # (security-best) or public IP + authorized networks (portfolio-cheap).
    # Default: public IP, no authorized networks → unreachable until we
    # add a network later. Pods reach Cloud SQL via the Cloud SQL Auth
    # Proxy sidecar pattern (which uses IAM auth, no IP allowlist).
    ip_configuration {
      ipv4_enabled = true
      # No authorized_networks — pods connect via Cloud SQL Auth Proxy.
    }
  }

  depends_on = [
    google_project_service.sqladmin,
  ]
}

resource "google_sql_database" "booking" {
  name     = "booking"
  instance = google_sql_database_instance.booking_monitor.name
}

# DB user — password set via terraform_data so we can rotate without
# tearing down the database. The password is stored in Secret Manager
# (handled in the runbook's secrets-provisioning step, not here, so
# the password doesn't go through Terraform state.)
resource "google_sql_user" "booking_app" {
  name     = "booking-app"
  instance = google_sql_database_instance.booking_monitor.name
  # No `password` field — `password_wo` (write-only attribute) is the
  # 2026 best practice for not landing secret values in state.
  # Operator sets the password via gcloud after creation:
  #   gcloud sql users set-password booking-app \
  #     --instance=booking-monitor-db --password=$(openssl rand -hex 32)
  # then captures the password into Secret Manager as `database-url`.
  password_wo = "set-via-gcloud-after-create"
}

# Grant the app_runtime SA permission to act as the SQL client.
resource "google_project_iam_member" "app_runtime_sql_client" {
  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:sa-app-runtime@${var.project_id}.iam.gserviceaccount.com"
}
