package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

// testFixReviewModel is the findings-only reviewer these tests configure, on a
// plan whose provider-setting markers the classifier knows.
const testFixReviewModel = "opencode-go/fix-reviewer"

// reviewScript is a scripted reply per bound review model, with every prompt
// each model received.
type reviewScript struct {
	replies map[string]func(ctx context.Context, call int, stdout, stderr io.Writer) error
	prompts map[string][]string
	// deadlines is each model's call deadlines, in order.
	deadlines map[string][]time.Time
}

// scriptedReviewer runs the reply its script holds for the model it is bound to.
type scriptedReviewer struct {
	script *reviewScript
	model  string
}

func (r scriptedReviewer) WithModel(model string) Agent {
	r.model = model
	return r
}

func (r scriptedReviewer) Run(ctx context.Context, _, _, prompt, _ string, stdout, stderr io.Writer) error {
	r.script.prompts[r.model] = append(r.script.prompts[r.model], prompt)
	deadline, _ := ctx.Deadline()
	r.script.deadlines[r.model] = append(r.script.deadlines[r.model], deadline)
	return r.script.replies[r.model](ctx, len(r.script.prompts[r.model]), stdout, stderr)
}

// findingsPerCall replies to the nth call with the nth block, repeating the last.
func findingsPerCall(blocks ...string) func(context.Context, int, io.Writer, io.Writer) error {
	return func(_ context.Context, call int, stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, reviewEvent(blocks[min(call, len(blocks))-1]))
		return err
	}
}

