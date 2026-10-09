package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const testSHA = "ee66687aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeObjects map[string][]byte

func (f fakeObjects) ReadObject(_ context.Context, name string) ([]byte, error) {
	b, ok := f[name]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return b, nil
}

func (f fakeObjects) CreateObject(_ context.Context, name string, r io.Reader) error {
	if _, ok := f[name]; ok {
		return fmt.Errorf("%s exists", name)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f[name] = b
	return nil
}

type fakeRecords map[string]Record

func (f fakeRecords) GetRecord(_ context.Context, id string) (Record, error) {
	r, ok := f[id]
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	return r, nil
}

func (f fakeRecords) PutRecord(_ context.Context, id string, r Record) error {
	f[id] = r
	return nil
}

var testTicket = Ticket{ID: "ABC-12", Title: "feat(x): add a file", Size: "S", SizedBy: "test", Body: "Add a file."}

var testIdentity = Identity{Account: "octo", Owner: "octo"}

type fakeAgent struct {
	calls   int
	prompt  string
	prompts []string
	// rules records the rules head every call received, in order — every round
	// of the build session carries the projection's rules, the review pass none.
	rules []string
	// dirs records the directory every call ran in, in order.
	dirs []string
	// sessions records the session id every call received, in order — the
	// pre-PR loop's check-rebuild and fix rounds must continue the same one.
	sessions []string
	// deadlines records the deadline of the context every call received, in
	// order — empty (the zero Time) for a call with no deadline.
	deadlines []time.Time
	edit      func(dir string) error
	events    string
	// eventsFn, when set, overrides events per call (its 1-based call number).
	eventsFn func(call int) string
	stderr   string
	// stderrFn, when set, overrides stderr per call (its 1-based call number).
	stderrFn func(call int) string
	err      error
	// errFn, when set, overrides err per call (its 1-based call number).
	errFn func(call int) error
}

func (a *fakeAgent) Run(ctx context.Context, dir, session, prompt, rules string, stdout, stderr io.Writer) error {
	a.calls++
	a.prompt = prompt
	a.prompts = append(a.prompts, prompt)
	a.rules = append(a.rules, rules)
	a.dirs = append(a.dirs, dir)
	a.sessions = append(a.sessions, session)
	deadline, _ := ctx.Deadline()
	a.deadlines = append(a.deadlines, deadline)
	if a.edit != nil {
		if err := a.edit(dir); err != nil {
			return err
		}
	}
	events := a.events
	if a.eventsFn != nil {
		events = a.eventsFn(a.calls)
	}
	if _, err := io.WriteString(stdout, events); err != nil {
		return err
	}
	stderrText := a.stderr
	if a.stderrFn != nil {
		stderrText = a.stderrFn(a.calls)
	}
	if stderrText != "" {
		if _, err := io.WriteString(stderr, stderrText); err != nil {
			return err
		}
	}
	if a.errFn != nil {
		return a.errFn(a.calls)
	}
	return a.err
}

// reviewEvent is one text event carrying the review agent's final
// message: a review-findings block naming findings, empty for a clean review.
func reviewEvent(findings string) string {
	return `{"type":"text","part":{"type":"text","text":` + strconv.Quote("```review-findings\n"+findings+"\n```") + `}}` + "\n"
}

// reports holds every summary a build reported; a build must report exactly one.
type reports []Summary

func (r *reports) last(t *testing.T) Summary {
	t.Helper()
	if len(*r) != 1 {
		t.Fatalf("%d summaries reported, want 1", len(*r))
	}
	s := (*r)[0]
	if !buildEndings[ending{s.Outcome, s.StopReason, s.Phase}] {
		t.Fatalf("reported %s/%s at %q, which the record job rejects", s.Outcome, s.StopReason, s.Phase)
	}
	return s
}

// alwaysPassChecks is the Checks fake most tests use: RunChecks's own
// behavior is checks_test.go's concern, not this file's.
func alwaysPassChecks(context.Context, string) (string, string, error) { return "", "", nil }

func testDeps(projections fakeObjects, agent *fakeAgent) (BuildDeps, fakeObjects, *reports) {
	completions, reported := fakeObjects{}, &reports{}
	return BuildDeps{
		Projections: projections,
		Completions: completions,
		Agent:       agent,
		ReviewAgent: &fakeAgent{events: reviewEvent("")},
		Checks:      alwaysPassChecks,
		Report: func(s Summary) error {
			*reported = append(*reported, s)
			return nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    time.Now,
	}, completions, reported
}

func testConfig(t *testing.T, repo string) BuildConfig {
	return BuildConfig{
		AttemptID:    "42-1",
		Repo:         repo,
		TempDir:      t.TempDir(),
		Pointer:      DefaultPointer,
		Model:        "p/m",
		PlanModels:   []string{"p/m"},
		ReviewModels: []string{"p/r"},
		Identity:     testIdentity,
		Ticket:       testTicket,
	}
}

// freeTierConfig is a free-tier run's config: the first free model is the
// run's build model and the ticket carries the whole list, as the run's ticket
// file does (AC1, AC2).
func freeTierConfig(t *testing.T, free ...string) BuildConfig {
	t.Helper()
	c := testConfig(t, initRepo(t))
	c.Model, c.Ticket.FreeModels = free[0], free
	return c
}

// stepModels are sum's steps of phase p, in the order they ran.
func stepModels(sum Summary, p Phase) []string {
	var models []string
	for _, st := range sum.Steps {
		if st.Phase == p {
			models = append(models, st.Model)
		}
	}
	return models
}

func TestBuildFailsClosedWithoutAProjection(t *testing.T) {
	projectionPath := "projections/" + testSHA + "/" + buildProjectionFile
	tests := []struct {
		name       string
		objects    fakeObjects
		wantReason StopReason
	}{
		{"missing pointer", fakeObjects{projectionPath: []byte("rules " + ticketSentinel)}, StopProjectionMissing},
		{"missing projection", fakeObjects{DefaultPointer: []byte(testSHA + "\n")}, StopProjectionMissing},
		{"pointer is not a sha", fakeObjects{DefaultPointer: []byte("main")}, StopProjectionInvalid},
		{"projection without a sentinel", fakeObjects{DefaultPointer: []byte(testSHA), projectionPath: []byte("rules")},
			StopProjectionInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, completions, reported := testDeps(tt.objects, agent)
			c := testConfig(t, t.TempDir())

			_, err := Build(context.Background(), deps, c)
			if err == nil {
				t.Fatal("Build succeeded without a projection")
			}
			if agent.calls != 0 {
				t.Fatalf("agent ran %d times, want 0", agent.calls)
			}
			if len(completions) != 0 {
				t.Fatalf("completions uploaded: %v", completions)
			}
			sum := reported.last(t)
			if sum.Outcome != OutcomeStopped || sum.StopReason != tt.wantReason || sum.Phase != PhaseProjection {
				t.Fatalf("summary = %s/%s at %s, want stopped/%s at projection",
					sum.Outcome, sum.StopReason, sum.Phase, tt.wantReason)
			}
		})
	}
}

func TestBuildCommitsAndBundlesTheAgentsEdits(t *testing.T) {
	repo := initRepo(t)
	objects := fakeObjects{
		DefaultPointer: []byte(testSHA + "\n"),
		"projections/" + testSHA + "/" + buildProjectionFile: []byte("# Rules\n\n# The ticket\n\n" + ticketSentinel + "\n"),
	}
	agent := &fakeAgent{
		edit: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "version.go"), []byte("package x\n"), 0o600)
		},
		events: `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"cache":{"read":90}},"cost":0.5}}` + "\n",
	}
	deps, completions, reported := testDeps(objects, agent)
	c := testConfig(t, repo)

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Branch != "wingman/abc-12-42-1" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.HasPrefix(agent.rules[0], "# Rules") {
		t.Fatalf("build round's rules = %q, want the projection's rules head", agent.rules[0])
	}
	if !strings.HasPrefix(agent.prompt, testTicket.Text()) ||
		!strings.HasSuffix(agent.prompt, testTicket.Text()+"\n\n\n"+commitMessageInstruction) {
		t.Fatalf("prompt does not carry the ticket then the commit-message instruction: %q", agent.prompt)
	}

	rec := reported.last(t)
	if rec.Outcome != OutcomeBuilt || rec.RuleStackSHA != testSHA || rec.Branch != res.Branch {
		t.Fatalf("summary = %+v", rec)
	}
	if !slices.Equal(rec.EditedFiles, []string{"version.go"}) {
		t.Fatalf("edited files = %q", rec.EditedFiles)
	}
	if rec.DiffLines.Added == 0 {
		t.Fatalf("diff lines = %+v, want the added file counted", rec.DiffLines)
	}
	if len(rec.Steps) != 2 {
		t.Fatalf("steps = %+v, want the build round then the review pass", rec.Steps)
	}
	if !rec.Ready {
		t.Fatalf("ready = %v, want true: checks passed and the review found nothing", rec.Ready)
	}
	step := rec.Steps[0]
	if step.Tokens.Input != 10 || step.Tokens.CacheRead != 90 || step.Model != "p/m" || step.Phase != PhaseBuild || step.Round != 1 {
		t.Fatalf("step = %+v", step)
	}
	if got := completions[step.CompletionsObject]; !bytes.Equal(got, []byte(agent.events)) {
		t.Fatalf("completions object %q = %q", step.CompletionsObject, got)
	}

	clone := t.TempDir()
	mustGit(t, clone, "clone", "-q", repo, ".")
	mustGit(t, clone, "fetch", "-q", res.BundlePath, res.Branch+":"+res.Branch)
	if files := mustGit(t, clone, "diff", "--name-only", "HEAD", res.Branch); files != "version.go\n" {
		t.Fatalf("bundle carries %q", files)
	}
}

// TestBuildRecordsTheHarnessThatRanEachStep asserts every step names the
// harness its model's plan routes to, the fallback included when the default is
// not ready (AC1, AC2).
func TestBuildRecordsTheHarnessThatRanEachStep(t *testing.T) {
	for name, tt := range map[string]struct {
		defaultReady error
		want         string
	}{
		"the plan's default": {nil, "default"},
		"the plan's fallback when the default is not ready": {errors.New("the default has no key"), "fallback"},
	} {
		t.Run(name, func(t *testing.T) {
			order := planOrders(map[string][]string{"p": {"default", "fallback"}})
			harnesses := func(agent Agent) []Harness {
				return []Harness{fakeHarness{name: "default", ready: tt.defaultReady, agent: agent},
					fakeHarness{name: "fallback", agent: agent}}
			}
			buildAgent := &fakeAgent{edit: edit("version.go", "package x\n")}
			reviewAgent := &fakeAgent{events: reviewEvent("")}
			deps, _, reported := testDeps(validObjects(), buildAgent)
			deps.Agent = newRouter(order, ProfileBuild, harnesses(buildAgent)...)
			deps.ReviewAgent = newRouter(order, ProfileReview, harnesses(reviewAgent)...)
			c := testConfig(t, initRepo(t))

			if _, err := Build(context.Background(), deps, c); err != nil {
				t.Fatal(err)
			}
			steps := reported.last(t).Steps
			if len(steps) != 2 {
				t.Fatalf("steps = %+v, want the build round then the review pass", steps)
			}
			for _, st := range steps {
				if st.Harness != tt.want {
					t.Errorf("step %s/%d harness = %q, want %q", st.Phase, st.Round, st.Harness, tt.want)
				}
			}
		})
	}
}

