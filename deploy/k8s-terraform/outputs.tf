output "cluster_name" {
  description = "GKE Autopilot cluster name. Use with `gcloud container clusters get-credentials` to kubeconfig."
  value       = google_container_cluster.booking_monitor.name
}

output "cluster_endpoint" {
  description = "GKE API server endpoint."
  value       = google_container_cluster.booking_monitor.endpoint
  sensitive   = true
}

output "cluster_location" {
  description = "GKE Autopilot location (always regional)."
  value       = google_container_cluster.booking_monitor.location
}

output "cloudsql_connection_name" {
  description = "Cloud SQL connection name in <project>:<region>:<instance> form. Use with Cloud SQL Auth Proxy sidecar."
  value       = google_sql_database_instance.booking_monitor.connection_name
}

output "cloudsql_public_ip" {
  description = "Cloud SQL public IPv4. Pods connect via Cloud SQL Auth Proxy (IAM auth), not via direct IP."
  value       = google_sql_database_instance.booking_monitor.public_ip_address
  sensitive   = true
}

output "next_steps" {
  description = "Operator next-steps after `terraform apply` finishes."
  value       = <<-EOT
    1. Set the booking-app DB password (no plaintext via tf):
       gcloud sql users set-password booking-app \
         --instance=${google_sql_database_instance.booking_monitor.name} \
         --password="$(openssl rand -hex 32)"

    2. Construct DATABASE_URL + add to Secret Manager:
       PWD=$(...above password...)
       URL="postgres://booking-app:$${PWD}@127.0.0.1:5432/booking?sslmode=disable"
       printf "%s" "$URL" | gcloud secrets versions add database-url --data-file=-

    3. kubectl credentials:
       gcloud container clusters get-credentials ${google_container_cluster.booking_monitor.name} \
         --region=${google_container_cluster.booking_monitor.location}

    4. Apply k8s manifests in order:
       kubectl apply -f deploy/k8s/manifests/namespace.yaml
       # ... replace <PROJECT_ID> / <REGION> / etc. via sed or kustomize first
       kubectl apply -f deploy/k8s/manifests/

    5. See docs/runbooks/k8s_migration.md for the full operator runbook.
  EOT
}
