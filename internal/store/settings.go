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
	runsPerRepoDocID = "runs-per-repo"
	runsPerRepoField = "runs_per_repo"
)

// Settings keeps the dispatch/runs-per-repo document the owner sets from the
// surface's Config settings and every poll reads before it admits runs.
type Settings struct {
	client *firestore.Client
}

// NewSettings returns a Settings backed by client.
func NewSettings(client *firestore.Client) *Settings {
	return &Settings{client: client}
}

func (s *Settings) doc() *firestore.DocumentRef {
	return s.client.Collection(dispatchCollection).Doc(runsPerRepoDocID)
}

// RunsPerRepo reads how many runs one repository may hold in flight at once,
// which is dispatcher.DefaultLimits.RepoCap while no setting is stored. A
// stored value that is not a whole number in the allowed range is an error, so
// a caller falls back to the default rather than guessing.
func (s *Settings) RunsPerRepo(ctx context.Context) (int, error) {
	snap, err := s.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return dispatcher.DefaultLimits.RepoCap, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the runs-per-repo setting: %w", err)
	}
	n, ok := snap.Data()[runsPerRepoField].(int64)
	if !ok || !dispatcher.ValidRunsPerRepo(int(n)) {
		return 0, fmt.Errorf("reading the runs-per-repo setting: %q is not a cap from %d to %d",
			runsPerRepoField, dispatcher.MinRunsPerRepo, dispatcher.MaxRunsPerRepo)
	}
	return int(n), nil
}

// SetRunsPerRepo stores the cap for the next poll. A value outside the range is
// refused rather than stored, so the setting never holds one the surface could
// not have offered.
func (s *Settings) SetRunsPerRepo(ctx context.Context, n int, at time.Time) error {
	if !dispatcher.ValidRunsPerRepo(n) {
		return fmt.Errorf("setting the runs-per-repo cap: %d is not from %d to %d",
			n, dispatcher.MinRunsPerRepo, dispatcher.MaxRunsPerRepo)
	}
	if _, err := s.doc().Set(ctx, map[string]any{runsPerRepoField: n, setAtField: at}); err != nil {
		return fmt.Errorf("setting the runs-per-repo cap: %w", err)
	}
	return nil
}
