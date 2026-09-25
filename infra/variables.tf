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
