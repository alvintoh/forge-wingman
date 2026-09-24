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
