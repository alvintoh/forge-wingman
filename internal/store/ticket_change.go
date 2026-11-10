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
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// RecordTicketChanges compares each ticket's current description with the copy
// its run was claimed with and records the difference on the run row: the first
// time it was seen, and the description Linear holds now. A run stamped by an
// earlier poll keeps its first time and has its stored description refreshed, so
// the PR quotes the latest difference; one whose description matches its copy
// again has the stamp cleared, so a draft never quotes nothing. It returns the
// runs it stamped this call, so a caller logs each run once. A stamp cleared by
// a requeue, a release or a revert can be stamped, and logged, again.
//
// "Changed" is runner.TicketChanged, the definition the PR's quote uses, so a
// re-save that moves no word changes nothing. A run whose PR has opened or whose
// run has ended is left alone, and a run whose ticket is not in the poll's
// delegated set is skipped. The query reads only the fields the comparison
// needs, and a transaction opens only for a run that has a write to make. A
// failure stops the comparison; the next poll retries it.
func (q *Queue) RecordTicketChanges(ctx context.Context, tickets map[string]string, at time.Time) ([]string, error) {
	if len(tickets) == 0 {
		return nil, nil
	}
	iter := q.client.Collection(runsCollection).
		Where(stateField, "in", []string{stateClaimed, statePlanned}).
		Select(ticketIDField, ticketBodyField, ticketChangedAtField, ticketChangedBodyField).
		Documents(ctx)
	defer iter.Stop()
	var changed []string
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return changed, fmt.Errorf("listing the runs in flight: %w", err)
		}
		body, ok := tickets[str(snap.Data(), ticketIDField)]
		if !ok || planTicketChange(snap.Data(), body) == ticketChangeNone {
			continue
		}
		stamped, err := q.recordTicketChange(ctx, snap.Ref, body, at)
		if err != nil {
			return changed, fmt.Errorf("recording run %s's ticket change: %w", snap.Ref.ID, err)
		}
		if stamped {
			changed = append(changed, snap.Ref.ID)
		}
	}
	return changed, nil
}

// ticketChangeAction is the write a run's ticket change calls for.
type ticketChangeAction int

const (
	ticketChangeNone ticketChangeAction = iota
	// ticketChangeStamp records the first sighting of a change.
	ticketChangeStamp
	// ticketChangeRefresh stores a later description on a stamped run.
	ticketChangeRefresh
	// ticketChangeClear removes the stamp from a run whose ticket matches its
	// copy again.
	ticketChangeClear
)

// planTicketChange decides, from a run row and the description Linear holds now,
// which write the row needs. It reads no state or PR field: the transaction
// re-checks those, and this is the same decision without the transaction.
func planTicketChange(row map[string]any, body string) ticketChangeAction {
	changed := runner.TicketChanged(str(row, ticketBodyField), body)
	switch {
	case ticketChangedAt(row).IsZero() && changed:
		return ticketChangeStamp
	case ticketChangedAt(row).IsZero():
		return ticketChangeNone
	case !changed:
		return ticketChangeClear
	case str(row, ticketChangedBodyField) == body:
		return ticketChangeNone
	default:
		return ticketChangeRefresh
	}
}

// recordTicketChange applies the write planTicketChange names to ref, in a
// transaction so two polls cannot both record the first sighting. It reports
// whether it stamped a first sighting, which a caller logs once.
func (q *Queue) recordTicketChange(ctx context.Context, ref *firestore.DocumentRef, body string, at time.Time) (bool, error) {
	var stamped bool
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		stamped = false
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		row := snap.Data()
		state := str(row, stateField)
		if (state != stateClaimed && state != statePlanned) || hasEnded(row) || str(row, prURLField) != "" {
			return nil
		}
		switch planTicketChange(row, body) {
		case ticketChangeNone:
			return nil
		case ticketChangeStamp:
			stamped = true
			return tx.Update(ref, []firestore.Update{
				{Path: ticketChangedAtField, Value: at},
				{Path: ticketChangedBodyField, Value: body},
			})
		case ticketChangeRefresh:
			return tx.Update(ref, []firestore.Update{{Path: ticketChangedBodyField, Value: body}})
		case ticketChangeClear:
			return tx.Update(ref, []firestore.Update{
				{Path: ticketChangedAtField, Value: firestore.Delete},
				{Path: ticketChangedBodyField, Value: firestore.Delete},
			})
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return stamped, nil
}

// TicketChange reads the change recorded on run runID: when its ticket first
// differed from the copy the run was claimed with, and the latest description
// the poll read. A run whose ticket did not change reads the zero value.
func (q *Queue) TicketChange(ctx context.Context, runID string) (dispatcher.TicketChange, error) {
	snap, err := q.client.Collection(runsCollection).Doc(runID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return dispatcher.TicketChange{}, nil
	}
	if err != nil {
		return dispatcher.TicketChange{}, fmt.Errorf("reading run %s's ticket change: %w", runID, err)
	}
	row := snap.Data()
	return dispatcher.TicketChange{At: ticketChangedAt(row), Body: str(row, ticketChangedBodyField)}, nil
}

// ticketChangedAt is row's recorded ticket change time, zero when it has none.
func ticketChangedAt(row map[string]any) time.Time {
	at, _ := row[ticketChangedAtField].(time.Time)
	return at
}
