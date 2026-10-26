package dispatcher

import (
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
)

var admitLimits = Limits{PlatformCap: 5, LargeCap: 1, ReviewWIP: 3, RepoCap: 5}

// admittable is a candidate every condition lets start, with two other runs in
// flight and room under each limit; each case breaks exactly one condition.
func admittable() AdmitInput {
	return AdmitInput{
		Facts: Facts{Limits: admitLimits, OpenPRs: 1, OpenPRsKnown: true},
		N:     3,
		InFlight: []InFlight{
			{Ticket: "run-1", Repo: "octo/one", Size: "M"},
			{Ticket: "run-2", Repo: "octo/two", Size: "L"},
		},
		Candidate: Subject{Ticket: "run-9", Repo: "octo/scratch", Size: "S"},
		Budget: BudgetConfig{
			Cash: Window{Name: CeilingCash, Limit: 10 * money.Dollar},
		},
		Reservation: Reservation{ProviderCost: money.Dollar},
	}
}

func TestAdmitLetsACandidateStartWhenNoConditionBinds(t *testing.T) {
	if ok, binding := Admit(admittable()); !ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q", ok, binding)
	}
}

func TestAdmitNamesTheConditionThatWithholdsACandidate(t *testing.T) {
	for name, tt := range map[string]struct {
		break_ func(*AdmitInput)
		want   string
	}{
		"the provider is halted":       {func(in *AdmitInput) { in.Facts.ProviderHalted = true }, CeilingProviderHalted},
		"the owner paused the project": {func(in *AdmitInput) { in.Facts.OwnerPaused = true }, ConditionOwnerPaused},
		"the platform cap is reached": {func(in *AdmitInput) {
			in.Facts.Limits.PlatformCap = 2
			in.N = 10
		}, ConditionPlatformCap},
		"N runs are in flight":    {func(in *AdmitInput) { in.N = 2 }, ConditionConcurrency},
		"an unset N reads as one": {func(in *AdmitInput) { in.N = 0; in.InFlight = in.InFlight[:1] }, ConditionConcurrency},
		"the large cap is reached": {func(in *AdmitInput) {
			in.Candidate.Size = largeSize
		}, ConditionLargeCap},
		"the repository already has its cap of runs": {func(in *AdmitInput) {
			in.Facts.Limits.RepoCap = 1
			in.Candidate.Repo = "octo/two"
		}, ConditionRepoCap},
		"a plan-stage run holds one of the repository's slots": {func(in *AdmitInput) {
			in.Facts.Limits.RepoCap = 1
			in.Candidate.Repo = "octo/two"
			in.InFlight[1].Stage = StagePlan
		}, ConditionRepoCap},
		"a planned write set overlaps one in flight": {func(in *AdmitInput) {
			in.Candidate.Repo, in.Candidate.Stage, in.Candidate.Files =
				"octo/two", StageBuild, []string{"a.go", "b.go"}
			in.InFlight[1].Stage, in.InFlight[1].Files = StageBuild, []string{"b.go"}
		}, ConditionRepoOverlap},
		"it is blocked by a run in flight": {func(in *AdmitInput) { in.Candidate.BlockedBy = []string{"run-1"} }, ConditionBlocked},
		"it blocks a run in flight":        {func(in *AdmitInput) { in.Candidate.Blocks = []string{"run-2"} }, ConditionBlocked},
		"the review WIP is reached":        {func(in *AdmitInput) { in.Facts.OpenPRs = 3 }, ConditionReviewWIP},
		"the open PR count is unknown while runs are in flight": {func(in *AdmitInput) {
			in.Facts.OpenPRsKnown = false
		}, ConditionReviewWIP},
		"the budget would be breached": {func(in *AdmitInput) {
			in.Reservation.ProviderCost = 11 * money.Dollar
		}, CeilingCash},
		"its blocker is still open": {func(in *AdmitInput) { in.Candidate.BlockedByOpen = []string{"run-p"} }, ConditionBlocked},
	} {
		t.Run(name, func(t *testing.T) {
			in := admittable()
			tt.break_(&in)
			if ok, binding := Admit(in); ok || binding != tt.want {
				t.Fatalf("ok = %v, binding = %q, want %q", ok, binding, tt.want)
			}
		})
	}
}

func TestAdmitIgnoresRelationsToRunsNotInFlight(t *testing.T) {
	in := admittable()
	in.Candidate.BlockedBy = []string{"run-m"}
	in.Candidate.Blocks = []string{"run-n"}
	if ok, binding := Admit(in); !ok {
		t.Fatalf("withheld by %q", binding)
	}
}

// TestAdmitWithholdsACandidateWhoseBlockerIsStillOpen is AC1 at the last step
// before dispatch: a blocked-by issue Linear has not completed or canceled
// holds the candidate even with nothing in flight and no run for it.
func TestAdmitWithholdsACandidateWhoseBlockerIsStillOpen(t *testing.T) {
	in := admittable()
	in.InFlight = nil
	in.Facts.OpenPRsKnown = false
	in.Candidate.BlockedBy = []string{"run-p"}
	in.Candidate.BlockedByOpen = []string{"run-p"}
	if ok, binding := Admit(in); ok || binding != ConditionBlocked {
		t.Fatalf("ok = %v, binding = %q, want an open blocker to withhold it", ok, binding)
	}
}

