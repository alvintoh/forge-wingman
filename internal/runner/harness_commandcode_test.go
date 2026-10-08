package runner

import (
	"context"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// commandCodeAttempt is one invocation the fake CLI recorded.
type commandCodeAttempt struct {
	args     []string
	env      []string
	auth     string
	stdin    string
	agents   string
	settings string
}

// scriptedCommandCode writes a fake command-code CLI that replays the named
// recorded fixture: its frames on stdout, its stderr, and its recorded exit code.
// Attempt n, counting from 0, also writes transcripts[n], when given and
// non-empty, as a session transcript under its HOME.
func scriptedCommandCode(t *testing.T, fixture string, transcripts ...string) (bin string, attempts func() []commandCodeAttempt) {
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
	bin, logDir, transcriptDir := filepath.Join(dir, "cmd"), filepath.Join(dir, "attempts"), filepath.Join(dir, "transcripts")
	for _, d := range []string{logDir, transcriptDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for i, body := range transcripts {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(transcriptDir, strconv.Itoa(i)+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	q := func(s string) string { return "'" + s + "'" }
	script := "#!/bin/sh\n" +
		"n=$(ls " + q(logDir) + " 2>/dev/null | grep -c '\\.args$')\n" +
		"printf '%s\\0' \"$@\" > " + q(logDir+"/") + "$n.args\n" +
		"env > " + q(logDir+"/") + "$n.env\n" +
		"cat > " + q(logDir+"/") + "$n.stdin\n" +
		"cat \"$HOME/.commandcode/auth.json\" > " + q(logDir+"/") + "$n.auth 2>/dev/null || echo MISSING > " + q(logDir+"/") + "$n.auth\n" +
		"cat \"$HOME/.commandcode/AGENTS.md\" > " + q(logDir+"/") + "$n.agents 2>/dev/null || echo MISSING > " + q(logDir+"/") + "$n.agents\n" +
		"cat \"$HOME/.commandcode/settings.json\" > " + q(logDir+"/") + "$n.settings 2>/dev/null || echo MISSING > " + q(logDir+"/") + "$n.settings\n" +
		"if [ -f " + q(transcriptDir+"/") + "$n.jsonl ]; then mkdir -p \"$HOME/.commandcode/projects/p\" && " +
		"cp " + q(transcriptDir+"/") + "$n.jsonl \"$HOME/.commandcode/projects/p/session-$n.jsonl\"; fi\n" +
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
			agents, _ := os.ReadFile(filepath.Join(logDir, base+".agents"))
			settings, _ := os.ReadFile(filepath.Join(logDir, base+".settings"))
			got = append(got, commandCodeAttempt{
				args:     strings.Split(strings.TrimRight(string(args), "\x00"), "\x00"),
				env:      strings.Split(strings.TrimSpace(string(env)), "\n"),
				auth:     strings.TrimSpace(string(auth)),
				stdin:    string(stdin),
				agents:   string(agents),
				settings: strings.TrimSpace(string(settings)),
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
	router := testRouter(ProfileBuild,
		CommandCodeHarness{Bin: "cmd", Key: "k"},
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
	agent := CommandCodeHarness{Bin: bin, Key: "k", Home: home}.Agent(ProfileBuild, "command-code/x")
	prompt := strings.Repeat("rule line\n", 20000)
	dir := t.TempDir()
	for _, tt := range []struct{ session, rules string }{
		{"", "# Rules\n"},
		{"ses_1", ""},
	} {
		var out, errBuf strings.Builder
		if err := agent.Run(context.Background(), dir, tt.session, prompt, tt.rules, &out, &errBuf); err != nil {
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
		// The rules head travels as the CLI's user memory, never stdin; an empty
		// rules removes the file so a later round cannot inherit the earlier one's.
		if want := []string{"# Rules\n", "MISSING\n"}[i]; a.agents != want {
			t.Errorf("attempt %d memory file = %q, want %q", i, a.agents, want)
		}
		if a.settings != `{"disableScratchpad":true}` {
			t.Errorf("attempt %d settings = %q, want the scratchpad disabled", i, a.settings)
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
	err := CommandCodeAgent{Bin: "cmd", Key: "k"}.Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("err = %v, want the missing home refused", err)
	}
}

// TestBuildRunsTheCommandCodeHarnessFromTheRecordedFixtures drives the real
// adapter against the recorded normal run and asserts the recorded usage, the
// transcript's cost, final text and session reach the Step (AC1). The plan
// prices this model, but the CLI's steps carry no time, so its billed figure
// stands.
func TestBuildRunsTheCommandCodeHarnessFromTheRecordedFixtures(t *testing.T) {
	const model = "command-code/deepseek/deepseek-v4.1-flash"
	bin, attempts := scriptedCommandCode(t, "normal", transcriptMessage("d666e9bd", "0.0030955680000000004"))
	t.Setenv("COMMANDCODE_API_KEY", "command-secret")

	deps, completions, reported := testDeps(validObjects(), nil)
	deps.Agent = testRouter(ProfileBuild,
		CommandCodeHarness{Bin: bin, Key: "command-secret", Home: t.TempDir()},
		fakeHarness{},
	)
	c := testConfig(t, initRepo(t))
	c.Model = model
	c.ReviewModels = []string{"p/r"}

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	rec := reported.last(t)
	if len(rec.Steps) != 1 {
		t.Fatalf("steps = %+v, want the one build attempt", rec.Steps)
	}
	step := rec.Steps[0]
	wantTokens := Usage{Input: 45386, Output: 384, CacheRead: 35072, Cost: 0.0030955680000000004, Steps: 3}
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
		if strings.HasPrefix(kv, "COMMANDCODE_API_KEY=") {
			t.Errorf("the command-code process inherited a key in its environment: %s", kv)
		}
	}
}

// TestCommandCodeAgentChoosesTheProfileFlag asserts the command line carries
// the restricted flag only for plan and review (AC2).
func TestCommandCodeAgentChoosesTheProfileFlag(t *testing.T) {
	bin, attempts := scriptedCommandCode(t, "readonly")
	harness := CommandCodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}
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
			if err := harness.Agent(tt.profile, "command-code/x").Run(context.Background(), t.TempDir(), "", "prompt", "", &out, &errBuf); err != nil {
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
	harness := CommandCodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}
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
	harness := CommandCodeHarness{Bin: bin, Key: "bad-key", Home: t.TempDir()}
	var out, errBuf strings.Builder
	err := harness.Agent(ProfileBuild, "command-code/x").Run(context.Background(), t.TempDir(), "", "prompt", "", &out, &errBuf)
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
	deps.Agent = CommandCodeHarness{Bin: bin, Key: "bad-key", Home: t.TempDir()}.Agent(ProfileBuild, "command-code/x")
	c := testConfig(t, initRepo(t))
	c.Model = "command-code/x"
	c.ReviewModels = []string{"p/r"}

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded on a bad key")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeAgentFailed || rec.StopReason != StopAgentExit {
		t.Fatalf("record = %s/%s, want an ordinary agent failure", rec.Outcome, rec.StopReason)
	}
}

// TestBuildRefusesACommandCodeModelWithoutTheKey asserts a harness without its
// key stops the run before any agent starts (AC7).
func TestBuildRefusesACommandCodeModelWithoutTheKey(t *testing.T) {
	deps, _, reported := testDeps(validObjects(), &fakeAgent{})
	deps.Agent = testRouter(ProfileBuild,
		CommandCodeHarness{Bin: "cmd"},
		fakeHarness{},
	)
	c := testConfig(t, initRepo(t))
	c.Model = "command-code/deepseek-v4.1-flash"
	c.ReviewModels = []string{"p/r"}

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build ran a model whose harness has no key")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeStopped || rec.StopReason != StopCredentialAbsent {
		t.Fatalf("record = %s/%s, want a credential-absent stop", rec.Outcome, rec.StopReason)
	}
	if !strings.Contains(rec.StopDetail, "COMMANDCODE_API_KEY") {
		t.Fatalf("stop detail %q does not name the missing key", rec.StopDetail)
	}
}

// TestBuildGatesEveryModelSlotOnTheKey asserts the missing-key gate covers the
// review model and, for a ticket that plans, every plan model (AC7).
func TestBuildGatesEveryModelSlotOnTheKey(t *testing.T) {
	for name, set := range map[string]func(*BuildConfig){
		"review": func(c *BuildConfig) { c.ReviewModels = []string{"command-code/x"} },
		"plan": func(c *BuildConfig) {
			c.Ticket.Size = "M"
			c.PlanModels = []string{"p/p", "command-code/x"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			deps, _, reported := testDeps(validObjects(), &fakeAgent{})
			deps.Agent = testRouter(ProfileBuild, CommandCodeHarness{Bin: "cmd"}, fakeHarness{})
			c := testConfig(t, initRepo(t))
			c.Model = "p/b"
			c.ReviewModels = []string{"p/r"}
			set(&c)
			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build ran with a keyless harness in the slot")
			}
			if rec := reported.last(t); rec.StopReason != StopCredentialAbsent {
				t.Fatalf("stop = %s, want a credential-absent stop", rec.StopReason)
			}
		})
	}
}

// TestModelWorkflowInstallsAndAuthorisesTheCommandCodeHarness asserts the
// workflow carries the package and secret the adapter needs (AC7).
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
	} {
		if !strings.Contains(yml, want) {
			t.Errorf("model.yml has no %q", want)
		}
	}
}

func init() { addConformanceCase(commandCodeConformance()) }

// commandCodeConformance wires the Command Code adapter to the shared suite: a
// fake CLI replaying the recorded fixtures, the arguments the adapter's profile
// and resume behaviour travels as, and the plan's rate card it is metered by.
func commandCodeConformance() harnessCase {
	return harnessCase{
		Name:           "command-code",
		Model:          "command-code/deepseek/deepseek-v4.1-flash",
		CredentialPath: ".commandcode/auth.json",
		ResumeFlag:     "--resume",
		ReadOnlyFlag:   "--plan",
		BuildFlag:      "--yolo",
		LimitExit:      5,
		Rates:          providers.Rates{Input: 0.28, Output: 0.42, CacheRead: 0.028},
		New: func(t *testing.T, fixture string) (Harness, func() []harnessInvocation, string) {
			replay := fixture
			var transcripts []string
			switch fixture {
			case "normal":
				transcripts = []string{transcriptMessage("d666e9bd", "0.0030955680000000004")}
			case "unpriced":
				// The same frames with no transcript: the harness reports no
				// cost of its own, so the runner must price the run from tokens.
				replay = "normal"
			}
			bin, attempts := scriptedCommandCode(t, replay, transcripts...)
			home := t.TempDir()
			h := CommandCodeHarness{Bin: bin, Key: conformanceKey, Home: home}
			return h, func() []harnessInvocation {
				var runs []harnessInvocation
				for _, a := range attempts() {
					runs = append(runs, harnessInvocation{Args: a.args, Stdin: a.stdin})
				}
				return runs
			}, home
		},
	}
}

// transcriptMessage is one assistant message line as the CLI writes it to a
// session transcript.
func transcriptMessage(id string, costUSD string) string {
	return `{"type":"message","id":"` + id + `","parentId":"aa5ad8af","timestamp":"2026-10-05T05:58:33.789Z",` +
		`"message":{"role":"assistant","content":[{"type":"text","text":"done"}],"meta":{"source":"model","createdAt":1791179909171,"messageId":"ea701058-f837-408c-b805-860fcaa5d1df"}},` +
		`"usage":{"inputTokens":113652,"outputTokens":329,"cacheReadTokens":96256,"cacheWriteTokens":0,"costUsd":` + costUSD + `},` +
		`"model":"deepseek/deepseek-v4.1-flash"}` + "\n"
}

// transcriptUserLine is a transcript line that bills nothing.
const transcriptUserLine = `{"type":"message","id":"aa5ad8af","message":{"role":"user","content":[{"type":"text","text":"go"}]}}` + "\n"

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// TestCommandCodeCostByID pins the transcript reader: one cost per message id
// across every transcript, checkpoints and non-billing lines ignored, no
// transcripts at all not an error, and an unparseable one an error.
func TestCommandCodeCostByID(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		want    map[string]float64
		wantErr bool
	}{
		{"no transcripts yet", nil, nil, false},
		{"a resume copy is billed once", map[string]string{
			"p/s1.jsonl": transcriptUserLine + transcriptMessage("d666e9bd", "0.0030955680000000004"),
			"p/s2.jsonl": transcriptUserLine + transcriptMessage("d666e9bd", "0.0030955680000000004") + transcriptMessage("e1f0c2aa", "0.001204"),
		}, map[string]float64{"d666e9bd": 0.0030955680000000004, "e1f0c2aa": 0.001204}, false},
		{"checkpoints and other lines are ignored", map[string]string{
			"p/s1.jsonl":             `{"type":"summary","usage":{"costUsd":9}}` + "\n" + transcriptMessage("d666e9bd", "0.002"),
			"p/s1.checkpoints.jsonl": transcriptMessage("ffff0000", "5"),
		}, map[string]float64{"d666e9bd": 0.002}, false},
		{"a line cut short by a killed CLI", map[string]string{"p/s1.jsonl": transcriptMessage("d666e9bd", "0.002") + `{"type":"mess`}, map[string]float64{"d666e9bd": 0.002}, false},
		{"an unparseable transcript", map[string]string{"p/s1.jsonl": "not json\n" + transcriptMessage("d666e9bd", "0.002")}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			for name, body := range tt.files {
				path := filepath.Join(home, ".commandcode", "projects", filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := commandCodeCostByID(home)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if !maps.Equal(got, tt.want) {
				t.Fatalf("costs = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCommandCodeAgentBillsEachAttemptOnlyItsOwnMessages runs two attempts in
// one HOME, the second resuming the first, whose transcript copies the first's
// messages: each attempt's cost event carries only the messages new to it.
func TestCommandCodeAgentBillsEachAttemptOnlyItsOwnMessages(t *testing.T) {
	first := transcriptMessage("d666e9bd", "0.0030955680000000004") + transcriptMessage("e1f0c2aa", "0.001204")
	resumed := first + transcriptMessage("0b7c9d21", "0.000871")
	bin, _ := scriptedCommandCode(t, "normal", first, resumed)
	agent := CommandCodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}.Agent(ProfileBuild, "command-code/x")
	dir := t.TempDir()
	for i, tt := range []struct {
		session string
		want    float64
	}{
		{"", 0.0030955680000000004 + 0.001204},
		{"ses_1", 0.000871},
	} {
		var out, errBuf strings.Builder
		if err := agent.Run(context.Background(), dir, tt.session, "p", "", &out, &errBuf); err != nil {
			t.Fatal(err)
		}
		u, err := SumUsage(strings.NewReader(out.String()))
		if err != nil {
			t.Fatal(err)
		}
		if !approxEqual(u.Cost, tt.want) {
			t.Errorf("attempt %d cost = %v, want %v", i+1, u.Cost, tt.want)
		}
		if w, _ := UsageWarnings(strings.NewReader(out.String())); len(w) != 0 {
			t.Errorf("attempt %d warned %v", i+1, w)
		}
	}
}

// TestBuildRecordsAWarningWhenTheCommandCodeCostIsUnavailable asserts a run
// whose transcripts are missing or unreadable still succeeds with a usage
// warning on the summary: unpriced when the plan does not price the model, and
// at the peak rate when it does.
func TestBuildRecordsAWarningWhenTheCommandCodeCostIsUnavailable(t *testing.T) {
	for name, tt := range map[string]struct {
		transcript, model string
		want              float64
	}{
		"no transcript":          {"", "command-code/x", 0},
		"unparseable transcript": {transcriptMessage("d666e9bd", "0.002") + "garbled{", "command-code/x", 0},
		// 45386 x $0.30 + 384 x $1.20 + 35072 x $0.006, per million: GOAT's peak card.
		"no transcript, a priced model": {"", "command-code/deepseek/deepseek-v4.1-flash", 0.014287032},
	} {
		transcript := tt.transcript
		t.Run(name, func(t *testing.T) {
			bin, _ := scriptedCommandCode(t, "normal", transcript)
			deps, _, reported := testDeps(validObjects(), nil)
			deps.Agent = testRouter(ProfileBuild, CommandCodeHarness{Bin: bin, Key: "k", Home: t.TempDir()}, fakeHarness{})
			c := testConfig(t, initRepo(t))
			c.Model = tt.model
			c.ReviewModels = []string{"p/r"}

			if _, err := Build(context.Background(), deps, c); err != nil {
				t.Fatal(err)
			}
			rec := reported.last(t)
			if !approxEqual(rec.Steps[0].Tokens.Cost, tt.want) {
				t.Errorf("cost = %v, want %v", rec.Steps[0].Tokens.Cost, tt.want)
			}
			if !strings.Contains(rec.UsageWarning, "transcript cost unavailable") {
				t.Errorf("usage warning = %q, want the unavailable cost named", rec.UsageWarning)
			}
		})
	}
}
