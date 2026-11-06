package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// RunningReader lists the runs in flight: one entry per ledger reservation. It
// has no write method — the view reads what the claims already recorded.
type RunningReader interface {
	Running(ctx context.Context) ([]store.Running, error)
}

// ProgressReader reads the newest progress of each run in flight. It never
// fails: a run whose progress was not read is absent from the map.
type ProgressReader interface {
	Progress(ctx context.Context, runs []store.Running) map[progressKey]*store.Progress
}

// runningResponse is the running endpoint's wire shape: the runs in flight,
// oldest claim first. It carries no cost, write set or run body.
type runningResponse struct {
	Runs []runningRun `json:"runs"`
}

type runningRun struct {
	Ticket       string `json:"ticket"`
	Repo         string `json:"repo"`
	Stage        string `json:"stage"`
	ClaimedAt    string `json:"claimed_at"`
	Title        string `json:"title"`
	LimitMinutes int    `json:"limit_minutes"`
	// Progress is the run's newest progress report, null when none was read.
	Progress *runningProgress `json:"progress"`
}

// runningProgress is a run's newest progress report: its phase, empty where
// the report named none the runner knows, its step count and diff lines, and
// how long ago Cloud Storage created it.
type runningProgress struct {
	Phase      string `json:"phase"`
	Steps      int64  `json:"steps"`
	Lines      int64  `json:"lines"`
	AgeSeconds int64  `json:"age_seconds"`
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
	var progress map[progressKey]*store.Progress
	if d.Progress != nil {
		progress = d.Progress.Progress(r.Context(), runs)
	}
	writeJSON(w, http.StatusOK, renderRunning(runs, progress, time.Now()))
}

// renderRunning orders the runs oldest claim first and renders each as the view
// draws it, with its progress as of now. A tie on the claim time breaks on the
// ticket and then the stage, so every read answers in the same order.
func renderRunning(runs []store.Running, progress map[progressKey]*store.Progress, now time.Time) runningResponse {
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
		stage := dispatcher.PhaseStage(run.Stage)
		out.Runs = append(out.Runs, runningRun{
			Ticket:       run.Ticket,
			Repo:         run.Repo,
			Stage:        stage,
			ClaimedAt:    run.ClaimedAt.UTC().Format(time.RFC3339),
			Title:        run.Title,
			LimitMinutes: runner.RunLimitMinutes(stage),
			Progress:     renderProgress(progress[progressKeyOf(run)], now),
		})
	}
	return out
}

func renderProgress(p *store.Progress, now time.Time) *runningProgress {
	if p == nil {
		return nil
	}
	return &runningProgress{Phase: string(p.Phase), Steps: p.Steps, Lines: p.Lines,
		AgeSeconds: max(0, int64(now.Sub(p.Created)/time.Second))}
}
