package modelprobe

import (
	"slices"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func pass(model string, calls, bytes int) Result {
	return Result{Model: model, Verdict: VerdictPass, ToolCalls: calls, TranscriptBytes: bytes}
}

func TestDecide(t *testing.T) {
	cfg := Config{Margin: 0.8, MaxFallbacks: 3}
	current := runner.ModelSet{Default: "opencode/inc", Fallbacks: []string{"opencode/b"}}
	for name, tc := range map[string]struct {
		results     []Result
		wantDefault string
		wantChanged bool
		wantReason  string
		wantFalls   []string
	}{
		"clear winner replaces": {
			[]Result{pass("opencode/inc", 20, 1), pass("opencode/new", 10, 1), pass("opencode/b", 30, 1)},
			"opencode/new", true, DecisionChallengerWins, []string{"opencode/inc", "opencode/b"},
		},
		"win inside the margin keeps the default": {
			[]Result{pass("opencode/inc", 20, 1), pass("opencode/new", 17, 1), pass("opencode/b", 30, 1)},
			"opencode/inc", true, DecisionFallbacksReordered, []string{"opencode/new", "opencode/b"},
		},
		"tie keeps the default and the set": {
			[]Result{pass("opencode/inc", 20, 5), pass("opencode/b", 20, 9)},
			"opencode/inc", false, DecisionIncumbentBest, []string{"opencode/b"},
		},
		"exactly at the margin does not flip": {
			[]Result{pass("opencode/inc", 20, 1), pass("opencode/new", 16, 1), pass("opencode/b", 30, 1)},
			"opencode/inc", true, DecisionFallbacksReordered, []string{"opencode/new", "opencode/b"},
		},
		"excluded incumbent is replaced by the best survivor": {
			[]Result{{Model: "opencode/inc", Verdict: VerdictFail, Reason: ReasonFabricatedBurst}, pass("opencode/b", 30, 1), pass("opencode/c", 25, 1)},
			"opencode/c", true, DecisionIncumbentExcluded, []string{"opencode/b"},
		},
		"no survivors keeps the set whole": {
			[]Result{{Model: "opencode/inc", Verdict: VerdictVoid}, {Model: "opencode/b", Verdict: VerdictFail}},
			"opencode/inc", false, DecisionNoSurvivors, []string{"opencode/b"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := Decide(current, tc.results, "2026-10-01", cfg)
			if got.Set.Default != tc.wantDefault || got.Changed != tc.wantChanged || got.Reason != tc.wantReason {
				t.Fatalf("Decide = %s changed=%v %s, want %s changed=%v %s",
					got.Set.Default, got.Changed, got.Reason, tc.wantDefault, tc.wantChanged, tc.wantReason)
			}
			if !slices.Equal(got.Set.Fallbacks, tc.wantFalls) {
				t.Fatalf("fallbacks = %v, want %v", got.Set.Fallbacks, tc.wantFalls)
			}
		})
	}
}

func TestDecideRecordsEvidenceOnlyForAChangedSet(t *testing.T) {
	current := runner.ModelSet{Default: "opencode/inc", ProbedAt: "2026-09-01"}
	got := Decide(current, []Result{pass("opencode/inc", 20, 1), pass("opencode/new", 5, 7)}, "2026-10-01", Config{Margin: 0.8, MaxFallbacks: 3})
	if got.Set.ProbedAt != "2026-10-01" || got.Set.Evidence[0] != (runner.ModelEvidence{Model: "opencode/new", ToolCalls: 5, TranscriptBytes: 7}) {
		t.Fatalf("set = %+v, want the new default's evidence first, stamped with the probe date", got.Set)
	}
	kept := Decide(current, []Result{pass("opencode/inc", 20, 1)}, "2026-10-01", Config{Margin: 0.8, MaxFallbacks: 3})
	if kept.Changed || kept.Set.ProbedAt != "2026-09-01" {
		t.Fatalf("set = %+v changed=%v, want the current set untouched", kept.Set, kept.Changed)
	}
}

func TestRankBreaksToolCallTiesByTranscriptSize(t *testing.T) {
	got := rank([]Result{pass("opencode/b", 5, 900), pass("opencode/a", 5, 100), {Model: "opencode/x", Verdict: VerdictFail}})
	if len(got) != 2 || got[0].Model != "opencode/a" {
		t.Fatalf("rank = %+v, want the smaller transcript first and the failure dropped", got)
	}
}

func TestRankBreaksFullTiesByModelName(t *testing.T) {
	got := rank([]Result{pass("opencode/b", 5, 100), pass("opencode/a", 5, 100)})
	if len(got) != 2 || got[0].Model != "opencode/a" {
		t.Fatalf("rank = %+v, want the alphabetically first model first on a full tie", got)
	}
}

func TestDecideCapsTheFallbacks(t *testing.T) {
	current := runner.ModelSet{Default: "opencode/inc"}
	results := []Result{pass("opencode/inc", 5, 1), pass("opencode/a", 6, 1), pass("opencode/b", 7, 1), pass("opencode/c", 8, 1)}
	got := Decide(current, results, "2026-10-01", Config{Margin: 0.8, MaxFallbacks: 2})
	if !slices.Equal(got.Set.Fallbacks, []string{"opencode/a", "opencode/b"}) || len(got.Set.Evidence) != 3 {
		t.Fatalf("set = %+v, want two fallbacks and three evidence entries", got.Set)
	}
}

func TestDecideNeverPromotesAnExcludedModelWhateverItsToolCalls(t *testing.T) {
	current := runner.ModelSet{Default: "opencode/inc"}
	results := []Result{
		pass("opencode/inc", 20, 1),
		{Model: "opencode/crashed", Verdict: VerdictVoid, Reason: ReasonRunErrored, ToolCalls: 1},
		{Model: "opencode/idle", Verdict: VerdictVoid, Reason: ReasonNoWork, ToolCalls: 2},
	}
	if got := Decide(current, results, "2026-10-01", Config{Margin: 0.8, MaxFallbacks: 3}); got.Changed || got.Set.Default != "opencode/inc" {
		t.Fatalf("decision = %+v, want the incumbent kept", got)
	}
}
