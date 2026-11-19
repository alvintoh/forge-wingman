package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// stubPollInterval is the same whole-number setting stub the runs-per-repo
// tests use, answering with the interval it holds.
type stubPollInterval = stubRunsPerRepo

// intervalMux serves the mux with store behind the poll-interval endpoint.
func intervalMux(store PollIntervalStore) *http.ServeMux {
	return newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), PollInterval: store})
}

// getPollInterval reads the endpoint and leaves the status to the caller.
func getPollInterval(t *testing.T, store PollIntervalStore) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	intervalMux(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/poll-interval", nil))
	return rec
}

// postPollInterval posts body to the endpoint under contentType.
func postPollInterval(t *testing.T, mux *http.ServeMux, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/poll-interval", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// decodeInterval reads the interval an answered endpoint carries, failing
// unless it answered 200.
func decodeInterval(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got pollIntervalResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.PollMinutes
}

func TestPollIntervalServesTheSavedInterval(t *testing.T) {
	if got := decodeInterval(t, getPollInterval(t, &stubPollInterval{n: 10})); got != 10 {
		t.Fatalf("poll minutes = %d, want the saved 10", got)
	}
}

func TestPollIntervalNeedsAStore(t *testing.T) {
	if rec := getPollInterval(t, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec := postPollInterval(t, intervalMux(nil), "application/json", `{"poll_minutes": 10}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestPollIntervalReportsAReadFailure(t *testing.T) {
	if rec := getPollInterval(t, &stubPollInterval{readErr: errors.New("no document")}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestSetPollIntervalSavesTenOrFifteen(t *testing.T) {
	store := &stubPollInterval{n: dispatcher.DefaultPollMinutes}
	mux := intervalMux(store)
	for _, n := range []int{10, 15} {
		rec := postPollInterval(t, mux, "application/json", fmt.Sprintf(`{"poll_minutes": %d}`, n))
		if got := decodeInterval(t, rec); got != n || store.n != n || store.setAt.IsZero() {
			t.Fatalf("answered %d, stored %d at %v, want %d recorded with when", got, store.n, store.setAt, n)
		}
	}
}

func TestSetPollIntervalRefusesAnyOtherValueAndWritesNothing(t *testing.T) {
	for _, n := range []int{0, 5, 9, 11, 14, 16, 30} {
		store := &stubPollInterval{n: dispatcher.DefaultPollMinutes}
		rec := postPollInterval(t, intervalMux(store), "application/json", fmt.Sprintf(`{"poll_minutes": %d}`, n))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%d = %d, want %d", n, rec.Code, http.StatusBadRequest)
		}
		if store.n != dispatcher.DefaultPollMinutes || !store.setAt.IsZero() {
			t.Fatalf("%d wrote %d at %v, want nothing written", n, store.n, store.setAt)
		}
	}
}

func TestSetPollIntervalRefusesABodyThatNamesNothing(t *testing.T) {
	for _, body := range []string{`{}`, `{"poll_minutes": "ten"}`, `not json`} {
		store := &stubPollInterval{}
		if rec := postPollInterval(t, intervalMux(store), "application/json", body); rec.Code != http.StatusBadRequest || !store.setAt.IsZero() {
			t.Fatalf("%s = %d, wrote at %v, want %d and nothing written", body, rec.Code, store.setAt, http.StatusBadRequest)
		}
	}
	store := &stubPollInterval{}
	rec := postPollInterval(t, intervalMux(store), "text/plain", `{"poll_minutes": 10}`)
	if rec.Code != http.StatusUnsupportedMediaType || !store.setAt.IsZero() {
		t.Fatalf("a text body = %d, wrote at %v, want %d and nothing written", rec.Code, store.setAt, http.StatusUnsupportedMediaType)
	}
}

func TestSetPollIntervalReportsAWriteFailure(t *testing.T) {
	rec := postPollInterval(t, intervalMux(&stubPollInterval{setErr: errors.New("the write failed")}), "application/json", `{"poll_minutes": 10}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
