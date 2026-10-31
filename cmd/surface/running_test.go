package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
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
		{Ticket: "FRG-3", Repo: "octo/scratch", Stage: "building", ClaimedAt: "2026-10-31T09:10:00Z", Title: "build c"},
		{Ticket: "FRG-2", Repo: "octo/scratch", Stage: "building", ClaimedAt: "2026-10-31T09:20:00Z", Title: "build and plan b"},
		{Ticket: "FRG-1", Repo: "octo/other", Stage: "planning", ClaimedAt: "2026-10-31T09:30:00Z", Title: "plan a"},
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
		{Ticket: "FRG-1", Repo: "octo/other", Stage: "planning", ClaimedAt: "2026-10-31T09:10:00Z", Title: "plan a"},
		{Ticket: "FRG-2", Repo: "octo/scratch", Stage: "building", ClaimedAt: "2026-10-31T09:10:00Z", Title: "build b"},
	}
	if !slices.Equal(got.Runs, want) {
		t.Fatalf("runs = %+v, want %+v", got.Runs, want)
	}
}

func TestRunningServesAnEmptyTitleForARowWithoutOne(t *testing.T) {
	// A reservation claimed before the claim recorded a title has none to serve.
	got, _ := servedRunning(t, &stubRunning{runs: []store.Running{{
		Ticket: "FRG-1", Repo: "octo/other", Stage: dispatcher.StageBuild, ClaimedAt: claimedAt(10),
	}}})
	want := []runningRun{
		{Ticket: "FRG-1", Repo: "octo/other", Stage: "building", ClaimedAt: "2026-10-31T09:10:00Z"},
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
	rec := getRunning(t, &stubRunning{runs: []store.Running{{
		Ticket: "FRG-1", Repo: "octo/other", Stage: dispatcher.StagePlan, ClaimedAt: claimedAt(10), Title: "plan c",
	}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	runs, _ := body["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %v, want the one reservation", body["runs"])
	}
	entry, _ := runs[0].(map[string]any)
	keys := make([]string, 0, len(entry))
	for key := range entry {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if want := []string{"claimed_at", "repo", "stage", "ticket", "title"}; !slices.Equal(keys, want) {
		t.Fatalf("an entry carries %v, want %v", keys, want)
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
