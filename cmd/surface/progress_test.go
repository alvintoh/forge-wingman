package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// fakeProgressSource answers each read with its progress or error, counting
// the reads and the most it saw in flight at once.
type fakeProgressSource struct {
	progress *store.Progress
	err      error
	// hold, when set, keeps every read waiting until it closes, whatever its
	// context says, as a hung read would.
	hold chan struct{}

	reads    atomic.Int32
	inFlight atomic.Int32
	mu       sync.Mutex
	peak     int32
	// at is the time the last read was given as now.
	at time.Time
}

func (f *fakeProgressSource) Progress(_ context.Context, _ store.Running, now time.Time) (*store.Progress, error) {
	f.reads.Add(1)
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	f.mu.Lock()
	f.peak = max(f.peak, n)
	f.at = now
	f.mu.Unlock()
	if f.hold != nil {
		<-f.hold
	}
	return f.progress, f.err
}

func progressRuns(n int) []store.Running {
	runs := make([]store.Running, 0, n)
	for i := range n {
		runs = append(runs, store.Running{Ticket: "DEMO-1", Repo: "octo/other", RunID: "run-" + string(rune('a'+i)),
			Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(i)})
	}
	return runs
}

func TestProgressFailureServesNullAndIsLogged(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	reader := newCachedProgress(&fakeProgressSource{err: errors.New("bucket unreadable")}, logger)
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: logger, Running: &stubRunning{runs: progressRuns(1)}, Progress: reader})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/running", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"progress":null`) {
		t.Fatalf("status %d body %s, want 200 with a null progress", rec.Code, rec.Body.String())
	}
	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("log %q: %v", logs.String(), err)
	}
	if line["msg"] != "progressNotRead" || line["reason"] != "read-failed" || line["run"] != "run-a" ||
		line["stage"] != "build" || line["err"] != "bucket unreadable" {
		t.Fatalf("logged %v, want progressNotRead naming the run", line)
	}
}

func TestProgressReadsBoundedByOneDeadline(t *testing.T) {
	source := &fakeProgressSource{hold: make(chan struct{})}
	defer close(source.hold)
	var logs bytes.Buffer
	reader := newCachedProgress(source, slog.New(slog.NewJSONHandler(&logs, nil)))
	reader.timeout = 50 * time.Millisecond
	start := time.Now()
	got := reader.Progress(context.Background(), progressRuns(10))
	if took := time.Since(start); took > time.Second {
		t.Fatalf("ten hung reads took %v, want them cut at one deadline", took)
	}
	if len(got) != 0 {
		t.Fatalf("progress = %v, want none for reads that never finished", got)
	}
	source.mu.Lock()
	peak := source.peak
	source.mu.Unlock()
	if peak > progressReads {
		t.Fatalf("%d reads ran at once, want at most %d", peak, progressReads)
	}
	if n := source.reads.Load(); n > progressReads {
		t.Fatalf("%d reads started, want none begun after the deadline", n)
	}
	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("log %q: %v", logs.String(), err)
	}
	if line["msg"] != "progressNotRead" || line["reason"] != "deadline" || line["runs"] != float64(10) {
		t.Fatalf("logged %v, want one progressNotRead for the 10 runs the deadline cut", line)
	}
}

func TestProgressSharesOneReadAmongConcurrentRequests(t *testing.T) {
	source := &fakeProgressSource{progress: &store.Progress{Steps: 3}, hold: make(chan struct{})}
	reader := newCachedProgress(source, discardLogger())
	runs := progressRuns(1)
	var wg sync.WaitGroup
	results := make([]map[progressKey]*store.Progress, 2)
	for i := range results {
		wg.Go(func() { results[i] = reader.Progress(context.Background(), runs) })
	}
	for source.reads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(source.hold)
	wg.Wait()
	if n := source.reads.Load(); n != 1 {
		t.Fatalf("%d reads, want one shared by both requests", n)
	}
	for _, got := range results {
		if p := got[progressKeyOf(runs[0])]; p == nil || p.Steps != 3 {
			t.Fatalf("a request read %v, want the shared progress", got)
		}
	}
}

func TestProgressCachesAReadAMinuteAndAFailureTenSeconds(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		ttl  time.Duration
	}{
		{"a read", nil, progressFreshFor},
		{"a failure", errors.New("bucket unreadable"), progressFailedFor},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeProgressSource{progress: &store.Progress{}, err: tt.err}
			reader := newCachedProgress(source, discardLogger())
			now := claimedAt(0)
			reader.now = func() time.Time { return now }
			runs := progressRuns(1)
			reader.Progress(context.Background(), runs)
			if source.at != now {
				t.Fatalf("the read was given %v as now, want the reader's clock %v", source.at, now)
			}
			now = now.Add(tt.ttl - time.Second)
			reader.Progress(context.Background(), runs)
			if n := source.reads.Load(); n != 1 {
				t.Fatalf("%d reads within the cache window, want 1", n)
			}
			now = now.Add(time.Second)
			reader.Progress(context.Background(), runs)
			if n := source.reads.Load(); n != 2 {
				t.Fatalf("%d reads once the window passed, want 2", n)
			}
		})
	}
}

func TestProgressDropsExpiredEntries(t *testing.T) {
	reader := newCachedProgress(&fakeProgressSource{}, discardLogger())
	now := claimedAt(0)
	reader.now = func() time.Time { return now }
	runs := progressRuns(2)
	reader.Progress(context.Background(), runs[:1])
	now = now.Add(progressFreshFor)
	reader.Progress(context.Background(), runs[1:])
	if n := len(reader.entries); n != 1 {
		t.Fatalf("the cache holds %d entries, want only the unexpired one", n)
	}
}
