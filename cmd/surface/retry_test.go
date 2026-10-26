package main

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubRetries records the retry it is asked for and answers with the result and
// error it holds.
type stubRetries struct {
	result store.RetryResult
	err    error
	calls  []store.RetryRequest
	runs   []string
}

func (s *stubRetries) RetryWithNote(_ context.Context, runID string, req store.RetryRequest, _ time.Time) (store.RetryResult, error) {
	s.calls = append(s.calls, req)
	s.runs = append(s.runs, runID)
	return s.result, s.err
}

// forbiddenMerger fails the test that reaches GitHub through it.
type forbiddenMerger struct{ t *testing.T }

func (f forbiddenMerger) PullState(context.Context, string, int) (dispatcher.PullStatus, error) {
	f.t.Error("the retry read the pull request")
	return dispatcher.PullStatus{}, nil
}

func (f forbiddenMerger) ChecksGreen(context.Context, string, string) (bool, error) {
	f.t.Error("the retry read the checks")
	return false, nil
}

func (f forbiddenMerger) Merge(context.Context, string, int, string) (string, error) {
	f.t.Error("the retry merged")
	return "", nil
}

// openDraft is an open draft PR, the state a retry replaces.
func openDraft() *stubMerger {
	return &stubMerger{state: dispatcher.PullStatus{Open: true, Draft: true, HeadSHA: "abc123"}}
}

const retryBody = `{"note":"Keep the old flag name.","expected_attempt":2}`

