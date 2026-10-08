package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

const opencodeTestModel = "command-code/deepseek/deepseek-v4.1-flash"

func init() { addConformanceCase(opencodeConformance()) }

// opencodeConformance wires the opencode adapter to the shared suite: a fake
// CLI replaying the recorded fixtures, and the plan's own rate card.
func opencodeConformance() harnessCase {
	rates, _ := providers.RatesFor(opencodeTestModel)
	return harnessCase{
		Name:          "opencode",
		Model:         opencodeTestModel,
		ResumeFlag:    "-s",
		ReadOnlyFlag:  "wingman-readonly",
		BuildFlag:     "--auto",
		LimitByMarker: true,
		Rates:         rates,
		New: func(t *testing.T, fixture string) (Harness, func() []harnessInvocation, string) {
			bin, attempts := scriptedOpencode(t, fixture)
			home := t.TempDir()
			return OpencodeHarness{Bin: bin, Key: conformanceKey, Home: home}, func() []harnessInvocation {
				var runs []harnessInvocation
				for _, a := range attempts() {
					runs = append(runs, harnessInvocation{Args: a.args, Stdin: a.stdin})
				}
				return runs
			}, home
		},
	}
}

// opencodeAttempt is one invocation the fake opencode recorded.
type opencodeAttempt struct {
	args   []string
	env    []string
	stdin  string
	rules  string
	config string
	// files are the planted global config and session db the run could see.
	files string
}

