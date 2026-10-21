package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// maxPhaseName bounds the phase a start request names, which is stored as sent.
const maxPhaseName = 200

// ProjectLister reads the owner's listed projects.
type ProjectLister interface {
	Listed(ctx context.Context) ([]dispatcher.PhaseProject, error)
}

// SnapshotReader reads the phase snapshots the dispatcher writes, keyed by
// lower-cased repository. The surface holds no Linear credential: Linear is
// read by the dispatcher alone.
type SnapshotReader interface {
	Snapshots(ctx context.Context) (map[string]dispatcher.PhaseSnapshot, error)
}

// ReachReader reads each repository's last read, keyed by lower-cased repository.
type ReachReader interface {
	Reads(ctx context.Context) (map[string]dispatcher.RepoReach, error)
}

// LoadReader reads each repository's queue load, keyed by lower-cased repository.
type LoadReader interface {
	RepoLoads(ctx context.Context) (map[string]store.RepoLoad, error)
}

// ActivityReader reads when a repository's newest run row was updated.
type ActivityReader interface {
	LastActivity(ctx context.Context, repository string) (time.Time, error)
}

// StartRequester records the owner's request to start a project's phase.
type StartRequester interface {
	RequestStart(ctx context.Context, repository, phase string, at time.Time) error
}

// ProjectStores are the stores behind the projects endpoints; every one is
// needed, and a surface without them answers 503.
type ProjectStores struct {
	Projects  ProjectLister
	Snapshots SnapshotReader
	Reaches   ReachReader
	Loads     LoadReader
	Activity  ActivityReader
	Starts    StartRequester
}

func (p ProjectStores) ready() bool {
	return p.Projects != nil && p.Snapshots != nil && p.Reaches != nil && p.Loads != nil && p.Activity != nil && p.Starts != nil
}

type projectReach struct {
	Status    dispatcher.ReachStatus `json:"status"`
	Reason    dispatcher.ReachReason `json:"reason,omitempty"`
	CheckedAt time.Time              `json:"checked_at,omitzero"`
}

type projectQueue struct {
	InFlight int `json:"in_flight"`
	Held     int `json:"held"`
}

type projectEntry struct {
	Repository    string                    `json:"repository"`
	LinearProject string                    `json:"linear_project"`
	AddedAt       time.Time                 `json:"added_at"`
	Paused        bool                      `json:"paused"`
	Reach         projectReach              `json:"reach"`
	Snapshot      *dispatcher.PhaseSnapshot `json:"snapshot"`
	Queue         projectQueue              `json:"queue"`
	LastActivity  time.Time                 `json:"last_activity,omitzero"`
	// SortKey is the later of the last activity and the time the project was added.
	SortKey          time.Time `json:"sort_key"`
	StartPhase       string    `json:"start_phase,omitempty"`
	StartRequestedAt time.Time `json:"start_requested_at,omitzero"`
	StartRefusal     string    `json:"start_refusal,omitempty"`
}

// projectsResponse lists every project, newest first by sort key; the view takes
// the first three and offers the Total behind "See all".
type projectsResponse struct {
	Total    int            `json:"total"`
	Projects []projectEntry `json:"projects"`
}

