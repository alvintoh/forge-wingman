package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// ComparisonReader reads the comparison of past runs the endpoint reports.
type ComparisonReader interface {
	Comparison(ctx context.Context, since time.Time) (store.Comparison, error)
}

// comparison serves GET /api/comparison.
func (d Deps) comparison(w http.ResponseWriter, r *http.Request) {
	if d.Comparison == nil {
		http.Error(w, "the comparison needs a run store", http.StatusServiceUnavailable)
		return
	}
	cmp, err := d.Comparison.Comparison(r.Context(), time.Now().Add(-runner.TrailingWindow))
	if err != nil {
		d.Logger.Error("comparisonNotRead", "err", err.Error())
		http.Error(w, "reading the comparison", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(renderComparison(cmp))
}

// comparisonResponse is the comparison endpoint's wire shape. It carries no
// ticket text: the run records it reads hold ticket titles and bodies, and what
// gates this binary today is IAP rather than the endpoint itself, so neither is
// served from here. Only the inbox serves a title, for a stopped run, and no
// endpoint serves a body.
type comparisonResponse struct {
	Models    []modelComparison    `json:"models"`
	Providers []providerComparison `json:"providers"`
	Runs      []runCache           `json:"runs"`
	Drifted   []driftedRun         `json:"drifted_runs"`
}

type modelComparison struct {
	Model string `json:"model"`
	// Tickets is how many runs built on this model and Passed how many of
	// them succeeded, so the rate is Passed/Tickets.
	Tickets          int     `json:"tickets"`
	Passed           int     `json:"passed"`
	CostPerTicketUSD float64 `json:"cost_per_ticket_usd"`
	// MinutesPerTicket is the mean wall-clock minutes of those runs, from the
	// build's start to the run's settlement.
	MinutesPerTicket float64 `json:"minutes_per_ticket"`
	// RewriteRate is the model's rewrite rate as the view prints it — a
	// percentage, or the words for a rate measured over none of the agent's
	// lines — and RewriteSample how many of its runs a merge measured.
	RewriteRate   string `json:"rewrite_rate"`
	RewriteSample int    `json:"rewrite_sample"`
}

type providerComparison struct {
	Provider                 string  `json:"provider"`
	TrailingCostPerTicketUSD float64 `json:"trailing_cost_per_ticket_usd"`
	Sample                   int     `json:"sample"`
	PeakTickets              int     `json:"peak_tickets"`
	PeakCostUSD              float64 `json:"peak_cost_usd"`
	OffPeakTickets           int     `json:"offpeak_tickets"`
	OffPeakCostUSD           float64 `json:"offpeak_cost_usd"`
}

// runCache is one run's own prompt-cache share: the share of its input tokens
// that came from the provider's cache, and each phase's own share as the run
// view lists it, both as the strings a view prints and as the fractions behind
// them.
type runCache struct {
	RunID       string  `json:"run_id"`
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	CachedShare float64 `json:"cached_share"`
	// BuildCache and PlanCache are the phase's own figures as the view prints
	// them: the share of its first request and of its whole input, or the word
	// that stands in for a share the run recorded none of.
	BuildCache string `json:"build_cache"`
	PlanCache  string `json:"plan_cache"`
	// The shares are the fractions behind those two strings, each between 0 and
	// 1, and null wherever the string is a word. A share of zero is a
	// measurement, so the keys are never omitted.
	BuildFirstShare *float64 `json:"build_first_share"`
	BuildPhaseShare *float64 `json:"build_phase_share"`
	PlanFirstShare  *float64 `json:"plan_first_share"`
	PlanPhaseShare  *float64 `json:"plan_phase_share"`
}

type driftedRun struct {
	RunID     string  `json:"run_id"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	CostUSD   float64 `json:"cost_usd"`
	CostDrift string  `json:"cost_drift"`
}

// renderComparison renders the store's comparison as JSON, converting its
// exact micro-dollar costs to the dollar figures the endpoint displays. Every
// list is rendered whether or not it holds anything, so a view narrowing to one
// of them reads an empty list rather than a null.
func renderComparison(c store.Comparison) comparisonResponse {
	out := comparisonResponse{
		Models:    []modelComparison{},
		Providers: []providerComparison{},
		Runs:      []runCache{},
		Drifted:   []driftedRun{},
	}
	for _, m := range c.Models {
		// A model no merge has measured has a zero rate, which would print as a
		// clean 0.0%.
		rate := "not measured"
		if m.RewriteSample > 0 {
			rate = rateText(m.RewriteRate)
		}
		out.Models = append(out.Models, modelComparison{
			Model: m.Model, Tickets: m.Tickets, Passed: m.Passed, RewriteSample: m.RewriteSample,
			CostPerTicketUSD: m.CostPerTicket.USD(), MinutesPerTicket: m.MinutesPerTicket,
			RewriteRate: rate,
		})
	}
	for _, p := range c.Providers {
		out.Providers = append(out.Providers, providerComparison{
			Provider: p.Provider, TrailingCostPerTicketUSD: p.TrailingCostPerTicket.USD(), Sample: p.Sample,
			PeakTickets: p.PeakTickets, PeakCostUSD: p.PeakCost.USD(),
			OffPeakTickets: p.OffPeakTickets, OffPeakCostUSD: p.OffPeakCost.USD(),
		})
	}
	for _, r := range c.Runs {
		out.Runs = append(out.Runs, runCache{
			RunID: r.RunID, Provider: r.Provider, Model: r.Model, CachedShare: r.CachedShare,
			BuildCache: r.BuildCache, PlanCache: r.PlanCache,
			BuildFirstShare: r.BuildFirstShare, BuildPhaseShare: r.BuildPhaseShare,
			PlanFirstShare: r.PlanFirstShare, PlanPhaseShare: r.PlanPhaseShare,
		})
	}
	for _, d := range c.Drifted {
		out.Drifted = append(out.Drifted, driftedRun{
			RunID: d.RunID, Provider: d.Provider, Model: d.Model,
			CostUSD: d.Cost.USD(), CostDrift: d.CostDrift,
		})
	}
	return out
}
