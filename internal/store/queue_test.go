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

// openFacts are the facts outside the ledger under which nothing but the
// ledger and the budget can withhold a run.
var openFacts = dispatcher.Facts{Limits: dispatcher.DefaultLimits, OpenPRsKnown: true}

// resetLedger replaces the shared ledger with an empty one whose N is n, so a
// test's admission starts from a known state, and drops it afterwards.
func resetLedger(t *testing.T, client *firestore.Client, n int) {
	t.Helper()
	ref := client.Collection(dispatchCollection).Doc(ledgerDocID)
	if _, err := ref.Set(context.Background(), ledgerDoc{Reservations: map[string]reservationEntry{}, N: n}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = ref.Delete(context.Background()) })
}

// readLedgerDoc reads the shared ledger as the store keeps it.
func readLedgerDoc(t *testing.T, client *firestore.Client) ledgerDoc {
	t.Helper()
	snap, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ledger ledgerDoc
	if err := snap.DataTo(&ledger); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestEnqueueStoresTheBlockingRelationsOnTheRow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-enq-relations"), 2)
	run.BlockedBy, run.Blocks = []string{"run-p"}, []string{"run-x"}
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := subject(snap.Data())
	if !slices.Equal(got.BlockedBy, []string{"run-p"}) || !slices.Equal(got.Blocks, []string{"run-x"}) {
		t.Fatalf("subject = %+v, want the relations read back as enqueued", got)
	}
	if _, has := snap.Data()[blocksField]; !has {
		t.Fatalf("queued row = %v, want a blocks column", snap.Data())
	}
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
	for _, field := range []string{blockedByField, blocksField} {
		if _, has := data[field]; has {
			t.Fatalf("queued row = %v, want no %s column for a ticket with none", data, field)
		}
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
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{ProviderCost: 3 * money.Dollar, RunnerMinutes: 12}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res)
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
	if entry.TicketID != run.Ticket.ID || entry.Repo != run.Repo || entry.Size != run.Ticket.Size || !entry.ClaimedAt.Equal(queueAt) {
		t.Fatalf("reservation = %+v, want the ticket, repository, size and claim time booked", entry)
	}
	if snap.Data()[claimConcurrencyField] != int64(1) {
		t.Fatalf("claim concurrency = %v, want 1 for a run claimed alone", snap.Data()[claimConcurrencyField])
	}
}

func TestTryClaimDefersARunTheProviderWindowWouldBreach(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-defer"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 2 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, tight, openFacts, dispatcher.Reservation{ProviderCost: 3 * money.Dollar})
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
	if snap.Data()[waitingOnField] != "5h" {
		t.Fatalf("waiting on %v, want the binding condition recorded on the row", snap.Data()[waitingOnField])
	}
}

