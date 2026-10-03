package modelprobe

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

type fakeAgent struct {
	events string
	edit   string
	err    error
	block  bool
}

func (f fakeAgent) Run(ctx context.Context, dir, _, _ string, stdout, _ io.Writer) error {
	if f.edit != "" {
		if err := os.WriteFile(filepath.Join(dir, collectorPath), []byte(f.edit), 0o644); err != nil {
			return err
		}
	}
	for _, line := range strings.SplitAfter(f.events, "\n") {
		if _, err := io.WriteString(stdout, line); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

func fakeDeps(a fakeAgent) Deps {
	return Deps{
		NewAgent:      func(string) runner.Agent { return a },
		NewShapeAgent: func(string) runner.Agent { return passingShape },
		Materialize: func(_ context.Context, dest string) error {
			if err := os.MkdirAll(filepath.Join(dest, filepath.Dir(collectorPath)), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dest, collectorPath), []byte(baselineSource), 0o644)
		},
	}
}

const (
	findingsLine = `{"type":"text","part":{"text":"` + "```review-findings\\n```" + `"}}` + "\n"
	toolLine     = `{"type":"tool_use","sessionID":"s"}` + "\n"
	stepLine     = `{"type":"step_finish","part":{"tokens":{"input":100,"output":20},"cost":0.5}}` + "\n"
)

func TestProbeRefusesAModelOutsideTheZenProvider(t *testing.T) {
	for _, m := range []string{"opencode-go/glm-5.3-flash", "big-pickle", "openrouter/x"} {
		_, err := Probe(context.Background(), fakeDeps(fakeAgent{}), m, DefaultConfig())
		if !errors.Is(err, ErrForeignProvider) {
			t.Fatalf("Probe(%s) err = %v, want ErrForeignProvider", m, err)
		}
	}
}

func TestProbeReadsToolCallsUsageAndTheFinalSource(t *testing.T) {
	edited := baselineSource + "// TODO: burst unverified\n"
	obs, err := Probe(context.Background(), fakeDeps(fakeAgent{events: toolLine + toolLine + "not json\n" + stepLine, edit: edited}), "opencode/m", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if obs.ToolCalls != 2 || obs.Files[collectorPath] != edited || obs.Baseline[collectorPath] != baselineSource || obs.Events != 3 || obs.Usage.Input != 100 || obs.Usage.Cost != 0.5 || obs.Errored {
		t.Fatalf("obs = %+v, want 2 tool calls, the edited source and the usage read", obs)
	}
}

func TestProbeStopsARunAtTheToolCallCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxToolCalls = 2
	obs, err := Probe(context.Background(), fakeDeps(fakeAgent{events: strings.Repeat(toolLine, 50), block: true}), "opencode/m", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Capped || obs.ToolCalls != 3 || obs.TimedOut || obs.Errored {
		t.Fatalf("obs = %+v, want the run cut at the first call past the cap", obs)
	}
}

func TestProbeStopsARunAtTheTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TimeoutSeconds = 0
	obs, err := Probe(context.Background(), fakeDeps(fakeAgent{block: true}), "opencode/m", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.TimedOut || obs.Capped || obs.Errored {
		t.Fatalf("obs = %+v, want a timeout", obs)
	}
}

func TestProbeMarksAnErrorEventOrFailedRunAsErrored(t *testing.T) {
	withEvent, err := Probe(context.Background(), fakeDeps(fakeAgent{events: `{"type":"error","error":{"name":"UnknownError"}}` + "\n"}), "opencode/m", DefaultConfig())
	if err != nil || !withEvent.Errored || withEvent.ToolCalls != 0 {
		t.Fatalf("obs = %+v err = %v, want an errored run with no work", withEvent, err)
	}
	failed, err := Probe(context.Background(), fakeDeps(fakeAgent{err: errors.New("exit status 1")}), "opencode/m", DefaultConfig())
	if err != nil || !failed.Errored {
		t.Fatalf("obs = %+v err = %v, want a failed run recorded, not returned", failed, err)
	}
}

func TestProbeReturnsAnInfrastructureFault(t *testing.T) {
	d := fakeDeps(fakeAgent{})
	d.Materialize = func(context.Context, string) error { return errors.New("no git") }
	if _, err := Probe(context.Background(), d, "opencode/m", DefaultConfig()); err == nil {
		t.Fatal("Probe succeeded with a fixture that will not build")
	}
}

func TestProbedRunJudgesEndToEnd(t *testing.T) {
	fabricated := strings.Replace(baselineSource, "Burst:    0", "Burst:    10", 1)
	obs, err := Probe(context.Background(), fakeDeps(fakeAgent{events: toolLine, edit: fabricated}), "opencode/m", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got := Judge("opencode/m", obs, 0, DefaultConfig()); got.Verdict != VerdictFail || got.Reason != ReasonFabricatedBurst {
		t.Fatalf("Judge = %+v, want the fabrication caught from the run's final source", got)
	}
}

func TestProbeSurvivesACollectorThatWasDeleted(t *testing.T) {
	d := fakeDeps(fakeAgent{events: toolLine})
	d.NewAgent = func(string) runner.Agent { return deletingAgent{} }
	obs, err := Probe(context.Background(), d, "opencode/m", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got := Judge("opencode/m", obs, 0, DefaultConfig()); got.Verdict != VerdictFail || got.Reason != ReasonBurstUnreadable {
		t.Fatalf("Judge = %+v, want a failed verdict rather than an aborted probe", got)
	}
}

type deletingAgent struct{}

func (deletingAgent) Run(_ context.Context, dir, _, _ string, stdout, _ io.Writer) error {
	if err := os.Remove(filepath.Join(dir, collectorPath)); err != nil {
		return err
	}
	_, err := io.WriteString(stdout, toolLine)
	return err
}

func TestProbeCountsUnrecognisedOutputAsNoEvents(t *testing.T) {
	obs, err := Probe(context.Background(), fakeDeps(fakeAgent{events: `{"kind":"tool-call"}` + "\n"}), "opencode/m", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Events != 0 || obs.TranscriptBytes == 0 {
		t.Fatalf("obs = %+v, want output that no event was recognised in", obs)
	}
	if got := Judge("opencode/m", obs, 0, DefaultConfig()); got.Verdict != VerdictVoid || got.Reason != ReasonEventShape {
		t.Fatalf("Judge = %+v, want void event-shape-unrecognised", got)
	}
}

func TestProbeReturnsAnErrorWhenTheCallerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Probe(ctx, fakeDeps(fakeAgent{block: true}), "opencode/m", DefaultConfig()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation returned rather than recorded as a run", err)
	}
}

func TestProbeCountsATextEventAsAnEvent(t *testing.T) {
	obs, err := Probe(context.Background(), fakeDeps(fakeAgent{events: `{"type":"text"}` + "\n"}), "opencode/m", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Events != 1 || obs.ToolCalls != 0 {
		t.Fatalf("obs = %+v, want one event and no tool calls", obs)
	}
}

func TestSnapshotReadsOnlyGoSourcesOutsideGit(t *testing.T) {
	dir := t.TempDir()
	for path, body := range map[string]string{
		"internal/collect/collector.go": "package collect",
		"README.md":                     "docs",
		".git/hooks/pre-commit.go":      "package hooks",
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[filepath.Join("internal", "collect", "collector.go")] != "package collect" {
		t.Fatalf("snapshot = %v, want only the Go source outside .git", got)
	}
}

type shapeAgent struct {
	events string
	stderr string
	err    error
	block  bool
}

func (f shapeAgent) Run(ctx context.Context, _, _, _ string, stdout, stderr io.Writer) error {
	for _, line := range strings.SplitAfter(f.events, "\n") {
		if _, err := io.WriteString(stdout, line); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if _, err := io.WriteString(stderr, f.stderr); err != nil {
		return err
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

var passingShape = shapeAgent{events: stepLine + findingsLine}

func shapeDeps(a shapeAgent) Deps {
	d := fakeDeps(fakeAgent{})
	d.NewShapeAgent = func(string) runner.Agent { return a }
	return d
}

func TestProbeShapeReadsStepsAndTheFindingsBlock(t *testing.T) {
	obs, err := ProbeShape(context.Background(), shapeDeps(passingShape), "opencode/m", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Steps != 1 || !obs.HasBlock || obs.Errored || obs.TimedOut || obs.Capped || obs.Unreadable {
		t.Fatalf("obs = %+v, want one step and a findings block", obs)
	}
}

func TestProbeShapeReportsAResponseWithoutAFindingsBlock(t *testing.T) {
	obs, err := ProbeShape(context.Background(), shapeDeps(shapeAgent{events: stepLine + `{"type":"text","part":{"text":"looks fine"}}` + "\n"}), "opencode/m", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if obs.Steps != 1 || obs.HasBlock {
		t.Fatalf("obs = %+v, want a step and no block", obs)
	}
}

func TestProbeShapeCapturesAnErrorEventAndStderr(t *testing.T) {
	obs, err := ProbeShape(context.Background(), shapeDeps(shapeAgent{
		events: `{"type":"error","error":{"data":{"message":"banner-event"}}}` + "\n",
		stderr: "banner-stderr",
		err:    errors.New("exit status 1"),
	}), "opencode/m", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Errored || !strings.Contains(obs.ErrorText, "banner-event") || !strings.Contains(obs.ErrorText, "banner-stderr") {
		t.Fatalf("obs = %+v, want the error event and stderr captured", obs)
	}
}

func TestProbeShapeMarksAFailedRunWithNoErrorEventAsErrored(t *testing.T) {
	obs, err := ProbeShape(context.Background(), shapeDeps(shapeAgent{err: errors.New("exit status 1")}), "opencode/m", DefaultConfig())
	if err != nil || !obs.Errored {
		t.Fatalf("obs = %+v err = %v, want an errored run recorded, not returned", obs, err)
	}
}

func TestProbeShapeAppliesItsOwnCaps(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ShapeMaxToolCalls = 2
	capped, err := ProbeShape(context.Background(), shapeDeps(shapeAgent{events: strings.Repeat(toolLine, 50), block: true}), "opencode/m", cfg)
	if err != nil || !capped.Capped || capped.TimedOut || capped.Errored {
		t.Fatalf("obs = %+v err = %v, want the run cut at the shape cap", capped, err)
	}
	cfg = DefaultConfig()
	cfg.ShapeTimeoutSeconds = 0
	timed, err := ProbeShape(context.Background(), shapeDeps(shapeAgent{block: true}), "opencode/m", cfg)
	if err != nil || !timed.TimedOut || timed.Capped || timed.Errored {
		t.Fatalf("obs = %+v err = %v, want a timeout", timed, err)
	}
}

func TestProbeShapeRefusesAForeignModelAndReturnsCancellation(t *testing.T) {
	if _, err := ProbeShape(context.Background(), shapeDeps(passingShape), "opencode-go/x", DefaultConfig()); !errors.Is(err, ErrForeignProvider) {
		t.Fatalf("err = %v, want ErrForeignProvider", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeShape(ctx, shapeDeps(shapeAgent{block: true}), "opencode/m", DefaultConfig()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation returned", err)
	}
}
