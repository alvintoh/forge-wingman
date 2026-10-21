package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

var projectsAt = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// stubProjects serves every project store from fixed data and records the start it is asked for.
type stubProjects struct {
	listed    []dispatcher.PhaseProject
	snapshots map[string]dispatcher.PhaseSnapshot
	reaches   map[string]dispatcher.RepoReach
	activity  map[string]time.Time
	requested []string
	pauses    []string
	pauseErr  error
}

func (s *stubProjects) Listed(context.Context) ([]dispatcher.PhaseProject, error) {
	return s.listed, nil
}

func (s *stubProjects) Snapshots(context.Context) (map[string]dispatcher.PhaseSnapshot, error) {
	return s.snapshots, nil
}

func (s *stubProjects) Reads(context.Context) (map[string]dispatcher.RepoReach, error) {
	return s.reaches, nil
}

func (s *stubProjects) RepoLoads(context.Context) (map[string]store.RepoLoad, error) {
	return map[string]store.RepoLoad{"octo/widget": {InFlight: 2, Held: 1}}, nil
}

func (s *stubProjects) LastActivity(_ context.Context, repo string) (time.Time, error) {
	return s.activity[repo], nil
}

func (s *stubProjects) RequestStart(_ context.Context, repo, phase string, _ time.Time) error {
	s.requested = append(s.requested, repo+":"+phase)
	return nil
}

func (s *stubProjects) Set(_ context.Context, repo string, paused bool, _ time.Time) error {
	if s.pauseErr != nil {
		return s.pauseErr
	}
	s.pauses = append(s.pauses, fmt.Sprintf("%s:%t", repo, paused))
	return nil
}

func newProjectsMux(s *stubProjects) *http.ServeMux {
	return newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Projects: ProjectStores{
		Projects: s, Snapshots: s, Reaches: s, Loads: s, Activity: s, Starts: s, Pauses: s,
	}})
}

// startableProjects is one listed project that can be started on phase P1.
func startableProjects() *stubProjects {
	return &stubProjects{
		listed:    []dispatcher.PhaseProject{{Repository: "octo/widget", LinearProject: "Roadmap", AddedAt: projectsAt.Add(-time.Hour)}},
		snapshots: map[string]dispatcher.PhaseSnapshot{"octo/widget": {Repository: "octo/widget", Current: "P1"}},
		reaches:   map[string]dispatcher.RepoReach{"octo/widget": {Repository: "octo/widget", Reachable: true, CheckedAt: projectsAt}},
	}
}

