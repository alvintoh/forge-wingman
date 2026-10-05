package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// commandCodeAttempt is one invocation the fake CLI recorded.
type commandCodeAttempt struct {
	args  []string
	env   []string
	auth  string
	stdin string
}

// scriptedCommandCode writes a fake command-code CLI that replays the named
// recorded fixture: its frames on stdout, its stderr, and its recorded exit code.
func scriptedCommandCode(t *testing.T, fixture string) (bin string, attempts func() []commandCodeAttempt) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake command-code CLI is a shell script, which Windows cannot execute")
	}
	abs := func(name string) string {
		p, err := filepath.Abs(filepath.Join("testdata", "commandcode", name))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ndjson, stderrPath := abs(fixture+".ndjson"), abs(fixture+".stderr")
	exitRaw, err := os.ReadFile(abs(fixture + ".exit"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin, logDir := filepath.Join(dir, "cmd"), filepath.Join(dir, "attempts")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	q := func(s string) string { return "'" + s + "'" }
	script := "#!/bin/sh\n" +
		"n=$(ls " + q(logDir) + " 2>/dev/null | wc -l)\n" +
		"printf '%s\\0' \"$@\" > " + q(logDir+"/") + "$n.args\n" +
		"env > " + q(logDir+"/") + "$n.env\n" +
		"cat > " + q(logDir+"/") + "$n.stdin\n" +
		"cat \"$HOME/.commandcode/auth.json\" > " + q(logDir+"/") + "$n.auth 2>/dev/null || echo MISSING > " + q(logDir+"/") + "$n.auth\n" +
		"cat " + q(ndjson) + "\n" +
		"cat " + q(stderrPath) + " >&2\n" +
		"exit " + strings.TrimSpace(string(exitRaw)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, func() []commandCodeAttempt {
		entries, err := os.ReadDir(logDir)
		if err != nil {
			t.Fatal(err)
		}
		var bases []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".args") {
				bases = append(bases, strings.TrimSuffix(e.Name(), ".args"))
			}
		}
		sort.Strings(bases)
		var got []commandCodeAttempt
		for _, base := range bases {
			args, _ := os.ReadFile(filepath.Join(logDir, base+".args"))
			env, _ := os.ReadFile(filepath.Join(logDir, base+".env"))
			auth, _ := os.ReadFile(filepath.Join(logDir, base+".auth"))
			stdin, _ := os.ReadFile(filepath.Join(logDir, base+".stdin"))
			got = append(got, commandCodeAttempt{
				args:  strings.Split(strings.TrimRight(string(args), "\x00"), "\x00"),
				env:   strings.Split(strings.TrimSpace(string(env)), "\n"),
				auth:  strings.TrimSpace(string(auth)),
				stdin: string(stdin),
			})
		}
		return got
	}
}

// TestClassifyAgentFailureDispatchesToTheAgentsHarness asserts the failure is
// classified through the harness that ran the model, not the generic markers.
func TestClassifyAgentFailureDispatchesToTheAgentsHarness(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh to produce an exit code")
	}
	path := filepath.Join(t.TempDir(), "stderr")
	if err := os.WriteFile(path, []byte("no marker matches this"), 0o600); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(ProfileBuild,
		CommandCodeHarness{Bin: "cmd", Key: "k", OptIn: true},
	).WithModel("command-code/x")
	err := exec.Command("sh", "-c", "exit 5").Run()
	if outcome, reason := classifyAgentFailure(router, path, err); outcome != OutcomeBudgetStop || reason != StopAllowanceExhausted {
		t.Fatalf("classifyAgentFailure = %s/%s, want the harness's allowance classification", outcome, reason)
	}
}

