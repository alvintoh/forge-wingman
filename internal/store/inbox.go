package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	waitReasonField         = "wait_reason"
	mergedByOwnerAtField    = "merged_by_owner_at"
	mergedSHAField          = "merged_sha"
	ticketTitleField        = "ticket_title"
	cancelledByOwnerAtField = "cancelled_by_owner_at"
	cancelledByField        = "cancelled_by"
)

// InboxRun is one run waiting on the owner, as the inbox lists it: one whose PR
// is open and waiting to be merged, or one that stopped. Attempt is the attempt
// the row is on, which a retry names to say which it saw.
type InboxRun struct {
	Run        string
	Ticket     string
	Repo       string
	Size       string
	PRURL      string
	WaitReason runner.WaitReason
	Attempt    int
	Outcome    string
	StopReason string
	// Stopped is set for a run that ended and waits for a retry, whatever PR the row
	// still names: an earlier attempt's draft stays on it until a later one opens its own.
	Stopped bool
	// Title is the ticket's title, read for a stopped run only, which has no PR whose
	// title could name it. The ticket's body is never read.
	Title     string
	UpdatedAt time.Time
}

// Inbox returns the runs updated since whose PR is open and not merged, that
// the owner has not cancelled, and that either name a wait reason or were not
// marked for auto-merge, and the runs that stopped and have no automatic requeue
// due, newest first, up to limit.
//
// A PR closed unmerged on GitHub is not known here, so it stays listed until
// the owner's merge attempt finds it closed.
func (r *Records) Inbox(ctx context.Context, since time.Time, limit int) ([]InboxRun, error) {
	iter := r.client.Collection(runsCollection).
		Where(updatedAtField, ">", since).
		OrderBy(updatedAtField, firestore.Desc).
		Select(repoField, sizeField, prURLField, autoMergeField, outcomeField, ticketIDField, updatedAtField,
			waitReasonField, mergedByOwnerAtField, rewriteMergedAtField, stateField, stopReasonField, attemptField,
			settledAtField, claimedAtField, transientField, transientRequeuesField, ticketTitleField,
			cancelledByOwnerAtField).
		Documents(ctx)
	defer iter.Stop()
	var out []InboxRun
	for len(out) < limit {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing the runs waiting on the owner: %w", err)
		}
		if run, ok := inboxRun(snap.Ref.ID, snap.Data()); ok {
			out = append(out, run)
		}
	}
	return out, nil
}

// inboxRun reads one row as an inbox entry, and whether the row belongs in it.
func inboxRun(id string, row map[string]any) (InboxRun, bool) {
	autoMerged, _ := row[autoMergeField].(bool)
	run := InboxRun{
		Run:        id,
		Ticket:     str(row, ticketIDField),
		Repo:       str(row, repoField),
		Size:       str(row, sizeField),
		PRURL:      str(row, prURLField),
		WaitReason: runner.WaitReason(str(row, waitReasonField)),
		Attempt:    rowAttempt(row),
		Outcome:    str(row, outcomeField),
		StopReason: str(row, stopReasonField),
	}
	run.UpdatedAt, _ = row[updatedAtField].(time.Time)
	if cancelledRow(row) {
		return run, false
	}
	if stoppedRun(row) {
		run.Stopped = true
		run.Title = str(row, ticketTitleField)
		return run, true
	}
	open := run.PRURL != "" && run.Outcome == string(runner.OutcomePROpened) && !mergedRow(row)
	return run, open && (run.WaitReason != "" || !autoMerged)
}

// cancelledRow reports whether the owner cancelled row's run. Finalize writes the
// cancel time as the zero time until a cancel sets it. A cancelled run is neither
// waiting nor stopped for the owner, so the inbox drops it whatever else the row says.
func cancelledRow(row map[string]any) bool {
	at, _ := row[cancelledByOwnerAtField].(time.Time)
	return !at.IsZero()
}

// mergedRow reports whether row's PR was merged, by the owner or by a poll.
func mergedRow(row map[string]any) bool {
	if _, merged := row[rewriteMergedAtField].(time.Time); merged {
		return true
	}
	// Finalize writes the owner's merge time as the zero time until a merge sets it.
	at, _ := row[mergedByOwnerAtField].(time.Time)
	return !at.IsZero()
}

// stoppedRun reports whether row is a run that ended and waits for the owner to
// retry it. A run that made no changes has nothing a note could change, and one a
// poll will requeue by itself is not the owner's yet.
func stoppedRun(row map[string]any) bool {
	if _, _, finished := finishedClaim(row); !finished && str(row, stateField) != stateStopped {
		return false
	}
	return str(row, outcomeField) != string(runner.OutcomeNoChanges) && !mergedRow(row) && !requeueDue(row)
}

// requeueDue reports whether a poll will requeue row by itself: it stopped on a
// transient fault the dispatcher knows and has automatic requeues left.
func requeueDue(row map[string]any) bool {
	class := runner.TransientClass(str(row, transientField))
	return class.Listed() && len(transientRequeues(row)) < dispatcher.DefaultTransientRequeues
}
