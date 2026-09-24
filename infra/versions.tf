terraform {
  required_version = ">= 1.12"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.4"
    }
  }

  # The state bucket is created by hand and stays outside this configuration.
  backend "gcs" {
    bucket = "forge-wingman-tfstate"
    prefix = "infra"
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}
