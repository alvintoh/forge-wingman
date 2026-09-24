resource "google_service_account" "runner" {
  account_id   = "runner"
  display_name = "Forge Wingman runner"

  depends_on = [google_project_service.this]
}

resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "github"
  display_name              = "GitHub Actions"

  depends_on = [google_project_service.this]
}

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github"
  display_name                       = "GitHub OIDC"

  attribute_mapping = {
    "google.subject"                = "assertion.sub"
    "attribute.repository_id"       = "assertion.repository_id"
    "attribute.repository_owner_id" = "assertion.repository_owner_id"
  }

  # Without a condition any repository on GitHub could mint a token against this pool; main-only keeps a
  # branch that rewrites a workflow from getting one.
  attribute_condition = "assertion.repository_id == \"${var.github_repository_id}\" && assertion.repository_owner_id == \"${var.github_owner_id}\" && assertion.ref == \"refs/heads/main\""

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

resource "google_service_account_iam_member" "runner_wif" {
  service_account_id = google_service_account.runner.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository_id/${var.github_repository_id}"
}

resource "google_project_iam_member" "runner_datastore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = google_service_account.runner.member

  depends_on = [google_project_service.this]
}

resource "google_storage_bucket_iam_member" "runner_completions" {
  bucket = google_storage_bucket.completions.name
  role   = "roles/storage.objectCreator"
  member = google_service_account.runner.member
}

resource "google_storage_bucket_iam_member" "runner_projections" {
  bucket = google_storage_bucket.projections.name
  role   = "roles/storage.objectViewer"
  member = google_service_account.runner.member
}
