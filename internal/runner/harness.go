package runner

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Profile is the behaviour an agent runs under: the build phase edits the
// worktree, while the plan and review phases are restricted to reading it.
type Profile int

const (
	ProfileBuild Profile = iota
	ProfilePlan
	ProfileReview
)

// Harness is one agent CLI a run can route a phase through.
type Harness interface {
	// Providers are the model-id prefixes the harness serves.
	Providers() []string
	// Agent returns the agent that runs profile p on model.
	Agent(p Profile, model string) Agent
	// EnvNames are the variables the harness's process inherits, so one
	// harness's API key never reaches another's process.
	EnvNames() []string
	// Classify maps a failed run's stderr tail and exit error to an outcome.
	Classify(stderrTail string, exitErr error) (Outcome, StopReason)
	// Ready reports whether the run is configured to use the harness — a
	// proprietary harness's opt-in — nil when it is (AC7).
	Ready() error
}

// Router is an Agent that runs each attempt through the harness serving its
// model's provider, so one ordered model list can mix harnesses.
type Router struct {
	profile   Profile
	harnesses []Harness
	model     string
}

// NewRouter returns a router that runs profile through harnesses.
func NewRouter(profile Profile, harnesses ...Harness) Router {
	return Router{profile: profile, harnesses: harnesses}
}

// WithModel returns the router bound to run model.
func (r Router) WithModel(model string) Agent { r.model = model; return r }

// harness returns the harness serving the router's current model.
func (r Router) harness() (Harness, bool) { return harnessFor(r.harnesses, Provider(r.model)) }

// harnessFor returns the harness serving provider, if any.
func harnessFor(harnesses []Harness, provider string) (Harness, bool) {
	for _, h := range harnesses {
		if slices.Contains(h.Providers(), provider) {
			return h, true
		}
	}
	return nil, false
}

// Run runs the router's profile through the harness serving its model,
// refusing a model whose harness is not configured (AC7).
func (r Router) Run(ctx context.Context, dir, session, prompt string, stdout, stderr io.Writer) error {
	h, ok := r.harness()
	if !ok {
		return fmt.Errorf("no harness serves model %q", r.model)
	}
	if err := h.Ready(); err != nil {
		return err
	}
	return h.Agent(r.profile, r.model).Run(ctx, dir, session, prompt, stdout, stderr)
}

// Classify classifies a failed run through the harness that ran it.
func (r Router) Classify(stderrTail string, exitErr error) (Outcome, StopReason) {
	h, ok := r.harness()
	if !ok {
		return OutcomeAgentFailed, StopAgentExit
	}
	return h.Classify(stderrTail, exitErr)
}

// Gate reports whether the run is configured to use model, refusing one no
// harness serves or whose harness is not opted in (AC7).
func (r Router) Gate(model string) error {
	h, ok := harnessFor(r.harnesses, Provider(model))
	if !ok {
		return fmt.Errorf("no harness serves model %q", model)
	}
	return h.Ready()
}

// failureClassifier is implemented by an Agent that classifies its own
// failures, so the classification rules of each harness stay with it (AC3).
type failureClassifier interface {
	Classify(stderrTail string, exitErr error) (Outcome, StopReason)
}

// modelGate is implemented by an Agent that can veto a model before it runs.
type modelGate interface {
	Gate(model string) error
}

// agentBaseEnvNames are the variables every agent process inherits, before a
// harness adds its own; names ending in "_" are prefixes.
var agentBaseEnvNames = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "CI", "USER", "SHELL",
	"GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE", "GOTOOLCHAIN", "GOFLAGS",
	"LC_", "XDG_",
}

// filterEnv narrows environ to the variables named in allowed — a name in
// allowed ending in "_" matches any variable it prefixes.
func filterEnv(environ []string, allowed []string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for _, a := range allowed {
			if name == a || (strings.HasSuffix(a, "_") && strings.HasPrefix(name, a)) {
				env = append(env, kv)
				break
			}
		}
	}
	return env
}

// allowanceMarkers are phrases assumed to appear in an agent's stderr when the
// provider's own allowance is exhausted mid-build, distinguishing a budget
// stop (FR-22, never escalated) from an ordinary agent failure (FR-13, which
// may retry at a higher tier). UNVERIFIED against a live exhaustion: no probe
// has confirmed a provider's actual wording, so this is a documented
// assumption pending that verification, not an observed fact — see the PR's
// Known Limitations.
var allowanceMarkers = []string{
	"allowance exhausted",
	"insufficient credit",
	"insufficient balance",
	"quota exceeded",
	"payment required",
}

// availabilityMarkers are phrases assumed to appear in an agent's stderr when
// the requested model itself is unavailable — rate-limited, overloaded, or
// pulled from the provider's roster — distinguishing an availability stop
// (AC1's same-provider substitution) from an ordinary agent failure.
// UNVERIFIED against a live outage, the same as allowanceMarkers.
var availabilityMarkers = []string{
	"model not found",
	"model not available",
	"model unavailable",
	"no endpoints found",
	"rate limited",
	"overloaded",
	"service unavailable",
	"bad gateway",
}

// classifyMarkers classifies a failed run's stderr by the assumed allowance
// and availability markers, falling back to the ordinary agent-failure
// classification.
func classifyMarkers(stderrTail string) (Outcome, StopReason) {
	lower := strings.ToLower(stderrTail)
	for _, marker := range allowanceMarkers {
		if strings.Contains(lower, marker) {
			return OutcomeBudgetStop, StopAllowanceExhausted
		}
	}
	for _, marker := range availabilityMarkers {
		if strings.Contains(lower, marker) {
			return OutcomeInfraFailure, StopModelUnavailable
		}
	}
	return OutcomeAgentFailed, StopAgentExit
}
