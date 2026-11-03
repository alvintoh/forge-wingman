# The repository was made by hand before this configuration held it; every
# deployable's image lives here, the surface's, the dispatcher's and the webhook's.
import {
  to = google_artifact_registry_repository.images
  id = "projects/${var.project_id}/locations/${var.region}/repositories/forge-wingman"
}

resource "google_artifact_registry_repository" "images" {
  repository_id = "forge-wingman"
  location      = var.region
  format        = "DOCKER"
  description   = "Forge Wingman dispatcher and surface images"

  # Every package keeps its newest 10 versions and anything younger than 7 days;
  # only a version that is both older and beyond the newest 10 is deleted, since a
  # keep rule wins where both match. A count alone gave the surface about 4 days of
  # rollback at ~7.6 merges a day; the time floor holds 1 week per deployable at
  # any merge rate, for near-free storage. Versions count per package and include
  # the untagged platform and attestation manifests a multi-platform push adds; a
  # manifest an index still references is never deleted.
  cleanup_policy_dry_run = true

  cleanup_policies {
    id     = "keep-newest-10"
    action = "KEEP"
    most_recent_versions {
      package_name_prefixes = ["dispatcher", "surface", "webhook"]
      keep_count            = 10
    }
  }

  cleanup_policies {
    id     = "delete-older-than-7d"
    action = "DELETE"
    condition {
      tag_state             = "ANY"
      package_name_prefixes = ["dispatcher", "surface", "webhook"]
      older_than            = "604800s"
    }
  }
}

# A cleanup dry run reports what it would delete only as a Data Access audit
# entry (a BatchDeleteVersions call with validateOnly true), and Data Access logs
# are off until a service's DATA_WRITE type is enabled. This config owns that
# service's audit setting alone, and nothing else sets it.
resource "google_project_iam_audit_config" "registry" {
  project = var.project_id
  service = "artifactregistry.googleapis.com"

  audit_log_config {
    log_type = "DATA_WRITE"
  }
}
