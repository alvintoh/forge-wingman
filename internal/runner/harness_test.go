package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// fakeHarness serves provider "p" — or its own name — through agent, or a fresh
// fakeAgent when unset; it is ready unless a test says otherwise.
type fakeHarness struct {
	name     string
	agent    Agent
	ready    error
	classify func(string, error) (Outcome, StopReason)
}

func (h fakeHarness) Name() string {
	if h.name == "" {
		return "p"
	}
	return h.name
}
func (h fakeHarness) Agent(Profile, string) Agent {
	if h.agent == nil {
		return &fakeAgent{}
	}
	return h.agent
}
func (h fakeHarness) Classify(stderrTail string, err error) (Outcome, StopReason) {
	if h.classify != nil {
		return h.classify(stderrTail, err)
	}
	return classifyMarkers(stderrTail)
}
func (h fakeHarness) Ready() error { return h.ready }

// planIdentity is the test plan-to-harness resolver: a plan runs the harness
// named after it, so a fake harness named "p" serves a "p/model" without a
// providers.json entry, and the command-code harness serves its own plan.
func planIdentity(plan string) []string { return []string{plan} }

// testRouter is NewRouter with planIdentity, so a test can bind a router to
// fake harnesses whose plans are not in the embedded configuration.
func testRouter(profile Profile, harnesses ...Harness) Router {
	return newRouter(planIdentity, profile, harnesses...)
}

func TestCommandCodeHarnessNamesItself(t *testing.T) {
	if got := (CommandCodeHarness{}).Name(); got != "command-code" {
		t.Fatalf("name = %q, want command-code", got)
	}
}

func TestRouterRunsTheHarnessItsModelNames(t *testing.T) {
	ccBin, ccAttempts := scriptedCommandCode(t, "normal")
	other := &fakeAgent{}
	r := testRouter(ProfileBuild,
		CommandCodeHarness{Bin: ccBin, Key: "k", Home: t.TempDir()},
		fakeHarness{agent: other},
	)

	var out, errBuf strings.Builder
	if err := r.WithModel("command-code/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if len(ccAttempts()) != 1 || other.calls != 0 {
		t.Fatalf("the command-code model did not route to the command-code harness alone")
	}
	if err := r.WithModel("p/y").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if len(ccAttempts()) != 1 || other.calls != 1 {
		t.Fatalf("the p model did not route to its own harness alone")
	}
}

func TestRouterRefusesAnUnservedModel(t *testing.T) {
	r := testRouter(ProfileBuild, fakeHarness{agent: &fakeAgent{}})
	if err := r.Gate("other/x"); err == nil {
		t.Fatal("the router served a model no harness serves")
	}
	var out, errBuf strings.Builder
	if err := r.WithModel("other/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err == nil {
		t.Fatal("the router ran a model no harness serves")
	}
}

func TestRouterRunRefusesAnUnreadyHarness(t *testing.T) {
	r := testRouter(ProfileBuild, CommandCodeHarness{Bin: "cmd"})
	var out, errBuf strings.Builder
	err := r.WithModel("command-code/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "COMMANDCODE_API_KEY") {
		t.Fatalf("err = %v, want the harness's missing-key refusal", err)
	}
}

// TestCommandCodeHarnessIsReadyWithItsKey asserts the harness is refused
// without its key and ready with it (AC7).
func TestCommandCodeHarnessIsReadyWithItsKey(t *testing.T) {
	for name, tt := range map[string]struct {
		h     CommandCodeHarness
		ready bool
	}{
		"no key": {CommandCodeHarness{}, false},
		"a key":  {CommandCodeHarness{Key: "k"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tt.h.Ready(); (err == nil) != tt.ready {
				t.Fatalf("Ready() = %v, want ready %v", err, tt.ready)
			}
		})
	}
}

// TestRouterClassifiesAnUnservedModelAsAnAgentFailure asserts a model no
// harness serves never reads as a budget or availability stop.
func TestRouterClassifiesAnUnservedModelAsAnAgentFailure(t *testing.T) {
	r := testRouter(ProfileBuild, fakeHarness{}).WithModel("other/x").(Router)
	if outcome, reason := r.Classify("allowance exhausted", nil); outcome != OutcomeAgentFailed || reason != StopAgentExit {
		t.Fatalf("Classify = %s/%s, want the ordinary agent failure", outcome, reason)
	}
}

