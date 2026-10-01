package modelprobe

import (
	"slices"
	"sort"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Reasons a decision went the way it did.
const (
	DecisionChallengerWins     = "challenger-beats-margin"
	DecisionWithinMargin       = "challenger-within-margin"
	DecisionIncumbentBest      = "incumbent-best"
	DecisionIncumbentExcluded  = "incumbent-excluded"
	DecisionNoSurvivors        = "no-survivors"
	DecisionFallbacksReordered = "fallbacks-changed-only"
)

// Decision is the outcome of one probe run over the free roster.
type Decision struct {
	Set     runner.ModelSet `json:"set"`
	Changed bool            `json:"changed"`
	Reason  string          `json:"reason"`
}

// Decide picks the next model set from a run's results.
//
// Survivors rank by tool calls, then transcript size. A challenger replaces the
// incumbent only by beating its tool calls by cfg.Margin, so a tie or a narrow
// win keeps the default; an incumbent that did not survive is replaced by the
// best survivor. At most cfg.MaxFallbacks survivors stay as fallbacks. With no
// survivors the current set is kept whole.
func Decide(current runner.ModelSet, results []Result, probedAt string, cfg Config) Decision {
	survivors := rank(results)
	if len(survivors) == 0 {
		return Decision{Set: current, Reason: DecisionNoSurvivors}
	}
	next := survivors[0]
	reason := DecisionIncumbentBest
	if inc, ok := find(survivors, current.Default); ok {
		if next.Model != inc.Model {
			if float64(next.ToolCalls) < cfg.Margin*float64(inc.ToolCalls) {
				reason = DecisionChallengerWins
			} else {
				next, reason = inc, DecisionWithinMargin
			}
		}
	} else {
		reason = DecisionIncumbentExcluded
	}

	var fallbacks []string
	evidence := []runner.ModelEvidence{evidenceOf(next)}
	for _, r := range survivors {
		if r.Model != next.Model && len(fallbacks) < cfg.MaxFallbacks {
			fallbacks = append(fallbacks, r.Model)
			evidence = append(evidence, evidenceOf(r))
		}
	}
	if next.Model == current.Default && slices.Equal(fallbacks, current.Fallbacks) {
		return Decision{Set: current, Reason: reason}
	}
	if next.Model == current.Default {
		reason = DecisionFallbacksReordered
	}
	return Decision{
		Set:     runner.ModelSet{Default: next.Model, Fallbacks: fallbacks, ProbedAt: probedAt, Evidence: evidence},
		Changed: true,
		Reason:  reason,
	}
}

func rank(results []Result) []Result {
	var survivors []Result
	for _, r := range results {
		if r.Verdict == VerdictPass {
			survivors = append(survivors, r)
		}
	}
	sort.SliceStable(survivors, func(i, j int) bool {
		a, b := survivors[i], survivors[j]
		if a.ToolCalls != b.ToolCalls {
			return a.ToolCalls < b.ToolCalls
		}
		if a.TranscriptBytes != b.TranscriptBytes {
			return a.TranscriptBytes < b.TranscriptBytes
		}
		return a.Model < b.Model
	})
	return survivors
}

func find(results []Result, model string) (Result, bool) {
	for _, r := range results {
		if r.Model == model {
			return r, true
		}
	}
	return Result{}, false
}

func evidenceOf(r Result) runner.ModelEvidence {
	return runner.ModelEvidence{Model: r.Model, ToolCalls: r.ToolCalls, TranscriptBytes: r.TranscriptBytes}
}