// projects serves GET /api/projects.
func (d Deps) projects(w http.ResponseWriter, r *http.Request) {
	if !d.Projects.ready() {
		http.Error(w, "the projects view needs its stores", http.StatusServiceUnavailable)
		return
	}
	out, err := d.readProjects(r.Context())
	if err != nil {
		d.Logger.Error("projectsNotRead", "err", err.Error())
		http.Error(w, "reading the projects", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (d Deps) readProjects(ctx context.Context) (projectsResponse, error) {
	listed, err := d.Projects.Projects.Listed(ctx)
	if err != nil {
		return projectsResponse{}, err
	}
	snapshots, err := d.Projects.Snapshots.Snapshots(ctx)
	if err != nil {
		return projectsResponse{}, err
	}
	reaches, err := d.Projects.Reaches.Reads(ctx)
	if err != nil {
		return projectsResponse{}, err
	}
	loads, err := d.Projects.Loads.RepoLoads(ctx)
	if err != nil {
		return projectsResponse{}, err
	}
	out := projectsResponse{Total: len(listed), Projects: make([]projectEntry, 0, len(listed))}
	for _, p := range listed {
		key := strings.ToLower(p.Repository)
		activity, err := d.Projects.Activity.LastActivity(ctx, p.Repository)
		if err != nil {
			return projectsResponse{}, err
		}
		entry := projectEntry{
			Repository: p.Repository, LinearProject: p.LinearProject, AddedAt: p.AddedAt, Paused: p.Paused,
			Reach: reachOf(reaches, key, p.AddedAt), LastActivity: activity,
			Queue:      projectQueue{InFlight: loads[key].InFlight, Held: loads[key].Held},
			SortKey:    later(activity, p.AddedAt),
			StartPhase: p.StartPhase, StartRequestedAt: p.StartRequestedAt, StartRefusal: p.StartRefusal,
		}
		if snap, ok := snapshots[key]; ok {
			entry.Snapshot = &snap
		}
		out.Projects = append(out.Projects, entry)
	}
	slices.SortStableFunc(out.Projects, func(a, b projectEntry) int { return b.SortKey.Compare(a.SortKey) })
	return out, nil
}

// reachOf reads the record kept for key against when the project was added.
func reachOf(reaches map[string]dispatcher.RepoReach, key string, addedAt time.Time) projectReach {
	var record *dispatcher.RepoReach
	if r, ok := reaches[key]; ok {
		record = &r
	}
	out := projectReach{Status: dispatcher.ReachState(record, addedAt)}
	if record != nil {
		out.Reason, out.CheckedAt = record.Reason, record.CheckedAt
	}
	return out
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// startProject serves POST /api/projects/start. It records a request and
// delegates nothing: the dispatcher re-reads Linear on its next poll and
// delegates only if the phase is still the one named here. Like the other writes
// it requires a JSON body, which a page that is not the surface cannot send
// cross-origin without a preflight.
func (d Deps) startProject(w http.ResponseWriter, r *http.Request) {
	if !d.Projects.ready() {
		http.Error(w, "the projects view needs its stores", http.StatusServiceUnavailable)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "a phase is started with a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		Repository string `json:"repository"`
		Phase      string `json:"phase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !store.ValidRepository(body.Repository) ||
		strings.TrimSpace(body.Phase) == "" || len(body.Phase) > maxPhaseName {
		http.Error(w, "the request body is not JSON naming a repository and a phase", http.StatusBadRequest)
		return
	}
	code, msg, err := d.checkStart(r.Context(), body.Repository, body.Phase)
	switch {
	case err != nil:
		d.Logger.Error("projectStartNotChecked", "repo", body.Repository, "err", err.Error())
		http.Error(w, "checking the project", http.StatusInternalServerError)
		return
	case code != 0:
		http.Error(w, msg, code)
		return
	}
	switch err := d.Projects.Starts.RequestStart(r.Context(), body.Repository, body.Phase, time.Now()); {
	case errors.Is(err, store.ErrRepositoryNotListed):
		http.Error(w, "no listed project names that repository", http.StatusNotFound)
		return
	case err != nil:
		d.Logger.Error("projectStartNotRequested", "repo", body.Repository, "err", err.Error())
		http.Error(w, "requesting the start", http.StatusInternalServerError)
		return
	}
	d.Logger.Info("projectStartRequested", "repo", body.Repository, "phase", body.Phase)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(struct {
		Repository string `json:"repository"`
		Phase      string `json:"phase"`
	}{body.Repository, body.Phase})
}

// checkStart is the status and message refusing a start, or zero when it may be
// requested: the project must be listed (404), its repository readable and the
// phase still the snapshot's current one (409).
func (d Deps) checkStart(ctx context.Context, repository, phase string) (int, string, error) {
	listed, err := d.Projects.Projects.Listed(ctx)
	if err != nil {
		return 0, "", err
	}
	i := slices.IndexFunc(listed, func(p dispatcher.PhaseProject) bool { return strings.EqualFold(p.Repository, repository) })
	if i < 0 {
		return http.StatusNotFound, "no listed project names that repository", nil
	}
	key := strings.ToLower(repository)
	reaches, err := d.Projects.Reaches.Reads(ctx)
	if err != nil {
		return 0, "", err
	}
	if reach := reachOf(reaches, key, listed[i].AddedAt); reach.Status != dispatcher.ReachReachable {
		if reach.Status == dispatcher.ReachChecking {
			return http.StatusConflict, "the repository has not been checked yet", nil
		}
		return http.StatusConflict, "the dispatcher cannot read the repository", nil
	}
	snapshots, err := d.Projects.Snapshots.Snapshots(ctx)
	if err != nil {
		return 0, "", err
	}
	switch snap, ok := snapshots[key]; {
	case !ok || snap.Unreadable:
		return http.StatusConflict, "the project's phases have not been read", nil
	case snap.Current != phase:
		return http.StatusConflict, "the current phase is no longer the one named", nil
	}
	return 0, "", nil
}