// planOrders is a plan-to-harness resolver a case below varies.
func planOrders(orders map[string][]string) harnessOrder {
	return func(plan string) []string { return orders[plan] }
}

// TestRouterSelectsTheHarnessItsPlanConfigures asserts the harness runs because
// the plan's configuration names it, not because the model's prefix does (AC3).
func TestRouterSelectsTheHarnessItsPlanConfigures(t *testing.T) {
	configured, prefixed := &fakeAgent{}, &fakeAgent{}
	r := newRouter(planOrders(map[string][]string{"plan": {"adapter"}}), ProfileBuild,
		fakeHarness{name: "adapter", agent: configured},
		fakeHarness{name: "plan", agent: prefixed},
	)
	var out, errBuf strings.Builder
	if err := r.WithModel("plan/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if configured.calls != 1 || prefixed.calls != 0 {
		t.Fatalf("ran the harness named by the prefix (%d) rather than the plan's configuration (%d)", prefixed.calls, configured.calls)
	}
}

// TestRouterFallsBackToThePlansNextHarness asserts the plan's ordered fallback
// runs when its default is not ready (AC3).
func TestRouterFallsBackToThePlansNextHarness(t *testing.T) {
	defaultAgent, fallbackAgent := &fakeAgent{}, &fakeAgent{}
	r := newRouter(planOrders(map[string][]string{"plan": {"default", "fallback"}}), ProfileBuild,
		fakeHarness{name: "default", ready: errors.New("default is unavailable"), agent: defaultAgent},
		fakeHarness{name: "fallback", agent: fallbackAgent},
	)
	var out, errBuf strings.Builder
	if err := r.WithModel("plan/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if defaultAgent.calls != 0 || fallbackAgent.calls != 1 {
		t.Fatalf("ran default %d times and fallback %d, want the ready fallback alone", defaultAgent.calls, fallbackAgent.calls)
	}
}

// TestRouterRefusesAPlanItHasNoHarnessFor asserts a plan no configuration names
// is refused, so an unregistered model never runs.
func TestRouterRefusesAPlanItHasNoHarnessFor(t *testing.T) {
	r := newRouter(planOrders(nil), ProfileBuild, fakeHarness{name: "adapter"})
	if err := r.Gate("plan/x"); err == nil || !strings.Contains(err.Error(), "no harness is configured") {
		t.Fatalf("Gate = %v, want the unconfigured plan refused", err)
	}
	var out, errBuf strings.Builder
	if err := r.WithModel("plan/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err == nil {
		t.Fatal("ran a model whose plan names no harness")
	}
}

// TestRouterRefusesAPlanWhoseHarnessesAreAllUnready asserts the default's own
// refusal is reported when no harness in the plan can run.
func TestRouterRefusesAPlanWhoseHarnessesAreAllUnready(t *testing.T) {
	r := newRouter(planOrders(map[string][]string{"plan": {"default", "fallback"}}), ProfileBuild,
		fakeHarness{name: "default", ready: errors.New("the default harness has no key")},
		fakeHarness{name: "fallback", ready: errors.New("the fallback harness has no key")},
	)
	err := r.Gate("plan/x")
	if err == nil || !strings.Contains(err.Error(), "default harness has no key") {
		t.Fatalf("Gate = %v, want the default harness's own refusal", err)
	}
}

// TestRouterSelectsFromTheEmbeddedPlanConfiguration asserts the production
// resolver wires a configured plan to its harness, and refuses one it has no
// configuration for (AC3).
func TestRouterSelectsFromTheEmbeddedPlanConfiguration(t *testing.T) {
	bin, attempts := scriptedCommandCode(t, "normal")
	r := NewRouter(ProfileBuild, CommandCodeHarness{Bin: bin, Key: "k", Home: t.TempDir()})
	if err := r.Gate("command-code/x"); err != nil {
		t.Fatalf("Gate = %v, want the plan's configured harness", err)
	}
	var out, errBuf strings.Builder
	if err := r.WithModel("command-code/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if len(attempts()) != 1 {
		t.Fatalf("the configured plan's harness ran %d times, want 1", len(attempts()))
	}
	if err := NewRouter(ProfileBuild, CommandCodeHarness{Bin: bin, Key: "k"}).Gate("unconfigured/x"); err == nil {
		t.Fatal("a plan with no harness configuration was gated in")
	}
}

