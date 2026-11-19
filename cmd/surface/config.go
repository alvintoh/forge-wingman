package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// configResponse is the configuration endpoint's wire shape: the model each run
// phase starts on, the concurrency the dispatcher runs under, and every run
// model's peak window.
type configResponse struct {
	Phases      []configPhase     `json:"phases"`
	Concurrency configConcurrency `json:"concurrency"`
	// PeakWindows has the shape of the models GET /api/peak serves.
	PeakWindows []peakModel `json:"peak_windows"`
}

type configPhase struct {
	Phase string `json:"phase"`
	Model string `json:"model"`
}

type configConcurrency struct {
	// ReviewWIP is how many open agent PRs may await review before a run starts.
	ReviewWIP int `json:"review_wip"`
	// PlatformCap is the concurrent jobs the platform allows one account.
	PlatformCap int `json:"platform_cap"`
	// PollMinutes is the interval a scheduled tick waits after the last poll.
	PollMinutes int `json:"poll_minutes"`
}

// config serves GET /api/config. The concurrency settings are the dispatcher's
// own WINGMAN_ variables, which this service is deployed with too, and the poll
// interval is the owner's saved one; a nil Getenv reads the process environment.
func (d Deps) config(w http.ResponseWriter, r *http.Request) {
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	limits, _, err := dispatcher.ConcurrencySettings(getenv)
	if err != nil {
		d.Logger.Error("configNotRead", "err", err.Error())
		http.Error(w, "reading the configuration", http.StatusInternalServerError)
		return
	}
	limits = limits.OrDefault()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(configResponse{
		Phases: []configPhase{
			{Phase: "plan", Model: runner.DefaultPlanModel()},
			{Phase: "build", Model: runner.DefaultModel()},
			{Phase: "review", Model: runner.DefaultReviewModels()[0]},
		},
		Concurrency: configConcurrency{
			ReviewWIP:   limits.ReviewWIP,
			PlatformCap: limits.PlatformCap,
			PollMinutes: d.pollMinutes(r.Context()),
		},
		PeakWindows: peakModels(providers.RunModels()),
	})
}

// pollMinutes is the interval the dispatcher polls on: the owner's saved one,
// or the default it falls back to while there is no store or none can be read.
func (d Deps) pollMinutes(ctx context.Context) int {
	if d.PollInterval == nil {
		return dispatcher.DefaultPollMinutes
	}
	n, err := d.PollInterval.Value(ctx)
	if err != nil {
		d.Logger.Warn("pollIntervalNotRead", "err", err.Error(), "poll_minutes", dispatcher.DefaultPollMinutes)
		return dispatcher.DefaultPollMinutes
	}
	return n
}
