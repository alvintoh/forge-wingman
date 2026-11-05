package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// tightBudget has a $5 plan window and a $5 cap on alpha/model, with cash and
// runner minutes out of the way.
var tightBudget = dispatcher.BudgetConfig{
	ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 5 * money.Dollar}},
	ModelCaps:       map[string]dispatcher.Window{"alpha/model": {Name: "alpha/model", Period: 24 * time.Hour, Limit: 5 * money.Dollar}},
	Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
	Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
}

// settleOn records a $4 settled run inside the window, on plan, or on no plan
// when plan is empty, as a run settled before plans were recorded is.
func settleOn(t *testing.T, client *firestore.Client, id, plan string) {
	t.Helper()
	fields := map[string]any{settledAtField: queueAt, settledProviderCostField: int64(4 * money.Dollar)}
	if plan != "" {
		fields[planField] = plan
	}
	seedRow(t, client, id, fields)
}

func TestTryClaimBindsARunOnlyByItsOwnPlansSpend(t *testing.T) {
	for name, tt := range map[string]struct {
		settledOn string
		model     string
		binding   string
	}{
		"spend on another plan":            {settledOn: "alpha", model: "beta/model"},
		"spend on its own plan":            {settledOn: "alpha", model: "alpha/model", binding: "5h"},
		"spend before plans were recorded": {model: "beta/model"},
		"legacy spend on the default plan": {model: runner.DefaultModel(), binding: "5h"},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			ctx := context.Background()
			settled, candidate := fresh("queue-plan-settled"), queuedRun(fresh("queue-plan-candidate"), 1)
			forget(t, client, candidate.RunID)
			resetLedger(t, client, 1)
			settleOn(t, client, settled, tt.settledOn)
			if err := q.Enqueue(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			facts := openFacts
			facts.Model = tt.model
			ok, binding, err := q.TryClaim(ctx, candidate.RunID, queueAt.Add(time.Minute), tightBudget, facts,
				dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
			if err != nil {
				t.Fatal(err)
			}
			if ok != (tt.binding == "") || binding != tt.binding {
				t.Fatalf("ok = %v, binding = %q, want binding %q", ok, binding, tt.binding)
			}
		})
	}
}

func TestTryClaimBindsAModelCapOnlyByItsOwnPlansSpend(t *testing.T) {
	capOnly := tightBudget
	capOnly.ProviderWindows = nil
	for name, tt := range map[string]struct {
		settledOn string
		binding   string
	}{
		"spend on another plan": {settledOn: "beta"},
		"spend on its own plan": {settledOn: "alpha", binding: "alpha/model"},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			ctx := context.Background()
			settled, candidate := fresh("queue-cap-settled"), queuedRun(fresh("queue-cap-candidate"), 1)
			forget(t, client, candidate.RunID)
			resetLedger(t, client, 1)
			settleOn(t, client, settled, tt.settledOn)
			if err := q.Enqueue(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			facts := openFacts
			facts.Model = "alpha/model"
			ok, binding, err := q.TryClaim(ctx, candidate.RunID, queueAt.Add(time.Minute), capOnly, facts,
				dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
			if err != nil {
				t.Fatal(err)
			}
			if ok != (tt.binding == "") || binding != tt.binding {
				t.Fatalf("ok = %v, binding = %q, want binding %q", ok, binding, tt.binding)
			}
		})
	}
}

