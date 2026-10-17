package dispatcher

import "fmt"

// The reasons an AutoMergeDecision gives, stable so a log can be counted by them.
const (
	// AutoMergeReasonOn is the reason when every clause of the rule holds.
	AutoMergeReasonOn = "on"
	// AutoMergeReasonSwitchOff is the owner's switch being off.
	AutoMergeReasonSwitchOff = "switch-off"
	// AutoMergeReasonTooFewMeasured is fewer than TrailingPRs merged PRs, or one with an unmeasured commit.
	AutoMergeReasonTooFewMeasured = "too-few-measured"
	// AutoMergeReasonNoAgentLines is a window whose PRs carry no agent lines, so no rate exists.
	AutoMergeReasonNoAgentLines = "no-agent-lines"
	// AutoMergeReasonRateAboveBar is a rewrite rate over AutoMergeRate.
	AutoMergeReasonRateAboveBar = "rate-above-bar"
	// AutoMergeReasonReverted is an auto-merged PR of the window a merged revert names.
	AutoMergeReasonReverted = "reverted"
	// AutoMergeReasonEvidenceUnread is evidence that could not be read.
	AutoMergeReasonEvidenceUnread = "evidence-unread"
)

// AutoMergeDecision is whether auto-merge is on and the numbers it was decided on.
type AutoMergeDecision struct {
	On bool
	// Reason is one of the AutoMerge reason tokens.
	Reason string
	// PRs is how many merged PRs the window held.
	PRs  int
	Rate float64
	// Reverted is how many auto-merged PRs of the window were reverted.
	Reverted int
}

// Evaluated reports whether the decision was made on evidence, which a switch
// that was off or evidence that could not be read leaves unmeasured.
func (d AutoMergeDecision) Evaluated() bool {
	return d.Reason != AutoMergeReasonSwitchOff && d.Reason != AutoMergeReasonEvidenceUnread
}

// RateText is the rate as a percentage, or "no agent lines" where none exists.
func (d AutoMergeDecision) RateText() string {
	if d.Reason == AutoMergeReasonNoAgentLines {
		return "no agent lines"
	}
	return fmt.Sprintf("%.1f%%", d.Rate*100)
}

// AutoMergeOn decides, with the owner's switch on, whether auto-merge is on: the
// newest TrailingPRs merged PRs are all measured, carry agent lines, rewrite at
// most AutoMergeRate pooled, and none of them is a reverted auto-merge.
func AutoMergeOn(ev Evidence) AutoMergeDecision {
	window := ev.Window(TrailingPRs)
	lines := Lines(window)
	d := AutoMergeDecision{PRs: len(window), Rate: lines.Rate(), Reverted: Reverts(window)}
	switch {
	case !allMeasured(window, TrailingPRs).Held || len(window) < TrailingPRs:
		d.Reason = AutoMergeReasonTooFewMeasured
	case lines.AgentLines <= 0:
		d.Reason = AutoMergeReasonNoAgentLines
	case d.Rate > AutoMergeRate:
		d.Reason = AutoMergeReasonRateAboveBar
	case d.Reverted > 0:
		d.Reason = AutoMergeReasonReverted
	default:
		d.Reason, d.On = AutoMergeReasonOn, true
	}
	return d
}
