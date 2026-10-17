package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
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
		reservationKey(id, 0, dispatcher.StagePlan): {Stage: dispatcher.StagePlan, ProviderCostMicros: 5, RunnerMinutes: 2, ClaimedAt: queueAt},
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
	if _, has := readLedgerDoc(t, client).Reservations[reservationKey(id, 0, dispatcher.StagePlan)]; has {
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
	if data[stateField] != stateStopped {
		t.Fatalf("state = %v, want a stopped wait out of the waiting state", data[stateField])
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
		reservationKey(id, 0, dispatcher.StagePlan): {Stage: dispatcher.StagePlan, ProviderCostMicros: 7, RunnerMinutes: 3, ClaimedAt: queueAt},
	})
	if err := NewRecords(client).WritePlan(ctx, id, runner.PlanRecord{
		Files: []string{"a.go"}, BaseSHA: "base", SettledAt: queueAt.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.SettlePlan(ctx, id, attemptOf(0)); err != nil {
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

// TestReplyIgnoresAnEmptyMessage: an empty message carries no answer, so it is
// not recorded as a reply the dispatcher would then try to match.
func TestReplyIgnoresAnEmptyMessage(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	if err := q.Posted(ctx, id, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	recorded, err := q.Reply(ctx, id, "session-1", "   ", queueAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("recorded an empty message as a reply")
	}
	row := readDecisions(t, client, id)
	if row.Decisions[0].Reply != "" || !row.Decisions[0].RepliedAt.IsZero() {
		t.Fatalf("decision = %+v, want no reply recorded", row.Decisions[0])
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
	if row.State != stateStopped || row.Decisions[0].Answer != "" || row.Decisions[0].Reply != "" {
		t.Fatalf("row = %+v, want a stopped wait left alone", row)
	}
	// A stopped wait leaves the waiting state, so it cannot hold a slot in the
	// scan that finds the runs still owed a question.
	waiting, err := q.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range waiting {
		if w.RunID == id {
			t.Fatalf("a stopped wait is still listed: %+v", w)
		}
	}
}

// runningPlan enqueues a run holding a plan reservation, as a plan stage in flight does.
func runningPlan(t *testing.T, q *Queue, client *firestore.Client) string {
	t.Helper()
	id := fresh("decision-auto")
	forget(t, client, id)
	if err := q.Enqueue(context.Background(), queuedRun(id, 2)); err != nil {
		t.Fatal(err)
	}
	seedLedger(t, client, 1, map[string]reservationEntry{
		reservationKey(id, 0, dispatcher.StagePlan): {Stage: dispatcher.StagePlan, ProviderCostMicros: 5, RunnerMinutes: 2, ClaimedAt: queueAt},
	})
	return id
}

func TestTakeRecommendationAnswersWithTheFirstOptionAndRequeues(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := runningPlan(t, q, client)
	if err := q.TakeRecommendation(ctx, id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
		t.Fatal(err)
	}
	row := readDecisions(t, client, id)
	if row.State != stateQueued || row.Outcome != "" {
		t.Fatalf("state = %q, outcome = %q, want queued", row.State, row.Outcome)
	}
	if len(row.Decisions) != 1 || row.Decisions[0].Answer != "Reuse the existing flag" || !row.Decisions[0].Provisional || !row.Decisions[0].ConfirmedAt.IsZero() {
		t.Fatalf("decisions = %+v", row.Decisions)
	}
	if row.PlanSpendMicros != 5 || row.PlanSpendMinutes != 2 {
		t.Fatalf("plan spend = %d/%d, want 5/2", row.PlanSpendMicros, row.PlanSpendMinutes)
	}
	if _, has := readLedgerDoc(t, client).Reservations[reservationKey(id, 0, dispatcher.StagePlan)]; has {
		t.Fatal("the run still holds its plan reservation")
	}
	answers, err := q.Decisions(ctx, id)
	if err != nil || len(answers) != 1 || answers[0].Choice != "Reuse the existing flag" {
		t.Fatalf("answers = %+v, %v", answers, err)
	}
	waiting, err := q.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range waiting {
		if w.RunID == id {
			t.Fatalf("an auto-pilot run is waiting: %+v", w)
		}
	}
}

func TestSuspendRecordsNothingProvisional(t *testing.T) {
	q, client := queue(t)
	id := parkedRun(t, q, client)
	row := readDecisions(t, client, id)
	if row.Decisions[0].Provisional || row.Decisions[0].Answer != "" {
		t.Fatalf("a guided decision = %+v, want it unanswered and not provisional", row.Decisions[0])
	}
	unposted, err := q.ProvisionalUnposted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range unposted {
		if p.RunID == id {
			t.Fatalf("a guided run is listed as provisional: %+v", p)
		}
	}
}

func TestTakeRecommendationRefusesADecisionWithNoOptions(t *testing.T) {
	q, client := queue(t)
	id := runningPlan(t, q, client)
	if err := q.TakeRecommendation(context.Background(), id, runner.Decision{Question: "Q?"}, "https://github.com/o/r/actions/runs/7", queueAt); err == nil {
		t.Fatal("took a recommendation from a decision with no options")
	}
}

func TestProvisionalUnpostedSkipsARunThatHasFinished(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := runningPlan(t, q, client)
	if err := q.TakeRecommendation(ctx, id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{{Path: outcomeField, Value: "agent-failed"}}); err != nil {
		t.Fatal(err)
	}
	runs, err := q.ProvisionalUnposted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.RunID == id {
			t.Fatalf("a finished run is listed for a post: %+v", r)
		}
	}
}

func TestProvisionalDecisionIsPostedOnce(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := runningPlan(t, q, client)
	if err := q.TakeRecommendation(ctx, id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
		t.Fatal(err)
	}
	listed := func() *dispatcher.ProvisionalRun {
		runs, err := q.ProvisionalUnposted(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			if r.RunID == id {
				return &r
			}
		}
		return nil
	}
	if p := listed(); p == nil || p.Taken != "Reuse the existing flag" || p.Index != 1 || p.Decision.Question != storeDecision.Question {
		t.Fatalf("listed = %+v", p)
	}
	if err := q.PostedProvisional(ctx, id, "session-1", queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if p := listed(); p != nil {
		t.Fatalf("a posted provisional decision is still listed: %+v", p)
	}
	if row := readDecisions(t, client, id); row.Decisions[0].SessionID != "session-1" || !row.Decisions[0].PostedAt.Equal(queueAt.Add(time.Minute)) {
		t.Fatalf("decision = %+v", row.Decisions[0])
	}
}

func TestProvisionalDecisionsReadTheOptionTakenAndItsConfirmation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := runningPlan(t, q, client)
	if err := q.TakeRecommendation(ctx, id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
		t.Fatal(err)
	}
	got, err := q.ProvisionalDecisions(ctx, id)
	want := []runner.Provisional{{Question: "Which flag should the run use?", Choice: "Reuse the existing flag", Cost: "no new config"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("provisional = %+v, %v, want %+v", got, err, want)
	}
	if _, err := q.ProvisionalDecisions(ctx, "no-such-run"); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want ErrRecordNotFound", err)
	}
}

func TestTryClaimRecordsTheDecisionModeOnce(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-mode"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 2)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.DecisionMode = runner.DecisionAutopilot
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StagePlan}); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data()[decisionModeField]; got != "autopilot" {
		t.Fatalf("decision_mode = %v, want autopilot", got)
	}
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{{Path: stateField, Value: statePlanned}}); err != nil {
		t.Fatal(err)
	}
	guided := openFacts
	guided.DecisionMode = runner.DecisionGuided
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, guided, dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StageBuild}); err != nil || !ok {
		t.Fatalf("second claim = %v, %v", ok, err)
	}
	if got := readRow(t, client, run.RunID)[decisionModeField]; got != "autopilot" {
		t.Fatalf("decision_mode = %v after a later claim, want the first claim's autopilot kept", got)
	}
}

