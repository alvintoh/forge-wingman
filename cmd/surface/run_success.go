package main

import (
	"context"
	"net/http"

	"github.com/alvintoh/forge-wingman/internal/store"
)

// RunSuccessReader reads the run success rate the endpoint serves over windows of
// size counted runs, from the runs' own rows.
type RunSuccessReader interface {
	RunSuccess(ctx context.Context, size int) (store.RunSuccess, error)
}

// runSuccess serves GET /api/run-success: the share of runs that passed over the
// last historyLimit counted runs (the strip's own count) and the same number
// before them, and the most common way runs failed. The checks result it counts
// is written once onto the run's row (adr/0022), so the read asks GitHub for
// nothing.
func (d Deps) runSuccess(w http.ResponseWriter, r *http.Request) {
	if d.RunSuccess == nil {
		http.Error(w, "the run success rate needs a run store", http.StatusServiceUnavailable)
		return
	}
	success, err := d.RunSuccess.RunSuccess(r.Context(), historyLimit)
	if err != nil {
		d.Logger.Error("runSuccessNotRead", "err", err.Error())
		http.Error(w, "reading the run success rate", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, renderRunSuccess(success))
}

// runSuccessResponse is the run success endpoint's wire shape.
type runSuccessResponse struct {
	// Recent is the newest window of counted runs, Previous the window before them.
	Recent   runSuccessWindow `json:"recent"`
	Previous runSuccessWindow `json:"previous"`
	// Failure is how most of the recent window's runs failed, and how many did;
	// omitted when none failed.
	Failure *runSuccessFailure `json:"failure,omitempty"`
}

// runSuccessWindow is one window as a view draws it: the runs that passed, the
// runs counted, and the rate between them.
type runSuccessWindow struct {
	Passed int `json:"passed"`
	Runs   int `json:"runs"`
	// Rate is Passed/Runs, null while the window holds fewer runs than a rate is
	// stated over. The count is always served.
	Rate *float64 `json:"rate"`
}

// runSuccessFailure is the most common failure class and its count.
type runSuccessFailure struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

// renderRunSuccess renders the store's rate as JSON. The rate is null rather than
// omitted, so a view reads "too few runs to say" from the key it plots.
func renderRunSuccess(s store.RunSuccess) runSuccessResponse {
	out := runSuccessResponse{
		Recent: runSuccessWindow{
			Passed: s.Recent.Passed, Runs: s.Recent.Runs, Rate: s.Recent.Rate,
		},
		Previous: runSuccessWindow{
			Passed: s.Previous.Passed, Runs: s.Previous.Runs, Rate: s.Previous.Rate,
		},
	}
	if s.Failure != nil {
		out.Failure = &runSuccessFailure{Class: s.Failure.Class, Count: s.Failure.Count}
	}
	return out
}
