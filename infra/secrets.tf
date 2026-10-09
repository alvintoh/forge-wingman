locals {
  # The two Linear apps' credentials and the notice destination. Values are added
  # by hand, and each consumer grants its own identity access when it lands.
  app_secrets = toset([
    "linear-client-id",
    "linear-client-secret",
    "linear-review-client-id",
    "linear-review-client-secret",
    "linear-webhook-secret",
    "notice-webhook-url",
  ])
}

resource "google_secret_manager_secret" "app" {
  for_each = local.app_secrets

  secret_id = each.value

  replication {
    auto {}
  }

  depends_on = [google_project_service.this]
}
