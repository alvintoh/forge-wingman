package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// stubSettings is a SettingsStore that answers with the cap it holds, recording
// what a write set it to.
type stubSettings struct {
	runs    int
	readErr error
	setErr  error
	// written is the cap the last write stored, and setAt when it did; zero
	// while none did.
	written int
	setAt   time.Time
}

func (s *stubSettings) RunsPerRepo(context.Context) (int, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	return s.runs, nil
}

func (s *stubSettings) SetRunsPerRepo(_ context.Context, n int, at time.Time) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.written, s.setAt = n, at
	return nil
}

// readSettings reads the Config settings through their endpoint, which answers
// the three rows of the modal.
func readSettings(t *testing.T, settings SettingsStore, auto AutoMergeStore, limit int) map[string]any {
	t.Helper()
	mux := newMux(Deps{
		SPA: fstest.MapFS{}, Logger: discardLogger(), Settings: settings, AutoMerge: auto, PlatformCap: limit,
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body = %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestSettingsShowsTheDefaultCapAndTheJobLimit(t *testing.T) {
	body := readSettings(t, &stubSettings{runs: dispatcher.DefaultLimits.RepoCap}, &stubAutoMerge{enabled: true}, 0)

	if body["runs_per_repo"] != float64(dispatcher.DefaultLimits.RepoCap) {
		t.Errorf("runs_per_repo = %v, want the default %d", body["runs_per_repo"], dispatcher.DefaultLimits.RepoCap)
	}
	if body["github_job_limit"] != float64(dispatcher.DefaultLimits.PlatformCap) {
		t.Errorf("github_job_limit = %v, want the default %d", body["github_job_limit"], dispatcher.DefaultLimits.PlatformCap)
	}
	if on, ok := body["auto_merge"].(bool); !ok || !on {
		t.Errorf("auto_merge = %v, want the switch as stored", body["auto_merge"])
	}
	if read, ok := body["auto_merge_read"].(bool); !ok || !read {
		t.Errorf("auto_merge_read = %v, want the switch read", body["auto_merge_read"])
	}
}

func TestSettingsShowsTheSavedCapAndTheConfiguredJobLimit(t *testing.T) {
	body := readSettings(t, &stubSettings{runs: 8}, &stubAutoMerge{}, 12)

	if body["runs_per_repo"] != float64(8) {
		t.Errorf("runs_per_repo = %v, want the saved 8", body["runs_per_repo"])
	}
	if body["github_job_limit"] != float64(12) {
		t.Errorf("github_job_limit = %v, want the configured 12", body["github_job_limit"])
	}
}

// TestSettingsReportsAnUnreadableAutoMergeSwitch asserts the modal is told the
// switch could not be read, rather than being shown off with no reason.
func TestSettingsReportsAnUnreadableAutoMergeSwitch(t *testing.T) {
	body := readSettings(t, &stubSettings{runs: 5}, &stubAutoMerge{readErr: errors.New("unreachable")}, 0)

	if on, ok := body["auto_merge"].(bool); !ok || on {
		t.Fatalf("auto_merge = %v, want off", body["auto_merge"])
	}
	if read, ok := body["auto_merge_read"].(bool); !ok || read {
		t.Fatalf("auto_merge_read = %v, want the read to have failed", body["auto_merge_read"])
	}
}

// TestSettingsShowsTheDefaultWhenTheCapCannotBeRead asserts the modal reports
// the number the dispatcher applies to a setting it cannot read, rather than
// failing the read.
func TestSettingsShowsTheDefaultWhenTheCapCannotBeRead(t *testing.T) {
	body := readSettings(t, &stubSettings{readErr: errors.New("unreachable")}, &stubAutoMerge{enabled: true}, 0)

	if body["runs_per_repo"] != float64(dispatcher.DefaultLimits.RepoCap) {
		t.Fatalf("runs_per_repo = %v, want the default %d", body["runs_per_repo"], dispatcher.DefaultLimits.RepoCap)
	}
}

func TestSettingsAnswers503WithoutAStore(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// postRunsPerRepo posts body as contentType to the settings endpoint.
func postRunsPerRepo(t *testing.T, settings SettingsStore, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Settings: settings})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/runs-per-repo", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSetRunsPerRepoSavesACapInRange(t *testing.T) {
	settings := &stubSettings{}
	rec := postRunsPerRepo(t, settings, `{"runs_per_repo":7}`, "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if settings.written != 7 || settings.setAt.IsZero() {
		t.Fatalf("wrote %d at %v, want 7 stamped now", settings.written, settings.setAt)
	}
	var body struct {
		RunsPerRepo int `json:"runs_per_repo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.RunsPerRepo != 7 {
		t.Fatalf("body = %q, want the saved cap", rec.Body.String())
	}
}

func TestSetRunsPerRepoRefusesAValueOutsideTheRange(t *testing.T) {
	for _, body := range []string{`{"runs_per_repo":0}`, `{"runs_per_repo":21}`} {
		settings := &stubSettings{}
		if rec := postRunsPerRepo(t, settings, body, "application/json"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
		if !settings.setAt.IsZero() {
			t.Fatalf("%s reached the store: %+v", body, settings)
		}
	}
}

func TestSetRunsPerRepoRefusesANonJSONBody(t *testing.T) {
	settings := &stubSettings{}
	rec := postRunsPerRepo(t, settings, `{"runs_per_repo":7}`, "text/plain")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnsupportedMediaType)
	}
	if !settings.setAt.IsZero() {
		t.Fatalf("a non-JSON body reached the store: %+v", settings)
	}
}

func TestSetRunsPerRepoAnswers500WhenTheStoreRefuses(t *testing.T) {
	settings := &stubSettings{setErr: errors.New("unreachable")}
	if rec := postRunsPerRepo(t, settings, `{"runs_per_repo":7}`, "application/json"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestSetRunsPerRepoRefusesABodyThatNamesNoCap(t *testing.T) {
	for _, body := range []string{`{}`, `{"runs_per_repo":"many"}`, `not json`} {
		settings := &stubSettings{}
		if rec := postRunsPerRepo(t, settings, body, "application/json"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: status = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
		if !settings.setAt.IsZero() {
			t.Fatalf("%q reached the store: %+v", body, settings)
		}
	}
}

func TestSetRunsPerRepoNeedsAStore(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/runs-per-repo", strings.NewReader(`{"runs_per_repo":5}`))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestPlatformCapReadsTheDispatcherSetting(t *testing.T) {
	if got := platformCap(func(string) string { return "" }); got != dispatcher.DefaultLimits.PlatformCap {
		t.Errorf("unset = %d, want the default", got)
	}
	if got := platformCap(func(string) string { return " 8 " }); got != 8 {
		t.Errorf("8 = %d, want 8", got)
	}
	for _, raw := range []string{"many", "0", "-3"} {
		getenv := func(string) string { return raw }
		if got := platformCap(getenv); got != dispatcher.DefaultLimits.PlatformCap {
			t.Errorf("%q = %d, want the default", raw, got)
		}
	}
}
