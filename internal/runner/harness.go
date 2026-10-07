package runner

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// Profile is the behaviour an agent runs under: the build phase edits the
// worktree, while the plan and review phases are restricted to reading it.
type Profile int

const (
	ProfileBuild Profile = iota
	ProfilePlan
	ProfileReview
)

// Harness is one agent CLI a run can route a phase through, an adapter behind
// this interface alone.
type Harness interface {
	// Name is the key a plan's configuration selects the harness by.
	Name() string
	// Agent returns the agent that runs profile p on model.
	Agent(p Profile, model string) Agent
	// Classify maps a failed run's stderr tail and exit error to an outcome.
	Classify(stderrTail string, exitErr error) (Outcome, StopReason)
	// Ready reports whether the run is configured to use the harness, nil
	// when it is (AC7).
	Ready() error
	// Conformant reports whether the adapter declares itself conformant, nil
	// when it does; the registry exposes only those.
	Conformant() error
}

// harnessOrder resolves a plan — a model id's prefix — to the harness names it
// runs through, in order: the default first, then the ordered fallbacks. It is
// providers.Harnesses in production, so a plan picks its harness by
// configuration, never by the model prefix.
type harnessOrder func(plan string) []string

// Router is an Agent that runs each attempt through the harness its model's
// plan selects, so one ordered model list can mix harnesses.
type Router struct {
	profile   Profile
	harnesses []Harness
	order     harnessOrder
	model     string
}

// NewRouter returns a router that runs profile through harnesses, selecting a
// model's harness from the plan its provider names.
func NewRouter(profile Profile, harnesses ...Harness) Router {
	return newRouter(providers.Harnesses, profile, harnesses...)
}

// newRouter is NewRouter with the plan-to-harness resolver supplied, so a test
// can route a plan without a providers.json entry.
func newRouter(order harnessOrder, profile Profile, harnesses ...Harness) Router {
	return Router{profile: profile, harnesses: harnesses, order: order}
}

// WithModel returns the router bound to run model.
func (r Router) WithModel(model string) Agent { r.model = model; return r }

// Run runs the router's profile through the harness the model's plan selects,
// refusing a model whose plan names no harness the run is configured for (AC7).
func (r Router) Run(ctx context.Context, dir, session, prompt, rules string, stdout, stderr io.Writer) error {
	h, err := pickHarness(r.harnesses, r.order, Provider(r.model))
	if err != nil {
		return err
	}
	return h.Agent(r.profile, r.model).Run(ctx, dir, session, prompt, rules, stdout, stderr)
}

// Classify classifies a failed run through the harness Run picked: a harness's
// readiness is fixed at construction, so picking again finds the fallback that
// ran when the default was not ready. When no harness was ready, Run failed
// before any ran, and the plan's first registered harness classifies it rather
// than a bare agent failure (AC3).
func (r Router) Classify(stderrTail string, exitErr error) (Outcome, StopReason) {
	plan := Provider(r.model)
	h, err := pickHarness(r.harnesses, r.order, plan)
	if err != nil {
		h = routedHarness(r.harnesses, r.order, plan)
	}
	if h == nil {
		return OutcomeAgentFailed, StopAgentExit
	}
	return h.Classify(stderrTail, exitErr)
}

// Gate reports whether the run is configured to use model, refusing one whose
// plan names no harness the run is configured for (AC7).
func (r Router) Gate(model string) error {
	_, err := pickHarness(r.harnesses, r.order, Provider(model))
	return err
}

// pickHarness returns the first harness the plan names, in order, that is
// registered and ready, so a plan's fallback runs when its default cannot. A
// plan whose harnesses are all unregistered or unready is refused.
func pickHarness(harnesses []Harness, order harnessOrder, plan string) (Harness, error) {
	names := order(plan)
	if len(names) == 0 {
		return nil, fmt.Errorf("no harness is configured for plan %q", plan)
	}
	var unready error
	for _, name := range names {
		h := harnessNamed(harnesses, name)
		if h == nil {
			continue
		}
		if err := h.Ready(); err != nil {
			if unready == nil {
				unready = err
			}
			continue
		}
		return h, nil
	}
	if unready != nil {
		return nil, unready
	}
	return nil, fmt.Errorf("plan %q names no configured harness", plan)
}

// harnessNamed returns the harness with this name, if one is registered.
func harnessNamed(harnesses []Harness, name string) Harness {
	for _, h := range harnesses {
		if h.Name() == name {
			return h
		}
	}
	return nil
}

// routedHarness returns the first harness the plan names that is registered,
// ready or not.
func routedHarness(harnesses []Harness, order harnessOrder, plan string) Harness {
	for _, name := range order(plan) {
		if h := harnessNamed(harnesses, name); h != nil {
			return h
		}
	}
	return nil
}

// provenHarnesses returns the candidates whose Conformant reports nil, so an
// adapter that declares itself unproven is never registered.
func provenHarnesses(candidates []Harness) []Harness {
	var proven []Harness
	for _, h := range candidates {
		if h.Conformant() == nil {
			proven = append(proven, h)
		}
	}
	return proven
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

// classifyMarkers classifies a failed run's stderr by the configured allowance
// and availability phrases (providers.json), falling back to the ordinary
// agent-failure classification. The phrases are UNVERIFIED against a live
// exhaustion or outage: no probe has confirmed a provider's actual wording, so
// they are documented assumptions pending that verification.
func classifyMarkers(stderrTail string) (Outcome, StopReason) {
	lower := strings.ToLower(stderrTail)
	for _, marker := range providers.AllowanceMarkers() {
		if strings.Contains(lower, marker) {
			return OutcomeBudgetStop, StopAllowanceExhausted
		}
	}
	for _, marker := range providers.AvailabilityMarkers() {
		if strings.Contains(lower, marker) {
			return OutcomeInfraFailure, StopModelUnavailable
		}
	}
	return OutcomeAgentFailed, StopAgentExit
}
