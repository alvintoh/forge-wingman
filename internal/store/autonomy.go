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
	autonomyDocID = "autonomy"
	levelField    = "level"
)

// Autonomies keeps the dispatch/autonomy document the owner sets and every poll
// reads, beside the CD flag its conditions are checked against and the history
// of its changes.
type Autonomies struct {
	client  *firestore.Client
	cds     *CDs
	history *AutonomyHistory
}

// NewAutonomies returns an Autonomies backed by client.
func NewAutonomies(client *firestore.Client) *Autonomies {
	return &Autonomies{client: client, cds: NewCDs(client), history: NewAutonomyHistory(client)}
}

func (a *Autonomies) doc() *firestore.DocumentRef {
	return a.client.Collection(dispatchCollection).Doc(autonomyDocID)
}

// Level returns the stored level, 0 while none is set. A stored value naming no
// level is an error, so a caller does not guess what it meant.
func (a *Autonomies) Level(ctx context.Context) (dispatcher.Autonomy, error) {
	snap, err := a.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the autonomy level: %w", err)
	}
	raw, present := snap.Data()[levelField]
	if _, wasSet := snap.Data()[setAtField]; !present && !wasSet {
		// The document also holds the earned snapshot, so it can exist before an
		// owner has set a level; one that was set and lost its level is garbled.
		return 0, nil
	}
	stored, ok := raw.(int64)
	level := dispatcher.Autonomy(stored)
	if !ok || !level.Valid() {
		return 0, fmt.Errorf("reading the autonomy level: %w %v", dispatcher.ErrUnknownAutonomy, raw)
	}
	return level, nil
}

// Set stores level, effective from the next claim: an operator's runner
// autonomy action. It refuses a level whose advance condition does not hold, and
// the refusal carries the numbers it was measured against, so a level the CLI
// would let through is still refused at the write.
func (a *Autonomies) Set(ctx context.Context, level dispatcher.Autonomy, at time.Time) error {
	if err := level.Known(); err != nil {
		return fmt.Errorf("setting the autonomy level: %w", err)
	}
	// Only a level that auto-merges has a condition to read evidence for.
	if level.AutoMerges() {
		ev, err := a.Evidence(ctx)
		if err != nil {
			return fmt.Errorf("setting the autonomy level: %w", err)
		}
		if err := level.Settable(ev); err != nil {
			return fmt.Errorf("setting the autonomy level: %w", err)
		}
	}
	if _, err := a.doc().Set(ctx, map[string]any{levelField: int64(level), setAtField: at}, firestore.MergeAll); err != nil {
		return fmt.Errorf("setting the autonomy level: %w", err)
	}
	return nil
}

// Evidence reads the level with everything its advance conditions are checked
// against: the merged agent PRs' samples, newest first, and the CD flag recorded
// for each repository they name.
func (a *Autonomies) Evidence(ctx context.Context) (dispatcher.Evidence, error) {
	level, err := a.Level(ctx)
	if err != nil {
		return dispatcher.Evidence{}, err
	}
	samples, err := recentSamples(ctx, a.client, dispatcher.SampledPRs)
	if err != nil {
		return dispatcher.Evidence{}, err
	}
	cd, err := a.cds.Recorded(ctx)
	if err != nil {
		return dispatcher.Evidence{}, err
	}
	return dispatcher.Evidence{Level: level, Trailing: samples, CD: cd}, nil
}

// AutonomyView is the surface's read of the level: the one the owner set, the
// snapshot the measured PRs last earned, and the changes recorded since, newest
// first.
type AutonomyView struct {
	Level dispatcher.Autonomy
	// Earned is the earned snapshot, and HasEarned is false while none is stored.
	Earned    dispatcher.Earned
	HasEarned bool
	History   []dispatcher.Change
}

// AutonomyView reads the stored level, the earned snapshot beside it and up to
// history changes, newest first.
func (a *Autonomies) AutonomyView(ctx context.Context, history int) (AutonomyView, error) {
	level, err := a.Level(ctx)
	if err != nil {
		return AutonomyView{}, err
	}
	earned, found, err := a.Earned(ctx)
	if err != nil {
		return AutonomyView{}, err
	}
	changes, err := a.history.List(ctx, history)
	if err != nil {
		return AutonomyView{}, err
	}
	return AutonomyView{Level: level, Earned: earned, HasEarned: found, History: changes}, nil
}
