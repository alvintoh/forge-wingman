package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

const ompTestModel = "command-code/deepseek/deepseek-v4.1-flash"

func init() { addConformanceCase(ompConformance()) }

// ompConformance wires the omp adapter to the shared suite: a fake CLI
// replaying the recorded fixtures, and the plan's own rate card.
func ompConformance() harnessCase {
	rates, _ := providers.RatesFor(ompTestModel)
	return harnessCase{
		Name:          "omp",
		Model:         ompTestModel,
		ResumeFlag:    "--resume",
		ReadOnlyFlag:  "always-ask",
		BuildFlag:     "yolo",
		LimitByMarker: true,
		Rates:         rates,
		New: func(t *testing.T, fixture string) (Harness, func() []harnessInvocation, string) {
			bin, attempts := scriptedOmp(t, fixture)
			home := t.TempDir()
			return OmpHarness{Bin: bin, Key: conformanceKey, Home: home}, func() []harnessInvocation {
				var runs []harnessInvocation
				for _, a := range attempts() {
					runs = append(runs, harnessInvocation{Args: a.args, Stdin: a.stdin})
				}
				return runs
			}, home
		},
	}
}

// ompAttempt is one invocation the fake omp recorded.
type ompAttempt struct {
	args   []string
	env    []string
	stdin  string
	rules  string
	config string
}

