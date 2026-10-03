// Package modelprobe judges free models against a fixture ticket and decides
// which one the default should be.
package modelprobe

import (
	"maps"
	"math"
	"regexp"
	"slices"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Verdict is the outcome of judging one model's run.
type Verdict string

// The verdicts a run can earn; a void run proves nothing about the model.
const (
	VerdictPass Verdict = "pass"
	VerdictFail Verdict = "fail"
	VerdictVoid Verdict = "void"
)

// Reasons a run is excluded, recorded verbatim in the results file.
const (
	ReasonFabricatedBurst = "fabricated-burst"
	ReasonBurstUnreadable = "burst-unreadable"
	ReasonNoWork          = "no-work"
	ReasonRunErrored      = "run-errored"
	ReasonEventShape      = "event-shape-unrecognised"
	ReasonTranscript      = "transcript-unreadable"
	ReasonTimedOut        = "timed-out"
	ReasonToolCallCap     = "tool-call-cap-hit"
	ReasonOverToolCallBar = "tool-calls-over-threshold"
	ReasonRefusedFreeTier = "refused-free-tier"
	ReasonNoSteps         = "no-steps"
	ReasonNoFindingsBlock = "no-findings-block"
)

var (
	burstAssignment = regexp.MustCompile(`\bBurst\s*(?::=|=|:)\s*([^\s,;)}=][^\s,;)}]*)`)
	lineComment     = regexp.MustCompile(`//.*`)
)

// Config holds the probe's tunable values.
type Config struct {
	// Margin is the ratio of the incumbent's tool calls a challenger must come in under to replace it.
	Margin float64 `json:"margin"`
	// ToolCallRatio is the ratio of the incumbent's tool calls above which a run fails.
	ToolCallRatio float64 `json:"tool_call_ratio"`
	// MaxToolCalls stops a run outright, bounding its spend.
	MaxToolCalls int `json:"max_tool_calls"`
	// TimeoutSeconds is the wall time each model gets.
	TimeoutSeconds int `json:"timeout_seconds"`
	// MaxFallbacks caps the fallbacks kept behind the default.
	MaxFallbacks int `json:"max_fallbacks"`
	// MaxModels caps the models probed in one run, so a run cannot outlast its job.
	MaxModels int `json:"max_models"`
	// ShapeTimeoutSeconds is the wall time each model's review-shape run gets.
	ShapeTimeoutSeconds int `json:"shape_timeout_seconds,omitempty"`
	// ShapeMaxToolCalls stops a review-shape run outright.
	ShapeMaxToolCalls int `json:"shape_max_tool_calls,omitempty"`
}

// DefaultConfig returns the starting values.
//
// STARTING VALUES, NOT MEASURED: Margin and ToolCallRatio are ratios to the incumbent
// chosen for a first run; the owner sets the real ones once probe results show the spread.
func DefaultConfig() Config {
	return Config{Margin: 0.8, ToolCallRatio: 2, MaxToolCalls: 150, TimeoutSeconds: 900, MaxFallbacks: 3, MaxModels: 8,
		ShapeTimeoutSeconds: 90, ShapeMaxToolCalls: 20}
}

// Observation is everything one model's run left behind that the judge reads.
type Observation struct {
	// Baseline and Files are the fixture's Go sources before and after the run, by path.
	Baseline        map[string]string
	Files           map[string]string
	ToolCalls       int
	Events          int
	TranscriptBytes int
	Errored         bool
	TimedOut        bool
	Capped          bool
	Unreadable      bool
	Usage           runner.Usage
}

// Result is one model's judged run.
type Result struct {
	Model           string       `json:"model"`
	Verdict         Verdict      `json:"verdict"`
	Reason          string       `json:"reason,omitempty"`
	ToolCalls       int          `json:"tool_calls"`
	TranscriptBytes int          `json:"transcript_bytes"`
	Usage           runner.Usage `json:"usage"`
	// ReviewVerdict and ReviewReason grade the run under the review and plan agents' restricted shape.
	ReviewVerdict Verdict `json:"review_verdict,omitempty"`
	ReviewReason  string  `json:"review_reason,omitempty"`
}

// Judge grades obs on voiding, fabrication and tool-call budget.
//
// A run is void, proving nothing about the model, when it errored, its events
// were unrecognisable, or it changed nothing. incumbentCalls is the incumbent's
// tool calls in the same run, or 0 when it has no passing run to compare
// against, which leaves only MaxToolCalls in force.
func Judge(model string, obs Observation, incumbentCalls int, cfg Config) Result {
	res := Result{Model: model, ToolCalls: obs.ToolCalls, TranscriptBytes: obs.TranscriptBytes, Usage: obs.Usage}
	finish := func(v Verdict, reason string) Result {
		res.Verdict, res.Reason = v, reason
		return res
	}
	tokens := burstTokens(obs.Files)
	switch {
	case obs.Errored:
		return finish(VerdictVoid, ReasonRunErrored)
	case obs.Unreadable:
		return finish(VerdictVoid, ReasonTranscript)
	case obs.Events == 0 && obs.TranscriptBytes > 0:
		return finish(VerdictVoid, ReasonEventShape)
	case obs.ToolCalls == 0 || maps.Equal(obs.Baseline, obs.Files):
		return finish(VerdictVoid, ReasonNoWork)
	case len(tokens) == 0:
		return finish(VerdictFail, ReasonBurstUnreadable)
	case !subset(tokens, burstTokens(obs.Baseline)):
		return finish(VerdictFail, ReasonFabricatedBurst)
	case obs.TimedOut:
		return finish(VerdictFail, ReasonTimedOut)
	case obs.Capped:
		return finish(VerdictFail, ReasonToolCallCap)
	case incumbentCalls > 0 && float64(obs.ToolCalls) > math.Ceil(float64(incumbentCalls)*cfg.ToolCallRatio):
		return finish(VerdictFail, ReasonOverToolCallBar)
	}
	return finish(VerdictPass, "")
}

// burstTokens returns the value text of every assignment to a Burst field or
// variable in files, whatever form it takes, ignoring line comments.
func burstTokens(files map[string]string) []string {
	var tokens []string
	for _, src := range files {
		for _, m := range burstAssignment.FindAllStringSubmatch(lineComment.ReplaceAllString(src, ""), -1) {
			tokens = append(tokens, m[1])
		}
	}
	return tokens
}

func subset(got, allowed []string) bool {
	for _, g := range got {
		if !slices.Contains(allowed, g) {
			return false
		}
	}
	return true
}