func TestAdmitStartsTheFirstRunWhenTheOpenPRCountIsUnknown(t *testing.T) {
	in := admittable()
	in.InFlight = nil
	in.Facts.OpenPRsKnown = false
	if ok, binding := Admit(in); !ok {
		t.Fatalf("withheld by %q, want a lone run admitted", binding)
	}
}

func TestAdmitReportsTheFirstConditionInOrder(t *testing.T) {
	in := admittable()
	in.Facts.ProviderHalted = true
	in.N = 1
	in.Candidate.Repo = "octo/two"
	in.Reservation.ProviderCost = 11 * money.Dollar
	if _, binding := Admit(in); binding != CeilingProviderHalted {
		t.Fatalf("binding = %q, want the earliest condition", binding)
	}
}

func TestAdmitChecksBudgetLast(t *testing.T) {
	in := admittable()
	in.Budget.ProviderWindows = []Window{{Name: "5h", Period: 5 * time.Hour, Limit: money.Dollar}}
	in.Reservation.ProviderCost = 2 * money.Dollar
	if _, binding := Admit(in); binding != "5h" {
		t.Fatalf("binding = %q, want the provider window", binding)
	}
}

// TestAdmitBoundsARunByItsModelsOwnCap asserts the run's build model selects
// its per-model cap, checked beside the plan's windows.
func TestAdmitBoundsARunByItsModelsOwnCap(t *testing.T) {
	in := admittable()
	in.Facts.Model = "p/flash"
	in.Budget.ProviderWindows = []Window{{Name: "month", Period: 720 * time.Hour, Limit: 70 * money.Dollar}}
	in.Budget.ModelCaps = map[string]Window{"p/flash": {Name: "p/flash", Period: 720 * time.Hour, Limit: 60 * money.Dollar}}
	in.Settled = Settled{Windows: []money.Micros{0}, ModelCap: 58 * money.Dollar}
	in.Reservation.ProviderCost = 5 * money.Dollar
	if ok, binding := Admit(in); ok || binding != "p/flash" {
		t.Fatalf("ok = %v, binding = %q, want the model cap to withhold it", ok, binding)
	}
}

func TestAdmitReadsAnUnsetNAsOne(t *testing.T) {
	in := admittable()
	in.N = 0
	in.InFlight = nil
	if ok, binding := Admit(in); !ok {
		t.Fatalf("withheld by %q, want a lone run admitted", binding)
	}
}

func TestAdmitWithholdsWhenTheOpenPRCountIsUnknownAndOneRunIsInFlight(t *testing.T) {
	in := admittable()
	in.InFlight = in.InFlight[:1]
	in.Facts.OpenPRsKnown = false
	if ok, binding := Admit(in); ok || binding != ConditionReviewWIP {
		t.Fatalf("ok = %v, binding = %q, want %q", ok, binding, ConditionReviewWIP)
	}
}

func TestAdmitCountsOnlyLargeRunsAgainstTheLargeCap(t *testing.T) {
	in := admittable()
	in.Candidate.Size = largeSize
	in.InFlight[1].Size = "M"
	if ok, binding := Admit(in); !ok {
		t.Fatalf("withheld by %q, want a large run admitted beside medium ones", binding)
	}
}

