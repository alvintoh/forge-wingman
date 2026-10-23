package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// stubRuns holds the run rows a merge reads and records the merge it is told of.
type stubRuns struct {
	rows      map[string]runner.Record
	recordErr error
	repo      string
	repoErr   error
	merges    []string
}

func (s *stubRuns) RunRepository(_ context.Context, id string) (string, error) {
	if _, ok := s.rows[id]; !ok {
		return "", runner.ErrRecordNotFound
	}
	return s.repo, s.repoErr
}

func (s *stubRuns) GetRecord(_ context.Context, id string) (runner.Record, error) {
	rec, ok := s.rows[id]
	if !ok {
		return runner.Record{}, runner.ErrRecordNotFound
	}
	return rec, nil
}

func (s *stubRuns) RecordMerge(_ context.Context, id, sha string, _ time.Time) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.merges = append(s.merges, id+"@"+sha)
	return nil
}

// stubMerger answers with the PR state it holds and records each merge.
type stubMerger struct {
	state    dispatcher.PullStatus
	stateErr error
	green    bool
	greenErr error
	mergeSHA string
	mergeErr error
	merged   []string
}

func (s *stubMerger) PullState(context.Context, string, int) (dispatcher.PullStatus, error) {
	return s.state, s.stateErr
}

func (s *stubMerger) ChecksGreen(context.Context, string, string) (bool, error) {
	return s.green, s.greenErr
}

func (s *stubMerger) Merge(_ context.Context, repo string, number int, sha string) (string, error) {
	if s.mergeErr != nil {
		return "", s.mergeErr
	}
	s.merged = append(s.merged, fmt.Sprintf("%s#%d@%s", repo, number, sha))
	return s.mergeSHA, nil
}

// readyToMerge is an open PR at head abc123 with green checks.
func readyToMerge() *stubMerger {
	return &stubMerger{state: dispatcher.PullStatus{Open: true, HeadSHA: "abc123"}, green: true, mergeSHA: "def456"}
}

func mergeRows(edit func(*runner.Record)) *stubRuns {
	rec := runner.Record{RunID: "r1", PRURL: "https://github.com/octo/scratch/pull/7"}
	if edit != nil {
		edit(&rec)
	}
	return &stubRuns{rows: map[string]runner.Record{"r1": rec}, repo: "octo/scratch"}
}

