package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	mux := newMux(spa, discardLogger(), nil, nil, nil)

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
	newMux(spa, discardLogger(), nil, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fonts/IBMPlexSans-400.woff2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "font/woff2" {
		t.Fatalf("content type = %q, want font/woff2", got)
	}
}

func TestVersion(t *testing.T) {
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, nil)

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
		Models: []store.ModelComparison{{Model: "command-code/x", Tickets: 4, Passed: 3, CostPerTicket: 150_000}},
		Providers: []store.ProviderComparison{{Provider: "command-code", TrailingCostPerTicket: 125_000, Sample: 6,
			PeakTickets: 2, PeakCost: 400_000, OffPeakTickets: 4, OffPeakCost: 300_000}},
		Drifted: []store.DriftedRun{{RunID: "run-1", Provider: "command-code", Model: "command-code/x",
			Cost: 2 * money.Dollar, CostDrift: "cost $2.0000 is 60% above the $1.2500 average of 6 of this provider's runs"}},
	}}
	mux := newMux(fstest.MapFS{}, discardLogger(), reader, nil, nil)

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
	if got, want := slices.Sorted(maps.Keys(body.Models[0])), []string{"cost_per_ticket_usd", "model", "passed", "tickets"}; !slices.Equal(got, want) {
		t.Errorf("model keys = %v, want %v", got, want)
	}
	if got, want := body.Models[0]["model"], "command-code/x"; got != want {
		t.Errorf("model = %v, want %v", got, want)
	}
	if got, want := body.Models[0]["cost_per_ticket_usd"], 0.15; got != want {
		t.Errorf("cost per ticket = %v, want %v", got, want)
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
	mux := newMux(fstest.MapFS{}, discardLogger(), reader, nil, nil)

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
	mux := newMux(fstest.MapFS{}, discardLogger(), reader, nil, nil)

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
	mux := newMux(fstest.MapFS{}, discardLogger(), &stubComparison{}, nil, nil)

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
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/comparison", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestComparisonReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	reader := &stubComparison{err: errors.New("firestore unreachable")}
	mux := newMux(fstest.MapFS{}, slog.New(slog.NewTextHandler(&log, nil)), reader, nil, nil)

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
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, reader, nil)

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
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, &stubProviders{}, nil)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/providers", strings.NewReader(`{"provider":"command-code"}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestProvidersWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestProvidersWithNoPlansListsNone asserts an owner with no plan recorded
// answers with an empty list rather than a null (AC5).
func TestProvidersWithNoPlansListsNone(t *testing.T) {
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, &stubProviders{}, nil)

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
	mux := newMux(fstest.MapFS{}, slog.New(slog.NewTextHandler(&log, nil)), nil, reader, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "providersNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

// stubAutonomy is an AutonomyReader that answers with the view it holds, or the
// error it was given.
type stubAutonomy struct {
	view store.AutonomyView
	err  error
	// limit is the history limit the last read asked for.
	limit int
}

func (s *stubAutonomy) AutonomyView(_ context.Context, limit int) (store.AutonomyView, error) {
	s.limit = limit
	if s.err != nil {
		return store.AutonomyView{}, s.err
	}
	return s.view, nil
}

// TestAutonomyReportsTheLevelItsEvidenceAndItsHistory asserts the autonomy
// endpoint serves the stored level and what it allows, the earned snapshot with
// the numbers behind it, and the changes recorded newest first, each with the
// cause it happened for and the numbers it was measured on (AC1, AC2, AC3).
func TestAutonomyReportsTheLevelItsEvidenceAndItsHistory(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	reader := &stubAutonomy{view: store.AutonomyView{
		Level: 4, HasEarned: true,
		Earned: dispatcher.Earned{
			Level: 3, ComputedAt: at,
			Numbers: dispatcher.Numbers{
				PRs: []int{141, 140}, Rate: 0.006, AgentLines: 1_000, OwnerLines: 6,
				ReviewFoundLines: 4, OwnerChangeLines: 2,
			},
		},
		History: []dispatcher.Change{
			{From: 4, To: 3, Cause: string(dispatcher.RollbackSampledRate), At: at,
				Numbers: dispatcher.Numbers{PRs: []int{141}, Rate: 0.11, AgentLines: 1_000, OwnerLines: 110}},
			{From: 3, To: 4, Cause: dispatcher.ChangeAdvance, At: at.Add(-time.Hour),
				Numbers: dispatcher.Numbers{PRs: []int{140}, AgentLines: 1_000, OwnerLines: 6}},
		},
	}}
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, reader)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// The endpoint serves these keys and no others, so nothing the level's
	// records hold beyond them — a ticket's text above all — can reach a reader
	// (AC5).
	if got, want := slices.Sorted(maps.Keys(body)), []string{"allows", "earned", "history", "level"}; !slices.Equal(got, want) {
		t.Fatalf("body keys = %v, want %v", got, want)
	}
	if got, want := body["level"], 4.0; got != want {
		t.Errorf("level = %v, want %v", got, want)
	}
	if got, want := body["allows"], "as level 3, and eligible S and M PRs auto-merge"; got != want {
		t.Errorf("allows = %v, want %v", got, want)
	}
	earned, ok := body["earned"].(map[string]any)
	if !ok {
		t.Fatalf("earned = %v, want the snapshot", body["earned"])
	}
	if got, want := slices.Sorted(maps.Keys(earned)), []string{"computed_at", "evidence", "level", "stale"}; !slices.Equal(got, want) {
		t.Errorf("earned keys = %v, want %v", got, want)
	}
	if got, want := earned["level"], 3.0; got != want {
		t.Errorf("earned level = %v, want %v", got, want)
	}
	if got, want := earned["stale"], false; got != want {
		t.Errorf("earned stale = %v, want %v", got, want)
	}
	if got, want := earned["computed_at"], "2026-10-15T09:00:00Z"; got != want {
		t.Errorf("computed_at = %v, want %v", got, want)
	}
	numbers, ok := earned["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("evidence = %v, want the numbers", earned["evidence"])
	}
	if got, want := slices.Sorted(maps.Keys(numbers)), []string{"agent_lines", "owner_change_lines", "owner_lines", "prs", "rate",
		"recorded", "reverts", "review_found_lines", "unavailable", "unlabelled_lines"}; !slices.Equal(got, want) {
		t.Errorf("evidence keys = %v, want %v", got, want)
	}
	if got, want := numbers["rate"], "0.6%"; got != want {
		t.Errorf("rate = %v, want %v", got, want)
	}
	if prs, ok := numbers["prs"].([]any); !ok || len(prs) != 2 || prs[0] != 141.0 || prs[1] != 140.0 {
		t.Errorf("prs = %v, want the two PRs measured", numbers["prs"])
	}
	if got, want := numbers["agent_lines"], 1000.0; got != want {
		t.Errorf("agent lines = %v, want %v", got, want)
	}
	if got, want := numbers["owner_lines"], 6.0; got != want {
		t.Errorf("owner lines = %v, want %v", got, want)
	}
	if got, want := numbers["review_found_lines"], 4.0; got != want {
		t.Errorf("review-found lines = %v, want %v", got, want)
	}
	if got, want := numbers["owner_change_lines"], 2.0; got != want {
		t.Errorf("owner-change lines = %v, want %v", got, want)
	}
	history, ok := body["history"].([]any)
	if !ok || len(history) != 2 {
		t.Fatalf("history = %v, want the two changes", body["history"])
	}
	drop, ok := history[0].(map[string]any)
	if !ok {
		t.Fatalf("newest change = %v, want an object", history[0])
	}
	if got, want := slices.Sorted(maps.Keys(drop)), []string{"at", "cause", "evidence", "from", "to"}; !slices.Equal(got, want) {
		t.Errorf("change keys = %v, want %v", got, want)
	}
	if got, want := drop["from"], 4.0; got != want {
		t.Errorf("drop from = %v, want %v", got, want)
	}
	if got, want := drop["to"], 3.0; got != want {
		t.Errorf("drop to = %v, want %v", got, want)
	}
	if got, want := drop["cause"], "sampled-rate"; got != want {
		t.Errorf("drop cause = %v, want %v", got, want)
	}
	if got, want := drop["at"], "2026-10-15T09:00:00Z"; got != want {
		t.Errorf("drop at = %v, want %v", got, want)
	}
	raised, ok := history[1].(map[string]any)
	if !ok {
		t.Fatalf("second change = %v, want an object", history[1])
	}
	if got, want := raised["cause"], "advance"; got != want {
		t.Errorf("second cause = %v, want %v", got, want)
	}
	if got, want := raised["from"], 3.0; got != want {
		t.Errorf("second from = %v, want %v", got, want)
	}
	if got, want := raised["at"], "2026-10-15T08:00:00Z"; got != want {
		t.Errorf("second at = %v, want %v", got, want)
	}
}

