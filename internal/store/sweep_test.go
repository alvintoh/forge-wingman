package store

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// sweepClaimedAt predates every other suite's rows, so a sweep run with a
// window measured from it reaches only the rows this file claimed.
var sweepClaimedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

var sweepWindows = dispatcher.AbandonWindows{Plan: time.Hour, Build: 2 * time.Hour}

// claimAt enqueues run runID and claims it at sweepClaimedAt for stage.
func claimAt(t *testing.T, q *Queue, client *firestore.Client, runID, stage string) {
	t.Helper()
	ctx := context.Background()
	forget(t, client, runID)
	if err := q.Enqueue(ctx, queuedRun(runID, 1)); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar, RunnerMinutes: 3, Stage: stage}
	if ok, _, err := q.TryClaim(ctx, runID, sweepClaimedAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
}

// swept reports whether the sweep at now flipped runID.
func swept(t *testing.T, q *Queue, runID string, now time.Time) bool {
	t.Helper()
	got, err := q.SweepAbandoned(context.Background(), now, sweepWindows)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(got, func(a dispatcher.Abandoned) bool { return a.RunID == runID })
}

func TestSweepAbandonsAClaimOlderThanItsWindowAndDropsItsReservation(t *testing.T) {
	q, client := queue(t)
	id := fresh("sweep-old")
	resetLedger(t, client, 3)
	claimAt(t, q, client, id, dispatcher.StageBuild)
	got, err := q.SweepAbandoned(context.Background(), sweepClaimedAt.Add(3*time.Hour), sweepWindows)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(got, func(a dispatcher.Abandoned) bool { return a.RunID == id }); i < 0 ||
		got[i].Repo != "octo/scratch" || got[i].Ticket != id || got[i].Stage != dispatcher.StageBuild || got[i].Attempt != 0 {
		t.Fatalf("abandoned = %+v, want run %s with its repo, stage and attempt", got, id)
	}
	if st := readRow(t, client, id)[stateField]; st != stateAbandoned {
		t.Fatalf("state = %v, want %s", st, stateAbandoned)
	}
	ledger := readLedgerDoc(t, client)
	if _, ok := ledger.Reservations[id]; ok {
		t.Fatal("the abandoned run's reservation was not dropped")
	}
	if ledger.N != 3 {
		t.Fatalf("N = %d, want the sweep to leave the limit alone", ledger.N)
	}
	if swept(t, q, id, sweepClaimedAt.Add(4*time.Hour)) {
		t.Fatal("a second sweep reported the run again")
	}
}

func TestSweepLeavesAClaimInsideItsWindow(t *testing.T) {
	q, client := queue(t)
	id := fresh("sweep-inside")
	resetLedger(t, client, 1)
	claimAt(t, q, client, id, dispatcher.StageBuild)
	if swept(t, q, id, sweepClaimedAt.Add(sweepWindows.Build)) {
		t.Fatal("the sweep abandoned a claim exactly as old as its window")
	}
	if st := readRow(t, client, id)[stateField]; st != stateClaimed {
		t.Fatalf("state = %v, want %s", st, stateClaimed)
	}
	if _, ok := readLedgerDoc(t, client).Reservations[id]; !ok {
		t.Fatal("the reservation of a claim inside its window was dropped")
	}
}

func TestSweepUsesTheShorterWindowForAPlanStageClaimAndDropsItsPlanKey(t *testing.T) {
	q, client := queue(t)
	plan, build := fresh("sweep-plan"), fresh("sweep-build")
	resetLedger(t, client, 2)
	claimAt(t, q, client, plan, dispatcher.StagePlan)
	claimAt(t, q, client, build, dispatcher.StageBuild)
	got, err := q.SweepAbandoned(context.Background(), sweepClaimedAt.Add(90*time.Minute), sweepWindows)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got, func(a dispatcher.Abandoned) bool { return a.RunID == plan && a.Stage == dispatcher.StagePlan }) ||
		slices.ContainsFunc(got, func(a dispatcher.Abandoned) bool { return a.RunID == build }) {
		t.Fatalf("abandoned = %+v, want only the plan-stage run", got)
	}
	res := readLedgerDoc(t, client).Reservations
	if _, ok := res[PlanKey(plan)]; ok {
		t.Fatal("the plan-stage reservation was not dropped")
	}
	if _, ok := res[build]; !ok {
		t.Fatal("the build-stage reservation was dropped")
	}
}

func TestSweepLeavesAClaimThatRecordedAnOutcome(t *testing.T) {
	q, client := queue(t)
	for name, field := range map[string]string{"an outcome": outcomeField, "a stop reason": stopReasonField} {
		t.Run(name, func(t *testing.T) {
			id := fresh("sweep-outcome")
			seedRow(t, client, id, map[string]any{stateField: stateClaimed, claimedAtField: sweepClaimedAt, field: "x"})
			if swept(t, q, id, sweepClaimedAt.Add(24*time.Hour)) {
				t.Fatalf("the sweep abandoned a claim holding %s", name)
			}
			if st := readRow(t, client, id)[stateField]; st != stateClaimed {
				t.Fatalf("state = %v, want %s", st, stateClaimed)
			}
		})
	}
}