func postMerge(t *testing.T, d Deps, run, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	d.SPA, d.Logger = fstest.MapFS{}, cmp.Or(d.Logger, discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run+"/merge", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	newMux(d).ServeHTTP(rec, req)
	return rec
}

const mergeBody = `{"expected_head_sha":"abc123"}`

func TestMergeActsOnTheRowsPRWhateverTheBodyNames(t *testing.T) {
	merger, runs := readyToMerge(), mergeRows(nil)
	rec := postMerge(t, Deps{Merger: merger, Runs: runs}, "r1", "application/json",
		`{"expected_head_sha":"abc123","repo":"evil/other","number":99,"sha":"zzz"}`)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"merged\",\"sha\":\"def456\"}\n" {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	if !slices.Equal(merger.merged, []string{"octo/scratch#7@abc123"}) {
		t.Fatalf("merged = %v, want the row's PR at the head the owner saw", merger.merged)
	}
	if !slices.Equal(runs.merges, []string{"r1@def456"}) {
		t.Fatalf("recorded = %v, want the merge commit recorded on the run", runs.merges)
	}
}

func TestMergeRefusesWhatTheOwnerMayNotMerge(t *testing.T) {
	for name, tt := range map[string]struct {
		edit   func(*runner.Record)
		merger func(*stubMerger)
		reason string
	}{
		"a row with no PR page":                     {func(r *runner.Record) { r.PRURL = "" }, nil, "no-pull-request"},
		"a PR in another repository than the run's": {func(r *runner.Record) { r.PRURL = "https://github.com/octo/other/pull/7" }, nil, "no-pull-request"},
		"a run the owner cancelled":                 {func(r *runner.Record) { r.CancelledByOwnerAt = measuredAt() }, nil, "cancelled"},
		"a closed PR":                               {nil, func(m *stubMerger) { m.state.Open = false }, "not-open"},
		"a draft":                                   {nil, func(m *stubMerger) { m.state.Draft = true }, "draft"},
		"a head that moved":                         {nil, func(m *stubMerger) { m.state.HeadSHA = "moved99" }, "head-moved"},
		"an unconfirmed decision":                   {func(r *runner.Record) { r.WaitReason = runner.WaitUnconfirmedDecision }, nil, "unconfirmed-decision"},
		"checks that are not green":                 {nil, func(m *stubMerger) { m.green = false }, "checks-not-green"},
	} {
		t.Run(name, func(t *testing.T) {
			merger, runs := readyToMerge(), mergeRows(tt.edit)
			if tt.merger != nil {
				tt.merger(merger)
			}
			rec := postMerge(t, Deps{Merger: merger, Runs: runs}, "r1", "application/json", mergeBody)
			if rec.Code != http.StatusConflict || rec.Body.String() != fmt.Sprintf("{\"reason\":%q}\n", tt.reason) {
				t.Fatalf("answer = %d %s, want a %s refusal", rec.Code, rec.Body.String(), tt.reason)
			}
			if len(merger.merged) != 0 || len(runs.merges) != 0 {
				t.Fatalf("merged %v, recorded %v, want neither", merger.merged, runs.merges)
			}
		})
	}
}

func TestMergeIsNotHeldByAWaitReasonThatNeverBlocks(t *testing.T) {
	merger := readyToMerge()
	runs := mergeRows(func(r *runner.Record) { r.WaitReason = runner.WaitFrontEnd })
	if rec := postMerge(t, Deps{Merger: merger, Runs: runs}, "r1", "application/json", mergeBody); rec.Code != http.StatusOK || len(merger.merged) != 1 {
		t.Fatalf("answer = %d, merged %v, want a merge", rec.Code, merger.merged)
	}
}

func TestMergeOfAMergedPRSucceedsWithoutMergingAgain(t *testing.T) {
	merger, runs := readyToMerge(), mergeRows(nil)
	merger.state = dispatcher.PullStatus{Merged: true, HeadSHA: "other"}
	rec := postMerge(t, Deps{Merger: merger, Runs: runs}, "r1", "application/json", mergeBody)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"already-merged\"}\n" {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	if len(merger.merged) != 0 || len(runs.merges) != 0 {
		t.Fatalf("merged %v, recorded %v, want neither", merger.merged, runs.merges)
	}
}

func TestMergePassesOnGitHubsOwnReasonForARefusedMerge(t *testing.T) {
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			merger, runs := readyToMerge(), mergeRows(nil)
			merger.mergeErr = fmt.Errorf("merging: %w", &dispatcher.APIError{Status: status, Reason: "Pull Request is not mergeable"})
			rec := postMerge(t, Deps{Merger: merger, Runs: runs}, "r1", "application/json", mergeBody)
			if rec.Code != http.StatusConflict || rec.Body.String() != "{\"reason\":\"github-refused\",\"detail\":\"Pull Request is not mergeable\"}\n" {
				t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
			}
			if len(runs.merges) != 0 {
				t.Fatalf("recorded = %v, want no merge recorded", runs.merges)
			}
		})
	}
}

func TestMergeReportsGitHubCallsThatFailAsBadGateway(t *testing.T) {
	for name, edit := range map[string]func(*stubMerger){
		"reading the pull request":        func(m *stubMerger) { m.stateErr = errors.New("timeout") },
		"reading the checks":              func(m *stubMerger) { m.greenErr = errors.New("timeout") },
		"a refusal other than 405 or 409": func(m *stubMerger) { m.mergeErr = &dispatcher.APIError{Status: http.StatusForbidden} },
	} {
		t.Run(name, func(t *testing.T) {
			var log strings.Builder
			merger, runs := readyToMerge(), mergeRows(nil)
			edit(merger)
			rec := postMerge(t, Deps{Merger: merger, Runs: runs, Logger: slog.New(slog.NewTextHandler(&log, nil))}, "r1", "application/json", mergeBody)
			if rec.Code != http.StatusBadGateway || !strings.Contains(log.String(), "ownerMergeFailed") || len(runs.merges) != 0 {
				t.Fatalf("answer = %d, log = %q, recorded %v", rec.Code, log.String(), runs.merges)
			}
		})
	}
}

func TestMergeStillAnswersMergedWhenTheRecordCannotBeWritten(t *testing.T) {
	var log strings.Builder
	runs := mergeRows(nil)
	runs.recordErr = errors.New("firestore unreachable")
	rec := postMerge(t, Deps{Merger: readyToMerge(), Runs: runs, Logger: slog.New(slog.NewTextHandler(&log, nil))}, "r1", "application/json", mergeBody)
	if rec.Code != http.StatusOK || !strings.Contains(log.String(), "ownerMergeNotRecorded") {
		t.Fatalf("answer = %d, log = %q, want the merge answered and the lost record reported", rec.Code, log.String())
	}
}

