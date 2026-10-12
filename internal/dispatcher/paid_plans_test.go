package dispatcher

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// nextPlan is a second paid plan with its own 5-hour window.
func nextPlan(plan string) PaidPlan {
	return PaidPlan{
		Plan: plan,
		Budget: BudgetConfig{
			ProviderWindows: []Window{{Name: plan + "-5h", Period: 5 * time.Hour, Limit: 9 * money.Dollar}},
			Cash:            paidPlan.Budget.Cash,
			Runner:          paidPlan.Budget.Runner,
		},
		Models: runner.ModelLabels{Build: plan + "/paid", Review: plan + "/review", Plan: []string{plan + "/paid"}},
	}
}

// walkConfig is paidPlan with q, then r, to move to.
func walkConfig() Config {
	c := paidPlan
	c.NextPlans = []PaidPlan{nextPlan("q"), nextPlan("r")}
	return c
}

// usableDeps is pollDeps with every plan recorded as billing a flat allowance.
func usableDeps(q *fakeQueue, w *fakeWorkflow) Deps {
	deps := pollDeps(fakeSource{}, q, w)
	deps.ModelPlans = &fakeModelPlans{plans: map[string]providers.Plan{
		"q": planBilled(providers.BillingAllowance, false),
		"r": planBilled(providers.BillingAllowance, false),
	}}
	return deps
}

func TestPollMovesARunToTheNextPaidPlanOnceTheFirstBinds(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		planBindings: map[string]string{"p": "p-5h"},
	}
	w := &fakeWorkflow{}
	deps := usableDeps(q, w)
	deps.Plans = fakeVerdicts{verdicts: map[string]providers.Verdict{"p": providers.VerdictAllowed, "q": providers.VerdictRestricted}}
	res, err := Poll(context.Background(), deps, walkConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) || len(res.Deferrals) != 0 || len(q.tryClaims) != 2 {
		t.Fatalf("dispatched %q, deferred %+v after %d claims, want the run on the second plan", res.Dispatched, res.Deferrals, len(q.tryClaims))
	}
	moved := q.tryClaims[1]
	want := &Move{From: "p", Models: runner.ModelLabels{Build: "q/paid", Review: "q/review", Plan: []string{"q/paid"}}}
	if moved.facts.Model != "q/paid" || !reflect.DeepEqual(moved.facts.Moved, want) || moved.facts.LastResort != nil {
		t.Fatalf("facts = %+v, want the move from p onto q's models", moved.facts)
	}
	if !reflect.DeepEqual(moved.cfg, nextPlan("q").Budget) || moved.cfg.ProviderFree {
		t.Fatalf("cfg = %+v, want the claim judged on q's own ceilings", moved.cfg)
	}
	if q.tryClaims[0].facts.Moved != nil {
		t.Fatalf("first claim facts = %+v, want no move recorded on the plan the run starts on", q.tryClaims[0].facts)
	}
	if w.claims[0].Verdict != providers.VerdictRestricted {
		t.Fatalf("verdict = %q, want the plan moved to's", w.claims[0].Verdict)
	}
}

func TestPollMovesPastEveryPaidPlanWhoseCeilingBinds(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		planBindings: map[string]string{"p": "p-5h", "q": "q-5h"},
	}
	if _, err := Poll(context.Background(), usableDeps(q, &fakeWorkflow{}), walkConfig()); err != nil {
		t.Fatal(err)
	}
	last := q.tryClaims[len(q.tryClaims)-1]
	if len(q.tryClaims) != 3 || last.facts.Model != "r/paid" || last.facts.Moved == nil || last.facts.Moved.From != "q" {
		t.Fatalf("tryClaims = %+v, want a third claim on r after q's ceiling bound", q.tryClaims)
	}
}

func TestPollFallsToTheFreeTierWhenEveryPaidPlanBinds(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		planBindings: map[string]string{"p": "p-5h", "q": "q-5h", "r": "r-5h"},
	}
	res, err := Poll(context.Background(), usableDeps(q, &fakeWorkflow{}), walkConfig())
	if err != nil {
		t.Fatal(err)
	}
	free := q.tryClaims[len(q.tryClaims)-1]
	if len(q.tryClaims) != 4 || !free.cfg.ProviderFree || free.facts.Model != "p/free-a" || free.facts.Moved != nil {
		t.Fatalf("tryClaims = %+v, want the free tier after all three paid plans, with no move recorded", q.tryClaims)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) {
		t.Fatalf("dispatched %q, want the run on the free tier", res.Dispatched)
	}
}

func TestPollDefersWhenTheNextPaidPlanIsBoundByAnotherCeiling(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		planBindings: map[string]string{"p": "p-5h", "q": CeilingCash},
	}
	res, err := Poll(context.Background(), usableDeps(q, &fakeWorkflow{}), walkConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 2 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != CeilingCash {
		t.Fatalf("claims %d, deferrals %+v, want the run deferred on cash with no further plan tried", len(q.tryClaims), res.Deferrals)
	}
}

