package store

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	runsPerRepoDocID = "runs-per-repo"
	runsPerRepoField = "runs_per_repo"
)

// RunsPerRepo keeps the owner's runs-per-repo cap in the owner database: how
// many runs admission allows in one repository at once, which the surface sets
// and every poll reads (adr/0019).
type RunsPerRepo struct {
	setting intSetting
}

// NewRunsPerRepo returns a RunsPerRepo backed by client, which must be one
// opened on OwnerDatabase.
func NewRunsPerRepo(client *firestore.Client) *RunsPerRepo {
	return &RunsPerRepo{setting: intSetting{
		client: client, docID: runsPerRepoDocID, field: runsPerRepoField, name: "runs per repo cap",
		min: dispatcher.MinRunsPerRepo, max: dispatcher.MaxRunsPerRepo, def: dispatcher.DefaultRunsPerRepo,
	}}
}

// Value returns the saved cap, DefaultRunsPerRepo while none is saved. A stored
// value that is not a whole number in the setting's range is an error, so the
// dispatcher falls back to the default rather than running without a cap.
func (r *RunsPerRepo) Value(ctx context.Context) (int, error) { return r.setting.value(ctx) }

// Set saves the cap, effective from the next poll.
func (r *RunsPerRepo) Set(ctx context.Context, n int, at time.Time) error {
	return r.setting.set(ctx, n, at)
}
