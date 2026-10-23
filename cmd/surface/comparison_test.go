package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// stubComparison is a ComparisonReader that answers with the comparison it
// holds, recording the window it was asked for.
type stubComparison struct {
	cmp      store.Comparison
	err      error
	gotSince time.Time
}

func (s *stubComparison) Comparison(_ context.Context, since time.Time) (store.Comparison, error) {
	s.gotSince = since
	if s.err != nil {
		return store.Comparison{}, s.err
	}
	return s.cmp, nil
}

func TestComparisonReportsEachModelAndProviderAndEveryDriftedRun(t *testing.T) {
	reader := &stubComparison{cmp: store.Comparison{
		Models: []store.ModelComparison{{Model: "command-code/x", Tickets: 4, Passed: 3, CostPerTicket: 150_000,
			MinutesPerTicket: 42.5, RewriteRate: 0.276, RewriteSample: 3}},
		Providers: []store.ProviderComparison{{Provider: "command-code", TrailingCostPerTicket: 125_000, Sample: 6,
			PeakTickets: 2, PeakCost: 400_000, OffPeakTickets: 4, OffPeakCost: 300_000}},
		Drifted: []store.DriftedRun{{RunID: "run-1", Provider: "command-code", Model: "command-code/x",
			Cost: 2 * money.Dollar, CostDrift: "cost $2.0000 is 60% above the $1.2500 average of 6 of this provider's runs"}},
	}}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Comparison: reader})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/comparison", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Models    []map[string]any `json:"models"`
		Providers []map[string]any `json:"providers"`
		Drifted   []map[string]any `json:"drifted_runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 1 || len(body.Providers) != 1 || len(body.Drifted) != 1 {
		t.Fatalf("body = %s, want one model, one provider and one drifted run", rec.Body.String())
	}
	if got, want := slices.Sorted(maps.Keys(body.Models[0])), []string{"cost_per_ticket_usd", "minutes_per_ticket", "model", "passed", "rewrite_rate", "rewrite_sample", "tickets"}; !slices.Equal(got, want) {
		t.Errorf("model keys = %v, want %v", got, want)
	}
	if got, want := body.Models[0]["model"], "command-code/x"; got != want {
		t.Errorf("model = %v, want %v", got, want)
	}
	if got, want := body.Models[0]["cost_per_ticket_usd"], 0.15; got != want {
		t.Errorf("cost per ticket = %v, want %v", got, want)
	}
	if got, want := body.Models[0]["minutes_per_ticket"], 42.5; got != want {
		t.Errorf("minutes per ticket = %v, want %v", got, want)
	}
	if got, want := body.Models[0]["rewrite_rate"], "27.6%"; got != want {
		t.Errorf("rewrite rate = %v, want %v", got, want)
	}
	if got, want := body.Models[0]["rewrite_sample"], 3.0; got != want {
		t.Errorf("rewrite sample = %v, want %v", got, want)
	}
	if got, want := body.Providers[0]["trailing_cost_per_ticket_usd"], 0.125; got != want {
		t.Errorf("trailing cost per ticket = %v, want %v", got, want)
	}
	if got, want := body.Providers[0]["offpeak_cost_usd"], 0.3; got != want {
		t.Errorf("off-peak cost = %v, want %v", got, want)
	}
	if got, want := body.Drifted[0]["cost_usd"], 2.0; got != want {
		t.Errorf("drifted cost = %v, want %v", got, want)
	}
	// The run records this reads hold the ticket's own title and body, and
	// this endpoint serves neither: only the inbox serves a title, for a stopped run.
	for _, banned := range []string{"ticket_title", "ticket_body"} {
		if strings.Contains(rec.Body.String(), banned) {
			t.Fatalf("body carries %s: %s", banned, rec.Body.String())
		}
	}
	if want := time.Now().Add(-runner.TrailingWindow); reader.gotSince.Sub(want).Abs() > time.Minute {
		t.Fatalf("window start = %v, want about %v", reader.gotSince, want)
	}
}

// TestComparisonReportsEachRunsPromptCacheShare asserts the statistics endpoint
// prints, per run, the share of that run's input tokens its provider served from
// cache and each phase's own figures, which the run view lists without asking
// the store again (AC2).
func TestComparisonReportsEachRunsPromptCacheShare(t *testing.T) {
	reader := &stubComparison{cmp: store.Comparison{
		Runs: []store.RunCache{{RunID: "run-1", Provider: "command-code", Model: "command-code/x", CachedShare: 0.75,
			BuildCache: "90% first, 75% phase", PlanCache: "50% first, 25% phase"}},
	}}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Comparison: reader})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/comparison", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 1 {
		t.Fatalf("body = %s, want one run", rec.Body.String())
	}
	if got, want := slices.Sorted(maps.Keys(body.Runs[0])), []string{"build_cache", "cached_share", "model", "plan_cache", "provider", "run_id"}; !slices.Equal(got, want) {
		t.Errorf("run keys = %v, want %v", got, want)
	}
	if got, want := body.Runs[0]["cached_share"], 0.75; got != want {
		t.Errorf("cached share = %v, want %v", got, want)
	}
	if got, want := body.Runs[0]["run_id"], "run-1"; got != want {
		t.Errorf("run id = %v, want %v", got, want)
	}
	if got, want := body.Runs[0]["build_cache"], "90% first, 75% phase"; got != want {
		t.Errorf("build cache = %v, want %v", got, want)
	}
	if got, want := body.Runs[0]["plan_cache"], "50% first, 25% phase"; got != want {
		t.Errorf("plan cache = %v, want %v", got, want)
	}
}

// TestComparisonReportsTheWordForAPhaseThereIsNoShareOf asserts a phase reads as
// none when the run made no request in it and as not recorded for a run recorded
// before the figures were, so the run view never prints 0% for a share nobody
// read (AC2, AC3).
func TestComparisonReportsTheWordForAPhaseThereIsNoShareOf(t *testing.T) {
	reader := &stubComparison{cmp: store.Comparison{Runs: []store.RunCache{
		{RunID: "run-1", Provider: "command-code", Model: "command-code/x", CachedShare: 0.4,
			BuildCache: "90% first, 25% phase", PlanCache: runner.PhaseCacheNone},
		{RunID: "run-2", Provider: "command-code", Model: "command-code/x", CachedShare: 0.5,
			BuildCache: runner.PhaseCacheNotRecorded, PlanCache: runner.PhaseCacheNotRecorded},
	}}}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Comparison: reader})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/comparison", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 2 {
		t.Fatalf("body = %s, want two runs", rec.Body.String())
	}
	if got, want := body.Runs[0]["plan_cache"], runner.PhaseCacheNone; got != want {
		t.Errorf("a phase the run made no request in = %v, want %v", got, want)
	}
	if got, want := body.Runs[1]["build_cache"], runner.PhaseCacheNotRecorded; got != want {
		t.Errorf("a run recorded before the figures were = %v, want %v", got, want)
	}
}

// TestComparisonWithNoSettledRunListsNone asserts a window no run settled in
// answers with an empty list rather than a null, which the run view reads as no
// run to show (AC4).
func TestComparisonWithNoSettledRunListsNone(t *testing.T) {
	mux := newMux(Deps{
		SPA:        fstest.MapFS{},
		Logger:     discardLogger(),
		Comparison: &stubComparison{},
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/comparison", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "{\"models\":[],\"providers\":[],\"runs\":[],\"drifted_runs\":[]}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestComparisonWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/comparison", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestComparisonReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	reader := &stubComparison{err: errors.New("firestore unreachable")}
	mux := newMux(Deps{
		SPA:        fstest.MapFS{},
		Logger:     slog.New(slog.NewTextHandler(&log, nil)),
		Comparison: reader,
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/comparison", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "comparisonNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

func TestComparisonReportsAModelNoMergeMeasuredAsNotMeasured(t *testing.T) {
	got := renderComparison(store.Comparison{
		Models: []store.ModelComparison{{Model: "command-code/x", Tickets: 2, Passed: 2}},
	})
	if len(got.Models) != 1 || got.Models[0].RewriteRate != "not measured" {
		t.Fatalf("models = %+v, want a model with no measured run reported as not measured, not 0.0%%", got.Models)
	}
}
