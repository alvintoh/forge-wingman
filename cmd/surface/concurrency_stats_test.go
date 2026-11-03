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

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubConcurrency is a ConcurrencyReader answering with the stats it holds, or
// the error it was given, recording the moment it was asked for.
type stubConcurrency struct {
	stats  store.ConcurrencyStats
	err    error
	gotNow time.Time
}

func (s *stubConcurrency) ConcurrencyStats(_ context.Context, now time.Time) (store.ConcurrencyStats, error) {
	s.gotNow = now
	return s.stats, s.err
}

// serveConcurrency serves the endpoint over reader and repoCap, with the
// dispatcher settings env holds.
func serveConcurrency(reader ConcurrencyReader, repoCap RunsPerRepoStore, env map[string]string) *httptest.ResponseRecorder {
	mux := newMux(Deps{
		SPA: fstest.MapFS{}, Logger: discardLogger(), Concurrency: reader, RepoCap: repoCap,
		Getenv: func(key string) string { return env[key] },
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/concurrency", nil))
	return rec
}

// getConcurrency serves the endpoint and fails unless it answers 200, returning
// the body as the JSON object the card reads.
func getConcurrency(t *testing.T, reader ConcurrencyReader, repoCap RunsPerRepoStore, env map[string]string) map[string]any {
	t.Helper()
	rec := serveConcurrency(reader, repoCap, env)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestConcurrencyServesThePeakBesideEachLimitAndTheHeldRunsLargestFirst(t *testing.T) {
	reader := &stubConcurrency{stats: store.ConcurrencyStats{Peak: 2, N: 2, Held: map[string]int{
		dispatcher.HoldConcurrency: 14, dispatcher.HoldReviewWIP: 6, dispatcher.HoldRepoBusy: 3,
		dispatcher.HoldBlocked: 2, dispatcher.HoldLargeCap: 1, dispatcher.HoldBudget: 0,
		dispatcher.HoldProviderDown: 1,
	}}}
	body := getConcurrency(t, reader, &stubRunsPerRepo{n: 5}, map[string]string{"WINGMAN_REVIEW_WIP": "5"})

	want := []string{"held", "peak", "platform_cap", "review_wip", "runs_at_once", "runs_per_repo"}
	if got := slices.Sorted(maps.Keys(body)); !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if body["peak"] != 2.0 || body["runs_at_once"] != 2.0 || body["runs_per_repo"] != 5.0 ||
		body["review_wip"] != 5.0 || body["platform_cap"] != float64(dispatcher.DefaultLimits.PlatformCap) {
		t.Errorf("figures = %v", body)
	}
	held, _ := body["held"].([]any)
	var got []string
	for _, h := range held {
		row, _ := h.(map[string]any)
		got = append(got, row["reason"].(string))
	}
	if !slices.Equal(got, []string{"concurrency", "review-wip", "repo-busy", "blocked", "large-cap", "provider-down"}) {
		t.Errorf("held reasons = %v, want largest first and budget's zero left out", got)
	}
	if first, _ := held[0].(map[string]any); first["count"] != 14.0 {
		t.Errorf("first held = %v, want 14", first)
	}
	if age := time.Since(reader.gotNow); age.Abs() > time.Minute {
		t.Errorf("read for %v, want about now", reader.gotNow)
	}
}

func TestConcurrencyServesAnEmptyHeldListWhenNoRunWasHeld(t *testing.T) {
	body := getConcurrency(t, &stubConcurrency{stats: store.ConcurrencyStats{N: 1}}, &stubRunsPerRepo{n: 5}, nil)
	if held, ok := body["held"].([]any); !ok || len(held) != 0 {
		t.Fatalf("held = %#v, want an empty list rather than null", body["held"])
	}
	if body["review_wip"] != float64(dispatcher.DefaultLimits.ReviewWIP) {
		t.Fatalf("review_wip = %v, want the default while none is set", body["review_wip"])
	}
}

func TestConcurrencyAnswersAnErrorForEachReadThatFails(t *testing.T) {
	ok := &stubConcurrency{}
	for _, tt := range []struct {
		name    string
		reader  ConcurrencyReader
		repoCap RunsPerRepoStore
		env     map[string]string
		want    int
	}{
		{"no run store", nil, &stubRunsPerRepo{n: 5}, nil, http.StatusServiceUnavailable},
		{"no settings store", ok, nil, nil, http.StatusServiceUnavailable},
		{"the stats", &stubConcurrency{err: errors.New("deadline exceeded")}, &stubRunsPerRepo{n: 5}, nil, http.StatusInternalServerError},
		{"the runs per repo cap", ok, &stubRunsPerRepo{readErr: errors.New("no document")}, nil, http.StatusInternalServerError},
		{"a dispatcher setting", ok, &stubRunsPerRepo{n: 5}, map[string]string{"WINGMAN_REVIEW_WIP": "zero"}, http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if rec := serveConcurrency(tt.reader, tt.repoCap, tt.env); rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
