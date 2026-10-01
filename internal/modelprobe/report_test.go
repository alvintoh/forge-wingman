package modelprobe

import (
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestReportTotalsSpendAndNamesEveryExclusion(t *testing.T) {
	results := []Result{
		{Model: "opencode/a", Verdict: VerdictPass, ToolCalls: 4, Usage: runner.Usage{Input: 100, Output: 10, Reasoning: 7, CacheRead: 30, CacheWrite: 3, Steps: 4, Cost: 0.25}},
		{Model: "opencode/b", Verdict: VerdictFail, Reason: ReasonFabricatedBurst, ToolCalls: 6, Usage: runner.Usage{Input: 50, Output: 5, Reasoning: 1, CacheRead: 20, CacheWrite: 2, Steps: 2, Cost: 0.5}},
	}
	d := Decision{Set: runner.ModelSet{Default: "opencode/a"}, Reason: DecisionIncumbentBest}
	r := NewReport("2026-10-01", DefaultConfig(), Sweep{Results: results, Skipped: []string{"opencode/z"}}, &d)
	if r.Usage.Input != 150 || r.Usage.Output != 15 || r.Usage.Cost != 0.75 ||
		r.Usage.Reasoning != 8 || r.Usage.CacheRead != 50 || r.Usage.CacheWrite != 5 || r.Usage.Steps != 6 {
		t.Fatalf("usage = %+v, want the per-model spend summed", r.Usage)
	}
	sum := r.Summary()
	for _, want := range []string{"`opencode/a` (incumbent-best)", "| `opencode/b` | fail | 6 | fabricated-burst |", "Not probed this run (model cap): opencode/z."} {
		if !strings.Contains(sum, want) {
			t.Fatalf("summary missing %q:\n%s", want, sum)
		}
	}
	if _, err := r.Marshal(); err != nil {
		t.Fatal(err)
	}
}