// TestBuildLogsTheHarnessAndModelWhenAPhaseStarts asserts an agent phase logs
// exactly one start line naming the harness and model it begins with (AC3).
func TestBuildLogsTheHarnessAndModelWhenAPhaseStarts(t *testing.T) {
	buildAgent := &fakeAgent{edit: edit("version.go", "package x\n")}
	deps, _, _ := testDeps(validObjects(), buildAgent)
	deps.Agent = testRouter(ProfileBuild, fakeHarness{name: "p", agent: buildAgent})
	deps.ReviewAgent = testRouter(ProfileReview, fakeHarness{name: "p", agent: &fakeAgent{events: reviewEvent("")}})
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	line := phaseStartLine(t, logs.String(), PhaseBuild)
	if !strings.Contains(line, "harness=p") || !strings.Contains(line, "model=p/m") {
		t.Fatalf("build phase start = %q, want it to name the harness and model", line)
	}
}

// phaseStartLine returns a phase's one phaseStarted log line, failing when the
// build logged more or fewer for it.
func phaseStartLine(t *testing.T, logs string, phase Phase) string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "msg=phaseStarted") && strings.Contains(line, "phase="+string(phase)) {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("%d phaseStarted lines for %s, want 1: %q", len(lines), phase, lines)
	}
	return lines[0]
}

func TestBuildReportsAFailedAgent(t *testing.T) {
	objects := fakeObjects{
		DefaultPointer: []byte(testSHA),
		"projections/" + testSHA + "/" + buildProjectionFile: []byte("# Rules\n" + ticketSentinel),
	}
	agent := &fakeAgent{events: "{}\n", err: errors.New("exit status 1")}
	deps, completions, reported := testDeps(objects, agent)
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with a failed agent")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeAgentFailed || rec.StopReason != StopAgentExit {
		t.Fatalf("record = %s/%s", rec.Outcome, rec.StopReason)
	}
	if len(rec.Steps) != 1 {
		t.Fatalf("steps = %+v, want exactly one", rec.Steps)
	}
	if _, ok := completions[rec.Steps[0].CompletionsObject]; !ok {
		t.Fatal("completions of a failed agent were not uploaded")
	}
}

func TestBuildClassifiesAProviderAllowanceExhaustionAsABudgetStopNotAgentFailed(t *testing.T) {
	agent := &fakeAgent{events: "{}\n", stderr: "Error: allowance exhausted for this billing period",
		err: errors.New("exit status 1")}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with a failed agent")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeBudgetStop || rec.StopReason != StopAllowanceExhausted {
		t.Fatalf("record = %s/%s, want a budget stop never escalated by FR-13", rec.Outcome, rec.StopReason)
	}
}

func TestBuildClassifiesAnOrdinaryAgentFailureUnchanged(t *testing.T) {
	agent := &fakeAgent{events: "{}\n", stderr: "Error: the model returned malformed output",
		err: errors.New("exit status 1")}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with a failed agent")
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeAgentFailed || rec.StopReason != StopAgentExit {
		t.Fatalf("record = %s/%s, want the ordinary agent-failure classification", rec.Outcome, rec.StopReason)
	}
}

func TestBuildSubstitutesTheNextModelOnAnAvailabilityFailureAndRecordsBothAttempts(t *testing.T) {
	agent := &fakeAgent{
		edit: edit("version.go", "package x\n"),
		errFn: func(call int) error {
			if call == 1 {
				return errors.New("exit status 1")
			}
			return nil
		},
		stderrFn: func(call int) string {
			if call == 1 {
				return "Error: no endpoints found for this model"
			}
			return ""
		},
		events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{"input":1}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Model = DefaultModel()

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 2 {
		t.Fatalf("build agent ran %d times, want 2 (the unavailable attempt plus the substituted one)", agent.calls)
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeBuilt {
		t.Fatalf("outcome = %s, want the substituted model's build to succeed", rec.Outcome)
	}
	want := fallbackModels(c.Model)[0]
	if rec.Steps[0].Model != c.Model || rec.Steps[0].Round != 1 || rec.Steps[1].Model != want || rec.Steps[1].Round != 2 {
		t.Fatalf("steps = %+v, want the original model recorded then the substitution (AC1)", rec.Steps)
	}
}

func TestBuildStopsWithModelUnavailableOnceTheAvailabilityOrderIsExhausted(t *testing.T) {
	agent := &fakeAgent{stderr: "Error: no endpoints found for this model", err: errors.New("exit status 1")}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Model = DefaultModel()

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with every model in the order unavailable")
	}
	wantAttempts := 1 + len(fallbackModels(c.Model))
	if agent.calls != wantAttempts {
		t.Fatalf("build agent ran %d times, want %d (every model in the order tried once)", agent.calls, wantAttempts)
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeInfraFailure || rec.StopReason != StopModelUnavailable {
		t.Fatalf("record = %s/%s, want an infra failure once the order is exhausted (AC3)", rec.Outcome, rec.StopReason)
	}
	if len(rec.Steps) != wantAttempts {
		t.Fatalf("steps = %d, want every attempt recorded (AC1)", len(rec.Steps))
	}
}

func TestBuildNeverSubstitutesOnAnOrdinaryAgentFailureEvenWithFallbacksConfigured(t *testing.T) {
	agent := &fakeAgent{stderr: "Error: the model returned malformed output", err: errors.New("exit status 1")}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Model = DefaultModel()

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with a failed agent")
	}
	if agent.calls != 1 {
		t.Fatalf("build agent ran %d times, want 1: a content/quality failure must never trigger the fallback (AC2)",
			agent.calls)
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeAgentFailed || rec.StopReason != StopAgentExit {
		t.Fatalf("record = %s/%s, want the ordinary agent-failure classification", rec.Outcome, rec.StopReason)
	}
}

// planEvent is one text event carrying the plan agent's final message.
func planEvent(text string) string {
	return `{"type":"text","part":{"type":"text","text":` + strconv.Quote(text) + `}}` + "\n"
}

func TestBuildSkipsThePlanPhaseForAnSTicket(t *testing.T) {
	buildAgent := &fakeAgent{edit: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "version.go"), []byte("package x\n"), 0o600)
	}}
	planAgent := &fakeAgent{}
	deps, _, reported := testDeps(validObjects(), buildAgent)
	deps.PlanAgent = planAgent
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if planAgent.calls != 0 {
		t.Fatalf("plan agent ran %d times for an S ticket, want 0", planAgent.calls)
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeBuilt {
		t.Fatalf("outcome = %s/%s", rec.Outcome, rec.StopReason)
	}
	if _, ok := rec.DurationsMS[string(PhasePlan)]; ok {
		t.Fatal("an S ticket recorded a plan phase duration")
	}
}

func TestBuildRunsThePlanPhaseForAnMOrLTicket(t *testing.T) {
	planAgent := &fakeAgent{events: planEvent("plan\n\n```plan-files\nversion.go\n```")}
	buildAgent := &fakeAgent{edit: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "version.go"), []byte("package x\n"), 0o600)
	}}
	objects := validObjects()
	objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
	deps, _, reported := testDeps(objects, buildAgent)
	deps.PlanAgent = planAgent
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "M"

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("build did not change anything")
	}
	if planAgent.calls != 1 || !strings.HasPrefix(planAgent.rules[0], "# Plan rules") ||
		!strings.Contains(planAgent.prompt, "plan-files") {
		t.Fatalf("plan agent calls %d, rules %q, prompt %q", planAgent.calls, planAgent.rules, planAgent.prompt)
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeBuilt {
		t.Fatalf("outcome = %s/%s", rec.Outcome, rec.StopReason)
	}
	if len(rec.OutOfPlanFiles) != 0 || !res.Ready || !rec.Ready {
		t.Fatalf("out of plan %q, ready %v/%v, want none and ready for a build inside its plan",
			rec.OutOfPlanFiles, res.Ready, rec.Ready)
	}
	var phases []Phase
	for _, st := range rec.Steps {
		phases = append(phases, st.Phase)
	}
	if !slices.Equal(phases, []Phase{PhasePlan, PhaseBuild, PhaseReview}) {
		t.Fatalf("steps = %v, want the plan phase, the build phase, then the review pass", phases)
	}
}

// TestBuildSkipsThePlanPhaseWhenTheTicketCarriesThePlanFiles covers the two-stage
// hand-off (FRG-33): a build the plan stage already fronted is handed its plan
// through the ticket and must not call the plan agent again.
func TestBuildSkipsThePlanPhaseWhenTheTicketCarriesThePlanFiles(t *testing.T) {
	buildAgent := &fakeAgent{edit: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "version.go"), []byte("package x\n"), 0o600)
	}}
	planAgent := &fakeAgent{}
	deps, _, reported := testDeps(validObjects(), buildAgent)
	deps.PlanAgent = planAgent
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "M"
	c.Ticket.PlanFiles = []string{"version.go"}

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if planAgent.calls != 0 {
		t.Fatalf("plan agent ran %d times with the plan handed in, want 0", planAgent.calls)
	}
	if !res.Changed || !res.Ready {
		t.Fatalf("changed %v, ready %v; want a ready build inside the handed plan", res.Changed, res.Ready)
	}
	rec := reported.last(t)
	if len(rec.OutOfPlanFiles) != 0 {
		t.Fatalf("out of plan = %q, want none", rec.OutOfPlanFiles)
	}
	if _, ok := rec.DurationsMS[string(PhasePlan)]; ok {
		t.Fatal("a build handed its plan recorded a plan phase duration")
	}
}

// TestBuildChecksAnEditedFileOutsideAHandedPlan is the same hand-off's other
// half: the out-of-plan check still runs when planning was skipped.
func TestBuildChecksAnEditedFileOutsideAHandedPlan(t *testing.T) {
	buildAgent := &fakeAgent{edit: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "extra.go"), []byte("package x\n"), 0o600)
	}}
	deps, _, reported := testDeps(validObjects(), buildAgent)
	deps.PlanAgent = &fakeAgent{}
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "M"
	c.Ticket.PlanFiles = []string{"version.go"}

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Ready {
		t.Fatalf("changed %v, ready %v; want a committed draft", res.Changed, res.Ready)
	}
	if got := reported.last(t).OutOfPlanFiles; !slices.Equal(got, []string{"extra.go"}) {
		t.Fatalf("out of plan = %q, want [extra.go]", got)
	}
}

func TestBuildCommitsADraftWhenTheBuildEditsOutsideThePlan(t *testing.T) {
	planAgent := &fakeAgent{events: planEvent("```plan-files\nversion.go\n```")}
	buildAgent := &fakeAgent{edit: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "extra.go"), []byte("package x\n"), 0o600)
	}}
	objects := validObjects()
	objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
	deps, _, reported := testDeps(objects, buildAgent)
	deps.PlanAgent = planAgent
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "M"

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Ready {
		t.Fatalf("changed %v, ready %v, want a committed draft", res.Changed, res.Ready)
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeBuilt || rec.Phase != PhaseCommit || rec.Ready {
		t.Fatalf("record = %s at %s, ready %v, want built at commit and not ready", rec.Outcome, rec.Phase, rec.Ready)
	}
	if !slices.Equal(rec.OutOfPlanFiles, []string{"extra.go"}) {
		t.Fatalf("out of plan = %q, want [extra.go]", rec.OutOfPlanFiles)
	}
}

func TestBuildStopsWhenThePlanAgentNamesNoFiles(t *testing.T) {
	planAgent := &fakeAgent{events: planEvent("no file list here")}
	buildAgent := &fakeAgent{}
	objects := validObjects()
	objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
	deps, _, reported := testDeps(objects, buildAgent)
	deps.PlanAgent = planAgent
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "L"

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with an unparseable plan")
	}
	if buildAgent.calls != 0 {
		t.Fatalf("build agent ran %d times, want 0", buildAgent.calls)
	}
	sum := reported.last(t)
	if sum.Outcome != OutcomeStopped || sum.StopReason != StopPlanInvalid || sum.Phase != PhasePlan {
		t.Fatalf("summary = %s/%s at %s", sum.Outcome, sum.StopReason, sum.Phase)
	}
}

