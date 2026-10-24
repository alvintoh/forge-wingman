package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// SettingsStore reads and sets the owner's Config settings: the cap on how many
// runs one repository may hold in flight, which every dispatcher poll reads.
// Like the auto-merge switch's, the write reaches the one document its store
// holds, and the service sits behind IAP, whose only authorised principal is
// the owner.
type SettingsStore interface {
	RunsPerRepo(ctx context.Context) (int, error)
	SetRunsPerRepo(ctx context.Context, n int, at time.Time) error
}

// settingsView is what the Config settings modal reads: the auto-merge switch
// and whether it could be read, the owner's runs-per-repo cap and the GitHub
// job limit the platform fixes. No field of it sets anything; the two settings
// are written through their own endpoints.
type settingsView struct {
	AutoMerge      bool `json:"auto_merge"`
	AutoMergeRead  bool `json:"auto_merge_read"`
	RunsPerRepo    int  `json:"runs_per_repo"`
	GitHubJobLimit int  `json:"github_job_limit"`
}

// settings serves GET /api/settings.
func (d Deps) settings(w http.ResponseWriter, r *http.Request) {
	if d.Settings == nil {
		http.Error(w, "the settings need a store", http.StatusServiceUnavailable)
		return
	}
	runs, err := d.Settings.RunsPerRepo(r.Context())
	if err != nil {
		// The dispatcher applies the default to a setting it cannot read, so
		// the modal shows the number the runs are actually held to.
		d.Logger.Warn("runsPerRepoUnread", "err", err.Error())
		runs = dispatcher.DefaultLimits.RepoCap
	}
	autoMerge, autoMergeRead := d.autoMergeSwitch(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(settingsView{
		AutoMerge:      autoMerge,
		AutoMergeRead:  autoMergeRead,
		RunsPerRepo:    runs,
		GitHubJobLimit: d.githubJobLimit(),
	})
}

// setRunsPerRepo serves POST /api/settings/runs-per-repo: a JSON body names the
// cap and nothing else, and a value outside the range the surface offers is
// refused before the store sees it.
func (d Deps) setRunsPerRepo(w http.ResponseWriter, r *http.Request) {
	if d.Settings == nil {
		http.Error(w, "the runs-per-repo setting needs a store", http.StatusServiceUnavailable)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "the runs-per-repo setting is set with a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		RunsPerRepo *int `json:"runs_per_repo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RunsPerRepo == nil {
		http.Error(w, "the request body is not JSON naming a runs-per-repo cap", http.StatusBadRequest)
		return
	}
	if !dispatcher.ValidRunsPerRepo(*body.RunsPerRepo) {
		http.Error(w, fmt.Sprintf("the runs-per-repo cap must be a whole number from %d to %d",
			dispatcher.MinRunsPerRepo, dispatcher.MaxRunsPerRepo), http.StatusBadRequest)
		return
	}
	if err := d.Settings.SetRunsPerRepo(r.Context(), *body.RunsPerRepo, time.Now()); err != nil {
		d.Logger.Error("runsPerRepoNotSet", "err", err.Error())
		http.Error(w, "setting the runs-per-repo cap", http.StatusInternalServerError)
		return
	}
	d.Logger.Info("runsPerRepoSet", "runs_per_repo", *body.RunsPerRepo)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		RunsPerRepo int `json:"runs_per_repo"`
	}{*body.RunsPerRepo})
}

// githubJobLimit is the GitHub job limit the modal shows, which is the platform
// cap the dispatcher holds runs to: 20 unless the deployment configures another.
func (d Deps) githubJobLimit() int {
	if d.PlatformCap > 0 {
		return d.PlatformCap
	}
	return dispatcher.DefaultLimits.PlatformCap
}

// platformCap reads WINGMAN_PLATFORM_CAP, the same variable the dispatcher
// applies, so the modal shows the limit runs are actually held to. An unset or
// unreadable value is the default.
func platformCap(getenv func(string) string) int {
	n, err := strconv.Atoi(strings.TrimSpace(getenv("WINGMAN_PLATFORM_CAP")))
	if err != nil || n < 1 {
		return dispatcher.DefaultLimits.PlatformCap
	}
	return n
}
