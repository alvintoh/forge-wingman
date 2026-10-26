package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// RunsPerRepoStore reads and sets the owner's cap on the runs in flight in one
// repository, which the Config settings modal sets.
type RunsPerRepoStore interface {
	Value(ctx context.Context) (int, error)
	Set(ctx context.Context, n int, at time.Time) error
}

// runsPerRepoResponse is the runs-per-repo endpoint's wire shape.
type runsPerRepoResponse struct {
	RunsPerRepo int `json:"runs_per_repo"`
}

// writeRunsPerRepo answers with the cap.
func writeRunsPerRepo(w http.ResponseWriter, n int) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runsPerRepoResponse{RunsPerRepo: n})
}

// runsPerRepo serves GET /api/runs-per-repo: the saved cap, or the default
// while the owner has not saved one.
func (d Deps) runsPerRepo(w http.ResponseWriter, r *http.Request) {
	if d.RepoCap == nil {
		http.Error(w, "the runs per repo cap needs a store", http.StatusServiceUnavailable)
		return
	}
	n, err := d.RepoCap.Value(r.Context())
	if err != nil {
		d.Logger.Error("runsPerRepoNotRead", "err", err.Error())
		http.Error(w, "reading the runs per repo cap", http.StatusInternalServerError)
		return
	}
	writeRunsPerRepo(w, n)
}

// setRunsPerRepo serves POST /api/runs-per-repo. It is gated as the peak mode's
// write is: a JSON body names the setting and nothing else, which a page that is
// not the surface cannot send cross-origin without a preflight.
func (d Deps) setRunsPerRepo(w http.ResponseWriter, r *http.Request) {
	if d.RepoCap == nil {
		http.Error(w, "the runs per repo cap needs a store", http.StatusServiceUnavailable)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "the runs per repo cap is set with a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		RunsPerRepo *int `json:"runs_per_repo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RunsPerRepo == nil {
		http.Error(w, "the request body is not JSON naming the runs per repo cap", http.StatusBadRequest)
		return
	}
	n := *body.RunsPerRepo
	if n < dispatcher.MinRunsPerRepo || n > dispatcher.MaxRunsPerRepo {
		http.Error(w, fmt.Sprintf("runs per repo %d is not from %d to %d",
			n, dispatcher.MinRunsPerRepo, dispatcher.MaxRunsPerRepo), http.StatusBadRequest)
		return
	}
	if err := d.RepoCap.Set(r.Context(), n, time.Now()); err != nil {
		d.Logger.Error("runsPerRepoNotSet", "err", err.Error())
		http.Error(w, "setting the runs per repo cap", http.StatusInternalServerError)
		return
	}
	d.Logger.Info("runsPerRepoSet", "runs_per_repo", n)
	writeRunsPerRepo(w, n)
}
