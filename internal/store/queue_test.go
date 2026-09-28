package store

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

var queueAt = time.Date(2026, 9, 27, 9, 15, 0, 0, time.UTC)

var queueSeq atomic.Uint64

// fresh suffixes an id, because the emulator outlives one run of the suite: a
// document an interrupted run left behind must not read as this run's own.
func fresh(id string) string {
	return id + "-" + strconv.FormatUint(queueSeq.Add(1), 10)
}

// queue returns a Queue over the emulator, with a cleanup for the documents
// forget() is given: one test's rows are its own, since the ids name the run.
func queue(t *testing.T) (*Queue, *firestore.Client) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "forge-wingman-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewQueue(client), client
}

// forget removes the run and refusal documents one test wrote, and its
// ledger reservation.
func forget(t *testing.T, client *firestore.Client, ids ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range ids {
			_, _ = client.Collection(runsCollection).Doc(id).Delete(ctx)
			_, _ = client.Collection(dispatchCollection).Doc(rejectedPrefix + id).Delete(ctx)
			_, _ = client.Collection(dispatchCollection).Doc(ledgerDocID).Update(ctx,
				[]firestore.Update{{FieldPath: firestore.FieldPath{"reservations", id}, Value: firestore.Delete}})
		}
	})
}

// queuedRun is a run the dispatcher would admit.
func queuedRun(id string, priority int) dispatcher.Queued {
	return dispatcher.Queued{
		RunID:    id,
		Ticket:   runner.Ticket{ID: id, Title: "feat(x): add a file", Size: "M", SizedBy: "linear-label", Body: "Add a file."},
		Repo:     "octo/scratch",
		Priority: priority,
		At:       queueAt,
	}
}

// generousBudget fits any candidate this suite claims, so a test not
// exercising the budget itself never has to think about it.
var generousBudget = dispatcher.BudgetConfig{
	ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 1000 * money.Dollar}},
	Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
	Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
}

func TestEnqueueWritesTheRunRecordQueued(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-enq"), 2)
	run.Private = true
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data[stateField] != stateQueued || data[priorityField] != int64(2) || data[repoField] != "octo/scratch" ||
		data[privateField] != true {
		t.Fatalf("queued row = %v", data)
	}
	var rec runner.Record
	if err := snap.DataTo(&rec); err != nil {
		t.Fatal(err)
	}
	if rec.Ticket() != run.Ticket || !rec.StartedAt.Equal(queueAt) || !rec.UpdatedAt.Equal(queueAt) || !rec.Private {
		t.Fatalf("record = %+v", rec)
	}
	// A poll that finds the same ticket delegated again must not start a second run.
	if err := q.Enqueue(ctx, run); !errors.Is(err, dispatcher.ErrAlreadyQueued) {
		t.Fatalf("err = %v, want ErrAlreadyQueued", err)
	}
}

func TestRejectRecordsTheRefusalAndEnqueueClearsIt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rej"), 1)
	forget(t, client, run.RunID)
	r := dispatcher.Rejection{Ticket: run.RunID, Reason: dispatcher.RefusalNoSize,
		Detail: "no size: label", At: queueAt}
	if err := q.Reject(ctx, r); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection("dispatch").Doc("rejected-" + run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()["reason"] != string(r.Reason) || snap.Data()["detail"] != r.Detail ||
		snap.Data()["at"] != queueAt {
		t.Fatalf("refusal = %v", snap.Data())
	}
	if _, err := client.Collection("dispatch").Doc("rejected-" + fresh("never-refused")).Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want no refusal to be found", err)
	}
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection("dispatch").Doc("rejected-" + run.RunID).Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("a refusal outlived the run that superseded it: %v", err)
	}
}

