package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// stubRunsPerRepo is a RunsPerRepoStore answering with the cap it holds, and
// recording what it was set to.
type stubRunsPerRepo struct {
	n       int
	setAt   time.Time
	readErr error
	setErr  error
}

func (s *stubRunsPerRepo) Value(context.Context) (int, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	return s.n, nil
}

func (s *stubRunsPerRepo) Set(_ context.Context, n int, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.n, s.setAt = n, at
	return nil
}

// capMux serves the mux with store behind the runs-per-repo endpoint.
func capMux(store RunsPerRepoStore) *http.ServeMux {
	return newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), RepoCap: store})
}

// getRunsPerRepo reads the endpoint and leaves the status to the caller.
func getRunsPerRepo(t *testing.T, store RunsPerRepoStore) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	capMux(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs-per-repo", nil))
	return rec
}

// postRunsPerRepo posts body to the endpoint under contentType, empty for a
// request carrying no content type at all.
func postRunsPerRepo(t *testing.T, mux *http.ServeMux, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runs-per-repo", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// decodeCap reads the cap an answered endpoint carries, failing unless it
// answered 200.
func decodeCap(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got runsPerRepoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.RunsPerRepo
}

func TestRunsPerRepoServesTheSavedCap(t *testing.T) {
	if got := decodeCap(t, getRunsPerRepo(t, &stubRunsPerRepo{n: 8})); got != 8 {
		t.Fatalf("runs per repo = %d, want the saved 8", got)
	}
}

func TestRunsPerRepoServesADefaultedCap(t *testing.T) {
	store := &stubRunsPerRepo{n: dispatcher.DefaultRunsPerRepo}
	if got := decodeCap(t, getRunsPerRepo(t, store)); got != 5 {
		t.Fatalf("runs per repo = %d, want the default 5", got)
	}
}

func TestRunsPerRepoNeedsAStore(t *testing.T) {
	if rec := getRunsPerRepo(t, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestSetRunsPerRepoNeedsAStore(t *testing.T) {
	rec := postRunsPerRepo(t, capMux(nil), "application/json", `{"runs_per_repo": 8}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestRunsPerRepoReportsAReadFailure(t *testing.T) {
	if rec := getRunsPerRepo(t, &stubRunsPerRepo{readErr: errors.New("no document")}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestSetRunsPerRepoSavesTheCap(t *testing.T) {
	store := &stubRunsPerRepo{n: dispatcher.DefaultRunsPerRepo}
	mux := capMux(store)
	for _, n := range []int{dispatcher.MinRunsPerRepo, 8, dispatcher.MaxRunsPerRepo} {
		rec := postRunsPerRepo(t, mux, "application/json", fmt.Sprintf(`{"runs_per_repo": %d}`, n))
		if got := decodeCap(t, rec); got != n || store.n != n {
			t.Fatalf("answered %d, stored %d, want %d", got, store.n, n)
		}
		if store.setAt.IsZero() {
			t.Fatalf("%d was saved without recording when", n)
		}
	}
}

func TestSetRunsPerRepoRefusesAValueOutsideTheRange(t *testing.T) {
	for _, body := range []string{`{"runs_per_repo": 0}`, `{"runs_per_repo": -1}`, `{"runs_per_repo": 21}`} {
		store := &stubRunsPerRepo{n: dispatcher.DefaultRunsPerRepo}
		rec := postRunsPerRepo(t, capMux(store), "application/json", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
		if store.n != dispatcher.DefaultRunsPerRepo || !store.setAt.IsZero() {
			t.Fatalf("%s wrote %d at %v, want nothing written", body, store.n, store.setAt)
		}
	}
}

func TestSetRunsPerRepoRefusesABodyThatNamesNothing(t *testing.T) {
	for _, body := range []string{`{}`, `{"runs_per_repo": "eight"}`, `not json`} {
		if rec := postRunsPerRepo(t, capMux(&stubRunsPerRepo{}), "application/json", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
	}
	rec := postRunsPerRepo(t, capMux(&stubRunsPerRepo{}), "text/plain", `{"runs_per_repo": 8}`)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("a text body = %d, want %d", rec.Code, http.StatusUnsupportedMediaType)
	}
}

func TestSetRunsPerRepoReportsAWriteFailure(t *testing.T) {
	store := &stubRunsPerRepo{setErr: errors.New("the write failed")}
	rec := postRunsPerRepo(t, capMux(store), "application/json", `{"runs_per_repo": 8}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
