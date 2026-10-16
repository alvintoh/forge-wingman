package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// transientAttempt is one recorded automatic requeue as the run row keeps it.
func transientAttempt(class runner.TransientClass, at time.Time) map[string]any {
	return map[string]any{"class": string(class), "at": at}
}

// stoppedOnTransient writes a queued run the store has stopped on a transient
// class, so a test's own seed is the shape a claim and a record leave behind.
func stoppedOnTransient(t *testing.T, q *Queue, client *firestore.Client, id string, class runner.TransientClass) {
	t.Helper()
	ctx := context.Background()
	forget(t, client, id)
	if err := q.Enqueue(ctx, queuedRun(id, 3)); err != nil {
		t.Fatal(err)
	}
	if err := q.Stop(ctx, id, runner.StopChecksRun, "go mod download: "+string(class), queueAt); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{
		{Path: transientField, Value: string(class)},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRequeueTransientReturnsAStoppedRunAsTheNextAttempt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("transient-requeue")
	stoppedOnTransient(t, q, client, id, runner.TransientModuleProxy)
	retriedAt := queueAt.Add(time.Minute)

	attempt, err := q.RequeueTransient(ctx, id, retriedAt, runner.TransientModuleProxy, 2)
	if err != nil {
		t.Fatal(err)
	}
	if attempt != 1 {
		t.Fatalf("attempt = %d, want the run put back as attempt 1", attempt)
	}
	row := readRow(t, client, id)
	if row[stateField] != stateQueued {
		t.Fatalf("state = %v, want %s", row[stateField], stateQueued)
	}
	if row[attemptField] != int64(1) {
		t.Fatalf("attempt field = %v, want 1", row[attemptField])
	}
	if v, held := row[transientField]; held {
		t.Fatalf("the class survived the requeue: %v", v)
	}
	if v, held := row[stopReasonField]; held {
		t.Fatalf("the stop reason survived the requeue: %v", v)
	}
	history := transientRequeues(row)
	if len(history) != 1 {
		t.Fatalf("transient_requeues = %v, want the one attempt recorded", history)
	}
	entry, ok := history[0].(map[string]any)
	if !ok || entry["class"] != string(runner.TransientModuleProxy) {
		t.Fatalf("recorded attempt = %v, want the class it was requeued for", history[0])
	}
	if at, ok := entry["at"].(time.Time); !ok || !at.Equal(retriedAt) {
		t.Fatalf("recorded attempt's time = %v, want the call's own time", entry["at"])
	}
	if !listed(t, q, ctx, id) {
		t.Fatalf("the requeued run %s is not a candidate", id)
	}
}

func TestRequeueTransientRefusesARunThatHasUsedEveryAttempt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("transient-limit")
	seedRow(t, client, id, map[string]any{stateField: stateStopped, transientField: string(runner.TransientModuleProxy)})
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{
		{Path: transientRequeuesField, Value: []any{transientAttempt(runner.TransientModuleProxy, queueAt), transientAttempt(runner.TransientModuleProxy, queueAt)}},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := q.RequeueTransient(ctx, id, queueAt.Add(time.Minute), runner.TransientModuleProxy, 2); !errors.Is(err, ErrRetriesExhausted) {
		t.Fatalf("err = %v, want ErrRetriesExhausted", err)
	}
	row := readRow(t, client, id)
	if row[stateField] != stateStopped {
		t.Fatalf("state = %v, want the run left stopped", row[stateField])
	}
	if len(transientRequeues(row)) != 2 {
		t.Fatalf("transient_requeues = %v, want the two attempts kept", transientRequeues(row))
	}
}

func TestRequeueTransientRefusesARunStillInFlight(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("transient-inflight")
	seedRow(t, client, id, map[string]any{stateField: stateClaimed, transientField: string(runner.TransientModuleProxy)})

	if _, err := q.RequeueTransient(ctx, id, queueAt, runner.TransientModuleProxy, 2); !errors.Is(err, ErrRunInFlight) {
		t.Fatalf("err = %v, want ErrRunInFlight", err)
	}
	if row := readRow(t, client, id); row[stateField] != stateClaimed {
		t.Fatalf("state = %v, want the running run left alone", row[stateField])
	}
}

func TestTransientStopsListsOnlyTheRunsCarryingAClass(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	proxy, lost, unclassed := fresh("transient-listed"), fresh("transient-lost"), fresh("transient-unclassed")
	seedRow(t, client, proxy, map[string]any{stateField: stateStopped, transientField: string(runner.TransientModuleProxy), ticketIDField: "FRG-1", repoField: "octo/scratch"})
	seedRow(t, client, lost, map[string]any{stateField: stateStopped, transientField: string(runner.TransientLostRunner)})
	seedRow(t, client, unclassed, map[string]any{stateField: stateStopped, stopReasonField: string(runner.StopChecksRun)})
	if _, err := client.Collection(runsCollection).Doc(proxy).Update(ctx, []firestore.Update{
		{Path: transientRequeuesField, Value: []any{transientAttempt(runner.TransientModuleProxy, queueAt)}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := q.TransientStops(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byRun := map[string]dispatcher.TransientStop{}
	for _, s := range got {
		byRun[s.RunID] = s
	}
	if p, ok := byRun[proxy]; !ok || p.Class != runner.TransientModuleProxy || p.Attempts != 1 ||
		p.Ticket != "FRG-1" || p.Repo != "octo/scratch" {
		t.Fatalf("the proxy stop = %+v (listed: %v), want %s with one recorded attempt, its ticket and repo",
			p, got, runner.TransientModuleProxy)
	}
	if l, ok := byRun[lost]; !ok || l.Class != runner.TransientLostRunner || l.Attempts != 0 {
		t.Fatalf("the lost runner = %+v, want %s with no attempt recorded", l, runner.TransientLostRunner)
	}
	if s, ok := byRun[unclassed]; ok {
		t.Fatalf("a run carrying no class was listed: %+v", s)
	}
}

// TestALostRunnerIsRequeuedAfterTheSweepClassesIt is the lost-runner path end
// to end, which has no step log to scan: the sweep records the class a claim
// that reported nothing is detected as, and the requeue accepts the abandoned
// row it left.
func TestALostRunnerIsRequeuedAfterTheSweepClassesIt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("transient-swept")
	resetLedger(t, client, 3)
	claimAt(t, q, client, id, dispatcher.StageBuild)
	abandonedAt := sweepClaimedAt.Add(3 * time.Hour)
	if _, err := q.SweepAbandoned(ctx, abandonedAt, sweepWindows); err != nil {
		t.Fatal(err)
	}
	row := readRow(t, client, id)
	if row[stateField] != stateAbandoned || row[transientField] != string(runner.TransientLostRunner) {
		t.Fatalf("abandoned row = state %v, transient %v; want %s classed %s",
			row[stateField], row[transientField], stateAbandoned, runner.TransientLostRunner)
	}

	stops, err := q.TransientStops(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byRun := map[string]dispatcher.TransientStop{}
	for _, s := range stops {
		byRun[s.RunID] = s
	}
	if s, ok := byRun[id]; !ok || s.Class != runner.TransientLostRunner {
		t.Fatalf("stops = %+v, want the abandoned run listed for %s", stops, runner.TransientLostRunner)
	}
	if _, err := q.RequeueTransient(ctx, id, abandonedAt.Add(time.Minute), runner.TransientLostRunner, 2); err != nil {
		t.Fatal(err)
	}
	if st := readRow(t, client, id)[stateField]; st != stateQueued {
		t.Fatalf("state = %v, want the lost run back in %s", st, stateQueued)
	}
}

// TestARequeueClearsTheTransientClass holds the operator's requeue to the same
// field clearing the automatic one does: a run between attempts carries no
// class, so the next stop's class is the one a poll acts on.
func TestARequeueClearsTheTransientClass(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("transient-cleared")
	stoppedOnTransient(t, q, client, id, runner.TransientModuleProxy)

	if _, err := q.Requeue(ctx, id, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if v, held := readRow(t, client, id)[transientField]; held {
		t.Fatalf("transient = %v survived the requeue", v)
	}
}

// TestARequeuedRunIsAdmittedLikeAnyOther holds an automatic requeue to the
// gates a first attempt passes: it only returns the run to the queue, so the
// breaker and the review WIP limit withhold the retry as they withhold a new
// run.
func TestARequeuedRunIsAdmittedLikeAnyOther(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("transient-admission")
	resetLedger(t, client, 1)
	stoppedOnTransient(t, q, client, id, runner.TransientModuleProxy)
	res := dispatcher.Reservation{Stage: dispatcher.StageBuild}
	if _, err := q.RequeueTransient(ctx, id, queueAt.Add(time.Minute), runner.TransientModuleProxy, 2); err != nil {
		t.Fatal(err)
	}

	tripped := openFacts
	tripped.BreakerTripped = true
	if ok, why, err := q.TryClaim(ctx, id, queueAt.Add(2*time.Minute), generousBudget, tripped, res); err != nil || ok || why != dispatcher.ConditionCircuitBreaker {
		t.Fatalf("claim with the breaker tripped = %v, %q, %v; want the breaker to withhold the retry", ok, why, err)
	}
	held := openFacts
	held.OpenPRs = dispatcher.DefaultLimits.ReviewWIP
	if ok, why, err := q.TryClaim(ctx, id, queueAt.Add(3*time.Minute), generousBudget, held, res); err != nil || ok || why != dispatcher.ConditionReviewWIP {
		t.Fatalf("claim at the review WIP limit = %v, %q, %v; want the limit to withhold the retry", ok, why, err)
	}
	if ok, why, err := q.TryClaim(ctx, id, queueAt.Add(4*time.Minute), generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim with nothing holding it = %v, %q, %v; want the retry admitted", ok, why, err)
	}
}