func TestCandidatesListsQueuedRunsInPriorityOrder(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	// The suffixes sort after the names, so the ids cannot stand in for the
	// order: only linear_priority can.
	var ids []string
	priorities := map[string]int{}
	for _, queued := range [][2]int{{1, 1}, {3, 2}, {5, 3}} {
		id := fresh(strconv.Itoa(queued[1]))
		ids = append(ids, id)
		priorities[id] = queued[0]
	}
	forget(t, client, ids...)
	for id, priority := range priorities {
		run := queuedRun(id, priority)
		run.Ticket.Size = "S"
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]dispatcher.Candidate{}
	var order []string
	for _, c := range cands {
		if _, ours := priorities[c.RunID]; !ours {
			continue // another test's row, from a shared collection
		}
		byID[c.RunID] = c
		order = append(order, c.RunID)
	}
	want := slices.Sorted(maps.Keys(priorities))
	if !slices.Equal(order, want) {
		t.Fatalf("candidate order = %v, want %v", order, want)
	}
	for id := range priorities {
		if byID[id].Repo != "octo/scratch" || byID[id].Size != "S" || byID[id].Priority == 0 {
			t.Fatalf("candidate %s = %+v", id, byID[id])
		}
	}
}

// TestCandidatesDefaultsAMissingPrivateFieldToTrue is the regression test for
// a rolling-deploy hazard: a run record written before this field existed has
// no privateField at all. Defaulting that to false would silently zero a
// private target's runner minutes against NFR-1's ceiling during the
// transition; defaulting to true is the safe direction instead.
func TestCandidatesDefaultsAMissingPrivateFieldToTrue(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("pre-migration")
	forget(t, client, id)
	if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, map[string]any{
		stateField:    stateQueued,
		priorityField: int64(1),
		repoField:     "octo/scratch",
		sizeField:     "S",
		// privateField deliberately omitted.
	}); err != nil {
		t.Fatal(err)
	}
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range cands {
		if c.RunID != id {
			continue
		}
		found = true
		if !c.Private {
			t.Fatalf("candidate %+v, want Private true for a record missing the field", c)
		}
	}
	if !found {
		t.Fatalf("candidate %s not listed among %+v", id, cands)
	}
}

func TestTryClaimAdmitsARunThatFitsAndBooksItsReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-claim"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{ProviderCost: 3 * money.Dollar, RunnerMinutes: 12}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, res)
	if err != nil || !ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q, err = %v", ok, binding, err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != stateClaimed {
		t.Fatalf("%s is %v after being claimed", run.RunID, snap.Data()[stateField])
	}
	ledgerSnap, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ledger ledgerDoc
	if err := ledgerSnap.DataTo(&ledger); err != nil {
		t.Fatal(err)
	}
	entry, ok := ledger.Reservations[run.RunID]
	if !ok || entry.ProviderCostMicros != res.ProviderCost || entry.RunnerMinutes != res.RunnerMinutes {
		t.Fatalf("reservation = %+v, ok %v", entry, ok)
	}
}

func TestTryClaimDefersARunTheProviderWindowWouldBreach(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-defer"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 2 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, tight, dispatcher.Reservation{ProviderCost: 3 * money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "5h" {
		t.Fatalf("ok = %v, binding = %q, want deferred on 5h", ok, binding)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != stateQueued {
		t.Fatalf("a deferred run's state = %v, want it to stay queued", snap.Data()[stateField])
	}
}