// scriptedOpencode writes a fake opencode that replays the named recorded
// fixture: its events on stdout, its stderr and its exit code. The "unpriced"
// fixture is the normal run with opencode's own cost figures removed.
func scriptedOpencode(t *testing.T, fixture string) (bin string, attempts func() []opencodeAttempt) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake opencode is a shell script, which Windows cannot execute")
	}
	dir := t.TempDir()
	abs := func(name string) string {
		p, err := filepath.Abs(filepath.Join("testdata", "opencode", name))
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
		if err := os.WriteFile(ndjson, unpricedOpencodeEvents(t, abs("normal.ndjson")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exitRaw, err := os.ReadFile(abs(replay + ".exit"))
	if err != nil {
		t.Fatal(err)
	}
	bin, logDir := filepath.Join(dir, "opencode"), filepath.Join(dir, "attempts")
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
		"cat \"$HOME/rules.md\" > " + log + ".rules 2>/dev/null || echo MISSING > " + log + ".rules\n" +
		"cat \"$OPENCODE_CONFIG\" > " + log + ".config 2>/dev/null || echo MISSING > " + log + ".config\n" +
		"ls \"$XDG_CONFIG_HOME/opencode/opencode.json\" \"$XDG_DATA_HOME/opencode/opencode.db\" > " + log + ".files 2>/dev/null\n" +
		"cat " + q(ndjson) + "\n" +
		"cat " + q(abs(replay+".stderr")) + " >&2\n" +
		"exit " + strings.TrimSpace(string(exitRaw)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, func() []opencodeAttempt {
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
		var got []opencodeAttempt
		for _, base := range bases {
			got = append(got, opencodeAttempt{
				args:   strings.Split(strings.TrimRight(read(base, ".args"), "\x00"), "\x00"),
				env:    strings.Split(strings.TrimSpace(read(base, ".env")), "\n"),
				stdin:  read(base, ".stdin"),
				rules:  read(base, ".rules"),
				config: read(base, ".config"),
				files:  read(base, ".files"),
			})
		}
		return got
	}
}

// unpricedOpencodeEvents is the recorded events at path with every part's cost
// figure removed.
func unpricedOpencodeEvents(t *testing.T, path string) []byte {
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
		if part, ok := ev["part"].(map[string]any); ok {
			delete(part, "cost")
		}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		out = append(append(out, b...), '\n')
	}
	return out
}

// TestOpencodeAgentResumesUnderOneHomeWithTheKeyInItsEnvironment asserts a
// later round resumes the earlier session from the same HOME and XDG
// directories, that the key reaches opencode in its environment under the
// variable the config names, and that the rules head is the config's first
// instruction file, removed on a round with none.
func TestOpencodeAgentResumesUnderOneHomeWithTheKeyInItsEnvironment(t *testing.T) {
	bin, attempts := scriptedOpencode(t, "normal")
	home := t.TempDir()
	agent := OpencodeHarness{Bin: bin, Key: "oc-secret", Home: home}.Agent(ProfileBuild, opencodeTestModel)
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
		t.Fatalf("opencode ran %d times, want 2", len(got))
	}
	if i := slices.Index(got[1].args, "-s"); i < 0 || i+1 >= len(got[1].args) || got[1].args[i+1] != "ses_1" {
		t.Errorf("round 2 args %v do not resume ses_1", got[1].args)
	}
	rulesPath := filepath.Join(home, "rules.md")
	for i, a := range got {
		if want := []string{"# Rules\n", "MISSING\n"}[i]; a.rules != want {
			t.Errorf("attempt %d rules = %q, want %q", i, a.rules, want)
		}
		var config opencodeConfigDoc
		if err := json.Unmarshal([]byte(a.config), &config); err != nil {
			t.Fatalf("attempt %d config %q: %v", i, a.config, err)
		}
		claudeMD := filepath.Join(dir, "CLAUDE.md")
		wantInstructions := [][]string{{rulesPath, claudeMD}, {claudeMD}}[i]
		if !slices.Equal(config.Instructions, wantInstructions) {
			t.Errorf("attempt %d instructions = %v, want %v", i, config.Instructions, wantInstructions)
		}
		provider := config.Provider["command-code"]
		if provider.Options.APIKey != "{env:COMMAND_CODE_API_KEY}" || provider.Options.BaseURL != "https://api.commandcode.ai/provider/v1" {
			t.Errorf("attempt %d provider = %+v, want the plan's endpoint and the key named, not held", i, provider)
		}
		if _, ok := provider.Models["deepseek/deepseek-v4.1-flash"]; !ok {
			t.Errorf("attempt %d provider models %v lack the run's model", i, provider.Models)
		}
		if config.Share != "disabled" {
			t.Errorf("attempt %d share = %q, want sharing disabled", i, config.Share)
		}
		if strings.Contains(a.config, "oc-secret") {
			t.Errorf("attempt %d config holds the key", i)
		}
		for _, want := range []string{
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
			"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
			"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
			"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
			"OPENCODE_CONFIG=" + filepath.Join(home, "opencode.json"),
			"OPENCODE_DISABLE_CLAUDE_CODE=1",
			"OPENCODE_DISABLE_PROJECT_CONFIG=1",
			"OPENCODE_DISABLE_AUTOUPDATE=1",
			"OPENCODE_DISABLE_MODELS_FETCH=1",
			"COMMAND_CODE_API_KEY=oc-secret",
		} {
			if !slices.Contains(a.env, want) {
				t.Errorf("attempt %d env lacks %s", i, want)
			}
		}
		for _, kv := range a.env {
			if strings.HasPrefix(kv, "COMMANDCODE_API_KEY=") {
				t.Errorf("attempt %d inherited the runner's secret variable: %s", i, kv)
			}
		}
		if j := slices.Index(a.args, "-m"); j < 0 || a.args[j+1] != opencodeTestModel {
			t.Errorf("attempt %d args %v do not name the model", i, a.args)
		}
		if j := slices.Index(a.args, "--dir"); j < 0 || a.args[j+1] != dir || !slices.Contains(a.args, "--pure") {
			t.Errorf("attempt %d args %v do not run pure in the worktree", i, a.args)
		}
	}
}

// TestOpencodeAgentClearsAPlantedGlobalConfigAndKeepsItsSessions asserts
// whatever an earlier round left under HOME — config, agents, plugins, any
// other file — is gone before opencode starts, while the session store a
// resume reads stays.
func TestOpencodeAgentClearsAPlantedGlobalConfigAndKeepsItsSessions(t *testing.T) {
	bin, attempts := scriptedOpencode(t, "normal")
	home := t.TempDir()
	global := filepath.Join(home, ".config", "opencode", "opencode.json")
	db := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	planted := []string{global,
		filepath.Join(home, ".opencode", "opencode.json"),
		filepath.Join(home, ".opencode", "agent", "x.md"),
		filepath.Join(home, ".opencode", "plugin", "p.js"),
		filepath.Join(home, ".local", "share", "stray"),
		filepath.Join(home, "stray"),
	}
	for _, path := range append([]string{db}, planted...) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errBuf strings.Builder
	agent := OpencodeHarness{Bin: bin, Key: "k", Home: home}.Agent(ProfileReview, opencodeTestModel)
	if err := agent.Run(context.Background(), t.TempDir(), "ses_1", "p", "", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	got := attempts()
	if len(got) != 1 {
		t.Fatalf("opencode ran %d times, want 1", len(got))
	}
	if strings.Contains(got[0].files, global) || !strings.Contains(got[0].files, db) {
		t.Fatalf("opencode saw %q, want the session db and no planted global config", got[0].files)
	}
	for _, path := range planted {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the run's start: %v", path, err)
		}
	}
	if i := slices.Index(got[0].args, "-s"); i < 0 || got[0].args[i+1] != "ses_1" {
		t.Fatalf("args %v do not resume ses_1", got[0].args)
	}
}

// TestRemoveAllExceptRemovesASymlinkOnTheWay asserts a symlink standing where
// the kept path's directory should be is removed, not followed.
func TestRemoveAllExceptRemovesASymlinkOnTheWay(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	if err := removeAllExcept(home, filepath.Join(home, opencodeSessionDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a file behind the symlink was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".local")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the symlink survived: %v", err)
	}
}

// TestOpencodeAgentFailsWhenHomeCannotBeCleared asserts a run whose HOME
// keeps a file it cannot remove does not start opencode.
func TestOpencodeAgentFailsWhenHomeCannotBeCleared(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a file from a read-only directory")
	}
	bin, attempts := scriptedOpencode(t, "normal")
	home := t.TempDir()
	locked := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "opencode.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	var out, errBuf strings.Builder
	agent := OpencodeHarness{Bin: bin, Key: "k", Home: home}.Agent(ProfileReview, opencodeTestModel)
	if err := agent.Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err == nil {
		t.Fatal("the run started with HOME uncleared")
	}
	if len(attempts()) != 0 {
		t.Fatal("opencode started with HOME uncleared")
	}
}

