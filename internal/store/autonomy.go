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

// Autonomies keeps the dispatch/autonomy document the owner sets and every poll reads.
type Autonomies struct {
	client *firestore.Client
}

// NewAutonomies returns an Autonomies backed by client.
func NewAutonomies(client *firestore.Client) *Autonomies {
	return &Autonomies{client: client}
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
	stored, ok := snap.Data()[levelField].(int64)
	level := dispatcher.Autonomy(stored)
	if !ok || !level.Valid() {
		return 0, fmt.Errorf("reading the autonomy level: %w %v", dispatcher.ErrUnknownAutonomy, snap.Data()[levelField])
	}
	return level, nil
}

// Set stores level, effective from the next claim: an operator's runner
// autonomy action. It refuses a level the owner may not set.
func (a *Autonomies) Set(ctx context.Context, level dispatcher.Autonomy, at time.Time) error {
	if err := level.Settable(); err != nil {
		return fmt.Errorf("setting the autonomy level: %w", err)
	}
	if _, err := a.doc().Set(ctx, map[string]any{levelField: int64(level), setAtField: at}); err != nil {
		return fmt.Errorf("setting the autonomy level: %w", err)
	}
	return nil
}
