package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// RunsAtOnceStore reads and sets the owner's N: how many runs may be in flight
// at once across every project.
type RunsAtOnceStore interface {
	Value(ctx context.Context) (int, error)
	Set(ctx context.Context, n int, at time.Time) error
}

// RunsAtOnceRecommender reads the N the concurrency rule recommends.
type RunsAtOnceRecommender interface {
	RecommendedRunsAtOnce(ctx context.Context) (store.RecommendedRunsAtOnce, error)
}

// runsAtOnceResponse is the runs-at-once endpoint's wire shape. Recommendation
// is null when it could not be read, which leaves N itself readable and
// settable.
type runsAtOnceResponse struct {
	RunsAtOnce     int                 `json:"runs_at_once"`
	PlatformCap    int                 `json:"platform_cap"`
	Recommendation *recommendationView `json:"recommendation"`
}

// recommendationView is the recommended N with the evidence its reason is
// worded from: the basis, the stable runs in a row at N, and the rule's numbers.
type recommendationView struct {
	N int `json:"n"`
	// Basis is none, steady, rise, slow or rate-limited.
	Basis       string  `json:"basis"`
	Stable      int     `json:"stable"`
	StableRuns  int     `json:"stable_runs"`
	RiseWithin  float64 `json:"rise_within"`
	HalveBeyond float64 `json:"halve_beyond"`
}

// basisNone is BasisNone on the wire, where an empty string would read as absent.
const basisNone = "none"

// recommendationAt is the recommendation beside the owner's n. One the ledger
// observed at another N, before the dispatcher adopted n, says nothing about n,
// so it is n with nothing observed yet.
func recommendationAt(n, platformCap int, rec store.RecommendedRunsAtOnce) recommendationView {
	view := recommendationView{
		N: n, Basis: basisNone,
		StableRuns: rec.Tuning.StableRuns, RiseWithin: rec.Tuning.RiseWithin, HalveBeyond: rec.Tuning.HalveBeyond,
	}
	if r := rec.Recommendation; rec.At == n && r.Basis != dispatcher.BasisNone {
		view.N, view.Basis, view.Stable = min(r.N, platformCap), r.Basis, r.Stable
	}
	return view
}

// platformCap is the configured platform cap, N's upper bound.
func (d Deps) platformCap() (int, error) {
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	limits, _, err := dispatcher.ConcurrencySettings(getenv)
	if err != nil {
		return 0, err
	}
	return limits.OrDefault().PlatformCap, nil
}

// writeRunsAtOnce answers with n held to the cap and the recommendation beside it.
func (d Deps) writeRunsAtOnce(w http.ResponseWriter, r *http.Request, n, platformCap int) {
	n = min(n, platformCap)
	out := runsAtOnceResponse{RunsAtOnce: n, PlatformCap: platformCap}
	if d.Recommend == nil {
		d.Logger.Warn("runsAtOnceRecommendationNotRead", "err", "no recommender")
	} else if rec, err := d.Recommend.RecommendedRunsAtOnce(r.Context()); err != nil {
		d.Logger.Warn("runsAtOnceRecommendationNotRead", "err", err.Error())
	} else {
		view := recommendationAt(n, platformCap, rec)
		out.Recommendation = &view
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// runsAtOnce serves GET /api/runs-at-once: the saved N, the default while the
// owner has not saved one, and the recommendation beside it.
func (d Deps) runsAtOnce(w http.ResponseWriter, r *http.Request) {
	if d.RunsAtOnce == nil {
		http.Error(w, "runs at once needs a store", http.StatusServiceUnavailable)
		return
	}
	platformCap, err := d.platformCap()
	if err != nil {
		d.Logger.Error("configNotRead", "err", err.Error())
		http.Error(w, "reading the configuration", http.StatusInternalServerError)
		return
	}
	n, err := d.RunsAtOnce.Value(r.Context())
	if err != nil {
		d.Logger.Error("runsAtOnceNotRead", "err", err.Error())
		http.Error(w, "reading runs at once", http.StatusInternalServerError)
		return
	}
	d.writeRunsAtOnce(w, r, n, platformCap)
}

// setRunsAtOnce serves POST /api/runs-at-once, gated as the runs-per-repo
// write is: a JSON body naming the setting and nothing else. N outside 1 to
// the platform cap is refused.
func (d Deps) setRunsAtOnce(w http.ResponseWriter, r *http.Request) {
	if d.RunsAtOnce == nil {
		http.Error(w, "runs at once needs a store", http.StatusServiceUnavailable)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "runs at once is set with a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		RunsAtOnce *int `json:"runs_at_once"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RunsAtOnce == nil {
		http.Error(w, "the request body is not JSON naming runs at once", http.StatusBadRequest)
		return
	}
	platformCap, err := d.platformCap()
	if err != nil {
		d.Logger.Error("configNotRead", "err", err.Error())
		http.Error(w, "reading the configuration", http.StatusInternalServerError)
		return
	}
	n := *body.RunsAtOnce
	if n < dispatcher.MinRunsAtOnce || n > platformCap {
		http.Error(w, fmt.Sprintf("runs at once %d is not from %d to %d", n, dispatcher.MinRunsAtOnce, platformCap),
			http.StatusBadRequest)
		return
	}
	if err := d.RunsAtOnce.Set(r.Context(), n, time.Now()); err != nil {
		d.Logger.Error("runsAtOnceNotSet", "err", err.Error())
		http.Error(w, "setting runs at once", http.StatusInternalServerError)
		return
	}
	d.Logger.Info("runsAtOnceSet", "runs_at_once", n)
	d.writeRunsAtOnce(w, r, n, platformCap)
}
