output "workload_identity_provider" {
  value = google_iam_workload_identity_pool_provider.github.name
}

output "rule_stack_workload_identity_provider" {
  value = google_iam_workload_identity_pool_provider.rule_stack.name
}

output "runner_service_account" {
  value = google_service_account.runner.email
}

output "model_service_account" {
  value = google_service_account.model.email
}

output "publisher_service_account" {
  value = google_service_account.publisher.email
}

output "completions_bucket" {
  value = google_storage_bucket.completions.name
}

output "projections_bucket" {
  value = google_storage_bucket.projections.name
}

output "dispatcher_service_account" {
  value = google_service_account.dispatcher.email
}

output "dispatcher_job" {
  value = google_cloud_run_v2_job.dispatcher.name
}

output "dispatcher_schedule" {
  value = google_cloud_scheduler_job.dispatcher.name
}

# The rollout step adds a value to each of these by hand, so no apply ever
# carries a token and the names stay the one contract between the two.
output "dispatcher_secrets" {
  value = sort(tolist(local.dispatcher_secrets))
}

output "webhook_service_account" {
  value = google_service_account.webhook.email
}

# The address to give the Linear app as its webhook URL.
output "webhook_url" {
  value = "${google_cloud_run_v2_service.webhook.uri}/linear"
}
