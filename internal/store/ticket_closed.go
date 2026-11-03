package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const ticketClosedAtField = "ticket_closed_at"

// RecordTicketsClosed writes at as the ticket-closed time on each run the inbox
// window holds whose ticket is one of tickets, that stopped for the owner, and
// that is neither cancelled nor marked already, and returns how many it marked.
// The owner's cancel fields are left alone, so a run closed elsewhere stays told
// apart from one the owner cancelled. A row is written only while it is as it was
// read, so a retry landing in between is not marked.
func (q *Queue) RecordTicketsClosed(ctx context.Context, tickets []string, at time.Time) (int, error) {
	closed := make(map[string]bool, len(tickets))
	for _, t := range tickets {
		closed[t] = true
	}
	snaps, err := allDocs(q.client.Collection(runsCollection).
		Where(updatedAtField, ">", at.Add(-runner.TrailingWindow)).
		Select(ticketIDField, stateField, outcomeField, prURLField, settledAtField, claimedAtField,
			transientField, transientRequeuesField, mergedByOwnerAtField, rewriteMergedAtField,
			cancelledByOwnerAtField, ticketClosedAtField).
		Documents(ctx))
	if err != nil {
		return 0, fmt.Errorf("listing the runs whose ticket may have closed: %w", err)
	}
	marked := 0
	for _, snap := range snaps {
		row := snap.Data()
		if !closed[str(row, ticketIDField)] || cancelledRow(row) || ticketClosedRow(row) || !stoppedRun(row) {
			continue
		}
		_, err := snap.Ref.Update(ctx, []firestore.Update{{Path: ticketClosedAtField, Value: at}},
			firestore.LastUpdateTime(snap.UpdateTime))
		if status.Code(err) == codes.FailedPrecondition {
			continue
		}
		if err != nil {
			return marked, fmt.Errorf("recording run %s's ticket closed: %w", snap.Ref.ID, err)
		}
		marked++
	}
	return marked, nil
}

// ticketClosedRow reports whether a poll found row's ticket closed while the run
// was stopped.
func ticketClosedRow(row map[string]any) bool {
	at, _ := row[ticketClosedAtField].(time.Time)
	return !at.IsZero()
}
