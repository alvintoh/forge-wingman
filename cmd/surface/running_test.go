package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubRunning is a RunningReader answering with the runs it holds, or the error
// it was given.
type stubRunning struct {
	runs []store.Running
	err  error
}

func (s *stubRunning) Running(context.Context) ([]store.Running, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.runs, nil
}

// claimedAt is when an entry of the running view was claimed: minute past 09:00
// UTC on the day the test reads it.
func claimedAt(minute int) time.Time {
	return time.Date(2026, 10, 31, 9, minute, 0, 0, time.UTC)
}

// getRunning serves the endpoint with reader behind it, leaving the status to
// the caller.
func getRunning(t *testing.T, reader RunningReader) *httptest.ResponseRecorder {
	t.Helper()
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Running: reader})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/running", nil))
	return rec
}

// servedRunning is the endpoint's answer decoded as its wire shape, failing
// unless it answered 200.
func servedRunning(t *testing.T, reader RunningReader) (runningResponse, string) {
	t.Helper()
	rec := getRunning(t, reader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got runningResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got, rec.Body.String()
}

func TestRunningServesTheRunsOldestClaimFirst(t *testing.T) {
	// The ledger holds its reservations in whatever order it holds them, and the
	// tickets' own order is the other way round, so only the claim time can
	// answer this oldest first.
	reader := &stubRunning{runs: []store.Running{
		{Ticket: "FRG-1", Repo: "octo/other", Stage: dispatcher.StagePlan, ClaimedAt: claimedAt(30), Title: "plan a"},
		{Ticket: "FRG-3", Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(10), Title: "build c"},
		{Ticket: "FRG-2", Repo: "octo/scratch", Stage: "", ClaimedAt: claimedAt(20), Title: "build and plan b"},
	}}
	got, _ := servedRunning(t, reader)
	want := []runningRun{
		{Ticket: "FRG-3", Repo: "octo/scratch", Stage: "building", ClaimedAt: "2026-10-31T09:10:00Z", Title: "build c", LimitMinutes: 90},
		{Ticket: "FRG-2", Repo: "octo/scratch", Stage: "building", ClaimedAt: "2026-10-31T09:20:00Z", Title: "build and plan b", LimitMinutes: 90},
		{Ticket: "FRG-1", Repo: "octo/other", Stage: "planning", ClaimedAt: "2026-10-31T09:30:00Z", Title: "plan a", LimitMinutes: 60},
	}
	if !slices.Equal(got.Runs, want) {
		t.Fatalf("runs = %+v, want %+v", got.Runs, want)
	}
}

func TestRunningTiesOnTheTicketWhenTwoClaimsShareATime(t *testing.T) {
	// The ledger can book two claims at one instant: the ticket decides, ahead of
	// the stage that would put them the other way round.
	got, _ := servedRunning(t, &stubRunning{runs: []store.Running{
		{Ticket: "FRG-2", Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(10), Title: "build b"},
		{Ticket: "FRG-1", Repo: "octo/other", Stage: dispatcher.StagePlan, ClaimedAt: claimedAt(10), Title: "plan a"},
	}})
	want := []runningRun{
		{Ticket: "FRG-1", Repo: "octo/other", Stage: "planning", ClaimedAt: "2026-10-31T09:10:00Z", Title: "plan a", LimitMinutes: 60},
		{Ticket: "FRG-2", Repo: "octo/scratch", Stage: "building", ClaimedAt: "2026-10-31T09:10:00Z", Title: "build b", LimitMinutes: 90},
	}
	if !slices.Equal(got.Runs, want) {
		t.Fatalf("runs = %+v, want %+v", got.Runs, want)
	}
}

func TestRunningTiesOnTheStageWhenOneTicketHoldsTwoClaimsAtOneTime(t *testing.T) {
	// The stage is the last key, so it orders the claims however the reader
	// listed them: here the plan comes first and the build sorts ahead of it.
	got, _ := servedRunning(t, &stubRunning{runs: []store.Running{
		{Ticket: "FRG-1", Repo: "octo/scratch", Stage: dispatcher.StagePlan, ClaimedAt: claimedAt(10), Title: "the widget"},
		{Ticket: "FRG-1", Repo: "octo/scratch", Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(10), Title: "the widget"},
	}})
	want := []runningRun{
		{Ticket: "FRG-1", Repo: "octo/scratch", Stage: "building", ClaimedAt: "2026-10-31T09:10:00Z", Title: "the widget", LimitMinutes: 90},
		{Ticket: "FRG-1", Repo: "octo/scratch", Stage: "planning", ClaimedAt: "2026-10-31T09:10:00Z", Title: "the widget", LimitMinutes: 60},
	}
	if !slices.Equal(got.Runs, want) {
		t.Fatalf("runs = %+v, want %+v", got.Runs, want)
	}
}

func TestRunningServesTheClaimTimeInUTC(t *testing.T) {
	// 19:10 at +11:00 is 08:10 UTC, which the view reads with a Z suffix whatever
	// zone the reader's time carries.
	aedt := time.FixedZone("AEDT", 11*60*60)
	got, _ := servedRunning(t, &stubRunning{runs: []store.Running{{
		Ticket: "FRG-1", Repo: "octo/scratch", Stage: dispatcher.StageBuild,
		ClaimedAt: time.Date(2026, 10, 31, 19, 10, 0, 0, aedt), Title: "build a",
	}}})
	want := []runningRun{
		{Ticket: "FRG-1", Repo: "octo/scratch", Stage: "building", ClaimedAt: "2026-10-31T08:10:00Z", Title: "build a", LimitMinutes: 90},
	}
	if !slices.Equal(got.Runs, want) {
		t.Fatalf("runs = %+v, want %+v", got.Runs, want)
	}
}

func TestRunningServesAnEmptyTitleForARowWithoutOne(t *testing.T) {
	// A run row that holds no title serves an empty one.
	got, _ := servedRunning(t, &stubRunning{runs: []store.Running{{
		Ticket: "FRG-1", Repo: "octo/other", Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(10),
	}}})
	want := []runningRun{
		{Ticket: "FRG-1", Repo: "octo/other", Stage: "building", ClaimedAt: "2026-10-31T09:10:00Z", LimitMinutes: 90},
	}
	if !slices.Equal(got.Runs, want) {
		t.Fatalf("runs = %+v, want %+v", got.Runs, want)
	}
}

func TestRunningServesAnEmptyListForAnEmptyLedger(t *testing.T) {
	_, raw := servedRunning(t, &stubRunning{})
	if trimmed := strings.TrimSpace(raw); trimmed != `{"runs":[]}` {
		t.Fatalf("answered %s, want an empty runs list", trimmed)
	}
}

func TestRunningAnswersNoCostOrWriteSet(t *testing.T) {
	rec := getRunning(t, &stubRunning{runs: []store.Running{
		{Ticket: "FRG-1", Repo: "octo/other", Stage: dispatcher.StagePlan, ClaimedAt: claimedAt(10), Title: "plan c"},
		{Ticket: "FRG-2", Repo: "octo/other", Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(20), Title: "build d"},
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	runs, _ := body["runs"].([]any)
	if len(runs) != 2 {
		t.Fatalf("runs = %v, want the two reservations", body["runs"])
	}
	for _, run := range runs {
		entry, _ := run.(map[string]any)
		keys := make([]string, 0, len(entry))
		for key := range entry {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if want := []string{"claimed_at", "limit_minutes", "progress", "repo", "stage", "ticket", "title"}; !slices.Equal(keys, want) {
			t.Fatalf("an entry carries %v, want %v", keys, want)
		}
		if limit, ok := entry["limit_minutes"].(float64); !ok || limit != math.Trunc(limit) {
			t.Fatalf("limit_minutes = %v, want a whole number", entry["limit_minutes"])
		}
	}
}

func TestRunningServesEachRunsOwnLimit(t *testing.T) {
	for _, tt := range []struct {
		name  string
		stage string
		want  int
	}{
		{"a plan-stage reservation", dispatcher.StagePlan, 60},
		{"a build-stage reservation", dispatcher.StageBuild, 90},
		{"a single-stage claim", "", 90},
		{"a stage the ledger does not name", "settled", 90},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := servedRunning(t, &stubRunning{runs: []store.Running{{
				Ticket: "FRG-1", Repo: "octo/other", Stage: tt.stage, ClaimedAt: claimedAt(10),
			}}})
			if len(got.Runs) != 1 || got.Runs[0].LimitMinutes != tt.want {
				t.Fatalf("runs = %+v, want one run limited to %d minutes", got.Runs, tt.want)
			}
		})
	}
}

// TestRunningMatchesTheFixture holds the page's fixture to what the endpoint
// serves: the claims and progress behind the fixture's runs, rendered, give the
// fixture back.
func TestRunningMatchesTheFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "fixtures", "running.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture runningResponse
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	claims := make([]store.Running, 0, len(fixture.Runs))
	reader := stubProgress{}
	for _, run := range fixture.Runs {
		claimed, err := time.Parse(time.RFC3339, run.ClaimedAt)
		if err != nil {
			t.Fatal(err)
		}
		stage := dispatcher.StageBuild
		if run.Stage == "planning" {
			stage = dispatcher.StagePlan
		}
		claim := store.Running{Ticket: run.Ticket, Repo: run.Repo, RunID: "run-" + run.Ticket, Stage: stage, ClaimedAt: claimed, Title: run.Title}
		claims = append(claims, claim)
		if p := run.Progress; p != nil {
			reader[progressKeyOf(claim)] = &store.Progress{Phase: runner.Phase(p.Phase), Steps: p.Steps, Lines: p.Lines,
				Created: now.Add(-time.Duration(p.AgeSeconds) * time.Second)}
		}
	}
	got := renderRunning(claims, reader.Progress(context.Background(), claims), now)
	if !reflect.DeepEqual(got, fixture) {
		t.Fatalf("the claims serve %s, want the fixture's", mustJSON(t, got))
	}
}

func TestRunningNeedsAStore(t *testing.T) {
	if rec := getRunning(t, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestRunningReportsAReadFailure(t *testing.T) {
	if rec := getRunning(t, &stubRunning{err: errors.New("the ledger is unreadable")}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// stubProgress is a ProgressReader answering with the progress it holds.
type stubProgress map[progressKey]*store.Progress

func (s stubProgress) Progress(_ context.Context, runs []store.Running) map[progressKey]*store.Progress {
	out := map[progressKey]*store.Progress{}
	for _, run := range runs {
		if p, ok := s[progressKeyOf(run)]; ok {
			out[progressKeyOf(run)] = p
		}
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRunningServesEachRunsProgressAsOfNow(t *testing.T) {
	now := claimedAt(40)
	build := store.Running{Ticket: "DEMO-1", Repo: "octo/other", RunID: "run-1", Attempt: 2, Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(10)}
	ahead := store.Running{Ticket: "DEMO-2", Repo: "octo/other", RunID: "run-2", Stage: dispatcher.StagePlan, ClaimedAt: claimedAt(20)}
	none := store.Running{Ticket: "DEMO-3", Repo: "octo/other", RunID: "run-3", ClaimedAt: claimedAt(30)}
	reader := stubProgress{
		progressKeyOf(build): {Phase: runner.PhaseBuild, Steps: 5, Lines: 12, Created: now.Add(-95 * time.Second)},
		// A clock ahead of the server's reads as just written, never a negative age.
		progressKeyOf(ahead): {Created: now.Add(time.Minute)},
	}
	runs := []store.Running{build, ahead, none}
	got := renderRunning(runs, reader.Progress(context.Background(), runs), now)
	want := []*runningProgress{
		{Phase: "build", Steps: 5, Lines: 12, AgeSeconds: 95},
		{AgeSeconds: 0},
		nil,
	}
	for i, run := range got.Runs {
		if !reflect.DeepEqual(run.Progress, want[i]) {
			t.Fatalf("run %s progress = %s, want %s", run.Ticket, mustJSON(t, run.Progress), mustJSON(t, want[i]))
		}
	}
}

func TestRunningServesNullProgressWithoutAReader(t *testing.T) {
	_, raw := servedRunning(t, &stubRunning{runs: []store.Running{
		{Ticket: "DEMO-1", Repo: "octo/other", RunID: "run-1", Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(10)},
	}})
	if !strings.Contains(raw, `"progress":null`) {
		t.Fatalf("answered %s, want a null progress", raw)
	}
}
