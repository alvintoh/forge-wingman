package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// SweepAbandoned flips every claimed run that has recorded nothing since before
// its stage's window to abandoned and drops its ledger reservation in the same
// transaction, returning the runs it flipped. It leaves the concurrency limit
// alone: moving N is Settle's job, and an abandoned run showed nothing about it.
//
// The dropped reservation has no settled cost, so the budget can under-count
// what an abandoned run spent. A failure on one run does not stop the rest.
func (q *Queue) SweepAbandoned(ctx context.Context, now time.Time, w dispatcher.AbandonWindows) ([]dispatcher.Abandoned, error) {
	iter := q.client.Collection(runsCollection).Where(stateField, "==", stateClaimed).Select().Documents(ctx)
	defer iter.Stop()
	var ids []string
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing claimed runs: %w", err)
		}
		ids = append(ids, snap.Ref.ID)
	}
	var out []dispatcher.Abandoned
	var errs []error
	for _, id := range ids {
		a, swept, err := q.abandon(ctx, id, now, w)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if swept {
			out = append(out, a)
		}
	}
	return out, errors.Join(errs...)
}

// abandon flips run runID when, in one transaction, its row is still claimed
// with no outcome and no stop reason and was claimed before its stage's cutoff.
func (q *Queue) abandon(ctx context.Context, runID string, now time.Time, w dispatcher.AbandonWindows) (dispatcher.Abandoned, bool, error) {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	var out dispatcher.Abandoned
	var swept bool
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		out, swept = dispatcher.Abandoned{}, false
		snap, err := tx.Get(rowRef)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		row := snap.Data()
		claimedAt, _ := row[claimedAtField].(time.Time)
		if str(row, stateField) != stateClaimed || str(row, outcomeField) != "" || str(row, stopReasonField) != "" || claimedAt.IsZero() {
			return nil
		}
		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		_, entry, _ := ledger.reservation(runID)
		window := w.Build
		if entry.Stage == dispatcher.StagePlan {
			window = w.Plan
		}
		if !claimedAt.Before(now.Add(-window)) {
			return nil
		}
		if err := tx.Update(rowRef, []firestore.Update{
			{Path: stateField, Value: stateAbandoned},
			{Path: updatedAtField, Value: now},
		}); err != nil {
			return err
		}
		attempt, _ := row[attemptField].(int64)
		out = dispatcher.Abandoned{
			RunID: runID, Ticket: str(row, ticketIDField), Repo: str(row, repoField),
			Stage: entry.Stage, Attempt: int(attempt),
		}
		swept = true
		return dropReservations(tx, ledgerRef, ledger.Reservations, runID)
	})
	if err != nil {
		return dispatcher.Abandoned{}, false, fmt.Errorf("abandoning run %s: %w", runID, err)
	}
	return out, swept, nil
}
