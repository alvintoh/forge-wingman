locals {
  linear_app_credentials = toset(["linear-client-id", "linear-client-secret"])
  # The PR review's own app, read scope only.
  linear_review_app_credentials = toset(["linear-review-client-id", "linear-review-client-secret"])
}

# The dispatcher and the webhook each mint their own Linear token from the app's
# client credentials.
resource "google_secret_manager_secret_iam_member" "dispatcher_linear_app" {
  for_each = local.linear_app_credentials

  secret_id = google_secret_manager_secret.app[each.value].id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.dispatcher.member
}

resource "google_secret_manager_secret_iam_member" "webhook_linear_app" {
  for_each = local.linear_app_credentials

  secret_id = google_secret_manager_secret.app[each.value].id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.webhook.member
}

resource "google_secret_manager_secret_iam_member" "pr_review_linear_app" {
  for_each = local.linear_review_app_credentials

  secret_id = google_secret_manager_secret.app[each.value].id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.pr_review.member
}