func TestTryClaimCountsAnotherRunsInFlightReservationTowardTheCeiling(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	first := queuedRun(fresh("queue-inflight-a"), 1)
	second := queuedRun(fresh("queue-inflight-b"), 2)
	first.Repo, second.Repo = "octo/first", "octo/second"
	forget(t, client, first.RunID, second.RunID)
	resetLedger(t, client, 2)
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
	ok, _, err := q.TryClaim(ctx, first.RunID, queueAt, tight, openFacts, dispatcher.Reservation{ProviderCost: 4 * money.Dollar})
	if err != nil || !ok {
		t.Fatalf("first claim: ok = %v, err = %v", ok, err)
	}
	// Nothing has settled yet, but the first run's reservation alone leaves no
	// room for a second $4 estimate.
	ok, binding, err := q.TryClaim(ctx, second.RunID, queueAt, tight, openFacts, dispatcher.Reservation{ProviderCost: 4 * money.Dollar})
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
	resetLedger(t, client, 1)
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
			ok, _, err := q.TryClaim(context.Background(), run.RunID, queueAt, generousBudget, openFacts,
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
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{ProviderCost: money.Dollar}); err != nil || !ok {
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
	ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil || !ok {
		t.Fatalf("the released run was not claimed again: %v, %v", ok, err)
	}
}

func TestSettleDropsTheReservationAndIsIdempotent(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-settle"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{ProviderCost: 2 * money.Dollar}); err != nil || !ok {
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
	if got := readLedgerDoc(t, client).N; got != 1 {
		t.Fatalf("N = %d, want an unsettled run to leave it alone", got)
	}
}

func TestTryClaimCountsSettledCostWithinTheWindow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	settled := queuedRun(fresh("queue-settled"), 1)
	candidate := queuedRun(fresh("queue-after-settled"), 2)
	forget(t, client, settled.RunID, candidate.RunID)
	resetLedger(t, client, 1)
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
	ok, binding, err := q.TryClaim(ctx, candidate.RunID, queueAt.Add(time.Minute), tight, openFacts,
		dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "5h" {
		t.Fatalf("ok = %v, binding = %q, want the $4 already settled in this window to defer a further $2", ok, binding)
	}

	// The same settled cost falls outside a window that starts after it.
	ok, binding, err = q.TryClaim(ctx, candidate.RunID, queueAt.Add(6*time.Hour), tight, openFacts,
		dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
	if err != nil || !ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q, want it to fit once the settled run has rolled out of the window", ok, binding)
	}
}

func TestTryClaimAdmitsOneRunPerRepositoryAndRecordsWhyTheOtherWaits(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	first := queuedRun(fresh("queue-repo-a"), 1)
	second := queuedRun(fresh("queue-repo-b"), 2)
	forget(t, client, first.RunID, second.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{first, second} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	if ok, _, err := q.TryClaim(ctx, first.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("first claim: ok %v, err %v", ok, err)
	}
	ok, binding, err := q.TryClaim(ctx, second.RunID, queueAt, generousBudget, openFacts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionRepoBusy {
		t.Fatalf("ok = %v, binding = %q, want the repository held by the first run", ok, binding)
	}
	snap, err := client.Collection(runsCollection).Doc(second.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[waitingOnField] != dispatcher.ConditionRepoBusy {
		t.Fatalf("waiting on %v, want the binding condition on the row", snap.Data()[waitingOnField])
	}
	if err := q.Settle(ctx, first.RunID); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, second.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("second claim after the first settled: ok %v, err %v", ok, err)
	}
	snap, err = client.Collection(runsCollection).Doc(second.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, waiting := snap.Data()[waitingOnField]; waiting {
		t.Fatalf("a claimed run still records %v as what it waits on", snap.Data()[waitingOnField])
	}
}

func TestTryClaimHoldsARunBlockedByOneInFlight(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	running := queuedRun(fresh("queue-blocker"), 1)
	blocked := queuedRun(fresh("queue-blocked"), 2)
	running.Repo, blocked.Repo = "octo/running", "octo/blocked"
	blocked.BlockedBy = []string{running.Ticket.ID}
	forget(t, client, running.RunID, blocked.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{running, blocked} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	if ok, _, err := q.TryClaim(ctx, running.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	ok, binding, err := q.TryClaim(ctx, blocked.RunID, queueAt, generousBudget, openFacts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionBlocked {
		t.Fatalf("ok = %v, binding = %q, want the blocking relation to hold the run", ok, binding)
	}
}

func TestTryClaimCapsTheRunsOfTheLargeSizeInFlight(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	first, second := queuedRun(fresh("queue-large-a"), 1), queuedRun(fresh("queue-large-b"), 2)
	first.Repo, second.Repo = "octo/large-a", "octo/large-b"
	first.Ticket.Size, second.Ticket.Size = "L", "L"
	forget(t, client, first.RunID, second.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{first, second} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	if ok, _, err := q.TryClaim(ctx, first.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("first claim: ok %v, err %v", ok, err)
	}
	ok, binding, err := q.TryClaim(ctx, second.RunID, queueAt, generousBudget, openFacts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionLargeCap {
		t.Fatalf("ok = %v, binding = %q, want the large run held by the one already in flight", ok, binding)
	}
}

func TestRacingClaimsForOneRepositoryAdmitOne(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	runs := []dispatcher.Queued{queuedRun(fresh("queue-same-repo-a"), 1), queuedRun(fresh("queue-same-repo-b"), 2)}
	forget(t, client, runs[0].RunID, runs[1].RunID)
	resetLedger(t, client, 5)
	for _, r := range runs {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	won, fails := raceClaims(q, runs, generousBudget)
	if won != 1 || len(fails) > 0 {
		t.Fatalf("%d claims won, failures %v, want exactly one claim for the repository", won, fails)
	}
}

func TestRacingClaimsNeverPushTheReservedTotalPastTheCeiling(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	var runs []dispatcher.Queued
	var ids []string
	for i := range 4 {
		r := queuedRun(fresh("queue-ceiling"), i+1)
		r.Repo = "octo/repo-" + strconv.Itoa(i)
		runs = append(runs, r)
		ids = append(ids, r.RunID)
	}
	forget(t, client, ids...)
	resetLedger(t, client, 10)
	for _, r := range runs {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 2 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	won, fails := raceClaims(q, runs, tight)
	if won != 2 || len(fails) > 0 {
		t.Fatalf("%d claims won, failures %v, want the $2 window to fit exactly two $1 runs", won, fails)
	}
	if got := reservedTotals(readLedgerDoc(t, client)).ProviderCost; got != 2*money.Dollar {
		t.Fatalf("reserved %v, want the ceiling respected", got)
	}
}

// raceClaims claims every run at once, each with a one-dollar reservation, and
// reports how many won and which failed outright.
func raceClaims(q *Queue, runs []dispatcher.Queued, cfg dispatcher.BudgetConfig) (won int, fails []error) {
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, r := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := q.TryClaim(context.Background(), r.RunID, queueAt, cfg, openFacts,
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
	return won, fails
}

// seedSettled writes id as a settled run of size that took the given time and
// ended with outcome, claimed at claimedAt while claimConcurrency runs were in
// flight.
func seedSettled(t *testing.T, client *firestore.Client, id, size string, outcome runner.Outcome, claimConcurrency int, took time.Duration, claimedAt time.Time) {
	t.Helper()
	settledRecord(t, client, id, size, money.Dollar, took.Milliseconds(), claimedAt.Add(took))
	if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), map[string]any{
		claimConcurrencyField: int64(claimConcurrency),
		claimedAtField:        claimedAt,
		outcomeField:          string(outcome),
	}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
}

// seedRuns settles count runs of size that reached a PR, claimed at
// concurrency and each taking took, and returns their ids. The latest ended
// agoBase before queueAt, and each earlier one an hour before the last.
func seedRuns(t *testing.T, client *firestore.Client, size, prefix string, count, concurrency int, took, agoBase time.Duration) []string {
	t.Helper()
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fresh(prefix)
		seedSettled(t, client, ids[i], size, runner.OutcomePROpened, concurrency, took, queueAt.Add(-agoBase-time.Duration(i)*time.Hour))
	}
	return ids
}

// holdReservation makes the ledger hold a reservation for each run, claimed at
// the given time, beside the ledger's own N, stable count and change marker.
func holdReservation(t *testing.T, client *firestore.Client, ledger ledgerDoc, claimedAt map[string]time.Time) {
	t.Helper()
	ledger.Reservations = map[string]reservationEntry{}
	for id, at := range claimedAt {
		ledger.Reservations[id] = reservationEntry{ClaimedAt: at}
	}
	if _, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Set(context.Background(), ledger); err != nil {
		t.Fatal(err)
	}
}

func TestSettleMovesNByWhatTheSettledRunShowed(t *testing.T) {
	const solo, slow = 10 * time.Minute, 20 * time.Minute
	for name, tt := range map[string]struct {
		before  ledgerDoc
		claimed int
		took    time.Duration
		outcome runner.Outcome
		// alone and loaded are earlier runs of the size that reached a PR, at
		// concurrency 1 and at the settled run's own; older are slow loaded runs
		// settled before them all.
		alone, loaded, older int
		// olderAlone are slow runs that ran alone, settled before all the others.
		olderAlone int
		want       ledgerDoc
	}{
		"the fifth stable run raises N": {
			before: ledgerDoc{N: 1, StableRuns: 4}, claimed: 1, took: solo, outcome: runner.OutcomePROpened, alone: 4,
			want: ledgerDoc{N: 2},
		},
		"a slow median of five at N halves it": {
			before: ledgerDoc{N: 2, StableRuns: 3}, claimed: 2, took: slow, outcome: runner.OutcomePROpened, alone: 5, loaded: 4,
			want: ledgerDoc{N: 1},
		},
		"a run claimed below N holds it": {
			before: ledgerDoc{N: 3, StableRuns: 2}, claimed: 1, took: solo, outcome: runner.OutcomePROpened, alone: 5,
			want: ledgerDoc{N: 3, StableRuns: 2},
		},
		"fewer than five runs at N is no evidence": {
			before: ledgerDoc{N: 1, StableRuns: 4}, claimed: 1, took: solo, outcome: runner.OutcomePROpened, alone: 3,
			want: ledgerDoc{N: 1, StableRuns: 4},
		},
		"a quick failure does not count as a stable run": {
			before: ledgerDoc{N: 1, StableRuns: 4}, claimed: 1, took: time.Second, outcome: runner.OutcomeAgentFailed, alone: 5,
			want: ledgerDoc{N: 1, StableRuns: 4},
		},
		"a ledger tuned to two stable runs raises N on the second": {
			before: ledgerDoc{N: 1, StableRuns: 1, TuneStableRuns: 2}, claimed: 1, took: solo, outcome: runner.OutcomePROpened, alone: 2,
			want: ledgerDoc{N: 2},
		},
		"a ledger tuned to a tighter rise band holds a median the default would count": {
			before: ledgerDoc{N: 2, StableRuns: 3, TuneRiseWithin: 1.05}, claimed: 2, took: 11 * time.Minute, outcome: runner.OutcomePROpened, alone: 5, loaded: 4,
			want: ledgerDoc{N: 2},
		},
		"a ledger tuned to an earlier fall band halves at a median the default would hold": {
			before: ledgerDoc{N: 2, StableRuns: 3, TuneRiseWithin: 1.1, TuneHalveBeyond: 1.2}, claimed: 2, took: 13 * time.Minute, outcome: runner.OutcomePROpened, alone: 5, loaded: 4,
			want: ledgerDoc{N: 1},
		},
		"a tuned count reads only that many of the latest runs": {
			before: ledgerDoc{N: 2, StableRuns: 1, TuneStableRuns: 2}, claimed: 2, took: solo, outcome: runner.OutcomePROpened, alone: 5, loaded: 1, older: 3,
			want: ledgerDoc{N: 3},
		},
		"a tuned count reads only that many of the latest runs alone": {
			before: ledgerDoc{N: 2, StableRuns: 1, TuneStableRuns: 2}, claimed: 2, took: 16 * time.Minute, outcome: runner.OutcomePROpened, alone: 2, loaded: 1, olderAlone: 3,
			want: ledgerDoc{N: 1},
		},
		"runs at N are no evidence while too few ran alone": {
			before: ledgerDoc{N: 2, StableRuns: 3}, claimed: 2, took: solo, outcome: runner.OutcomePROpened, alone: 3, loaded: 4,
			want: ledgerDoc{N: 2, StableRuns: 3},
		},
		"only the latest five runs at N make the median": {
			before: ledgerDoc{N: 2, StableRuns: 4}, claimed: 2, took: solo, outcome: runner.OutcomePROpened, alone: 5, loaded: 4, older: 6,
			want: ledgerDoc{N: 3},
		},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			size := "S-" + fresh("settle-n")
			id := fresh("queue-settle-n")
			ids := []string{id}
			ids = append(ids, seedRuns(t, client, size, "queue-settle-alone", tt.alone, 1, solo, 2*time.Hour)...)
			ids = append(ids, seedRuns(t, client, size, "queue-settle-loaded", tt.loaded, tt.claimed, tt.took, 2*time.Hour)...)
			ids = append(ids, seedRuns(t, client, size, "queue-settle-older", tt.older, tt.claimed, 6*slow, 101*time.Hour)...)
			ids = append(ids, seedRuns(t, client, size, "queue-settle-older-alone", tt.olderAlone, 1, 6*slow, 102*time.Hour)...)
			forget(t, client, ids...)
			resetLedger(t, client, tt.before.N)
			seedSettled(t, client, id, size, tt.outcome, tt.claimed, tt.took, queueAt)
			holdReservation(t, client, tt.before, map[string]time.Time{id: queueAt})
			if err := q.Settle(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			got := readLedgerDoc(t, client)
			if got.N != tt.want.N || got.StableRuns != tt.want.StableRuns {
				t.Fatalf("N %d stable %d, want N %d stable %d", got.N, got.StableRuns, tt.want.N, tt.want.StableRuns)
			}
			if _, held := got.Reservations[id]; held {
				t.Fatal("the settled run's reservation was not dropped")
			}
		})
	}
}

func TestSettleHalvesNOncePerIncident(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	ids := []string{fresh("queue-limited-a"), fresh("queue-limited-b"), fresh("queue-limited-c")}
	forget(t, client, ids...)
	resetLedger(t, client, 4)
	claimedAt := map[string]time.Time{}
	for i, id := range ids {
		at := queueAt.Add(time.Duration(i) * time.Second)
		claimedAt[id] = at
		rec := runner.NewRecord(id, runner.Ticket{ID: id, Title: "feat(x): add a file", Size: "S", Body: "Add a file."}, at)
		rec.Outcome, rec.StopReason = runner.OutcomeInfraFailure, runner.StopModelUnavailable
		data := fields(rec)
		data[claimConcurrencyField], data[claimedAtField] = int64(4), at
		if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, data); err != nil {
			t.Fatal(err)
		}
	}
	holdReservation(t, client, ledgerDoc{N: 4, StableRuns: 3}, claimedAt)
	if err := q.Settle(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 2 || got.StableRuns != 0 {
		t.Fatalf("N %d stable %d, want N halved to 2 with the count restarted", got.N, got.StableRuns)
	}
	for _, id := range ids[1:] {
		if err := q.Settle(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if got := readLedgerDoc(t, client).N; got != 2 {
		t.Fatalf("N = %d, want the other failures of the same incident to leave it at 2", got)
	}
}

func TestSettleIgnoresARunClaimedAboveAnNAlreadyHalved(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("queue-overloaded")
	forget(t, client, id)
	resetLedger(t, client, 4)
	seedSettled(t, client, id, "S-"+fresh("overloaded"), runner.OutcomePROpened, 8, time.Hour, queueAt)
	holdReservation(t, client, ledgerDoc{N: 4, StableRuns: 2, ChangedAfter: queueAt.Add(time.Minute)}, map[string]time.Time{id: queueAt})
	if err := q.Settle(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 4 || got.StableRuns != 2 {
		t.Fatalf("N %d stable %d, want a run claimed before N moved to leave both alone", got.N, got.StableRuns)
	}
}

func TestSettleCapsNAtTheConfiguredPlatformCap(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("cap")
	id := fresh("queue-cap")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-cap-alone", 5, 1, 10*time.Minute, time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-cap-loaded", 4, 2, 10*time.Minute, time.Hour)...)
	forget(t, client, ids...)
	resetLedger(t, client, 2)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 2, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 4, PlatformCap: 2}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client).N; got != 2 {
		t.Fatalf("N = %d, want it held at the configured cap", got)
	}
}

func TestTryClaimUsesTheRelationsThePollReadNow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	running := queuedRun(fresh("queue-fresh-running"), 1)
	waiting := queuedRun(fresh("queue-fresh-waiting"), 2)
	running.Repo, waiting.Repo = "octo/running", "octo/waiting"
	forget(t, client, running.RunID, waiting.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{running, waiting} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	if ok, _, err := q.TryClaim(ctx, running.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	facts := openFacts
	facts.Relations = dispatcher.Relations{Known: true, BlockedBy: []string{running.Ticket.ID}}
	ok, binding, err := q.TryClaim(ctx, waiting.RunID, queueAt, generousBudget, facts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionBlocked {
		t.Fatalf("ok = %v, binding = %q, want a relation added after enqueue to hold the run", ok, binding)
	}
}

func TestTryClaimStoresNAndThePlatformCapOnTheLedger(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-ledger-n"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 0)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.Limits.PlatformCap = 7
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{}); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if got := readLedgerDoc(t, client); got.N != 1 || got.PlatformCap != 7 {
		t.Fatalf("N %d, platform cap %d, want 1 and 7 stored", got.N, got.PlatformCap)
	}
}

func TestTryClaimStoresTheTuningOnTheLedger(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-ledger-tuning"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 0)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.Tuning = dispatcher.Tuning{StableRuns: 3, RiseWithin: 1.1, HalveBeyond: 1.8}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{}); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	got := readLedgerDoc(t, client)
	if got.TuneStableRuns != 3 || got.TuneRiseWithin != 1.1 || got.TuneHalveBeyond != 1.8 {
		t.Fatalf("stored tuning = %d %v %v, want 3 1.1 1.8", got.TuneStableRuns, got.TuneRiseWithin, got.TuneHalveBeyond)
	}
}

func TestSettleIgnoresAnInfraFailureThatIsNotAProviderStop(t *testing.T) {
	for name, tt := range map[string]struct {
		outcome runner.Outcome
		reason  runner.StopReason
	}{
		"an infrastructure failure for another reason":        {runner.OutcomeInfraFailure, runner.StopAgentExit},
		"a model-unavailable stop that is not infrastructure": {runner.OutcomeAgentFailed, runner.StopModelUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			id := fresh("queue-not-limited")
			forget(t, client, id)
			resetLedger(t, client, 4)
			rec := runner.NewRecord(id, runner.Ticket{ID: id, Title: "feat(x): add a file", Size: "S", Body: "Add a file."}, queueAt)
			rec.Outcome, rec.StopReason = tt.outcome, tt.reason
			data := fields(rec)
			data[claimConcurrencyField], data[claimedAtField] = int64(4), queueAt
			if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), data); err != nil {
				t.Fatal(err)
			}
			holdReservation(t, client, ledgerDoc{N: 4, StableRuns: 3}, map[string]time.Time{id: queueAt})
			if err := q.Settle(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if got := readLedgerDoc(t, client); got.N != 4 || got.StableRuns != 3 {
				t.Fatalf("N %d stable %d, want N left at 4", got.N, got.StableRuns)
			}
		})
	}
}

func TestSettleNeedsFiveRunsAtNNotOnlyFiveAlone(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("few-loaded")
	id := fresh("queue-few-loaded")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-few-alone", 5, 1, 10*time.Minute, 10*time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-few-loaded-slow", 2, 2, 40*time.Minute, time.Hour)...)
	forget(t, client, ids...)
	resetLedger(t, client, 2)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 2, 40*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 3}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 2 || got.StableRuns != 3 {
		t.Fatalf("N %d stable %d, want three loaded runs to be no evidence", got.N, got.StableRuns)
	}
}

func TestSettleCountsRunsThatFoundNothingToChange(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("no-changes")
	id := fresh("queue-no-changes")
	ids := []string{id}
	for i := range 4 {
		alone := fresh("queue-no-changes-alone")
		ids = append(ids, alone)
		seedSettled(t, client, alone, size, runner.OutcomeNoChanges, 1, 10*time.Minute, queueAt.Add(-time.Duration(i+1)*time.Hour))
	}
	forget(t, client, ids...)
	resetLedger(t, client, 1)
	seedSettled(t, client, id, size, runner.OutcomeNoChanges, 1, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 1, StableRuns: 4}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client).N; got != 2 {
		t.Fatalf("N = %d, want five no-change runs to count as stable and raise it to 2", got)
	}
}

func TestSettleReadsOnlyRunsOfTheSettledRunsSize(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("own-size")
	other := "L-" + fresh("other-size")
	id := fresh("queue-own-size")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-own-alone", 5, 1, 10*time.Minute, 20*time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-own-loaded", 4, 2, 10*time.Minute, time.Hour)...)
	ids = append(ids, seedRuns(t, client, other, "queue-other-loaded", 5, 2, 60*time.Minute, 0)...)
	forget(t, client, ids...)
	resetLedger(t, client, 2)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 2, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 4}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client).N; got != 3 {
		t.Fatalf("N = %d, want runs of another size left out of the medians", got)
	}
}

