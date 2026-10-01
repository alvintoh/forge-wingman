variable "project_id" {
  type    = string
  default = "forge-wingman"
}

variable "region" {
  type    = string
  default = "us-central1"
}

# Numeric ids, not names: a deleted-and-recreated name cannot inherit them.
variable "github_repository_id" {
  type    = string
  default = "1382834276"
}

# The rule stack the projections are generated from.
variable "rule_stack_repository_id" {
  type    = string
  default = "1282955517"
}

variable "github_owner_id" {
  type    = string
  default = "24959836"
}

# Workload Identity bindings match on job_workflow_ref, which names the repository;
# the provider's condition still pins the numeric ids above.
variable "github_repository" {
  type    = string
  default = "alvintoh/forge-wingman"
}

# Nothing here builds or pushes the dispatcher's image: the Dockerfile builds it
# with CMD=dispatcher, and the push stays with whoever holds the registry.
variable "dispatcher_image" {
  type = string
}

# The Linear agent the job acts for, as Linear's own id for it. A poll refuses
# to admit anything if the token it reads is a different agent's.
variable "linear_delegate" {
  type = string
}

# The repositories a ticket's repo: label may name, in the spelling the
# dispatch sends back to GitHub. A ticket naming any other repository is refused,
# so leaving this empty admits nothing rather than anything.
variable "linear_repositories" {
  type    = list(string)
  default = []
}

# Optional concurrency limits and tuning the dispatcher reads; a key left out
# keeps its default. The allowed keys are the variables concurrencySettings
# reads in cmd/dispatcher/main.go, and change with it.
variable "dispatcher_settings" {
  type    = map(string)
  default = {}

  validation {
    condition = alltrue([for k in keys(var.dispatcher_settings) : contains([
      "WINGMAN_PLATFORM_CAP", "WINGMAN_LARGE_CAP", "WINGMAN_REVIEW_WIP",
      "WINGMAN_STABLE_RUNS", "WINGMAN_RISE_WITHIN", "WINGMAN_HALVE_BEYOND",
    ], k)])
    error_message = "dispatcher_settings may only set the WINGMAN_ concurrency limits and tuning."
  }
}
