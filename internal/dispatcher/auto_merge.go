package dispatcher

import (
	"fmt"
	"math"
)

// The reasons the switch and the rate report, stable so a log line and a view
// can be counted by them.
const (
	// AutoMergeReasonOn is a rate within the bar over a full measured window of
	// PRs a human merged.
	AutoMergeReasonOn = "on"
	// AutoMergeReasonSwitchOff is the owner's switch being off.
	AutoMergeReasonSwitchOff = "switch-off"
	// AutoMergeReasonSwitchUnread is the owner's switch being unreadable. pr-meta
	// takes the switch as a boolean and reports a failed read as a warning before
	// it reads the rate; the surface, which reads the switch itself, reports it
	// as the reason auto-merge is off.
	AutoMergeReasonSwitchUnread = "switch-unread"
	// AutoMergeReasonBarInvalid is a stored bar that is not a number from 1 to 100.
	AutoMergeReasonBarInvalid = "bar-invalid"
	// AutoMergeReasonEscaped is an auto-merged PR the owner's review never read: one
	// a later merge reverted or named as fixed, which holds auto-merge off until the
	// owner switches it on again.
	AutoMergeReasonEscaped = "escaped"
	// AutoMergeReasonTooFewMeasured is fewer than TrailingPRs PRs a human merged, or one with an unmeasured commit.
	AutoMergeReasonTooFewMeasured = "too-few-measured"
	// AutoMergeReasonNoAgentLines is a window whose PRs carry no agent lines, so no rate exists.
	AutoMergeReasonNoAgentLines = "no-agent-lines"
	// AutoMergeReasonRateAboveBar is a rewrite rate over the bar.
	AutoMergeReasonRateAboveBar = "rate-above-bar"
	// AutoMergeReasonEvidenceUnread is evidence that could not be read.
	AutoMergeReasonEvidenceUnread = "evidence-unread"
)

// AutoMergeDecision is the rewrite rate's verdict over the trailing window:
// whether the rate is within its bar and what it was read from.
type AutoMergeDecision struct {
	// WithinBar is whether the rate is at or under the bar: exactly at the bar
	// allows it.
	WithinBar bool
	// Reason is one of the AutoMerge reason tokens.
	Reason string
	// PRs is how many PRs a human merged the window held.
	PRs  int
	Rate float64
}

// Evaluated reports whether the rate was read from evidence: a switch that was
// off, a switch that could not be read, a bar that could not be read and
// evidence that could not be read leave it unread.
func (d AutoMergeDecision) Evaluated() bool {
	switch d.Reason {
	case AutoMergeReasonSwitchOff, AutoMergeReasonSwitchUnread, AutoMergeReasonBarInvalid, AutoMergeReasonEvidenceUnread:
		return false
	}
	return true
}

// RateText is the rate as a percentage, or "no agent lines" where the window has
// none.
func (d AutoMergeDecision) RateText() string {
	if d.Reason == AutoMergeReasonNoAgentLines || math.IsInf(d.Rate, 0) {
		return "no agent lines"
	}
	return fmt.Sprintf("%.1f%%", d.Rate*100)
}

// AutoMergeOn reads the rate's verdict over the newest TrailingPRs PRs a human
// merged, newest first: whether they are all measured and carry agent lines, and
// whether they rewrite at most bar. A PR that merged itself is left out of the
// window, as is one whose row names no merge account, so the rate is what the
// owner's own reading caught; a whole window of them leaves the rate unmeasured
// rather than clean.
func AutoMergeOn(ev Evidence, bar float64) AutoMergeDecision {
	window := ev.HumanWindow(TrailingPRs)
	lines := Lines(window)
	d := AutoMergeDecision{PRs: len(window), Rate: lines.Rate()}
	switch {
	case !allMeasured(window) || len(window) < TrailingPRs:
		d.Reason = AutoMergeReasonTooFewMeasured
	case lines.AgentLines <= 0:
		d.Reason = AutoMergeReasonNoAgentLines
	case d.Rate > bar:
		d.Reason = AutoMergeReasonRateAboveBar
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