func postStart(mux http.Handler, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/start", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestStartProjectRefusals(t *testing.T) {
	tests := []struct {
		name   string
		change func(*stubProjects)
		body   string
		want   int
	}{
		{"unlisted repository", nil, `{"repository":"octo/other","phase":"P1"}`, http.StatusNotFound},
		{"the phase moved", func(s *stubProjects) {
			s.snapshots["octo/widget"] = dispatcher.PhaseSnapshot{Current: "P2"}
		}, `{"repository":"octo/widget","phase":"P1"}`, http.StatusConflict},
		{"phases not read", func(s *stubProjects) {
			s.snapshots["octo/widget"] = dispatcher.PhaseSnapshot{Unreadable: true}
		}, `{"repository":"octo/widget","phase":"P1"}`, http.StatusConflict},
		{"repository unreachable", func(s *stubProjects) {
			s.reaches["octo/widget"] = dispatcher.RepoReach{Reason: dispatcher.ReachForbidden, CheckedAt: projectsAt}
		}, `{"repository":"octo/widget","phase":"P1"}`, http.StatusConflict},
		{"not JSON", nil, `nope`, http.StatusBadRequest},
		{"no phase", nil, `{"repository":"octo/widget"}`, http.StatusBadRequest},
		{"a phase name over the bound", nil, `{"repository":"octo/widget","phase":"` + strings.Repeat("p", maxPhaseName+1) + `"}`, http.StatusBadRequest},
		{"repository not owner/name", nil, `{"repository":"widget","phase":"P1"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := startableProjects()
			if tt.change != nil {
				tt.change(s)
			}
			rec := postStart(newProjectsMux(s), "application/json", tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
			if len(s.requested) != 0 {
				t.Errorf("recorded %v for a refused start", s.requested)
			}
		})
	}
}

func TestStartProjectRecordsTheRequestAndRefusesANonJSONBody(t *testing.T) {
	s := startableProjects()
	mux := newProjectsMux(s)
	if rec := postStart(mux, "text/plain", `{"repository":"octo/widget","phase":"P1"}`); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain status = %d, want %d", rec.Code, http.StatusUnsupportedMediaType)
	}
	rec := postStart(mux, "application/json", `{"repository":"Octo/Widget","phase":"P1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if want := []string{"Octo/Widget:P1"}; !slices.Equal(s.requested, want) {
		t.Errorf("recorded %v, want %v", s.requested, want)
	}
}

func TestProjectsListNewestActivityFirstWithTheTotal(t *testing.T) {
	s := &stubProjects{
		listed: []dispatcher.PhaseProject{
			{Repository: "octo/old", AddedAt: projectsAt.Add(-72 * time.Hour)},
			{Repository: "octo/widget", AddedAt: projectsAt.Add(-48 * time.Hour)},
			{Repository: "octo/fresh", AddedAt: projectsAt.Add(-time.Hour)},
		},
		snapshots: map[string]dispatcher.PhaseSnapshot{},
		reaches:   map[string]dispatcher.RepoReach{},
		// widget was added before fresh but ran after it, so it leads.
		activity: map[string]time.Time{"octo/widget": projectsAt},
	}
	rec := httptest.NewRecorder()
	newProjectsMux(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got projectsResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, p := range got.Projects {
		order = append(order, p.Repository)
	}
	if want := []string{"octo/widget", "octo/fresh", "octo/old"}; !slices.Equal(order, want) || got.Total != 3 {
		t.Errorf("order %v total %d, want %v total 3", order, got.Total, want)
	}
	if first := got.Projects[0]; first.Queue != (projectQueue{InFlight: 2, Held: 1}) || first.Reach.Status != dispatcher.ReachChecking {
		t.Errorf("first = %+v, want its queue load and a reach still checking", first)
	}
}

func TestProjectsWithoutStoresAreUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestStartProjectSaysWhyARepositoryIsRefused(t *testing.T) {
	s := startableProjects()
	s.reaches["octo/widget"] = dispatcher.RepoReach{Reason: dispatcher.ReachForbidden, CheckedAt: projectsAt}
	rec := postStart(newProjectsMux(s), "application/json", `{"repository":"octo/widget","phase":"P1"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "cannot read the repository") {
		t.Errorf("unreachable: %d %q, want 409 saying the dispatcher cannot read it", rec.Code, rec.Body.String())
	}
	delete(s.reaches, "octo/widget")
	rec = postStart(newProjectsMux(s), "application/json", `{"repository":"octo/widget","phase":"P1"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not been checked") {
		t.Errorf("unchecked: %d %q, want 409 saying it has not been checked", rec.Code, rec.Body.String())
	}
}

func postPause(mux http.Handler, action, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+action, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestStopAndResumeSetThePauseThroughTheSetter(t *testing.T) {
	s := startableProjects()
	mux := newProjectsMux(s)
	for _, action := range []string{"stop", "resume", "resume"} {
		rec := postPause(mux, action, "application/json", `{"repository":"Octo/Widget"}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d, want %d: %s", action, rec.Code, http.StatusAccepted, rec.Body.String())
		}
	}
	if want := []string{"Octo/Widget:true", "Octo/Widget:false", "Octo/Widget:false"}; !slices.Equal(s.pauses, want) {
		t.Errorf("set %v, want %v", s.pauses, want)
	}
}

func TestStopAndResumeRefusals(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		err         error
		want        int
	}{
		{"unlisted repository", "application/json", `{"repository":"octo/other"}`, store.ErrRepositoryNotListed, http.StatusNotFound},
		{"not JSON", "application/json", `nope`, nil, http.StatusBadRequest},
		{"no repository", "application/json", `{}`, nil, http.StatusBadRequest},
		{"repository not owner/name", "application/json", `{"repository":"widget"}`, nil, http.StatusBadRequest},
		{"not a JSON content type", "text/plain", `{"repository":"octo/widget"}`, nil, http.StatusUnsupportedMediaType},
		{"the write fails", "application/json", `{"repository":"octo/widget"}`, errors.New("down"), http.StatusInternalServerError},
	}
	for _, action := range []string{"stop", "resume"} {
		for _, tt := range tests {
			t.Run(action+" "+tt.name, func(t *testing.T) {
				s := startableProjects()
				s.pauseErr = tt.err
				rec := postPause(newProjectsMux(s), action, tt.contentType, tt.body)
				if rec.Code != tt.want {
					t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
				}
				if len(s.pauses) != 0 {
					t.Errorf("set %v for a refused request", s.pauses)
				}
			})
		}
	}
}

func TestStopWithoutStoresIsUnavailable(t *testing.T) {
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger()})
	if rec := postPause(mux, "stop", "application/json", `{"repository":"octo/widget"}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestPauseIsUnavailableWithoutThePauseStore(t *testing.T) {
	s := startableProjects()
	mux := newMux(Deps{SPA: fstest.MapFS{}, Logger: discardLogger(), Projects: ProjectStores{
		Projects: s, Snapshots: s, Reaches: s, Loads: s, Activity: s, Starts: s,
	}})
	for _, action := range []string{"stop", "resume"} {
		rec := postPause(mux, action, "application/json", `{"repository":"octo/widget"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without a pause store = %d, want %d", action, rec.Code, http.StatusServiceUnavailable)
		}
	}
}