// TestOpencodeAgentFailsWhenOpencodeFallsBack asserts a run whose named agent
// opencode did not find, and so replaced with its default, fails, whichever
// stream carries the warning.
func TestOpencodeAgentFailsWhenOpencodeFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake opencode is a shell script, which Windows cannot execute")
	}
	for name, redirect := range map[string]string{"stderr": " >&2", "stdout": ""} {
		t.Run(name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "opencode")
			script := "#!/bin/sh\ncat > /dev/null\necho '! agent \"wingman-readonly\" not found. Falling back to default agent'" + redirect + "\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			var out, errBuf strings.Builder
			agent := OpencodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}.Agent(ProfileReview, opencodeTestModel)
			if err := agent.Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); !errors.Is(err, errOpencodeFallback) {
				t.Fatalf("err = %v, want the fallback refused", err)
			}
		})
	}
}

// TestMarkerWriterSeesAMarkerSplitAcrossWrites asserts the fallback warning is
// caught when a copy delivers it in two writes, and everything passes through.
func TestMarkerWriterSeesAMarkerSplitAcrossWrites(t *testing.T) {
	var out strings.Builder
	w := &markerWriter{w: &out, marker: []byte(opencodeFallbackMarker)}
	for _, chunk := range []string{"! agent not found. Falling back", " to default agent\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if !w.seen || !strings.Contains(out.String(), opencodeFallbackMarker) {
		t.Fatalf("seen = %v, out = %q, want the split marker seen and passed through", w.seen, out.String())
	}
}

