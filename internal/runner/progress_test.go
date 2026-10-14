package runner

import (
	"errors"
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
	if want := "no edit or write in 60 steps (peak context 480000 tokens)"; stalled.stalledDetail() != want {
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
