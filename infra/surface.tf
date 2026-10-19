# The identity the surface runs under: the Cloud Run service serving the SPA and
# the JSON views. It reads the store for those views and writes only the settings
# its pages switch — `dispatch/auto-merge`, and `dispatch/peak-mode` beside it.
resource "google_service_account" "surface" {
  account_id   = "surface"
  display_name = "Forge Wingman surface"

  depends_on = [google_project_service.this]
}

# A Firestore grant is made at the project, and no role narrows one to a document,
# so the surface holds the single role that admits a write: the documents it may
# write are held by `cmd/surface`'s own code, which writes no others.
resource "google_project_iam_member" "surface_datastore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = google_service_account.surface.member

  depends_on = [google_project_service.this]
}