// scriptedOmp writes a fake omp that replays the named recorded fixture: its
// events on stdout, its stderr and its exit code. The "unpriced" fixture is the
// normal run with omp's own cost figures removed.
func scriptedOmp(t *testing.T, fixture string) (bin string, attempts func() []ompAttempt) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake omp is a shell script, which Windows cannot execute")
	}
	dir := t.TempDir()
	abs := func(name string) string {
		p, err := filepath.Abs(filepath.Join("testdata", "omp", name))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	replay := fixture
	if fixture == "unpriced" {
		replay = "normal"
	}
	ndjson := abs(replay + ".ndjson")
	if fixture == "unpriced" {
		ndjson = filepath.Join(dir, "unpriced.ndjson")
		if err := os.WriteFile(ndjson, unpricedOmpEvents(t, abs("normal.ndjson")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exitRaw, err := os.ReadFile(abs(replay + ".exit"))
	if err != nil {
		t.Fatal(err)
	}
	bin, logDir := filepath.Join(dir, "omp"), filepath.Join(dir, "attempts")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	q := func(s string) string { return "'" + s + "'" }
	log := q(logDir+"/") + "$n"
	script := "#!/bin/sh\n" +
		"n=$(ls " + q(logDir) + " 2>/dev/null | grep -c '\\.args$')\n" +
		"printf '%s\\0' \"$@\" > " + log + ".args\n" +
		"env > " + log + ".env\n" +
		"cat > " + log + ".stdin\n" +
		"cat \"$PI_CODING_AGENT_DIR/AGENTS.md\" > " + log + ".rules 2>/dev/null || echo MISSING > " + log + ".rules\n" +
		"cat \"$PI_CODING_AGENT_DIR/config.yml\" > " + log + ".config 2>/dev/null || echo MISSING > " + log + ".config\n" +
		"cat " + q(ndjson) + "\n" +
		"cat " + q(abs(replay+".stderr")) + " >&2\n" +
		"exit " + strings.TrimSpace(string(exitRaw)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, func() []ompAttempt {
		entries, err := os.ReadDir(logDir)
		if err != nil {
			t.Fatal(err)
		}
		var bases []string
		for _, e := range entries {
			if base, ok := strings.CutSuffix(e.Name(), ".args"); ok {
				bases = append(bases, base)
			}
		}
		sort.Strings(bases)
		read := func(base, ext string) string {
			b, _ := os.ReadFile(filepath.Join(logDir, base+ext))
			return string(b)
		}
		var got []ompAttempt
		for _, base := range bases {
			got = append(got, ompAttempt{
				args:   strings.Split(strings.TrimRight(read(base, ".args"), "\x00"), "\x00"),
				env:    strings.Split(strings.TrimSpace(read(base, ".env")), "\n"),
				stdin:  read(base, ".stdin"),
				rules:  read(base, ".rules"),
				config: read(base, ".config"),
			})
		}
		return got
	}
}

// unpricedOmpEvents is the recorded events at path with every message's cost
// figure removed.
func unpricedOmpEvents(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if msg, ok := ev["message"].(map[string]any); ok {
			if usage, ok := msg["usage"].(map[string]any); ok {
				delete(usage, "cost")
			}
		}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		out = append(append(out, b...), '\n')
	}
	return out
}

// TestOmpAgentResumesUnderOneHomeWithTheKeyInItsEnvironment asserts a later
// round resumes the earlier session from the same HOME, that the key reaches
// omp in its environment under omp's own variable, that extensions and skills
// stay off, and that the rules head travels as the agent directory's user
// rules, removed on a round with none.
func TestOmpAgentResumesUnderOneHomeWithTheKeyInItsEnvironment(t *testing.T) {
	bin, attempts := scriptedOmp(t, "normal")
	home := t.TempDir()
	agent := OmpHarness{Bin: bin, Key: "omp-secret", Home: home}.Agent(ProfileBuild, ompTestModel)
	dir := t.TempDir()
	for _, tt := range []struct{ session, rules string }{
		{"", "# Rules\n"},
		{"ses_1", ""},
	} {
		var out, errBuf strings.Builder
		if err := agent.Run(context.Background(), dir, tt.session, "p", tt.rules, &out, &errBuf); err != nil {
			t.Fatal(err)
		}
	}
	got := attempts()
	if len(got) != 2 {
		t.Fatalf("omp ran %d times, want 2", len(got))
	}
	if i := slices.Index(got[1].args, "--resume"); i < 0 || i+1 >= len(got[1].args) || got[1].args[i+1] != "ses_1" {
		t.Errorf("round 2 args %v do not resume ses_1", got[1].args)
	}
	agentDir := filepath.Join(home, "agent")
	for i, a := range got {
		if want := []string{"# Rules\n", "MISSING\n"}[i]; a.rules != want {
			t.Errorf("attempt %d rules = %q, want %q", i, a.rules, want)
		}
		if !strings.Contains(a.config, "checkUpdate: false") || !strings.Contains(a.config, "enableProjectConfig: false") {
			t.Errorf("attempt %d config = %q, want the update check and project MCP off", i, a.config)
		}
		for _, want := range []string{"HOME=" + home, "PI_CODING_AGENT_DIR=" + agentDir, "COMMAND_CODE_API_KEY=omp-secret"} {
			if !slices.Contains(a.env, want) {
				t.Errorf("attempt %d env lacks %s", i, want)
			}
		}
		for _, kv := range a.env {
			if strings.HasPrefix(kv, "COMMANDCODE_API_KEY=") {
				t.Errorf("attempt %d inherited the runner's secret variable: %s", i, kv)
			}
		}
		if j := slices.Index(a.args, "--model"); j < 0 || a.args[j+1] != "commandcode/deepseek/deepseek-v4.1-flash" {
			t.Errorf("attempt %d args %v do not name omp's own model id", i, a.args)
		}
		if !slices.Contains(a.args, "--no-extensions") || !slices.Contains(a.args, "--no-skills") {
			t.Errorf("attempt %d args %v load extensions or skills", i, a.args)
		}
		if j := slices.Index(a.args, "--session-dir"); j < 0 || a.args[j+1] != filepath.Join(home, "sessions") {
			t.Errorf("attempt %d args %v keep sessions outside the run's HOME", i, a.args)
		}
	}
}

// TestOmpAgentWritesThePlansOutputCap asserts the plan's per-model ceiling
// reaches omp's own override file. omp asks its registry for the model's stated
// maximum, and an API that caps lower rejects the request outright: ling 3.1
// Flash answers 400 to max_tokens 64000 and 200 to 32768 (run 37881527153).
func TestOmpAgentWritesThePlansOutputCap(t *testing.T) {
	bin, _ := scriptedOmp(t, "normal")
	home := t.TempDir()
	agent := OmpHarness{Bin: bin, Key: "k", Home: home}.Agent(ProfileBuild, "command-code/inclusionai/ling-3.1-flash:free")
	if err := agent.Run(context.Background(), t.TempDir(), "", "p", "", io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "agent", "models.yml"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "providers:\n  commandcode:\n    modelOverrides:\n      \"inclusionai/ling-3.1-flash:free\":\n        maxTokens: 32768\n"
	if string(raw) != want {
		t.Fatalf("models.yml = %q, want %q", raw, want)
	}
}

// TestOmpModelsCapsOnlyThePlansOwnModels keeps the override file to the plan's
// own models: another plan's model has no ceiling here, and the file is left out
// entirely when nothing is capped, so omp keeps its own default.
func TestOmpModelsCapsOnlyThePlansOwnModels(t *testing.T) {
	for name, caps := range map[string]map[string]int{
		"no caps":           nil,
		"another plan only": {"other/plan/model": 10},
		"a bare model id":   {"ling-3.1-flash:free": 10},
		"an empty model id": {"command-code/": 10},
	} {
		if got := ompModels(caps); got != "" {
			t.Errorf("%s: ompModels = %q, want no file", name, got)
		}
	}
}

// TestOmpAgentChoosesTheApprovalMode asserts only the build profile runs
// unprompted; plan and review need approval no headless run can give (AC2).
func TestOmpAgentChoosesTheApprovalMode(t *testing.T) {
	bin, attempts := scriptedOmp(t, "readonly")
	harness := OmpHarness{Bin: bin, Key: "k", Home: t.TempDir()}
	for name, tt := range map[string]struct {
		profile Profile
		want    string
	}{
		"build":  {ProfileBuild, "yolo"},
		"plan":   {ProfilePlan, "always-ask"},
		"review": {ProfileReview, "always-ask"},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(attempts())
			var out, errBuf strings.Builder
			if err := harness.Agent(tt.profile, ompTestModel).Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
				t.Fatal(err)
			}
			got := attempts()[before:]
			if len(got) != 1 {
				t.Fatalf("omp ran %d times, want 1", len(got))
			}
			if i := slices.Index(got[0].args, "--approval-mode"); i < 0 || got[0].args[i+1] != tt.want {
				t.Fatalf("args = %v, want --approval-mode %s", got[0].args, tt.want)
			}
		})
	}
}

// TestOmpAgentRefusesAModelOfAnotherPlan asserts the adapter runs only a
// named model of the plan whose provider it maps to, before starting omp.
func TestOmpAgentRefusesAModelOfAnotherPlan(t *testing.T) {
	for _, model := range []string{"other/model", "command-code/"} {
		t.Run(model, func(t *testing.T) {
			bin, attempts := scriptedOmp(t, "normal")
			agent := OmpHarness{Bin: bin, Key: "k", Home: t.TempDir()}.Agent(ProfileBuild, model)
			var out, errBuf strings.Builder
			if err := agent.Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err == nil {
				t.Fatal("the omp agent ran a model it cannot map")
			}
			if len(attempts()) != 0 {
				t.Fatal("omp started for a model it cannot map")
			}
		})
	}
}

// TestOmpAgentNeedsAHomeAndAKey asserts a harness with no HOME refuses to run,
// and one with no key is not ready (AC7).
func TestOmpAgentNeedsAHomeAndAKey(t *testing.T) {
	var out, errBuf strings.Builder
	err := OmpAgent{Bin: "omp", Key: "k", Model: ompTestModel}.Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("err = %v, want the missing home refused", err)
	}
	if err := (OmpHarness{}).Ready(); err == nil || !strings.Contains(err.Error(), "COMMANDCODE_API_KEY") {
		t.Fatalf("Ready = %v, want the missing key named", err)
	}
}