func TestPollMovesToAPerTokenPlanOnlyWithTheOwnersOptIn(t *testing.T) {
	for name, tt := range map[string]struct {
		plan     providers.Plan
		present  bool
		movedToQ bool
	}{
		"opted in":         {plan: planBilled(providers.BillingPerToken, true), present: true, movedToQ: true},
		"not opted in":     {plan: planBilled(providers.BillingPerToken, false), present: true},
		"no plan record":   {},
		"a flat allowance": {plan: planBilled(providers.BillingAllowance, false), present: true, movedToQ: true},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{
				candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
				planBindings: map[string]string{"p": "p-5h"},
			}
			deps := usableDeps(q, &fakeWorkflow{})
			plans := map[string]providers.Plan{}
			if tt.present {
				plans["q"] = tt.plan
			}
			deps.ModelPlans = &fakeModelPlans{plans: plans}
			cfg := walkConfig()
			cfg.NextPlans = cfg.NextPlans[:1]
			if _, err := Poll(context.Background(), deps, cfg); err != nil {
				t.Fatal(err)
			}
			onQ := slices.ContainsFunc(q.tryClaims, func(c tryClaimCall) bool { return c.facts.Model == "q/paid" })
			if onQ != tt.movedToQ {
				t.Fatalf("claimed on q = %v, want %v (claims %+v)", onQ, tt.movedToQ, q.tryClaims)
			}
			if last := q.tryClaims[len(q.tryClaims)-1]; !tt.movedToQ && !last.cfg.ProviderFree {
				t.Fatalf("last claim = %+v, want the free tier once the move is refused", last)
			}
		})
	}
}

func TestPollSkipsAHaltedPaidPlanAndTriesTheNext(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		planBindings: map[string]string{"p": "p-5h"},
	}
	deps := usableDeps(q, &fakeWorkflow{})
	deps.Providers = fakeProviders{halted: map[string]bool{"q": true}}
	if _, err := Poll(context.Background(), deps, walkConfig()); err != nil {
		t.Fatal(err)
	}
	if last := q.tryClaims[len(q.tryClaims)-1]; len(q.tryClaims) != 2 || last.facts.Model != "r/paid" {
		t.Fatalf("tryClaims = %+v, want q skipped as halted and r claimed", q.tryClaims)
	}
}

func TestPollKeepsATicketsNamedModelOnItsOwnPlan(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch", Model: "p/other", ModelsNamed: true}},
		planBindings: map[string]string{"p": "p-5h"},
	}
	if _, err := Poll(context.Background(), usableDeps(q, &fakeWorkflow{}), walkConfig()); err != nil {
		t.Fatal(err)
	}
	for _, c := range q.tryClaims {
		if c.facts.Moved != nil {
			t.Fatalf("tryClaims = %+v, want a ticket that named a model never moved", q.tryClaims)
		}
	}
}

func TestPollJudgesAPlannedRunByThePlanItsPlanStageClaimed(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch", Stage: StageBuild, Plan: "q", PlanFiles: []string{"a.go"}}},
		planBindings: map[string]string{"p": "p-5h", "q": "q-5h"},
	}
	cfg := walkConfig()
	cfg.TwoStage = true
	res, err := Poll(context.Background(), usableDeps(q, &fakeWorkflow{}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || !reflect.DeepEqual(q.tryClaims[0].cfg, nextPlan("q").Budget) || q.tryClaims[0].facts.Model != "q/paid" {
		t.Fatalf("tryClaims = %+v, want one claim on q's budget and build model", q.tryClaims)
	}
	if len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "q-5h" {
		t.Fatalf("deferrals = %+v, want the run deferred on q's own window", res.Deferrals)
	}
}

func TestPollJudgesAPlannedFreeTierRunOnTheFreeTier(t *testing.T) {
	free := runner.ModelLabels{Build: "p/free-a", Review: "p/free-b", Plan: []string{"p/free-a", "p/free-b"}}
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Plan: "p", LastResort: &free, PlanFiles: []string{"a.go"}}},
		planBindings: map[string]string{"p": "p-5h"},
	}
	deps := usableDeps(q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}}}
	cfg := walkConfig()
	cfg.TwoStage = true
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := q.tryClaims[0]
	if len(q.tryClaims) != 1 || !got.cfg.ProviderFree || got.facts.Model != "p/free-a" || got.res.ProviderCost != 0 {
		t.Fatalf("tryClaims = %+v, want one free-tier claim reserving no provider cost", q.tryClaims)
	}
	if got.res.Stage != StageBuild || !slices.Equal(got.res.Files, []string{"a.go"}) || len(res.Dispatched) != 1 {
		t.Fatalf("reservation = %+v, dispatched %q, want the build stage on its planned write set", got.res, res.Dispatched)
	}
}

func TestPollJudgesAPlannedRunWhosePlanIsUnknownByTheDefaultPlan(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Stage: StageBuild, Plan: "gone"}, {RunID: "run-b", Stage: StageBuild}},
		planBindings: map[string]string{"q": "q-5h"},
	}
	cfg := walkConfig()
	cfg.TwoStage = true
	if _, err := Poll(context.Background(), usableDeps(q, &fakeWorkflow{}), cfg); err != nil {
		t.Fatal(err)
	}
	for _, c := range q.tryClaims {
		if !reflect.DeepEqual(c.cfg, paidPlan.Budget) || c.facts.Model != "p/paid" {
			t.Fatalf("claim = %+v, want the default plan's budget and model", c)
		}
	}
}