// TestTranslateCommandCodeFrames pins the frame-to-event translation: the
// session, one step per turn, one text part per finished message, and the
// result line's text as the final answer.
func TestTranslateCommandCodeFrames(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"event","event":{"type":"run_start","sessionId":"ses_1"}}`,
		`{"type":"event","event":{"type":"text_delta","delta":"fir"}}`,
		`{"type":"event","event":{"type":"message_end","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"first"},{"type":"tool_result","text":"tool output"}]}}`,
		`{"type":"event","event":{"type":"turn_end","turnNumber":1,"usage":{"inputTokens":10,"outputTokens":2,"cacheReadTokens":3,"cacheWriteTokens":4}}}`,
		`{"type":"event","event":{"type":"message_end","content":[{"type":"text","text":"second"}]}}`,
		`{"type":"event","event":{"type":"run_error","error":{"name":"TransportError","message":"boom"}}}`,
		`{"type":"result","subtype":"success","sessionId":"ses_1","finalText":"authoritative"}`,
	}, "\n")
	var stdout, stderr strings.Builder
	if err := translateCommandCodeFrames(strings.NewReader(stream), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if got, _ := SessionID(strings.NewReader(out)); got != "ses_1" {
		t.Errorf("session = %q, want ses_1", got)
	}
	if u, _ := SumUsage(strings.NewReader(out)); u != (Usage{Input: 10, Output: 2, CacheRead: 3, CacheWrite: 4, Steps: 1}) {
		t.Errorf("usage = %+v, want the turn's own counts", u)
	}
	if !strings.Contains(out, `"text":"first"`) || !strings.Contains(out, `"text":"second"`) ||
		strings.Contains(out, `"text":"fir"`) || strings.Contains(out, "tool output") {
		t.Errorf("want one text part per finished message and none per delta:\n%s", out)
	}
	if got, _ := FinalText(strings.NewReader(out)); got != "authoritative" {
		t.Errorf("final text = %q, want the result line's", got)
	}
	if !strings.Contains(stderr.String(), "TransportError: boom") {
		t.Errorf("stderr %q does not carry the run_error", stderr.String())
	}
}