func TestBuildStopsWhenThePlanNamesAWorkflowFile(t *testing.T) {
	planAgent := &fakeAgent{events: planEvent("```plan-files\nversion.go\n.github/workflows/ci.yml\n```")}
	buildAgent := &fakeAgent{}
	objects := validObjects()
	objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
	deps, _, reported := testDeps(objects, buildAgent)
	deps.PlanAgent = planAgent
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "M"

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with a plan naming a workflow file")
	}
	if buildAgent.calls != 0 {
		t.Fatalf("build agent ran %d times, want 0", buildAgent.calls)
	}
	sum := reported.last(t)
	if sum.Outcome != OutcomeStopped || sum.StopReason != StopWorkflowChange || sum.Phase != PhasePlan {
		t.Fatalf("summary = %s/%s at %s", sum.Outcome, sum.StopReason, sum.Phase)
	}
	if !strings.Contains(sum.StopDetail, ".github/workflows/ci.yml") || strings.Contains(sum.StopDetail, "version.go") {
		t.Fatalf("detail = %q, want the workflow file alone", sum.StopDetail)
	}
	if !strings.Contains(logs.String(), "msg=workflowChange") || !strings.Contains(logs.String(), ".github/workflows/ci.yml") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestBuildStopsBeforeThePushWhenTheBuildTouchesAWorkflowFile(t *testing.T) {
	writeFile := func(name string) func(string) error {
		return func(dir string) error {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, name), []byte("on: push\n"), 0o600)
		}
	}
	tests := []struct {
		name     string
		size     string
		plan     string
		existing bool
		edit     func(string) error
		want     string
	}{
		{"a small build adding one", "S", "", false, writeFile(".github/workflows/ci.yml"), ".github/workflows/ci.yml"},
		{"a planned build editing one outside its plan", "M", "version.go", false, writeFile(".github/workflows/ci.yml"), ".github/workflows/ci.yml"},
		{"a build moving one out of the directory", "S", "", true, func(dir string) error {
			return os.Rename(filepath.Join(dir, ".github/workflows/ci.yml"), filepath.Join(dir, "ci.yml"))
		}, ".github/workflows/ci.yml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initRepo(t)
			if tt.existing {
				if err := writeFile(".github/workflows/ci.yml")(repo); err != nil {
					t.Fatal(err)
				}
				mustGit(t, repo, "add", ".github")
				mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "ci")
			}
			objects := validObjects()
			objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
			deps, _, reported := testDeps(objects, &fakeAgent{edit: tt.edit})
			deps.PlanAgent = &fakeAgent{events: planEvent("```plan-files\n" + tt.plan + "\n```")}
			var logs bytes.Buffer
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			c := testConfig(t, repo)
			c.Ticket.Size = tt.size

			res, err := Build(context.Background(), deps, c)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Changed || res.Ready {
				t.Fatalf("changed %v, ready %v, want a bundled commit that is not ready", res.Changed, res.Ready)
			}
			if _, err := os.Stat(res.BundlePath); err != nil {
				t.Fatalf("bundle: %v", err)
			}
			sum := reported.last(t)
			if sum.Outcome != OutcomeStopped || sum.StopReason != StopWorkflowChange || sum.Phase != PhaseCommit {
				t.Fatalf("summary = %s/%s at %s", sum.Outcome, sum.StopReason, sum.Phase)
			}
			if !strings.Contains(sum.StopDetail, tt.want) || !strings.Contains(logs.String(), "msg=workflowChange") {
				t.Fatalf("detail %q, logs %q, want both naming %s", sum.StopDetail, logs.String(), tt.want)
			}
		})
	}
}

func TestBuildFailsClosedWithoutAPlanProjection(t *testing.T) {
	planProjectionPath := "projections/" + testSHA + "/" + planProjectionFile
	tests := []struct {
		name       string
		objects    fakeObjects
		wantReason StopReason
	}{
		{"missing plan projection", validObjects(), StopProjectionMissing},
		{"plan projection without a sentinel", func() fakeObjects {
			o := validObjects()
			o[planProjectionPath] = []byte("rules")
			return o
		}(), StopProjectionInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buildAgent, planAgent := &fakeAgent{}, &fakeAgent{}
			deps, _, reported := testDeps(tt.objects, buildAgent)
			deps.PlanAgent = planAgent
			c := testConfig(t, initRepo(t))
			c.Ticket.Size = "M"

			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build succeeded without a plan projection")
			}
			if planAgent.calls != 0 || buildAgent.calls != 0 {
				t.Fatalf("plan calls %d, build calls %d, want 0 and 0", planAgent.calls, buildAgent.calls)
			}
			sum := reported.last(t)
			if sum.Outcome != OutcomeStopped || sum.StopReason != tt.wantReason || sum.Phase != PhasePlan {
				t.Fatalf("summary = %s/%s at %s, want stopped/%s at plan",
					sum.Outcome, sum.StopReason, sum.Phase, tt.wantReason)
			}
		})
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "README.md")
	mustGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func validObjects() fakeObjects {
	return fakeObjects{
		DefaultPointer: []byte(testSHA),
		"projections/" + testSHA + "/" + buildProjectionFile: []byte("# Rules\n" + ticketSentinel),
	}
}

// overLongModel is a well-formed model id one byte past what a run record accepts.
var overLongModel = "p/" + strings.Repeat("a", maxModelBytes-1)

func TestValidModelAcceptsExactlyTheRecordableLength(t *testing.T) {
	atLimit := "p/" + strings.Repeat("a", maxModelBytes-2)
	if !ValidModel(atLimit) || len(atLimit) != maxModelBytes {
		t.Fatalf("a model of %d bytes was rejected", len(atLimit))
	}
	if ValidModel(overLongModel) || len(overLongModel) != maxModelBytes+1 {
		t.Fatalf("a model of %d bytes was accepted", len(overLongModel))
	}
	if err := ValidatePlanModels([]string{atLimit}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIgnoresThePlanModelsOfATicketThatDoesNotPlan(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n")}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.PlanModels = []string{"not a model"}

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatalf("an S ticket never plans, but Build failed: %v", err)
	}
	if rec := reported.last(t); rec.Outcome != OutcomeBuilt {
		t.Fatalf("outcome = %s/%s", rec.Outcome, rec.StopReason)
	}
}

func TestBuildStopsOnAnInvalidModel(t *testing.T) {
	for _, model := range []string{"", "big-pickle", "command-code/big pickle", "-x/y", "command-code/x;rm", overLongModel} {
		t.Run(model, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, _, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))
			c.Model = model
			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build accepted the model")
			}
			if agent.calls != 0 || reported.last(t).StopReason != StopModelInvalid {
				t.Fatalf("calls %d, reason %s", agent.calls, reported.last(t).StopReason)
			}
		})
	}
}

func TestBuildStopsWhenTheReviewModelsAreInvalid(t *testing.T) {
	for name, reviewModels := range map[string][]string{
		"none":                nil,
		"malformed":           {"not a model"},
		"same as build model": {"p/m"},
		"repeated":            {"p/r", "p/r"},
		"over long":           {overLongModel},
	} {
		t.Run(name, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, _, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))
			c.ReviewModels = reviewModels
			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build accepted the review models")
			}
			if agent.calls != 0 || reported.last(t).StopReason != StopModelInvalid {
				t.Fatalf("calls %d, reason %s", agent.calls, reported.last(t).StopReason)
			}
		})
	}
}

func TestBuildStopsWhenAReviewModelIsABuildFallback(t *testing.T) {
	fallbacks := fallbackModels(DefaultModel())
	if len(fallbacks) == 0 {
		t.Fatal("models.json names no build fallbacks for this test to cover")
	}
	for _, fallback := range fallbacks {
		t.Run(fallback, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, _, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))
			c.Model = DefaultModel()
			c.ReviewModels = []string{fallback}
			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build accepted a review model that is a build fallback")
			}
			if agent.calls != 0 || reported.last(t).StopReason != StopModelInvalid {
				t.Fatalf("calls %d, reason %s", agent.calls, reported.last(t).StopReason)
			}
		})
	}
}

func TestBuildStopsWhenThePlanModelsAreInvalid(t *testing.T) {
	for name, models := range map[string][]string{
		"none":        nil,
		"malformed":   {"p/m", "not a model"},
		"repeated":    {"p/m", "p/b", "p/m"},
		"empty entry": {""},
		"over long":   {"p/" + strings.Repeat("a", maxModelBytes)},
	} {
		t.Run(name, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, _, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))
			c.Ticket.Size = "M"
			c.PlanModels = models
			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build accepted the plan models")
			}
			if agent.calls != 0 || reported.last(t).StopReason != StopModelInvalid {
				t.Fatalf("calls %d, reason %s", agent.calls, reported.last(t).StopReason)
			}
		})
	}
}

func TestDefaultPlanModelsValidate(t *testing.T) {
	if err := ValidatePlanModels([]string{DefaultPlanModel()}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReviewModels(DefaultReviewModels(), DefaultModel()); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAdvancesTheBuildOrderOnAllowanceExhaustion(t *testing.T) {
	agent := &fakeAgent{stderr: "Error: allowance exhausted", err: errors.New("exit status 1")}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Model = DefaultModel()
	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded with every model's allowance exhausted")
	}
	want := []string{"command-code/deepseek/deepseek-v4.1-flash", "command-code/inclusionai/ling-3.1-flash:free"}
	if agent.calls != len(want) {
		t.Fatalf("build agent ran %d times, want %d: an exhausted allowance moves to the fallback", agent.calls, len(want))
	}
	var models []string
	for _, st := range reported.last(t).Steps {
		models = append(models, st.Model)
	}
	if !slices.Equal(models, want) {
		t.Fatalf("build ran on %v, want the ordered list %v", models, want)
	}
	if rec := reported.last(t); rec.Outcome != OutcomeBudgetStop || rec.StopReason != StopAllowanceExhausted {
		t.Fatalf("record = %s/%s", rec.Outcome, rec.StopReason)
	}
}

func edit(name, content string) func(dir string) error {
	return func(dir string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
	}
}

func TestPrePRLoopOpensReadyWhenChecksAndReviewPassFirstTry(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n")}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 {
		t.Fatalf("build agent ran %d times, want 1", agent.calls)
	}
	rec := reported.last(t)
	if !rec.Ready || rec.LoopDetail != "" {
		t.Fatalf("ready = %v, detail = %q, want ready with no detail", rec.Ready, rec.LoopDetail)
	}
	var phases []Phase
	for _, st := range rec.Steps {
		phases = append(phases, st.Phase)
	}
	if !slices.Equal(phases, []Phase{PhaseBuild, PhaseReview}) {
		t.Fatalf("steps = %v, want the build round then the review pass", phases)
	}
}

