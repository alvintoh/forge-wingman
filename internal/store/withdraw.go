package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The fields a withdrawn run records: when a poll found its ticket closed while
// the run waited for a claim, and the Linear state type that closed it.
const (
	withdrawnAtField     = "withdrawn_at"
	withdrawnReasonField = "withdrawn_reason"
)

// UnclaimedTickets returns the ticket of every run still waiting for a claim:
// the queued and the planned.
func (q *Queue) UnclaimedTickets(ctx context.Context) ([]string, error) {
	iter := q.client.Collection(runsCollection).
		Where(stateField, "in", candidateStates).
		Documents(ctx)
	defer iter.Stop()
	var tickets []string
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing the runs waiting for a claim: %w", err)
		}
		tickets = append(tickets, snap.Ref.ID)
	}
	return tickets, nil
}

// WithdrawRuns withdraws each named ticket's run while it still waits for a
// claim, recording reason and at on its row, and returns how many it withdrew.
// A run claimed, waiting on the owner or settled since the poll read it is left
// as it is, and so is a ticket with no row. Each withdrawal is logged once as
// runWithdrawn, with the run and the state type that closed its ticket as
// separate attributes.
func (q *Queue) WithdrawRuns(ctx context.Context, reasons map[string]string, at time.Time) (int, error) {
	withdrawn := 0
	for ticket, reason := range reasons {
		ok, err := q.withdrawRun(ctx, ticket, reason, at)
		if err != nil {
			return withdrawn, err
		}
		if !ok {
			continue
		}
		withdrawn++
		q.logger.InfoContext(ctx, "runWithdrawn", "run", ticket, "state_type", reason)
	}
	return withdrawn, nil
}

// withdrawRun moves runID's row to withdrawn while it still waits for a claim,
// reporting whether it did.
func (q *Queue) withdrawRun(ctx context.Context, runID, reason string, at time.Time) (bool, error) {
	ref := q.client.Collection(runsCollection).Doc(runID)
	withdrawn := false
	err := q.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading run %s: %w", runID, err)
		}
		withdrawn = slices.Contains(candidateStates, str(snap.Data(), stateField))
		if !withdrawn {
			return nil
		}
		return tx.Update(ref, []firestore.Update{
			{Path: stateField, Value: stateWithdrawn},
			{Path: updatedAtField, Value: at},
			{Path: withdrawnAtField, Value: at},
			{Path: withdrawnReasonField, Value: reason},
		})
	})
	if err != nil {
		return false, fmt.Errorf("withdrawing run %s: %w", runID, err)
	}
	return withdrawn, nil
}
