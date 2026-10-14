package store

import (
	"cmp"
	"context"
	"slices"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	prov "github.com/alvintoh/forge-wingman/internal/providers"
)

// ProviderPlan is one plan as the providers view lists it: the owner's record of
// the plan and what its provider's own allowance windows hold spent, empty for a
// provider that reports no window.
type ProviderPlan struct {
	Provider string
	Price    money.Micros
	Verdict  prov.Verdict
	Windows  []WindowUsage
}

// WindowUsage is one of a plan's own allowance windows and what is spent in it.
type WindowUsage struct {
	Name  string
	Limit money.Micros
	Used  money.Micros
}

// ProviderPlans reads the providers view: every plan the owner has recorded,
// each with the spend against its provider's own allowance windows.
type ProviderPlans struct {
	plans *Plans
	queue *Queue
}

// NewProviderPlans returns a ProviderPlans backed by client.
func NewProviderPlans(client *firestore.Client) *ProviderPlans {
	return &ProviderPlans{plans: NewPlans(client), queue: NewQueue(client)}
}

// Providers lists every configured plan and every plan the owner has recorded,
// in the order a run walks the configuration, each with what its provider's own
// allowance windows hold spent at at. A configured plan with no record is listed
// unpriced and unconfirmed, its usage unreported.
func (p *ProviderPlans) Providers(ctx context.Context, at time.Time) ([]ProviderPlan, error) {
	rows, err := p.plans.List(ctx)
	if err != nil {
		return nil, err
	}
	order := prov.DispatchOrder()
	recorded := make(map[string]bool, len(rows))
	for _, row := range rows {
		recorded[row.Provider] = true
	}
	rows = withConfigured(rows, order)
	sortByDispatch(rows, order)
	out := make([]ProviderPlan, 0, len(rows))
	for _, row := range rows {
		view := ProviderPlan{
			Provider: row.Provider,
			Price:    row.Plan.Definition.MonthlyPrice,
			Verdict:  row.Plan.RecordedVerdict(),
		}
		windows := dispatcher.ProviderWindows(row.Provider)
		if recorded[row.Provider] && len(windows) > 0 {
			spend, err := p.queue.PlanWindows(ctx, row.Provider, windows, at)
			if err != nil {
				return nil, err
			}
			for i, window := range windows {
				view.Windows = append(view.Windows, WindowUsage{
					Name:  window.Name,
					Limit: window.Limit,
					Used:  spend[i],
				})
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// withConfigured adds a row, holding no record, for each plan of order that rows
// lacks.
func withConfigured(rows []prov.Listing, order []string) []prov.Listing {
	for _, plan := range order {
		if !slices.ContainsFunc(rows, func(r prov.Listing) bool { return r.Provider == plan }) {
			rows = append(rows, prov.Listing{Provider: plan})
		}
	}
	return rows
}

// sortByDispatch orders rows the way a run walks the plans of order: those it
// names, in its order, and any plan it does not name after them, keeping the
// store's own provider order among those.
func sortByDispatch(rows []prov.Listing, order []string) {
	rank := make(map[string]int, len(order))
	for i, plan := range order {
		rank[plan] = i
	}
	slices.SortStableFunc(rows, func(a, b prov.Listing) int {
		return cmp.Compare(planRank(rank, a.Provider), planRank(rank, b.Provider))
	})
}

// planRank is plan's place in the dispatch order, one past the last for a plan
// the order does not name, so those sort after every plan it does.
func planRank(rank map[string]int, plan string) int {
	if i, ok := rank[plan]; ok {
		return i
	}
	return len(rank)
}
