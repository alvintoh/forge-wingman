package dispatcher

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// laterPlan is a paid plan listed after the first, with its own 5-hour window.
func laterPlan(plan string) PaidPlan {
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

// laterPlansConfig is paidPlan with q, then r, listed after it.
func laterPlansConfig() Config {
	c := paidPlan
	c.PlanBudgets = []PaidPlan{laterPlan("q"), laterPlan("r")}
	return c
}

func TestPollHoldsARunOnItsPlanOnceThePaidCeilingBinds(t *testing.T) {
	for name, tt := range map[string]struct {
		ceiling string
		private bool
	}{
		"a plan window":                    {ceiling: "p-5h"},
		"a model cap":                      {ceiling: "p/paid"},
		"a private run the owner opted in": {ceiling: "p-5h", private: true},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{
				candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M", Private: tt.private}},
				bindings:   map[string]string{"run-a": tt.ceiling},
			}
			holds := &fakeHolds{}
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.Holds = holds
			deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}}}
			deps.ModelPlans = &fakeModelPlans{plans: map[string]providers.Plan{"p": {PrivateOptIn: true}}}
			res, err := Poll(context.Background(), deps, laterPlansConfig())
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Dispatched) != 0 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != tt.ceiling {
				t.Fatalf("dispatched %q, deferred %+v, want the run held on %s", res.Dispatched, res.Deferrals, tt.ceiling)
			}
			if len(q.tryClaims) != 1 || q.tryClaims[0].cfg.ProviderFree {
				t.Fatalf("tryClaims = %+v, want one claim on the paid plan and none on a free model", q.tryClaims)
			}
			if len(holds.recorded) != 1 || holds.recorded[0].reason != HoldBudget {
				t.Fatalf("holds = %+v, want the run held as %s", holds.recorded, HoldBudget)
			}
		})
	}
}

func TestPollNeverClaimsOnAPaidPlanListedAfterTheFirst(t *testing.T) {
	for name, bindings := range map[string]map[string]string{
		"no ceiling binding":       nil,
		"the first plan's binding": {"p": "p-5h"},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch"}}, planBindings: bindings}
			res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), laterPlansConfig())
			if err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 || !reflect.DeepEqual(q.tryClaims[0].cfg, paidPlan.Budget) || q.tryClaims[0].facts.Model != "p/paid" {
				t.Fatalf("tryClaims = %+v, want one claim on the first plan's budget and model", q.tryClaims)
			}
			if admitted := bindings == nil; (len(res.Dispatched) == 1) != admitted {
				t.Fatalf("dispatched %q, deferred %+v, want admitted = %v", res.Dispatched, res.Deferrals, admitted)
			}
		})
	}
}

func TestPollAdmitsAsBeforeWhenNoCeilingBinds(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}}}
	res, err := Poll(context.Background(), deps, laterPlansConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) || len(res.Deferrals) != 0 {
		t.Fatalf("dispatched %q, deferred %+v, want the run admitted", res.Dispatched, res.Deferrals)
	}
	got := q.tryClaims[0]
	if len(q.tryClaims) != 1 || got.res.ProviderCost != 2*money.Dollar || got.cfg.ProviderFree {
		t.Fatalf("tryClaims = %+v, want one paid claim reserving the estimate", q.tryClaims)
	}
}

func TestPollHoldsATicketsNamedPaidModelLikeAnyOther(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch", Model: "p/other", ModelsNamed: true}},
		planBindings: map[string]string{"p": "p-5h"},
	}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), laterPlansConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || q.tryClaims[0].facts.Model != "p/other" || q.tryClaims[0].cfg.ProviderFree {
		t.Fatalf("tryClaims = %+v, want one claim on the named model", q.tryClaims)
	}
	if len(res.Dispatched) != 0 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "p-5h" {
		t.Fatalf("dispatched %q, deferred %+v, want the run held on the plan window", res.Dispatched, res.Deferrals)
	}
}

func TestPollJudgesAPlannedRunByThePlanItsPlanStageClaimed(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Repo: "octo/scratch", Stage: StageBuild, Plan: "q", PlanFiles: []string{"a.go"}}},
		planBindings: map[string]string{"p": "p-5h", "q": "q-5h"},
	}
	cfg := laterPlansConfig()
	cfg.TwoStage = true
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || !reflect.DeepEqual(q.tryClaims[0].cfg, laterPlan("q").Budget) || q.tryClaims[0].facts.Model != "q/paid" {
		t.Fatalf("tryClaims = %+v, want one claim on q's budget and build model", q.tryClaims)
	}
	if len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "q-5h" {
		t.Fatalf("deferrals = %+v, want the run deferred on q's own window", res.Deferrals)
	}
}