// TestBuildFailsTheRoundWhenOpencodeFallsBack asserts the refused fallback
// stops a build as an ordinary agent failure.
func TestBuildFailsTheRoundWhenOpencodeFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake opencode is a shell script, which Windows cannot execute")
	}
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat > /dev/null\necho 'Falling back to default agent' >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	deps, _, reported := testDeps(validObjects(), nil)
	deps.Agent = OpencodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}.Agent(ProfileBuild, opencodeTestModel)
	c := testConfig(t, initRepo(t))
	c.Model = opencodeTestModel
	c.ReviewModels = []string{"p/r"}
	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded after opencode fell back to its default agent")
	}
	if rec := reported.last(t); rec.Outcome != OutcomeAgentFailed {
		t.Fatalf("outcome = %s, want an agent failure", rec.Outcome)
	}
}

// TestOpencodeAgentChoosesTheProfile asserts only the build profile runs with
// --auto, and plan and review select the read-only agent, whose edit, bash and
// task permissions the config denies (AC2).
func TestOpencodeAgentChoosesTheProfile(t *testing.T) {
	bin, attempts := scriptedOpencode(t, "readonly")
	harness := OpencodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}
	for name, tt := range map[string]struct {
		profile Profile
		auto    bool
	}{
		"build":  {ProfileBuild, true},
		"plan":   {ProfilePlan, false},
		"review": {ProfileReview, false},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(attempts())
			var out, errBuf strings.Builder
			if err := harness.Agent(tt.profile, opencodeTestModel).Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
				t.Fatal(err)
			}
			got := attempts()[before:]
			if len(got) != 1 {
				t.Fatalf("opencode ran %d times, want 1", len(got))
			}
			args := got[0].args
			readOnly := slices.Index(args, "--agent") >= 0 && args[slices.Index(args, "--agent")+1] == "wingman-readonly"
			if slices.Contains(args, "--auto") != tt.auto || readOnly == tt.auto {
				t.Fatalf("args = %v, want --auto %v and the read-only agent %v", args, tt.auto, !tt.auto)
			}
			var config opencodeConfigDoc
			if err := json.Unmarshal([]byte(got[0].config), &config); err != nil {
				t.Fatal(err)
			}
			agent := config.Agent["wingman-readonly"]
			for _, tool := range []string{"edit", "bash", "task"} {
				if agent.Mode != "primary" || agent.Permission[tool] != "deny" {
					t.Fatalf("read-only agent = %+v, want a primary agent denying %s", agent, tool)
				}
			}
			for _, tool := range []string{"edit", "bash", "task"} {
				if denied := config.Permission[tool] == "deny"; denied == tt.auto {
					t.Fatalf("top-level permission = %v, want %s denied %v", config.Permission, tool, !tt.auto)
				}
			}
		})
	}
}

// TestOpencodeAgentRefusesAModelOfAnotherPlan asserts the adapter runs only a
// named model of its plan, before starting opencode.
func TestOpencodeAgentRefusesAModelOfAnotherPlan(t *testing.T) {
	for _, model := range []string{"other/model", "command-code/"} {
		t.Run(model, func(t *testing.T) {
			bin, attempts := scriptedOpencode(t, "normal")
			agent := OpencodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}.Agent(ProfileBuild, model)
			var out, errBuf strings.Builder
			if err := agent.Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err == nil {
				t.Fatal("the opencode agent ran a model of another plan")
			}
			if len(attempts()) != 0 {
				t.Fatal("opencode started for a model of another plan")
			}
		})
	}
}

