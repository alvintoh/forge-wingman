locals {
  webhook = "forge-wingman-webhook"
}

# The identity the webhook runs under: it writes its marker, reads the signing
# secret and the agent's token, and can start a poll.
resource "google_service_account" "webhook" {
  account_id   = "webhook"
  display_name = "Forge Wingman webhook"

  depends_on = [google_project_service.this]
}

resource "google_project_iam_member" "webhook_datastore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = google_service_account.webhook.member

  depends_on = [google_project_service.this]
}

resource "google_secret_manager_secret_iam_member" "webhook_signing" {
  secret_id = google_secret_manager_secret.app["linear-webhook-secret"].id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.webhook.member
}

# The session acknowledgement is the only call the webhook makes with it.
resource "google_secret_manager_secret_iam_member" "webhook_linear_token" {
  secret_id = google_secret_manager_secret.dispatcher["linear-token"].id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.webhook.member
}

resource "google_cloud_run_v2_job_iam_member" "webhook_invoker" {
  project  = var.project_id
  name     = google_cloud_run_v2_job.dispatcher.name
  location = google_cloud_run_v2_job.dispatcher.location
  role     = "roles/run.invoker"
  member   = google_service_account.webhook.member
}

resource "google_cloud_run_v2_service" "webhook" {
  name     = local.webhook
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  # Linear calls without a Google identity, so the service takes every caller
  # and Linear's signature is the gate.
  invoker_iam_disabled = true

  # A service holds no data of its own, so a destroy must not need an override.
  deletion_protection = false

  template {
    service_account = google_service_account.webhook.email
    timeout         = "10s"

    scaling {
      min_instance_count = 0
      max_instance_count = 2
    }

    containers {
      image = var.webhook_image

      resources {
        limits = {
          cpu    = "1"
          memory = "256Mi"
        }
      }

      env {
        name  = "GOOGLE_CLOUD_PROJECT"
        value = var.project_id
      }

      env {
        name  = "DISPATCHER_RUN_URI"
        value = local.dispatcher_run_uri
      }
    }
  }

  # The secrets are read at start-up, so the first revision must already be
  # allowed to read them.
  depends_on = [
    google_project_iam_member.webhook_datastore,
    google_secret_manager_secret_iam_member.webhook_signing,
    google_secret_manager_secret_iam_member.webhook_linear_token,
    google_cloud_run_v2_job_iam_member.webhook_invoker,
    google_project_service.this,
  ]
}
