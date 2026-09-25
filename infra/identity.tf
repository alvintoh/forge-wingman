resource "google_service_account" "runner" {
  account_id   = "runner"
  display_name = "Forge Wingman runner"

  depends_on = [google_project_service.this]
}

# The identity model.yml runs under: it reads projections and creates completions,
# and holds nothing on Firestore.
resource "google_service_account" "model" {
  account_id   = "wingman-model"
  display_name = "Forge Wingman model"

  depends_on = [google_project_service.this]
}

locals {
  # A job's token names the workflow file its steps come from; for a job outside a
  # reusable workflow that is checked by the README's rollout step 1, not assumed.
  workflow_principal = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.job_workflow_ref/${var.github_repository}/.github/workflows/%s@refs/heads/main"
  runner_workflows   = toset(["run.yml", "infra-smoke.yml"])
  model_workflows    = toset(["model.yml", "model-smoke.yml"])
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
    "attribute.job_workflow_ref"    = "assertion.job_workflow_ref"
  }

  # Without a condition any repository on GitHub could mint a token against this pool; main-only keeps a
  # branch that rewrites a workflow from getting one.
  attribute_condition = "assertion.repository_id == \"${var.github_repository_id}\" && assertion.repository_owner_id == \"${var.github_owner_id}\" && assertion.ref == \"refs/heads/main\""

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

resource "google_service_account_iam_member" "runner_wif_workflow" {
  for_each = local.runner_workflows

  service_account_id = google_service_account.runner.name
  role               = "roles/iam.workloadIdentityUser"
  member             = format(local.workflow_principal, each.value)
}

resource "google_service_account_iam_member" "model_wif" {
  for_each = local.model_workflows

  service_account_id = google_service_account.model.name
  role               = "roles/iam.workloadIdentityUser"
  member             = format(local.workflow_principal, each.value)
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

resource "google_storage_bucket_iam_member" "model_completions" {
  bucket = google_storage_bucket.completions.name
  role   = "roles/storage.objectCreator"
  member = google_service_account.model.member
}

resource "google_storage_bucket_iam_member" "model_projections" {
  bucket = google_storage_bucket.projections.name
  role   = "roles/storage.objectViewer"
  member = google_service_account.model.member
}