// TestOpencodeAgentNeedsAHomeAndAKey asserts a harness with no HOME refuses to
// run, and one with no key is not ready (AC7).
func TestOpencodeAgentNeedsAHomeAndAKey(t *testing.T) {
	var out, errBuf strings.Builder
	err := OpencodeAgent{Bin: "opencode", Key: "k", Model: opencodeTestModel}.Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("err = %v, want the missing home refused", err)
	}
	if err := (OpencodeHarness{}).Ready(); err == nil || !strings.Contains(err.Error(), "COMMANDCODE_API_KEY") {
		t.Fatalf("Ready = %v, want the missing key named", err)
	}
	if err := (OpencodeHarness{Key: "k"}).Ready(); err != nil {
		t.Fatalf("Ready = %v with a key", err)
	}
}

// TestTranslateOpencodeEvents pins the event translation: the session once,
// one step per step_finish timed by its event with reasoning counted in its
// output, one text part per non-empty text event, other events ignored, and an error
// echoed to stderr.
func TestTranslateOpencodeEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"step_start","timestamp":1,"sessionID":"ses_1","part":{"type":"step-start"}}`,
		`{"type":"tool_use","timestamp":2,"sessionID":"ses_1","part":{"type":"tool","tool":"write","state":{"output":"tool output"}}}`,
		`{"type":"step_finish","timestamp":1791426695160,"sessionID":"ses_1","part":{"type":"step-finish",` +
			`"tokens":{"total":19,"input":10,"output":3,"reasoning":2,"cache":{"write":5,"read":4}},"cost":0.5}}`,
		`{"type":"text","timestamp":3,"sessionID":"ses_1","part":{"type":"text","text":"last"}}`,
		`{"type":"text","timestamp":3,"sessionID":"ses_1","part":{"type":"text","text":""}}`,
		`{"type":"step_finish","timestamp":1791426697437,"sessionID":"ses_1","part":{"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"write":0,"read":0}},"cost":0}}`,
		`{"type":"error","timestamp":4,"sessionID":"ses_1","error":{"name":"APIError","data":{"message":"402 Insufficient balance"}}}`,
	}, "\n")
	var stdout, stderr strings.Builder
	if err := translateOpencodeEvents(strings.NewReader(stream), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if got, _ := SessionID(strings.NewReader(out)); got != "ses_1" {
		t.Errorf("session = %q, want ses_1", got)
	}
	if n := strings.Count(out, `"sessionID"`); n != 1 {
		t.Errorf("the session is reported %d times, want once", n)
	}
	if u, _ := SumUsage(strings.NewReader(out)); u != (Usage{Input: 11, Output: 6, Reasoning: 2, CacheRead: 4, CacheWrite: 5, Cost: 0.5, Steps: 2}) {
		t.Errorf("usage = %+v, want the two steps with reasoning in output", u)
	}
	if !strings.Contains(out, `"time":1791426695160`) {
		t.Errorf("the steps carry no time:\n%s", out)
	}
	if strings.Contains(out, "tool output") {
		t.Errorf("a tool event was translated:\n%s", out)
	}
	if got, _ := FinalText(strings.NewReader(out)); got != "last" {
		t.Errorf("final text = %q, want last", got)
	}
	if outcome, reason := (OpencodeHarness{}).Classify(stderr.String(), errors.New("exit status 1")); outcome != OutcomeBudgetStop || reason != StopAllowanceExhausted {
		t.Errorf("stderr %q classified %s/%s, want the allowance stop", stderr.String(), outcome, reason)
	}
}