// TestRouterClassifiesThroughTheHarnessThatRan asserts a failed run is
// classified by the harness Run picked — the fallback when the default was not
// ready — and, when none was ready, by the plan's first registered harness
// rather than a bare agent failure (AC3).
func TestRouterClassifiesThroughTheHarnessThatRan(t *testing.T) {
	budget := func(string, error) (Outcome, StopReason) { return OutcomeBudgetStop, StopAllowanceExhausted }
	infra := func(string, error) (Outcome, StopReason) { return OutcomeInfraFailure, StopModelUnavailable }
	for name, tc := range map[string]struct {
		harnesses  []Harness
		wantReason StopReason
	}{
		"the ready fallback ran": {[]Harness{
			fakeHarness{name: "default", ready: errors.New("no key"), classify: budget},
			fakeHarness{name: "fallback", classify: infra},
		}, StopModelUnavailable},
		"none was ready": {[]Harness{
			fakeHarness{name: "fallback", ready: errors.New("no key"), classify: infra},
		}, StopModelUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRouter(planOrders(map[string][]string{"plan": {"default", "fallback"}}), ProfileBuild,
				tc.harnesses...).WithModel("plan/x").(Router)
			if _, reason := r.Classify("", nil); reason != tc.wantReason {
				t.Fatalf("Classify reason = %s, want %s", reason, tc.wantReason)
			}
		})
	}
}

// TestMeterUsageRepricesFromThePlansRates asserts the runner prices a timed
// run from the plan's rates — tokens times the rates, replacing the harness's
// own step and cost-event figures — and keeps those figures when the plan does
// not price the model (FR-22).
func TestMeterUsageRepricesFromThePlansRates(t *testing.T) {
	wednesday := time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC).UnixMilli()
	stream := stepFinish(1_000_000, 1_000_000, 2_000_000, 4, wednesday) + `{"type":"cost","part":{"cost":5}}` + "\n"
	plan := func(model string) (providers.Rates, bool) {
		return providers.Rates{Input: 0.28, Output: 0.42, CacheRead: 0.028}, model == "plan/model"
	}
	if got, _ := meterUsage(strings.NewReader(stream), "plan/model", plan); !approxEqual(got.Cost, 0.756) {
		t.Fatalf("priced cost = %v, want tokens x the plan's rates = 0.756", got.Cost)
	}
	if got, _ := meterUsage(strings.NewReader(stream), "nobody/model", plan); !approxEqual(got.Cost, 9) {
		t.Fatalf("metered cost = %v, want the harness's own figures kept", got.Cost)
	}
}

// TestMeterUsagePricesEachStepAtItsOwnTime asserts a run crossing into a peak
// window prices each step at the rates in force when it was made, and that an
// untimed step prices at peak unless the run carries the harness's own cost.
func TestMeterUsagePricesEachStepAtItsOwnTime(t *testing.T) {
	plan := func(string) (providers.Rates, bool) {
		return providers.Rates{Input: 1, Output: 1, Peak: &providers.Peak{Multiplier: 2,
			Hours: []providers.HourRange{{From: 6, To: 10}}}}, true
	}
	// A Wednesday, a minute either side of 06:00 UTC.
	before := time.Date(2026, 10, 7, 5, 59, 0, 0, time.UTC).UnixMilli()
	after := time.Date(2026, 10, 7, 6, 1, 0, 0, time.UTC).UnixMilli()
	const harnessCost = `{"type":"cost","part":{"cost":0.25}}` + "\n"
	for name, tt := range map[string]struct {
		stream string
		want   float64
	}{
		"crossing 06:00":  {stepFinish(1_000_000, 0, 0, 0, before) + stepFinish(1_000_000, 0, 0, 0, after), 1 + 2},
		"an untimed step": {stepFinish(1_000_000, 0, 0, 0, before) + stepFinish(1_000_000, 0, 0, 0, 0), 1 + 2},
		"an untimed step beside the harness's cost": {
			stepFinish(1_000_000, 0, 0, 0, before) + stepFinish(1_000_000, 0, 0, 0.5, 0) + harnessCost, 1 + 0.5 + 0.25},
		"timed steps beside a cost event": {
			stepFinish(1_000_000, 0, 0, 0, before) + stepFinish(1_000_000, 0, 0, 0, after) + harnessCost, 1 + 2},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := meterUsage(strings.NewReader(tt.stream), "plan/model", plan)
			if err != nil {
				t.Fatal(err)
			}
			if !approxEqual(got.Cost, tt.want) || got.Steps != 2 || got.Input != 2_000_000 {
				t.Fatalf("usage = %+v, want cost %v over 2 steps", got, tt.want)
			}
		})
	}
}

