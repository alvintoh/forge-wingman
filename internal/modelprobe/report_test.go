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

func TestSummaryShowsTheReviewVerdictBesideTheBuildVerdict(t *testing.T) {
	r := Report{Results: []Result{
		{Model: "opencode/a", Verdict: VerdictPass, ToolCalls: 3, ReviewVerdict: VerdictPass},
		{Model: "opencode/b", Verdict: VerdictPass, ToolCalls: 4, ReviewVerdict: VerdictFail, ReviewReason: ReasonRefusedFreeTier},
		{Model: "opencode/c", Verdict: VerdictFail, ToolCalls: 5, Reason: ReasonNoWork},
	}}
	want := "| Model | Verdict | Tool calls | Reason | Review/plan |\n|---|---|---|---|---|\n" +
		"| `opencode/a` | pass | 3 |  | pass |\n" +
		"| `opencode/b` | pass | 4 |  | fail (refused-free-tier) |\n" +
		"| `opencode/c` | fail | 5 | no-work | - |\n"
	if got := r.Summary(); !strings.Contains(got, want) {
		t.Fatalf("summary =\n%s\nwant to contain\n%s", got, want)
	}
}

func TestReadReportChecksTheReviewFields(t *testing.T) {
	report := func(res string) []byte {
		return []byte(`{"probed_at":"d","config":{},"results":[` + res + `],"usage":{}}`)
	}
	const head = `{"model":"opencode/a","verdict":"pass","tool_calls":1,"transcript_bytes":1,"usage":{}`
	if r, err := ReadReport(report(head + `,"review_verdict":"fail","review_reason":"refused-free-tier"}`)); err != nil || r.Results[0].ReviewReason != ReasonRefusedFreeTier {
		t.Fatalf("ReadReport = %+v, %v, want the review fields accepted", r, err)
	}
	for _, reason := range []string{"refused-free-tier", "no-steps", "no-findings-block"} {
		if _, err := ReadReport(report(head + `,"review_verdict":"fail","review_reason":"` + reason + `"}`)); err != nil {
			t.Fatalf("ReadReport rejected review reason %q: %v", reason, err)
		}
	}
	if _, err := ReadReport(report(head + `,"review_verdict":"fail","review_reason":"leaked provider text"}`)); err == nil {
		t.Fatal("ReadReport accepted an unknown review reason")
	}
	if _, err := ReadReport(report(head + `,"review_verdict":"void"}`)); err == nil {
		t.Fatal("ReadReport accepted a review verdict of void")
	}
}