func TestPrePRLoopRebuildsOnceOnAFailingCheckThenPasses(t *testing.T) {
	agent := &fakeAgent{
		edit:   edit("version.go", "package x\n"),
		events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{"input":1}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	calls := 0
	deps.Checks = func(context.Context, string) (string, string, error) {
		calls++
		if calls == 1 {
			return checkVet, "vet failed: bad format", nil
		}
		return "", "", nil
	}
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 2 {
		t.Fatalf("build agent ran %d times, want 2 (initial attempt + one rebuild)", agent.calls)
	}
	if agent.sessions[1] != "ses_1" {
		t.Fatalf("rebuild session = %q, want the initial round's session continued", agent.sessions[1])
	}
	if agent.rules[1] == "" || agent.rules[1] != agent.rules[0] {
		t.Fatalf("rebuild rules = %q, want the build round's %q", agent.rules[1], agent.rules[0])
	}
	if want := filepath.Join(c.TempDir, "wt"); agent.dirs[0] != want || agent.dirs[1] != want {
		t.Fatalf("rounds ran in %q, want the fixed worktree %q on every run", agent.dirs, want)
	}
	if !strings.Contains(agent.prompts[1], checkVet) || !strings.Contains(agent.prompts[1], "bad format") {
		t.Fatalf("rebuild prompt = %q, want it to name the failing gate and its output", agent.prompts[1])
	}
	rec := reported.last(t)
	if !rec.Ready {
		t.Fatal("ready = false, want true: checks eventually passed and the review found nothing")
	}
	if rec.Steps[1].Phase != PhaseBuild || rec.Steps[1].Round != 2 || rec.Steps[1].Detail != checkVet {
		t.Fatalf("rebuild step = %+v", rec.Steps[1])
	}
}

func TestPrePRLoopRecordsCheckTimeAndLogsEachRound(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n")}
	deps, _, reported := testDeps(validObjects(), agent)
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	clock := time.Now()
	deps.Now = func() time.Time { return clock }
	calls := 0
	deps.Checks = func(context.Context, string) (string, string, error) {
		calls++
		clock = clock.Add(3 * time.Second)
		if calls == 1 {
			return checkVet, "vet failed", nil
		}
		return "", "", nil
	}

	if _, err := Build(context.Background(), deps, testConfig(t, initRepo(t))); err != nil {
		t.Fatal(err)
	}
	if got := reported.last(t).DurationsMS["checks"]; got != 6000 {
		t.Fatalf("checks duration = %d ms, want 6000 across both rounds", got)
	}
	type roundLine struct {
		Round      int
		FailedGate string
		DurationMS int64
	}
	var rounds []roundLine
	for l := range strings.Lines(logs.String()) {
		if strings.Contains(l, `"msg":"checkRound"`) {
			var r roundLine
			if err := json.Unmarshal([]byte(l), &r); err != nil {
				t.Fatal(err)
			}
			rounds = append(rounds, r)
		}
	}
	if want := []roundLine{{1, "vet", 3000}, {2, "", 3000}}; !slices.Equal(rounds, want) {
		t.Fatalf("checkRound lines = %+v, want %+v", rounds, want)
	}
}

func TestPrePRLoopGivesUpAfterThreeRoundsStillFailing(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n")}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.Checks = func(context.Context, string) (string, string, error) {
		return checkTest, "still failing", nil
	}
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if agent.calls != checkLoopMaxRounds {
		t.Fatalf("build agent ran %d times, want %d", agent.calls, checkLoopMaxRounds)
	}
	rec := reported.last(t)
	if rec.Ready {
		t.Fatal("ready = true, want false: the checks never passed")
	}
	if !strings.Contains(rec.LoopDetail, checkTest) || !strings.Contains(rec.LoopDetail, "still failing") {
		t.Fatalf("loop detail = %q, want it to name the failing gate", rec.LoopDetail)
	}
	for _, st := range rec.Steps {
		if st.Phase == PhaseReview {
			t.Fatalf("the review ran despite the checks never passing: %+v", rec.Steps)
		}
	}
}