// TestAutonomyWithNoSnapshotAndNoHistoryListsNone asserts an owner whose level
// has never been computed reads no snapshot and an empty history rather than a
// null, which the view reads as nothing to show (AC2, AC6).
func TestAutonomyWithNoSnapshotAndNoHistoryListsNone(t *testing.T) {
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, &stubAutonomy{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "{\"level\":0,\"allows\":\"you delegate and you merge\",\"earned\":null,\"history\":[]}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestAutonomyReportsAStaleSnapshotAboveTheEarnedLevel asserts the two facts the
// view words: the snapshot is stale, and the stored level is above the one the
// PRs have earned (AC4, AC6).
func TestAutonomyReportsAStaleSnapshotAboveTheEarnedLevel(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	reader := &stubAutonomy{view: store.AutonomyView{
		Level: 5, HasEarned: true,
		Earned: dispatcher.Earned{Level: 4, Stale: true, ComputedAt: at},
	}}
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, reader)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := body["level"], 5.0; got != want {
		t.Errorf("level = %v, want %v", got, want)
	}
	earned, ok := body["earned"].(map[string]any)
	if !ok {
		t.Fatalf("earned = %v, want the snapshot", body["earned"])
	}
	if got, want := earned["stale"], true; got != want {
		t.Errorf("stale = %v, want %v", got, want)
	}
	if got, want := earned["level"], 4.0; got != want {
		t.Errorf("earned level = %v, want %v", got, want)
	}
}

// TestAutonomyPrintsTheWordForARateWithNoAgentLines asserts a rate measured over
// none of the agent's lines reads as words rather than the +Inf that no JSON
// number holds, which would fail the whole answer (AC1).
func TestAutonomyPrintsTheWordForARateWithNoAgentLines(t *testing.T) {
	reader := &stubAutonomy{view: store.AutonomyView{
		Level: 3, HasEarned: true,
		Earned: dispatcher.Earned{Level: 3, ComputedAt: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC),
			Numbers: dispatcher.Numbers{PRs: []int{141}, Rate: math.Inf(1), OwnerLines: 4}},
	}}
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, reader)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"rate":"no agent lines"`) {
		t.Fatalf("body = %s, want the word for a rate with no agent lines under it", rec.Body.String())
	}
}

// TestAutonomyRefusesAWriteMethod asserts the autonomy endpoint refuses every
// method that could set a level: the surface has no such route, and the level
// stays the runner autonomy command's (AC5).
func TestAutonomyRefusesAWriteMethod(t *testing.T) {
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, &stubAutonomy{})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/autonomy", strings.NewReader(`{"level":5}`)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

// TestAutonomyWithoutAStoreIsUnavailable asserts a local run with no store
// answers 503 rather than a level nobody recorded.
func TestAutonomyWithoutAStoreIsUnavailable(t *testing.T) {
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestAutonomyReportsAStoreItCannotRead asserts a failed read is a 500 with the
// failure reported, not a level nobody could read.
func TestAutonomyReportsAStoreItCannotRead(t *testing.T) {
	var log strings.Builder
	reader := &stubAutonomy{err: errors.New("firestore unreachable")}
	mux := newMux(fstest.MapFS{}, slog.New(slog.NewTextHandler(&log, nil)), nil, nil, reader)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(log.String(), "autonomyNotRead") {
		t.Fatalf("log = %q, want the failed read reported", log.String())
	}
}

func getAutonomy(t *testing.T, reader *stubAutonomy) *httptest.ResponseRecorder {
	t.Helper()
	mux := newMux(fstest.MapFS{}, discardLogger(), nil, nil, reader)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autonomy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	return rec
}

// TestAutonomySerialisesAnEmptySnapshotsPRsAsAnEmptyList asserts evidence with no
// PRs reads `"prs":[]` and never null, which would blank the view.
func TestAutonomySerialisesAnEmptySnapshotsPRsAsAnEmptyList(t *testing.T) {
	rec := getAutonomy(t, &stubAutonomy{view: store.AutonomyView{
		Level: 3, HasEarned: true,
		Earned: dispatcher.Earned{Level: 3, ComputedAt: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)},
	}})
	if !strings.Contains(rec.Body.String(), `"prs":[]`) || strings.Contains(rec.Body.String(), `"prs":null`) {
		t.Fatalf("body = %s, want prs as an empty list", rec.Body.String())
	}
}

// TestAutonomyAsksForTheCappedHistoryAndSaysWhenItIsCut asserts the reader is
// asked for exactly the cap, and the answer says so only when the list reached it.
func TestAutonomyAsksForTheCappedHistoryAndSaysWhenItIsCut(t *testing.T) {
	full := make([]dispatcher.Change, autonomyHistoryShown)
	for _, tt := range []struct {
		name    string
		history []dispatcher.Change
		cutAt   string
	}{
		{"whole", full[:2], ``},
		{"cut", full, `cut`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := &stubAutonomy{view: store.AutonomyView{History: tt.history}}
			rec := getAutonomy(t, reader)
			if reader.limit != 50 {
				t.Errorf("limit asked = %d, want 50", reader.limit)
			}
			if got := strings.Contains(rec.Body.String(), `"history_cut_at":50`); got != (tt.cutAt != "") {
				t.Errorf("history_cut_at present = %v, want %v", got, tt.cutAt != "")
			}
		})
	}
}

// TestAutonomyMarksEvidenceTheStoreHeldNoneOfAsNotRecorded asserts the endpoint
// says the numbers are absent rather than leaving the view to read zeros.
func TestAutonomyMarksEvidenceTheStoreHeldNoneOfAsNotRecorded(t *testing.T) {
	rec := getAutonomy(t, &stubAutonomy{view: store.AutonomyView{
		History: []dispatcher.Change{
			{From: 3, To: 4, Numbers: dispatcher.Numbers{NotRecorded: true}},
			{From: 4, To: 3, Numbers: dispatcher.Numbers{PRs: []int{1}}},
		},
	}})
	if got := strings.Count(rec.Body.String(), `"recorded":false`); got != 1 {
		t.Fatalf("body = %s, want exactly the first change not recorded", rec.Body.String())
	}
}
