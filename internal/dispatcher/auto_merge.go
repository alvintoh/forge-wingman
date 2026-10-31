package dispatcher

import (
	"fmt"
	"math"
)

// The reasons the switch and the rate report, stable so a log line and a view
// can be counted by them.
const (
	// AutoMergeReasonOn is a rate within AutoMergeRate over a full measured window
	// that holds no reverted auto-merge.
	AutoMergeReasonOn = "on"
	// AutoMergeReasonSwitchOff is the owner's switch being off.
	AutoMergeReasonSwitchOff = "switch-off"
	// AutoMergeReasonSwitchUnread is the owner's switch being unreadable. pr-meta
	// takes the switch as a boolean and reports a failed read as a warning before
	// it reads the rate; the surface, which reads the switch itself, reports it
	// as the reason auto-merge is off.
	AutoMergeReasonSwitchUnread = "switch-unread"
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

// AutoMergeDecision is the rewrite rate's own verdict over the trailing window:
// whether the rate is within its bar and what it was read from. The owner's
// switch alone decides whether auto-merge is on, so this is information for the
// runner's log and the autonomy view.
type AutoMergeDecision struct {
	// WithinBar is the rate alone: whether it is at or under AutoMergeRate. It
	// changes nothing about auto-merge.
	WithinBar bool
	// Reason is one of the AutoMerge reason tokens.
	Reason string
	// PRs is how many merged PRs the window held.
	PRs  int
	Rate float64
	// Reverted is how many auto-merged PRs of the window were reverted.
	Reverted int
}

// Evaluated reports whether the rate was read from evidence: a switch that was
// off, or evidence that could not be read, leaves it unread.
func (d AutoMergeDecision) Evaluated() bool {
	return d.Reason != AutoMergeReasonSwitchOff && d.Reason != AutoMergeReasonEvidenceUnread
}

// RateText is the rate as a percentage, or "no agent lines" where the window has
// none.
func (d AutoMergeDecision) RateText() string {
	if d.Reason == AutoMergeReasonNoAgentLines || math.IsInf(d.Rate, 0) {
		return "no agent lines"
	}
	return fmt.Sprintf("%.1f%%", d.Rate*100)
}

// AutoMergeOn reads the rate's verdict over the newest TrailingPRs merged PRs:
// whether they are all measured, carry agent lines, rewrite at most
// AutoMergeRate pooled, and hold no auto-merged PR a merged revert names. Its
// WithinBar is the rate's own answer, which the views report as information:
// auto-merge follows the owner's switch.
func AutoMergeOn(ev Evidence) AutoMergeDecision {
	window := ev.Window(TrailingPRs)
	lines := Lines(window)
	d := AutoMergeDecision{PRs: len(window), Rate: lines.Rate(), Reverted: Reverts(window)}
	switch {
	case !allMeasured(window) || len(window) < TrailingPRs:
		d.Reason = AutoMergeReasonTooFewMeasured
	case lines.AgentLines <= 0:
		d.Reason = AutoMergeReasonNoAgentLines
	case d.Rate > AutoMergeRate:
		d.Reason = AutoMergeReasonRateAboveBar
	case d.Reverted > 0:
		d.Reason = AutoMergeReasonReverted
	default:
		d.Reason, d.WithinBar = AutoMergeReasonOn, true
	}
	return d
}

// allMeasured reports whether every PR of window has all its commits measured: a
// PR with an unmeasured commit undercounts the rate, so a window holding one
// measures nothing.
func allMeasured(window []Sample) bool {
	for _, s := range window {
		if s.UnmeasuredCommits > 0 {
			return false
		}
	}
	return true
}