// stepFinish is one step_finish event in the runner's own shape, at timeMS
// epoch milliseconds, or untimed when zero.
func stepFinish(input, output, cacheRead int64, cost float64, timeMS int64) string {
	var e event
	e.Type = "step_finish"
	e.Part.Tokens.Input, e.Part.Tokens.Output, e.Part.Tokens.Cache.Read = input, output, cacheRead
	e.Part.Cost, e.Part.Time = cost, timeMS
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// TestFilterEnvMatchesAPrefixOnlyForAnUnderscoreName asserts a plain allowed
// name admits that variable alone, never one it happens to prefix.
func TestFilterEnvMatchesAPrefixOnlyForAnUnderscoreName(t *testing.T) {
	got := filterEnv([]string{"CI=true", "CI_JOB_TOKEN=t", "HOME=/h", "HOMEBREW_GITHUB_API_TOKEN=t", "LC_ALL=C"},
		[]string{"CI", "HOME", "LC_"})
	if want := []string{"CI=true", "HOME=/h", "LC_ALL=C"}; !slices.Equal(got, want) {
		t.Fatalf("filterEnv = %v, want %v", got, want)
	}
}

// TestNoHarnessIsNamedOutsideItsAdapter keeps the vendor vocabulary inside its
// adapter, so no provider or plan is named in the runner's or dispatcher's Go
// logic and a provider is swapped by a configuration edit alone (AC5).
func TestNoHarnessIsNamedOutsideItsAdapter(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if harnessNameAllowed(rel) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(b))
		if strings.Contains(lower, "commandcode") || strings.Contains(lower, "command-code") ||
			strings.Contains(lower, "command code") || ompVocabulary.MatchString(lower) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("a harness is named outside its adapter: %v", offenders)
	}
}

// ompVocabulary matches omp's names as words, so "compile" is not one.
var ompVocabulary = regexp.MustCompile(`oh-my-pi|\bomp\b`)

// harnessNameAllowed lists where a harness name may appear: its own adapter
// alone, so every provider fact elsewhere is read from configuration.
func harnessNameAllowed(rel string) bool {
	return rel == "internal/runner/harness_commandcode.go" || rel == "internal/runner/harness_omp.go"
}

// TestNoRetiredHarnessIsNamed asserts no tracked file outside the vault copies
// and the historical probe results names the retired harness or its model set.
func TestNoRetiredHarnessIsNamed(t *testing.T) {
	root := filepath.Join("..", "..")
	// Copies stamped from elsewhere are exempt: the vault's docs, and the agents
	// /repo-init composes from poly-mind's stack packs, which name the fallback.
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".",
		":!docs/adr", ":!docs/tech-design-v1.md", ":!probe/results", ":!.claude/agents")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	retired := regexp.MustCompile(`(?i:open[c]ode|\bz[e]n\b)`)
	var offenders []string
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if retired.Match(b) {
			offenders = append(offenders, rel)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("the retired harness is named in: %v", offenders)
	}
}

// TestRegistrationRefusesADuplicateName asserts a harness name, or a
// conformance case, registered a second time panics rather than shadowing the
// first.
func TestRegistrationRefusesADuplicateName(t *testing.T) {
	for name, register := range map[string]func(){
		"harness": func() {
			registerHarness(harnessRegistry[0].name, harnessRegistry[0].build, harnessRegistry[0].secretEnv)
		},
		"conformance case": func() {
			for _, hc := range conformanceCases {
				addConformanceCase(hc)
				return
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("a duplicate registration did not panic")
				}
			}()
			register()
		})
	}
}