// TestBuildRunsTheOpencodeHarnessFromTheRecordedFixtures drives the real
// adapter against the recorded normal run: the recorded usage, final text and
// session reach the Step, priced from the plan's card at the peak hour it ran in.
func TestBuildRunsTheOpencodeHarnessFromTheRecordedFixtures(t *testing.T) {
	bin, _ := scriptedOpencode(t, "normal")
	deps, completions, reported := testDeps(validObjects(), nil)
	orders := planOrders(map[string][]string{"command-code": {"opencode"}, "p": {"p"}})
	deps.Agent = newRouter(orders, ProfileBuild, OpencodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}, fakeHarness{})
	c := testConfig(t, initRepo(t))
	c.Model = opencodeTestModel
	c.ReviewModels = []string{"p/r"}

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	step := reported.last(t).Steps[0]
	tokens := step.Tokens
	tokens.Cost = 0
	if want := (Usage{Input: 5322, Output: 128, Reasoning: 17, CacheRead: 9728, Steps: 2}); tokens != want {
		t.Fatalf("tokens = %+v, want %+v", tokens, want)
	}
	// 5322 x $0.30 + 128 x $1.20 + 9728 x $0.006, per million: peak, 02:31 UTC on a Thursday.
	if !approxEqual(step.Tokens.Cost, 0.001808568) {
		t.Fatalf("cost = %v, want the peak card's 0.001808568", step.Tokens.Cost)
	}
	events := string(completions[step.CompletionsObject])
	for _, want := range []string{`"sessionID":"ses_ee6a5fca3ffeEzBTpNjqW6hl9S"`, `"text":"done"`} {
		if !strings.Contains(events, want) {
			t.Errorf("translated events do not carry %s:\n%s", want, events)
		}
	}
}

// TestBuildStopsOnABadKey drives the recorded bad-key run through a build and
// asserts it stops as an agent failure (AC3).
func TestBuildStopsOnABadKey(t *testing.T) {
	bin, _ := scriptedOpencode(t, "badkey")
	deps, _, reported := testDeps(validObjects(), nil)
	deps.Agent = OpencodeHarness{Bin: bin, Key: "bad-key", Home: t.TempDir()}.Agent(ProfileBuild, opencodeTestModel)
	c := testConfig(t, initRepo(t))
	c.Model = opencodeTestModel
	c.ReviewModels = []string{"p/r"}

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded on a bad key")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeAgentFailed || rec.StopReason != StopAgentExit {
		t.Fatalf("record = %s/%s, want an ordinary agent failure", rec.Outcome, rec.StopReason)
	}
}

// TestRouterRunsOmpAndFallsBackToOpencode asserts the embedded plan
// configuration runs omp first, and the opencode harness when omp is not ready.
func TestRouterRunsOmpAndFallsBackToOpencode(t *testing.T) {
	for name, tt := range map[string]struct {
		ompKey            string
		wantOmp, wantOpen int
	}{
		"omp ready":     {"k", 1, 0},
		"omp not ready": {"", 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			ompBin, ompRuns := scriptedOmp(t, "normal")
			ocBin, ocRuns := scriptedOpencode(t, "normal")
			omp := OmpHarness{Bin: ompBin, Key: tt.ompKey, Home: t.TempDir()}
			oc := OpencodeHarness{Bin: ocBin, Key: "k", Home: t.TempDir()}
			var out, errBuf strings.Builder
			if err := NewRouter(ProfileBuild, omp, oc).WithModel(opencodeTestModel).Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
				t.Fatal(err)
			}
			if len(ompRuns()) != tt.wantOmp || len(ocRuns()) != tt.wantOpen {
				t.Fatalf("omp ran %d and opencode %d times, want %d and %d", len(ompRuns()), len(ocRuns()), tt.wantOmp, tt.wantOpen)
			}
		})
	}
}

// TestWorkflowsInstallThePinnedOpencode asserts the workflows that run an
// agent install opencode from its lockfile with npm and check the pinned version.
func TestWorkflowsInstallThePinnedOpencode(t *testing.T) {
	for _, name := range []string{"model.yml", "plan-smoke.yml", "review-smoke.yml"} {
		b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		yml := string(b)
		for _, want := range []string{
			"uses: actions/setup-node@",
			"npm ci --prefix tools/opencode",
			"tools/opencode/node_modules/.bin",
			`jq -r '.dependencies["opencode-ai"]' tools/opencode/package.json`,
			"COMMANDCODE_API_KEY: ${{ secrets.COMMANDCODE_API_KEY }}",
		} {
			if !strings.Contains(yml, want) {
				t.Errorf("%s has no %q", name, want)
			}
		}
	}
}