func TestTryClaimCountsAnotherRunsInFlightReservationTowardTheCeiling(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	first := queuedRun(fresh("queue-inflight-a"), 1)
	second := queuedRun(fresh("queue-inflight-b"), 2)
	forget(t, client, first.RunID, second.RunID)
	for _, r := range []dispatcher.Queued{first, second} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 5 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	// Claiming the first run reserves $4 of the $5 window.
	ok, _, err := q.TryClaim(ctx, first.RunID, queueAt, tight, dispatcher.Reservation{ProviderCost: 4 * money.Dollar})
	if err != nil || !ok {
		t.Fatalf("first claim: ok = %v, err = %v", ok, err)
	}
	// Nothing has settled yet, but the first run's reservation alone leaves no
	// room for a second $4 estimate.
	ok, binding, err := q.TryClaim(ctx, second.RunID, queueAt, tight, dispatcher.Reservation{ProviderCost: 4 * money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "5h" {
		t.Fatalf("ok = %v, binding = %q, want the in-flight reservation to defer this claim", ok, binding)
	}
}

func TestTwoTryClaimsClaimOneRunBetweenThem(t *testing.T) {
	q, client := queue(t)
	run := queuedRun(fresh("queue-race"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		won   int
		fails []error
	)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := q.TryClaim(context.Background(), run.RunID, queueAt, generousBudget,
				dispatcher.Reservation{ProviderCost: money.Dollar})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				fails = append(fails, err)
			case ok:
				won++
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d claims won, want exactly one", won)
	}
	if len(fails) > 0 {
		t.Fatalf("a claim failed rather than reporting the run was taken: %v", fails)
	}
}

func TestReleaseReturnsAClaimedRunToTheQueueAndDropsItsReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rel"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, dispatcher.Reservation{ProviderCost: money.Dollar}); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if err := q.Release(ctx, run.RunID, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != stateQueued {
		t.Fatalf("released run is %v", snap.Data()[stateField])
	}
	if _, ok := snap.Data()[claimedAtField]; ok {
		t.Fatalf("released run still holds %s", claimedAtField)
	}
	ledgerSnap, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ledger ledgerDoc
	if err := ledgerSnap.DataTo(&ledger); err != nil {
		t.Fatal(err)
	}
	if _, ok := ledger.Reservations[run.RunID]; ok {
		t.Fatalf("a released run's reservation was not dropped: %+v", ledger.Reservations[run.RunID])
	}
	// The released run is claimable again, with the ledger no longer double-
	// counting its reservation.
	ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil || !ok {
		t.Fatalf("the released run was not claimed again: %v, %v", ok, err)
	}
}

func TestSettleDropsTheReservationAndIsIdempotent(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-settle"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, dispatcher.Reservation{ProviderCost: 2 * money.Dollar}); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if err := q.Settle(ctx, run.RunID); err != nil {
		t.Fatal(err)
	}
	ledgerSnap, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ledger ledgerDoc
	if err := ledgerSnap.DataTo(&ledger); err != nil {
		t.Fatal(err)
	}
	if _, ok := ledger.Reservations[run.RunID]; ok {
		t.Fatalf("a settled run's reservation was not dropped: %+v", ledger.Reservations[run.RunID])
	}
	// Settling a second time (a rerun record job) must not error.
	if err := q.Settle(ctx, run.RunID); err != nil {
		t.Fatalf("settling an already-settled run: %v", err)
	}
}

func TestTryClaimCountsSettledCostWithinTheWindow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	settled := queuedRun(fresh("queue-settled"), 1)
	candidate := queuedRun(fresh("queue-after-settled"), 2)
	forget(t, client, settled.RunID, candidate.RunID)
	for _, r := range []dispatcher.Queued{settled, candidate} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Settle a run's cost directly, as runner.Finalize would.
	if _, err := client.Collection(runsCollection).Doc(settled.RunID).Set(ctx, map[string]any{
		settledAtField:           queueAt,
		settledProviderCostField: int64(4 * money.Dollar),
	}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}

	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 5 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	ok, binding, err := q.TryClaim(ctx, candidate.RunID, queueAt.Add(time.Minute), tight,
		dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "5h" {
		t.Fatalf("ok = %v, binding = %q, want the $4 already settled in this window to defer a further $2", ok, binding)
	}

	// The same settled cost falls outside a window that starts after it.
	ok, binding, err = q.TryClaim(ctx, candidate.RunID, queueAt.Add(6*time.Hour), tight,
		dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
	if err != nil || !ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q, want it to fit once the settled run has rolled out of the window", ok, binding)
	}
}
