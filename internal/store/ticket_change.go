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

// RecordTicketChanges compares each ticket's current description with the copy
// its run was claimed with and records the difference on the run row: the first
// time it was seen, and the description Linear holds now. A run stamped by an
// earlier poll keeps its first time and has its stored description refreshed, so
// the PR quotes the latest difference. It returns the runs it stamped this call,
// so a caller logs each run once.
//
// A run whose ticket still matches its copy and carries no recorded change
// changes nothing; a run already stamped has its stored description refreshed. A
// run whose PR has opened or whose run has ended is left alone, and a run whose
// ticket is not in the poll's delegated set is skipped. A failure stops the
// comparison; the next poll retries it.
func (q *Queue) RecordTicketChanges(ctx context.Context, tickets map[string]string, at time.Time) ([]string, error) {
	if len(tickets) == 0 {
		return nil, nil
	}
	iter := q.client.Collection(runsCollection).
		Where(stateField, "in", []string{stateClaimed, statePlanned}).
		Select(ticketIDField).
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
		if !ok {
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

// recordTicketChange records body as ref's ticket change, in a transaction so
// two polls cannot both record the first sighting. It reports whether it stamped
// a first sighting, which a caller logs once; a later description only refreshes
// the stored text.
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
		changedAt := ticketChangedAt(row)
		if changedAt.IsZero() {
			if str(row, ticketBodyField) == body {
				return nil
			}
			stamped = true
			return tx.Update(ref, []firestore.Update{
				{Path: ticketChangedAtField, Value: at},
				{Path: ticketChangedBodyField, Value: body},
			})
		}
		if str(row, ticketChangedBodyField) == body {
			return nil
		}
		return tx.Update(ref, []firestore.Update{{Path: ticketChangedBodyField, Value: body}})
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
