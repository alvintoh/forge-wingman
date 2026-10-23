package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMux(t *testing.T) {
	spa := fstest.MapFS{
		"index.html":    {Data: []byte("<html>shell</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	mux := newMux(Deps{SPA: spa, Logger: discardLogger()})

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantBody string
	}{
		{"health check", "/healthz", http.StatusOK, ""},
		{"built asset", "/assets/app.js", http.StatusOK, "console.log(1)"},
		{"client route falls back to the shell", "/runs/abc", http.StatusOK, "shell"},
		{"unknown api endpoint is not the shell", "/api/nope", http.StatusNotFound, ""},
		{"missing asset is not the shell", "/favicon.ico", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestMuxServesAFontFileAsAFont(t *testing.T) {
	spa := fstest.MapFS{
		"index.html":                  {Data: []byte("<html>shell</html>")},
		"fonts/IBMPlexSans-400.woff2": {Data: []byte("wOF2\x00\x01\x00\x00fontbytes")},
	}
	rec := httptest.NewRecorder()
	newMux(Deps{SPA: spa, Logger: discardLogger()}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/IBMPlexSans-400.woff2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "font/woff2" {
		t.Fatalf("content type = %q, want font/woff2", got)
	}
}

func TestVersion(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "{\"sha\":\"dev\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

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
	// this endpoint is public, so nothing of them may reach it.
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

// stubProviders is a ProviderPlansReader that answers with the plans it holds.
type stubProviders struct {
	plans []store.ProviderPlan
	err   error
}

func (s *stubProviders) Providers(context.Context, time.Time) ([]store.ProviderPlan, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.plans, nil
}

// TestProvidersListsEachPlanInOrderWithPriceUsageAndVerdict asserts the
// providers endpoint lists the plans in the order it is handed them, each with
// its price, its recorded verdict and, where the provider reports one, its
// window usage — the word "unknown" for a plan that reports none (AC1, AC5).
func TestProvidersListsEachPlanInOrderWithPriceUsageAndVerdict(t *testing.T) {
	reader := &stubProviders{plans: []store.ProviderPlan{
		{Provider: "command-code", Price: 10 * money.Dollar, Verdict: providers.VerdictAllowed,
			Windows: []store.WindowUsage{{Name: "command-code-goat-5h", Limit: 14 * money.Dollar, Used: 2 * money.Dollar}}},
		{Provider: "gemini", Price: 0, Verdict: providers.VerdictUnconfirmed},
	}}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), ProviderPlans: reader})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string][]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	plans := body["plans"]
	if len(plans) != 2 {
		t.Fatalf("body = %s, want two plans", rec.Body.String())
	}
	if got, want := plans[0]["provider"], "command-code"; got != want {
		t.Errorf("first provider = %v, want %v", got, want)
	}
	if got, want := plans[1]["provider"], "gemini"; got != want {
		t.Errorf("second provider = %v, want %v", got, want)
	}
	if got, want := plans[0]["price_usd"], 10.0; got != want {
		t.Errorf("command-code price = %v, want %v", got, want)
	}
	if got, want := plans[1]["price_usd"], 0.0; got != want {
		t.Errorf("gemini price = %v, want %v", got, want)
	}
	if got, want := plans[0]["verdict"], string(providers.VerdictAllowed); got != want {
		t.Errorf("command-code verdict = %v, want %v", got, want)
	}
	if got, want := plans[1]["verdict"], string(providers.VerdictUnconfirmed); got != want {
		t.Errorf("gemini verdict = %v, want %v", got, want)
	}
	if got, want := plans[1]["window_usage"], usageUnknown; got != want {
		t.Errorf("gemini window usage = %v, want %v", got, want)
	}
	windows, ok := plans[0]["window_usage"].([]any)
	if !ok || len(windows) != 1 {
		t.Fatalf("command-code window usage = %v, want one window", plans[0]["window_usage"])
	}
	window, ok := windows[0].(map[string]any)
	if !ok {
		t.Fatalf("command-code window = %v, want an object", windows[0])
	}
	if got, want := window["name"], "command-code-goat-5h"; got != want {
		t.Errorf("window name = %v, want %v", got, want)
	}
	if got, want := window["limit_usd"], 14.0; got != want {
		t.Errorf("window limit = %v, want %v", got, want)
	}
	if got, want := window["used_usd"], 2.0; got != want {
		t.Errorf("window used = %v, want %v", got, want)
	}
	// The endpoint serves these keys and no others, so nothing a plan record
	// holds beyond them — a key above all — can reach a reader (AC3).
	if got, want := slices.Sorted(maps.Keys(plans[0])), []string{"price_usd", "provider", "verdict", "window_usage"}; !slices.Equal(got, want) {
		t.Errorf("plan keys = %v, want %v", got, want)
	}
	if got, want := slices.Sorted(maps.Keys(window)), []string{"limit_usd", "name", "used_usd"}; !slices.Equal(got, want) {
		t.Errorf("window keys = %v, want %v", got, want)
	}
	// The name a provider's key secret goes by in configuration is not a plan
	// field, and the endpoint must carry none of it (AC3).
	for _, provider := range []string{"command-code", "gemini"} {
		if secret := providers.KeySecret(provider); secret != "" && strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("body carries %s's key secret name: %s", provider, rec.Body.String())
		}
	}
}

// TestProvidersRefusesAWriteMethod asserts the providers endpoint refuses every
// method that could write a plan record: the surface has no such route, and the
// owner CLI alone writes them (AC4, adr/0014).
func TestProvidersRefusesAWriteMethod(t *testing.T) {
	mux := newMux(Deps{
		SPA:           fstest.MapFS{},
		Logger:        discardLogger(),
		ProviderPlans: &stubProviders{},
	})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/providers", strings.NewReader(`{"provider":"command-code"}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestProvidersWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestProvidersWithNoPlansListsNone asserts an owner with no plan recorded
// answers with an empty list rather than a null (AC5).
func TestProvidersWithNoPlansListsNone(t *testing.T) {
	mux := newMux(Deps{
		SPA:           fstest.MapFS{},
		Logger:        discardLogger(),
		ProviderPlans: &stubProviders{},
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "{\"plans\":[]}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestProvidersReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	reader := &stubProviders{err: errors.New("firestore unreachable")}
	mux := newMux(Deps{
		SPA:           fstest.MapFS{},
		Logger:        slog.New(slog.NewTextHandler(&log, nil)),
		ProviderPlans: reader,
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "providersNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

// stubMeasured is a MeasuredReader that answers with the merges it holds, or the
// error it was given.
type stubMeasured struct {
	merges []dispatcher.Sample
	err    error
	// limit is the cap the last read asked for.
	limit int
}

func (s *stubMeasured) Recent(_ context.Context, limit int) ([]dispatcher.Sample, error) {
	s.limit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.merges, nil
}

// TestAutonomyRefusesAWriteMethod asserts the autonomy endpoint refuses every
// method that could set anything: the surface has no such route, and the switch
// is set through its own endpoint (AC5).
func TestAutonomyRefusesAWriteMethod(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Measured: &stubMeasured{}})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/autonomy", strings.NewReader(`{}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

// TestAutonomyWithoutAStoreIsUnavailable asserts a local run with no store
// answers 503 rather than figures nobody could read.
func TestAutonomyWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestAutonomyReportsAStoreItCannotRead asserts a failed read is a 500 with the
// failure reported, not figures nobody could read.
func TestAutonomyReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	reader := &stubMeasured{err: errors.New("firestore unreachable")}
	mux := newMux(Deps{
		SPA:      fstest.MapFS{},
		Logger:   slog.New(slog.NewTextHandler(&log, nil)),
		Measured: reader,
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "autonomyNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

func getAutonomy(t *testing.T, reader *stubMeasured) *httptest.ResponseRecorder {
	t.Helper()
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Measured: reader})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	return rec
}

// TestAutonomyAsksForTheMeasuredCap asserts the endpoint reads the measured
// merges under the cap the figures are drawn from, never the whole collection.
func TestAutonomyAsksForTheMeasuredCap(t *testing.T) {
	reader := &stubMeasured{}
	getAutonomy(t, reader)
	if reader.limit != measuredShown {
		t.Errorf("limit asked = %d, want %d", reader.limit, measuredShown)
	}
}

// seriesOf is the rolling series the autonomy endpoint served.
func seriesOf(t *testing.T, rec *httptest.ResponseRecorder) []any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	series, ok := rewrite["series"].([]any)
	if !ok {
		t.Fatalf("series = %v, want a list", rewrite["series"])
	}
	return series
}

// TestAutonomyReportsTheRewriteFiguresThePageDraws asserts the endpoint serves
// the tiles, the PRs the rate is measured on and the same figures split by size
// and by repository, every rate pooled over the lines rather than a mean of the
// PRs' own rates (AC1, AC3, AC4, AC5, AC6).
func TestAutonomyReportsTheRewriteFiguresThePageDraws(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	reader := &stubMeasured{merges: []dispatcher.Sample{
		{Run: "run-2", Repo: "octo/scratch", Size: "M", PR: 143, MergedAt: at,
			PRURL:   "https://github.com/octo/scratch/pull/143",
			Title:   "feat(runner): FRG-143 add the widget",
			Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 50}},
		{Run: "run-1", Repo: "octo/other", Size: "S", PR: 142, MergedAt: at.Add(-time.Hour),
			Rewrite: dispatcher.Rewrite{AgentLines: 10, OwnerLines: 10}},
		{Run: "run-0", PR: 141, MergedAt: at.Add(-2 * time.Hour),
			Rewrite: dispatcher.Rewrite{OwnerLines: 4}},
	}}
	rec := getAutonomy(t, reader)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	if got, want := slices.Sorted(maps.Keys(rewrite)), []string{"by_repo", "by_size", "prs", "series", "tiles"}; !slices.Equal(got, want) {
		t.Errorf("rewrite keys = %v, want %v", got, want)
	}

	tiles, ok := rewrite["tiles"].([]any)
	if !ok || len(tiles) != 3 {
		t.Fatalf("tiles = %v, want the trailing, five and one windows", rewrite["tiles"])
	}
	trailing, ok := tiles[0].(map[string]any)
	if !ok {
		t.Fatalf("trailing tile = %v, want an object", tiles[0])
	}
	if got, want := slices.Sorted(maps.Keys(trailing)), []string{"agent_lines", "asked", "owner_lines", "prs", "rate"}; !slices.Equal(got, want) {
		t.Errorf("tile keys = %v, want %v", got, want)
	}
	// The three PRs' own rates are 50%, 100% and +Inf, which a mean of them
	// would not read as the 64 owner lines over the 110 agent lines (AC6).
	if got, want := trailing["rate"], "58.2%"; got != want {
		t.Errorf("trailing rate = %v, want %v", got, want)
	}
	if got, want := trailing["asked"], 10.0; got != want {
		t.Errorf("trailing asked = %v, want %v", got, want)
	}
	if got, want := trailing["agent_lines"], 110.0; got != want {
		t.Errorf("trailing agent lines = %v, want %v", got, want)
	}
	if got, want := trailing["owner_lines"], 64.0; got != want {
		t.Errorf("trailing owner lines = %v, want %v", got, want)
	}
	if prs, ok := trailing["prs"].([]any); !ok || len(prs) != 3 || prs[0] != 143.0 || prs[2] != 141.0 {
		t.Errorf("trailing prs = %v, want the three PRs newest first", trailing["prs"])
	}
	// The one-PR window holds the newest merge and says it asked for one.
	last, ok := tiles[2].(map[string]any)
	if !ok || last["asked"] != 1.0 || last["rate"] != "50.0%" {
		t.Errorf("one-PR tile = %v, want the newest merge's own window", tiles[2])
	}
	if prs, ok := last["prs"].([]any); !ok || len(prs) != 1 || prs[0] != 143.0 {
		t.Errorf("one-PR window = %v, want the newest PR alone", last["prs"])
	}
	// Three measured PRs fill no window, so the series has no point (AC2).
	if series, ok := rewrite["series"].([]any); !ok || len(series) != 0 {
		t.Errorf("series = %v, want no point under ten measured PRs", rewrite["series"])
	}

	prs, ok := rewrite["prs"].([]any)
	if !ok || len(prs) != 3 {
		t.Fatalf("prs = %v, want the three measured merges", rewrite["prs"])
	}
	newest, ok := prs[0].(map[string]any)
	if !ok {
		t.Fatalf("newest PR = %v, want an object", prs[0])
	}
	if got, want := slices.Sorted(maps.Keys(newest)), []string{"agent_lines", "merged_at", "owner_lines", "pr", "pr_url", "rate", "repo", "run", "size", "title"}; !slices.Equal(got, want) {
		t.Errorf("PR keys = %v, want %v", got, want)
	}
	if newest["run"] != "run-2" || newest["repo"] != "octo/scratch" || newest["size"] != "M" ||
		newest["pr"] != 143.0 || newest["agent_lines"] != 100.0 || newest["owner_lines"] != 50.0 ||
		newest["rate"] != "50.0%" || newest["merged_at"] != "2026-10-15T09:00:00Z" ||
		newest["title"] != "feat(runner): FRG-143 add the widget" ||
		newest["pr_url"] != "https://github.com/octo/scratch/pull/143" {
		t.Errorf("newest PR = %v, want its run, repository, size, title, lines, own rate and merge", newest)
	}
	if oldest, ok := prs[2].(map[string]any); !ok || oldest["rate"] != "no agent lines" || oldest["title"] != "" {
		t.Errorf("oldest PR = %v, want a PR with no agent lines and the empty title a row measured before titles reads as", prs[2])
	}

	sizes, ok := rewrite["by_size"].([]any)
	if !ok || len(sizes) != 4 {
		t.Fatalf("by_size = %v, want the S, M, L and unsized rows", rewrite["by_size"])
	}
	for i, want := range []string{"S", "M", "L", "unsized"} {
		row, ok := sizes[i].(map[string]any)
		if !ok || row["size"] != want {
			t.Fatalf("by_size[%d] = %v, want %s", i, sizes[i], want)
		}
		// Under ten PRs a row carries its count and no rates.
		if row["rate"] != nil || row["median"] != nil || row["p90"] != nil {
			t.Errorf("%s = %v, want no rates below ten PRs", want, row)
		}
	}
	if m, _ := sizes[1].(map[string]any); m["prs"] != 1.0 {
		t.Errorf("M = %v, want its single measured PR counted", sizes[1])
	}
	if unsized, _ := sizes[3].(map[string]any); unsized["prs"] != 1.0 {
		t.Errorf("unsized = %v, want the PR naming no size counted", sizes[3])
	}

	repos, ok := rewrite["by_repo"].([]any)
	if !ok || len(repos) != 3 {
		t.Fatalf("by_repo = %v, want the two repositories and the PR naming none", rewrite["by_repo"])
	}
	for i, want := range []string{"octo/other", "octo/scratch", "unattributed"} {
		row, ok := repos[i].(map[string]any)
		if !ok || row["repo"] != want {
			t.Fatalf("by_repo[%d] = %v, want %s", i, repos[i], want)
		}
		if _, ok := row["tiles"].([]any); !ok {
			t.Errorf("%s = %v, want its own tiles", want, row)
		}
		if _, ok := row["series"].([]any); !ok {
			t.Errorf("%s = %v, want its own series", want, row)
		}
	}
	// Each repository is split over its own merges: the scratch one holds one PR
	// where the whole set holds three.
	scratch, _ := repos[1].(map[string]any)
	own, _ := scratch["tiles"].([]any)
	one, _ := own[0].(map[string]any)
	if one["asked"] != 10.0 || one["agent_lines"] != 100.0 || one["owner_lines"] != 50.0 || one["rate"] != "50.0%" {
		t.Errorf("scratch trailing tile = %v, want its own PR alone", own[0])
	}
}

// TestAutonomyReportsTheRollingRateOverTheMerges asserts the series a page plots:
// one point per merged PR with ten measured up to it, oldest first, each the rate
// pooled over its own window (AC2).
func TestAutonomyReportsTheRollingRateOverTheMerges(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	// Newest first: PR 110 merged last, and its ten PRs carry one more owner line
	// each, so the window they fill pools 55 lines over 1000.
	measured := make([]dispatcher.Sample, 0, 10)
	for i := range 10 {
		measured = append(measured, dispatcher.Sample{
			PR:       110 - i,
			MergedAt: at.Add(-time.Duration(i) * time.Hour),
			Rewrite:  dispatcher.Rewrite{AgentLines: 100, OwnerLines: int64(i + 1)},
		})
	}
	series := seriesOf(t, getAutonomy(t, &stubMeasured{merges: measured}))
	if len(series) != 1 {
		t.Fatalf("series = %v, want the one window ten measured PRs fill", series)
	}
	point, ok := series[0].(map[string]any)
	if !ok {
		t.Fatalf("point = %v, want an object", series[0])
	}
	if got, want := slices.Sorted(maps.Keys(point)), []string{"merged_at", "pr", "rate"}; !slices.Equal(got, want) {
		t.Errorf("point keys = %v, want %v", got, want)
	}
	if point["pr"] != 110.0 || point["rate"] != "5.5%" || point["merged_at"] != "2026-10-15T09:00:00Z" {
		t.Errorf("point = %v, want the newest merge at its own window's pooled rate", point)
	}

	// One merge more than the window fills adds the point before it, covering
	// that merge and the nine after it, while the newest window reads the same:
	// the series runs oldest first.
	measured = append(measured, dispatcher.Sample{PR: 100, MergedAt: at.Add(-10 * time.Hour),
		Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 100}})
	series = seriesOf(t, getAutonomy(t, &stubMeasured{merges: measured}))
	if len(series) != 2 {
		t.Fatalf("series = %v, want a point per merge with ten measured up to it", series)
	}
	for i, want := range []struct {
		pr   float64
		rate string
	}{{109, "15.4%"}, {110, "5.5%"}} {
		point, _ := series[i].(map[string]any)
		if point["pr"] != want.pr || point["rate"] != want.rate {
			t.Errorf("point %d = %v, want PR %v at %s", i, series[i], want.pr, want.rate)
		}
	}
}

// TestAutonomyCapsThePRListAtTheTrailingWindow asserts the list of PRs stops at
// the ten merges the tiles are read over, newest first, so a page lists the same
// window the rate is pooled over (AC3).
func TestAutonomyPrintsNoAgentLinesForAnEmptyWindowAndAPRWithNoLines(t *testing.T) {
	empty := getAutonomy(t, &stubMeasured{})
	if strings.Contains(empty.Body.String(), `"rate":"0.0%"`) {
		t.Fatalf("body = %s, want no measured zero over no merges", empty.Body.String())
	}
	var body struct {
		Rewrite map[string][]map[string]any `json:"rewrite"`
	}
	if err := json.Unmarshal(empty.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	tiles := body.Rewrite["tiles"]
	if len(tiles) == 0 {
		t.Fatal("tiles empty, want the windows")
	}
	for _, tile := range tiles {
		if tile["rate"] != "no agent lines" {
			t.Errorf("empty tile = %v, want the words for no agent lines", tile)
		}
	}

	rec := getAutonomy(t, &stubMeasured{merges: []dispatcher.Sample{{PR: 7}}})
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if pr := body.Rewrite["prs"][0]; pr["rate"] != "no agent lines" {
		t.Errorf("PR with no lines = %v, want the words for no agent lines", pr)
	}
}

func TestAutonomyFoldsAPRsSizeIntoTheBucketBySizeUses(t *testing.T) {
	measured := []dispatcher.Sample{
		{PR: 4, Size: "XL", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
		{PR: 3, Size: "s", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
		{PR: 2, Size: "", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
		{PR: 1, Size: "L", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
	}
	rec := getAutonomy(t, &stubMeasured{merges: measured})
	var body struct {
		Rewrite map[string][]map[string]any `json:"rewrite"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"unsized", "unsized", "unsized", "L"} {
		if got := body.Rewrite["prs"][i]["size"]; got != want {
			t.Errorf("prs[%d].size = %v, want %s", i, got, want)
		}
	}
}

func TestAutonomyCapsThePRListAtTheTrailingWindow(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	measured := make([]dispatcher.Sample, 0, dispatcher.TrailingPRs+1)
	for i := range dispatcher.TrailingPRs + 1 {
		measured = append(measured, dispatcher.Sample{
			PR:       200 - i,
			MergedAt: at.Add(-time.Duration(i) * time.Hour),
			Rewrite:  dispatcher.Rewrite{AgentLines: 100},
		})
	}
	rec := getAutonomy(t, &stubMeasured{merges: measured})

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	prs, ok := rewrite["prs"].([]any)
	if !ok || len(prs) != dispatcher.TrailingPRs {
		t.Fatalf("prs = %v, want the newest %d merges", rewrite["prs"], dispatcher.TrailingPRs)
	}
	newest, _ := prs[0].(map[string]any)
	oldest, _ := prs[len(prs)-1].(map[string]any)
	if newest["pr"] != 200.0 || oldest["pr"] != 191.0 {
		t.Errorf("prs run %v to %v, want the newest ten", newest["pr"], oldest["pr"])
	}
}

// TestAutonomyReportsARatedSizesSpreadAndWithholdsTheOneItLacks asserts a size
// with the PRs its rates need carries the median and p90 of their own rates, and
// a size whose PRs all have +Inf rates carries its pooled rate with neither,
// which a zero would otherwise read as a clean spread (AC4).
func TestAutonomyReportsARatedSizesSpreadAndWithholdsTheOneItLacks(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	var measured []dispatcher.Sample
	for range dispatcher.MinPRsPerSize {
		measured = append(measured, dispatcher.Sample{
			Size: dispatcher.SizeMedium, MergedAt: at,
			Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 20},
		})
	}
	// A rated size whose PRs the agent wrote nothing in: +Inf rates, no spread.
	for range dispatcher.MinPRsPerSize {
		measured = append(measured, dispatcher.Sample{
			Size: dispatcher.SizeLarge, MergedAt: at,
			Rewrite: dispatcher.Rewrite{OwnerLines: 10},
		})
	}
	rec := getAutonomy(t, &stubMeasured{merges: measured})

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	sizes, ok := rewrite["by_size"].([]any)
	if !ok || len(sizes) != 4 {
		t.Fatalf("by_size = %v, want the S, M, L and unsized rows", rewrite["by_size"])
	}
	if m, _ := sizes[1].(map[string]any); m["size"] != dispatcher.SizeMedium ||
		m["prs"] != float64(dispatcher.MinPRsPerSize) ||
		m["rate"] != "20.0%" || m["median"] != "20.0%" || m["p90"] != "20.0%" {
		t.Errorf("M = %v, want its pooled rate with the median and p90 of its own rates", sizes[1])
	}
	if lazy, _ := sizes[2].(map[string]any); lazy["size"] != dispatcher.SizeLarge ||
		lazy["prs"] != float64(dispatcher.MinPRsPerSize) ||
		lazy["rate"] != "no agent lines" || lazy["median"] != nil || lazy["p90"] != nil {
		t.Errorf("L = %v, want its +Inf pooled rate with no spread", sizes[2])
	}
}

// stubAutoMerge is an AutoMergeStore that answers with the switch it holds,
// recording what a write set it to.
type stubAutoMerge struct {
	enabled bool
	readErr error
	setErr  error
	// setAt is when the last write happened, zero while none did.
	setAt time.Time
}

func (s *stubAutoMerge) Enabled(context.Context) (bool, error) {
	if s.readErr != nil {
		return false, s.readErr
	}
	return s.enabled, nil
}

func (s *stubAutoMerge) Set(_ context.Context, enabled bool, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.enabled, s.setAt = enabled, at
	return nil
}

// measuredAt is when the merges the auto-merge tests read merged.
func measuredAt() time.Time { return time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC) }

// ratedMerges is the window the auto-merge rule reads: ten measured merges of
// one PR each, ownerPerPR of them rewritten.
func ratedMerges(at time.Time, ownerPerPR int64) []dispatcher.Sample {
	measured := make([]dispatcher.Sample, 0, dispatcher.TrailingPRs)
	for i := range dispatcher.TrailingPRs {
		measured = append(measured, dispatcher.Sample{
			PR: 140 - i, MergedAt: at.Add(-time.Duration(i) * time.Hour),
			Rewrite: dispatcher.Rewrite{AgentLines: 1_000, OwnerLines: ownerPerPR},
		})
	}
	return measured
}

// readAutoMerge reads the auto-merge state through the autonomy endpoint, which
// is where the page reads it.
func readAutoMerge(t *testing.T, merges []dispatcher.Sample, switches AutoMergeStore) map[string]any {
	t.Helper()
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Measured: &stubMeasured{merges: merges}, AutoMerge: switches})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	state, ok := body["auto_merge"].(map[string]any)
	if !ok {
		t.Fatalf("auto_merge = %v, want the switch and the rate", body["auto_merge"])
	}
	return state
}

// TestAutonomyReportsTheAutoMergeSwitchAndTheRatesVerdict asserts the autonomy
// view carries the owner's switch beside the rate's verdict, the two facts the
// effective state needs both of, with the bar read from the dispatcher's own
// constant (AC1).
func TestAutonomyReportsTheAutoMergeSwitchAndTheRatesVerdict(t *testing.T) {
	state := readAutoMerge(t, ratedMerges(measuredAt(), 98), &stubAutoMerge{enabled: true})

	if got, want := slices.Sorted(maps.Keys(state)), []string{"effective", "limit", "rate_allows", "rate_value", "reason", "switch_on", "window"}; !slices.Equal(got, want) {
		t.Fatalf("auto_merge keys = %v, want %v", got, want)
	}
	if state["switch_on"] != true || state["rate_allows"] != true || state["effective"] != true {
		t.Errorf("auto_merge = %v, want the switch on and the 9.8%% rate allowing it", state)
	}
	if state["rate_value"] != "9.8%" || state["limit"] != "10.0%" || state["window"] != 10.0 || state["reason"] != dispatcher.AutoMergeReasonOn {
		t.Errorf("auto_merge = %v, want the rule's own rate against its bar", state)
	}
}

// TestAutonomyReportsARateAboveTheBarAsNotAllowing asserts the two facts are
// separate: the owner's switch is on while the rate does not allow, so
// auto-merge is not effective (AC1).
func TestAutonomyReportsARateAboveTheBarAsNotAllowing(t *testing.T) {
	state := readAutoMerge(t, ratedMerges(measuredAt(), 101), &stubAutoMerge{enabled: true})

	if state["switch_on"] != true || state["rate_allows"] != false || state["effective"] != false {
		t.Errorf("auto_merge = %v, want the switch on and the rate refusing", state)
	}
	if state["rate_value"] != "10.1%" || state["reason"] != dispatcher.AutoMergeReasonRateAboveBar {
		t.Errorf("auto_merge = %v, want the rate with the reason it refused", state)
	}
}

// TestAutonomyReportsAnOffSwitchBesideARateThatAllows asserts the switch off is
// reported on its own, with the rate's verdict still read beside it (AC1).
func TestAutonomyReportsAnOffSwitchBesideARateThatAllows(t *testing.T) {
	state := readAutoMerge(t, ratedMerges(measuredAt(), 0), &stubAutoMerge{})

	if state["switch_on"] != false || state["effective"] != false {
		t.Errorf("auto_merge = %v, want the switch off and no effect", state)
	}
	if state["rate_allows"] != true || state["reason"] != dispatcher.AutoMergeReasonSwitchOff {
		t.Errorf("auto_merge = %v, want the rate still allowing it and the switch named as the reason", state)
	}
}

// TestAutonomyReportsASwitchItCannotReadAsOff asserts a switch that cannot be
// read is reported off with the reason, never on (AC4).
func TestAutonomyReportsASwitchItCannotReadAsOff(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{
		SPA:       fstest.MapFS{},
		Logger:    slog.New(slog.NewTextHandler(&log, nil)),
		Measured:  &stubMeasured{merges: ratedMerges(measuredAt(), 0)},
		AutoMerge: &stubAutoMerge{enabled: true, readErr: errors.New("firestore unreachable")},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	state, ok := body["auto_merge"].(map[string]any)
	if !ok {
		t.Fatalf("auto_merge = %v, want the switch and the rate", body["auto_merge"])
	}
	if state["switch_on"] != false || state["effective"] != false || state["reason"] != dispatcher.AutoMergeReasonSwitchUnread {
		t.Errorf("auto_merge = %v, want the unread switch reported off with the reason", state)
	}
	if !strings.Contains(log.String(), "autoMergeSwitchUnread") {
		t.Errorf("log = %q, want the unread switch reported", log.String())
	}
}

// postAutoMerge posts body to the auto-merge endpoint under contentType, empty
// for a request carrying no content type at all.
func postAutoMerge(t *testing.T, mux *http.ServeMux, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auto-merge", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAutoMergeSetsTheSwitchItIsGiven asserts the endpoint sets the switch its
// store holds and answers the state it set (AC2).
func TestAutoMergeSetsTheSwitchItIsGiven(t *testing.T) {
	stored := &stubAutoMerge{enabled: true}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), AutoMerge: stored})
	rec := postAutoMerge(t, mux, "application/json", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stored.enabled || stored.setAt.IsZero() {
		t.Fatalf("stored = %v at %v, want off recorded", stored.enabled, stored.setAt)
	}
	if got, want := rec.Body.String(), "{\"enabled\":false}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestAutoMergeRefusesABodyNamingNoSetting asserts a request that does not say
// whether auto-merge is enabled is refused, so a body naming nothing never reads
// as off (AC3).
func TestAutoMergeRefusesABodyNamingNoSetting(t *testing.T) {
	stored := &stubAutoMerge{enabled: true}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), AutoMerge: stored})
	for _, body := range []string{`{}`, `not json`, `{"autoMerge":false}`} {
		if rec := postAutoMerge(t, mux, "application/json", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
	}
	if !stored.enabled || !stored.setAt.IsZero() {
		t.Errorf("stored = %v at %v, want a refused body to write nothing", stored.enabled, stored.setAt)
	}
}

// TestAutoMergeRefusesANonJSONContentType asserts the write needs a JSON body,
// which a browser cannot send cross-origin without a preflight: a page that is
// not the surface cannot set the switch with the session IAP establishes (AC3).
func TestAutoMergeRefusesANonJSONContentType(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), AutoMerge: &stubAutoMerge{}})
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		if rec := postAutoMerge(t, mux, contentType, `{"enabled":false}`); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("content type %q = %d, want %d", contentType, rec.Code, http.StatusUnsupportedMediaType)
		}
	}
}

// TestAutoMergeAnswersTheWriteAlone asserts the endpoint takes the write and
// nothing else: the state is read from the autonomy view, so a read of the
// switch's own path is no endpoint.
func TestAutoMergeAnswersTheWriteAlone(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), AutoMerge: &stubAutoMerge{}})
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/auto-merge", strings.NewReader(`{"enabled":false}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auto-merge", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestAutoMergeWithoutAStoreIsUnavailable asserts a local run with no store
// answers 503 rather than recording a switch nowhere.
func TestAutoMergeWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})
	if rec := postAutoMerge(t, mux, "application/json", `{"enabled":false}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestAutoMergeReportsAStoreItCannotWrite asserts a failed write is a 500 with
// the failure reported, and answers no state the switch did not take.
func TestAutoMergeReportsAStoreItCannotWrite(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{
		SPA:       fstest.MapFS{},
		Logger:    slog.New(slog.NewTextHandler(&log, nil)),
		AutoMerge: &stubAutoMerge{setErr: errors.New("firestore unreachable")},
	})
	if rec := postAutoMerge(t, mux, "application/json", `{"enabled":false}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "autoMergeSwitchNotSet") {
		t.Fatalf("log = %q, want the failed write reported", log.String())
	}
}

// peakModelID is the configured model whose rate card prices peak hours.
const peakModelID = "command-code/deepseek/deepseek-v4.1-flash"

// unpricedModelID is the configured model its plan holds no rate card for, so a
// view tells it from a model priced the same at every hour.
const unpricedModelID = "command-code/meta/muse-spark-1.3-contributor"

// peakAt is a Monday inside that model's first window, 01:00-04:00 UTC on
// weekdays, so a run admitted then waits under off-peak-only.
func peakAt() time.Time { return time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC) }

// stubPeak is a PeakModeStore that answers with the mode it holds, recording
// what it was set to.
type stubPeak struct {
	mode    dispatcher.PeakMode
	setAt   time.Time
	readErr error
	setErr  error
}

func (s *stubPeak) Mode(context.Context) (dispatcher.PeakMode, error) {
	if s.readErr != nil {
		return "", s.readErr
	}
	return s.mode, nil
}

func (s *stubPeak) Set(_ context.Context, mode dispatcher.PeakMode, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.mode, s.setAt = mode, at
	return nil
}

// getPeak reads the peak view through the endpoint, failing if it is not served.
func getPeak(t *testing.T, reader PeakModeStore) *httptest.ResponseRecorder {
	t.Helper()
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Peak: reader})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/peak", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	return rec
}

// postPeak posts body to the peak endpoint under contentType, empty for a
// request carrying no content type at all.
func postPeak(t *testing.T, mux *http.ServeMux, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/peak", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestPeakShowsEveryModelsPricingAndWhetherAWindowIsOpen asserts the view lists
// every model a run may use with the window its rate prices peak hours in and
// whether the plan prices it at all — the peak-priced model's own window, and
// none for a model priced flat or not at all, which differ — and reports a
// window open at a moment inside it (AC1, AC2).
func TestPeakShowsEveryModelsPricingAndWhetherAWindowIsOpen(t *testing.T) {
	out := renderPeak(dispatcher.PeakOffPeakOnly, peakAt())
	if out.Mode != string(dispatcher.PeakOffPeakOnly) || !out.Holding {
		t.Fatalf("mode = %q, holding = %v, want off-peak-only and holding", out.Mode, out.Holding)
	}
	if want := providers.RunModels(); len(out.Models) != len(want) {
		t.Fatalf("models = %+v, want the %d a run may use", out.Models, len(want))
	}
	modelled := make(map[string]peakModel, len(out.Models))
	for _, model := range out.Models {
		modelled[model.Model] = model
	}
	want := []peakHour{{From: 1, To: 4}, {From: 6, To: 10}}
	if window := modelled[peakModelID].Peak; window == nil || window.Multiplier != 2 || !slices.Equal(window.Hours, want) {
		t.Errorf("window of %s = %+v, want x2 in %v", peakModelID, window, want)
	}
	for name, model := range modelled {
		if name == peakModelID {
			if !model.Priced {
				t.Errorf("%s is not priced, want the plan's rate card to price it", name)
			}
			continue
		}
		if model.Peak != nil {
			t.Errorf("%s carries a window, want none for a model priced flat or not at all", name)
		}
		if name == unpricedModelID && model.Priced {
			t.Errorf("%s is priced, want the plan to price it nowhere", name)
		}
		if name != unpricedModelID && !model.Priced {
			t.Errorf("%s is not priced, want the plan to price it the same at every hour", name)
		}
	}
}

// TestPeakReportsNoHoldWhileTheModeStartsRunsAtAnyHour asserts the same instant
// is nothing held under always, so the mode reaches the hold (AC2, AC3).
func TestPeakReportsNoHoldWhileTheModeStartsRunsAtAnyHour(t *testing.T) {
	if out := renderPeak(dispatcher.PeakAlways, peakAt()); out.Holding {
		t.Fatalf("holding = true at %v under always, want none", peakAt())
	}
}

// TestPeakServesTheStoredModeAndEveryModelsPricing asserts the endpoint serves
// the stored mode, whether a window is open, and its own keys per model — the
// window the peak-priced one's rate states, null for the rest, and whether the
// plan prices the model at all (AC1, AC2).
func TestPeakServesTheStoredModeAndEveryModelsPricing(t *testing.T) {
	rec := getPeak(t, &stubPeak{mode: dispatcher.PeakOffPeakOnly})
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := slices.Sorted(maps.Keys(body)), []string{"holding", "mode", "models"}; !slices.Equal(got, want) {
		t.Fatalf("body keys = %v, want %v", got, want)
	}
	if got, want := body["mode"], string(dispatcher.PeakOffPeakOnly); got != want {
		t.Errorf("mode = %v, want %v", got, want)
	}
	models, ok := body["models"].([]any)
	if !ok || len(models) != len(providers.RunModels()) {
		t.Fatalf("models = %v, want one per model a run may use", body["models"])
	}
	var window map[string]any
	for _, entry := range models {
		model, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("model = %v, want an object", entry)
		}
		if got, want := slices.Sorted(maps.Keys(model)), []string{"model", "peak", "priced"}; !slices.Equal(got, want) {
			t.Errorf("model keys = %v, want %v", got, want)
		}
		switch model["model"] {
		case peakModelID:
			window, ok = model["peak"].(map[string]any)
			if !ok {
				t.Fatalf("the peak-priced model's window = %v, want an object", model["peak"])
			}
			if got := model["priced"]; got != true {
				t.Errorf("priced of %s = %v, want true", peakModelID, got)
			}
		case unpricedModelID:
			if model["peak"] != nil {
				t.Errorf("%v carries a window, want null for a model the plan prices nowhere", model["model"])
			}
			if got := model["priced"]; got != false {
				t.Errorf("priced of %s = %v, want false", unpricedModelID, got)
			}
		default:
			if model["peak"] != nil {
				t.Errorf("%v carries a window, want null for a model priced flat", model["model"])
			}
			if got := model["priced"]; got != true {
				t.Errorf("priced of %v = %v, want true for a model priced flat", model["model"], got)
			}
		}
	}
	if window == nil {
		t.Fatalf("models = %v, want the peak-priced model's window", body["models"])
	}
	if got, want := slices.Sorted(maps.Keys(window)), []string{"hours", "multiplier"}; !slices.Equal(got, want) {
		t.Errorf("window keys = %v, want %v", got, want)
	}
	if got, want := window["multiplier"], 2.0; got != want {
		t.Errorf("multiplier = %v, want %v", got, want)
	}
	hours, ok := window["hours"].([]any)
	if !ok || len(hours) != 2 {
		t.Fatalf("hours = %v, want the two the rate card states", window["hours"])
	}
	for i, want := range []struct{ from, to float64 }{{1, 4}, {6, 10}} {
		hour, ok := hours[i].(map[string]any)
		if !ok {
			t.Fatalf("hour %d = %v, want an object", i, hours[i])
		}
		if got, want := slices.Sorted(maps.Keys(hour)), []string{"from", "to"}; !slices.Equal(got, want) {
			t.Errorf("hour keys = %v, want %v", got, want)
		}
		if hour["from"] != want.from || hour["to"] != want.to {
			t.Errorf("hour %d = %v-%v, want %v-%v", i, hour["from"], hour["to"], want.from, want.to)
		}
	}
}

// TestPeakSetsTheModeItIsGiven asserts a switch writes the mode the request
// names and answers with the state it left, which the next poll then reads
// (AC3, AC4).
func TestPeakSetsTheModeItIsGiven(t *testing.T) {
	stored := &stubPeak{mode: dispatcher.PeakOffPeakOnly}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Peak: stored})
	rec := postPeak(t, mux, "application/json", `{"mode":"always"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if stored.mode != dispatcher.PeakAlways || stored.setAt.IsZero() {
		t.Fatalf("stored mode = %q at %v, want always recorded", stored.mode, stored.setAt)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := body["mode"], string(dispatcher.PeakAlways); got != want {
		t.Errorf("mode = %v, want %v", got, want)
	}
	if got, want := body["holding"], false; got != want {
		t.Errorf("holding = %v, want %v under always", got, want)
	}
}

// TestPeakRefusesAModeItDoesNotName asserts a mode naming neither is refused and
// nothing is written, so the store never holds a value the dispatcher reads as
// unknown (AC3).
func TestPeakRefusesAModeItDoesNotName(t *testing.T) {
	stored := &stubPeak{mode: dispatcher.PeakOffPeakOnly}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Peak: stored})
	if rec := postPeak(t, mux, "application/json", `{"mode":"sometimes"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !stored.setAt.IsZero() {
		t.Fatalf("stored at %v, want a refused mode to write nothing", stored.setAt)
	}
}

// TestPeakRefusesABodyItCannotRead asserts a body that is not JSON naming a mode
// is a 400, not a mode nobody chose.
func TestPeakRefusesABodyItCannotRead(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Peak: &stubPeak{}})
	if rec := postPeak(t, mux, "application/json", "not json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestPeakRefusesANonJSONContentType asserts the write needs a JSON body, which
// a browser cannot send cross-origin without a preflight: a page that is not the
// surface cannot switch the mode with the session IAP establishes (AC4).
func TestPeakRefusesANonJSONContentType(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Peak: &stubPeak{}})
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		rec := postPeak(t, mux, contentType, `{"mode":"always"}`)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("content type %q = %d, want %d", contentType, rec.Code, http.StatusUnsupportedMediaType)
		}
	}
}

// TestPeakRefusesAnUnsupportedMethod asserts the endpoint answers the read and
// the write and nothing else: the mode has no other transition.
func TestPeakRefusesAnUnsupportedMethod(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Peak: &stubPeak{}})
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/peak", strings.NewReader(`{"mode":"always"}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

