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

# The queue's claim reads one queued run, ordered by the Linear priority the
# dispatcher wrote, which a single-field index cannot serve.
resource "google_firestore_index" "queue_claim" {
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
