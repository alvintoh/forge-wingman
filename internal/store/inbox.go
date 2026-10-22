package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	waitReasonField      = "wait_reason"
	mergedByOwnerAtField = "merged_by_owner_at"
	mergedSHAField       = "merged_sha"
)

// InboxRun is one run whose PR is open and waiting to be merged, as the inbox
// lists it.
type InboxRun struct {
	Run        string
	Ticket     string
	Repo       string
	Size       string
	PRURL      string
	WaitReason runner.WaitReason
	UpdatedAt  time.Time
}

// Inbox returns the runs updated since whose PR is open and not merged, and
// that either name a wait reason or were not marked for auto-merge, newest
// first, up to limit.
//
// A PR closed unmerged on GitHub is not known here, so it stays listed until
// the owner's merge attempt finds it closed.
func (r *Records) Inbox(ctx context.Context, since time.Time, limit int) ([]InboxRun, error) {
	iter := r.client.Collection(runsCollection).
		Where(updatedAtField, ">", since).
		OrderBy(updatedAtField, firestore.Desc).
		Select(repoField, sizeField, prURLField, autoMergeField, outcomeField, ticketIDField, updatedAtField,
			waitReasonField, mergedByOwnerAtField, rewriteMergedAtField).
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
	}
	run.UpdatedAt, _ = row[updatedAtField].(time.Time)
	_, merged := row[rewriteMergedAtField].(time.Time)
	// Finalize writes the owner's merge time as the zero time until a merge sets it.
	mergedByOwner, _ := row[mergedByOwnerAtField].(time.Time)
	open := run.PRURL != "" && str(row, outcomeField) == string(runner.OutcomePROpened) && !merged && mergedByOwner.IsZero()
	return run, open && (run.WaitReason != "" || !autoMerged)
}
