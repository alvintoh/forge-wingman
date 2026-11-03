package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// ConcurrencyReader reads the peak in flight, the N admission holds and the held
// runs over the trailing window ending at now, from the stored run rows.
type ConcurrencyReader interface {
	ConcurrencyStats(ctx context.Context, now time.Time) (store.ConcurrencyStats, error)
}

// concurrencyResponse is the concurrency endpoint's wire shape.
type concurrencyResponse struct {
	// Peak is the most runs in flight at once in the trailing 30 days.
	Peak        int `json:"peak"`
	RunsAtOnce  int `json:"runs_at_once"`
	RunsPerRepo int `json:"runs_per_repo"`
	ReviewWIP   int `json:"review_wip"`
	PlatformCap int `json:"platform_cap"`
	// Held lists each reason with a hold in the window, largest first; empty when
	// no run was held.
	Held []heldReason `json:"held"`
}

type heldReason struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// concurrency serves GET /api/concurrency: the peak in flight beside each limit,
// and the held runs by reason. Every figure is pooled across projects, since the
// limits are the dispatcher's own. Review WIP and the platform cap are read as
// GET /api/config reads them, so the two pages agree.
func (d Deps) concurrency(w http.ResponseWriter, r *http.Request) {
	if d.Concurrency == nil || d.RepoCap == nil {
		http.Error(w, "the concurrency needs a run store", http.StatusServiceUnavailable)
		return
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	limits, _, err := dispatcher.ConcurrencySettings(getenv)
	if err != nil {
		d.Logger.Error("concurrencyNotRead", "err", err.Error())
		http.Error(w, "reading the concurrency limits", http.StatusInternalServerError)
		return
	}
	limits = limits.OrDefault()
	perRepo, err := d.RepoCap.Value(r.Context())
	if err != nil {
		d.Logger.Error("concurrencyNotRead", "err", err.Error())
		http.Error(w, "reading the runs per repo cap", http.StatusInternalServerError)
		return
	}
	stats, err := d.Concurrency.ConcurrencyStats(r.Context(), time.Now())
	if err != nil {
		d.Logger.Error("concurrencyNotRead", "err", err.Error())
		http.Error(w, "reading the concurrency", http.StatusInternalServerError)
		return
	}
	ranked := dispatcher.RankHolds(stats.Held)
	held := make([]heldReason, 0, len(ranked))
	for _, h := range ranked {
		held = append(held, heldReason{Reason: h.Reason, Count: h.Count})
	}
	writeJSON(w, http.StatusOK, concurrencyResponse{
		Peak:        stats.Peak,
		RunsAtOnce:  stats.N,
		RunsPerRepo: perRepo,
		ReviewWIP:   limits.ReviewWIP,
		PlatformCap: limits.PlatformCap,
		Held:        held,
	})
}
