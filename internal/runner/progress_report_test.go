package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingCreator is an ObjectCreator safe to call from the reporter's
// goroutine; it keeps every body it was handed, fails every create with err
// when err is set, and signals each call on created.
type recordingCreator struct {
	mu      sync.Mutex
	objects map[string][]byte
	names   []string
	err     error
	created chan string
}

func newRecordingCreator(err error) *recordingCreator {
	return &recordingCreator{objects: map[string][]byte{}, err: err, created: make(chan string, 16)}
}

func (c *recordingCreator) CreateObject(_ context.Context, name string, r io.Reader) error {
	b, rerr := io.ReadAll(r)
	c.mu.Lock()
	c.names = append(c.names, name)
	c.objects[name] = b
	c.mu.Unlock()
	c.created <- name
	if rerr != nil {
		return rerr
	}
	return c.err
}

func (c *recordingCreator) object(name string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.objects[name]
}

func (c *recordingCreator) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.names)
}

type fakeDiff struct {
	lines DiffLines
	err   error
}

func (f fakeDiff) DiffStat(context.Context) (DiffLines, error) { return f.lines, f.err }

// manualClock drives a reporter: send on ticks to fire one interval, and at
// is the time each write reads.
type manualClock struct {
	ticks    chan time.Time
	mu       sync.Mutex
	at       time.Time
	interval time.Duration
	stopped  bool
}

func newManualClock(at time.Time) *manualClock {
	return &manualClock{ticks: make(chan time.Time), at: at}
}

func (m *manualClock) now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at
}

func (m *manualClock) set(at time.Time) {
	m.mu.Lock()
	m.at = at
	m.mu.Unlock()
}

func (m *manualClock) ticker(d time.Duration) (<-chan time.Time, func()) {
	m.mu.Lock()
	m.interval = d
	m.mu.Unlock()
	return m.ticks, func() {
		m.mu.Lock()
		m.stopped = true
		m.mu.Unlock()
	}
}

func (m *manualClock) tickerStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}

