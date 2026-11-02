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

  # Each merge that changes the surface pushes an image, so its package is pruned
  # to the newest 20. Both rules name the surface package alone: the dispatcher's
  # and the webhook's images are never matched. A keep rule deletes nothing by
  # itself, and where both rules match a version the keep rule wins.
  cleanup_policy_dry_run = true

  cleanup_policies {
    id     = "keep-newest-surface"
    action = "KEEP"
    most_recent_versions {
      package_name_prefixes = ["surface"]
      keep_count            = 20
    }
  }

  cleanup_policies {
    id     = "delete-older-surface"
    action = "DELETE"
    condition {
      tag_state             = "ANY"
      package_name_prefixes = ["surface"]
    }
  }
}
