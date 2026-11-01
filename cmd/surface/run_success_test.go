package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubRunSuccess is a RunSuccessReader that answers with the rate it holds, or
// the error it was given, recording the moment it was asked for.
type stubRunSuccess struct {
	success store.RunSuccess
	err     error
	gotNow  time.Time
}

func (s *stubRunSuccess) RunSuccess(_ context.Context, now time.Time) (store.RunSuccess, error) {
	s.gotNow = now
	if s.err != nil {
		return store.RunSuccess{}, s.err
	}
	return s.success, nil
}

// runSuccessMux serves the endpoint with reader behind it.
func runSuccessMux(reader RunSuccessReader) *http.ServeMux {
	return newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), RunSuccess: reader})
}

// getRunSuccess serves the endpoint and fails unless it answers 200, returning
// the body as the JSON object the view reads.
func getRunSuccess(t *testing.T, reader RunSuccessReader) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	runSuccessMux(reader).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run-success", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// TestRunSuccessServesEachWindowsCountsAndTheCommonestFailure asserts both
// windows carry passed and runs, the rate is served beside them, and the most
// common failure class is served with its count (AC1).
func TestRunSuccessServesEachWindowsCountsAndTheCommonestFailure(t *testing.T) {
	rate := 0.995
	reader := &stubRunSuccess{success: store.RunSuccess{
		Recent:   store.RunWindow{Passed: 199, Runs: 200, Rate: &rate},
		Previous: store.RunWindow{Passed: 3, Runs: 4},
		Failure:  &store.FailureClass{Class: "checks-failing", Count: 1},
	}}
	body := getRunSuccess(t, reader)

	if got, want := slices.Sorted(maps.Keys(body)), []string{"failure", "previous", "recent"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	recent, _ := body["recent"].(map[string]any)
	if got, want := slices.Sorted(maps.Keys(recent)), []string{"passed", "rate", "runs"}; !slices.Equal(got, want) {
		t.Errorf("recent keys = %v, want %v", got, want)
	}
	if recent["passed"] != 199.0 || recent["runs"] != 200.0 || recent["rate"] != 0.995 {
		t.Errorf("recent = %v, want 199 of 200 at 0.995", recent)
	}
	previous, _ := body["previous"].(map[string]any)
	if previous["passed"] != 3.0 || previous["runs"] != 4.0 {
		t.Errorf("previous = %v, want 3 of 4", previous)
	}
	// Under the minimum runs a window states no rate, and the count is still served.
	if served, present := previous["rate"]; !present || served != nil {
		t.Errorf("previous rate = %v (present %t), want null rather than omitted", served, present)
	}
	failure, _ := body["failure"].(map[string]any)
	if failure["class"] != "checks-failing" || failure["count"] != 1.0 {
		t.Errorf("failure = %v, want checks-failing counted once", failure)
	}
	if age := time.Since(reader.gotNow); age.Abs() > time.Minute {
		t.Errorf("read for %v, want about now", reader.gotNow)
	}
}

// TestRunSuccessOmitsTheFailureWhenNoneFailed asserts the failure key is left
// out rather than served empty (AC1).
func TestRunSuccessOmitsTheFailureWhenNoneFailed(t *testing.T) {
	body := getRunSuccess(t, &stubRunSuccess{success: store.RunSuccess{
		Recent:   store.RunWindow{Passed: 20, Runs: 20},
		Previous: store.RunWindow{Passed: 20, Runs: 20},
	}})
	if failure, present := body["failure"]; present {
		t.Errorf("failure = %v, want the key left out when no run failed", failure)
	}
}

func TestRunSuccessAnswers503WithoutAStore(t *testing.T) {
	rec := httptest.NewRecorder()
	runSuccessMux(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run-success", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestRunSuccessAnswers500WhenTheReadFails(t *testing.T) {
	rec := httptest.NewRecorder()
	reader := &stubRunSuccess{err: errors.New("the store is down")}
	runSuccessMux(reader).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run-success", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
}