func testReporter(t *testing.T, objects ObjectCreator, logs io.Writer, clock *manualClock) *ProgressReporter {
	t.Helper()
	r, err := NewProgressReporter(ProgressRun{RunID: "run1", Attempt: 2, Stage: "build"}, "42-1", objects,
		slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.now, r.ticker = clock.now, clock.ticker
	return r
}

func waitCreated(t *testing.T, c *recordingCreator) string {
	t.Helper()
	select {
	case name := <-c.created:
		return name
	case <-time.After(5 * time.Second):
		t.Fatal("no progress object was written")
		return ""
	}
}

func TestProgressObjectNamesTheRunAttemptStageAndSortableTime(t *testing.T) {
	run := ProgressRun{RunID: "AbC_12-x", Attempt: 3, Stage: "plan"}
	if got, want := progressObject(run, time.Unix(1762400000, 0), "987-2"),
		"progress/AbC_12-x-a3-plan/1762400000-987-2.json"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
	if got, want := progressObject(run, time.Unix(5, 0), "987-2"),
		"progress/AbC_12-x-a3-plan/0000000005-987-2.json"; got != want {
		t.Fatalf("name = %q, want %q: the seconds are zero-padded to 10 digits", got, want)
	}
}

func TestNewProgressReporterRejectsANameItCannotPlaceInAnObject(t *testing.T) {
	for _, tc := range []struct {
		name      string
		run       ProgressRun
		attemptID string
	}{
		{"a run id with a slash", ProgressRun{RunID: "a/b", Attempt: 1, Stage: "build"}, "1-1"},
		{"an empty run id", ProgressRun{Attempt: 1, Stage: "build"}, "1-1"},
		{"a negative attempt", ProgressRun{RunID: "r", Attempt: -1, Stage: "build"}, "1-1"},
		{"an unknown stage", ProgressRun{RunID: "r", Attempt: 1, Stage: "review"}, "1-1"},
		{"an attempt id with a dot", ProgressRun{RunID: "r", Attempt: 1, Stage: "plan"}, "../1"},
	} {
		if _, err := NewProgressReporter(tc.run, tc.attemptID, newRecordingCreator(nil), slog.Default()); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}

func TestProgressReporterWritesCountsAtEachTickAndStopsOnReturn(t *testing.T) {
	creator := newRecordingCreator(nil)
	clock := newManualClock(time.Unix(1762400000, 0))
	r := testReporter(t, creator, io.Discard, clock)
	r.setPhase(PhaseBuild)
	r.countStep()
	r.countStep()

	stop := r.Start(context.Background(), fakeDiff{lines: DiffLines{Added: 12, Removed: 4}})
	if clock.interval != progressInterval {
		t.Fatalf("interval = %s, want %s", clock.interval, progressInterval)
	}
	if n := creator.count(); n != 0 {
		t.Fatalf("%d objects written at start, want none before the first tick", n)
	}
	clock.ticks <- time.Time{}
	first := waitCreated(t, creator)
	if first != "progress/run1-a2-build/1762400000-42-1.json" {
		t.Fatalf("first object = %q", first)
	}
	var fields map[string]any
	if err := json.Unmarshal(creator.object(first), &fields); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"lines_added", "lines_removed", "phase", "steps", "written_at"}; !slices.Equal(keys, want) {
		t.Fatalf("payload fields = %q, want exactly %q", keys, want)
	}
	if fields["phase"] != "build" || fields["steps"] != 2.0 || fields["lines_added"] != 12.0 ||
		fields["lines_removed"] != 4.0 || fields["written_at"] != "2025-11-06T03:33:20Z" {
		t.Fatalf("payload = %v", fields)
	}

	r.setPhase(PhaseReview)
	r.countStep()
	clock.set(time.Unix(1762400180, 0))
	clock.ticks <- time.Time{}
	second := waitCreated(t, creator)
	var p progressPayload
	if err := json.Unmarshal(creator.object(second), &p); err != nil {
		t.Fatal(err)
	}
	if second != "progress/run1-a2-build/1762400180-42-1.json" || p.Phase != PhaseReview || p.Steps != 3 {
		t.Fatalf("second object %q = %+v, want the review phase and 3 steps", second, p)
	}

	stop()
	if !clock.tickerStopped() {
		t.Fatal("the ticker still runs after stop returned")
	}
	select {
	case clock.ticks <- time.Time{}:
		t.Fatal("the reporter took a tick after stop returned")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProgressReporterLogsAFailedWriteAndCarriesOn(t *testing.T) {
	creator := newRecordingCreator(errors.New("precondition failed"))
	clock := newManualClock(time.Unix(1762400000, 0))
	var logs syncBuffer
	r := testReporter(t, creator, &logs, clock)
	stop := r.Start(context.Background(), fakeDiff{err: errors.New("git diff: exit status 128")})
	defer stop()

	clock.ticks <- time.Time{}
	waitCreated(t, creator)
	clock.set(time.Unix(1762400180, 0))
	clock.ticks <- time.Time{}
	waitCreated(t, creator)
	stop()

	out := logs.String()
	if strings.Count(out, "msg=progressWriteFailed") != 2 ||
		!strings.Contains(out, "object=progress/run1-a2-build/1762400000-42-1.json") ||
		!strings.Contains(out, `err="precondition failed"`) {
		t.Fatalf("logs = %s, want progressWriteFailed with the object and error as attributes, once per tick", out)
	}
	if !strings.Contains(out, "msg=progressDiffUnread") {
		t.Fatalf("logs = %s, want the unreadable diff logged", out)
	}
}

func TestNilProgressReporterReportsNothing(t *testing.T) {
	var r *ProgressReporter
	r.setPhase(PhaseBuild)
	r.countStep()
	r.Start(context.Background(), nil)()
}

func TestBuildWritesProgressWhileTheAgentRunsAndIgnoresAFailedWrite(t *testing.T) {
	repo := initRepo(t)
	creator := newRecordingCreator(errors.New("permission denied"))
	clock := newManualClock(time.Unix(1762400000, 0))
	var logs syncBuffer
	r := testReporter(t, creator, &logs, clock)
	var name string
	agent := &fakeAgent{
		edit: func(dir string) error {
			select {
			case clock.ticks <- time.Time{}:
			case <-time.After(5 * time.Second):
				return errors.New("no reporter took the tick while the agent ran")
			}
			name = waitCreated(t, creator)
			return os.WriteFile(filepath.Join(dir, "version.go"), []byte("package x\n"), 0o600)
		},
		events: `{"type":"step_finish","part":{"tokens":{"input":1}}}` + "\n",
	}
	deps, _, reported := testDeps(validObjects(), agent)
	deps.Progress = r

	res, err := Build(context.Background(), deps, testConfig(t, repo))
	if err != nil {
		t.Fatalf("Build failed on a failed progress write: %v", err)
	}
	if !res.Changed || reported.last(t).Outcome != OutcomeBuilt {
		t.Fatalf("result = %+v, want the build to complete", res)
	}
	if !clock.tickerStopped() {
		t.Fatal("the reporter still runs after Build returned")
	}
	if !strings.Contains(logs.String(), "msg=progressWriteFailed") {
		t.Fatalf("logs = %s, want the failed write logged", logs.String())
	}
	var p progressPayload
	if err := json.Unmarshal(creator.object(name), &p); err != nil {
		t.Fatal(err)
	}
	if p.Phase != PhaseBuild {
		t.Fatalf("progress phase = %q, want build while the build agent ran", p.Phase)
	}
}

// syncBuffer is a bytes.Buffer the reporter's goroutine and the test may share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestPlanStageWritesPlanProgressWithNoLines(t *testing.T) {
	creator := newRecordingCreator(nil)
	clock := newManualClock(time.Unix(1762400000, 0))
	r, err := NewProgressReporter(ProgressRun{RunID: "run1", Attempt: 1, Stage: "plan"}, "42-1", creator,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.now, r.ticker = clock.now, clock.ticker
	var name string
	agent := &fakeAgent{
		events: planEvent("```plan-files\nversion.go\n```"),
		edit: func(dir string) error {
			if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("a\nb\n"), 0o600); err != nil {
				return err
			}
			select {
			case clock.ticks <- time.Time{}:
			case <-time.After(5 * time.Second):
				return errors.New("no reporter took the tick while the plan agent ran")
			}
			name = waitCreated(t, creator)
			return nil
		},
	}
	deps, c := planStageDeps(t, agent)
	deps.Progress = r

	if _, err := PlanStage(context.Background(), deps, c); err != nil {
		t.Fatal(err)
	}
	var p progressPayload
	if err := json.Unmarshal(creator.object(name), &p); err != nil {
		t.Fatal(err)
	}
	if name != "progress/run1-a1-plan/1762400000-42-1.json" || p.Phase != PhasePlan || p.LinesAdded != 0 || p.LinesRemoved != 0 {
		t.Fatalf("object %q = %+v, want the plan phase and no lines", name, p)
	}
	if !clock.tickerStopped() {
		t.Fatal("the reporter still runs after PlanStage returned")
	}
}