// TestTranslateCommandCodeFramesFindsTheSessionInEitherFrame asserts the
// session id survives when only one of run_start and the result line carries
// it — the docs make the result line's optional.
func TestTranslateCommandCodeFramesFindsTheSessionInEitherFrame(t *testing.T) {
	for name, stream := range map[string]string{
		"run_start": `{"type":"event","event":{"type":"run_start","sessionId":"ses_1"}}` + "\n" +
			`{"type":"result","subtype":"error","finalText":""}`,
		"result": `{"type":"event","event":{"type":"run_start"}}` + "\n" +
			`{"type":"result","subtype":"success","sessionId":"ses_1","finalText":"done"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if err := translateCommandCodeFrames(strings.NewReader(stream), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if got, _ := SessionID(strings.NewReader(stdout.String())); got != "ses_1" {
				t.Fatalf("session = %q, want ses_1 from the %s frame", got, name)
			}
		})
	}
}

// TestCommandCodeAgentResumesTheSessionUnderOneHome asserts a later round
// resumes the earlier session from the same HOME, and that the prompt travels
// on stdin, never as an argument.
func TestCommandCodeAgentResumesTheSessionUnderOneHome(t *testing.T) {
	bin, attempts := scriptedCommandCode(t, "normal")
	home := t.TempDir()
	agent := CommandCodeHarness{Bin: bin, Key: "k", OptIn: true, Home: home}.Agent(ProfileBuild, "command-code/x")
	prompt := strings.Repeat("rule line\n", 20000)
	dir := t.TempDir()
	for _, session := range []string{"", "ses_1"} {
		var out, errBuf strings.Builder
		if err := agent.Run(context.Background(), dir, session, prompt, &out, &errBuf); err != nil {
			t.Fatal(err)
		}
	}
	got := attempts()
	if len(got) != 2 {
		t.Fatalf("the CLI ran %d times, want 2", len(got))
	}
	if _, err := os.Stat(filepath.Join(home, ".commandcode", "auth.json")); !os.IsNotExist(err) {
		t.Errorf("the auth file outlived the run (stat err %v)", err)
	}
	if slices.Contains(got[0].args, "--resume") {
		t.Errorf("round 1 args %v resume a session it never had", got[0].args)
	}
	if i := slices.Index(got[1].args, "--resume"); i < 0 || i+1 >= len(got[1].args) || got[1].args[i+1] != "ses_1" {
		t.Errorf("round 2 args %v do not resume ses_1", got[1].args)
	}
	for i, a := range got {
		if a.stdin != prompt {
			t.Errorf("attempt %d stdin carries %d bytes, want the %d-byte prompt", i, len(a.stdin), len(prompt))
		}
		if slices.Contains(a.args, prompt) {
			t.Errorf("attempt %d carries the prompt as an argument", i)
		}
		if a.auth != `{"apiKey":"k"}` {
			t.Errorf("attempt %d saw auth file %q, want the key written for that run", i, a.auth)
		}
		if !slices.Contains(a.env, "HOME="+home) {
			t.Errorf("attempt %d does not run under the harness's own HOME", i)
		}
		for _, want := range []string{"--skip-onboarding", "--no-auto-update"} {
			if !slices.Contains(a.args, want) {
				t.Errorf("attempt %d args %v lack %s", i, a.args, want)
			}
		}
	}
}

// TestCommandCodeAgentNeedsAHome asserts a harness with no HOME refuses to
// run rather than reading the job's own credentials.
func TestCommandCodeAgentNeedsAHome(t *testing.T) {
	var out, errBuf strings.Builder
	err := CommandCodeAgent{Bin: "cmd", Key: "k"}.Run(context.Background(), t.TempDir(), "", "p", &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("err = %v, want the missing home refused", err)
	}
}

// TestBuildRunsTheCommandCodeHarnessFromTheRecordedFixtures drives the real
// adapter against the recorded normal run and asserts the recorded usage,
// final text and session reach the Step (AC1).
func TestBuildRunsTheCommandCodeHarnessFromTheRecordedFixtures(t *testing.T) {
	const model = "command-code/deepseek/deepseek-v4.1-flash"
	bin, attempts := scriptedCommandCode(t, "normal")
	t.Setenv("OPENCODE_API_KEY", "opencode-secret")
	t.Setenv("COMMANDCODE_API_KEY", "command-secret")

	deps, completions, reported := testDeps(validObjects(), nil)
	deps.Agent = NewRouter(ProfileBuild,
		CommandCodeHarness{Bin: bin, Key: "command-secret", OptIn: true, Home: t.TempDir()},
		OpencodeHarness{Bin: "opencode"},
	)
	c := testConfig(t, initRepo(t))
	c.Model = model
	c.ReviewModel = "opencode/space-bunny-free"

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	rec := reported.last(t)
	if len(rec.Steps) != 1 {
		t.Fatalf("steps = %+v, want the one build attempt", rec.Steps)
	}
	step := rec.Steps[0]
	wantTokens := Usage{Input: 45386, Output: 384, CacheRead: 35072, Steps: 3}
	if step.Model != model || step.Phase != PhaseBuild || step.Tokens != wantTokens {
		t.Fatalf("step = %+v, want model %s and tokens %+v", step, model, wantTokens)
	}
	events := string(completions[step.CompletionsObject])
	for _, want := range []string{
		`"sessionID":"f6d435ce-5392-47aa-8e48-9cf281e162b3"`,
		`"type":"step_finish"`,
		"Final contents of `hello.txt`",
	} {
		if !strings.Contains(events, want) {
			t.Errorf("translated events do not carry %s:\n%s", want, events)
		}
	}

	got := attempts()
	if len(got) != 1 {
		t.Fatalf("the CLI ran %d times, want 1", len(got))
	}
	for _, want := range []string{"--yolo", "--output-format", "json", "deepseek/deepseek-v4.1-flash", "--max-turns"} {
		if !slices.Contains(got[0].args, want) {
			t.Errorf("args %v lack %q", got[0].args, want)
		}
	}
	if slices.Contains(got[0].args, model) {
		t.Errorf("args %v carry our prefixed id, want the CLI's own", got[0].args)
	}
	if got[0].auth != `{"apiKey":"command-secret"}` {
		t.Errorf("auth file = %q, want the key written under the run's own HOME", got[0].auth)
	}
	for _, kv := range got[0].env {
		if strings.HasPrefix(kv, "OPENCODE_API_KEY=") || strings.HasPrefix(kv, "COMMANDCODE_API_KEY=") {
			t.Errorf("the command-code process inherited a key in its environment: %s", kv)
		}
	}
}

// TestCommandCodeAgentChoosesTheProfileFlag asserts the command line carries
// the restricted flag only for plan and review (AC2).
func TestCommandCodeAgentChoosesTheProfileFlag(t *testing.T) {
	bin, attempts := scriptedCommandCode(t, "readonly")
	harness := CommandCodeHarness{Bin: bin, Key: "k", OptIn: true, Home: t.TempDir()}
	for name, tt := range map[string]struct {
		profile Profile
		want    string
		dropped string
	}{
		"build":  {ProfileBuild, "--yolo", "--plan"},
		"plan":   {ProfilePlan, "--plan", "--yolo"},
		"review": {ProfileReview, "--plan", "--yolo"},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(attempts())
			var out, errBuf strings.Builder
			if err := harness.Agent(tt.profile, "command-code/x").Run(context.Background(), t.TempDir(), "", "prompt", &out, &errBuf); err != nil {
				t.Fatal(err)
			}
			got := attempts()[before:]
			if len(got) != 1 || !slices.Contains(got[0].args, tt.want) || slices.Contains(got[0].args, tt.dropped) {
				t.Fatalf("args = %v, want %s and not %s", got, tt.want, tt.dropped)
			}
		})
	}
}

// TestPlanSmokeRefusesUnderTheCommandCodePlanProfile replays the recorded
// read-only run and confirms by effect that nothing was written (AC2).
func TestPlanSmokeRefusesUnderTheCommandCodePlanProfile(t *testing.T) {
	bin, _ := scriptedCommandCode(t, "readonly")
	harness := CommandCodeHarness{Bin: bin, Key: "k", OptIn: true, Home: t.TempDir()}
	res, err := PlanSmoke(context.Background(), harness.Agent(ProfilePlan, "command-code/x"), "command-code/x", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused {
		t.Fatalf("the plan profile left an effect: %+v", res)
	}
}

// TestCommandCodeFailureMappings matches the CLI's documented exit codes to the
// outcome each records; 5 and 10 are the limit-declined mapping (AC3).
func TestCommandCodeFailureMappings(t *testing.T) {
	want := map[int]struct {
		outcome Outcome
		reason  StopReason
	}{
		3:  {OutcomeAgentFailed, StopAgentExit},
		4:  {OutcomeAgentFailed, StopAgentExit},
		5:  {OutcomeBudgetStop, StopAllowanceExhausted},
		6:  {OutcomeInfraFailure, StopModelUnavailable},
		7:  {OutcomeInfraFailure, StopModelUnavailable},
		8:  {OutcomeAgentFailed, StopAgentExit},
		9:  {OutcomeAgentFailed, StopAgentExit},
		10: {OutcomeBudgetStop, StopAllowanceExhausted},
	}
	if len(commandCodeFailure) != len(want) {
		t.Fatalf("mapped %d exit codes, want %d", len(commandCodeFailure), len(want))
	}
	for code, w := range want {
		if got := commandCodeFailure[code]; got.outcome != w.outcome || got.reason != w.reason {
			t.Errorf("exit %d = %s/%s, want %s/%s", code, got.outcome, got.reason, w.outcome, w.reason)
		}
	}
}

// TestCommandCodeHarnessClassifiesTheBadKeyFixture classifies the recorded bad
// key run from its exit code (AC3).
func TestCommandCodeHarnessClassifiesTheBadKeyFixture(t *testing.T) {
	bin, _ := scriptedCommandCode(t, "badkey")
	harness := CommandCodeHarness{Bin: bin, Key: "bad-key", OptIn: true, Home: t.TempDir()}
	var out, errBuf strings.Builder
	err := harness.Agent(ProfileBuild, "command-code/x").Run(context.Background(), t.TempDir(), "", "prompt", &out, &errBuf)
	if err == nil {
		t.Fatal("the bad-key run reported success")
	}
	if outcome, reason := harness.Classify(errBuf.String(), err); outcome != OutcomeAgentFailed || reason != StopAgentExit {
		t.Fatalf("classified %s/%s, want the ordinary agent failure", outcome, reason)
	}
	if !strings.Contains(errBuf.String(), "Authentication failed") {
		t.Errorf("stderr %q does not carry the CLI's own auth error", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "TransportError") {
		t.Errorf("stderr %q does not carry the adapter's run_error echo", errBuf.String())
	}
}

// TestCommandCodeHarnessClassifiesLiveExitCodes exercises the exit-code path
// Classify takes before it falls back to stderr markers (AC3).
func TestCommandCodeHarnessClassifiesLiveExitCodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh to produce an exit code")
	}
	harness := CommandCodeHarness{}
	for _, tt := range []struct {
		code        int
		wantOutcome Outcome
		wantReason  StopReason
	}{
		{5, OutcomeBudgetStop, StopAllowanceExhausted},
		{6, OutcomeInfraFailure, StopModelUnavailable},
		{10, OutcomeBudgetStop, StopAllowanceExhausted},
	} {
		t.Run(strconv.Itoa(tt.code), func(t *testing.T) {
			err := exec.Command("sh", "-c", "exit "+strconv.Itoa(tt.code)).Run()
			if outcome, reason := harness.Classify("", err); outcome != tt.wantOutcome || reason != tt.wantReason {
				t.Fatalf("exit %d classified %s/%s, want %s/%s", tt.code, outcome, reason, tt.wantOutcome, tt.wantReason)
			}
		})
	}
}

// TestBuildStopsOnACommandCodeBadKey drives the recorded bad key run through a
// build and asserts it stops as an agent failure (AC3).
func TestBuildStopsOnACommandCodeBadKey(t *testing.T) {
	bin, _ := scriptedCommandCode(t, "badkey")
	deps, _, reported := testDeps(validObjects(), nil)
	deps.Agent = CommandCodeHarness{Bin: bin, Key: "bad-key", OptIn: true, Home: t.TempDir()}.Agent(ProfileBuild, "command-code/x")
	c := testConfig(t, initRepo(t))
	c.Model = "command-code/x"
	c.ReviewModel = "opencode/space-bunny-free"

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded on a bad key")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeAgentFailed || rec.StopReason != StopAgentExit {
		t.Fatalf("record = %s/%s, want an ordinary agent failure", rec.Outcome, rec.StopReason)
	}
}

// TestBuildRefusesACommandCodeModelWithoutTheOptIn asserts an un-opted-in
// proprietary harness stops the run before any agent starts (AC7).
func TestBuildRefusesACommandCodeModelWithoutTheOptIn(t *testing.T) {
	deps, _, reported := testDeps(validObjects(), &fakeAgent{})
	deps.Agent = NewRouter(ProfileBuild,
		CommandCodeHarness{Bin: "cmd"},
		OpencodeHarness{Bin: "opencode"},
	)
	c := testConfig(t, initRepo(t))
	c.Model = "command-code/deepseek-v4.1-flash"
	c.ReviewModel = "opencode/space-bunny-free"

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build ran a model whose harness is not opted in")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeStopped || rec.StopReason != StopModelInvalid {
		t.Fatalf("record = %s/%s, want a model-invalid stop", rec.Outcome, rec.StopReason)
	}
	if !strings.Contains(rec.StopDetail, "COMMAND_CODE_OPT_IN") {
		t.Fatalf("stop detail %q does not name the missing opt-in", rec.StopDetail)
	}
}

// TestBuildGatesEveryModelSlotOnTheOptIn asserts the opt-in gate covers the
// review model and, for a ticket that plans, every plan model (AC7).
func TestBuildGatesEveryModelSlotOnTheOptIn(t *testing.T) {
	for name, set := range map[string]func(*BuildConfig){
		"review": func(c *BuildConfig) { c.ReviewModel = "command-code/x" },
		"plan": func(c *BuildConfig) {
			c.Ticket.Size = "M"
			c.PlanModels = []string{"opencode/p", "command-code/x"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			deps, _, reported := testDeps(validObjects(), &fakeAgent{})
			deps.Agent = NewRouter(ProfileBuild, CommandCodeHarness{Bin: "cmd"}, OpencodeHarness{Bin: "opencode"})
			c := testConfig(t, initRepo(t))
			c.Model = "opencode/b"
			c.ReviewModel = "opencode/r"
			set(&c)
			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build ran with an un-opted-in harness in the slot")
			}
			if rec := reported.last(t); rec.StopReason != StopModelInvalid {
				t.Fatalf("stop = %s, want a model-invalid stop", rec.StopReason)
			}
		})
	}
}

// TestModelWorkflowInstallsAndAuthorisesTheCommandCodeHarness asserts the
// workflow carries the package, secret and opt-in the adapter needs (AC7).
func TestModelWorkflowInstallsAndAuthorisesTheCommandCodeHarness(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "model.yml"))
	if err != nil {
		t.Fatal(err)
	}
	yml := string(b)
	for _, want := range []string{
		"node-version: 22",
		"npm ci --prefix tools/command-code",
		"COMMANDCODE_API_KEY: ${{ secrets.COMMANDCODE_API_KEY }}",
		"COMMAND_CODE_OPT_IN: ${{ vars.COMMAND_CODE_OPT_IN }}",
	} {
		if !strings.Contains(yml, want) {
			t.Errorf("model.yml has no %q", want)
		}
	}
}