func TestPrePRLoopStillGetsThreeRebuildRoundsAfterAMidRoundModelSubstitution(t *testing.T) {
	agent := &fakeAgent{
		edit: edit("version.go", "package x\n"),
		errFn: func(call int) error {
			if call == 1 {
				return errors.New("exit status 1")
			}
			return nil
		},
		stderrFn: func(call int) string {
			if call == 1 {
				return "Error: no endpoints found for this model"
			}
			return ""
		},
		events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{"input":1}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.Checks = func(context.Context, string) (string, string, error) {
		return checkTest, "still failing", nil
	}
	c := testConfig(t, initRepo(t))
	c.Model = DefaultModel()

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	want := checkLoopMaxRounds + 1 // one extra call to substitute past the unavailable model on the first rebuild pass
	if agent.calls != want {
		t.Fatalf("build agent ran %d times, want %d: a same-pass model substitution must not cost a rebuild round",
			agent.calls, want)
	}
	rec := reported.last(t)
	if rec.Ready {
		t.Fatal("ready = true, want false: the checks never passed")
	}
}

func TestPrePRLoopReviewFindingsForceADraftAndOneFixRound(t *testing.T) {
	const finding = "the retry never bounds its attempts"
	agent := &fakeAgent{
		edit:   edit("version.go", "package x\n"),
		events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	review := &fakeAgent{events: reviewEvent(finding)}
	deps.ReviewAgent = review
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 2 {
		t.Fatalf("build agent ran %d times, want 2 (initial attempt + one fix round)", agent.calls)
	}
	if agent.sessions[1] != "ses_1" {
		t.Fatalf("fix round session = %q, want the initial round's session continued", agent.sessions[1])
	}
	if !strings.Contains(agent.prompts[1], finding) {
		t.Fatalf("fix round prompt = %q, want it to carry the review's finding", agent.prompts[1])
	}
	// The review pass starts a fresh session with no projection, so it carries
	// no rules; its fix round resumes the build session, so it must carry the
	// build round's rules — an empty rules would change the resumed session's
	// system prompt mid-conversation. The re-review starts fresh too, so it
	// carries no rules either.
	if len(review.rules) != 2 || review.rules[0] != "" || review.rules[1] != "" {
		t.Fatalf("review pass rules = %q, want none on either pass", review.rules)
	}
	if agent.rules[1] == "" || agent.rules[1] != agent.rules[0] {
		t.Fatalf("fix round rules = %q, want the build round's %q", agent.rules[1], agent.rules[0])
	}
	rec := reported.last(t)
	if rec.Ready {
		t.Fatal("ready = true, want false: the review left a finding open")
	}
	if !strings.Contains(rec.LoopDetail, finding) {
		t.Fatalf("loop detail = %q, want it to name the finding", rec.LoopDetail)
	}
	if len(rec.Steps) != 4 || rec.Steps[2].Phase != PhaseBuild || rec.Steps[2].Round != 2 || rec.Steps[2].Detail != finding ||
		rec.Steps[3].Phase != PhaseReview || rec.Steps[3].Round != 2 {
		t.Fatalf("steps = %+v, want [build round1] [review round1] [build round2, the fix] [review round2, the re-review]", rec.Steps)
	}
}

// TestPrePRLoopReReviewListsOnlyTheFindingsStillOpen is FR-59's first
// acceptance criterion: once the review has run again on the fixed diff, the
// detail names only the findings that re-review still reports, so a reader
// stops re-deriving the ones the fix round already resolved.
func TestPrePRLoopReReviewListsOnlyTheFindingsStillOpen(t *testing.T) {
	const (
		first     = "the retry never bounds its attempts"
		remaining = "the timeout is hard-coded"
	)
	agent := &fakeAgent{events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{}}}` + "\n"}
	agent.edit = func(dir string) error {
		files := []string{"version.go"}
		if agent.calls > 1 {
			files = append(files, "fixed.go")
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("package x\n"), 0o600); err != nil {
				return err
			}
		}
		return nil
	}
	deps, _, reported := testDeps(validObjects(), agent)
	review := &fakeAgent{eventsFn: func(call int) string {
		if call == 1 {
			return reviewEvent(first + "\n" + remaining)
		}
		return reviewEvent(remaining)
	}}
	deps.ReviewAgent = review
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if review.calls != 2 {
		t.Fatalf("review agent ran %d times, want 2 (the review pass, then one re-review)", review.calls)
	}
	if !strings.Contains(review.prompts[1], "fixed.go") {
		t.Fatalf("re-review prompt = %q, want it to carry the diff after the fix round", review.prompts[1])
	}
	rec := reported.last(t)
	if rec.Ready {
		t.Fatal("ready = true, want false: the re-review still reports a finding")
	}
	if !strings.Contains(rec.LoopDetail, remaining) {
		t.Fatalf("loop detail = %q, want it to name the finding still open", rec.LoopDetail)
	}
	if strings.Contains(rec.LoopDetail, first) {
		t.Fatalf("loop detail = %q, want only the findings the re-review left open", rec.LoopDetail)
	}
}

// TestPrePRLoopCleanReReviewWithPassingChecksOpensReady is FR-59's third
// acceptance criterion: a fix round that satisfies the re-review, with the
// checks still passing, opens the PR as ready rather than a draft.
func TestPrePRLoopCleanReReviewWithPassingChecksOpensReady(t *testing.T) {
	const finding = "the retry never bounds its attempts"
	agent := &fakeAgent{
		edit:   edit("version.go", "package x\n"),
		events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	review := &fakeAgent{eventsFn: func(call int) string {
		if call == 1 {
			return reviewEvent(finding)
		}
		return reviewEvent("")
	}}
	deps.ReviewAgent = review
	c := testConfig(t, initRepo(t))

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if review.calls != 2 {
		t.Fatalf("review agent ran %d times, want the review pass then the re-review", review.calls)
	}
	if !res.Ready {
		t.Fatalf("ready = false, want true: the re-review was clean and the checks passed (detail %q)", res.LoopDetail)
	}
	rec := reported.last(t)
	if !rec.Ready || rec.LoopDetail != "" {
		t.Fatalf("record ready = %v, detail = %q, want ready with no detail", rec.Ready, rec.LoopDetail)
	}
}

// TestPrePRLoopCleanReReviewWithAFailingCheckStaysADraft covers the other side
// of FR-59's ready decision: a clean re-review cannot open the PR when the fix
// round newly broke a gate, and the detail names the gate rather than a
// finding.
func TestPrePRLoopCleanReReviewWithAFailingCheckStaysADraft(t *testing.T) {
	const finding = "the retry never bounds its attempts"
	agent := &fakeAgent{
		edit:   edit("version.go", "package x\n"),
		events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	review := &fakeAgent{eventsFn: func(call int) string {
		if call == 1 {
			return reviewEvent(finding)
		}
		return reviewEvent("")
	}}
	deps.ReviewAgent = review
	checkCalls := 0
	deps.Checks = func(context.Context, string) (string, string, error) {
		checkCalls++
		if checkCalls == 2 {
			return checkVet, "vet failed: bad format", nil
		}
		return "", "", nil
	}
	c := testConfig(t, initRepo(t))

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if review.calls != 2 {
		t.Fatalf("review agent ran %d times, want 2: the re-review runs even when the checks fail", review.calls)
	}
	if res.Ready {
		t.Fatal("ready = true, want false: the fix round broke a gate")
	}
	if !strings.Contains(res.LoopDetail, checkVet) {
		t.Fatalf("loop detail = %q, want it to name the failing gate", res.LoopDetail)
	}
	if strings.Contains(res.LoopDetail, "review findings open") {
		t.Fatalf("loop detail = %q, want no findings listed: the re-review was clean", res.LoopDetail)
	}
	rec := reported.last(t)
	if rec.Ready {
		t.Fatalf("record ready = true, want false (detail %q)", rec.LoopDetail)
	}
}

// TestPrePRLoopLabelsFindingsNotReReviewedWhenTheBudgetIsSpent is FR-59's
// second acceptance criterion: when the run-duration budget leaves no room for
// the re-review, the detail must not claim the findings are open — it labels
// them addressed by the fix round but not re-reviewed.
func TestPrePRLoopLabelsFindingsNotReReviewedWhenTheBudgetIsSpent(t *testing.T) {
	const finding = "the retry never bounds its attempts"
	agent := &fakeAgent{
		edit:   edit("version.go", "package x\n"),
		events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	review := &fakeAgent{events: reviewEvent(finding)}
	deps.ReviewAgent = review
	started := time.Now()
	clock := started
	deps.Now = func() time.Time { return clock }
	checkCalls := 0
	deps.Checks = func(context.Context, string) (string, string, error) {
		checkCalls++
		if checkCalls == 2 {
			clock = started.Add(checkLoopBudget + time.Minute)
		}
		return "", "", nil
	}
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if review.calls != 1 {
		t.Fatalf("review agent ran %d times, want 1: the budget was spent before the re-review", review.calls)
	}
	rec := reported.last(t)
	if rec.Ready {
		t.Fatal("ready = true, want false: a finding was never re-reviewed")
	}
	if !strings.Contains(rec.LoopDetail, "addressed by a fix round, not re-reviewed") {
		t.Fatalf("loop detail = %q, want the addressed-not-re-reviewed label", rec.LoopDetail)
	}
	if !strings.Contains(rec.LoopDetail, finding) {
		t.Fatalf("loop detail = %q, want it to carry the finding", rec.LoopDetail)
	}
	if strings.Contains(rec.LoopDetail, "review findings open") {
		t.Fatalf("loop detail = %q, must not call the findings open when they were not re-reviewed", rec.LoopDetail)
	}
}

func TestReviewAdvancesTheReviewOrderOnAllowanceExhaustion(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n")}
	review := &fakeAgent{
		events: reviewEvent(""),
		errFn: func(call int) error {
			if call == 1 {
				return errors.New("exit status 1")
			}
			return nil
		},
		stderrFn: func(call int) string {
			if call == 1 {
				return allowanceStderr
			}
			return ""
		},
	}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.ReviewAgent = review
	c := testConfig(t, initRepo(t))
	c.ReviewModels = []string{"p/r1", "p/r2"}

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if review.calls != 2 {
		t.Fatalf("review agent ran %d times, want 2: an exhausted allowance moves on to the backup", review.calls)
	}
	models := stepModels(reported.last(t), PhaseReview)
	if !slices.Equal(models, c.ReviewModels) {
		t.Fatalf("review ran on %v, want the ordered list %v", models, c.ReviewModels)
	}
}

// exitClassifiedAgent classifies its failures by exit code alone, the way a
// harness reads a server error it prints nothing about.
type exitClassifiedAgent struct{ *fakeAgent }

func (exitClassifiedAgent) Classify(_ string, exitErr error) (Outcome, StopReason) {
	var exit *exec.ExitError
	if errors.As(exitErr, &exit) && exit.ExitCode() == 7 {
		return OutcomeInfraFailure, StopModelUnavailable
	}
	return OutcomeAgentFailed, StopAgentExit
}

// exitWith is the *exec.ExitError of a process that exited with code.
func exitWith(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("exit %d succeeded", code)
	}
	return err
}

func TestReviewMovesToItsBackupWhenTheFirstModelIsUnavailable(t *testing.T) {
	unavailable := exitWith(t, 7)
	review := exitClassifiedAgent{&fakeAgent{
		events: reviewEvent(""),
		errFn: func(call int) error {
			if call == 1 {
				return unavailable
			}
			return nil
		},
	}}
	deps, _, reported := testDeps(validObjects(), &fakeAgent{edit: edit("version.go", "package x\n")})
	deps.ReviewAgent = review
	c := testConfig(t, initRepo(t))
	c.ReviewModels = []string{"p/r1", "p/r2"}

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	models := stepModels(reported.last(t), PhaseReview)
	if !slices.Equal(models, c.ReviewModels) {
		t.Fatalf("review ran on %v, want %v: the backup completes the review", models, c.ReviewModels)
	}
}

func TestFreeTierReviewRunsOnTheFreeModelThatDidNotBuild(t *testing.T) {
	deps, _, reported := testDeps(validObjects(), &fakeAgent{edit: edit("version.go", "package x\n")})
	deps.ReviewAgent = &fakeAgent{events: reviewEvent("")}
	c := freeTierConfig(t, "p/free-a", "p/free-b")

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	sum := reported.last(t)
	if got := stepModels(sum, PhaseBuild); !slices.Equal(got, []string{"p/free-a"}) {
		t.Fatalf("build ran on %v, want the free list's first model", got)
	}
	if got := stepModels(sum, PhaseReview); !slices.Equal(got, []string{"p/free-b"}) {
		t.Fatalf("review ran on %v, want the free model the build did not use", got)
	}
	if !sum.Ready {
		t.Fatalf("ready = false (%q), want a clean review to leave the run ready", sum.LoopDetail)
	}
}

func TestFreeTierBuildFallsThroughTheFreeModels(t *testing.T) {
	build := exitClassifiedAgent{&fakeAgent{
		edit: edit("version.go", "package x\n"),
		errFn: func(call int) error {
			if call == 1 {
				return exitWith(t, 7)
			}
			return nil
		},
	}}
	deps, _, reported := testDeps(validObjects(), &fakeAgent{})
	deps.Agent, deps.ReviewAgent = build, &fakeAgent{events: reviewEvent("")}
	c := freeTierConfig(t, "p/free-a", "p/free-b", "p/free-c")

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	sum := reported.last(t)
	if got := stepModels(sum, PhaseBuild); !slices.Equal(got, []string{"p/free-a", "p/free-b"}) {
		t.Fatalf("build ran on %v, want the unavailable model's successor", got)
	}
	if got := stepModels(sum, PhaseReview); !slices.Equal(got, []string{"p/free-c"}) {
		t.Fatalf("review ran on %v, want the free model left after both build attempts", got)
	}
	if !sum.Ready {
		t.Fatalf("ready = false (%q), want a clean review to leave the run ready", sum.LoopDetail)
	}
}

func TestFreeTierRunWithNoFreeModelLeftDraftsWithoutAReview(t *testing.T) {
	build := exitClassifiedAgent{&fakeAgent{
		edit: edit("version.go", "package x\n"),
		errFn: func(call int) error {
			if call == 1 {
				return exitWith(t, 7)
			}
			return nil
		},
	}}
	review := &fakeAgent{events: reviewEvent("")}
	deps, _, reported := testDeps(validObjects(), &fakeAgent{})
	deps.Agent, deps.ReviewAgent = build, review
	c := freeTierConfig(t, "p/free-a", "p/free-b")

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if review.calls != 0 {
		t.Fatalf("the review ran %d times, want none: the build used every free model", review.calls)
	}
	sum := reported.last(t)
	if sum.Ready {
		t.Fatal("ready = true, want a draft with no free model left to review")
	}
	if !strings.Contains(sum.LoopDetail, "none was left to review") {
		t.Fatalf("loop detail = %q, want it to name the missing reviewer", sum.LoopDetail)
	}
	if got := stepModels(sum, PhaseReview); len(got) != 0 {
		t.Fatalf("review steps = %v, want none", got)
	}
}

func TestFreeTierReReviewRunsOnTheFreeModelLeftAfterTheFixRound(t *testing.T) {
	const finding = "the retry never bounds its attempts"
	build := &fakeAgent{edit: edit("version.go", "package x\n")}
	review := &fakeAgent{eventsFn: func(call int) string {
		if call == 1 {
			return reviewEvent(finding)
		}
		return reviewEvent("")
	}}
	deps, _, reported := testDeps(validObjects(), build)
	deps.ReviewAgent = review
	c := freeTierConfig(t, "p/free-a", "p/free-b")

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	sum := reported.last(t)
	if got := stepModels(sum, PhaseReview); !slices.Equal(got, []string{"p/free-b", "p/free-b"}) {
		t.Fatalf("review ran on %v, want the free model left after the build on both passes", got)
	}
	if !sum.Ready {
		t.Fatalf("ready = false (%q), want a fixed diff, cleanly re-reviewed, to leave the run ready", sum.LoopDetail)
	}
}

func TestFreeTierFixRoundDoesNotFallThroughToTheReviewModel(t *testing.T) {
	const finding = "the retry never bounds its attempts"
	build := exitClassifiedAgent{&fakeAgent{
		edit: edit("version.go", "package x\n"),
		errFn: func(call int) error {
			if call == 2 {
				return exitWith(t, 7)
			}
			return nil
		},
	}}
	review := &fakeAgent{events: reviewEvent(finding)}
	deps, _, reported := testDeps(validObjects(), &fakeAgent{})
	deps.Agent, deps.ReviewAgent = build, review
	c := freeTierConfig(t, "p/free-a", "p/free-b")

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	sum := reported.last(t)
	if got := stepModels(sum, PhaseBuild); !slices.Equal(got, []string{"p/free-a", "p/free-a"}) {
		t.Fatalf("build ran on %v, want the fix round to stay on the model that built the run", got)
	}
	if sum.Ready {
		t.Fatal("ready = true, want a draft: the fix round had no model to run on")
	}
}

func TestBuildStopsWhenTheFreeListDoesNotStartOnTheBuildModel(t *testing.T) {
	agent := &fakeAgent{}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Model = "p/paid"
	c.Ticket.FreeModels = []string{"p/free-a", "p/free-b"}

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build accepted a free list its build model does not start")
	}
	if agent.calls != 0 {
		t.Fatalf("agent ran %d times, want 0", agent.calls)
	}
	if sum := reported.last(t); sum.Outcome != OutcomeStopped || sum.StopReason != StopModelInvalid {
		t.Fatalf("summary = %s/%s, want stopped/model-invalid", sum.Outcome, sum.StopReason)
	}
}

func TestAFailedAgentRunLogsItsExitCodeAndRedactedStderr(t *testing.T) {
	const secret = "sk-live-0123456789"
	agent := &fakeAgent{stderr: "dialling with " + secret + "\nError: server 502", err: exitWith(t, 7)}
	deps, _, _ := testDeps(validObjects(), agent)
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	c := testConfig(t, initRepo(t))
	c.Secrets = []string{secret}

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("the build succeeded, want the agent failure")
	}
	var line struct {
		Msg, Phase, Model, Outcome, Reason, StderrTail string
		ExitCode                                       int
	}
	for l := range strings.Lines(logs.String()) {
		if strings.Contains(l, `"msg":"agentFailed"`) {
			if err := json.Unmarshal([]byte(l), &line); err != nil {
				t.Fatal(err)
			}
		}
	}
	if line.Msg != "agentFailed" {
		t.Fatalf("no agentFailed line in %s", logs.String())
	}
	if line.Phase != string(PhaseBuild) || line.Model != c.Model || line.Outcome != string(OutcomeAgentFailed) ||
		line.Reason != string(StopAgentExit) || line.ExitCode != 7 {
		t.Errorf("agentFailed = %+v, want the build phase, model, classification and exit code 7", line)
	}
	if want := "dialling with [redacted]\nError: server 502"; line.StderrTail != want {
		t.Errorf("stderrTail = %q, want %q", line.StderrTail, want)
	}
}

func TestAFailedAgentRunNeverLogsTheEndOfASecretTheTailCuts(t *testing.T) {
	const secret = "sk-live-0123456789"
	tail := strings.Repeat("y", 2043)
	agent := &fakeAgent{stderr: secret + tail, err: errors.New("exit status 1")}
	deps, _, _ := testDeps(validObjects(), agent)
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	c := testConfig(t, initRepo(t))
	c.Secrets = []string{secret}

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("the build succeeded, want the agent failure")
	}
	if !strings.Contains(logs.String(), `"stderrTail":"`+tail+`"`) {
		t.Fatalf("logs = %s, want a stderr tail of only the bytes after the secret", logs.String())
	}
}

func TestAnAgentFailureWithoutAnExitLogsExitCodeMinusOne(t *testing.T) {
	deps, _, _ := testDeps(validObjects(), &fakeAgent{err: errors.New("could not start")})
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	if _, err := Build(context.Background(), deps, testConfig(t, initRepo(t))); err == nil {
		t.Fatal("the build succeeded, want the agent failure")
	}
	if !strings.Contains(logs.String(), `"msg":"agentFailed"`) || !strings.Contains(logs.String(), `"exitCode":-1`) {
		t.Fatalf("logs = %s, want agentFailed with exitCode -1", logs.String())
	}
}

func TestATimedOutAgentRunLogsWhyItFailed(t *testing.T) {
	deps, _, _ := testDeps(validObjects(), nil)
	deps.Agent = blockingAgent{}
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	c := testConfig(t, initRepo(t))
	c.AgentTimeout = 100 * time.Millisecond

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded past the agent deadline")
	}
	if !strings.Contains(logs.String(), `"msg":"agentFailed"`) || !strings.Contains(logs.String(), `"reason":"`+string(StopAgentTimeout)+`"`) {
		t.Fatalf("logs = %s, want agentFailed naming the timeout", logs.String())
	}
}

func TestRedactedTail(t *testing.T) {
	long := strings.Repeat("a", 3000) + "END"
	for name, tc := range map[string]struct {
		in      string
		limit   int
		secrets []string
		want    string
	}{
		"cut to its end":           {long, 2048, nil, long[len(long)-2048:]},
		"short kept whole":         {"boom", 2048, nil, "boom"},
		"a secret redacted":        {"key=s3cret!", 64, []string{"s3cret"}, "key=[redacted]!"},
		"every occurrence":         {"s3cret s3cret", 64, []string{"s3cret"}, "[redacted] [redacted]"},
		"an empty secret ignored":  {"boom", 64, []string{""}, "boom"},
		"a split secret dropped":   {"xxs3cret tail", 8, []string{"s3cret"}, " tail"},
		"a secret before the tail": {"s3cretxxxxxxxxxx", 4, []string{"s3cret"}, "xxxx"},
		"overlapping secrets":      {"abcd!", 64, []string{"abc", "bcd"}, "[redacted]!"},
		"invalid UTF-8 dropped":    {"é tail", 6, nil, " tail"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := redactedTail([]byte(tc.in), tc.limit, tc.secrets); got != tc.want {
				t.Fatalf("redactedTail = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrePRLoopStopsWithinNFR1Budget(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n")}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.Checks = func(context.Context, string) (string, string, error) {
		return checkTest, "still failing", nil
	}
	started := time.Now()
	first := true
	deps.Now = func() time.Time {
		if first {
			first = false
			return started
		}
		return started.Add(checkLoopBudget + time.Minute)
	}
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 1 {
		t.Fatalf("build agent ran %d times, want 1: the budget was already spent before a second round", agent.calls)
	}
	rec := reported.last(t)
	if rec.Ready {
		t.Fatal("ready = true, want false")
	}
	for _, st := range rec.Steps {
		if st.Phase == PhaseReview {
			t.Fatalf("the review ran despite the run-duration budget being spent: %+v", rec.Steps)
		}
	}
}

// fakeClockElapsed returns a Now func whose first call anchors sum.StartedAt
// at started, and every later call reports elapsed further on — simulating a
// meaningful fraction of NFR-1's window already spent by the phases that ran
// before the call under test, regardless of how many Now calls land in between.
func fakeClockElapsed(started time.Time, elapsed time.Duration) func() time.Time {
	first := true
	return func() time.Time {
		if first {
			first = false
			return started
		}
		return started.Add(elapsed)
	}
}

// assertBoundedByRemainingBudget asserts a call's deadline reflects what
// remained of checkLoopBudget after elapsed had already passed, not the full
// defaultAgentTimeout — the regression for round 1 and the plan phase both
// having ignored NFR-1's budget entirely.
func assertBoundedByRemainingBudget(t *testing.T, deadline time.Time, elapsed time.Duration) {
	t.Helper()
	if deadline.IsZero() {
		t.Fatal("the call recorded no deadline")
	}
	got := time.Until(deadline)
	want := checkLoopBudget - elapsed
	if diff := got - want; diff < -2*time.Second || diff > 2*time.Second {
		t.Fatalf("timeout = %s, want ~%s (the remaining NFR-1 budget, not the full %s default)", got, want, defaultAgentTimeout)
	}
}

func TestPrePRLoopBoundsRound1ToTheRemainingNFR1Budget(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n")}
	deps, _, _ := testDeps(validObjects(), agent)
	elapsed := 20 * time.Minute
	deps.Now = fakeClockElapsed(time.Now(), elapsed)
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if len(agent.deadlines) == 0 {
		t.Fatal("round 1 recorded no deadline")
	}
	assertBoundedByRemainingBudget(t, agent.deadlines[0], elapsed)
}

func TestBuildBoundsThePlanPhaseCallToTheRemainingNFR1Budget(t *testing.T) {
	planAgent := &fakeAgent{events: planEvent("```plan-files\nversion.go\n```")}
	buildAgent := &fakeAgent{edit: edit("version.go", "package x\n")}
	objects := validObjects()
	objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
	deps, _, _ := testDeps(objects, buildAgent)
	deps.PlanAgent = planAgent
	elapsed := 20 * time.Minute
	deps.Now = fakeClockElapsed(time.Now(), elapsed)
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "M"

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if len(planAgent.deadlines) == 0 {
		t.Fatal("the plan phase recorded no deadline")
	}
	assertBoundedByRemainingBudget(t, planAgent.deadlines[0], elapsed)
}

// TestPrePRLoopWarnsWhenARoundPastTheFirstReportsNoSessionID is the
// regression for a round silently losing session continuity: the agent's own
// event stream carries no sessionID on round 2, so the *next* round would
// start a brand-new session with none of this run's history — a real
// occurrence must surface in the summary rather than degrade silently.
func TestPrePRLoopWarnsWhenARoundPastTheFirstReportsNoSessionID(t *testing.T) {
	agent := &fakeAgent{
		edit: edit("version.go", "package x\n"),
		eventsFn: func(call int) string {
			if call == 1 {
				return `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{}}}` + "\n"
			}
			return `{"type":"step_finish","part":{"tokens":{}}}` + "\n" // round 2: no sessionID
		},
	}
	deps, _, reported := testDeps(validObjects(), agent)
	calls := 0
	deps.Checks = func(context.Context, string) (string, string, error) {
		calls++
		if calls == 1 {
			return checkVet, "vet failed", nil
		}
		return "", "", nil
	}
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if agent.calls != 2 {
		t.Fatalf("build agent ran %d times, want 2", agent.calls)
	}
	rec := reported.last(t)
	if !strings.Contains(rec.UsageWarning, "round 2") {
		t.Fatalf("usage warning = %q, want it to note round 2's missing session id", rec.UsageWarning)
	}
}

// TestPrePRLoopStillCommitsAsADraftWhenTheReviewPassFails is the regression
// for Fix 1 making the review pass reachable: before it, the review always
// saw an empty diff, so a malformed response was near-unreachable. Now that
// it sees real content, a response with no review-findings block is a real
// failure mode, and it must not discard the build's own edits.
func TestPrePRLoopStillCommitsAsADraftWhenTheReviewPassFails(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n")}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.ReviewAgent = &fakeAgent{events: `{"type":"text","part":{"type":"text","text":"looks fine, no block here"}}` + "\n"}
	c := testConfig(t, initRepo(t))

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("Build discarded the build's edits when the review pass failed")
	}
	if res.Ready {
		t.Fatal("ready = true, want false: the review pass never produced a verdict")
	}
	if !strings.Contains(res.LoopDetail, "review") {
		t.Fatalf("loop detail = %q, want it to name the review failure", res.LoopDetail)
	}
	if agent.calls != 1 {
		t.Fatalf("build agent ran %d times, want 1: a review-phase failure gets no fix round", agent.calls)
	}
	rec := reported.last(t)
	if rec.Outcome != OutcomeBuilt || rec.Phase != PhaseCommit {
		t.Fatalf("record = %s at %s, want built at commit despite the review failure", rec.Outcome, rec.Phase)
	}
	var phases []Phase
	for _, st := range rec.Steps {
		phases = append(phases, st.Phase)
	}
	if !slices.Equal(phases, []Phase{PhaseBuild, PhaseReview}) {
		t.Fatalf("steps = %v, want the build round then the failed review attempt", phases)
	}
}

func TestClassifyAgentFailure(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		path := filepath.Join(dir, "stderr-"+strings.ReplaceAll(content, " ", "_"))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tt := range []struct {
		name       string
		path       string
		wantOut    Outcome
		wantReason StopReason
	}{
		{"an allowance marker", write("Error: allowance exhausted"), OutcomeBudgetStop, StopAllowanceExhausted},
		{"a case-insensitive marker", write("QUOTA EXCEEDED for this account"), OutcomeBudgetStop, StopAllowanceExhausted},
		{"an availability marker", write("Error: no endpoints found for this model"), OutcomeInfraFailure, StopModelUnavailable},
		{"an ordinary failure", write("panic: nil pointer"), OutcomeAgentFailed, StopAgentExit},
		{"an unreadable file", filepath.Join(dir, "does-not-exist"), OutcomeAgentFailed, StopAgentExit},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotOut, gotReason := classifyAgentFailure(&fakeAgent{}, tt.path, nil)
			if gotOut != tt.wantOut || gotReason != tt.wantReason {
				t.Fatalf("classifyAgentFailure = %s/%s, want %s/%s", gotOut, gotReason, tt.wantOut, tt.wantReason)
			}
		})
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc…"},
		{"éé", 3, "é…"},
		{"a\xffb", 5, "a\uFFFDb"},
	} {
		got := truncate(tc.in, tc.n)
		if got != tc.want || !utf8.ValidString(got) {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestModelPatternAcceptsProviderIDs(t *testing.T) {
	for _, model := range []string{"command-code/deepseek/deepseek-v4.1-flash", "openrouter/deepseek/deepseek-v4:free", "command-code/inclusionai/ling-3.1-flash:free"} {
		if !modelPattern.MatchString(model) {
			t.Errorf("rejected %q", model)
		}
	}
}

func TestProviderIsTheModelsPrefixBeforeItsFirstSlash(t *testing.T) {
	for _, tt := range []struct{ model, want string }{
		{"command-code/deepseek/deepseek-v4.1-flash", "command-code"},
		{"openrouter/deepseek/deepseek-v4:free", "openrouter"},
		{"noslash", "noslash"},
	} {
		if got := Provider(tt.model); got != tt.want {
			t.Errorf("Provider(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

type blockingAgent struct{}

func (blockingAgent) Run(ctx context.Context, _, _, _, _ string, stdout, _ io.Writer) error {
	if _, err := io.WriteString(stdout, "{}\n"); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestBuildStopsTheAgentAtItsDeadline(t *testing.T) {
	deps, completions, reported := testDeps(validObjects(), nil)
	deps.Agent = blockingAgent{}
	c := testConfig(t, initRepo(t))
	c.AgentTimeout = 100 * time.Millisecond

	if _, err := Build(context.Background(), deps, c); err == nil {
		t.Fatal("Build succeeded past the agent deadline")
	}
	rec := reported.last(t)
	if rec.StopReason != StopAgentTimeout {
		t.Fatalf("reason = %s", rec.StopReason)
	}
	if len(rec.Steps) != 1 {
		t.Fatalf("steps = %+v, want exactly one", rec.Steps)
	}
	if _, ok := completions[rec.Steps[0].CompletionsObject]; !ok {
		t.Fatal("completions were not uploaded after the deadline")
	}
}

func TestBuildUploadsCompletionsEvenWhenUsageCannotBeSummed(t *testing.T) {
	old := maxEventLine
	maxEventLine = 16
	t.Cleanup(func() { maxEventLine = old })
	longLine := strings.Repeat("x", 64) + "\n"

	tests := []struct {
		name       string
		agentErr   error
		wantReason StopReason
	}{
		{"agent succeeded", nil, ""},
		{"agent failed too", errors.New("exit status 1"), StopAgentExit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := &fakeAgent{events: longLine, err: tt.agentErr}
			deps, completions, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))

			_, _ = Build(context.Background(), deps, c)
			rec := reported.last(t)
			if len(rec.Steps) != 1 {
				t.Fatalf("steps = %+v, want exactly one", rec.Steps)
			}
			if got := completions[rec.Steps[0].CompletionsObject]; string(got) != longLine {
				t.Fatalf("completions = %q", got)
			}
			if rec.UsageWarning == "" || rec.StopReason != tt.wantReason {
				t.Fatalf("warning %q, reason %q", rec.UsageWarning, rec.StopReason)
			}
		})
	}
}

func TestBuildRefusesToBundleASecret(t *testing.T) {
	const secret = "sk-live-abcdef"
	agent := &fakeAgent{edit: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "cfg.go"), []byte(`var k = "`+secret+`"`), 0o600)
	}}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Secrets = []string{"sk-absent-from-the-branch", secret}

	res, err := Build(context.Background(), deps, c)
	if err == nil || res.Changed {
		t.Fatal("Build bundled a branch holding the secret")
	}
	rec := reported.last(t)
	if rec.StopReason != StopSecretInBranch || strings.Contains(rec.StopDetail, secret) {
		t.Fatalf("reason %s, detail %q", rec.StopReason, rec.StopDetail)
	}
	if _, err := os.Stat(filepath.Join(c.TempDir, "wingman.bundle")); err == nil {
		t.Fatal("a bundle was written")
	}
}

type panickingAgent struct{}

func (panickingAgent) Run(context.Context, string, string, string, string, io.Writer, io.Writer) error {
	panic("boom")
}

func TestBuildReportsAPanicThenRepanics(t *testing.T) {
	deps, _, reported := testDeps(validObjects(), nil)
	deps.Agent = panickingAgent{}
	c := testConfig(t, initRepo(t))

	defer func() {
		if recover() == nil {
			t.Fatal("Build swallowed the panic")
		}
		rec := reported.last(t)
		if rec.Outcome != OutcomeInfraFailure || rec.StopReason != StopPanic {
			t.Fatalf("record = %s/%s", rec.Outcome, rec.StopReason)
		}
	}()
	_, _ = Build(context.Background(), deps, c)
}

// branchPattern is the branch check in run.yml's check and pr jobs, with
// ${GITHUB_RUN_ID} standing for the run.
const branchPattern = `^wingman/[a-z0-9]+(-[a-z0-9]+)*-${GITHUB_RUN_ID}-[0-9]+$`

func TestRunWorkflowChecksTheBranchPattern(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(yml), `[[ "$BRANCH" =~ `+branchPattern); n != 2 {
		t.Fatalf("run.yml checks the branch pattern %d times, want 2 (check and pr)", n)
	}
}

// inputDefault is the first default declared after a workflow input's name, or
// "" when the input or its default is missing.
func inputDefault(yml, input string) string {
	i := strings.Index(yml, input+":\n")
	if i < 0 {
		return ""
	}
	rest := yml[i:]
	j := strings.Index(rest, "default: ")
	if j < 0 {
		return ""
	}
	line, _, _ := strings.Cut(rest[j+len("default: "):], "\n")
	return line
}

func TestWorkflowsCarryThePlanModelsFromTheDefaultToTheRunner(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, name := range []string{"run.yml", "plan-smoke.yml"} {
		if got := inputDefault(read(name), "plan_models"); got != DefaultPlanModel() {
			t.Errorf("%s: the plan_models input defaults to %q, want %s", name, got, DefaultPlanModel())
		}
	}
	for _, name := range []string{"run.yml", "review-smoke.yml"} {
		if got, want := inputDefault(read(name), "review_models"), strings.Join(DefaultReviewModels(), ","); got != want {
			t.Errorf("%s: the review_models input defaults to %q, want %s", name, got, want)
		}
	}
	if !strings.Contains(read("run.yml"), "plan_models: ${{ needs.ticket.outputs.override_plan_models || inputs.plan_models }}") {
		t.Error("run.yml does not pass plan_models to model.yml")
	}
	if !strings.Contains(read("model.yml"), `-plan-models "$PLAN_MODELS"`) {
		t.Error("model.yml does not pass plan_models to the runner")
	}
}

// TestWorkflowsHandTheTicketOffAsAnArtifactNotAJobOutput is the regression for
// FRG-60: GitHub drops any job output holding a value masked in that job, so the
// ticket travels as a file the ticket job uploads and the model job downloads,
// never as a job output or a job input.
func TestWorkflowsHandTheTicketOffAsAnArtifactNotAJobOutput(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	run := read("run.yml")
	for _, want := range []string{
		`-out "$RUNNER_TEMP/ticket.json"`,
		"name: ticket\n",
		"path: ${{ runner.temp }}/ticket.json",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("run.yml lacks %q", want)
		}
	}
	for _, gone := range []string{"steps.read.outputs.ticket", "needs.ticket.outputs.ticket"} {
		if strings.Contains(run, gone) {
			t.Errorf("run.yml still passes the ticket as a job output: %q", gone)
		}
	}
	model := read("model.yml")
	for _, want := range []string{
		"continue-on-error: true\n        with:\n          name: ticket\n",
		`-ticket-file "$RUNNER_TEMP/ticket.json"`,
	} {
		if !strings.Contains(model, want) {
			t.Errorf("model.yml lacks %q", want)
		}
	}
	if strings.Contains(model, "inputs.ticket") {
		t.Error("model.yml still takes the ticket as an input")
	}
}

// TestRunWorkflowRebasesTheBranchBeforePushing is the regression for a branch
// pushed on a stale base: the branch is rebased onto current main before it is
// pushed, so its delta against main is only the run's own commits, and a
// conflict stops the run through a named stop reason.
func TestRunWorkflowRebasesTheBranchBeforePushing(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	run := string(yml)
	for _, want := range []string{
		`git -c transfer.fsckObjects=true fetch "$RUNNER_TEMP/wingman.bundle" "refs/heads/${BRANCH}:refs/heads/${BRANCH}"`,
		"git fetch --quiet origin main",
		`git checkout -q "$BRANCH"`,
		"git rebase origin/main",
		"git rebase --abort",
		`echo "stop_reason=rebase-conflict" >> "$GITHUB_OUTPUT"`,
		"if: steps.rebase.outputs.stop_reason == ''",
		"if: steps.rebase.outputs.stop_reason != ''",
		"PR_STOP_REASON: ${{ needs.pr.outputs.stop_reason }}",
		`-pr-stop-reason "$PR_STOP_REASON"`,
	} {
		if !strings.Contains(run, want) {
			t.Errorf("run.yml lacks %q", want)
		}
	}
	// Order is the point, not presence: a push ahead of the rebase is the defect
	// this change exists for, and both steps would still be in the file.
	order := []string{
		`fetch "$RUNNER_TEMP/wingman.bundle"`,
		"git fetch --quiet origin main",
		"git rebase origin/main",
		`git push origin "refs/heads/${BRANCH}"`,
		"name: Stop on a conflicting rebase",
		"name: Open the PR",
	}
	prev := -1
	for _, marker := range order {
		i := strings.Index(run, marker)
		if i < 0 {
			t.Errorf("run.yml lacks %q", marker)
			continue
		}
		if i < prev {
			t.Errorf("run.yml places %q before the step it must follow", marker)
		}
		prev = i
	}
}

// TestRunWorkflowRebaseStepRunsOnAFreshRunner executes run.yml's own rebase
// script in a scratch repository with no git identity, as on a fresh runner. A
// rebase writes commits, so without the step's committer env it fails exactly
// when main has moved; a conflict must be named and anything else must fail.
func TestRunWorkflowRebaseStepRunsOnAFreshRunner(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	run := string(yml)
	step := run[strings.Index(run, "id: rebase"):]
	step = step[:strings.Index(step, "id: push")]
	envValue := func(name string) string {
		m := regexp.MustCompile(`(?m)^\s+` + name + `: (.+)$`).FindStringSubmatch(step)
		if m == nil {
			t.Fatalf("rebase step sets no %s", name)
		}
		return m[1]
	}
	committerName, committerEmail := envValue("GIT_COMMITTER_NAME"), envValue("GIT_COMMITTER_EMAIL")
	if committerName != commitAuthorName || committerEmail != commitAuthorEmail {
		t.Errorf("rebase committer = %s <%s>, want the run's identity %s <%s>",
			committerName, committerEmail, commitAuthorName, commitAuthorEmail)
	}
	start := strings.Index(step, "git fetch --quiet origin main")
	end := strings.LastIndex(step, "\n          fi\n")
	if start < 0 || end < 0 {
		t.Fatal("rebase step lacks its fetch-and-rebase block")
	}
	script := strings.ReplaceAll(step[start:end+len("\n          fi")], "\n          ", "\n")

	for _, tc := range []struct {
		name       string
		mainFile   string
		noIdentity bool
		wantFail   bool
		wantStop   bool
		wantOnTop  bool
	}{
		{"main moved without a conflict", "other.txt", false, false, false, true},
		{"main moved with a conflict", "run.txt", false, false, true, false},
		// A failure that is not a conflict must fail the job, never read as one.
		{"a rebase that fails without a conflict", "other.txt", true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git := func(cwd string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-c", "user.name=setup", "-c", "user.email=setup@example.com"}, args...)...)
				cmd.Dir = cwd
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			origin, work := filepath.Join(dir, "origin"), filepath.Join(dir, "work")
			git(dir, "init", "-q", "--bare", "-b", "main", origin)
			git(dir, "clone", "-q", origin, work)
			git(work, "commit", "-q", "--allow-empty", "-m", "base")
			git(work, "push", "-q", "origin", "HEAD:main")
			git(work, "checkout", "-q", "-b", "wingman/x-1-1")
			if err := os.WriteFile(filepath.Join(work, "run.txt"), []byte("run\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(work, "add", "run.txt")
			git(work, "commit", "-q", "-m", "the run's work")
			git(work, "checkout", "-q", "--detach", "main")
			// Main moves after the run was dispatched.
			other := filepath.Join(dir, "other")
			git(dir, "clone", "-q", origin, other)
			if err := os.WriteFile(filepath.Join(other, tc.mainFile), []byte("main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(other, "add", tc.mainFile)
			git(other, "commit", "-q", "-m", "main moved")
			git(other, "push", "-q", "origin", "HEAD:main")

			output := filepath.Join(dir, "github_output")
			cmd := exec.Command("bash", "-e", "-c", script)
			cmd.Dir = work
			// user.useConfigOnly stops git inventing an identity from the hostname, as
			// macOS does, so a missing committer fails here as it does on the runner.
			cmd.Env = append(os.Environ(), "HOME="+dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
				"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.useConfigOnly", "GIT_CONFIG_VALUE_0=true",
				"BRANCH=wingman/x-1-1", "GITHUB_OUTPUT="+output)
			if !tc.noIdentity {
				cmd.Env = append(cmd.Env, "GIT_COMMITTER_NAME="+committerName, "GIT_COMMITTER_EMAIL="+committerEmail)
			}
			if out, err := cmd.CombinedOutput(); (err != nil) != tc.wantFail {
				t.Fatalf("rebase step error = %v, want failure %v\n%s", err, tc.wantFail, out)
			}
			written, _ := os.ReadFile(output)
			if got := strings.Contains(string(written), "stop_reason=rebase-conflict"); got != tc.wantStop {
				t.Errorf("stop_reason written = %v, want %v (output %q)", got, tc.wantStop, written)
			}
			onTop := git(work, "rev-parse", "wingman/x-1-1~1") == git(work, "rev-parse", "origin/main")
			if onTop != tc.wantOnTop {
				t.Errorf("branch rebased onto main = %v, want %v", onTop, tc.wantOnTop)
			}
		})
	}
}

func TestBranchNameMatchesThePRJobsPattern(t *testing.T) {
	pattern := regexp.MustCompile(strings.ReplaceAll(branchPattern, "${GITHUB_RUN_ID}", "42"))
	for _, id := range []string{"ABC-12", "xyz-7", "A1-2-3", "x"} {
		tk := testTicket
		tk.ID = id
		if err := tk.Validate(); err != nil {
			t.Fatalf("%q: %v", id, err)
		}
		if got := BranchName(tk.BranchSegment(), "42-3"); !pattern.MatchString(got) {
			t.Errorf("branch %q does not match the pr job's pattern", got)
		}
	}
	for _, branch := range []string{"wingman/ABC-12-42-3", "wingman/abc--12-42-3", "wingman/abc-12-43-3", "wingman/-42-3"} {
		if pattern.MatchString(branch) {
			t.Errorf("pattern accepts %q", branch)
		}
	}
}

func TestBuildRefusesAMismatchedIdentityBeforeTheAgent(t *testing.T) {
	for name, id := range map[string]Identity{
		"no account":          {Owner: "octo"},
		"another account":     {Account: "work-account", Owner: "octo"},
		"a GitHub token held": {Account: "octo", Owner: "octo", GitHubToken: true},
	} {
		t.Run(name, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, completions, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))
			c.Identity = id

			res, err := Build(context.Background(), deps, c)
			if !errors.Is(err, ErrIdentityMismatch) || res.Changed {
				t.Fatalf("err = %v, result %+v", err, res)
			}
			if agent.calls != 0 || len(completions) != 0 {
				t.Fatalf("agent ran %d times, completions %v", agent.calls, completions)
			}
			if sum := reported.last(t); sum.Outcome != OutcomeStopped || sum.StopReason != StopIdentityMismatch {
				t.Fatalf("summary = %s/%s", sum.Outcome, sum.StopReason)
			}
		})
	}
}

func TestBuildChecksTheIdentityBeforeTheTicket(t *testing.T) {
	deps, _, reported := testDeps(validObjects(), &fakeAgent{})
	c := testConfig(t, initRepo(t))
	c.Identity = Identity{Account: "octo", Owner: "octo", GitHubToken: true}
	c.Ticket = Ticket{}
	if _, err := Build(context.Background(), deps, c); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
	}
	if sum := reported.last(t); sum.StopReason != StopIdentityMismatch {
		t.Fatalf("stop reason = %s", sum.StopReason)
	}
}

func TestBuildStopsOnAnUnbuildableTicketBeforeTheAgent(t *testing.T) {
	for name, tk := range map[string]Ticket{
		"no ticket":         {},
		"no body":           {ID: "ABC-12", Title: "t", Size: "S"},
		"an unsafe id":      {ID: "../x", Title: "t", Size: "S", Body: "b"},
		"a multiline title": {ID: "ABC-12", Title: "t\nu", Size: "S", Body: "b"},
	} {
		t.Run(name, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, _, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))
			c.Ticket = tk

			if _, err := Build(context.Background(), deps, c); !errors.Is(err, ErrTicketInvalid) {
				t.Fatalf("err = %v, want ErrTicketInvalid", err)
			}
			if agent.calls != 0 {
				t.Fatalf("agent ran %d times", agent.calls)
			}
			if sum := reported.last(t); sum.Outcome != OutcomeStopped || sum.StopReason != StopTicketMissing {
				t.Fatalf("summary = %s/%s", sum.Outcome, sum.StopReason)
			}
		})
	}
}

func TestBuildDependsOnNoRecordStore(t *testing.T) {
	store := reflect.TypeFor[RecordStore]()
	deps := reflect.TypeFor[BuildDeps]()
	for f := range deps.Fields() {
		if f.Type.Implements(store) {
			t.Errorf("BuildDeps.%s can write run records", f.Name)
		}
	}
}

func TestBuildReportsASummaryTheRecordJobAccepts(t *testing.T) {
	agent := &fakeAgent{
		edit: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "version.go"), []byte("package x\n"), 0o600)
		},
		events: `{"type":"step_finish","part":{"tokens":{"input":10,"output":2},"cost":0.5}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	raw, err := reported.last(t).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(raw, "\r\n") {
		t.Fatalf("summary spans lines: %q", raw)
	}
	got, err := ParseSummary(raw, c.AttemptID, c.Ticket, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != OutcomeBuilt || got.Branch != BranchName(c.Ticket.BranchSegment(), c.AttemptID) ||
		len(got.Steps) != 2 || got.Steps[0].Tokens.Cost != 0.5 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestBuildFailsWhenTheSummaryCannotBeReported(t *testing.T) {
	deps, _, _ := testDeps(validObjects(), &fakeAgent{})
	deps.Report = func(Summary) error { return errors.New("GITHUB_OUTPUT unwritable") }
	_, err := Build(context.Background(), deps, testConfig(t, initRepo(t)))
	if !errors.Is(err, ErrSummaryUnreported) {
		t.Fatalf("err = %v, want it marked unreported", err)
	}

	deps, _, _ = testDeps(fakeObjects{}, &fakeAgent{})
	deps.Report = func(Summary) error { return errors.New("GITHUB_OUTPUT unwritable") }
	_, err = Build(context.Background(), deps, testConfig(t, initRepo(t)))
	var stopped *StopError
	if !errors.As(err, &stopped) || !errors.Is(err, ErrSummaryUnreported) {
		t.Fatalf("err = %v, want a stop marked unreported", err)
	}
}

// TestRunAgentLogsEveryModelRequest asserts one modelRequest line per
// step_finish event, carrying that request's own tokens.
func TestRunAgentLogsEveryModelRequest(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n"), events: strings.Join([]string{
		`{"type":"step_finish","part":{"tokens":{"input":14946,"output":104,"cache":{"read":5248}}}}`,
		`{"type":"text","part":{"text":"reading"}}`,
		`{"type":"step_finish","part":{"tokens":{"input":15210,"output":96,"cache":{"read":14848}}}}`,
	}, "\n") + "\n"}
	deps, _, _ := testDeps(validObjects(), agent)
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	type request struct {
		Phase     string `json:"phase"`
		Round     int    `json:"round"`
		Request   int    `json:"request"`
		Input     int64  `json:"input"`
		CacheRead int64  `json:"cacheRead"`
		Output    int64  `json:"output"`
	}
	var got []request
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var l struct {
			Msg string `json:"msg"`
			request
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatal(err)
		}
		if l.Msg == "modelRequest" && l.Phase == string(PhaseBuild) {
			got = append(got, l.request)
		}
	}
	want := []request{
		{string(PhaseBuild), 1, 1, 14946, 5248, 104},
		{string(PhaseBuild), 1, 2, 15210, 14848, 96},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("modelRequest lines = %+v, want %+v", got, want)
	}
}