func TestSettleSkipsARunWithNoSettlementTime(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("unsettled")
	id := fresh("queue-unsettled")
	unsettled := fresh("queue-unsettled-zero")
	ids := append([]string{id, unsettled}, seedRuns(t, client, size, "queue-unsettled-alone", 3, 1, 10*time.Minute, time.Hour)...)
	if _, err := client.Collection(runsCollection).Doc(unsettled).Set(context.Background(), map[string]any{
		sizeField: size, outcomeField: string(runner.OutcomePROpened), claimConcurrencyField: int64(1), settledAtField: time.Time{},
	}); err != nil {
		t.Fatal(err)
	}
	forget(t, client, ids...)
	resetLedger(t, client, 1)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 1, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 1, StableRuns: 4}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 1 || got.StableRuns != 4 {
		t.Fatalf("N %d stable %d, want four settled runs to be no evidence", got.N, got.StableRuns)
	}
}

func TestSettleTakesTheMedianOfOnlyTheLatestFiveLoadedRuns(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("latest-five")
	id := fresh("queue-latest-five")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-five-alone", 5, 1, 10*time.Minute, 50*time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-five-recent", 1, 2, 10*time.Minute, time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-five-slow", 3, 2, 40*time.Minute, 2*time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-five-old", 1, 2, 10*time.Minute, 10*time.Hour)...)
	forget(t, client, ids...)
	resetLedger(t, client, 2)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 2, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 3}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client).N; got != 1 {
		t.Fatalf("N = %d, want the slow median of the latest five to halve it", got)
	}
}

