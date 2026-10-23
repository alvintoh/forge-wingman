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

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

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