// commitBlock is a valid commit-message block for testTicket naming subject.
func commitBlock(subject string) string {
	return "Done.\n\n```commit-message\n" + subject + "\n\nAdds the version file.\n\n- add version.go\n```"
}

func TestBuildCommitsWithTheAgentsCommitMessage(t *testing.T) {
	agent := &fakeAgent{edit: edit("version.go", "package x\n"), events: planEvent(commitBlock("feat(x): ABC-12 add the version file"))}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Ticket.Title = "[BE] Add a file"

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, c.Repo, "log", "-1", "--format=%B", res.Branch); got != "feat(x): ABC-12 add the version file\n\n- add version.go\n\n" {
		t.Fatalf("commit message = %q", got)
	}
	sum := reported.last(t)
	if sum.CommitSubject != "feat(x): ABC-12 add the version file" || sum.CommitBody != "- add version.go" ||
		sum.PRSummary != "Adds the version file." {
		t.Fatalf("summary commit fields = %q, %q, %q", sum.CommitSubject, sum.CommitBody, sum.PRSummary)
	}
}

func TestBuildFallsBackToTheLaneAndPackageWithoutAValidBlock(t *testing.T) {
	agent := &fakeAgent{
		edit: func(dir string) error {
			if err := os.MkdirAll(filepath.Join(dir, "internal", "runner"), 0o700); err != nil {
				return err
			}
			return edit("internal/runner/x.go", "package runner\n")(dir)
		},
		events: planEvent(commitBlock("feat: add it")),
	}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Ticket.Title = "[INFRA] Add a file"

	res, err := Build(context.Background(), deps, c)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, c.Repo, "log", "-1", "--format=%B", res.Branch); got != "chore(runner): ABC-12 add a file\n\nAdd a file.\n\n" {
		t.Fatalf("commit message = %q", got)
	}
	sum := reported.last(t)
	if sum.CommitSubject != "chore(runner): ABC-12 add a file" || sum.CommitBody != c.Ticket.Body || sum.PRSummary != "Add a file" {
		t.Fatalf("summary commit fields = %q, %q, %q", sum.CommitSubject, sum.CommitBody, sum.PRSummary)
	}
}

