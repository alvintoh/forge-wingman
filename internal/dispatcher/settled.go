package dispatcher

import (
	"context"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Settlement is one settled run as a poll's snapshot of the run history holds
// it: the stamps and totals the budget ceilings sum. A run that planned and
// never built carries PlanSettledAt and its plan spend with no settlement.
type Settlement struct {
	SettledAt     time.Time
	PlanSettledAt time.Time
	Plan          string
	Cost          money.Micros
	Minutes       int64
	PlanCost      money.Micros
	PlanMinutes   int64
}

// SettledHistory is the settled run history one poll reads once: every run
// whose settlement, or whose plan stage's settlement, falls inside the widest
// window the poll's budgets reach back over. Every candidate's ceilings are
// then summed from it in memory, so the history costs a poll one read however
// many candidates it walks (AC1).
type SettledHistory []Settlement

// HistoryLoader loads one poll's snapshot of the settled run history: a poll
// builds one loader and hands it to every claim it walks and to its delegation,
// so the history costs one read however many candidates are queued, and it is
// read only once a candidate reaches its budget ceilings, so a poll whose
// candidates a non-budget limit holds reads none of it (AC1, AC5). Nil means the
// claimant holds no snapshot and reads the history in its own transaction.
type HistoryLoader func() (SettledHistory, error)

// newHistoryLoader is the loader one poll shares: the first caller reads the
// settled history at since, every later one reuses that snapshot.
func newHistoryLoader(ctx context.Context, q Queue, since time.Time) HistoryLoader {
	var (
		history SettledHistory
		err     error
		read    bool
	)
	return func() (SettledHistory, error) {
		if !read {
			read = true
			history, err = q.SettledHistory(ctx, since)
		}
		return history, err
	}
}

// Sums is the settled spend cfg's ceilings measure at at for model's own plan:
// what the runs that ended inside each window settled for, plus the reserved
// estimate their plan stages booked, summed the way the store's own
// in-transaction scan sums them.
func (h SettledHistory) Sums(cfg BudgetConfig, model string, at time.Time) Settled {
	plan := runner.Provider(model)
	settled := Settled{Windows: make([]money.Micros, len(cfg.ProviderWindows))}
	for i, w := range cfg.ProviderWindows {
		settled.Windows[i] = h.windowSpend(w, plan, at)
	}
	if modelCap, ok := cfg.ModelCaps[model]; ok {
		settled.ModelCap = h.windowSpend(modelCap, plan, at)
	}
	cashSince := cfg.Cash.Since(at)
	settled.CashCost, settled.CashMinutes = h.settledSince(cashSince, "")
	planCost, planMinutes := h.planSince(cashSince, "")
	settled.CashCost += planCost
	settled.CashMinutes += planMinutes
	return settled
}

// windowSpend is what window holds at at for plan's own runs: what they settled
// for plus the reserved estimate of their plan stages, so a plan is counted
// against the ceiling it was checked against and never twice.
func (h SettledHistory) windowSpend(w Window, plan string, at time.Time) money.Micros {
	cost, _ := h.settledSince(w.Since(at), plan)
	planCost, _ := h.planSince(w.Since(at), plan)
	return cost + planCost
}

// settledSince sums the runs settled at or after since that are on plan.
func (h SettledHistory) settledSince(since time.Time, plan string) (money.Micros, int64) {
	var cost money.Micros
	var minutes int64
	for _, s := range h {
		if s.SettledAt.IsZero() || s.SettledAt.Before(since) || !s.onPlan(plan) {
			continue
		}
		cost += s.Cost
		minutes += s.Minutes
	}
	return cost, minutes
}

// planSince sums the plan stages settled at or after since that are on plan: a
// run that planned and never built counts here alone, which is what it cost.
func (h SettledHistory) planSince(since time.Time, plan string) (money.Micros, int64) {
	var cost money.Micros
	var minutes int64
	for _, s := range h {
		if s.PlanSettledAt.IsZero() || s.PlanSettledAt.Before(since) || !s.onPlan(plan) {
			continue
		}
		cost += s.PlanCost
		minutes += s.PlanMinutes
	}
	return cost, minutes
}

// onPlan reports whether the settlement's run is on plan, every run being on
// plan "". A run settled before plans were recorded is on the default model's
// plan, as the store reads it.
func (s Settlement) onPlan(plan string) bool {
	if plan == "" {
		return true
	}
	if s.Plan != "" {
		return s.Plan == plan
	}
	return plan == runner.Provider(runner.DefaultModel())
}

// historySince is the earliest moment the budgets a poll admits against reach
// back to: its own, every plan a run may move to, and the free tier's. A
// claim's ceilings are summed from the poll's one snapshot of the settled
// history, so that snapshot must cover every window any of them measures.
func (c Config) historySince(at time.Time) time.Time {
	budgets := make([]BudgetConfig, 0, len(c.NextPlans)+2)
	budgets = append(budgets, c.Budget)
	for _, p := range c.NextPlans {
		budgets = append(budgets, p.Budget)
	}
	budgets = append(budgets, c.Budget.lastResort())
	since := at
	for _, b := range budgets {
		for _, w := range b.ProviderWindows {
			if s := w.Since(at); s.Before(since) {
				since = s
			}
		}
		for _, w := range b.ModelCaps {
			if s := w.Since(at); s.Before(since) {
				since = s
			}
		}
		if s := b.Cash.Since(at); s.Before(since) {
			since = s
		}
	}
	return since
}
