package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestReadProgressRecordsWhatTheAgentDid pins the per-invocation read of the
// agent's own stream: the files it read, deduplicated in first-seen order, the
// largest context a request read, and the requests in a row that changed
// nothing. The request that made the edit is not one of them.
func TestReadProgressRecordsWhatTheAgentDid(t *testing.T) {
	var sum Summary
	stream := strings.NewReader(strings.Join([]string{
		`{"type":"step_finish","part":{"tokens":{"input":10,"cache":{"read":90}},"tools":[{"tool":"read","path":"a.go"}]}}`,
		`{"type":"step_finish","part":{"tokens":{"input":20,"cache":{"read":100}}}}`,
		`{"type":"step_finish","part":{"tokens":{"input":5,"cache":{"read":5}},"tools":[{"tool":"read","path":"a.go"},{"tool":"read","path":"b.go"}]}}`,
		`{"type":"step_finish","part":{"tokens":{"input":1},"tools":[{"tool":"edit","path":"c.go"}]}}`,
		`{"type":"step_finish","part":{"tokens":{"input":1}}}`,
	}, "\n"))

	got := readProgress(stream, &sum)
	if want := []string{"a.go", "b.go"}; !slices.Equal(sum.ReadFiles, want) {
		t.Fatalf("files read = %q, want %q", sum.ReadFiles, want)
	}
	if sum.PeakContextTokens != 120 {
		t.Fatalf("peak context = %d, want the largest request's fresh plus cached input", sum.PeakContextTokens)
	}
	if got.noProgress != 1 {
		t.Fatalf("no progress = %d, want the one request after the edit, the editing request itself excluded", got.noProgress)
	}
	if got.stalled(PhasePlan) || got.stalled(PhaseReview) {
		t.Fatal("a plan or review pass is guarded for no progress, but neither can edit")
	}
	if !(agentProgress{noProgress: DEFAULT_NO_PROGRESS_STEPS}).stalled(PhaseBuild) {
		t.Fatal("a build phase at the limit did not stall")
	}
	if (agentProgress{noProgress: DEFAULT_NO_PROGRESS_STEPS - 1}).stalled(PhaseBuild) {
		t.Fatal("a build phase under the limit stalled")
	}
	stalled := agentProgress{noProgress: 60, peakContext: 480000}
	if want := "no file change in 60 steps (peak context 480000 tokens)"; stalled.stalledDetail() != want {
		t.Fatalf("stalled detail = %q, want %q", stalled.stalledDetail(), want)
	}
}

// TestReadProgressWarnsInsteadOfStoppingOnAnUnreadableStream keeps a stream the
// runner cannot read from costing the run: the summary carries the warning, and
// no phase is reported stalled on evidence nobody could read.
func TestReadProgressWarnsInsteadOfStoppingOnAnUnreadableStream(t *testing.T) {
	var sum Summary
	got := readProgress(errReadSeeker{}, &sum)
	if got.stalled(PhaseBuild) {
		t.Fatal("an unreadable stream stalled the build phase")
	}
	if !strings.Contains(sum.UsageWarning, "boom") {
		t.Fatalf("usage warning = %q, want the read failure named", sum.UsageWarning)
	}
}

// errReadSeeker seeks but cannot be read.
type errReadSeeker struct{}

func (errReadSeeker) Seek(int64, int) (int64, error) { return 0, nil }
func (errReadSeeker) Read([]byte) (int, error)       { return 0, errors.New("boom") }

