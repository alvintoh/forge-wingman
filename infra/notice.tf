# The dispatcher posts the systemic-failure notice, so it alone reads where to.
resource "google_secret_manager_secret_iam_member" "dispatcher_notice" {
  secret_id = google_secret_manager_secret.app["notice-webhook-url"].id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.dispatcher.member
}
