resource "google_firestore_database" "default" {
  name        = "(default)"
  type        = "FIRESTORE_NATIVE"
  location_id = var.region

  # The database cannot be recreated elsewhere, so a destroy must fail rather than delete it.
  delete_protection_state = "DELETE_PROTECTION_ENABLED"
  deletion_policy         = "PREVENT"

  depends_on = [google_project_service.this]

  lifecycle {
    prevent_destroy = true
  }
}

# The queue's claim read, ordered by the Linear priority the dispatcher wrote and
# then by when the run was queued, which a single-field index cannot serve.
resource "google_firestore_index" "queue_claim_by_queued_at" {
  project    = var.project_id
  database   = google_firestore_database.default.name
  collection = "runs"

  fields {
    field_path = "state"
    order      = "ASCENDING"
  }

  fields {
    field_path = "linear_priority"
    order      = "ASCENDING"
  }

  fields {
    field_path = "queued_at"
    order      = "ASCENDING"
  }
}

# Serves the latest-settled-runs query behind concurrency tuning.
resource "google_firestore_index" "settled_by_size_and_concurrency" {
  project    = var.project_id
  database   = google_firestore_database.default.name
  collection = "runs"

  fields {
    field_path = "size"
    order      = "ASCENDING"
  }

  fields {
    field_path = "outcome"
    order      = "ASCENDING"
  }

  fields {
    field_path = "claim_concurrency"
    order      = "ASCENDING"
  }

  fields {
    field_path = "settled_at"
    order      = "DESCENDING"
  }
}