func TestPollJudgesAPlannedRunWhosePlanIsUnknownByTheDefaultPlan(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Stage: StageBuild, Plan: "gone"}, {RunID: "run-b", Stage: StageBuild}},
		planBindings: map[string]string{"q": "q-5h"},
	}
	cfg := laterPlansConfig()
	cfg.TwoStage = true
	if _, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), cfg); err != nil {
		t.Fatal(err)
	}
	for _, c := range q.tryClaims {
		if !reflect.DeepEqual(c.cfg, paidPlan.Budget) || c.facts.Model != "p/paid" {
			t.Fatalf("claim = %+v, want the default plan's budget and model", c)
		}
	}
}

func TestPollAdmitsATicketNamingOnlyFreeModelsWhileThePaidCeilingBinds(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M", ModelsNamed: true,
			Model: "p/free-b", ReviewModel: "p/free-a", PlanModels: []string{"p/free-b"}}},
		planBindings: map[string]string{"p": "p-5h"},
	}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}}}
	res, err := Poll(context.Background(), deps, laterPlansConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) || len(res.Deferrals) != 0 {
		t.Fatalf("dispatched %q, deferred %+v, want the named free model admitted", res.Dispatched, res.Deferrals)
	}
	got := q.tryClaims[0]
	if len(q.tryClaims) != 1 || !got.cfg.ProviderFree || got.res.ProviderCost != 0 || got.facts.Model != "p/free-b" {
		t.Fatalf("tryClaims = %+v, want one free-tier claim on the named model at no provider cost", q.tryClaims)
	}
}

func TestPollJudgesTheBuildOfANamedFreeModelOnTheFreeTier(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Plan: "p", ModelsNamed: true,
			Model: "p/free-b", ReviewModel: "p/free-a", PlanModels: []string{"p/free-b"}, PlanFiles: []string{"a.go"}}},
		planBindings: map[string]string{"p": "p-5h"},
	}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}}}
	cfg := laterPlansConfig()
	cfg.TwoStage = true
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := q.tryClaims[0]
	if len(q.tryClaims) != 1 || !got.cfg.ProviderFree || got.res.ProviderCost != 0 || got.res.Stage != StageBuild || len(res.Dispatched) != 1 {
		t.Fatalf("tryClaims = %+v, dispatched %q, want the build claimed on the free tier at no provider cost", q.tryClaims, res.Dispatched)
	}
}

func TestPollAdmitsAPrivateNamedFreeModelOnlyWithThePlansPrivateOptIn(t *testing.T) {
	for name, tt := range map[string]struct {
		private  bool
		plans    map[string]providers.Plan
		readErr  error
		admitted bool
	}{
		"private, opted in":      {private: true, plans: map[string]providers.Plan{"p": {PrivateOptIn: true}}, admitted: true},
		"private, not opted in":  {private: true, plans: map[string]providers.Plan{"p": {}}},
		"private, lookup failed": {private: true, readErr: errors.New("unavailable")},
		"public":                 {admitted: true},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "S", ModelsNamed: true,
				Model: "p/free-a", ReviewModel: "p/free-b", Private: tt.private}}}
			holds := &fakeHolds{}
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.ModelPlans = &fakeModelPlans{plans: tt.plans, err: tt.readErr}
			deps.Holds = holds
			res, err := Poll(context.Background(), deps, laterPlansConfig())
			if err != nil {
				t.Fatal(err)
			}
			if tt.admitted {
				if len(res.Dispatched) != 1 {
					t.Fatalf("dispatched %q, deferred %+v, want the run admitted", res.Dispatched, res.Deferrals)
				}
				return
			}
			if len(q.tryClaims) != 0 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != ConditionPrivateOptIn {
				t.Fatalf("claims %d, deferred %+v, want the run held for the private opt-in", len(q.tryClaims), res.Deferrals)
			}
			if len(holds.recorded) != 1 || holds.recorded[0].reason != HoldPrivateOptIn {
				t.Fatalf("holds = %+v, want the run held as %s", holds.recorded, HoldPrivateOptIn)
			}
		})
	}
}

