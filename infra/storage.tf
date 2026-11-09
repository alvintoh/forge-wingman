resource "google_storage_bucket" "completions" {
  name                        = "${var.project_id}-completions"
  location                    = var.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  depends_on = [google_project_service.this]

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

  depends_on = [google_project_service.this]
}

# The second source for the build tools the workflows download from GitHub releases, which
# can answer 504 for many minutes. Public-read so a job with no credentials can fetch it,
# writable only by the project's own identities; each download is checked against its pinned
# checksum before extraction, so an object here never needs trusting on its own.
resource "google_storage_bucket" "tools" {
  name                        = "${var.project_id}-tools"
  location                    = var.region
  uniform_bucket_level_access = true

  depends_on = [google_project_service.this]
}

# Read one object by name; unlike objectViewer it cannot list the bucket.
resource "google_storage_bucket_iam_member" "tools_public_read" {
  bucket = google_storage_bucket.tools.name
  role   = "roles/storage.legacyObjectReader"
  member = "allUsers"
}