func TestMergeRefusesARequestThatIsNotWellFormed(t *testing.T) {
	d := Deps{Merger: readyToMerge(), Runs: mergeRows(nil)}
	for name, tt := range map[string]struct {
		run, contentType, body string
		want                   int
	}{
		"not JSON":                {"r1", "text/plain", mergeBody, http.StatusUnsupportedMediaType},
		"no content type":         {"r1", "", mergeBody, http.StatusUnsupportedMediaType},
		"no head":                 {"r1", "application/json", `{}`, http.StatusBadRequest},
		"a body that is not JSON": {"r1", "application/json", `head`, http.StatusBadRequest},
		"a run that is absent":    {"nope", "application/json", mergeBody, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := postMerge(t, d, tt.run, tt.contentType, tt.body); rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
	if merged := d.Merger.(*stubMerger).merged; len(merged) != 0 {
		t.Fatalf("merged = %v, want none", merged)
	}
}

func TestMergeWithoutATokenOrAStoreIsUnavailable(t *testing.T) {
	for name, d := range map[string]Deps{
		"no merge token": {Runs: mergeRows(nil)},
		"no run store":   {Merger: readyToMerge()},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := postMerge(t, d, "r1", "application/json", mergeBody); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestParsePullURLReadsOnlyAGitHubPullRequestPage(t *testing.T) {
	repo, number, err := parsePullURL("https://github.com/octo/scratch/pull/7")
	if err != nil || repo != "octo/scratch" || number != 7 {
		t.Fatalf("got %q, %d, %v", repo, number, err)
	}
	for _, raw := range []string{"", "http://github.com/octo/scratch/pull/7", "https://evil.example/octo/scratch/pull/7",
		"https://github.com/octo/scratch/issues/7", "https://github.com/octo/scratch/pull/7/files", "https://github.com/octo/scratch/pull/x",
		"https://github.com/octo/scratch/pull/0", "https://github.com/octo/scratch/pull/-3",
		"https://github.com/../scratch/pull/7", "https://github.com/octo/../pull/7", "https://github.com/./scratch/pull/7", "https://github.com/oc%20to/scratch/pull/7"} {
		if _, _, err := parsePullURL(raw); err == nil {
			t.Errorf("parsePullURL(%q) read a PR out of it", raw)
		}
	}
}

func TestMergeRefusalReasonsAreTheStringsThePageSwitchesOn(t *testing.T) {
	for got, want := range map[string]string{
		refusedCancelled:      "cancelled",
		refusedChecksNotGreen: "checks-not-green",
		refusedDraft:          "draft",
		refusedGitHub:         "github-refused",
		refusedHeadMoved:      "head-moved",
		refusedNoPullRequest:  "no-pull-request",
		refusedNotOpen:        "not-open",
		refusedUnconfirmed:    "unconfirmed-decision",
	} {
		if got != want {
			t.Errorf("reason %q, want %q", got, want)
		}
	}
	for refusal, reason := range map[string]runner.WaitReason{
		refusedChecksNotGreen: runner.WaitChecksNotGreen,
		refusedDraft:          runner.WaitDraft,
		refusedUnconfirmed:    runner.WaitUnconfirmedDecision,
	} {
		if refusal != string(reason) {
			t.Errorf("the refusal %q is not the wait reason %q the row shows", refusal, reason)
		}
	}
}

func TestMergeAnswers500WhenTheRunsRepositoryCannotBeRead(t *testing.T) {
	merger, runs := readyToMerge(), mergeRows(nil)
	runs.repoErr = errors.New("firestore unreachable")
	rec := postMerge(t, Deps{Merger: merger, Runs: runs}, "r1", "application/json", mergeBody)
	if rec.Code != http.StatusInternalServerError || len(merger.merged) != 0 {
		t.Fatalf("answer = %d %s, merged %v, want a 500 and no merge", rec.Code, rec.Body.String(), merger.merged)
	}
}

func TestMergeMatchesTheQueuedRepositoryWhateverItsCase(t *testing.T) {
	merger, runs := readyToMerge(), mergeRows(nil)
	runs.repo = "Octo/Scratch"
	if rec := postMerge(t, Deps{Merger: merger, Runs: runs}, "r1", "application/json", mergeBody); rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s, want the merge made", rec.Code, rec.Body.String())
	}
}