func TestSweepLeavesAClaimWithNoClaimTime(t *testing.T) {
	q, client := queue(t)
	id := fresh("sweep-untimed")
	seedRow(t, client, id, map[string]any{stateField: stateClaimed})
	if swept(t, q, id, sweepClaimedAt.Add(24*time.Hour)) {
		t.Fatal("the sweep abandoned a claim that carries no claim time")
	}
}

// The listing and the transaction read the row apart, so a run released in
// between must not be abandoned.
func TestAbandonLeavesARunThatIsNoLongerClaimed(t *testing.T) {
	q, client := queue(t)
	id := fresh("sweep-released")
	seedRow(t, client, id, map[string]any{stateField: stateQueued, claimedAtField: sweepClaimedAt})
	_, ok, err := q.abandon(context.Background(), id, sweepClaimedAt.Add(24*time.Hour), sweepWindows)
	if err != nil || ok {
		t.Fatalf("abandon = %v, %v, want a queued run left alone", ok, err)
	}
	if st := readRow(t, client, id)[stateField]; st != stateQueued {
		t.Fatalf("state = %v, want %s", st, stateQueued)
	}
}

// A record job and the sweep may reach one run together: whichever commits
// first, the run ends either abandoned or claimed with its outcome, never
// abandoned with its reservation left or an error from either side.
func TestSweepRacingTheRecordOfTheSameRunLosesNothing(t *testing.T) {
	q, client := queue(t)
	records := NewRecords(client)
	for range 8 {
		id := fresh("sweep-race")
		resetLedger(t, client, 1)
		claimAt(t, q, client, id, dispatcher.StageBuild)
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := q.SweepAbandoned(context.Background(), sweepClaimedAt.Add(3*time.Hour), sweepWindows)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			err := records.PutRecord(context.Background(), id, runner.Record{Outcome: runner.OutcomeAgentFailed, SettledAt: queueAt})
			errs <- errors.Join(err, q.Settle(context.Background(), id))
		}()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		row := readRow(t, client, id)
		if st := row[stateField]; st != stateAbandoned && st != stateClaimed {
			t.Fatalf("state = %v", st)
		}
		if row[outcomeField] != string(runner.OutcomeAgentFailed) {
			t.Fatalf("outcome = %v, want the record kept", row[outcomeField])
		}
		if _, ok := readLedgerDoc(t, client).Reservations[id]; ok {
			t.Fatal("the reservation outlived both the sweep and the settle")
		}
	}
}

func TestALateRecordAndSettleOnAnAbandonedRunStampItWithoutRevivingIt(t *testing.T) {
	q, client := queue(t)
	id := fresh("sweep-late")
	resetLedger(t, client, 2)
	claimAt(t, q, client, id, dispatcher.StageBuild)
	if !swept(t, q, id, sweepClaimedAt.Add(3*time.Hour)) {
		t.Fatal("the run was not abandoned")
	}
	if err := NewRecords(client).PutRecord(context.Background(), id, runner.Record{Outcome: runner.OutcomeAgentFailed, SettledAt: queueAt}); err != nil {
		t.Fatal(err)
	}
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	row := readRow(t, client, id)
	if row[stateField] != stateAbandoned {
		t.Fatalf("state = %v, want a late record to leave it %s", row[stateField], stateAbandoned)
	}
	if at, _ := row[settledAtField].(time.Time); !at.Equal(queueAt) {
		t.Fatalf("settled_at = %v, want the late record stamped", row[settledAtField])
	}
	if got := readLedgerDoc(t, client).N; got != 2 {
		t.Fatalf("N = %d, want a late settle to leave it alone", got)
	}
	if swept(t, q, id, sweepClaimedAt.Add(5*time.Hour)) {
		t.Fatal("the sweep reported an already abandoned run")
	}
}

func TestRequeueReturnsAnAbandonedRunToTheBackOfTheQueue(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	abandoned, newer := fresh("sweep-requeued"), fresh("sweep-newer")
	seedRow(t, client, abandoned, map[string]any{stateField: stateAbandoned, priorityField: int64(3), queuedAtField: queueAt})
	forget(t, client, newer)
	run := queuedRun(newer, 3)
	run.At = queueAt.Add(time.Minute)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := q.Requeue(ctx, abandoned, queueAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != stateQueued || got.Attempt != 1 {
		t.Fatalf("result = %+v, want %s as attempt 1", got, stateQueued)
	}
	if order, want := ownOrder(t, q, abandoned, newer), []string{newer, abandoned}; !slices.Equal(order, want) {
		t.Fatalf("candidate order = %v, want %v", order, want)
	}
}