// TestPeakWithoutAStoreIsUnavailable asserts a local run with no store answers
// 503 on the read and the write rather than a mode nobody recorded.
func TestPeakWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/peak", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec := postPeak(t, mux, "application/json", `{"mode":"always"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestPeakReportsAStoreItCannotRead asserts a failed read is a 500 with the
// failure reported, not a mode nobody could read.
func TestPeakReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{
		SPA:    fstest.MapFS{},
		Logger: slog.New(slog.NewTextHandler(&log, nil)),
		Peak:   &stubPeak{readErr: errors.New("firestore unreachable")},
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/peak", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "peakModeNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

// TestPeakReportsAStoreItCannotWrite asserts a failed write is a 500 with the
// failure reported, and answers no mode the switch did not make.
func TestPeakReportsAStoreItCannotWrite(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{
		SPA:    fstest.MapFS{},
		Logger: slog.New(slog.NewTextHandler(&log, nil)),
		Peak:   &stubPeak{setErr: errors.New("firestore unreachable")},
	})

	if rec := postPeak(t, mux, "application/json", `{"mode":"always"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "peakModeNotSet") {
		t.Fatalf("log = %q, want the failed write reported", log.String())
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

// stubInbox answers with the runs it holds and counts its reads.
type stubInbox struct {
	runs  []store.InboxRun
	err   error
	reads atomic.Int32
	// since and limit are what the last read asked for.
	since time.Time
	limit int
	// hold blocks a read until closed; entered is sent to as a read starts.
	hold    chan struct{}
	entered chan struct{}
}

func (s *stubInbox) Inbox(_ context.Context, since time.Time, limit int) ([]store.InboxRun, error) {
	s.reads.Add(1)
	s.since, s.limit = since, limit
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	if s.hold != nil {
		<-s.hold
	}
	return s.runs, s.err
}

func getInbox(mux *http.ServeMux) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/inbox", nil))
	return rec
}

func TestInboxListsEachWaitingRunWithoutTicketText(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "r1", Ticket: "ABC-1", Repo: "octo/scratch", Size: "L", PRURL: "https://github.com/octo/scratch/pull/7",
			WaitReason: runner.WaitSizeL, UpdatedAt: measuredAt()},
	}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, AutoMerge: &stubAutoMerge{enabled: true}}))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, max-age=30" {
		t.Fatalf("status = %d, cache = %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	want := `{"runs":[{"run":"r1","ticket":"ABC-1","repo":"octo/scratch","size":"L","pr_url":"https://github.com/octo/scratch/pull/7","wait_reason":"size-l","updated_at":"2026-10-18T09:00:00Z"}]}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s, want %s", rec.Body.String(), want)
	}
}

