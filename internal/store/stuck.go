package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// noPROutcomes are the outcomes a finished run records when it opened no PR,
// so the listing never reads the runs that merged.
var noPROutcomes = []string{
	string(runner.OutcomeBuilt), string(runner.OutcomeNoChanges), string(runner.OutcomeStopped),
	string(runner.OutcomeAgentFailed), string(runner.OutcomeInfraFailure), string(runner.OutcomeBudgetStop),
}

// StuckRuns lists a queued or planned run waiting since before before, and a
// finished claim that opened no PR or a stopped or abandoned run that ended in
// [since, before). It skips a run the owner cancelled and writes nothing.
func (q *Queue) StuckRuns(ctx context.Context, before, since time.Time) ([]dispatcher.Stuck, error) {
	runs := q.client.Collection(runsCollection)
	var out []dispatcher.Stuck
	add := func(id string, row map[string]any, state, reason string) {
		out = append(out, dispatcher.Stuck{RunID: id, Ticket: str(row, ticketIDField), State: state, Reason: reason})
	}
	addEnded := func(id string, row map[string]any, state, reason string, at time.Time) {
		if !at.IsZero() && !at.Before(since) && at.Before(before) {
			add(id, row, state, reason)
		}
	}

	waiting := runs.Where(stateField, "in", candidateStates).
		Select(ticketIDField, stateField, queuedAtField, waitingOnField).Documents(ctx)
	err := eachRow(waiting, func(id string, row map[string]any) {
		at, _ := row[queuedAtField].(time.Time)
		if at.IsZero() || !at.Before(before) {
			return
		}
		state := dispatcher.StuckQueued
		if str(row, stateField) == statePlanned {
			state = dispatcher.StuckPlanned
		}
		add(id, row, state, str(row, waitingOnField))
	})
	if err != nil {
		return nil, fmt.Errorf("listing waiting runs: %w", err)
	}

	finished := runs.Where(outcomeField, "in", noPROutcomes).
		Select(ticketIDField, stateField, outcomeField, stopReasonField, prURLField, settledAtField, claimedAtField, cancelledByOwnerAtField).Documents(ctx)
	err = eachRow(finished, func(id string, row map[string]any) {
		if reason, at, ok := finishedClaim(row); ok && !cancelledRow(row) {
			addEnded(id, row, dispatcher.StuckStopped, reason, at)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("listing finished runs: %w", err)
	}

	ended := runs.Where(stateField, "in", []string{stateStopped, stateAbandoned}).
		Select(ticketIDField, stateField, stopReasonField, abandonedReasonField, abandonedAtField, settledAtField, updatedAtField, cancelledByOwnerAtField).Documents(ctx)
	err = eachRow(ended, func(id string, row map[string]any) {
		if cancelledRow(row) {
			return
		}
		state, reason := dispatcher.StuckStopped, str(row, stopReasonField)
		if str(row, stateField) == stateAbandoned {
			state, reason = dispatcher.StuckAbandoned, str(row, abandonedReasonField)
		}
		var at time.Time
		for _, field := range []string{abandonedAtField, settledAtField, updatedAtField} {
			if at, _ = row[field].(time.Time); !at.IsZero() {
				break
			}
		}
		addEnded(id, row, state, reason, at)
	})
	if err != nil {
		return nil, fmt.Errorf("listing ended runs: %w", err)
	}
	return out, nil
}

// finishedClaim reports whether row is a claim that finished and opened no PR,
// with the reason it names and when it settled. A claim made after it settled is
// a later attempt still running, so it is not one.
func finishedClaim(row map[string]any) (reason string, settledAt time.Time, ok bool) {
	if str(row, stateField) != stateClaimed || str(row, prURLField) != "" || !slices.Contains(noPROutcomes, str(row, outcomeField)) {
		return "", time.Time{}, false
	}
	at, _ := row[settledAtField].(time.Time)
	if claimed, _ := row[claimedAtField].(time.Time); claimed.After(at) {
		return "", time.Time{}, false
	}
	reason = str(row, stopReasonField)
	if reason == "" {
		reason = str(row, outcomeField)
	}
	return reason, at, true
}

// eachRow calls fn for every document iter yields.
func eachRow(iter *firestore.DocumentIterator, fn func(id string, row map[string]any)) error {
	snaps, err := allDocs(iter)
	for _, snap := range snaps {
		fn(snap.Ref.ID, snap.Data())
	}
	return err
}
