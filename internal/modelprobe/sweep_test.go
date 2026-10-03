package modelprobe

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const markedSource = baselineSource + "// TODO: quota unverified\n"

type perModelAgent map[string]fakeAgent

func (p perModelAgent) deps() Deps {
	d := fakeDeps(fakeAgent{})
	d.NewAgent = func(model string) runner.Agent { return p[model] }
	return d
}

func TestRunSweepProbesTheIncumbentFirstAndHoldsOthersToItsToolCalls(t *testing.T) {
	agents := perModelAgent{
		"opencode/a":   {events: strings.Repeat(toolLine, 30), edit: markedSource},
		"opencode/inc": {events: strings.Repeat(toolLine, 10), edit: markedSource},
		"opencode/b":   {events: strings.Repeat(toolLine, 20), edit: markedSource},
		"opencode/c":   {events: strings.Repeat(toolLine, 21), edit: markedSource},
	}
	models := []string{"opencode/a", "opencode/b", "opencode/inc", "opencode/c"}
	sw, err := RunSweep(context.Background(), agents.deps(), "opencode/inc", models, DefaultConfig(), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Result{}
	var order []string
	for _, r := range sw.Results {
		got[r.Model] = r
		order = append(order, r.Model)
	}
	if order[0] != "opencode/inc" {
		t.Fatalf("order = %v, want the incumbent first", order)
	}
	if got["opencode/a"].Reason != ReasonOverToolCallBar || got["opencode/b"].Verdict != VerdictPass || got["opencode/c"].Reason != ReasonOverToolCallBar {
		t.Fatalf("results = %+v, want the bar at twice the incumbent's 10 calls", got)
	}
}

func TestRunSweepStopsOnAnInfrastructureFault(t *testing.T) {
	d := perModelAgent{}.deps()
	d.Materialize = func(context.Context, string) error { return errors.New("no git") }
	if _, err := RunSweep(context.Background(), d, "opencode/a", []string{"opencode/a"}, DefaultConfig(), discardLogger()); err == nil {
		t.Fatal("Sweep succeeded with a fixture that will not build")
	}
}

func TestRunSweepKeepsTheFixtureFreshPerModel(t *testing.T) {
	var dests []string
	d := perModelAgent{"opencode/a": {events: toolLine, edit: markedSource}, "opencode/b": {events: toolLine, edit: markedSource}}.deps()
	inner := d.Materialize
	d.Materialize = func(ctx context.Context, dest string) error {
		dests = append(dests, dest)
		if err := inner(ctx, dest); err != nil {
			return err
		}
		_, err := os.Stat(filepath.Join(dest, collectorPath))
		return err
	}
	if _, err := RunSweep(context.Background(), d, "opencode/a", []string{"opencode/a", "opencode/b"}, DefaultConfig(), discardLogger()); err != nil {
		t.Fatal(err)
	}
	if len(dests) != 2 || dests[0] == dests[1] {
		t.Fatalf("fixture built at %v, want a separate directory per model", dests)
	}
}

func TestSelect(t *testing.T) {
	free := []string{"opencode/a", "opencode/b"}
	if got, err := Select(free, nil); err != nil || !slices.Equal(got, free) {
		t.Fatalf("Select(nil) = %v, %v, want the whole roster", got, err)
	}
	if got, err := Select(free, []string{"opencode/b"}); err != nil || !slices.Equal(got, []string{"opencode/b"}) {
		t.Fatalf("Select(b) = %v, %v", got, err)
	}
	if _, err := Select(free, []string{"opencode/paid"}); err == nil {
		t.Fatal("Select accepted a model that is not free")
	}
	if _, err := Select(free, []string{"opencode-go/a"}); !errors.Is(err, ErrForeignProvider) {
		t.Fatalf("err = %v, want ErrForeignProvider", err)
	}
}

func TestRunSweepReprobesAVoidIncumbentBeforeJudgingIt(t *testing.T) {
	calls := 0
	d := fakeDeps(fakeAgent{})
	d.NewAgent = func(model string) runner.Agent {
		if model != "opencode/inc" {
			return fakeAgent{events: strings.Repeat(toolLine, 5), edit: markedSource}
		}
		calls++
		if calls == 1 {
			return fakeAgent{events: `{"type":"error"}` + "\n"}
		}
		return fakeAgent{events: strings.Repeat(toolLine, 8), edit: markedSource}
	}
	sw, err := RunSweep(context.Background(), d, "opencode/inc", []string{"opencode/inc", "opencode/b"}, DefaultConfig(), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || sw.Results[0].Verdict != VerdictPass || sw.Results[0].ToolCalls != 8 {
		t.Fatalf("calls = %d, first = %+v, want the second probe of the incumbent to stand", calls, sw.Results[0])
	}
}

func TestRunSweepExcludesAnIncumbentThatIsVoidTwice(t *testing.T) {
	calls := 0
	d := fakeDeps(fakeAgent{})
	d.NewAgent = func(string) runner.Agent {
		calls++
		return fakeAgent{events: `{"type":"error"}` + "\n"}
	}
	sw, err := RunSweep(context.Background(), d, "opencode/inc", []string{"opencode/inc"}, DefaultConfig(), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || sw.Results[0].Verdict != VerdictVoid {
		t.Fatalf("calls = %d, result = %+v, want two probes and a void verdict", calls, sw.Results[0])
	}
}

func TestRunSweepStopsAtTheModelCapKeepingTheIncumbent(t *testing.T) {
	agents := perModelAgent{
		"opencode/a": {events: toolLine, edit: markedSource}, "opencode/b": {events: toolLine, edit: markedSource},
		"opencode/inc": {events: toolLine, edit: markedSource},
	}
	cfg := DefaultConfig()
	cfg.MaxModels = 2
	sw, err := RunSweep(context.Background(), agents.deps(), "opencode/inc", []string{"opencode/a", "opencode/b", "opencode/inc"}, cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(sw.Results) != 2 || sw.Results[0].Model != "opencode/inc" || !slices.Equal(sw.Skipped, []string{"opencode/b"}) {
		t.Fatalf("sweep = %+v, want the incumbent and one other probed and one skipped", sw)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestRunSweepMovesAnIncumbentFromAnyPositionToFirst(t *testing.T) {
	agents := perModelAgent{
		"opencode/a": {events: toolLine, edit: markedSource}, "opencode/inc": {events: toolLine, edit: markedSource},
		"opencode/c": {events: toolLine, edit: markedSource},
	}
	sw, err := RunSweep(context.Background(), agents.deps(), "opencode/inc", []string{"opencode/a", "opencode/inc", "opencode/c"}, DefaultConfig(), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range sw.Results {
		order = append(order, r.Model)
	}
	if !slices.Equal(order, []string{"opencode/inc", "opencode/a", "opencode/c"}) {
		t.Fatalf("order = %v, want the incumbent first and the rest in roster order", order)
	}
}

func TestRunSweepHasNoModelCapWhenTheCapIsZeroOrNotReached(t *testing.T) {
	agents := perModelAgent{"opencode/a": {events: toolLine, edit: markedSource}, "opencode/b": {events: toolLine, edit: markedSource}}
	for name, maxModels := range map[string]int{"zero cap": 0, "cap equal to the roster": 2} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxModels = maxModels
			sw, err := RunSweep(context.Background(), agents.deps(), "opencode/a", []string{"opencode/a", "opencode/b"}, cfg, discardLogger())
			if err != nil {
				t.Fatal(err)
			}
			if len(sw.Results) != 2 || sw.Skipped != nil {
				t.Fatalf("sweep = %+v, want both models probed and nothing skipped", sw)
			}
		})
	}
}

func TestRunSweepReprobesOnlyAVoidIncumbent(t *testing.T) {
	fabricated := strings.Replace(baselineSource, "Burst:    0", "Burst:    10", 1)
	for name, tc := range map[string]struct {
		model    string
		agent    fakeAgent
		wantRuns int
	}{
		"void challenger":  {"opencode/b", fakeAgent{events: `{"type":"error"}` + "\n"}, 1},
		"failed incumbent": {"opencode/inc", fakeAgent{events: toolLine, edit: fabricated}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			runs := 0
			d := fakeDeps(fakeAgent{})
			d.NewAgent = func(model string) runner.Agent {
				if model == tc.model {
					runs++
					return tc.agent
				}
				return fakeAgent{events: toolLine, edit: markedSource}
			}
			if _, err := RunSweep(context.Background(), d, "opencode/inc", []string{"opencode/inc", "opencode/b"}, DefaultConfig(), discardLogger()); err != nil {
				t.Fatal(err)
			}
			if runs != tc.wantRuns {
				t.Fatalf("%s probed %d times, want %d", tc.model, runs, tc.wantRuns)
			}
		})
	}
}

func TestRunSweepDoesNotHoldChallengersToAFailedIncumbent(t *testing.T) {
	fabricated := strings.Replace(baselineSource, "Burst:    0", "Burst:    10", 1)
	agents := perModelAgent{
		"opencode/inc": {events: toolLine, edit: fabricated},
		"opencode/b":   {events: strings.Repeat(toolLine, 20), edit: markedSource},
	}
	sw, err := RunSweep(context.Background(), agents.deps(), "opencode/inc", []string{"opencode/inc", "opencode/b"}, DefaultConfig(), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if sw.Results[1].Verdict != VerdictPass {
		t.Fatalf("challenger = %+v, want a pass: a failed incumbent sets no tool-call bar", sw.Results[1])
	}
}

func TestSweepNoEvidenceOnlyWhenEveryRunIsVoid(t *testing.T) {
	void, pass, fail := Result{Verdict: VerdictVoid}, Result{Verdict: VerdictPass}, Result{Verdict: VerdictFail}
	for name, tc := range map[string]struct {
		results []Result
		want    bool
	}{
		"every run void":           {[]Result{void, void}, true},
		"one pass among voids":     {[]Result{void, pass}, false},
		"a fail is still evidence": {[]Result{void, fail}, false},
		"nothing probed":           {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := (Sweep{Results: tc.results}).NoEvidence(); got != tc.want {
				t.Fatalf("NoEvidence = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunSweepKeepsTheBuildVerdictOfAModelRefusedUnderTheShape(t *testing.T) {
	refused := shapeAgent{events: `{"type":"error","error":"403 free models only"}` + "\n", err: errors.New("exit status 1")}
	d := perModelAgent{"opencode/a": {events: toolLine, edit: markedSource}, "opencode/b": {events: toolLine, edit: markedSource}}.deps()
	d.NewShapeAgent = func(model string) runner.Agent {
		if model == "opencode/b" {
			return refused
		}
		return passingShape
	}
	sw, err := RunSweep(context.Background(), d, "opencode/a", []string{"opencode/a", "opencode/b"}, DefaultConfig(), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	a, b := sw.Results[0], sw.Results[1]
	if a.ReviewVerdict != VerdictPass || b.Verdict != VerdictPass || b.ReviewVerdict != VerdictFail || b.ReviewReason != ReasonRefusedFreeTier {
		t.Fatalf("results = %+v, want b's build pass kept beside a refused review run", sw.Results)
	}
	stripped := slices.Clone(sw.Results)
	for i := range stripped {
		stripped[i].ReviewVerdict, stripped[i].ReviewReason = "", ""
	}
	current := runner.ModelSet{Default: "opencode/a"}
	if got, want := Decide(current, sw.Results, "2026-10-01", DefaultConfig()), Decide(current, stripped, "2026-10-01", DefaultConfig()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Decide = %+v, want %+v: the shape verdict must not move it", got, want)
	}
}

func TestRunSweepRunsTheShapeOncePerModelEvenWhenTheIncumbentIsReprobed(t *testing.T) {
	runs := 0
	d := fakeDeps(fakeAgent{})
	d.NewAgent = func(string) runner.Agent { return fakeAgent{events: `{"type":"error"}` + "\n"} }
	d.NewShapeAgent = func(string) runner.Agent { runs++; return passingShape }
	if _, err := RunSweep(context.Background(), d, "opencode/inc", []string{"opencode/inc"}, DefaultConfig(), discardLogger()); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("shape runs = %d, want 1", runs)
	}
}
