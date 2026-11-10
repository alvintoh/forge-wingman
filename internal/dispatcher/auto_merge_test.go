package dispatcher

import (
	"math"
	"testing"
)

func TestAutoMergeOn(t *testing.T) {
	// rated is ten PRs of 1000 agent lines each, ownerPerPR of them rewritten.
	rated := func(ownerPerPR int64) []Sample { return samples(scratch, 10, 1000, ownerPerPR) }
	autoMerged := func(n int) []Sample {
		out := make([]Sample, 0, n)
		for range n {
			out = append(out, sample(scratch, 1000, 0, "wingman[bot]"))
		}
		return out
	}
	unmeasured := rated(0)
	unmeasured[3].UnmeasuredCommits = 1
	unaccounted := make([]Sample, 0, TrailingPRs)
	for range TrailingPRs {
		unaccounted = append(unaccounted, Sample{Rewrite: Rewrite{AgentLines: 1_000}})
	}
	for name, tt := range map[string]struct {
		trailing []Sample
		bar      float64
		want     AutoMergeDecision
	}{
		"9.8% over ten is within the bar":      {rated(98), 0.20, AutoMergeDecision{WithinBar: true, Reason: "on", PRs: 10, Rate: 0.098}},
		"exactly the bar is within it":         {rated(200), 0.20, AutoMergeDecision{WithinBar: true, Reason: "on", PRs: 10, Rate: 0.2}},
		"20.1% is over the bar":                {rated(201), 0.20, AutoMergeDecision{Reason: "rate-above-bar", PRs: 10, Rate: 0.201}},
		"15.3% is over a 10% bar":              {rated(153), 0.10, AutoMergeDecision{Reason: "rate-above-bar", PRs: 10, Rate: 0.153}},
		"nine measured PRs is unmeasured":      {samples(scratch, 9, 1000, 0), 0.20, AutoMergeDecision{Reason: "too-few-measured", PRs: 9}},
		"an unmeasured commit is unmeasured":   {unmeasured, 0.20, AutoMergeDecision{Reason: "too-few-measured", PRs: 10}},
		"no agent lines has no rate":           {samples(scratch, 10, 0, 0), 0.20, AutoMergeDecision{Reason: "no-agent-lines", PRs: 10}},
		"a PR that merged itself is left out":  {joined(rated(100), autoMerged(1)), 0.20, AutoMergeDecision{WithinBar: true, Reason: "on", PRs: 10, Rate: 0.1}},
		"a window of self-merged PRs is empty": {autoMerged(10), 0.20, AutoMergeDecision{Reason: "too-few-measured", PRs: 0}},
		"no merge account at all":              {unaccounted, 0.20, AutoMergeDecision{Reason: "too-few-measured", PRs: 0}},
		"an older PR is not consulted":         {joined(rated(0), samples(scratch, 5, 100, 100)), 0.20, AutoMergeDecision{WithinBar: true, Reason: "on", PRs: 10}},
		"the order is newest first":            {joined(rated(0), []Sample{sample(scratch, 1000, 0, "alvintoh")}), 0.20, AutoMergeDecision{WithinBar: true, Reason: "on", PRs: 10}},
		"the pooled rate weighs lines not PRs": {joined(samples(scratch, 9, 100, 0), []Sample{sample(scratch, 10000, 1000, "alvintoh")}), 0.20, AutoMergeDecision{WithinBar: true, Reason: "on", PRs: 10, Rate: 1000.0 / 10900}},
	} {
		t.Run(name, func(t *testing.T) {
			got := AutoMergeOn(Evidence{Trailing: tt.trailing}, tt.bar)
			if got.WithinBar != tt.want.WithinBar || got.Reason != tt.want.Reason || got.PRs != tt.want.PRs ||
				math.Abs(got.Rate-tt.want.Rate) > 1e-9 {
				t.Fatalf("AutoMergeOn = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAutoMergeDecisionLogsOnlyWhatWasMeasured(t *testing.T) {
	for name, tt := range map[string]struct {
		d         AutoMergeDecision
		evaluated bool
		rate      string
	}{
		"a rate":                           {AutoMergeDecision{Reason: AutoMergeReasonRateAboveBar, Rate: 0.1234}, true, "12.3%"},
		"no agent lines":                   {AutoMergeDecision{Reason: AutoMergeReasonNoAgentLines, Rate: math.Inf(1)}, true, "no agent lines"},
		"too few measured, no agent lines": {AutoMergeDecision{Reason: AutoMergeReasonTooFewMeasured, Rate: math.Inf(1)}, true, "no agent lines"},
		"the switch off":                   {AutoMergeDecision{Reason: AutoMergeReasonSwitchOff}, false, "0.0%"},
		"a bar that cannot be read":        {AutoMergeDecision{Reason: AutoMergeReasonBarInvalid}, false, "0.0%"},
		"unreadable":                       {AutoMergeDecision{Reason: AutoMergeReasonEvidenceUnread}, false, "0.0%"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tt.d.Evaluated(); got != tt.evaluated {
				t.Errorf("Evaluated = %v, want %v", got, tt.evaluated)
			}
			if got := tt.d.RateText(); got != tt.rate {
				t.Errorf("RateText = %q, want %q", got, tt.rate)
			}
		})
	}
}