func TestBuildKeepsAValidCommitMessageOverALaterInvalidOne(t *testing.T) {
	agent := &fakeAgent{
		edit: edit("version.go", "package x\n"),
		eventsFn: func(call int) string {
			if call == 1 {
				return planEvent(commitBlock("feat(x): ABC-12 add the version file"))
			}
			return planEvent(commitBlock("bound the retries"))
		},
	}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.ReviewAgent = &fakeAgent{events: reviewEvent("the retry never bounds its attempts")}
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if sum := reported.last(t); sum.CommitSubject != "feat(x): ABC-12 add the version file" {
		t.Fatalf("commit subject = %q, want the build round's valid one", sum.CommitSubject)
	}
}

func TestBuildFallbackScopesFilesTheAgentCommittedItself(t *testing.T) {
	agent := &fakeAgent{
		edit: func(dir string) error {
			if err := os.MkdirAll(filepath.Join(dir, "internal", "dispatcher"), 0o700); err != nil {
				return err
			}
			if err := edit("internal/dispatcher/x.go", "package dispatcher\n")(dir); err != nil {
				return err
			}
			cmd := exec.Command("git", "-c", "user.name=a", "-c", "user.email=a@a", "commit", "-qam", "wip")
			cmd.Dir = dir
			if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
				return fmt.Errorf("git add: %v: %s", err, out)
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("git commit: %v: %s", err, out)
			}
			return nil
		},
		events: planEvent("Done."),
	}
	deps, _, reported := testDeps(validObjects(), agent)
	c := testConfig(t, initRepo(t))
	c.Ticket.Title = "[BE] Add a file"

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if sum := reported.last(t); sum.CommitSubject != "feat(dispatcher): ABC-12 add a file" {
		t.Fatalf("commit subject = %q, want the scope of the file the agent committed", sum.CommitSubject)
	}
}

