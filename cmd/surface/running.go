package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// RunningReader lists the runs in flight: one entry per ledger reservation. It
// has no write method — the view reads what the claims already recorded.
type RunningReader interface {
	Running(ctx context.Context) ([]store.Running, error)
}

// runningResponse is the running endpoint's wire shape: the runs in flight,
// oldest claim first. It carries no cost, write set or run body.
type runningResponse struct {
	Runs []runningRun `json:"runs"`
}

type runningRun struct {
	Ticket    string `json:"ticket"`
	Repo      string `json:"repo"`
	Stage     string `json:"stage"`
	ClaimedAt string `json:"claimed_at"`
	Title     string `json:"title"`
}

// running serves GET /api/running: the runs the dispatcher has claimed and not
// yet settled.
func (d Deps) running(w http.ResponseWriter, r *http.Request) {
	if d.Running == nil {
		http.Error(w, "the running view needs a run store", http.StatusServiceUnavailable)
		return
	}
	runs, err := d.Running.Running(r.Context())
	if err != nil {
		d.Logger.Error("runningNotRead", "err", err.Error())
		http.Error(w, "reading the runs in flight", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, renderRunning(runs))
}

// renderRunning orders the runs oldest claim first and renders each as the view
// draws it. A tie on the claim time breaks on the ticket and then the stage, so
// every read answers in the same order.
func renderRunning(runs []store.Running) runningResponse {
	sorted := slices.Clone(runs)
	slices.SortFunc(sorted, func(a, b store.Running) int {
		if c := a.ClaimedAt.Compare(b.ClaimedAt); c != 0 {
			return c
		}
		if c := strings.Compare(a.Ticket, b.Ticket); c != 0 {
			return c
		}
		return strings.Compare(a.Stage, b.Stage)
	})
	out := runningResponse{Runs: make([]runningRun, 0, len(sorted))}
	for _, run := range sorted {
		out.Runs = append(out.Runs, runningRun{
			Ticket:    run.Ticket,
			Repo:      run.Repo,
			Stage:     dispatcher.PhaseStage(run.Stage),
			ClaimedAt: run.ClaimedAt.UTC().Format(time.RFC3339),
			Title:     run.Title,
		})
	}
	return out
}