// stubPulls answers PullSummaries with the summaries it holds and counts the reads.
type stubPulls struct {
	summaries map[dispatcher.PullRef]dispatcher.PullSummary
	err       error
	reads     int
	refs      []dispatcher.PullRef
}

func (s *stubPulls) PullSummaries(_ context.Context, refs []dispatcher.PullRef) (map[dispatcher.PullRef]dispatcher.PullSummary, error) {
	s.reads++
	s.refs = refs
	return s.summaries, s.err
}

func TestInboxCarriesGitHubsViewOfEachPRFromOneRead(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "r1", Repo: "octo/scratch", PRURL: "https://github.com/octo/scratch/pull/7"},
		{Run: "r2", Repo: "octo/scratch", PRURL: "https://github.com/octo/scratch/pull/8"},
		{Run: "r3", Repo: "octo/scratch", PRURL: "https://github.com/octo/scratch/pull/9"},
	}}
	pulls := &stubPulls{summaries: map[dispatcher.PullRef]dispatcher.PullSummary{
		{Repo: "octo/scratch", Number: 7}: {Title: "feat: a", State: dispatcher.PullStateOpen, HeadSHA: "abc123", Checks: dispatcher.CheckGreen, ChecksSummary: "go and web"},
		{Repo: "octo/scratch", Number: 8}: {Title: "fix: b", State: dispatcher.PullStateOpen, Draft: true, HeadSHA: "def456", Checks: dispatcher.CheckFailing, ChecksSummary: "web"},
	}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: pulls, AutoMerge: &stubAutoMerge{enabled: true}}))
	var got struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Runs) != 3 {
		t.Fatalf("body = %s, %v", rec.Body.String(), err)
	}
	if pulls.reads != 1 || len(pulls.refs) != 3 {
		t.Fatalf("GitHub read %d times for %v, want one read of all three PRs", pulls.reads, pulls.refs)
	}
	one, two, three := got.Runs[0], got.Runs[1], got.Runs[2]
	if one["title"] != "feat: a" || one["head_sha"] != "abc123" || one["draft"] != false ||
		!reflect.DeepEqual(one["checks"], map[string]any{"state": "green", "summary": "go and web"}) {
		t.Errorf("first = %v, want GitHub's title, head, draft and checks", one)
	}
	if two["draft"] != true || !reflect.DeepEqual(two["checks"], map[string]any{"state": "failing", "summary": "web"}) {
		t.Errorf("second = %v, want a failing draft", two)
	}
	for _, k := range []string{"title", "head_sha", "draft", "checks"} {
		if _, ok := three[k]; ok {
			t.Errorf("third has %q though GitHub did not return its PR: %v", k, three)
		}
	}
}

