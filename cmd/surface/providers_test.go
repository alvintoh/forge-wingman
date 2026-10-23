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
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/store"
)

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
