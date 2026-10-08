locals {
  dispatcher = "forge-wingman-dispatcher"

  # The v2 run endpoint for a regional job; the job resource does not export it.
  dispatcher_run_uri = "https://${var.region}-run.googleapis.com/v2/projects/${var.project_id}/locations/${var.region}/jobs/${local.dispatcher}:run"

  # The tokens the job polls with. The values are added by hand, so an apply
  # never carries a credential and a rotation is not a redeploy.
  dispatcher_secrets = toset(["linear-token", "github-token"])
}

# The identity one poll runs under: it reads the two tokens and the queue, and
# holds nothing on a repository.
resource "google_service_account" "dispatcher" {
  account_id   = "dispatcher"
  display_name = "Forge Wingman dispatcher"

  depends_on = [google_project_service.this]
}

# The identity the schedule calls as. It can start a poll and nothing else: the
# runtime identity above is what the job reads the store with.
resource "google_service_account" "dispatcher_schedule" {
  account_id   = "dispatcher-schedule"
  display_name = "Forge Wingman dispatcher schedule"

  depends_on = [google_project_service.this]
}

resource "google_project_iam_member" "dispatcher_datastore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = google_service_account.dispatcher.member

  depends_on = [google_project_service.this]
}

resource "google_secret_manager_secret" "dispatcher" {
  for_each = local.dispatcher_secrets

  secret_id = each.value

  replication {
    auto {}
  }

  depends_on = [google_project_service.this]
}

resource "google_secret_manager_secret_iam_member" "dispatcher" {
  for_each = local.dispatcher_secrets

  secret_id = google_secret_manager_secret.dispatcher[each.value].id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.dispatcher.member
}

resource "google_cloud_run_v2_job" "dispatcher" {
  name     = local.dispatcher
  location = var.region

  # A job holds no data of its own, so a destroy must not need an override.
  deletion_protection = false

  template {
    task_count = 1

    template {
      # A retried poll would find the work the first one did, and the next
      # schedule comes in fifteen minutes: one execution is one poll.
      max_retries     = 0
      timeout         = "600s"
      service_account = google_service_account.dispatcher.email

      containers {
        image = var.dispatcher_image

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
        }

        env {
          name  = "GOOGLE_CLOUD_PROJECT"
          value = var.project_id
        }

        env {
          name  = "LINEAR_DELEGATE"
          value = var.linear_delegate
        }

        env {
          name  = "WINGMAN_REPOS"
          value = join(",", var.linear_repositories)
        }

        dynamic "env" {
          for_each = var.dispatcher_settings
          content {
            name  = env.key
            value = env.value
          }
        }
      }
    }
  }

  depends_on = [google_project_service.this]
}

resource "google_cloud_run_v2_job_iam_member" "dispatcher_invoker" {
  project  = var.project_id
  name     = google_cloud_run_v2_job.dispatcher.name
  location = google_cloud_run_v2_job.dispatcher.location
  role     = "roles/run.invoker"
  member   = google_service_account.dispatcher_schedule.member
}

# The Scheduler service agent mints the access token the call carries, so it
# must be allowed to act as the schedule's own account.
resource "google_service_account_iam_member" "dispatcher_schedule_act_as" {
  service_account_id = google_service_account.dispatcher_schedule.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:service-${data.google_project.this.number}@gcp-sa-cloudscheduler.iam.gserviceaccount.com"
}

data "google_project" "this" {
  project_id = var.project_id
}

# pollInterval and noticeLease in internal/dispatcher/notice.go assume this
# schedule and the job's timeout.
resource "google_cloud_scheduler_job" "dispatcher" {
  name      = local.dispatcher
  region    = var.region
  schedule  = "*/15 * * * *"
  time_zone = "Etc/UTC"

  description      = "Wakes the dispatcher to admit delegated tickets and start one run."
  attempt_deadline = "300s"

  retry_config {
    retry_count = 0
  }

  http_target {
    uri         = local.dispatcher_run_uri
    http_method = "POST"

    # An access token, not an OIDC token: Cloud Run checks the identity the
    # token carries against run.invoker, which needs no audience to match the
    # run endpoint.
    oauth_token {
      service_account_email = google_service_account.dispatcher_schedule.email
    }
  }

  depends_on = [
    google_cloud_run_v2_job_iam_member.dispatcher_invoker,
    google_service_account_iam_member.dispatcher_schedule_act_as,
    google_project_service.this,
  ]
}
