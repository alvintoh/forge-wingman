package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// getConfig reads the configuration through the endpoint with env as the
// service's environment.
func getConfig(t *testing.T, env map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	getenv := func(k string) string { return env[k] }
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Getenv: getenv})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	return rec
}

func decodeConfig(t *testing.T, rec *httptest.ResponseRecorder) configResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var got configResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestConfigServesTheDefaultsForAnEmptyEnvironment(t *testing.T) {
	got := decodeConfig(t, getConfig(t, nil)).Concurrency
	if want := (configConcurrency{ReviewWIP: 3, PlatformCap: 20, TwoStage: false, PollMinutes: 15}); got != want {
		t.Fatalf("concurrency = %+v, want %+v", got, want)
	}
}

func TestConfigServesAnOverriddenSetting(t *testing.T) {
	got := decodeConfig(t, getConfig(t, map[string]string{"WINGMAN_REVIEW_WIP": "5"})).Concurrency
	if got.ReviewWIP != 5 || got.PlatformCap != 20 {
		t.Fatalf("concurrency = %+v, want review WIP 5 beside the default platform cap", got)
	}
}

func TestConfigReadsTheProcessEnvironmentWithoutAnInjectedReader(t *testing.T) {
	t.Setenv("WINGMAN_REVIEW_WIP", "7")
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if got := decodeConfig(t, rec).Concurrency.ReviewWIP; got != 7 {
		t.Fatalf("review WIP = %d, want 7 from the process environment", got)
	}
}

func TestConfigShowsTheTwoStageSwitch(t *testing.T) {
	if got := decodeConfig(t, getConfig(t, map[string]string{"WINGMAN_TWO_STAGE": "on"})).Concurrency; !got.TwoStage {
		t.Fatalf("concurrency = %+v, want two-stage on", got)
	}
}

func TestConfigAnswers500ForAMalformedSetting(t *testing.T) {
	if rec := getConfig(t, map[string]string{"WINGMAN_REVIEW_WIP": "many"}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestConfigNamesEachPhasesDefaultModel(t *testing.T) {
	got := decodeConfig(t, getConfig(t, nil)).Phases
	want := []configPhase{
		{Phase: "plan", Model: runner.DefaultPlanModel()},
		{Phase: "build", Model: runner.DefaultModel()},
		{Phase: "review", Model: runner.DefaultReviewModels()[0]},
	}
	if len(got) != len(want) {
		t.Fatalf("phases = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("phases = %+v, want %+v", got, want)
		}
	}
}

func TestConfigHasAPeakWindowEntryForEachPhasesModel(t *testing.T) {
	got := decodeConfig(t, getConfig(t, nil))
	for _, phase := range got.Phases {
		if !slices.ContainsFunc(got.PeakWindows, func(m peakModel) bool { return m.Model == phase.Model }) {
			t.Errorf("no peak window entry for the %s phase's model %s", phase.Phase, phase.Model)
		}
	}
}

func TestConfigCarriesThePeakWindowOfAModelThatPricesPeak(t *testing.T) {
	for _, m := range decodeConfig(t, getConfig(t, nil)).PeakWindows {
		if m.Model != peakModelID {
			continue
		}
		if !m.Priced || m.Peak == nil || m.Peak.Multiplier != 2 || len(m.Peak.Hours) == 0 {
			t.Fatalf("%s = %+v, want a priced peak window at twice the price", peakModelID, m)
		}
		return
	}
	t.Fatalf("no peak window for %s", peakModelID)
}
