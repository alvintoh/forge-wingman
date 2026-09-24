resource "google_storage_bucket" "completions" {
  name                        = "${var.project_id}-completions"
  location                    = var.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  lifecycle_rule {
    condition {
      age = 90
    }
    action {
      type = "Delete"
    }
  }

  # Soft delete off, so an object is gone at 90 days rather than kept another 7.
  soft_delete_policy {
    retention_duration_seconds = 0
  }
}

# No lifecycle rule: an expiry would delete the current projection.
resource "google_storage_bucket" "projections" {
  name                        = "${var.project_id}-projections"
  location                    = var.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
}