func TestInboxLeavesOutRunsWhosePRIsClosedOrMerged(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{
		{Run: "open", PRURL: "https://github.com/octo/scratch/pull/7"},
		{Run: "closed", PRURL: "https://github.com/octo/scratch/pull/8"},
		{Run: "merged", PRURL: "https://github.com/octo/scratch/pull/9"},
		{Run: "unknown", PRURL: "https://github.com/octo/scratch/pull/11"},
		{Run: "unread", PRURL: "https://github.com/octo/scratch/pull/10"},
	}}
	pulls := &stubPulls{summaries: map[dispatcher.PullRef]dispatcher.PullSummary{
		{Repo: "octo/scratch", Number: 7}:  {Title: "feat: a", State: dispatcher.PullStateOpen, HeadSHA: "abc123", Checks: dispatcher.CheckGreen, ChecksSummary: "go"},
		{Repo: "octo/scratch", Number: 8}:  {Title: "fix: b", State: dispatcher.PullStateClosed, HeadSHA: "def456", Checks: dispatcher.CheckGreen, ChecksSummary: "go"},
		{Repo: "octo/scratch", Number: 9}:  {Title: "chore: c", State: dispatcher.PullStateMerged, HeadSHA: "0a0a0a", Checks: dispatcher.CheckGreen, ChecksSummary: "go"},
		{Repo: "octo/scratch", Number: 11}: {Title: "docs: d", HeadSHA: "1b1b1b", Checks: dispatcher.CheckGreen, ChecksSummary: "go"},
	}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: pulls, AutoMerge: &stubAutoMerge{enabled: true}}))
	var got struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("status %d, body %s, %v", rec.Code, rec.Body.String(), err)
	}
	var listed []string
	for _, r := range got.Runs {
		listed = append(listed, r["run"].(string))
	}
	if !reflect.DeepEqual(listed, []string{"open", "unknown", "unread"}) {
		t.Fatalf("listed %v, want the open PR, the one whose state GitHub did not say and the one it did not return", listed)
	}
	if got.Runs[0]["title"] != "feat: a" || got.Runs[0]["head_sha"] != "abc123" {
		t.Errorf("open row = %v, want GitHub's title and head", got.Runs[0])
	}
}