// TestTranslateOmpEvents pins the event translation: the session line, one
// timed step per assistant request with omp's own figures, one text part per
// assistant text, user and tool messages ignored, and a turn-fatal error
// echoed to stderr.
func TestTranslateOmpEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"session","version":3,"id":"ses_1"}`,
		`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"prompt"}],"timestamp":1}}`,
		`{"type":"message_end","message":{"role":"assistant","timestamp":1791413995593,"stopReason":"toolUse",` +
			`"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"first"}],` +
			`"usage":{"input":10,"output":3,"cacheRead":4,"cacheWrite":5,"reasoningTokens":2,"cost":{"total":0.5}}}}`,
		`{"type":"message_end","message":{"role":"toolResult","content":[{"type":"text","text":"tool output"}]}}`,
		`{"type":"message_end","message":{"role":"assistant","timestamp":1791413997525,"stopReason":"error",` +
			`"errorMessage":"insufficient balance","content":[{"type":"text","text":"last"}],"usage":{"input":1,"output":1}}}`,
	}, "\n")
	var stdout, stderr strings.Builder
	if err := translateOmpEvents(strings.NewReader(stream), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if got, _ := SessionID(strings.NewReader(out)); got != "ses_1" {
		t.Errorf("session = %q, want ses_1", got)
	}
	if u, _ := SumUsage(strings.NewReader(out)); u != (Usage{Input: 11, Output: 4, Reasoning: 2, CacheRead: 4, CacheWrite: 5, Cost: 0.5, Steps: 2}) {
		t.Errorf("usage = %+v, want the two requests' own counts and figures", u)
	}
	if !strings.Contains(out, `"time":1791413995593`) {
		t.Errorf("the steps carry no request time:\n%s", out)
	}
	if strings.Contains(out, "prompt") || strings.Contains(out, "tool output") {
		t.Errorf("a user or tool message was translated:\n%s", out)
	}
	if got, _ := FinalText(strings.NewReader(out)); got != "last" {
		t.Errorf("final text = %q, want the last assistant text", got)
	}
	if !strings.Contains(stderr.String(), "insufficient balance") {
		t.Errorf("stderr %q does not carry the turn-fatal error", stderr.String())
	}
}

