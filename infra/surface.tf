# The identity the surface runs under: the Cloud Run service serving the SPA and
# the JSON views. It reads the store for those views and writes only the settings
# its pages switch — `dispatch/auto-merge`, `dispatch/peak-mode` beside it, and a
# project's start request in the owner database.
resource "google_service_account" "surface" {
  account_id   = "surface"
  display_name = "Forge Wingman surface"

  depends_on = [google_project_service.this]
}

# A Firestore grant is made at the project, and no role narrows one to a document,
# so the surface holds the single role that admits a write: the documents it may
# write are held by `cmd/surface`'s own code, which writes no others.
resource "google_project_iam_member" "surface_datastore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = google_service_account.surface.member

  depends_on = [google_project_service.this]
}

resource "google_cloud_run_v2_service" "surface" {
  name     = "forge-wingman-surface"
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  # IAP signs the caller in before a request reaches the container, so the
  # service holds no credential of its own.
  iap_enabled = true

  # A service holds no data of its own, so a destroy must not need an override.
  deletion_protection = false

  template {
    service_account = google_service_account.surface.email

    scaling {
      min_instance_count = 0
      max_instance_count = 2
    }

    containers {
      image = var.surface_image

      resources {
        limits = {
          cpu    = "1"
          memory = "256Mi"
        }
        cpu_idle = true
      }

      env {
        name  = "GOOGLE_CLOUD_PROJECT"
        value = var.project_id
      }
    }
  }

  depends_on = [
    google_project_iam_member.surface_datastore,
    google_project_service.this,
  ]
}

# IAP calls the service as its own service agent, so that agent needs the
# invoker role; the caller's access is the grant below it.
resource "google_cloud_run_v2_service_iam_member" "surface_iap_invoker" {
  project  = var.project_id
  location = google_cloud_run_v2_service.surface.location
  name     = google_cloud_run_v2_service.surface.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:service-${data.google_project.this.number}@gcp-sa-iap.iam.gserviceaccount.com"
}

resource "google_iap_web_cloud_run_service_iam_member" "surface_owner" {
  project                = var.project_id
  location               = google_cloud_run_v2_service.surface.location
  cloud_run_service_name = google_cloud_run_v2_service.surface.name
  role                   = "roles/iap.httpsResourceAccessor"
  member                 = "user:${var.surface_owner_email}"
}
