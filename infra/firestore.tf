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
