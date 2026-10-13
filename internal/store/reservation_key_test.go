package store

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// claimOn enqueues run id, puts its row on attempt and claims it for stage at
// sweepClaimedAt, so the reservation is booked by TryClaim itself.
func claimOn(t *testing.T, q *Queue, client *firestore.Client, id, stage string, attempt int) {
	t.Helper()
	ctx := context.Background()
	forget(t, client, id)
	if err := q.Enqueue(ctx, queuedRun(id, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{{Path: attemptField, Value: attempt}}); err != nil {
		t.Fatal(err)
	}
	reclaim(t, q, id, stage)
}

// reclaim claims run id for stage at sweepClaimedAt.
func reclaim(t *testing.T, q *Queue, id, stage string) {
	t.Helper()
	res := dispatcher.Reservation{ProviderCost: money.Dollar, RunnerMinutes: 3, Stage: stage}
	if ok, _, err := q.TryClaim(context.Background(), id, sweepClaimedAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
}

// heldKeys is the sorted set of keys the ledger holds a reservation under.
func heldKeys(t *testing.T, client *firestore.Client) []string {
	t.Helper()
	return slices.Sorted(maps.Keys(readLedgerDoc(t, client).Reservations))
}

// markStopped puts a claimed run's row in a state a requeue accepts.
func markStopped(t *testing.T, client *firestore.Client, id string) {
	t.Helper()
	if _, err := client.Collection(runsCollection).Doc(id).Update(context.Background(), []firestore.Update{{Path: stateField, Value: stateStopped}}); err != nil {
		t.Fatal(err)
	}
}

func TestTryClaimBooksUnderTheRowsAttemptKeyAndRecordsTheAttempt(t *testing.T) {
	q, client := queue(t)
	resetLedger(t, client, 2)
	id := fresh("key-claim")
	claimOn(t, q, client, id, dispatcher.StagePlan, 2)
	entry, ok := readLedgerDoc(t, client).Reservations[reservationKey(id, 2, dispatcher.StagePlan)]
	if !ok || entry.Attempt != 2 || entry.Stage != dispatcher.StagePlan {
		t.Fatalf("entry = %+v (held %v), want a plan reservation for attempt 2", entry, ok)
	}
	if got := heldKeys(t, client); len(got) != 1 {
		t.Fatalf("held = %v, want the one attempt key", got)
	}
}

// A run swept while still alive, requeued and claimed again has a late Settle
// from its first attempt arrive after the second claimed: it must leave the
// second attempt's reservation and N alone.
func TestALateSettleOfAnEarlierAttemptLeavesTheNewAttemptsReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	resetLedger(t, client, 2)
	id := fresh("key-late")
	claimOn(t, q, client, id, dispatcher.StageBuild, 0)
	if !swept(t, q, id, sweepClaimedAt.Add(3*time.Hour)) {
		t.Fatal("the run was not abandoned")
	}
	if res, err := q.Requeue(ctx, id, queueAt); err != nil || res.Attempt != 1 {
		t.Fatalf("requeue = %+v, %v, want attempt 1", res, err)
	}
	reclaim(t, q, id, dispatcher.StageBuild)
	before := readLedgerDoc(t, client)

	if err := q.Settle(ctx, id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	after := readLedgerDoc(t, client)
	if _, ok := after.Reservations[reservationKey(id, 1, dispatcher.StageBuild)]; !ok {
		t.Fatal("the earlier attempt's settle deleted the new attempt's reservation")
	}
	if after.N != before.N || after.StableRuns != before.StableRuns {
		t.Fatalf("N %d stable %d, want %d and %d unchanged", after.N, after.StableRuns, before.N, before.StableRuns)
	}

	if err := q.Settle(ctx, id, attemptOf(1)); err != nil {
		t.Fatal(err)
	}
	if got := heldKeys(t, client); len(got) != 0 {
		t.Fatalf("held = %v, want the new attempt's own settle to drop it", got)
	}
}

// Every operation that books, reads or drops a reservation reaches it through
// the one key builder: each removes the claim's own key and nothing beside it.
func TestEveryReservationOperationUsesTheClaimsAttemptKey(t *testing.T) {
	ctx := context.Background()
	const attempt = 1
	for _, tt := range []struct {
		name  string
		stage string
		prep  func(t *testing.T, client *firestore.Client, id string)
		op    func(q *Queue, id string) error
	}{
		{name: "Release build", stage: dispatcher.StageBuild,
			op: func(q *Queue, id string) error { return q.Release(ctx, id, queueAt) }},
		{name: "Release plan", stage: dispatcher.StagePlan,
			op: func(q *Queue, id string) error { return q.Release(ctx, id, queueAt) }},
		{name: "Stop", stage: dispatcher.StagePlan,
			op: func(q *Queue, id string) error { return q.Stop(ctx, id, runner.StopPlanStageFailed, "x", queueAt) }},
		{name: "Suspend", stage: dispatcher.StagePlan,
			op: func(q *Queue, id string) error {
				return q.Suspend(ctx, id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt)
			}},
		{name: "Requeue", stage: dispatcher.StageBuild, prep: markStopped,
			op: func(q *Queue, id string) error { _, err := q.Requeue(ctx, id, queueAt); return err }},
		{name: "RequeueAfterConflict", stage: dispatcher.StageBuild, prep: markStopped,
			op: func(q *Queue, id string) error { _, err := q.RequeueAfterConflict(ctx, id, queueAt, 5); return err }},
		{name: "sweep build", stage: dispatcher.StageBuild,
			op: func(q *Queue, id string) error {
				_, err := q.SweepAbandoned(ctx, sweepClaimedAt.Add(3*time.Hour), sweepWindows)
				return err
			}},
		{name: "sweep plan", stage: dispatcher.StagePlan,
			op: func(q *Queue, id string) error {
				_, err := q.SweepAbandoned(ctx, sweepClaimedAt.Add(90*time.Minute), sweepWindows)
				return err
			}},
		{name: "Settle", stage: dispatcher.StageBuild,
			op: func(q *Queue, id string) error { return q.Settle(ctx, id, attemptOf(attempt)) }},
		{name: "SettlePlan", stage: dispatcher.StagePlan,
			op: func(q *Queue, id string) error { return q.SettlePlan(ctx, id, attemptOf(attempt)) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			q, client := queue(t)
			resetLedger(t, client, 3)
			id := fresh("key-op")
			claimOn(t, q, client, id, tt.stage, attempt)
			own := reservationKey(id, attempt, tt.stage)
			if !slices.Contains(heldKeys(t, client), own) {
				t.Fatalf("TryClaim did not book %s: held %v", own, heldKeys(t, client))
			}
			beside := []string{
				reservationKey(id, attempt-1, tt.stage),
				reservationKey(id, attempt+1, tt.stage),
				legacyReservationKey(id, tt.stage),
			}
			// A stage unlike the claim's, so a lookup that lands on one of these
			// reads the wrong abandon window or the wrong settle.
			otherStage := dispatcher.StageBuild
			if tt.stage == dispatcher.StageBuild {
				otherStage = dispatcher.StagePlan
			}
			updates := make([]firestore.Update, 0, len(beside))
			for _, key := range beside {
				updates = append(updates, firestore.Update{
					FieldPath: firestore.FieldPath{"reservations", key},
					Value:     reservationEntry{Stage: otherStage, ClaimedAt: sweepClaimedAt},
				})
			}
			if _, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Update(ctx, updates); err != nil {
				t.Fatal(err)
			}
			if tt.prep != nil {
				tt.prep(t, client, id)
			}
			if err := tt.op(q, id); err != nil {
				t.Fatal(err)
			}
			if want := slices.Sorted(slices.Values(beside)); !slices.Equal(heldKeys(t, client), want) {
				t.Fatalf("held = %v, want only the keys beside the claim's own: %v", heldKeys(t, client), want)
			}
		})
	}
}

// A claim made before attempts were part of the key holds a plain run-id key;
// it still settles, releases and drops, at any attempt, until it drains.
func TestAPreAttemptReservationStillSettlesReleasesAndDrops(t *testing.T) {
	ctx := context.Background()
	for _, attempt := range []int{0, 2} {
		for _, tt := range []struct {
			name  string
			stage string
			state string
			op    func(q *Queue, id string) error
		}{
			{"Settle", dispatcher.StageBuild, stateClaimed, func(q *Queue, id string) error { return q.Settle(ctx, id, nil) }},
			{"SettlePlan", dispatcher.StagePlan, stateClaimed, func(q *Queue, id string) error { return q.SettlePlan(ctx, id, nil) }},
			{"Release", dispatcher.StageBuild, stateClaimed, func(q *Queue, id string) error { return q.Release(ctx, id, queueAt) }},
			{"Release plan", dispatcher.StagePlan, stateClaimed, func(q *Queue, id string) error { return q.Release(ctx, id, queueAt) }},
			{"Requeue", dispatcher.StageBuild, stateStopped, func(q *Queue, id string) error { _, err := q.Requeue(ctx, id, queueAt); return err }},
			{"sweep", dispatcher.StageBuild, stateClaimed, func(q *Queue, id string) error {
				_, err := q.SweepAbandoned(ctx, sweepClaimedAt.Add(3*time.Hour), sweepWindows)
				return err
			}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				q, client := queue(t)
				id := fresh("key-legacy")
				seedRow(t, client, id, map[string]any{
					stateField: tt.state, claimedAtField: sweepClaimedAt, attemptField: int64(attempt),
				})
				legacy := legacyReservationKey(id, tt.stage)
				seedLedger(t, client, 2, map[string]reservationEntry{legacy: {Stage: tt.stage, ClaimedAt: sweepClaimedAt}})
				if err := tt.op(q, id); err != nil {
					t.Fatal(err)
				}
				if got := heldKeys(t, client); len(got) != 0 {
					t.Fatalf("attempt %d: held = %v, want the pre-attempt reservation dropped", attempt, got)
				}
			})
		}
	}
}

// Once the attempt holds a key of its own, the pre-attempt key is never read:
// a row-side drop leaves it, and an attempt's settle never reaches it.
func TestAPreAttemptReservationIsNeverMatchedBesideAnAttemptKey(t *testing.T) {
	ctx := context.Background()
	const attempt = 2
	for name, op := range map[string]func(q *Queue, id string) error{
		"Release": func(q *Queue, id string) error { return q.Release(ctx, id, queueAt) },
		"Requeue": func(q *Queue, id string) error { _, err := q.Requeue(ctx, id, queueAt); return err },
		"Settle":  func(q *Queue, id string) error { return q.Settle(ctx, id, attemptOf(attempt)) },
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			id := fresh("key-both")
			seedRow(t, client, id, map[string]any{
				stateField: stateStopped, claimedAtField: sweepClaimedAt, attemptField: int64(attempt),
			})
			own, legacy := reservationKey(id, attempt, dispatcher.StageBuild), legacyReservationKey(id, dispatcher.StageBuild)
			seedLedger(t, client, 2, map[string]reservationEntry{
				own:    {Stage: dispatcher.StageBuild, ClaimedAt: sweepClaimedAt},
				legacy: {Stage: dispatcher.StageBuild, ClaimedAt: sweepClaimedAt},
			})
			if err := op(q, id); err != nil {
				t.Fatal(err)
			}
			if got := heldKeys(t, client); !slices.Equal(got, []string{legacy}) {
				t.Fatalf("held = %v, want only the pre-attempt key %s left", got, legacy)
			}
		})
	}
}

func TestTheSweepAndTheCandidateListCarryTheRowsAttempt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	resetLedger(t, client, 2)
	id := fresh("key-carry")
	claimOn(t, q, client, id, dispatcher.StageBuild, 2)
	got, err := q.SweepAbandoned(ctx, sweepClaimedAt.Add(3*time.Hour), sweepWindows)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(got, func(a dispatcher.Abandoned) bool { return a.RunID == id }); i < 0 || got[i].Attempt != 2 {
		t.Fatalf("abandoned = %+v, want run %s at attempt 2", got, id)
	}
	if _, err := q.Requeue(ctx, id, queueAt); err != nil {
		t.Fatal(err)
	}
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(cands, func(c dispatcher.Candidate) bool { return c.RunID == id }); i < 0 || cands[i].Attempt != 3 {
		t.Fatalf("candidates = %+v, want run %s at attempt 3", cands, id)
	}
}