// stepEvent is a step_finish event whose one call used tool.
func stepEvent(t *testing.T, tool string) event {
	t.Helper()
	var e event
	line := fmt.Sprintf(`{"type":"step_finish","part":{"tools":[{"tool":%q,"path":"a.go"}]}}`, tool)
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// TestObserveCountsEveryFileChangingTool asserts each tool name the harnesses emit for a file change is progress, and bash and reads are not.
func TestObserveCountsEveryFileChangingTool(t *testing.T) {
	for _, tool := range []string{"edit", "write", "patch", "multiedit", "apply_patch", "ast_edit"} {
		p := agentProgress{noProgress: 5}
		p.observe(stepEvent(t, tool))
		if p.noProgress != 0 {
			t.Errorf("%s did not count as progress", tool)
		}
	}
	for _, tool := range []string{"bash", "read", "glob"} {
		p := agentProgress{noProgress: 5}
		p.observe(stepEvent(t, tool))
		if p.noProgress != 6 {
			t.Errorf("%s counted as progress", tool)
		}
	}
}

// TestHarnessStreamCountsItsFileChangingToolsAsProgress runs the harness's own tool names through its translator into the progress scan.
func TestHarnessStreamCountsItsFileChangingToolsAsProgress(t *testing.T) {
	scan := func(t *testing.T, translated string) int {
		t.Helper()
		var sum Summary
		return readProgress(strings.NewReader(translated), &sum).noProgress
	}
	for _, tool := range []string{"edit", "write", "apply_patch", "ast_edit"} {
		stream := strings.Join([]string{
			`{"type":"message_end","message":{"role":"assistant","usage":{"input":1}}}`,
			`{"type":"tool_execution_start","toolCallId":"c1","toolName":"` + tool + `","args":{"path":"a.go"}}`,
			`{"type":"tool_execution_end","toolCallId":"c1","toolName":"` + tool + `","isError":false}`,
			`{"type":"message_end","message":{"role":"assistant","usage":{"input":1}}}`,
		}, "\n")
		var out, errOut strings.Builder
		if err := translateOmpEvents(strings.NewReader(stream), "/repo", &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if got := scan(t, out.String()); got != 1 {
			t.Errorf("omp %s: no-progress = %d, want 1 (only the step after the change)", tool, got)
		}
	}
}

// TestProgressTapStopsOnTheStallingStepAndSurvivesSplitWrites feeds the stream in odd chunks and asserts the stop fires exactly once, at the limit.
func TestProgressTapStopsOnTheStallingStepAndSurvivesSplitWrites(t *testing.T) {
	stops := 0
	tap := newProgressTap(PhaseBuild, func() { stops++ })
	stream := readsOnlyEvents(DEFAULT_NO_PROGRESS_STEPS + 5)
	for i := 0; i < len(stream); i += 7 {
		if _, err := tap.Write([]byte(stream[i:min(i+7, len(stream))])); err != nil {
			t.Fatal(err)
		}
	}
	if stops != 1 || !tap.didStop() {
		t.Fatalf("stops = %d, want exactly one", stops)
	}
}

func TestProgressTapStaysQuietBelowTheLimitAndOutsideBuild(t *testing.T) {
	under := newProgressTap(PhaseBuild, func() { t.Fatal("stopped under the limit") })
	_, _ = under.Write([]byte(readsOnlyEvents(DEFAULT_NO_PROGRESS_STEPS - 1)))
	plan := newProgressTap(PhasePlan, func() { t.Fatal("stopped a plan phase") })
	_, _ = plan.Write([]byte(readsOnlyEvents(2 * DEFAULT_NO_PROGRESS_STEPS)))
}

// TestProgressTapFailsOpenOnAnUnreadableStream asserts junk and an overlong line neither error a write nor stop the run, and a later good line still counts.
func TestProgressTapFailsOpenOnAnUnreadableStream(t *testing.T) {
	old := maxEventLine
	maxEventLine = 32
	t.Cleanup(func() { maxEventLine = old })
	tap := newProgressTap(PhaseBuild, func() { t.Fatal("stopped on unreadable input") })
	for _, chunk := range []string{"not json\n", strings.Repeat("x", 100) + "\n", "{\"type\":\"step_finish\"}"} {
		if n, err := tap.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	tap.finish()
	if tap.p.noProgress != 1 {
		t.Fatalf("no progress = %d, want the one good line counted after the bad ones", tap.p.noProgress)
	}
}

// TestReadProgressRecordsTheStepsPeakContext asserts the round's peak lands on its step and the summary's peak is the largest step's.
func TestReadProgressRecordsTheStepsPeakContext(t *testing.T) {
	sum := Summary{PeakContextTokens: 5, Steps: []Step{{PeakContextTokens: 900}, {}}}
	readProgress(strings.NewReader(`{"type":"step_finish","part":{"context_tokens":300,"tokens":{"input":1}}}`+"\n"), &sum)
	if sum.Steps[1].PeakContextTokens != 300 || sum.Steps[0].PeakContextTokens != 900 {
		t.Fatalf("step peaks = %d, %d, want 900 and 300", sum.Steps[0].PeakContextTokens, sum.Steps[1].PeakContextTokens)
	}
	if sum.PeakContextTokens != 900 {
		t.Fatalf("summary peak = %d, want the largest step's", sum.PeakContextTokens)
	}
}

// TestObserveKeepsDistinctReadsUpToTheCap asserts reads dedupe on append and stop at the cap.
func TestObserveKeepsDistinctReadsUpToTheCap(t *testing.T) {
	var p agentProgress
	for i := range 3 * maxEditedFiles {
		e := stepEvent(t, "read")
		e.Part.Tools[0].Path = fmt.Sprintf("f%d.go", i%(2*maxEditedFiles))
		p.observe(e)
	}
	if len(p.reads) != maxEditedFiles {
		t.Fatalf("reads = %d, want %d", len(p.reads), maxEditedFiles)
	}
	if p.reads[0] != "f0.go" || p.reads[1] != "f1.go" {
		t.Fatalf("reads = %q, want first-seen order", p.reads[:2])
	}
}

// TestProgressTapDoesNotCountAnOverlongStepAsIdle asserts a step too long to read resets the idle count rather than extending it.
func TestProgressTapDoesNotCountAnOverlongStepAsIdle(t *testing.T) {
	old := maxEventLine
	maxEventLine = 200
	t.Cleanup(func() { maxEventLine = old })
	tap := newProgressTap(PhaseBuild, func() {})
	_, _ = tap.Write([]byte(readsOnlyEvents(3)))
	long := `{"type":"step_finish","part":{"tools":[{"tool":"edit","path":"a.go"}],"pad":"` + strings.Repeat("x", 400) + `"}}` + "\n"
	_, _ = tap.Write([]byte(long))
	if tap.p.noProgress != 0 {
		t.Fatalf("no progress = %d, want the overlong step to reset it", tap.p.noProgress)
	}
	_, _ = tap.Write([]byte(strings.Repeat("x", 400) + "\n"))
	_, _ = tap.Write([]byte(readsOnlyEvents(1)))
	if tap.p.noProgress != 1 {
		t.Fatalf("no progress = %d, want a long non-step line left the count alone", tap.p.noProgress)
	}
}