func TestMedianDurationIsTheLowerMiddleOfTheSortedValues(t *testing.T) {
	for name, tt := range map[string]struct {
		in   []time.Duration
		want time.Duration
	}{
		"none":           {nil, 0},
		"an odd count":   {[]time.Duration{3, 1, 2}, 2},
		"an even count":  {[]time.Duration{4, 1, 3, 2}, 2},
		"a single value": {[]time.Duration{7}, 7},
	} {
		t.Run(name, func(t *testing.T) {
			if got := medianDuration(tt.in); got != tt.want {
				t.Fatalf("median = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReleaseDropsOnlyTheReleasedRunsReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	kept := queuedRun(fresh("queue-rel-kept"), 1)
	released := queuedRun(fresh("queue-rel-gone"), 2)
	kept.Repo = "octo/kept"
	forget(t, client, kept.RunID, released.RunID)
	resetLedger(t, client, 5)
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	for _, r := range []dispatcher.Queued{kept, released} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
		if ok, _, err := q.TryClaim(ctx, r.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
			t.Fatalf("claim %s: ok %v, err %v", r.RunID, ok, err)
		}
	}
	if err := q.Release(ctx, released.RunID, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	ledger := readLedgerDoc(t, client)
	if _, ok := ledger.Reservations[kept.RunID]; !ok || len(ledger.Reservations) != 1 {
		t.Fatalf("reservations = %v, want only the run still in flight", slices.Sorted(maps.Keys(ledger.Reservations)))
	}
}

func TestReleaseReturnsARunThatHoldsNoReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rel-none"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.Release(ctx, run.RunID, queueAt.Add(time.Minute)); err != nil {
		t.Fatalf("release with no ledger to drop a reservation from: %v", err)
	}
}

func TestSettleIgnoresARunClaimedBelowNEvenWhereNHasEvidence(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("below-n")
	id := fresh("queue-below-n")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-below-alone", 5, 1, 10*time.Minute, time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-below-loaded", 5, 3, 40*time.Minute, time.Hour)...)
	forget(t, client, ids...)
	resetLedger(t, client, 3)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 1, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 3, StableRuns: 2}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 3 || got.StableRuns != 2 {
		t.Fatalf("N %d stable %d, want a run claimed below N to leave both alone", got.N, got.StableRuns)
	}
}

func TestSettleDropsTheReservationOfARunWithNoRow(t *testing.T) {
	q, client := queue(t)
	id := fresh("queue-no-row")
	forget(t, client, id)
	resetLedger(t, client, 2)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 3}, map[string]time.Time{id: queueAt, "other-" + id: queueAt.Add(time.Minute)})
	if err := q.Settle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	got := readLedgerDoc(t, client)
	if _, held := got.Reservations[id]; held || got.N != 2 || got.StableRuns != 3 {
		t.Fatalf("reservations %v, N %d stable %d, want the reservation dropped and N left alone", got.Reservations, got.N, got.StableRuns)
	}
	if !got.ChangedAfter.IsZero() {
		t.Fatalf("changed_after = %v, want it left alone while N did not move", got.ChangedAfter)
	}
}