// fixReviewRun builds testConfig's ticket with the review model answering muse
// and testFixReviewModel answering fix, and returns the record, the build agent,
// the script and the logs.
func fixReviewRun(t *testing.T, muse, fix func(context.Context, int, io.Writer, io.Writer) error, prepare func(*BuildDeps, *BuildConfig)) (Summary, *fakeAgent, *reviewScript, string) {
	t.Helper()
	agent := &fakeAgent{
		edit:   edit("version.go", "package x\n"),
		events: `{"type":"step_finish","sessionID":"ses_1","part":{"tokens":{}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	var logs bytes.Buffer
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	script := &reviewScript{prompts: map[string][]string{}, deadlines: map[string][]time.Time{}}
	deps.ReviewAgent = scriptedReviewer{script: script}
	c := testConfig(t, initRepo(t))
	c.FixReviewModels = []string{testFixReviewModel}
	if prepare != nil {
		prepare(&deps, &c)
	}
	fixModel := testFixReviewModel
	if len(c.FixReviewModels) > 0 {
		fixModel = c.FixReviewModels[0]
	}
	script.replies = map[string]func(context.Context, int, io.Writer, io.Writer) error{"p/r": muse, fixModel: fix}
	if _, err := Build(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	return reported.last(t), agent, script, logs.String()
}

func TestFixReviewFailureIsLoggedAndTheReviewPassStillDecides(t *testing.T) {
	failWith := func(stderr string) func(context.Context, int, io.Writer, io.Writer) error {
		return func(_ context.Context, _ int, _, errOut io.Writer) error {
			_, _ = io.WriteString(errOut, stderr)
			return errors.New("exit status 1")
		}
	}
	hang := func(ctx context.Context, _ int, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	unreadable := func(_ context.Context, _ int, stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, `{"type":"text","part":{"type":"text","text":"looks fine"}}`+"\n")
		return err
	}
	for name, tc := range map[string]struct {
		reply  func(context.Context, int, io.Writer, io.Writer) error
		reason StopReason
	}{
		"an error":               {failWith("boom"), StopAgentExit},
		"a timeout":              {hang, StopAgentTimeout},
		"an exhausted allowance": {failWith("Error: allowance exhausted for this billing period"), StopAllowanceExhausted},
		"a provider setting":     {failWith("This Go model trains on request data"), StopProviderSetting},
		"no findings block":      {unreadable, StopReviewInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			rec, agent, _, logs := fixReviewRun(t, findingsPerCall(""), tc.reply, func(_ *BuildDeps, c *BuildConfig) {
				c.AgentTimeout = 500 * time.Millisecond
			})
			if !rec.Ready || rec.LoopDetail != "" || rec.Outcome != OutcomeBuilt || rec.StopReason != "" {
				t.Fatalf("record ready %v, detail %q, %s/%s; want ready and built: the review pass was clean",
					rec.Ready, rec.LoopDetail, rec.Outcome, rec.StopReason)
			}
			if agent.calls != 1 {
				t.Fatalf("build agent ran %d times, want 1: a failed fix-review starts no round", agent.calls)
			}
			if want := "msg=fixReviewFailed model=" + testFixReviewModel + " reason=" + string(tc.reason); !strings.Contains(logs, want) ||
				strings.Count(logs, "msg=fixReviewFailed") != 1 || strings.Contains(logs, "msg=fixReviewRan") {
				t.Fatalf("logs = %s\nwant one failure line, containing %q, and none saying the pass ran", logs, want)
			}
		})
	}
}

func TestFixReviewFindingsJoinTheReviewPassesDeduplicatedAndSourced(t *testing.T) {
	const (
		shared = "the retry never bounds its attempts"
		extra  = "the timeout is hard-coded"
	)
	rec, agent, _, _ := fixReviewRun(t, findingsPerCall(shared, ""), findingsPerCall(shared+"\n"+extra+"\n"+extra), nil)
	fix := agent.prompts[1]
	if strings.Count(fix, shared) != 1 || strings.Count(fix, extra) != 1 {
		t.Fatalf("fix prompt = %q, want each finding once", fix)
	}
	if want := []ReviewFinding{
		{Finding: shared, Resolution: "fixed in round 2", Source: "p/r"},
		{Finding: extra, Resolution: "addressed in round 2, not re-checked", Source: "opencode-go/fix-reviewer"},
	}; !slices.Equal(rec.ReviewFindings, want) {
		t.Fatalf("findings = %+v, want %+v", rec.ReviewFindings, want)
	}
}

func TestFixReviewFindingsAreCappedBeforeTheMerge(t *testing.T) {
	lines := make([]string, maxReviewFindings+6)
	for i := range lines {
		lines[i] = fmt.Sprintf("finding %02d", i)
	}
	_, agent, _, logs := fixReviewRun(t, findingsPerCall(""), findingsPerCall(strings.Join(lines, "\n")), nil)
	fix := agent.prompts[1]
	if !strings.Contains(fix, "finding 23") || strings.Contains(fix, "finding 24") {
		t.Fatalf("fix prompt = %q, want the first 24 findings and none after", fix)
	}
	if !strings.Contains(logs, "msg=fixReviewFindingsCut model="+testFixReviewModel+" found=30 kept=24") {
		t.Fatalf("logs = %s\nwant the cut logged", logs)
	}
}

func TestFixReviewFindingAloneRunsAFixRoundAndTheReviewPassDecidesReady(t *testing.T) {
	const finding = "the timeout is hard-coded"
	for name, tc := range map[string]struct {
		reReviews []string
		wantReady bool
		// wantLast is the last row, when the last re-review reports a finding no
		// round was given.
		wantLast *ReviewFinding
	}{
		"a clean re-review": {[]string{""}, true, nil},
		"a re-review's finding": {[]string{"the retry never bounds its attempts", "the lock is never released"}, false,
			&ReviewFinding{Finding: "the lock is never released", Resolution: "still open after round 3", Source: "p/r"}},
	} {
		t.Run(name, func(t *testing.T) {
			rec, agent, script, _ := fixReviewRun(t, findingsPerCall(append([]string{""}, tc.reReviews...)...), findingsPerCall(finding), nil)
			if agent.calls < 2 || !strings.Contains(agent.prompts[1], finding) {
				t.Fatalf("build agent ran %d times, prompts %q; want a fix round given the finding", agent.calls, agent.prompts)
			}
			if got := len(script.prompts[testFixReviewModel]); got != 1 {
				t.Fatalf("fix-review ran %d times, want once", got)
			}
			if len(script.prompts["p/r"]) < 2 {
				t.Fatalf("review pass ran %d times, want a re-review after the round", len(script.prompts["p/r"]))
			}
			if rec.Ready != tc.wantReady {
				t.Fatalf("ready = %v, want %v: the re-review decides", rec.Ready, tc.wantReady)
			}
			if rec.ReviewFindings[0].Source != testFixReviewModel || rec.ReviewFindingsOpen == 0 {
				t.Fatalf("findings = %+v, open %d; want the fix-review's finding sourced and counted open",
					rec.ReviewFindings, rec.ReviewFindingsOpen)
			}
			if last := rec.ReviewFindings[len(rec.ReviewFindings)-1]; tc.wantLast != nil && last != *tc.wantLast {
				t.Fatalf("last finding = %+v, want %+v", last, *tc.wantLast)
			}
		})
	}
}

func TestFixReviewFindingsNeverMakeTheRunReadyOverTheReviewPass(t *testing.T) {
	const finding = "the retry never bounds its attempts"
	rec, _, _, _ := fixReviewRun(t, findingsPerCall(finding), findingsPerCall(""), nil)
	if rec.Ready || !strings.Contains(rec.LoopDetail, finding) {
		t.Fatalf("ready = %v, detail %q; want a draft naming the review pass's finding", rec.Ready, rec.LoopDetail)
	}
}

func TestFixReviewIsSkippedWhenTheReviewStageHasNoStartRoom(t *testing.T) {
	started := time.Now()
	clock := started
	museThenSpend := func(ctx context.Context, call int, stdout, stderr io.Writer) error {
		// Eight minutes is room for a pass on its own, but not once a fix round
		// and a re-check have their start room set aside.
		clock = reviewFloorSpent(started, 8*time.Minute)
		return findingsPerCall("")(ctx, call, stdout, stderr)
	}
	rec, _, script, logs := fixReviewRun(t, museThenSpend, findingsPerCall("the timeout is hard-coded"), func(d *BuildDeps, _ *BuildConfig) {
		d.Now = func() time.Time { return clock }
		d.Checks = func(context.Context, string, []string) (string, string, error) {
			clock = reviewFloorStart(started)
			return "", "", nil
		}
	})
	if n := len(script.prompts[testFixReviewModel]); n != 0 || len(stepModels(rec, PhaseFixReview)) != 0 {
		t.Fatalf("fix-review ran %d times, steps %+v; want it skipped", n, rec.Steps)
	}
	if !strings.Contains(logs, "msg=fixReviewSkipped model="+testFixReviewModel+" reason=budget") {
		t.Fatalf("logs = %s\nwant the skip logged with its reason", logs)
	}
	if !rec.Ready || rec.Outcome != OutcomeBuilt {
		t.Fatalf("ready %v, outcome %s; want the run to go on as the review pass decided", rec.Ready, rec.Outcome)
	}
}

// floorClock starts the review on its floor and returns the clock a test's
// replies move.
func floorClock(started time.Time, clock *time.Time) func(*BuildDeps, *BuildConfig) {
	*clock = started
	return func(d *BuildDeps, _ *BuildConfig) {
		d.Now = func() time.Time { return *clock }
		d.Checks = func(context.Context, string, []string) (string, string, error) {
			*clock = reviewFloorStart(started)
			return "", "", nil
		}
	}
}

func TestFixReviewLeavesAFixRoundAndARecheckTheirStartRoom(t *testing.T) {
	started := time.Now()
	var clock time.Time
	_, _, script, _ := fixReviewRun(t, findingsPerCall(""), findingsPerCall(""), floorClock(started, &clock))
	deadlines := script.deadlines[testFixReviewModel]
	if len(deadlines) != 1 {
		t.Fatalf("fix-review ran %d times, want once", len(deadlines))
	}
	// The 22-minute floor less two 3-minute start rooms.
	if got := time.Until(deadlines[0]); got < 16*time.Minute-2*time.Second || got > 16*time.Minute+2*time.Second {
		t.Fatalf("fix-review deadline is %s away, want the 16 minutes the floor leaves after two start rooms", got)
	}
}

func TestFixReviewFindingsAloneDoNotHoldARunNoRoundCanActOn(t *testing.T) {
	const finding = "the timeout is hard-coded"
	started := time.Now()
	var clock time.Time
	slow := func(ctx context.Context, call int, stdout, stderr io.Writer) error {
		clock = reviewFloorSpent(started, 2*time.Minute)
		return findingsPerCall(finding)(ctx, call, stdout, stderr)
	}
	rec, agent, _, logs := fixReviewRun(t, findingsPerCall(""), slow, floorClock(started, &clock))
	if !rec.Ready || rec.LoopDetail != "" || agent.calls != 1 {
		t.Fatalf("ready %v, detail %q, build calls %d; want ready with no fix round: the review pass was clean",
			rec.Ready, rec.LoopDetail, agent.calls)
	}
	if want := []ReviewFinding{{Finding: finding, Resolution: "not acted on: no fix round had room", Source: testFixReviewModel}}; !slices.Equal(rec.ReviewFindings, want) ||
		rec.ReviewFindingsOpen != 1 {
		t.Fatalf("findings = %+v, open %d; want %+v counted open", rec.ReviewFindings, rec.ReviewFindingsOpen, want)
	}
	if !strings.Contains(logs, "msg=fixReviewFindingsNotActedOn count=1 reason=budget") {
		t.Fatalf("logs = %s\nwant the findings logged as not acted on", logs)
	}
}

func TestFixReviewTimeIsItsOwnPhasesNotTheReviews(t *testing.T) {
	started := time.Now()
	var clock time.Time
	advance := func(by time.Duration) func(context.Context, int, io.Writer, io.Writer) error {
		return func(ctx context.Context, call int, stdout, stderr io.Writer) error {
			clock = clock.Add(by)
			return findingsPerCall("")(ctx, call, stdout, stderr)
		}
	}
	rec, _, _, _ := fixReviewRun(t, advance(time.Minute), advance(4*time.Minute), floorClock(started, &clock))
	if got, want := rec.DurationsMS, (map[string]int64{"fix-review": 240_000, "review": 60_000}); got["fix-review"] != want["fix-review"] ||
		got["review"] != want["review"] {
		t.Fatalf("durations = %v, want fix-review %d ms and review %d ms", got, want["fix-review"], want["review"])
	}
}

func TestAFindingNoRoundCouldActOnKeepsItsSource(t *testing.T) {
	const finding = "the retry never bounds its attempts"
	started := time.Now()
	clock := started
	museThenSpendAll := func(ctx context.Context, call int, stdout, stderr io.Writer) error {
		clock = runCapSpentAt(started)
		return findingsPerCall(finding)(ctx, call, stdout, stderr)
	}
	rec, _, _, logs := fixReviewRun(t, museThenSpendAll, findingsPerCall(""), func(d *BuildDeps, _ *BuildConfig) {
		d.Now = func() time.Time { return clock }
	})
	if want := []ReviewFinding{{Finding: finding, Resolution: "still open", Source: "p/r"}}; rec.Ready || !slices.Equal(rec.ReviewFindings, want) {
		t.Fatalf("ready %v, findings = %+v; want a draft holding %+v", rec.Ready, rec.ReviewFindings, want)
	}
	if !strings.Contains(logs, "msg=fixReviewSkipped") {
		t.Fatalf("logs = %s\nwant the fix-review skipped", logs)
	}
}

func TestFixReviewPassIsItsOwnStepWithItsModelAndUsage(t *testing.T) {
	const haiku = "opencode-go/claude-haiku-5-5"
	// One request under the card's 100K-token tier and one over it; the
	// harness's own cost figure is replaced by the card's.
	usage := func(_ context.Context, _ int, stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, requestEvent(1200, 300, 400, 0, 0)+requestEvent(150_000, 1000, 0, 0, 0)+reviewEvent(""))
		return err
	}
	rec, _, _, _ := fixReviewRun(t, findingsPerCall(""), usage, func(_ *BuildDeps, c *BuildConfig) {
		c.FixReviewModels = []string{haiku}
	})
	var steps []Step
	for _, st := range rec.Steps {
		if st.Phase == PhaseFixReview {
			steps = append(steps, st)
		}
	}
	if len(steps) != 1 || steps[0].Model != haiku || steps[0].Round != 1 {
		t.Fatalf("fix-review steps = %+v, want one on %s round 1", steps, haiku)
	}
	got := steps[0].Tokens
	// (1200*0.10 + 300*0.50 + 400*0.01) + (150000*0.50 + 1000*2.50), per 1M tokens.
	if got.Input != 151_200 || got.Output != 1300 || got.CacheRead != 400 || got.Steps != 2 || !approxEqual(got.Cost, 0.077774) {
		t.Fatalf("fix-review usage = %+v, want 151200/1300/400 tokens over 2 requests costing 0.077774", got)
	}
	if got := stepModels(rec, PhaseReview); !slices.Equal(got, []string{"p/r"}) {
		t.Fatalf("review steps = %v, want only the review pass's", got)
	}
}

func TestBuildWithoutFixReviewModelsRunsNoFixReview(t *testing.T) {
	rec, agent, script, logs := fixReviewRun(t, findingsPerCall(""), findingsPerCall("the timeout is hard-coded"), func(_ *BuildDeps, c *BuildConfig) {
		c.FixReviewModels = nil
	})
	if n := len(script.prompts[testFixReviewModel]); n != 0 || agent.calls != 1 || strings.Contains(logs, "fixReview") {
		t.Fatalf("fix-review ran %d times, build agent %d times, logs %s; want neither", n, agent.calls, logs)
	}
	if got := stepModels(rec, PhaseReview); !rec.Ready || len(rec.Steps) != 2 || !slices.Equal(got, []string{"p/r"}) {
		t.Fatalf("ready %v, steps %+v; want the build and the review pass alone", rec.Ready, rec.Steps)
	}
}

func TestBuildStopsWhenAFixReviewModelIsTheBuilder(t *testing.T) {
	withFallbacks(t, testFallbackModel)
	for name, model := range map[string]string{"the build model": DefaultModel(), "a fallback": testFallbackModel} {
		t.Run(name, func(t *testing.T) {
			agent := &fakeAgent{}
			deps, _, reported := testDeps(validObjects(), agent)
			c := testConfig(t, initRepo(t))
			c.Model = DefaultModel()
			c.FixReviewModels = []string{model}
			if _, err := Build(context.Background(), deps, c); err == nil {
				t.Fatal("Build accepted a fix-review model the builder runs on")
			}
			if agent.calls != 0 || reported.last(t).StopReason != StopModelInvalid {
				t.Fatalf("calls %d, reason %s", agent.calls, reported.last(t).StopReason)
			}
		})
	}
}

func TestValidateFixReviewModels(t *testing.T) {
	for name, tc := range map[string]struct {
		models []string
		want   string
	}{
		"empty":     {nil, ""},
		"malformed": {[]string{"not a model"}, "not provider/model"},
		"repeated":  {[]string{"p/h", "p/h"}, "listed twice"},
		"builder":   {[]string{"p/m"}, "is the build model"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateFixReviewModels(tc.models, "p/m")
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// refusingGate is the build agent with a model gate that refuses one model.
type refusingGate struct {
	*fakeAgent
	refused string
}

func (g refusingGate) Gate(model string) error {
	if model == g.refused {
		return errors.New("no harness is configured for " + model)
	}
	return nil
}

func TestBuildDropsAFixReviewModelTheHarnessRefuses(t *testing.T) {
	rec, _, script, logs := fixReviewRun(t, findingsPerCall(""), findingsPerCall("the timeout is hard-coded"), func(d *BuildDeps, _ *BuildConfig) {
		d.Agent = refusingGate{fakeAgent: d.Agent.(*fakeAgent), refused: testFixReviewModel}
	})
	if rec.Outcome != OutcomeBuilt || !rec.Ready || len(script.prompts[testFixReviewModel]) != 0 {
		t.Fatalf("outcome %s, ready %v, fix-review calls %d; want the run to go on without it",
			rec.Outcome, rec.Ready, len(script.prompts[testFixReviewModel]))
	}
	if !strings.Contains(logs, "msg=fixReviewDropped model="+testFixReviewModel) {
		t.Fatalf("logs = %s\nwant the drop logged", logs)
	}
}
