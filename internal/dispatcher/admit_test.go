package dispatcher

import (
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
)

var admitLimits = Limits{PlatformCap: 5, LargeCap: 1, ReviewWIP: 3}

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
		"the repository already has a run": {func(in *AdmitInput) { in.Candidate.Repo = "octo/two" }, ConditionRepoBusy},
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
		"the large cap before the repository": {func(in *AdmitInput) {
			in.Candidate.Size = largeSize
			in.Candidate.Repo = "octo/one"
		}, ConditionLargeCap},
		"the repository before a blocking relation": {func(in *AdmitInput) {
			in.Candidate.Repo = "octo/two"
			in.Candidate.BlockedBy = []string{"run-1"}
		}, ConditionRepoBusy},
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

// TestAdmitSharesARepositoryBetweenKnownWriteSets checks that two runs in one
// repository whose plans are both recorded are admitted together whether or not
// their files intersect, while either side's unknown write set keeps the
// interim one-run-per-repository rule. The plan stage's agent writes nothing, so a
// plan-stage run collides with no candidate on either side of the test.
func TestAdmitSharesARepositoryBetweenKnownWriteSets(t *testing.T) {
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
		"intersecting write sets share the repository": {
			planned("a.go", "b.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Files: []string{"b.go", "z.go"}}},
			true,
		},
		"a candidate with no plan holds the interim rule": {
			Subject{Ticket: "run-9", Repo: "octo/scratch", Size: "M", Stage: StageBuild},
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Files: []string{"c.go"}}},
			false,
		},
		"an in-flight run with no plan holds the interim rule": {
			planned("a.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StageBuild}},
			false,
		},
		"a single-stage in-flight run holds the interim rule": {
			planned("a.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M"}},
			false,
		},
		"a plan-stage run collides with nothing": {
			planned("a.go"),
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Size: "M", Stage: StagePlan}},
			true,
		},
		"a plan-stage candidate collides with nothing": {
			Subject{Ticket: "run-9", Repo: "octo/scratch", Size: "M", Stage: StagePlan},
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
			if !ok && binding != ConditionRepoBusy {
				t.Fatalf("binding = %q, want %s", binding, ConditionRepoBusy)
			}
		})
	}
}

func TestOverlappingRunsNamesTheRunsWhoseWriteSetsIntersect(t *testing.T) {
	candidate := Subject{Ticket: "run-9", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Files: []string{"a.go", "b.go"}}
	for name, tt := range map[string]struct {
		inFlight []InFlight
		want     []string
	}{
		"an intersecting run is named": {
			[]InFlight{
				{Ticket: "run-1", Repo: "octo/scratch", Stage: StageBuild, Files: []string{"b.go", "z.go"}},
				{Ticket: "run-2", Repo: "octo/scratch", Stage: StageBuild, Files: []string{"c.go"}},
			},
			[]string{"run-1"},
		},
		"disjoint sets name nothing": {
			[]InFlight{{Ticket: "run-1", Repo: "octo/scratch", Stage: StageBuild, Files: []string{"c.go"}}},
			nil,
		},
		"a run in another repository is not named": {
			[]InFlight{{Ticket: "run-1", Repo: "octo/other", Stage: StageBuild, Files: []string{"a.go"}}},
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := admittable()
			in.Candidate = candidate
			in.InFlight = tt.inFlight
			if got := OverlappingRuns(in); !slices.Equal(got, tt.want) {
				t.Fatalf("OverlappingRuns = %v, want %v", got, tt.want)
			}
		})
	}
}
