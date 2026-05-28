# e2-micro Always-Free VM hosting the booking app.
#
# Tier eligibility:
#   - Machine type:  e2-micro (2 vCPU shared, 1 GB RAM)
#   - Region:        us-central1 / us-east1 / us-west1 only
#   - Disk:          30 GB pd-standard
#   - Egress:        1 GB/month to most destinations
#   - Instances:     1 per project
#
# Going over any one of these drops the instance into normal billing
# (~$7/mo). Stay inside; we don't need more for an interview-grade
# flash-sale simulator.

resource "google_service_account" "app_runtime" {
  project      = local.project_id
  account_id   = "sa-app-runtime"
  display_name = "Booking app runtime"
  description  = "Used by the VM to fetch Secret Manager values + write Cloud Logging. Has NO ability to push container images or modify other resources."
}

resource "google_compute_instance" "booking_app" {
  project      = local.project_id
  name         = "booking-app"
  machine_type = "e2-micro"
  zone         = "${var.region}-${var.vm_zone_suffix}"

  tags = ["booking-app", "ssh"]

  boot_disk {
    initialize_params {
      # Pin to a major + minor (debian-12). Don't use :latest — that's
      # an implicit "redeploy when Debian moves the tag" landmine.
      image = "debian-cloud/debian-12"
      size  = 30
      type  = "pd-standard"
    }
  }

  network_interface {
    network = "default"
    access_config {
      # Ephemeral public IP. Cloudflare Tunnel (PR 4) sits in front,
      # so the IP isn't actually exposed for HTTP traffic — but we
      # need it for `gcloud compute ssh --tunnel-through-iap` health-
      # check, and for outbound egress.
    }
  }

  metadata = {
    # OS Login replaces local /etc/passwd SSH keys with IAM-managed
    # ones. Means revoking access = removing IAM role, not editing
    # authorized_keys on every host.
    enable-oslogin = "TRUE"

    # Block project-wide SSH keys; force per-instance via OS Login.
    # Without this, anyone with project metadata write can add an SSH
    # key and bypass IAM.
    block-project-ssh-keys = "TRUE"
  }

  # cloud-init equivalent on Debian/GCP: metadata_startup_script runs
  # on first boot AND on every restart. Idempotent by design.
  metadata_startup_script = file("${path.module}/cloud-init/bootstrap.sh")

  service_account {
    email = google_service_account.app_runtime.email
    # cloud-platform is over-broad in principle, but actual access is
    # gated by IAM bindings (Secret Manager + Logging only). Without
    # cloud-platform scope, even an IAM-allowed call from the SA is
    # rejected by the scope filter — confusing for operators.
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  scheduling {
    automatic_restart   = true
    on_host_maintenance = "MIGRATE"
    preemptible         = false # preemptible loses Always-Free status
  }

  # Always-Free tier requires `automatic_restart=true` + non-preemptible.

  depends_on = [
    google_project_service.apis,
    google_service_account.app_runtime,
  ]
}

# SSH ingress — IAP only.
#
# IAP (Identity-Aware Proxy) tunnels SSH through Google's edge,
# authenticated by IAM. The 35.235.240.0/20 range is GCP-published as
# IAP's source IPs. With this firewall + no other SSH rule, port 22 is
# unreachable from the public internet — you must SSH via
# `gcloud compute ssh --tunnel-through-iap`.
resource "google_compute_firewall" "iap_ssh" {
  project = local.project_id
  name    = "allow-iap-ssh"
  network = "default"

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["ssh"]
}

# Explicitly DENY all other inbound (defense-in-depth; default VPC
# already has implicit deny but stating it makes intent reviewable).
resource "google_compute_firewall" "deny_all_inbound" {
  project = local.project_id
  name    = "deny-all-inbound"
  network = "default"
  priority = 65534 # just above the lowest allow

  deny {
    protocol = "all"
  }

  direction     = "INGRESS"
  source_ranges = ["0.0.0.0/0"]
}
