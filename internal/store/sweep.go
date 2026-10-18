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

// abandonedNoOutcome is the reason a claim is abandoned: its run reported no
// outcome within its window.
const abandonedNoOutcome = "no-outcome-past-window"

// SweepAbandoned flips every claimed run that has recorded nothing since before
// its stage's window to abandoned and drops its ledger reservation in the same
// transaction, returning the runs it flipped. It leaves the concurrency limit
// alone: moving N is Settle's job, and an abandoned run showed nothing about it.
//
// The dropped reservation has no settled cost, so the budget can under-count
// what an abandoned run spent. A failure on one run does not stop the rest.
//
// It then drops every reservation past its window whose run row has settled.
func (q *Queue) SweepAbandoned(ctx context.Context, now time.Time, w dispatcher.AbandonWindows) ([]dispatcher.Abandoned, error) {
	ids, err := q.staleClaims(ctx, now, w)
	var out []dispatcher.Abandoned
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
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
	errs = append(errs, q.releaseSettledReservations(ctx, now, w))
	return out, errors.Join(errs...)
}

// staleClaims lists the claimed runs that may have outlived a window, so a run
// that recorded an outcome never opens a transaction. A claimed row keeps its
// state after its record is written, so these accumulate with every run, and an
// equality filter on the outcome would miss a requeued row, whose outcome is
// deleted rather than empty. The listing therefore reads three fields and
// filters here; abandon stays the authority.
func (q *Queue) staleClaims(ctx context.Context, now time.Time, w dispatcher.AbandonWindows) ([]string, error) {
	cutoff := now.Add(-min(w.Plan, w.Build))
	iter := q.client.Collection(runsCollection).Where(stateField, "==", stateClaimed).
		Select(outcomeField, stopReasonField, claimedAtField).Documents(ctx)
	defer iter.Stop()
	var ids []string
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return ids, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing claimed runs: %w", err)
		}
		row := snap.Data()
		claimedAt, _ := row[claimedAtField].(time.Time)
		if str(row, outcomeField) != "" || str(row, stopReasonField) != "" || claimedAt.IsZero() || !claimedAt.Before(cutoff) {
			continue
		}
		ids = append(ids, snap.Ref.ID)
	}
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
		attempt := rowAttempt(row)
		_, entry, _ := ledger.reservation(runID, attempt)
		if !claimedAt.Before(now.Add(-w.For(entry.Stage))) {
			return nil
		}
		found := dispatcher.Abandoned{
			RunID: runID, Ticket: str(row, ticketIDField), Repo: str(row, repoField),
			Stage: entry.Stage, Attempt: attempt, TicketState: str(row, ticketStateField),
		}
		notice := found.Notice()
		noticeRef := NewNotices(q.client).doc(notice.ID)
		_, err = tx.Get(noticeRef)
		noticed := err == nil
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if err := tx.Update(rowRef, []firestore.Update{
			{Path: stateField, Value: stateAbandoned},
			{Path: updatedAtField, Value: now},
			{Path: abandonedReasonField, Value: abandonedNoOutcome},
			{Path: abandonedAtField, Value: now},
			{Path: ticketStateField, Value: firestore.Delete},
		}); err != nil {
			return err
		}
		if !noticed {
			if err := tx.Create(noticeRef, noticeData(notice, now)); err != nil {
				return err
			}
		}
		out, swept = found, true
		return dropReservations(tx, ledgerRef, ledger, runID, attempt)
	})
	if err != nil {
		return dispatcher.Abandoned{}, false, fmt.Errorf("abandoning run %s: %w", runID, err)
	}
	return out, swept, nil
}

// reservationExpired reports whether entry was claimed before its stage's window.
func reservationExpired(entry reservationEntry, now time.Time, w dispatcher.AbandonWindows) bool {
	return !entry.ClaimedAt.IsZero() && entry.ClaimedAt.Before(now.Add(-w.For(entry.Stage)))
}

// releaseSettledReservations drops each reservation past its window whose run
// row has settled. A failure on one key does not stop the rest.
func (q *Queue) releaseSettledReservations(ctx context.Context, now time.Time, w dispatcher.AbandonWindows) error {
	var keys []string
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		keys = nil
		ledger, err := q.readLedger(tx, q.ledgerRef())
		if err != nil {
			return err
		}
		for key, entry := range ledger.Reservations {
			if reservationExpired(entry, now, w) {
				keys = append(keys, key)
			}
		}
		return nil
	}, firestore.ReadOnly)
	if err != nil {
		return fmt.Errorf("listing expired reservations: %w", err)
	}
	var errs []error
	for _, key := range keys {
		released, err := q.releaseIfSettled(ctx, key, now, w)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if released {
			q.logger.InfoContext(ctx, "reservationReleased", "run", reservationRunID(key), "key", key)
		}
	}
	return errors.Join(errs...)
}

// releaseIfSettled drops the reservation under key when, in one transaction, it
// is still past its window and its run row is missing, no longer claimed, carries
// an outcome or a stop reason, or has moved on to a later attempt.
func (q *Queue) releaseIfSettled(ctx context.Context, key string, now time.Time, w dispatcher.AbandonWindows) (bool, error) {
	ledgerRef := q.ledgerRef()
	rowRef := q.client.Collection(runsCollection).Doc(reservationRunID(key))
	var released bool
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		released = false
		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		entry, held := ledger.Reservations[key]
		if !held || !reservationExpired(entry, now, w) {
			return nil
		}
		snap, err := tx.Get(rowRef)
		settled := status.Code(err) == codes.NotFound
		if err != nil && !settled {
			return err
		}
		if !settled {
			row := snap.Data()
			settled = str(row, stateField) != stateClaimed || str(row, outcomeField) != "" || str(row, stopReasonField) != "" ||
				entry.Attempt < rowAttempt(row)
		}
		if !settled {
			return nil
		}
		released = true
		return tx.Update(ledgerRef, []firestore.Update{{FieldPath: firestore.FieldPath{"reservations", key}, Value: firestore.Delete}})
	})
	if err != nil {
		return false, fmt.Errorf("releasing reservation %s: %w", key, err)
	}
	return released, nil
}
