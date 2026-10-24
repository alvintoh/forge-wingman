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

// SettledRun is one settled run as the run history lists it: how it ended and
// whether its PR was merged. It carries no ticket text.
type SettledRun struct {
	Run    string
	Ticket string
	// Repo is the repository the run was queued for as owner/name; empty when
	// the row records none.
	Repo       string
	Outcome    runner.Outcome
	StopReason runner.StopReason
	WaitReason runner.WaitReason
	Merged     bool
	// Cancelled is whether the owner cancelled the run from its inbox row.
	Cancelled bool
	SettledAt time.Time
}

// RecentSettled returns the limit most recently settled runs, newest first. A
// row carrying no settlement is never listed.
func (r *Records) RecentSettled(ctx context.Context, limit int) ([]SettledRun, error) {
	iter := r.client.Collection(runsCollection).
		Where(settledAtField, ">", time.Time{}).
		OrderBy(settledAtField, firestore.Desc).
		Limit(limit).
		Select(ticketIDField, repoField, outcomeField, stopReasonField, waitReasonField, settledAtField,
			mergedByOwnerAtField, rewriteMergedAtField, cancelledByOwnerAtField).
		Documents(ctx)
	defer iter.Stop()
	var out []SettledRun
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing the settled runs: %w", err)
		}
		out = append(out, historyRun(snap.Ref.ID, snap.Data()))
	}
	return out, nil
}

// historyRun reads one row as a history entry.
func historyRun(id string, row map[string]any) SettledRun {
	run := SettledRun{
		Run:        id,
		Ticket:     str(row, ticketIDField),
		Repo:       str(row, repoField),
		Outcome:    runner.Outcome(str(row, outcomeField)),
		StopReason: runner.StopReason(str(row, stopReasonField)),
		WaitReason: runner.WaitReason(str(row, waitReasonField)),
		Merged:     settledMerged(row),
	}
	run.SettledAt, _ = row[settledAtField].(time.Time)
	cancelled, _ := row[cancelledByOwnerAtField].(time.Time)
	run.Cancelled = !cancelled.IsZero()
	return run
}

// settledMerged reports whether row's PR was merged: by the owner through the
// surface, or by a merge the dispatcher's poll measured.
func settledMerged(row map[string]any) bool {
	_, polled := row[rewriteMergedAtField].(time.Time)
	// Finalize writes the owner's merge time as the zero time until a merge sets it.
	byOwner, _ := row[mergedByOwnerAtField].(time.Time)
	return polled || !byOwner.IsZero()
}