func TestAdmitReportsTheEarlierOfTwoConditionsThatWithhold(t *testing.T) {
	for name, tt := range map[string]struct {
		break_ func(*AdmitInput)
		want   string
	}{
		"the breaker before the provider halt": {func(in *AdmitInput) {
			in.Facts.BreakerTripped = true
			in.Facts.ProviderHalted = true
		}, ConditionCircuitBreaker},
		"the breaker before the owner pause": {func(in *AdmitInput) {
			in.Facts.BreakerTripped = true
			in.Facts.OwnerPaused = true
		}, ConditionCircuitBreaker},
		"the owner pause before the provider halt": {func(in *AdmitInput) {
			in.Facts.OwnerPaused = true
			in.Facts.ProviderHalted = true
		}, ConditionOwnerPaused},
		"the provider halt before the platform cap": {func(in *AdmitInput) {
			in.Facts.ProviderHalted = true
			in.Facts.Limits.PlatformCap = 2
		}, CeilingProviderHalted},
		"the platform cap before N": {func(in *AdmitInput) {
			in.Facts.Limits.PlatformCap = 2
			in.N = 1
		}, ConditionPlatformCap},
		"N before the large cap": {func(in *AdmitInput) {
			in.N = 2
			in.Candidate.Size = largeSize
		}, ConditionConcurrency},
		"the large cap before the repository cap": {func(in *AdmitInput) {
			in.Candidate.Size = largeSize
			in.Candidate.Repo = "octo/two"
			in.InFlight[1].Size = largeSize
			in.Facts.Limits.RepoCap = 1
		}, ConditionLargeCap},
		"the repository cap before the overlap": {func(in *AdmitInput) {
			in.Facts.Limits.RepoCap = 1
			in.Candidate.Repo, in.Candidate.Stage, in.Candidate.Files =
				"octo/two", StageBuild, []string{"a.go"}
			in.InFlight[1].Stage, in.InFlight[1].Files = StageBuild, []string{"a.go"}
		}, ConditionRepoCap},
		"the overlap before a blocking relation": {func(in *AdmitInput) {
			in.Candidate.Repo, in.Candidate.Stage, in.Candidate.Files =
				"octo/two", StageBuild, []string{"a.go"}
			in.InFlight[1].Stage, in.InFlight[1].Files = StageBuild, []string{"a.go"}
			in.Candidate.BlockedBy = []string{"run-1"}
		}, ConditionRepoOverlap},
		"a blocking relation before the review WIP": {func(in *AdmitInput) {
			in.Candidate.BlockedBy = []string{"run-1"}
			in.Facts.OpenPRs = 3
		}, ConditionBlocked},
		"the review WIP before the budget": {func(in *AdmitInput) {
			in.Facts.OpenPRs = 3
			in.Reservation.ProviderCost = 11 * money.Dollar
		}, ConditionReviewWIP},
	} {
		t.Run(name, func(t *testing.T) {
			in := admittable()
			tt.break_(&in)
			if ok, binding := Admit(in); ok || binding != tt.want {
				t.Fatalf("ok = %v, binding = %q, want %q", ok, binding, tt.want)
			}
		})
	}
}

// TestAdmitSharesARepositoryBelowTheCapUnlessTheWriteSetsOverlap checks the
// repository rule: a run below the cap is admitted whether or not its
// neighbours' write sets are known, and a candidate whose planned files share a
// path with a run in flight is withheld. A plan-stage claim writes nothing, so it
// overlaps nothing on either side.
func TestAdmitSharesARepositoryBelowTheCapUnlessTheWriteSetsOverlap(t *testing.T) {
	planned := func(files ...string) Subject {
		return Subject{Ticket: "run-9", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Files: files}
	}
	for name, tt := range map[string]struct {
		candidate Subject
		inFlight  []InFlight
		want      bool
	}{
		"disjoint write sets share the repository": {
			planned("a.go", "b.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Files: []string{"c.go"}}},
			true,
		},
		"intersecting write sets do not share the repository": {
			planned("a.go", "b.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Files: []string{"b.go", "z.go"}}},
			false,
		},
		"a candidate with no plan yet is not held by a planned run": {
			Subject{Ticket: "run-9", Repo: "octo/scratch", Size: "M", Stage: StageBuild},
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Files: []string{"c.go"}}},
			true,
		},
		"an in-flight run with no plan holds no candidate": {
			planned("a.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StageBuild}},
			true,
		},
		"a single-stage in-flight run holds no candidate": {
			planned("a.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M"}},
			true,
		},
		"a plan-stage run overlaps nothing, whatever files it carries": {
			planned("a.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StagePlan, Files: []string{"a.go"}}},
			true,
		},
		"a plan-stage candidate overlaps nothing, whatever files its row carries": {
			Subject{Ticket: "run-9", Repo: "octo/scratch", Size: "M", Stage: StagePlan, Files: []string{"a.go"}},
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Files: []string{"a.go"}}},
			true,
		},
		"a run in another repository is not consulted": {
			planned("a.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/other", Size: "M", Stage: StageBuild, Files: []string{"a.go"}}},
			true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := admittable()
			in.Candidate = tt.candidate
			in.InFlight = tt.inFlight
			ok, binding := Admit(in)
			if ok != tt.want {
				t.Fatalf("ok = %v, binding = %q, want ok = %v", ok, binding, tt.want)
			}
			if !ok && binding != ConditionRepoOverlap {
				t.Fatalf("binding = %q, want %s", binding, ConditionRepoOverlap)
			}
		})
	}
}

// TestAdmitHoldsARepositoryAtItsCap counts the runs in flight in a repository
// whatever their write sets, and names the repository the way GitHub does.
func TestAdmitHoldsARepositoryAtItsCap(t *testing.T) {
	in := admittable()
	in.N, in.Facts.Limits.RepoCap = 10, 2
	in.Candidate.Repo = "octo/one"
	in.InFlight = []InFlight{
		{Ticket: "run-1", Repo: "octo/one", Size: "S"},
		{Ticket: "run-2", Repo: "octo/two", Size: "S"},
		{Ticket: "run-3", Repo: "Octo/One", Size: "S"},
	}
	if ok, binding := Admit(in); ok || binding != ConditionRepoCap {
		t.Fatalf("ok = %v, binding = %q, want the cap to withhold a third run", ok, binding)
	}
	in.InFlight = in.InFlight[:2]
	if ok, binding := Admit(in); !ok {
		t.Fatalf("withheld by %q, want room under the cap", binding)
	}
}
