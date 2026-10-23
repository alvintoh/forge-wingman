package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubHistory is a RunHistoryReader that answers with the runs it holds.
type stubHistory struct {
	runs  []store.SettledRun
	err   error
	limit int
}

func (s *stubHistory) RecentSettled(_ context.Context, limit int) ([]store.SettledRun, error) {
	s.limit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.runs, nil
}

func historyMux(reader RunHistoryReader) *http.ServeMux {
	return newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), History: reader})
}

// TestRunKindPlacesEveryOutcomeInOneKindWithItsWords asserts each way a run
// settles lands in the kind and words the strip draws and reads.
func TestRunKindPlacesEveryOutcomeInOneKindWithItsWords(t *testing.T) {
	tests := []struct {
		name  string
		run   store.SettledRun
		kind  string
		words string
	}{
		{"merged", store.SettledRun{Merged: true, Outcome: runner.OutcomePROpened}, "merged", "merged"},
		{"merged beats a wait reason", store.SettledRun{Merged: true, Outcome: runner.OutcomePROpened, WaitReason: runner.WaitDraft}, "merged", "merged"},
		{"open PR on an unconfirmed decision", store.SettledRun{Outcome: runner.OutcomePROpened, WaitReason: runner.WaitUnconfirmedDecision}, "decision", "stopped at an unconfirmed decision"},
		{"open draft PR", store.SettledRun{Outcome: runner.OutcomePROpened, WaitReason: runner.WaitDraft}, "draft", "draft PR with open boxes"},
		{"open PR waiting for review", store.SettledRun{Outcome: runner.OutcomePROpened, WaitReason: runner.WaitSizeL}, "pr-open", "PR open, waiting on you"},
		{"open PR with no reason", store.SettledRun{Outcome: runner.OutcomePROpened}, "pr-open", "PR open, waiting on you"},
		{"budget stop", store.SettledRun{Outcome: runner.OutcomeBudgetStop}, "allowance", "stopped, allowance spent"},
		{"allowance exhausted", store.SettledRun{Outcome: runner.OutcomeStopped, StopReason: runner.StopAllowanceExhausted}, "allowance", "stopped, allowance spent"},
		{"credential absent", store.SettledRun{Outcome: runner.OutcomeStopped, StopReason: runner.StopCredentialAbsent}, "credential", "stopped, credential missing"},
		{"owner never answered", store.SettledRun{Outcome: runner.OutcomeStopped, StopReason: runner.StopWaitingOnOwner}, "decision", "stopped at an unconfirmed decision"},
		{"no changes", store.SettledRun{Outcome: runner.OutcomeNoChanges}, "no-changes", "ended with no changes"},
		{"stopped", store.SettledRun{Outcome: runner.OutcomeStopped, StopReason: runner.StopAgentExit}, "failed", "failed"},
		{"agent failed", store.SettledRun{Outcome: runner.OutcomeAgentFailed}, "failed", "failed"},
		{"infra failure", store.SettledRun{Outcome: runner.OutcomeInfraFailure}, "failed", "failed"},
		{"built with no PR", store.SettledRun{Outcome: runner.OutcomeBuilt}, "other", "ended without a PR"},
		{"unknown outcome", store.SettledRun{Outcome: "something-new"}, "other", "ended without a PR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, words := runKind(tt.run)
			if kind != tt.kind || words != tt.words {
				t.Errorf("runKind = %q, %q, want %q, %q", kind, words, tt.kind, tt.words)
			}
		})
	}
}

// TestRunHistoryListsTheRunsOldestFirstWithKindWordsAndTheMergedCount asserts
// the endpoint reverses the store's newest-first rows into the strip's order,
// counts the merged ones from those same rows and asks for twenty.
func TestRunHistoryListsTheRunsOldestFirstWithKindWordsAndTheMergedCount(t *testing.T) {
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	reader := &stubHistory{runs: []store.SettledRun{
		{Run: "run-3", Ticket: "ABC-3", Outcome: runner.OutcomePROpened, Merged: true, SettledAt: at.Add(2 * time.Hour)},
		{Run: "run-2", Ticket: "ABC-2", Outcome: runner.OutcomeBudgetStop, SettledAt: at.Add(time.Hour)},
		{Run: "run-1", Ticket: "ABC-1", Outcome: runner.OutcomePROpened, Merged: true, SettledAt: at},
	}}
	rec := httptest.NewRecorder()
	historyMux(reader).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run-history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if reader.limit != 20 {
		t.Errorf("asked for %d runs, want 20", reader.limit)
	}

	var body struct {
		Runs   []map[string]any `json:"runs"`
		Merged int              `json:"merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Merged != 2 {
		t.Errorf("merged = %d, want 2", body.Merged)
	}
	var order []string
	for _, run := range body.Runs {
		order = append(order, fmt.Sprint(run["run"], ":", run["kind"], ":", run["words"]))
	}
	want := "run-1:merged:merged run-2:allowance:stopped, allowance spent run-3:merged:merged"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("runs = %q, want %q", got, want)
	}
	if body.Runs[0]["ticket"] != "ABC-1" || body.Runs[0]["settled_at"] != "2026-10-20T09:00:00Z" {
		t.Errorf("first run = %v, want its ticket and settlement time", body.Runs[0])
	}
	// Only these keys can reach a reader; ticket text is not among them.
	if len(body.Runs[0]) != 5 {
		t.Errorf("run keys = %v, want run, ticket, kind, words and settled_at", body.Runs[0])
	}
}

func TestRunHistoryRefusesAWriteMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		historyMux(&stubHistory{}).ServeHTTP(rec, httptest.NewRequest(method, "/api/run-history", strings.NewReader(`{}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestRunHistoryWithoutAStoreIsUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	historyMux(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run-history", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestRunHistoryWithNoSettledRunListsNone asserts an empty history answers with
// an empty list and a zero count rather than a null.
func TestRunHistoryWithNoSettledRunListsNone(t *testing.T) {
	rec := httptest.NewRecorder()
	historyMux(&stubHistory{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run-history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "{\"runs\":[],\"merged\":0}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestRunHistoryReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{
		SPA:     fstest.MapFS{},
		Logger:  slog.New(slog.NewTextHandler(&log, nil)),
		History: &stubHistory{err: errors.New("firestore unreachable")},
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run-history", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.Contains(rec.Body.String(), "firestore unreachable") {
		t.Errorf("body leaks the store's error: %q", rec.Body.String())
	}
	if !strings.Contains(log.String(), "runHistoryNotRead") || !strings.Contains(log.String(), "firestore unreachable") {
		t.Errorf("log = %q, want the token and the error", log.String())
	}
}
