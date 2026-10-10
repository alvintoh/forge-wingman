package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// storeDecision is a decision the store tests park a run on.
var storeDecision = runner.Decision{
	Question: "Which flag should the run use?",
	Context:  []string{"The flag is read in two places."},
	Options: []runner.DecisionOption{
		{Label: "Reuse the existing flag", Value: "reuse", Cost: "no new config"},
		{Label: "Add a new flag", Value: "new", Cost: "one more env var"},
	},
}

// seedLedger replaces the shared ledger with the reservations a test needs.
func seedLedger(t *testing.T, client *firestore.Client, n int, reservations map[string]reservationEntry) {
	t.Helper()
	ref := client.Collection(dispatchCollection).Doc(ledgerDocID)
	if _, err := ref.Set(context.Background(), ledgerDoc{Reservations: reservations, N: n}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = ref.Delete(context.Background()) })
}

// readDecisions reads a run row's decision surface.
func readDecisions(t *testing.T, client *firestore.Client, id string) decisionsRow {
	t.Helper()
	snap, err := client.Collection(runsCollection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var row decisionsRow
	if err := snap.DataTo(&row); err != nil {
		t.Fatal(err)
	}
	return row
}

// parkedRun enqueues a run and parks it on storeDecision, returning its id.
func parkedRun(t *testing.T, q *Queue, client *firestore.Client) string {
	t.Helper()
	id := fresh("decision")
	forget(t, client, id)
	if err := q.Enqueue(context.Background(), queuedRun(id, 2)); err != nil {
		t.Fatal(err)
	}
	seedLedger(t, client, 1, map[string]reservationEntry{
		PlanKey(id): {Stage: dispatcher.StagePlan, ProviderCostMicros: 5, RunnerMinutes: 2, ClaimedAt: queueAt},
	})
	if err := q.Suspend(context.Background(), id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestSuspendParksTheRunAndDropsItsReservation covers AC3: a plan-stage
// decision pauses the run holding no runner and no reservation, keeping the
// plan's own cost on the row.
func TestSuspendParksTheRunAndDropsItsReservation(t *testing.T) {
	q, client := queue(t)
	id := parkedRun(t, q, client)

	row := readDecisions(t, client, id)
	if row.State != stateWaiting {
		t.Fatalf("state = %q, want waiting", row.State)
	}
	if len(row.Decisions) != 1 || row.Decisions[0].Decision.Question != storeDecision.Question || len(row.Decisions[0].Decision.Options) != 2 {
		t.Fatalf("decisions = %+v", row.Decisions)
	}
	if row.RunURL != "https://github.com/o/r/actions/runs/7" {
		t.Fatalf("run_url = %q", row.RunURL)
	}
	if row.PlanSpendMicros != 5 || row.PlanSpendMinutes != 2 {
		t.Fatalf("plan spend = %d/%d, want 5/2", row.PlanSpendMicros, row.PlanSpendMinutes)
	}
	if _, has := readLedgerDoc(t, client).Reservations[PlanKey(id)]; has {
		t.Fatal("the suspended run still holds its plan reservation")
	}
}

// TestSuspendKeepsTheRunOutOfCandidates: a waiting run is never claimed again.
func TestSuspendKeepsTheRunOutOfCandidates(t *testing.T) {
	q, client := queue(t)
	id := parkedRun(t, q, client)
	cands, err := q.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.RunID == id {
			t.Fatalf("a waiting run was a candidate: %+v", c)
		}
	}
}

func TestReplyStampsTheLastUnansweredDecision(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	if err := q.Posted(ctx, id, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	recorded, err := q.Reply(ctx, id, "session-1", "reuse", queueAt.Add(time.Minute))
	if err != nil || !recorded {
		t.Fatalf("reply = %v, %v", recorded, err)
	}
	row := readDecisions(t, client, id)
	if row.Decisions[0].Reply != "reuse" || !row.Decisions[0].RepliedAt.Equal(queueAt.Add(time.Minute)) {
		t.Fatalf("decision = %+v", row.Decisions[0])
	}
	// A reply on another session, or on a run waiting on none, is a no-op.
	if recorded, err := q.Reply(ctx, id, "session-2", "reuse", queueAt); err != nil || recorded {
		t.Fatalf("mismatched session recorded: %v, %v", recorded, err)
	}
	none := fresh("decision-none")
	forget(t, client, none)
	if err := q.Enqueue(ctx, queuedRun(none, 2)); err != nil {
		t.Fatal(err)
	}
	if recorded, err := q.Reply(ctx, none, "session-1", "x", queueAt); err != nil || recorded {
		t.Fatalf("a run waiting on none recorded: %v, %v", recorded, err)
	}
}

// TestAnswerResumesTheRun covers AC3: the matched answer is written and the run
// returns to the queue carrying it.
func TestAnswerResumesTheRun(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	if err := q.Posted(ctx, id, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Reply(ctx, id, "session-1", "reuse", queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	opt, ok := storeDecision.Match("reuse")
	if !ok {
		t.Fatal("fixture does not match")
	}
	if err := q.Answer(ctx, id, opt.Label, queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row := readDecisions(t, client, id)
	if row.State != stateQueued || row.Decisions[0].Answer != "Reuse the existing flag" {
		t.Fatalf("row = %+v", row)
	}
	answers, err := q.Decisions(ctx, id)
	if err != nil || len(answers) != 1 || answers[0].Choice != "Reuse the existing flag" || answers[0].Question != storeDecision.Question {
		t.Fatalf("answers = %+v, %v", answers, err)
	}
	waiting, err := q.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range waiting {
		if w.RunID == id {
			t.Fatalf("an answered run is still waiting: %+v", w)
		}
	}
}

// TestWaitingReadsThePendingDecision: a waiting run is listed with its
// unanswered decision; a stopped one is not.
func TestWaitingReadsThePendingDecision(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	waiting, err := q.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range waiting {
		if w.RunID != id {
			continue
		}
		found = true
		if w.Decision.Question != storeDecision.Question || w.Index != 1 || w.RunURL == "" {
			t.Fatalf("waiting = %+v", w)
		}
	}
	if !found {
		t.Fatal("the waiting run was not listed")
	}
	if err := q.StopWaiting(ctx, id, storeDecision, queueAt); err != nil {
		t.Fatal(err)
	}
	waiting, err = q.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range waiting {
		if w.RunID == id {
			t.Fatal("a stopped run is still listed as waiting")
		}
	}
}

func TestStopWaitingRecordsTheStop(t *testing.T) {
	q, client := queue(t)
	id := parkedRun(t, q, client)
	if err := q.StopWaiting(context.Background(), id, storeDecision, queueAt); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data[outcomeField] != string(runner.OutcomeStopped) ||
		data[buildOutcomeField] != string(runner.OutcomeStopped) ||
		data[stopReasonField] != string(runner.StopWaitingOnOwner) ||
		data[stopDetailField] != storeDecision.Question {
		t.Fatalf("row = %v", data)
	}
	if data[stateField] != stateWaiting {
		t.Fatalf("state = %v, want the run to stay waiting", data[stateField])
	}
}

// TestPlanSpendAccumulatesAcrossARePlan: a run that plans, is suspended and
// resumed counts both plans' estimates, at the later settle stamp.
func TestPlanSpendAccumulatesAcrossARePlan(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	// The resumed plan books a fresh plan reservation, then records and settles.
	seedLedger(t, client, 1, map[string]reservationEntry{
		PlanKey(id): {Stage: dispatcher.StagePlan, ProviderCostMicros: 7, RunnerMinutes: 3, ClaimedAt: queueAt},
	})
	if err := NewRecords(client).WritePlan(ctx, id, runner.PlanRecord{
		Files: []string{"a.go"}, BaseSHA: "base", SettledAt: queueAt.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Settle(ctx, PlanKey(id)); err != nil {
		t.Fatal(err)
	}
	row := readDecisions(t, client, id)
	if row.PlanSpendMicros != 12 || row.PlanSpendMinutes != 5 {
		t.Fatalf("plan spend = %d/%d, want 12/5 (both plans)", row.PlanSpendMicros, row.PlanSpendMinutes)
	}
	snap, err := client.Collection(runsCollection).Doc(id).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data()[planSettledAtField]; got == nil {
		t.Fatal("the re-plan left no settle stamp")
	}
}

// TestPostedAndSessionBind: the session the decision was asked in is recorded
// with the post, and the ticket's binding resolves it.
func TestPostedAndSessionBind(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	if err := q.Posted(ctx, id, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	row := readDecisions(t, client, id)
	if row.Decisions[0].SessionID != "session-1" || !row.Decisions[0].PostedAt.Equal(queueAt) {
		t.Fatalf("decision = %+v", row.Decisions[0])
	}
	markers := NewMarkers(client)
	t.Cleanup(func() {
		_, _ = client.Collection(dispatchCollection).Doc(sessionBindingPrefix + id).Delete(context.Background())
	})
	if _, err := markers.Mark(ctx, "session-9", id, queueAt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.Collection(dispatchCollection).Doc(webhookPrefix + "session-9").Delete(context.Background())
	})
	got, err := q.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got != "session-9" {
		t.Fatalf("session = %q, want the binding the marker wrote", got)
	}
	if got, err := q.Session(ctx, fresh("no-binding")); err != nil || got != "" {
		t.Fatalf("session for an unbound ticket = %q, %v", got, err)
	}
}

// TestDecisionsReadsTheAnswersInOrder: several settled decisions come back in
// the order they were asked.
func TestDecisionsReadsTheAnswersInOrder(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	if err := q.Answer(ctx, id, "first", queueAt); err != nil {
		t.Fatal(err)
	}
	// A second decision on the same run, answered after a resume.
	if err := q.Suspend(ctx, id, storeDecision, "url", queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := q.Answer(ctx, id, "second", queueAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	answers, err := q.Decisions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{answers[0].Choice, answers[1].Choice}; !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("answers = %v", got)
	}
}

// TestAStoppedWaitTakesNoReplyAndNoAnswer: once the wait ended as
// waiting-on-owner the run is done, so neither a late reply nor an answer puts
// it back in the queue.
func TestAStoppedWaitTakesNoReplyAndNoAnswer(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	if err := q.Posted(ctx, id, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	if err := q.StopWaiting(ctx, id, storeDecision, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	recorded, err := q.Reply(ctx, id, "session-1", "reuse", queueAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("recorded a reply on a run already stopped as waiting-on-owner")
	}
	if err := q.Answer(ctx, id, "Reuse the existing flag", queueAt.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row := readDecisions(t, client, id)
	if row.State != stateWaiting || row.Decisions[0].Answer != "" || row.Decisions[0].Reply != "" {
		t.Fatalf("row = %+v, want a stopped wait left alone", row)
	}
}