// postRetry sends a retry as email, or as a request that skipped the gate when email is nil.
func postRetry(t *testing.T, d Deps, email *string, run, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	d.SPA, d.Logger = fstest.MapFS{}, cmp.Or(d.Logger, discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run+"/retry", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	var h http.Handler = newMux(d)
	if email != nil {
		h = signedInAs(*email, h)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func owner() *string { e := "owner@example.test"; return &e }

func TestRetryRecordsTheSignedInOwnerNeverTheOneTheBodyNames(t *testing.T) {
	retries, runs := &stubRetries{result: store.RetryResult{Attempt: 3}}, mergeRows(nil)
	rec := postRetry(t, Deps{Retries: retries, Runs: runs, Merger: openDraft()}, owner(), "r1", "application/json",
		`{"note":"Keep the old flag name.","expected_attempt":2,"actor":"evil@example.test","supersede_pr":99,"run":"r2"}`)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"queued\",\"attempt\":3}\n" {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
	want := store.RetryRequest{Note: "Keep the old flag name.", Actor: "owner@example.test", ExpectedAttempt: 2, SupersedePR: 7}
	if len(retries.calls) != 1 || retries.calls[0] != want || retries.runs[0] != "r1" {
		t.Fatalf("retried %v of %v, want %+v of r1: the owner is the IAP identity and the PR the row's", retries.calls, retries.runs, want)
	}
}

func TestRetryWithoutAnIdentityWritesNothing(t *testing.T) {
	for name, email := range map[string]*string{"a request that skipped the gate": nil, "an empty email": new(string)} {
		t.Run(name, func(t *testing.T) {
			retries := &stubRetries{}
			rec := postRetry(t, Deps{Retries: retries, Runs: mergeRows(nil), Merger: openDraft()}, email, "r1", "application/json", retryBody)
			if rec.Code != http.StatusUnauthorized || len(retries.calls) != 0 {
				t.Fatalf("answer = %d, calls %v, want 401 and no write", rec.Code, retries.calls)
			}
		})
	}
}

func TestRetryRefusesWhatItCannotAct(t *testing.T) {
	long := strings.Repeat("a", runner.MaxNoteBytes+1)
	for name, tt := range map[string]struct {
		deps        Deps
		contentType string
		body        string
		want        int
	}{
		"no run store":            {Deps{Retries: &stubRetries{}}, "application/json", retryBody, http.StatusServiceUnavailable},
		"no retry store":          {Deps{Runs: mergeRows(nil), Merger: openDraft()}, "application/json", retryBody, http.StatusServiceUnavailable},
		"a body that is not JSON": {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "text/plain", retryBody, http.StatusUnsupportedMediaType},
		"no body":                 {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "application/json", ``, http.StatusBadRequest},
		"no note":                 {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "application/json", `{"expected_attempt":2}`, http.StatusBadRequest},
		"a blank note":            {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "application/json", `{"note":" \n ","expected_attempt":2}`, http.StatusBadRequest},
		"a note over the limit":   {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "application/json", `{"note":"` + long + `","expected_attempt":2}`, http.StatusBadRequest},
		"a body over the cap":     {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "application/json", `{"note":"x","expected_attempt":2,"pad":"` + strings.Repeat("a", maxRetryBodyBytes) + `"}`, http.StatusBadRequest},
		"a note with a NUL":       {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "application/json", `{"note":"a\u0000b","expected_attempt":2}`, http.StatusBadRequest},
		"no attempt":              {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "application/json", `{"note":"x"}`, http.StatusBadRequest},
		"a negative attempt":      {Deps{Retries: &stubRetries{}, Runs: mergeRows(nil)}, "application/json", `{"note":"x","expected_attempt":-1}`, http.StatusBadRequest},
		"a run with no row":       {Deps{Retries: &stubRetries{}, Runs: &stubRuns{rows: map[string]runner.Record{}}}, "application/json", retryBody, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			retries, _ := tt.deps.Retries.(*stubRetries)
			if rec := postRetry(t, tt.deps, owner(), "r1", tt.contentType, tt.body); rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
			if retries != nil && len(retries.calls) != 0 {
				t.Fatalf("retried %v, want nothing written", retries.calls)
			}
		})
	}
}

func TestRetryAnswersEachStoreRefusalWithItsOwnReason(t *testing.T) {
	for name, tt := range map[string]struct {
		err    error
		reason string
	}{
		"a run that moved on":     {store.ErrAttemptMoved, "attempt-moved"},
		"a run still in flight":   {store.ErrRunInFlight, "in-flight"},
		"a run waiting":           {store.ErrRunWaiting, "waiting"},
		"a run already queued":    {store.ErrRunDispatchable, "already-queued"},
		"an abandoned run":        {store.ErrRunAbandoned, "abandoned"},
		"a merged run":            {store.ErrRunMerged, "merged"},
		"a cancelled run":         {store.ErrRunCancelled, "cancelled"},
		"a run that made nothing": {store.ErrRunNoChanges, "no-changes"},
	} {
		t.Run(name, func(t *testing.T) {
			retries := &stubRetries{err: errors.Join(errors.New("retrying run r1"), tt.err)}
			rec := postRetry(t, Deps{Retries: retries, Runs: mergeRows(nil), Merger: openDraft()}, owner(), "r1", "application/json", retryBody)
			if want := `{"reason":"` + tt.reason + `"}` + "\n"; rec.Code != http.StatusConflict || rec.Body.String() != want {
				t.Fatalf("answer = %d %s, want 409 %s", rec.Code, rec.Body.String(), want)
			}
		})
	}
}

func TestRetryAnswersAStoreFailureWithoutNamingIt(t *testing.T) {
	retries := &stubRetries{err: errors.New("rpc error: secret detail")}
	rec := postRetry(t, Deps{Retries: retries, Runs: mergeRows(nil), Merger: openDraft()}, owner(), "r1", "application/json", retryBody)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("answer = %d %s, want a bare 500", rec.Code, rec.Body.String())
	}
	gone := &stubRetries{err: runner.ErrRecordNotFound}
	if rec := postRetry(t, Deps{Retries: gone, Runs: mergeRows(nil), Merger: openDraft()}, owner(), "r1", "application/json", retryBody); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a row that vanished", rec.Code)
	}
}

func TestRetryAnswersARepeatAsAlreadyRetried(t *testing.T) {
	retries := &stubRetries{result: store.RetryResult{Attempt: 3, Already: true}}
	rec := postRetry(t, Deps{Retries: retries, Runs: mergeRows(nil), Merger: openDraft()}, owner(), "r1", "application/json", retryBody)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"already-retried\",\"attempt\":3}\n" {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRetryReadsThePRARowNamesAndReplacesOnlyAnOpenDraft(t *testing.T) {
	for name, tt := range map[string]struct {
		edit        func(*stubMerger)
		runs        func(*runner.Record)
		noMerger    bool
		code        int
		reason      string
		supersedePR int
	}{
		"an open draft":         {nil, nil, false, http.StatusOK, "", 7},
		"a ready PR":            {func(m *stubMerger) { m.state.Draft = false }, nil, false, http.StatusConflict, "pr-ready", 0},
		"a merged PR":           {func(m *stubMerger) { m.state.Merged = true }, nil, false, http.StatusConflict, "pr-merged", 0},
		"a closed PR":           {func(m *stubMerger) { m.state.Open = false }, nil, false, http.StatusConflict, "pr-closed", 0},
		"GitHub unread":         {func(m *stubMerger) { m.stateErr = errors.New("rate limited") }, nil, false, http.StatusBadGateway, "", 0},
		"another repository":    {nil, func(r *runner.Record) { r.PRURL = "https://github.com/octo/other/pull/7" }, false, http.StatusConflict, "no-pull-request", 0},
		"a PR page that is not": {nil, func(r *runner.Record) { r.PRURL = "https://example.test/x" }, false, http.StatusConflict, "no-pull-request", 0},
		"no merge token":        {nil, nil, true, http.StatusServiceUnavailable, "no-merge-token", 0},
	} {
		t.Run(name, func(t *testing.T) {
			merger, retries := openDraft(), &stubRetries{result: store.RetryResult{Attempt: 3}}
			if tt.edit != nil {
				tt.edit(merger)
			}
			d := Deps{Retries: retries, Runs: mergeRows(tt.runs), Merger: merger}
			if tt.noMerger {
				d.Merger = nil
			}
			rec := postRetry(t, d, owner(), "r1", "application/json", retryBody)
			if rec.Code != tt.code || (tt.reason != "" && !strings.Contains(rec.Body.String(), `"`+tt.reason+`"`)) {
				t.Fatalf("answer = %d %s, want %d %s", rec.Code, rec.Body.String(), tt.code, tt.reason)
			}
			if tt.code != http.StatusOK {
				if len(retries.calls) != 0 {
					t.Fatalf("retried %v after a refusal, want nothing written", retries.calls)
				}
				return
			}
			if len(retries.calls) != 1 || retries.calls[0].SupersedePR != tt.supersedePR {
				t.Fatalf("retried %+v, want it to replace PR %d", retries.calls, tt.supersedePR)
			}
		})
	}
}

func TestRetryOfACancelledRecordNeverReadsGitHubAndTakesTheStoresAnswer(t *testing.T) {
	cancelled := func(r *runner.Record) { r.CancelledByOwnerAt = measuredAt() }
	for name, tt := range map[string]struct {
		edit   func(*runner.Record)
		err    error
		reason string
	}{
		"a run with a PR the store finds cancelled": {cancelled, store.ErrRunCancelled, "cancelled"},
		"a run the store finds merged since":        {cancelled, store.ErrRunMerged, "merged"},
		"a stopped run with no PR": {func(r *runner.Record) {
			cancelled(r)
			r.PRURL, r.Outcome, r.StopReason = "", runner.OutcomeAgentFailed, runner.StopAgentExit
		}, store.ErrRunCancelled, "cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			retries := &stubRetries{err: errors.Join(errors.New("retrying run r1"), tt.err)}
			rec := postRetry(t, Deps{Retries: retries, Runs: mergeRows(tt.edit), Merger: forbiddenMerger{t}}, owner(), "r1", "application/json", retryBody)
			if want := `{"reason":"` + tt.reason + `"}` + "\n"; rec.Code != http.StatusConflict || rec.Body.String() != want {
				t.Fatalf("answer = %d %s, want 409 %s", rec.Code, rec.Body.String(), want)
			}
			if len(retries.calls) != 1 || retries.calls[0].SupersedePR != 0 {
				t.Fatalf("retried %+v, want the store asked once and no PR to replace", retries.calls)
			}
		})
	}
}

func TestRetryOfARunWithNoPRNeverReadsGitHub(t *testing.T) {
	retries := &stubRetries{result: store.RetryResult{Attempt: 3}}
	runs := mergeRows(func(r *runner.Record) { r.PRURL = "" })
	rec := postRetry(t, Deps{Retries: retries, Runs: runs, Merger: forbiddenMerger{t}}, owner(), "r1", "application/json", retryBody)
	if rec.Code != http.StatusOK || len(retries.calls) != 1 || retries.calls[0].SupersedePR != 0 {
		t.Fatalf("answer = %d, retried %+v, want a retry that replaces no PR", rec.Code, retries.calls)
	}
	// With no merge token at all, a run with no PR is still retried.
	rec = postRetry(t, Deps{Retries: retries, Runs: runs}, owner(), "r1", "application/json", retryBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want a run with no PR retried without the merge token", rec.Code)
	}
}

// A note of the longest length whose every byte JSON escapes to six bytes is the largest body a retry
// may send, and the cap on the body must let it through.
func TestRetryAcceptsTheLargestNoteEscapedInTheBody(t *testing.T) {
	note := strings.Repeat(`\u0001`, runner.MaxNoteBytes)
	retries := &stubRetries{result: store.RetryResult{Attempt: 3}}
	rec := postRetry(t, Deps{Retries: retries, Runs: mergeRows(nil), Merger: openDraft()}, owner(), "r1", "application/json",
		`{"note":"`+note+`","expected_attempt":2}`)
	if rec.Code != http.StatusOK || len(retries.calls) != 1 || len(retries.calls[0].Note) != runner.MaxNoteBytes {
		t.Fatalf("answer = %d, retried %d notes, want the %d byte note sent on", rec.Code, len(retries.calls), runner.MaxNoteBytes)
	}
}
