package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	autoMergeSwitchDocID = "auto-merge"
	enabledField         = "enabled"
)

// AutoMergeSwitches keeps the dispatch/auto-merge document the owner sets and every pr-meta reads.
type AutoMergeSwitches struct {
	client *firestore.Client
}

// NewAutoMergeSwitches returns an AutoMergeSwitches backed by client.
func NewAutoMergeSwitches(client *firestore.Client) *AutoMergeSwitches {
	return &AutoMergeSwitches{client: client}
}

func (a *AutoMergeSwitches) doc() *firestore.DocumentRef {
	return a.client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID)
}

// Enabled reports whether auto-merge is on, which it is while no switch is set.
// A stored value that is not a boolean is an error, so a caller does not guess.
func (a *AutoMergeSwitches) Enabled(ctx context.Context) (bool, error) {
	snap, err := a.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the auto-merge switch: %w", err)
	}
	enabled, ok := snap.Data()[enabledField].(bool)
	if !ok {
		return false, fmt.Errorf("reading the auto-merge switch: %q is not a boolean", enabledField)
	}
	return enabled, nil
}

// Set stores the switch, effective from the next pr-meta: an operator's runner
// auto-merge action.
func (a *AutoMergeSwitches) Set(ctx context.Context, enabled bool, at time.Time) error {
	if _, err := a.doc().Set(ctx, map[string]any{enabledField: enabled, setAtField: at}); err != nil {
		return fmt.Errorf("setting the auto-merge switch: %w", err)
	}
	return nil
}