func TestTryClaimBindsARunOnlyByItsOwnPlansPlanStageSpend(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	planned, candidate := fresh("queue-plan-stage"), queuedRun(fresh("queue-plan-stage-candidate"), 1)
	forget(t, client, candidate.RunID)
	resetLedger(t, client, 1)
	seedRow(t, client, planned, map[string]any{planField: "alpha", planSettledAtField: queueAt, planSpendMicrosField: int64(4 * money.Dollar)})
	if err := q.Enqueue(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.Model = "beta/model"
	if ok, binding, err := q.TryClaim(ctx, candidate.RunID, queueAt.Add(time.Minute), tightBudget, facts,
		dispatcher.Reservation{ProviderCost: 2 * money.Dollar}); err != nil || !ok {
		t.Fatalf("ok = %v, binding = %q, err = %v, want another plan's plan stage not to bind", ok, binding, err)
	}
	if err := q.Release(ctx, candidate.RunID, queueAt); err != nil {
		t.Fatal(err)
	}
	facts.Model = "alpha/other"
	if ok, binding, err := q.TryClaim(ctx, candidate.RunID, queueAt.Add(time.Minute), tightBudget, facts,
		dispatcher.Reservation{ProviderCost: 2 * money.Dollar}); err != nil || ok || binding != "5h" {
		t.Fatalf("ok = %v, binding = %q, err = %v, want the plan's own plan stage to bind", ok, binding, err)
	}
}

func TestTryClaimCountsEveryPlansSpendTowardCash(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	settled, candidate := fresh("queue-cash-settled"), queuedRun(fresh("queue-cash-candidate"), 1)
	forget(t, client, candidate.RunID)
	resetLedger(t, client, 1)
	settleOn(t, client, settled, "alpha")
	if err := q.Enqueue(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	perToken := dispatcher.BudgetConfig{
		Cash:   dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 5 * money.Dollar},
		Runner: tightBudget.Runner,
	}
	facts := openFacts
	facts.Model = "beta/model"
	ok, binding, err := q.TryClaim(ctx, candidate.RunID, queueAt.Add(time.Minute), perToken, facts, dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
	if err != nil || ok || binding != dispatcher.CeilingCash {
		t.Fatalf("ok = %v, binding = %q, err = %v, want another plan's spend to bind cash", ok, binding, err)
	}
}

func TestTryClaimLeavesTheTierAPlanStageChoseOnABuildStageClaim(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-build-keeps"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 2)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.Model = "beta/build"
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{Stage: dispatcher.StagePlan}); err != nil || !ok {
		t.Fatalf("plan stage ok = %v, err = %v", ok, err)
	}
	if err := q.Release(ctx, run.RunID, queueAt); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{
		{Path: stateField, Value: statePlanned},
	}); err != nil {
		t.Fatal(err)
	}
	facts.Model = "alpha/build"
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{Stage: dispatcher.StageBuild}); err != nil || !ok {
		t.Fatalf("build stage ok = %v, err = %v", ok, err)
	}
	rec, err := NewRecords(client).GetRecord(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Plan != "beta" {
		t.Fatalf("plan %q, want the plan stage's to stand", rec.Plan)
	}
}

func TestCandidatesReadsThePlanAndNamedModelsARowRecords(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	paid, named := queuedRun(fresh("queue-cand-paid"), 1), queuedRun(fresh("queue-cand-named"), 2)
	named.Models = runner.ModelLabels{Review: "gamma/review", Plan: []string{"gamma/plan"}}
	forget(t, client, paid.RunID, named.RunID)
	resetLedger(t, client, 5)
	for _, run := range []dispatcher.Queued{paid, named} {
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	paidFacts, namedFacts := openFacts, openFacts
	paidFacts.Model, namedFacts.Model = "alpha/build", "beta/build"
	for run, facts := range map[string]dispatcher.Facts{paid.RunID: paidFacts, named.RunID: namedFacts} {
		if ok, _, err := q.TryClaim(ctx, run, queueAt, generousBudget, facts, dispatcher.Reservation{}); err != nil || !ok {
			t.Fatalf("claim %s ok = %v, err = %v", run, ok, err)
		}
		if err := q.Release(ctx, run, queueAt); err != nil {
			t.Fatal(err)
		}
	}
	writeLegacyClaimFields(t, client, named.RunID)
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]dispatcher.Candidate{}
	for _, c := range cands {
		found[c.RunID] = c
	}
	if c := found[paid.RunID]; c.Plan != "alpha" || c.ModelsNamed {
		t.Fatalf("paid candidate = %+v, want plan alpha, no model named", c)
	}
	if c := found[named.RunID]; c.Plan != "beta" || c.Model != "" || !c.ModelsNamed || c.ReviewModel != "gamma/review" || !slices.Equal(c.PlanModels, []string{"gamma/plan"}) {
		t.Fatalf("named candidate = %+v, want plan beta and only the review and plan models its ticket named", c)
	}
}