// TestOmpHarnessClassifiesAnExhaustedPlanFromItsErrorText asserts a limit
// omp reports only in its message reaches Classify as an allowance stop.
func TestOmpHarnessClassifiesAnExhaustedPlanFromItsErrorText(t *testing.T) {
	var stdout, stderr strings.Builder
	msg := `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"402 Insufficient balance"}}`
	if err := translateOmpEvents(strings.NewReader(msg), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if outcome, reason := (OmpHarness{}).Classify(stderr.String(), errors.New("exit status 1")); outcome != OutcomeBudgetStop || reason != StopAllowanceExhausted {
		t.Fatalf("classified %s/%s, want the allowance stop", outcome, reason)
	}
}

// TestBuildRunsTheOmpHarnessFromTheRecordedFixtures drives the real adapter
// against the recorded normal run: the recorded usage, final text and session
// reach the Step, priced from the plan's card at the off-peak hour it ran in.
func TestBuildRunsTheOmpHarnessFromTheRecordedFixtures(t *testing.T) {
	bin, _ := scriptedOmp(t, "normal")
	deps, completions, reported := testDeps(validObjects(), nil)
	orders := planOrders(map[string][]string{"command-code": {"omp"}, "p": {"p"}})
	deps.Agent = newRouter(orders, ProfileBuild, OmpHarness{Bin: bin, Key: "k", Home: t.TempDir()}, fakeHarness{})
	c := testConfig(t, initRepo(t))
	c.Model = ompTestModel
	c.ReviewModels = []string{"p/r"}

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	step := reported.last(t).Steps[0]
	tokens := step.Tokens
	tokens.Cost = 0
	if want := (Usage{Input: 5398, Output: 81, Reasoning: 6, CacheRead: 9344, Steps: 2}); tokens != want {
		t.Fatalf("tokens = %+v, want %+v", tokens, want)
	}
	// 5398 x $0.15 + 81 x $0.60 + 9344 x $0.003, per million: off-peak, 23:00 UTC.
	if !approxEqual(step.Tokens.Cost, 0.000886332) {
		t.Fatalf("cost = %v, want the off-peak card's 0.000886332", step.Tokens.Cost)
	}
	events := string(completions[step.CompletionsObject])
	for _, want := range []string{`"sessionID":"01a11898-4774-76ed-9370-94b04bae6811"`, `"text":"done"`} {
		if !strings.Contains(events, want) {
			t.Errorf("translated events do not carry %s:\n%s", want, events)
		}
	}
}

// TestWorkflowsInstallThePinnedOmp asserts the workflows that run an agent
// install omp from its lockfile with bun and check the pinned version.
func TestWorkflowsInstallThePinnedOmp(t *testing.T) {
	for _, name := range []string{"model.yml", "plan-smoke.yml", "review-smoke.yml"} {
		b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		yml := string(b)
		for _, want := range []string{
			"uses: oven-sh/setup-bun@",
			"bun install --frozen-lockfile --cwd tools/omp",
			"tools/omp/node_modules/.bin",
			`jq -r '.dependencies["@oh-my-pi/pi-coding-agent"]' tools/omp/package.json`,
		} {
			if !strings.Contains(yml, want) {
				t.Errorf("%s has no %q", name, want)
			}
		}
	}
}

func TestOmpRunsAGoatFreeModelMeteredAtNothing(t *testing.T) {
	const model = "command-code/poolside/laguna-s-2.1-free"
	bin, attempts := scriptedOmp(t, "laguna-s-2.1-free")
	var out strings.Builder
	h := OmpHarness{Bin: bin, Key: conformanceKey, Home: t.TempDir()}
	if err := h.Agent(ProfileBuild, model).Run(context.Background(), t.TempDir(), "", "p", "", &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := attempts(); len(got) != 1 || !slices.Contains(got[0].args, "commandcode/poolside/laguna-s-2.1-free") {
		t.Fatalf("attempts = %+v, want one run naming the free model to omp's own provider", got)
	}
	u, err := meterUsage(strings.NewReader(out.String()), model, providers.RatesFor)
	if err != nil {
		t.Fatal(err)
	}
	if u.Input == 0 || u.Output == 0 || u.Cost != 0 {
		t.Fatalf("usage = %+v, want tokens metered at $0", u)
	}
}

// TestOmpRunsTheFreeListsTailModelMeteredAtNothing covers ling 3.1 Flash, the
// tail of GOAT's free list. It answers only when the request's output ceiling is
// the 32768 its API caps at, which the plan's output_caps now supply; before
// that, every run of it died at `exit status 1` (plan-smoke 37881527153, fixed
// by 37884808729).
func TestOmpRunsTheFreeListsTailModelMeteredAtNothing(t *testing.T) {
	const model = "command-code/inclusionai/ling-3.1-flash:free"
	bin, attempts := scriptedOmp(t, "ling-3.1-flash-free")
	var out strings.Builder
	h := OmpHarness{Bin: bin, Key: conformanceKey, Home: t.TempDir()}
	if err := h.Agent(ProfileBuild, model).Run(context.Background(), t.TempDir(), "", "p", "", &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := attempts(); len(got) != 1 || !slices.Contains(got[0].args, "commandcode/inclusionai/ling-3.1-flash:free") {
		t.Fatalf("attempts = %+v, want one run naming the free model to omp's own provider", got)
	}
	u, err := meterUsage(strings.NewReader(out.String()), model, providers.RatesFor)
	if err != nil {
		t.Fatal(err)
	}
	if u.Input != 59312 || u.Output != 31 || u.Cost != 0 {
		t.Fatalf("usage = %+v, want the recorded 59312 in / 31 out at $0", u)
	}
}
