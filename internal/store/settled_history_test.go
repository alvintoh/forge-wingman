package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
)

// windowName is the provider window the budgets these tests admit against
// measure: a seeded run fills it, so a candidate is withheld on it.
const windowName = "5h"

// historyBudget admits a $2 claim into an empty window, and withholds the
// same claim once an earlier run has filled the window.
var historyBudget = dispatcher.BudgetConfig{
	ProviderWindows: []dispatcher.Window{{Name: windowName, Period: 5 * time.Hour, Limit: 4 * money.Dollar}},
	Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
	Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
}

// historyRow writes a run row carrying the stamps and totals the budget ceilings
// sum: on plan, settled at settledAt and having planned at planSettledAt, either
// of which may be zero for a run that only settled or only planned.
func historyRow(t *testing.T, client *firestore.Client, id, plan string, settledAt, planSettledAt time.Time, cost, planCost money.Micros) {
	t.Helper()
	row := map[string]any{planField: plan, settledProviderCostField: int64(cost), planSpendMicrosField: int64(planCost)}
	if !settledAt.IsZero() {
		row[settledAtField] = settledAt
	}
	if !planSettledAt.IsZero() {
		row[planSettledAtField] = planSettledAt
	}
	seedRow(t, client, id, row)
}

// A window sums the same from the poll's one snapshot as from the store's own
// in-transaction scan, so reading the history once for a poll changes no
// admission decision (AC4).
func TestThePollSnapshotSumsAWindowAsTheStoresOwnScanDoes(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	// A plan name of this test's own, so the runs other tests leave behind in
	// the shared collection cannot count toward either sum.
	plan := fresh("history-plan")
	model := plan + "/model"
	settled, planned, outside := fresh("history-settled"), fresh("history-planned"), fresh("history-outside")
	historyRow(t, client, settled, plan, queueAt, queueAt, 3*money.Dollar, money.Dollar)
	historyRow(t, client, planned, plan, time.Time{}, queueAt, 0, 2*money.Dollar)
	historyRow(t, client, outside, plan, queueAt.Add(-6*time.Hour), time.Time{}, 9*money.Dollar, 0)

	at := queueAt.Add(time.Minute)
	// The horizon the poll computes for its widest window — the cash month here
	// — so the snapshot holds every run either sum can count.
	history, err := q.SettledHistory(ctx, historyBudget.Cash.Since(at))
	if err != nil {
		t.Fatal(err)
	}
	_, scanned, err := q.Spend(ctx, historyBudget, model, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := history.Sums(historyBudget, model, at); !reflect.DeepEqual(got, scanned) {
		t.Fatalf("the snapshot sums %+v, the store's own scan %+v", got, scanned)
	}
}

// The snapshot reads the runs its since covers and no earlier ones: the poll's
// own horizon is what bounds the read, so a run settled before it is left out
// however wide the ceilings' own windows are (AC1). The cash window here spans
// both runs' month, and still sums only the one the snapshot read.
func TestSettledHistoryLeavesOutARunSettledBeforeItsSince(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	plan := fresh("history-plan")
	model := plan + "/model"
	inside, before := fresh("history-inside"), fresh("history-before")
	historyRow(t, client, inside, plan, queueAt, time.Time{}, 2*money.Dollar, 0)
	historyRow(t, client, before, plan, queueAt.Add(-6*time.Hour), time.Time{}, 9*money.Dollar, 0)

	history, err := q.SettledHistory(ctx, queueAt.Add(-5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := history.Sums(historyBudget, model, queueAt)
	if len(got.Windows) != 1 || got.Windows[0] != 2*money.Dollar {
		t.Fatalf("the 5h window sums %v, want only the run inside it", got.Windows)
	}
	if got.CashCost != 2*money.Dollar {
		t.Fatalf("the cash month sums %v, want only the run the snapshot read", got.CashCost)
	}
}

// A claim's ceilings are summed from the poll's snapshot: the same claim is
// admitted against an empty window and withheld once a run has filled it (AC1).
func TestTryClaimChecksACeilingAgainstThePollSnapshot(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	plan := fresh("history-plan")
	model := plan + "/model"
	empty, filled := queuedRun(fresh("history-empty"), 1), queuedRun(fresh("history-filled"), 2)
	forget(t, client, empty.RunID, filled.RunID)
	resetLedger(t, client, 1)
	for _, run := range []dispatcher.Queued{empty, filled} {
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	facts := openFacts
	facts.Model = model
	facts.History = func() (dispatcher.SettledHistory, error) {
		return q.SettledHistory(ctx, queueAt.Add(-5*time.Hour))
	}
	res := dispatcher.Reservation{ProviderCost: 2 * money.Dollar}
	if ok, binding, err := q.TryClaim(ctx, empty.RunID, queueAt, historyBudget, facts, res); err != nil || !ok {
		t.Fatalf("ok = %v, binding = %q, err = %v, want an empty window to admit the claim", ok, binding, err)
	}
	// The first claim's own reservation is released, so only the settled run
	// seeded below can withhold the second.
	if err := q.Release(ctx, empty.RunID, queueAt); err != nil {
		t.Fatal(err)
	}
	historyRow(t, client, fresh("history-spent"), plan, queueAt, time.Time{}, 3*money.Dollar, 0)

	ok, binding, err := q.TryClaim(ctx, filled.RunID, queueAt, historyBudget, facts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != windowName {
		t.Fatalf("ok = %v, binding = %q, want the window the settled run fills", ok, binding)
	}
}

// A poll whose settled history cannot be read admits nothing: a claim that
// swallowed the failure would sum every ceiling from an empty history and admit
// a run as though nothing had been spent.
func TestTryClaimFailsWhenThePollSnapshotCannotBeRead(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("history-unread"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	errUnread := errors.New("history refused")
	facts := openFacts
	facts.History = func() (dispatcher.SettledHistory, error) { return nil, errUnread }

	ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{})
	if !errors.Is(err, errUnread) || ok {
		t.Fatalf("ok = %v, err = %v, want the unread history to fail the claim", ok, err)
	}
	if got := readRow(t, client, run.RunID)[stateField]; got != stateQueued {
		t.Fatalf("state = %v, want %q: a claim that failed must leave the run queued", got, stateQueued)
	}
}

// The snapshot is read from two scans, and a failure of either fails the read
// rather than yielding the history of whichever scan succeeded.
func TestSettledHistoryFailsWhenAScanCannotBeRead(t *testing.T) {
	q, _ := queue(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	history, err := q.SettledHistory(ctx, queueAt.Add(-5*time.Hour))
	if err == nil || history != nil {
		t.Fatalf("history = %v, err = %v, want the failed scan to fail the read", history, err)
	}
}

// A candidate the review WIP already holds is withheld without the poll's
// snapshot being read at all (AC5).
func TestTryClaimWithholdsACandidateWithoutReadingTheSettledHistory(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("history-held"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	reads := 0
	facts := openFacts
	facts.OpenPRs = dispatcher.DefaultLimits.ReviewWIP
	facts.History = func() (dispatcher.SettledHistory, error) {
		reads++
		return q.SettledHistory(ctx, queueAt.Add(-5*time.Hour))
	}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionReviewWIP {
		t.Fatalf("ok = %v, binding = %q, want the review WIP to withhold it", ok, binding)
	}
	if reads != 0 {
		t.Fatalf("the settled history was read %d times for a candidate the review WIP holds, want none", reads)
	}
	if got := readRow(t, client, run.RunID)[waitingOnField]; got != dispatcher.ConditionReviewWIP {
		t.Fatalf("waiting on %v, want %q", got, dispatcher.ConditionReviewWIP)
	}
}

// Spend sums its settled half from the poll's snapshot, so the history costs a
// poll one read however many deferrals raise a notice (AC1).
func TestSpendSumsItsSettledHalfFromThePollSnapshot(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	plan := fresh("history-plan")
	model := plan + "/model"
	historyRow(t, client, fresh("history-spent"), plan, queueAt, time.Time{}, 3*money.Dollar, 0)
	reads := 0
	history := func() (dispatcher.SettledHistory, error) {
		reads++
		return q.SettledHistory(ctx, queueAt.Add(-5*time.Hour))
	}

	_, settled, err := q.Spend(ctx, historyBudget, model, queueAt.Add(time.Minute), history)
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("the spend read the settled history %d times, want the poll's one snapshot", reads)
	}
	if len(settled.Windows) != 1 || settled.Windows[0] != 3*money.Dollar {
		t.Fatalf("the spend sums %v, want the seeded run's $3", settled.Windows)
	}
}