// TestPollClaimsAPlanStageOnTheDefaultPlanWhateverTheRowRecords asserts a
// plan-stage claim is judged on the default plan even when its row still
// records the plan of an earlier claim, as a requeued run's does, so a run is
// never put back on another plan without the owner naming one.
func TestPollClaimsAPlanStageOnTheDefaultPlanWhateverTheRowRecords(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M", Plan: "q"}}}
	cfg := laterPlansConfig()
	cfg.TwoStage = true
	if _, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), cfg); err != nil {
		t.Fatal(err)
	}
	got := q.tryClaims[0]
	if len(q.tryClaims) != 1 || got.res.Stage != StagePlan || !reflect.DeepEqual(got.cfg, paidPlan.Budget) || got.facts.Model != "p/paid" {
		t.Fatalf("tryClaims = %+v, want one plan-stage claim on the default plan's budget and model", q.tryClaims)
	}
}

func TestPollBuildsATicketsNamedModelOnThePlanItsPlanStageClaimed(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Stage: StageBuild, Plan: "q", Model: "q/other", ModelsNamed: true, PlanFiles: []string{"a.go"}}}}
	cfg := laterPlansConfig()
	cfg.TwoStage = true
	if _, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), cfg); err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || !reflect.DeepEqual(q.tryClaims[0].cfg, laterPlan("q").Budget) || q.tryClaims[0].facts.Model != "q/other" {
		t.Fatalf("tryClaims = %+v, want one claim on q's budget building the named model", q.tryClaims)
	}
}

func TestPollJudgesATicketOnThePaidBudgetUnlessEveryModelItUsesIsFree(t *testing.T) {
	for name, cand := range map[string]Candidate{
		"a free build with the paid default review":        {Size: "S", Model: "p/free-a"},
		"a free build and review with the paid plan model": {Size: "M", Model: "p/free-a", ReviewModel: "p/free-b"},
	} {
		t.Run(name, func(t *testing.T) {
			cand.RunID, cand.Repo, cand.ModelsNamed = "run-a", "octo/scratch", true
			q := &fakeQueue{candidates: []Candidate{cand}, planBindings: map[string]string{"p": "p-5h"}}
			res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), laterPlansConfig())
			if err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 || q.tryClaims[0].cfg.ProviderFree {
				t.Fatalf("tryClaims = %+v, want one claim on the paid budget", q.tryClaims)
			}
			if len(res.Dispatched) != 0 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "p-5h" {
				t.Fatalf("dispatched %q, deferred %+v, want the run held on the plan window", res.Dispatched, res.Deferrals)
			}
		})
	}
}

func TestPollChecksThePrivateOptInOfEveryNamedFreeModelsOwnPlan(t *testing.T) {
	for name, tt := range map[string]struct {
		cand  Candidate
		plans map[string]providers.Plan
	}{
		"a paid build and a free review not opted in": {
			cand:  Candidate{Size: "S", Model: "p/paid", ReviewModel: "p/free-a"},
			plans: map[string]providers.Plan{"p": {}},
		},
		"free models on two plans, one not opted in": {
			cand:  Candidate{Size: "S", Model: "p/free-a", ReviewModel: "q/free-a"},
			plans: map[string]providers.Plan{"p": {PrivateOptIn: true}, "q": {}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cand := tt.cand
			cand.RunID, cand.Repo, cand.ModelsNamed, cand.Private = "run-a", "octo/scratch", true, true
			q := &fakeQueue{candidates: []Candidate{cand}}
			holds := &fakeHolds{}
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.ModelPlans = &fakeModelPlans{plans: tt.plans}
			deps.Holds = holds
			cfg := laterPlansConfig()
			cfg.LastResortPlans = [][]string{{"p/free-a", "p/free-b"}, {"q/free-a"}}
			res, err := Poll(context.Background(), deps, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 0 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != ConditionPrivateOptIn {
				t.Fatalf("claims %d, deferred %+v, want the run held for the private opt-in", len(q.tryClaims), res.Deferrals)
			}
			if len(holds.recorded) != 1 || holds.recorded[0].reason != HoldPrivateOptIn {
				t.Fatalf("holds = %+v, want the run held as %s", holds.recorded, HoldPrivateOptIn)
			}
		})
	}
}
