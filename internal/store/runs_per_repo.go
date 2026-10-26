package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	settingsCollection = "settings"
	runsPerRepoDocID   = "runs-per-repo"
	runsPerRepoField   = "runs_per_repo"
)

// RunsPerRepo keeps the owner's runs-per-repo cap in the owner database: how
// many runs admission allows in one repository at once, which the surface sets
// and every poll reads (adr/0019).
type RunsPerRepo struct {
	client *firestore.Client
}

// NewRunsPerRepo returns a RunsPerRepo backed by client, which must be one
// opened on OwnerDatabase.
func NewRunsPerRepo(client *firestore.Client) *RunsPerRepo {
	return &RunsPerRepo{client: client}
}

func (r *RunsPerRepo) doc() *firestore.DocumentRef {
	return r.client.Collection(settingsCollection).Doc(runsPerRepoDocID)
}

// Value returns the saved cap, DefaultRunsPerRepo while none is saved. A stored
// value that is not a whole number in the setting's range is an error, so the
// dispatcher falls back to the default rather than running without a cap.
func (r *RunsPerRepo) Value(ctx context.Context) (int, error) {
	snap, err := r.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return dispatcher.DefaultRunsPerRepo, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the runs per repo cap: %w", err)
	}
	n, ok := snap.Data()[runsPerRepoField].(int64)
	if !ok || n < int64(dispatcher.MinRunsPerRepo) || n > int64(dispatcher.MaxRunsPerRepo) {
		return 0, fmt.Errorf("reading the runs per repo cap: %q is not a whole number from %d to %d",
			runsPerRepoField, dispatcher.MinRunsPerRepo, dispatcher.MaxRunsPerRepo)
	}
	return int(n), nil
}

// Set saves the cap, effective from the next poll.
func (r *RunsPerRepo) Set(ctx context.Context, n int, at time.Time) error {
	if n < dispatcher.MinRunsPerRepo || n > dispatcher.MaxRunsPerRepo {
		return fmt.Errorf("setting the runs per repo cap: %d is not from %d to %d",
			n, dispatcher.MinRunsPerRepo, dispatcher.MaxRunsPerRepo)
	}
	if _, err := r.doc().Set(ctx, map[string]any{runsPerRepoField: n, setAtField: at}); err != nil {
		return fmt.Errorf("setting the runs per repo cap: %w", err)
	}
	return nil
}