func TestInboxStillListsWhenGitHubCannotBeRead(t *testing.T) {
	inbox := &stubInbox{runs: []store.InboxRun{{Run: "r1", PRURL: "https://github.com/octo/scratch/pull/7", WaitReason: runner.WaitSizeL}}}
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox,
		Pulls: &stubPulls{err: errors.New("unreachable")}, AutoMerge: &stubAutoMerge{enabled: true}}))
	var got struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || len(got.Runs) != 1 {
		t.Fatalf("status %d, body %s, %v, want the run listed", rec.Code, rec.Body.String(), err)
	}
	if _, ok := got.Runs[0]["checks"]; ok || got.Runs[0]["wait_reason"] != "size-l" {
		t.Fatalf("row = %v, want the store's fields without checks", got.Runs[0])
	}
}

func TestInboxShowsAutoMergeOffOnlyWhereNoNamedReasonHoldsThePR(t *testing.T) {
	runs := []store.InboxRun{{Run: "named", WaitReason: runner.WaitFrontEnd}, {Run: "unnamed"}}
	for name, tt := range map[string]struct {
		switches   AutoMergeStore
		wantNamed  string
		wantUnname string
	}{
		"switch off":        {&stubAutoMerge{}, "front-end", "auto-merge-off"},
		"switch unreadable": {&stubAutoMerge{readErr: errors.New("unreachable")}, "front-end", "auto-merge-off"},
		"switch on":         {&stubAutoMerge{enabled: true}, "front-end", ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: &stubInbox{runs: runs}, AutoMerge: tt.switches}))
			var raw struct {
				Runs []map[string]any `json:"runs"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || len(raw.Runs) != 2 {
				t.Fatalf("body = %s, %v", rec.Body.String(), err)
			}
			if raw.Runs[0]["wait_reason"] != tt.wantNamed || raw.Runs[1]["wait_reason"] != tt.wantUnname {
				t.Fatalf("reasons = %v, %v, want %q, %q", raw.Runs[0]["wait_reason"], raw.Runs[1]["wait_reason"], tt.wantNamed, tt.wantUnname)
			}
		})
	}
}

func TestInboxReadsTheTrailingWindowUpToItsLimit(t *testing.T) {
	inbox := &stubInbox{}
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	if _, err := (Deps{Inbox: inbox, AutoMerge: &stubAutoMerge{enabled: true}}).readInbox(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-runner.TrailingWindow); !inbox.since.Equal(want) || inbox.limit != 100 {
		t.Fatalf("read since %v up to %d, want since %v up to 100", inbox.since, inbox.limit, want)
	}
}

func TestInboxWithNoRunsListsNone(t *testing.T) {
	rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: &stubInbox{}}))
	if rec.Body.String() != "{\"runs\":[]}\n" {
		t.Fatalf("body = %q, want an empty list, not null", rec.Body.String())
	}
}

func TestInboxWithoutAStoreIsUnavailable(t *testing.T) {
	if rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestInboxReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: slog.New(slog.NewTextHandler(&log, nil)),
		Inbox: &stubInbox{err: errors.New("firestore unreachable")}})
	if rec := getInbox(mux); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "inboxNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

func TestInboxReadsOnceForRequestsThatOverlap(t *testing.T) {
	inbox := &stubInbox{hold: make(chan struct{}), entered: make(chan struct{}, 8)}
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			getInbox(mux)
		}()
	}
	<-inbox.entered
	time.Sleep(100 * time.Millisecond)
	close(inbox.hold)
	wg.Wait()
	if got := inbox.reads.Load(); got != 1 {
		t.Fatalf("reads = %d, want one shared by the three requests", got)
	}
}

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

func TestInboxShowsChecksNotGreenAboveTheReasonsThatNeverBlock(t *testing.T) {
	const pr = "https://github.com/octo/scratch/pull/7"
	ref := dispatcher.PullRef{Repo: "octo/scratch", Number: 7}
	for name, tt := range map[string]struct {
		stored  runner.WaitReason
		checks  string
		unread  bool
		enabled bool
		want    string
	}{
		"failing checks, nothing else holds it":        {"", dispatcher.CheckFailing, false, true, "checks-not-green"},
		"pending checks count as not green":            {runner.WaitSizeL, dispatcher.CheckPending, false, true, "checks-not-green"},
		"failing checks beat the switch being off":     {"", dispatcher.CheckFailing, false, false, "checks-not-green"},
		"failing checks beat a front-end hold":         {runner.WaitFrontEnd, dispatcher.CheckFailing, false, true, "checks-not-green"},
		"green checks leave the stored reason":         {runner.WaitFrontEnd, dispatcher.CheckGreen, false, true, "front-end"},
		"a draft is named before the checks":           {runner.WaitDraft, dispatcher.CheckFailing, false, true, "draft"},
		"an open decision is named before checks":      {runner.WaitUnconfirmedDecision, dispatcher.CheckFailing, false, true, "unconfirmed-decision"},
		"checks GitHub did not return are not made up": {"", "", true, true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			pulls := &stubPulls{summaries: map[dispatcher.PullRef]dispatcher.PullSummary{ref: {State: dispatcher.PullStateOpen, Checks: tt.checks}}}
			if tt.unread {
				pulls.summaries = nil
			}
			inbox := &stubInbox{runs: []store.InboxRun{{Run: "r1", PRURL: pr, WaitReason: tt.stored}}}
			rec := getInbox(newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Inbox: inbox, Pulls: pulls, AutoMerge: &stubAutoMerge{enabled: tt.enabled}}))
			var got struct {
				Runs []map[string]any `json:"runs"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Runs) != 1 {
				t.Fatalf("body = %s, %v", rec.Body.String(), err)
			}
			if got.Runs[0]["wait_reason"] != tt.want {
				t.Fatalf("wait_reason = %v, want %q", got.Runs[0]["wait_reason"], tt.want)
			}
		})
	}
}

func TestInboxPageFitsOneBatchedGitHubRead(t *testing.T) {
	if inboxLimit > dispatcher.MaxSummaryRefs {
		t.Fatalf("inboxLimit %d exceeds the %d pull requests one GitHub read holds, so no row would show its checks", inboxLimit, dispatcher.MaxSummaryRefs)
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
