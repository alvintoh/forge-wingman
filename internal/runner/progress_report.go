package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const progressInterval = 3 * time.Minute

// ProgressRun names the in-flight run a progress object reports on: its run
// row, the attempt the dispatcher claimed it at, and the stage.
type ProgressRun struct {
	RunID   string
	Attempt int
	Stage   string
}

// progressPayload is a progress object's body: counts only, never content.
type progressPayload struct {
	Phase        Phase     `json:"phase"`
	Steps        int64     `json:"steps"`
	LinesAdded   int64     `json:"lines_added"`
	LinesRemoved int64     `json:"lines_removed"`
	WrittenAt    time.Time `json:"written_at"`
}

// diffStater measures a worktree's pending diff.
type diffStater interface {
	DiffStat(ctx context.Context) (DiffLines, error)
}

// ProgressReporter writes a running build's progress to the completions
// bucket on every tick. A nil reporter reports nothing, so every method is safe
// to call on one.
type ProgressReporter struct {
	run       ProgressRun
	attemptID string
	objects   ObjectCreator
	logger    *slog.Logger
	now       func() time.Time
	ticker    func(time.Duration) (<-chan time.Time, func())

	steps atomic.Int64
	mu    sync.Mutex
	phase Phase
	lines DiffLines
}

// NewProgressReporter is the reporter for run, naming each object by the
// workflow run attempt attemptID; it fails on a run whose names are unsafe in
// an object name.
func NewProgressReporter(run ProgressRun, attemptID string, objects ObjectCreator, logger *slog.Logger) (*ProgressReporter, error) {
	switch {
	case !ValidRunID(run.RunID):
		return nil, errors.New("run id is not an object name segment")
	case run.Attempt < 0:
		return nil, errors.New("attempt is negative")
	case run.Stage != string(PhasePlan) && run.Stage != string(PhaseBuild):
		return nil, fmt.Errorf("stage %q is neither plan nor build", run.Stage)
	case !ValidRunID(attemptID):
		return nil, errors.New("attempt id is not an object name segment")
	}
	return &ProgressReporter{run: run, attemptID: attemptID, objects: objects, logger: logger, now: time.Now,
		ticker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}}, nil
}

// progressObject is the name of the progress object a write at time at creates.
func progressObject(run ProgressRun, at time.Time, attemptID string) string {
	return fmt.Sprintf("progress/%s-a%d-%s/%010d-%s.json", run.RunID, run.Attempt, run.Stage, at.Unix(), attemptID)
}

// Start writes progress at every interval until the returned stop is called;
// stop cancels a write in flight and returns once the writer has exited. diff
// is nil for a stage that edits nothing, which reports 0 lines.
func (r *ProgressReporter) Start(ctx context.Context, diff diffStater) (stop func()) {
	if r == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	ticks, stopTicker := r.ticker(progressInterval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stopTicker()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				r.write(ctx, diff)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (r *ProgressReporter) write(ctx context.Context, diff diffStater) {
	if diff != nil {
		lines, err := diff.DiffStat(ctx)
		if err != nil {
			r.logger.Warn("progressDiffUnread", "err", truncate(err.Error(), logErrorLimit))
		} else {
			r.mu.Lock()
			r.lines = lines
			r.mu.Unlock()
		}
	}
	at := r.now()
	r.mu.Lock()
	p := progressPayload{Phase: r.phase, Steps: r.steps.Load(), LinesAdded: r.lines.Added,
		LinesRemoved: r.lines.Removed, WrittenAt: at.UTC()}
	r.mu.Unlock()
	body, err := json.Marshal(p)
	if err != nil {
		r.logger.Warn("progressWriteFailed", "err", truncate(err.Error(), logErrorLimit))
		return
	}
	name := progressObject(r.run, at, r.attemptID)
	wctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	if err := r.objects.CreateObject(wctx, name, bytes.NewReader(body)); err != nil {
		r.logger.Warn("progressWriteFailed", "object", name, "err", truncate(err.Error(), logErrorLimit))
	}
}

func (r *ProgressReporter) setPhase(p Phase) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
}

func (r *ProgressReporter) countStep() {
	if r == nil {
		return
	}
	r.steps.Add(1)
}
