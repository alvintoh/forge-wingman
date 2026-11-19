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
	phaseWakeDocID = "phase-wake"
	wokeAtField    = "woke_at"
)

// PhaseWakes keeps the dispatch/phase-wake document: when the webhook last woke
// the dispatcher to refresh the phase snapshots, shared by every instance so a
// burst of Linear edits starts one poll.
type PhaseWakes struct {
	client *firestore.Client
}

// NewPhaseWakes returns a PhaseWakes backed by client.
func NewPhaseWakes(client *firestore.Client) *PhaseWakes {
	return &PhaseWakes{client: client}
}

func (p *PhaseWakes) doc() *firestore.DocumentRef {
	return p.client.Collection(dispatchCollection).Doc(phaseWakeDocID)
}

// Claim records a wake at at and reports true, unless one was recorded within
// window of at, either side: a stored time ahead of the clock by more than the
// window is not trusted, so it cannot hold every later wake back.
func (p *PhaseWakes) Claim(ctx context.Context, at time.Time, window time.Duration) (bool, error) {
	at = at.Truncate(time.Microsecond)
	var claimed bool
	err := p.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		claimed = false
		snap, err := tx.Get(p.doc())
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if err == nil {
			if last, ok := snap.Data()[wokeAtField].(time.Time); ok && at.Sub(last).Abs() < window {
				return nil
			}
		}
		if err := tx.Set(p.doc(), map[string]any{wokeAtField: at}); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("claiming a phase wake: %w", err)
	}
	return claimed, nil
}

// Release removes the wake claimed at at, so a retried delivery wakes again. A
// claim made since is left alone.
func (p *PhaseWakes) Release(ctx context.Context, at time.Time) error {
	at = at.Truncate(time.Microsecond)
	err := p.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(p.doc())
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if last, ok := snap.Data()[wokeAtField].(time.Time); !ok || !last.Equal(at) {
			return nil
		}
		return tx.Delete(p.doc())
	})
	if err != nil {
		return fmt.Errorf("releasing a phase wake: %w", err)
	}
	return nil
}