func TestBuildCommitsWithTheFixRoundsCommitMessage(t *testing.T) {
	agent := &fakeAgent{
		edit: edit("version.go", "package x\n"),
		eventsFn: func(call int) string {
			if call == 1 {
				return planEvent(commitBlock("feat(x): ABC-12 add the version file"))
			}
			return planEvent(commitBlock("fix(x): ABC-12 bound the retries"))
		},
	}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.ReviewAgent = &fakeAgent{events: reviewEvent("the retry never bounds its attempts")}
	c := testConfig(t, initRepo(t))

	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(agent.prompts[1], commitMessageInstruction) {
		t.Fatalf("fix round prompt = %q, want it to end with the commit-message instruction", agent.prompts[1])
	}
	if sum := reported.last(t); sum.CommitSubject != "fix(x): ABC-12 bound the retries" {
		t.Fatalf("commit subject = %q, want the fix round's", sum.CommitSubject)
	}
}

func TestDetailPartsDropsEmptyParts(t *testing.T) {
	for _, tt := range []struct {
		parts []string
		want  string
	}{
		{[]string{"review findings open: x", ""}, "review findings open: x"},
		{[]string{"", "checks: vet failing"}, "checks: vet failing"},
		{[]string{"a", "b"}, "a; b"},
	} {
		if got := detailParts(tt.parts...); got != tt.want {
			t.Errorf("detailParts(%q) = %q, want %q", tt.parts, got, tt.want)
		}
	}
}