func TestSuspendAndTakeRecommendationFailOnAMissingRun(t *testing.T) {
	q, _ := queue(t)
	ctx := context.Background()
	if err := q.Suspend(ctx, "no-such-run", storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err == nil {
		t.Fatal("Suspend succeeded on a run that does not exist")
	}
	if err := q.TakeRecommendation(ctx, "no-such-run", storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err == nil {
		t.Fatal("TakeRecommendation succeeded on a run that does not exist")
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestProvisionalUnpostedFailsWhenTheScanCannotBeReadOrDecoded(t *testing.T) {
	q, client := queue(t)
	if _, err := q.ProvisionalUnposted(cancelledContext()); err == nil {
		t.Fatal("a cancelled scan was reported as empty")
	}
	seedRow(t, client, fresh("decision-corrupt"), map[string]any{decisionsField: "not a list", provisionalUnpostedField: true})
	if _, err := q.ProvisionalUnposted(context.Background()); err == nil {
		t.Fatal("a row whose decisions cannot be decoded was skipped")
	}
}

func TestPostedProvisionalIsANoOpWhereThereIsNothingToStamp(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	if err := q.PostedProvisional(ctx, "no-such-run", "session-1", queueAt); err != nil {
		t.Fatalf("a missing run = %v, want a no-op", err)
	}
	id := parkedRun(t, q, client)
	if err := q.PostedProvisional(ctx, id, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	if got := readDecisions(t, client, id).Decisions[0]; got.SessionID != "" || !got.PostedAt.IsZero() {
		t.Fatalf("a guided decision was stamped as posted: %+v", got)
	}
}

func TestPostedProvisionalFailsWhenTheRowCannotBeReadOrDecoded(t *testing.T) {
	q, client := queue(t)
	id := runningPlan(t, q, client)
	if err := q.TakeRecommendation(context.Background(), id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
		t.Fatal(err)
	}
	if err := q.PostedProvisional(cancelledContext(), id, "session-1", queueAt); err == nil {
		t.Fatal("a cancelled stamp was reported as done")
	}
	bad := fresh("decision-corrupt-posted")
	seedRow(t, client, bad, map[string]any{decisionsField: "not a list", provisionalUnpostedField: true})
	if err := q.PostedProvisional(context.Background(), bad, "session-1", queueAt); err == nil {
		t.Fatal("a row whose decisions cannot be decoded was stamped")
	}
}

func TestProvisionalMarkStaysUntilEveryProvisionalDecisionIsPosted(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := runningPlan(t, q, client)
	for range 2 {
		if err := q.TakeRecommendation(ctx, id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.PostedProvisional(ctx, id, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	if got := readRow(t, client, id)[provisionalUnpostedField]; got != true {
		t.Fatalf("provisional_unposted = %v after the first of two posts, want it kept", got)
	}
	if err := q.PostedProvisional(ctx, id, "session-1", queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, has := readRow(t, client, id)[provisionalUnpostedField]; has {
		t.Fatal("provisional_unposted still set after every decision was posted")
	}
}

func TestProvisionalDecisionsReadsOnlyTheProvisionalOnesAndToleratesAnUnlistedAnswer(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("decision-mixed")
	seedRow(t, client, id, map[string]any{decisionsField: []decisionEntry{
		{Decision: storeDecision, Answer: storeDecision.Options[1].Label},
		{Decision: storeDecision, Answer: "An option the plan no longer lists", Provisional: true},
	}})
	got, err := q.ProvisionalDecisions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []runner.Provisional{{Question: storeDecision.Question, Choice: "An option the plan no longer lists"}}
	if !slices.Equal(got, want) {
		t.Fatalf("provisional = %+v, want only the provisional decision, with no cost for an unlisted answer: %+v", got, want)
	}
}

func TestProvisionalDecisionsFailsWhenTheRowCannotBeReadOrDecoded(t *testing.T) {
	q, client := queue(t)
	if _, err := q.ProvisionalDecisions(cancelledContext(), "any-run"); err == nil || errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("a cancelled read = %v, want an error that is not a missing record", err)
	}
	bad := fresh("decision-corrupt-read")
	seedRow(t, client, bad, map[string]any{decisionsField: "not a list"})
	if _, err := q.ProvisionalDecisions(context.Background(), bad); err == nil {
		t.Fatal("a row whose decisions cannot be decoded was read as empty")
	}
}

// repliedAtOf is when the owner replied to the index'th decision (1-based) on id, zero when it has none.
func repliedAtOf(t *testing.T, client *firestore.Client, id string, index int) time.Time {
	t.Helper()
	row := readDecisions(t, client, id)
	if index < 1 || index > len(row.Decisions) {
		return time.Time{}
	}
	return row.Decisions[index-1].RepliedAt
}

// postedProvisionalRun is an auto-pilot run whose provisional decision has been posted in session-1.
func postedProvisionalRun(t *testing.T, q *Queue, client *firestore.Client) string {
	t.Helper()
	ctx := context.Background()
	id := runningPlan(t, q, client)
	if err := q.TakeRecommendation(ctx, id, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
		t.Fatal(err)
	}
	if err := q.PostedProvisional(ctx, id, "session-1", queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return id
}

// endRun records the result of a finished run that opened prURL.
func endRun(t *testing.T, client *firestore.Client, id, prURL string) {
	t.Helper()
	if _, err := client.Collection(runsCollection).Doc(id).Update(context.Background(), []firestore.Update{
		{Path: stateField, Value: stateStopped},
		{Path: outcomeField, Value: runner.OutcomeBuilt},
		{Path: prURLField, Value: prURL},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReplyRecordsOnAPostedUnconfirmedProvisionalDecisionOfARunThatIsNotWaiting(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	recorded, err := q.Reply(ctx, id, "session-1", "new", queueAt.Add(2*time.Minute))
	if err != nil || !recorded {
		t.Fatalf("reply = %v, %v", recorded, err)
	}
	row := readDecisions(t, client, id)
	if row.Decisions[0].Reply != "new" || !row.Decisions[0].RepliedAt.Equal(queueAt.Add(2*time.Minute)) {
		t.Fatalf("decision = %+v", row.Decisions[0])
	}
	if row.State != stateQueued {
		t.Fatalf("state = %q, a reply must not move the run", row.State)
	}
}

func TestReplyDropsWhatAProvisionalDecisionCannotTake(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	if recorded, err := q.Reply(ctx, id, "session-2", "new", queueAt.Add(2*time.Minute)); err != nil || recorded {
		t.Fatalf("a reply on the wrong session recorded: %v, %v", recorded, err)
	}
	if recorded, err := q.Reply(ctx, id, "session-1", "  ", queueAt.Add(2*time.Minute)); err != nil || recorded {
		t.Fatalf("an empty reply recorded: %v, %v", recorded, err)
	}
	unposted := runningPlan(t, q, client)
	if err := q.TakeRecommendation(ctx, unposted, storeDecision, "https://github.com/o/r/actions/runs/7", queueAt); err != nil {
		t.Fatal(err)
	}
	if recorded, err := q.Reply(ctx, unposted, "", "new", queueAt); err != nil || recorded {
		t.Fatalf("a reply to a decision never posted recorded: %v, %v", recorded, err)
	}
	if err := q.ConfirmProvisional(ctx, id, 1, repliedAtOf(t, client, id, 1), queueAt.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if recorded, err := q.Reply(ctx, id, "session-1", "new", queueAt.Add(4*time.Minute)); err != nil || recorded {
		t.Fatalf("a reply to a confirmed decision recorded: %v, %v", recorded, err)
	}
}

func TestReplyOnAGuidedRunStillStampsItsWaitingDecisionOnly(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := parkedRun(t, q, client)
	if err := q.Posted(ctx, id, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	if recorded, err := q.Reply(ctx, id, "session-1", "new", queueAt.Add(time.Minute)); err != nil || !recorded {
		t.Fatalf("reply = %v, %v", recorded, err)
	}
	if replied, err := q.ProvisionalReplied(ctx); err != nil || slices.ContainsFunc(replied, func(p dispatcher.ProvisionalRun) bool { return p.RunID == id }) {
		t.Fatalf("a guided run's reply is listed as provisional: %+v, %v", replied, err)
	}
	snap, err := client.Collection(runsCollection).Doc(id).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, marked := snap.Data()[provisionalRepliedField]; marked {
		t.Fatal("a guided run carries the provisional-replied mark")
	}
	// A stopped guided run takes no reply.
	if err := q.StopWaiting(ctx, id, storeDecision, queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if recorded, err := q.Reply(ctx, id, "session-1", "reuse", queueAt.Add(3*time.Minute)); err != nil || recorded {
		t.Fatalf("a stopped guided run took a reply: %v, %v", recorded, err)
	}
}

func TestProvisionalRepliedListsExactlyTheRepliedUnconfirmedOnes(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	replied := postedProvisionalRun(t, q, client)
	silent := postedProvisionalRun(t, q, client)
	confirmed := postedProvisionalRun(t, q, client)
	for _, id := range []string{replied, confirmed} {
		if _, err := q.Reply(ctx, id, "session-1", "reuse", queueAt.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.ConfirmProvisional(ctx, confirmed, 1, repliedAtOf(t, client, confirmed, 1), queueAt.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := q.ProvisionalReplied(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, p := range got {
		if p.RunID == replied {
			if p.Reply != "reuse" || p.Index != 1 || p.Taken != "Reuse the existing flag" || !p.RepliedAt.Equal(queueAt.Add(2*time.Minute)) {
				t.Fatalf("listed = %+v", p)
			}
		}
		listed = append(listed, p.RunID)
	}
	if !slices.Contains(listed, replied) || slices.Contains(listed, silent) || slices.Contains(listed, confirmed) {
		t.Fatalf("listed %v, want only %s", listed, replied)
	}
}

func TestConfirmProvisionalStampsTheDecisionAndLeavesTheRunAlone(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	if _, err := q.Reply(ctx, id, "session-1", "reuse", queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	before := readDecisions(t, client, id)
	if err := q.ConfirmProvisional(ctx, id, 1, repliedAtOf(t, client, id, 1), queueAt.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row := readDecisions(t, client, id)
	if !row.Decisions[0].ConfirmedAt.Equal(queueAt.Add(3*time.Minute)) || !row.Decisions[0].Provisional {
		t.Fatalf("decision = %+v", row.Decisions[0])
	}
	if row.State != before.State || row.Outcome != before.Outcome {
		t.Fatalf("state %q/%q moved from %q/%q", row.State, row.Outcome, before.State, before.Outcome)
	}
	got, err := q.ProvisionalDecisions(ctx, id)
	if err != nil || len(got) != 1 || !got[0].Confirmed {
		t.Fatalf("provisional = %+v, %v", got, err)
	}
	// Confirming again, or a decision that is not there, changes nothing.
	if err := q.ConfirmProvisional(ctx, id, 1, repliedAtOf(t, client, id, 1), queueAt.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := q.ConfirmProvisional(ctx, id, 5, repliedAtOf(t, client, id, 5), queueAt.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if again := readDecisions(t, client, id); !again.Decisions[0].ConfirmedAt.Equal(queueAt.Add(3 * time.Minute)) {
		t.Fatalf("confirmed_at = %v, want the first confirmation kept", again.Decisions[0].ConfirmedAt)
	}
	if err := q.ConfirmProvisional(ctx, "no-such-run", 1, time.Time{}, queueAt); err != nil {
		t.Fatalf("a missing run is a no-op: %v", err)
	}
}

func TestSupersedeQueuesAnEndedRunWithTheOwnersAnswer(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	if _, err := q.Reply(ctx, id, "session-1", "new", queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	endRun(t, client, id, "https://github.com/o/r/pull/141")
	at := queueAt.Add(5 * time.Minute)
	if err := q.Supersede(ctx, id, 1, repliedAtOf(t, client, id, 1), "Add a new flag", at); err != nil {
		t.Fatal(err)
	}
	data := readRow(t, client, id)
	if data[stateField] != stateQueued || data[attemptField] != int64(1) {
		t.Fatalf("state %v, attempt %v, want queued as attempt 1", data[stateField], data[attemptField])
	}
	for _, field := range []string{outcomeField, claimedAtField, provisionalRepliedField} {
		if _, ok := data[field]; ok {
			t.Errorf("field %s survived the supersede: %v", field, data[field])
		}
	}
	if data[supersededPRField] != int64(141) || data[supersededAttemptField] != int64(0) {
		t.Fatalf("superseded_pr %v, superseded_attempt %v, want 141 and 0", data[supersededPRField], data[supersededAttemptField])
	}
	row := readDecisions(t, client, id)
	if d := row.Decisions[0]; d.Answer != "Add a new flag" || d.Provisional {
		t.Fatalf("decision = %+v, want the owner's answer, not provisional", d)
	}
	if got, err := q.ProvisionalDecisions(ctx, id); err != nil || len(got) != 0 {
		t.Fatalf("provisional = %+v, %v, want none left to confirm", got, err)
	}
	answers, err := q.Decisions(ctx, id)
	if err != nil || len(answers) != 1 || answers[0].Choice != "Add a new flag" {
		t.Fatalf("answers = %+v, %v", answers, err)
	}
	if n, err := q.SupersededPR(ctx, id); err != nil || n != 141 {
		t.Fatalf("SupersededPR = %d, %v", n, err)
	}
	res := dispatcher.Reservation{ProviderCost: money.FromUSD(0.25), RunnerMinutes: 4, Stage: dispatcher.StagePlan}
	resetLedger(t, client, 1)
	if ok, why, err := q.TryClaim(ctx, id, at.Add(time.Minute), generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim after the supersede: ok %v, why %q, err %v", ok, why, err)
	}
}

func TestClearProvisionalReplyDropsTheMarkOnlyForTheReplyThePollRead(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	first := queueAt.Add(2 * time.Minute)
	if _, err := q.Reply(ctx, id, "session-1", "hmm", first); err != nil {
		t.Fatal(err)
	}
	later := queueAt.Add(3 * time.Minute)
	if _, err := q.Reply(ctx, id, "session-1", "still unsure", later); err != nil {
		t.Fatal(err)
	}
	if err := q.ClearProvisionalReply(ctx, id, first); err != nil {
		t.Fatal(err)
	}
	if marked, _ := readRow(t, client, id)[provisionalRepliedField].(bool); !marked {
		t.Fatal("a stale clear dropped the mark of a newer reply")
	}
	if err := q.ClearProvisionalReply(ctx, id, later); err != nil {
		t.Fatal(err)
	}
	data := readRow(t, client, id)
	if _, marked := data[provisionalRepliedField]; marked {
		t.Fatal("the mark survived a clear of the latest reply")
	}
	if row := readDecisions(t, client, id); row.Decisions[0].Reply != "still unsure" {
		t.Fatalf("the clear changed the decision: %+v", row.Decisions[0])
	}
	if got, err := q.ProvisionalReplied(ctx); err != nil || len(got) != 0 {
		t.Fatalf("replied = %+v, %v, want none listed after the clear", got, err)
	}
}

func TestSupersedeCarriesTheEndedAttemptsSpendForward(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	if _, err := q.Reply(ctx, id, "session-1", "new", queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	endRun(t, client, id, "https://github.com/o/r/pull/141")
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{
		{Path: settledProviderCostField, Value: int64(250_000)},
		{Path: settledRunnerMinutesField, Value: int64(4)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Supersede(ctx, id, 1, repliedAtOf(t, client, id, 1), "Add a new flag", queueAt.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	data := readRow(t, client, id)
	if data[priorSettledCostField] != int64(250_000) || data[priorSettledMinutesField] != int64(4) {
		t.Fatalf("prior settled = %v micros, %v minutes, want 250000 and 4", data[priorSettledCostField], data[priorSettledMinutesField])
	}
}

func TestSupersedeOfARunThatOpenedNoPRRecordsNone(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	endRun(t, client, id, "")
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{{Path: supersededPRField, Value: int64(7)}}); err != nil {
		t.Fatal(err)
	}
	if err := q.Supersede(ctx, id, 1, repliedAtOf(t, client, id, 1), "Add a new flag", queueAt.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := q.SupersededPR(ctx, id); err != nil || n != 0 {
		t.Fatalf("SupersededPR = %d, %v, want none", n, err)
	}
}

func TestSupersedeRefusesARunThatHasNotEnded(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	if err := q.Supersede(ctx, id, 1, repliedAtOf(t, client, id, 1), "Add a new flag", queueAt); !errors.Is(err, dispatcher.ErrRunNotEnded) || !errors.Is(err, ErrRunDispatchable) {
		t.Fatalf("a still-dispatchable run: err = %v", err)
	}
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{{Path: stateField, Value: stateClaimed}}); err != nil {
		t.Fatal(err)
	}
	if err := q.Supersede(ctx, id, 1, repliedAtOf(t, client, id, 1), "Add a new flag", queueAt); !errors.Is(err, dispatcher.ErrRunNotEnded) || !errors.Is(err, ErrRunInFlight) {
		t.Fatalf("a run in flight: err = %v", err)
	}
	row := readDecisions(t, client, id)
	if row.Decisions[0].Answer != "Reuse the existing flag" || !row.Decisions[0].Provisional {
		t.Fatalf("a refused supersede changed the decision: %+v", row.Decisions[0])
	}
}

func TestSupersedeRefusesADecisionThatIsNotProvisionalOrNotThere(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	endRun(t, client, id, "https://github.com/o/r/pull/141")
	if err := q.Supersede(ctx, id, 2, repliedAtOf(t, client, id, 2), "Add a new flag", queueAt); !errors.Is(err, dispatcher.ErrReplyUnusable) {
		t.Fatalf("a decision not on the row: err = %v, want an unusable reply", err)
	}
	if err := q.Supersede(ctx, id, 0, repliedAtOf(t, client, id, 0), "Add a new flag", queueAt); err == nil {
		t.Fatal("superseded decision 0")
	}
	if err := q.Supersede(ctx, id, 1, repliedAtOf(t, client, id, 1), "Add a new flag", queueAt); err != nil {
		t.Fatal(err)
	}
	endRun(t, client, id, "https://github.com/o/r/pull/142")
	if err := q.Supersede(ctx, id, 1, repliedAtOf(t, client, id, 1), "Reuse the existing flag", queueAt); !errors.Is(err, dispatcher.ErrReplyUnusable) {
		t.Fatalf("a settled decision: err = %v, want an unusable reply", err)
	}
}

func TestPullNumberReadsThePullRequestURL(t *testing.T) {
	for url, want := range map[string]int{
		"https://github.com/o/r/pull/141": 141,
		"":                                0,
		"https://github.com/o/r/pull/x":   0,
		"https://github.com/o/r/issues/4": 0,
		"https://github.com/o/r/pull/-3":  0,
	} {
		if got := pullNumber(url); got != want {
			t.Errorf("pullNumber(%q) = %d, want %d", url, got, want)
		}
	}
}

func TestProvisionalRepliedListsAReplyWhateverTheClocksSay(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	row := readDecisions(t, client, id)
	row.Decisions[0].Reply, row.Decisions[0].RepliedAt = "new", row.Decisions[0].PostedAt.Add(-time.Hour)
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{
		{Path: decisionsField, Value: row.Decisions},
		{Path: provisionalRepliedField, Value: true},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := q.ProvisionalReplied(ctx)
	if err != nil || !slices.ContainsFunc(got, func(p dispatcher.ProvisionalRun) bool { return p.RunID == id }) {
		t.Fatalf("a reply stamped before the post by a skewed clock is not listed: %+v, %v", got, err)
	}
}

func TestProvisionalRepliedSkipsAMarkedRunWithNoReply(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{
		{Path: provisionalRepliedField, Value: true},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := q.ProvisionalReplied(ctx)
	if err != nil || slices.ContainsFunc(got, func(p dispatcher.ProvisionalRun) bool { return p.RunID == id }) {
		t.Fatalf("a run with no reply is listed: %+v, %v", got, err)
	}
}

func TestReplyLandsOnTheNewestPostedDecisionNotAnUnpostedOne(t *testing.T) {
	q, client := queue(t)
	id := fresh("decision-two")
	seedRow(t, client, id, map[string]any{
		stateField: stateStopped, outcomeField: string(runner.OutcomeBuilt),
		decisionsField: []decisionEntry{
			{Decision: storeDecision, SessionID: "session-1", PostedAt: queueAt, Answer: storeDecision.Options[0].Label, Provisional: true},
			{Decision: storeDecision, Answer: storeDecision.Options[0].Label, Provisional: true},
		},
	})
	if recorded, err := q.Reply(context.Background(), id, "session-1", "new", queueAt.Add(time.Minute)); err != nil || !recorded {
		t.Fatalf("a reply to the posted question was dropped: %v, %v", recorded, err)
	}
	row := readDecisions(t, client, id)
	if row.Decisions[0].Reply != "new" || row.Decisions[1].Reply != "" {
		t.Fatalf("replies = %q and %q, want the posted decision's only", row.Decisions[0].Reply, row.Decisions[1].Reply)
	}
}

func TestSupersedeRefusesAnAbandonedRunAndAWaitingOneAsUnusable(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	for name, state := range map[string]string{"abandoned": stateAbandoned, "waiting": stateWaiting} {
		id := postedProvisionalRun(t, q, client)
		if _, err := q.Reply(ctx, id, "session-1", "new", queueAt.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		endRun(t, client, id, "https://github.com/o/r/pull/141")
		if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{{Path: stateField, Value: state}}); err != nil {
			t.Fatal(err)
		}
		err := q.Supersede(ctx, id, 1, repliedAtOf(t, client, id, 1), "Add a new flag", queueAt.Add(5*time.Minute))
		if !errors.Is(err, dispatcher.ErrReplyUnusable) {
			t.Fatalf("%s run: err = %v, want an unusable reply", name, err)
		}
		if data := readRow(t, client, id); data[stateField] != state || data[attemptField] != nil && data[attemptField] != int64(0) {
			t.Fatalf("%s run: state %v, attempt %v, want it left as it was", name, data[stateField], data[attemptField])
		}
	}
}

func TestSupersedeAndConfirmLeaveAReplyTheOwnerHasSinceReplaced(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := postedProvisionalRun(t, q, client)
	first := queueAt.Add(2 * time.Minute)
	if _, err := q.Reply(ctx, id, "session-1", "new", first); err != nil {
		t.Fatal(err)
	}
	endRun(t, client, id, "https://github.com/o/r/pull/141")
	if _, err := q.Reply(ctx, id, "session-1", "reuse", queueAt.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := q.Supersede(ctx, id, 1, first, "Add a new flag", queueAt.Add(5*time.Minute)); err != nil {
		t.Fatalf("a stale supersede is a no-op, got %v", err)
	}
	if err := q.ConfirmProvisional(ctx, id, 1, first, queueAt.Add(5*time.Minute)); err != nil {
		t.Fatalf("a stale confirm is a no-op, got %v", err)
	}
	data := readRow(t, client, id)
	row := readDecisions(t, client, id)
	if data[stateField] != stateStopped || !row.Decisions[0].Provisional || !row.Decisions[0].ConfirmedAt.IsZero() {
		t.Fatalf("a stale reply acted: state %v, decision %+v", data[stateField], row.Decisions[0])
	}
	if marked, _ := data[provisionalRepliedField].(bool); !marked {
		t.Fatal("the newer reply lost its mark")
	}
}

func TestConfirmProvisionalLeavesAGuidedDecisionAndClearsTheRepliedMark(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	guided := parkedRun(t, q, client)
	if err := q.Posted(ctx, guided, "session-1", queueAt); err != nil {
		t.Fatal(err)
	}
	if err := q.Answer(ctx, guided, "Add a new flag", queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := q.ConfirmProvisional(ctx, guided, 1, repliedAtOf(t, client, guided, 1), queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if row := readDecisions(t, client, guided); !row.Decisions[0].ConfirmedAt.IsZero() {
		t.Fatalf("a guided decision was confirmed: %+v", row.Decisions[0])
	}
	id := postedProvisionalRun(t, q, client)
	if _, err := q.Reply(ctx, id, "session-1", "reuse", queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := q.ConfirmProvisional(ctx, id, 1, repliedAtOf(t, client, id, 1), queueAt.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, marked := readRow(t, client, id)[provisionalRepliedField]; marked {
		t.Fatal("the replied mark survived the confirmation")
	}
}
