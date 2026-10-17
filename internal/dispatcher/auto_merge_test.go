package dispatcher

import (
	"math"
	"testing"
)

func TestAutoMergeOn(t *testing.T) {
	// rated is ten PRs of 1000 agent lines each, ownerPerPR of them rewritten.
	rated := func(ownerPerPR int64) []Sample { return samples(scratch, 10, 1000, ownerPerPR) }
	unmeasured := rated(0)
	unmeasured[3].UnmeasuredCommits = 1
	reverted := rated(0)
	reverted[9] = sample(scratch, 1000, 0, true, true)
	revertedByHand := rated(0)
	revertedByHand[0] = sample(scratch, 1000, 0, false, true)
	for name, tt := range map[string]struct {
		trailing []Sample
		want     AutoMergeDecision
	}{
		"9.8% over ten is on":                  {rated(98), AutoMergeDecision{On: true, Reason: AutoMergeReasonOn, PRs: 10, Rate: 0.098}},
		"exactly 10.0% is on":                  {rated(100), AutoMergeDecision{On: true, Reason: AutoMergeReasonOn, PRs: 10, Rate: 0.1}},
		"10.1% is off":                         {rated(101), AutoMergeDecision{Reason: AutoMergeReasonRateAboveBar, PRs: 10, Rate: 0.101}},
		"nine measured PRs is off":             {samples(scratch, 9, 1000, 0), AutoMergeDecision{Reason: AutoMergeReasonTooFewMeasured, PRs: 9}},
		"an unmeasured commit is off":          {unmeasured, AutoMergeDecision{Reason: AutoMergeReasonTooFewMeasured, PRs: 10}},
		"no agent lines is off":                {samples(scratch, 10, 0, 0), AutoMergeDecision{Reason: AutoMergeReasonNoAgentLines, PRs: 10}},
		"a reverted auto-merge is off":         {reverted, AutoMergeDecision{Reason: AutoMergeReasonReverted, PRs: 10, Reverted: 1}},
		"a revert of a hand merge is on":       {revertedByHand, AutoMergeDecision{On: true, Reason: AutoMergeReasonOn, PRs: 10}},
		"an older PR is not consulted":         {joined(rated(0), samples(scratch, 5, 100, 100)), AutoMergeDecision{On: true, Reason: AutoMergeReasonOn, PRs: 10}},
		"the order is newest first":            {joined(rated(0), []Sample{sample(scratch, 1000, 0, true, true)}), AutoMergeDecision{On: true, Reason: AutoMergeReasonOn, PRs: 10}},
		"the pooled rate weighs lines not PRs": {joined(samples(scratch, 9, 100, 0), []Sample{sample(scratch, 10000, 1000, false, false)}), AutoMergeDecision{On: true, Reason: AutoMergeReasonOn, PRs: 10, Rate: 1000.0 / 10900}},
	} {
		t.Run(name, func(t *testing.T) {
			got := AutoMergeOn(Evidence{Trailing: tt.trailing})
			if got.On != tt.want.On || got.Reason != tt.want.Reason || got.PRs != tt.want.PRs ||
				got.Reverted != tt.want.Reverted || math.Abs(got.Rate-tt.want.Rate) > 1e-9 {
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
		"a rate":         {AutoMergeDecision{Reason: AutoMergeReasonRateAboveBar, Rate: 0.1234}, true, "12.3%"},
		"no agent lines": {AutoMergeDecision{Reason: AutoMergeReasonNoAgentLines, Rate: math.Inf(1)}, true, "no agent lines"},
		"the switch off": {AutoMergeDecision{Reason: AutoMergeReasonSwitchOff}, false, "0.0%"},
		"unreadable":     {AutoMergeDecision{Reason: AutoMergeReasonEvidenceUnread}, false, "0.0%"},
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
