package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

var (
	stuckSince  = time.Date(2021, 3, 1, 0, 0, 0, 0, time.UTC)
	stuckBefore = stuckSince.Add(7 * 24 * time.Hour)
	stuckInside = stuckSince.Add(24 * time.Hour)
)

// stuckRows writes rows under fresh ids, returning each row's id by name.
func stuckRows(t *testing.T, client *firestore.Client, rows map[string]map[string]any) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for name, row := range rows {
		id := fresh("stuck-" + name)
		forget(t, client, id)
		row[ticketIDField] = id
		if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	return ids
}

func stuckByID(t *testing.T, q *Queue) map[string]dispatcher.Stuck {
	t.Helper()
	got, err := q.StuckRuns(context.Background(), stuckBefore, stuckSince)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]dispatcher.Stuck{}
	for _, s := range got {
		out[s.RunID] = s
	}
	return out
}

func TestStuckRunsReturnsEachStuckRowShapeAndSkipsTheRest(t *testing.T) {
	q, client := queue(t)
	ids := stuckRows(t, client, map[string]map[string]any{
		"queued":       {stateField: stateQueued, queuedAtField: stuckInside, waitingOnField: dispatcher.ConditionBlocked},
		"planned":      {stateField: statePlanned, queuedAtField: stuckInside},
		"too-new":      {stateField: stateQueued, queuedAtField: stuckBefore},
		"long-queued":  {stateField: stateQueued, queuedAtField: stuckSince.Add(-time.Hour)},
		"just-settled": {stateField: stateClaimed, outcomeField: "agent-failed", settledAtField: stuckBefore},
		"settled-old":  {stateField: stateClaimed, outcomeField: "agent-failed", settledAtField: stuckSince.Add(-time.Hour)},
		"finished":     {stateField: stateClaimed, outcomeField: "agent-failed", stopReasonField: "agent-exit", settledAtField: stuckInside},
		"outcome-only": {stateField: stateClaimed, outcomeField: "no-changes", settledAtField: stuckInside},
		"with-pr":      {stateField: stateClaimed, outcomeField: "built", prURLField: "https://github.com/o/r/pull/1", settledAtField: stuckInside},
		"in-flight":    {stateField: stateClaimed, claimedAtField: stuckInside},
		"reclaimed":    {stateField: stateClaimed, outcomeField: "agent-failed", stopReasonField: "agent-timeout", settledAtField: stuckInside, claimedAtField: stuckInside.Add(time.Hour)},
		"requeued":     {stateField: stateQueued, queuedAtField: stuckBefore, settledAtField: stuckInside, outcomeField: "agent-failed", stopReasonField: "agent-exit"},
		"stopped":      {stateField: stateStopped, stopReasonField: "waiting-on-owner", updatedAtField: stuckInside},
		"abandoned":    {stateField: stateAbandoned, abandonedReasonField: abandonedNoOutcome, abandonedAtField: stuckInside},
	})
	got := stuckByID(t, q)
	want := map[string]dispatcher.Stuck{
		"queued":       {State: "queued", Reason: dispatcher.ConditionBlocked},
		"planned":      {State: "planned"},
		"long-queued":  {State: "queued"},
		"finished":     {State: "stopped", Reason: "agent-exit"},
		"outcome-only": {State: "stopped", Reason: "no-changes"},
		"stopped":      {State: "stopped", Reason: "waiting-on-owner"},
		"abandoned":    {State: "abandoned", Reason: abandonedNoOutcome},
	}
	for name, id := range ids {
		w, listed := want[name]
		s, found := got[id]
		if listed != found {
			t.Errorf("%s: listed = %v, want %v", name, found, listed)
			continue
		}
		if found && (s.State != w.State || s.Reason != w.Reason || s.Ticket != id) {
			t.Errorf("%s: got %+v, want %+v", name, s, w)
		}
	}
}

func TestStuckRunsSkipsAStoppedRunTheOwnerCancelled(t *testing.T) {
	q, client := queue(t)
	cancel := func(row map[string]any) map[string]any {
		row[cancelledByOwnerAtField], row[cancelledByField] = stuckInside, "owner@example.com"
		return row
	}
	finished := func() map[string]any {
		return map[string]any{stateField: stateClaimed, outcomeField: "agent-failed", stopReasonField: "agent-exit", settledAtField: stuckInside}
	}
	stopped := func() map[string]any {
		return map[string]any{stateField: stateStopped, stopReasonField: "waiting-on-owner", updatedAtField: stuckInside}
	}
	unset := finished()
	unset[cancelledByOwnerAtField] = time.Time{}
	ids := stuckRows(t, client, map[string]map[string]any{
		"finished":           finished(),
		"finished-cancelled": cancel(finished()),
		"finished-unset":     unset,
		"stopped":            stopped(),
		"stopped-cancelled":  cancel(stopped()),
	})
	got := stuckByID(t, q)
	for name, listed := range map[string]bool{
		"finished": true, "finished-cancelled": false, "finished-unset": true, "stopped": true, "stopped-cancelled": false,
	} {
		if _, found := got[ids[name]]; found != listed {
			t.Errorf("%s: listed = %v, want %v", name, found, listed)
		}
	}
}

func TestStuckRunsChangesNoRow(t *testing.T) {
	q, client := queue(t)
	ids := stuckRows(t, client, map[string]map[string]any{
		"queued": {stateField: stateQueued, queuedAtField: stuckInside},
	})
	ref := client.Collection(runsCollection).Doc(ids["queued"])
	before, err := ref.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stuckByID(t, q)
	after, err := ref.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdateTime.Equal(before.UpdateTime) {
		t.Fatalf("update time moved from %v to %v", before.UpdateTime, after.UpdateTime)
	}
}
