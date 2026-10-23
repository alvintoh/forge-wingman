package main

import (
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
)

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

// TestAutonomyAsksForTheMeasuredCap asserts the endpoint reads the measured
// merges under the cap the figures are drawn from, never the whole collection.
func TestAutonomyAsksForTheMeasuredCap(t *testing.T) {
	reader := &stubMeasured{}
	getAutonomy(t, reader)
	if reader.limit != measuredShown {
		t.Errorf("limit asked = %d, want %d", reader.limit, measuredShown)
	}
}

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
