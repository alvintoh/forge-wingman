package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/alvintoh/forge-wingman/internal/store"
)

// ProviderPlansReader reads what the providers view lists: every plan the owner
// has recorded, each with what its provider's own allowance windows hold spent.
// It has no write method, because the records behind it are written by the owner
// CLI alone (adr/0014).
type ProviderPlansReader interface {
	Providers(ctx context.Context, at time.Time) ([]store.ProviderPlan, error)
}

// providers serves GET /api/providers.
func (d Deps) providers(w http.ResponseWriter, r *http.Request) {
	if d.ProviderPlans == nil {
		http.Error(w, "the providers view needs a plan store", http.StatusServiceUnavailable)
		return
	}
	plans, err := d.ProviderPlans.Providers(r.Context(), time.Now())
	if err != nil {
		d.Logger.Error("providersNotRead", "err", err.Error())
		http.Error(w, "reading the plans", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(renderProviders(plans))
}

// providersResponse is the providers endpoint's wire shape: every plan the owner
// has recorded, in the order a run walks them. It carries no API key — a plan's
// key is a repository secret the runner reads directly and is never a field of
// the record — so nothing of one can reach a reader.
type providersResponse struct {
	Plans []providerPlan `json:"plans"`
}

type providerPlan struct {
	Provider string  `json:"provider"`
	PriceUSD float64 `json:"price_usd"`
	Verdict  string  `json:"verdict"`
	// WindowUsage is the plan's own allowance windows with what each holds
	// spent, or usageUnknown for a plan whose provider reports none.
	WindowUsage any `json:"window_usage"`
}

// windowUsage is one of a plan's own allowance windows and what is spent in it.
type windowUsage struct {
	Name     string  `json:"name"`
	LimitUSD float64 `json:"limit_usd"`
	UsedUSD  float64 `json:"used_usd"`
}

// usageUnknown is what the endpoint reports for a plan whose provider states no
// window: it has no allowance to have spent.
const usageUnknown = "unknown"

// renderProviders renders the store's plans as JSON, converting each exact
// micro-dollar figure to the dollars the endpoint displays.
func renderProviders(plans []store.ProviderPlan) providersResponse {
	out := providersResponse{Plans: make([]providerPlan, 0, len(plans))}
	for _, plan := range plans {
		entry := providerPlan{
			Provider: plan.Provider,
			PriceUSD: plan.Price.USD(),
			Verdict:  string(plan.Verdict),
		}
		entry.WindowUsage = usageUnknown
		if len(plan.Windows) > 0 {
			usage := make([]windowUsage, 0, len(plan.Windows))
			for _, w := range plan.Windows {
				usage = append(usage, windowUsage{
					Name:     w.Name,
					LimitUSD: w.Limit.USD(),
					UsedUSD:  w.Used.USD(),
				})
			}
			entry.WindowUsage = usage
		}
		out.Plans = append(out.Plans, entry)
	}
	return out
}
