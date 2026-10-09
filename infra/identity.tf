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

# The identity the rule stack's publish workflow runs under: it creates projections and
# moves projections/current, and holds nothing else.
resource "google_service_account" "publisher" {
  account_id   = "wingman-publisher"
  display_name = "Forge Wingman projection publisher"

  depends_on = [google_project_service.this]
}

# The identity pr-review.yml's context job runs under: it reads the Linear app's client
# credentials to mint a read token, and holds nothing else.
resource "google_service_account" "pr_review" {
  account_id   = "wingman-pr-review"
  display_name = "Forge Wingman PR review"

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

# The rule stack's own provider, so the one above keeps admitting this repository alone.
resource "google_iam_workload_identity_pool_provider" "rule_stack" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "rule-stack"
  display_name                       = "GitHub OIDC (rule stack)"

  # No job_workflow_ref: bindings are pool-wide, so mapping it would let a rule-stack token match a workflow binding.
  attribute_mapping = {
    "google.subject"                = "assertion.sub"
    "attribute.repository_id"       = "assertion.repository_id"
    "attribute.repository_owner_id" = "assertion.repository_owner_id"
  }

  attribute_condition = "assertion.repository_id == \"${var.rule_stack_repository_id}\" && assertion.repository_owner_id == \"${var.github_owner_id}\" && assertion.ref == \"refs/heads/main\" && (assertion.event_name == \"push\" || assertion.event_name == \"workflow_dispatch\")"

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

resource "google_service_account_iam_member" "pr_review_wif" {
  service_account_id = google_service_account.pr_review.name
  role               = "roles/iam.workloadIdentityUser"
  member             = format(local.workflow_principal, "pr-review.yml")
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

resource "google_service_account_iam_member" "publisher_wif" {
  service_account_id = google_service_account.publisher.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository_id/${var.rule_stack_repository_id}"
}

resource "google_storage_bucket_iam_member" "publisher_projections" {
  bucket = google_storage_bucket.projections.name
  role   = "roles/storage.objectCreator"
  member = google_service_account.publisher.member

  condition {
    title      = "projections-prefix-only"
    expression = "resource.name.startsWith(\"projects/_/buckets/${google_storage_bucket.projections.name}/objects/projections/\")"
  }
}

# Overwriting an object needs delete, so it is granted on the pointer alone: a published
# projections/<sha>/ object stays create-only.
resource "google_storage_bucket_iam_member" "publisher_current" {
  bucket = google_storage_bucket.projections.name
  role   = "roles/storage.objectUser"
  member = google_service_account.publisher.member

  condition {
    title      = "projections-current-only"
    expression = "resource.name == \"projects/_/buckets/${google_storage_bucket.projections.name}/objects/projections/current\""
  }
}
