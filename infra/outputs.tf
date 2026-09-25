output "workload_identity_provider" {
  value = google_iam_workload_identity_pool_provider.github.name
}

output "runner_service_account" {
  value = google_service_account.runner.email
}

output "model_service_account" {
  value = google_service_account.model.email
}

output "completions_bucket" {
  value = google_storage_bucket.completions.name
}

output "projections_bucket" {
  value = google_storage_bucket.projections.name
}
