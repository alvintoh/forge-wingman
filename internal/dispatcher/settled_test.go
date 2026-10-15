package dispatcher

import (
	"reflect"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// The poll's horizon reaches back to the oldest window of every budget it may
// admit a run against — its own, each plan a run may move to, and the free
// tier's — so one snapshot of the settled history covers every ceiling any
// candidate is summed against, and none is summed from a window it never read.
func TestTheHistoryHorizonCoversEveryWindowTheBudgetsMeasure(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	wider := 56 * 24 * time.Hour
	c := Config{
		Budget: BudgetConfig{
			ProviderWindows: []Window{{Name: "5h", Period: 5 * time.Hour, Limit: 10 * money.Dollar}},
			Cash:            Window{Name: CeilingCash, Calendar: true, Limit: 30 * money.Dollar},
		},
		NextPlans: []PaidPlan{{Plan: "other", Budget: BudgetConfig{
			ProviderWindows: []Window{{Name: "8w", Period: wider, Limit: 100 * money.Dollar}},
		}}},
	}
	want := at.Add(-wider)
	if got := c.historySince(at); !got.Equal(want) {
		t.Fatalf("the poll's horizon is %v, want %v, the widest window any of its budgets measures", got, want)
	}
}

// Each ceiling is summed from the poll's one snapshot of the history, counting
// a run on the model's own plan once for what it settled and once for what its
// plan stage reserved, and a run recorded before plans were kept as one on the
// default plan.
func TestSumsCountOnlyTheRunsOnTheModelsPlanInsideEachWindow(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	model := runner.DefaultModel()
	plan := runner.Provider(model)
	cfg := BudgetConfig{
		ProviderWindows: []Window{{Name: "5h", Period: 5 * time.Hour}},
		ModelCaps:       map[string]Window{model: {Name: "day", Period: 24 * time.Hour}},
		Cash:            Window{Name: CeilingCash, Period: 2 * time.Hour},
	}
	hourAgo, anHourAndAHalfAgo, twentyHoursAgo := at.Add(-time.Hour), at.Add(-90*time.Minute), at.Add(-20*time.Hour)
	h := SettledHistory{
		{SettledAt: hourAgo, PlanSettledAt: anHourAndAHalfAgo, Plan: plan, Cost: money.Dollar, Minutes: 10, PlanCost: 16 * money.Dollar, PlanMinutes: 100},
		{SettledAt: hourAgo, PlanSettledAt: hourAgo, Plan: "other", Cost: 2 * money.Dollar, Minutes: 20, PlanCost: 256 * money.Dollar, PlanMinutes: 2000},
		{SettledAt: hourAgo, Cost: 4 * money.Dollar, Minutes: 40},
		{PlanSettledAt: hourAgo, PlanCost: 8 * money.Dollar, PlanMinutes: 200},
		{SettledAt: twentyHoursAgo, PlanSettledAt: twentyHoursAgo, Plan: plan, Cost: 32 * money.Dollar, Minutes: 400, PlanCost: 128 * money.Dollar, PlanMinutes: 4000},
		{SettledAt: at, Plan: plan, Cost: 64 * money.Dollar, Minutes: 800},
	}

	got := h.Sums(cfg, model, at)
	want := Settled{
		Windows:     []money.Micros{93 * money.Dollar},
		ModelCap:    253 * money.Dollar,
		CashCost:    351 * money.Dollar,
		CashMinutes: 3170,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sums on the default plan = %+v, want %+v", got, want)
	}

	other := h.Sums(cfg, "other/model", at)
	if !reflect.DeepEqual(other.Windows, []money.Micros{258 * money.Dollar}) || other.ModelCap != 0 {
		t.Fatalf("sums on another plan = %+v, want only that plan's own runs and no model cap", other)
	}

	uncapped := cfg
	uncapped.ModelCaps = nil
	if got := h.Sums(uncapped, model, at); got.ModelCap != 0 {
		t.Fatalf("a model with no cap sums %v against one, want none", got.ModelCap)
	}
}

func TestTheHistoryHorizonReachesTheWidestWindowOfAnyKind(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		budget BudgetConfig
		want   time.Time
	}{
		"a provider window wider than a later one": {
			budget: BudgetConfig{ProviderWindows: []Window{{Period: 72 * time.Hour}, {Period: 24 * time.Hour}}},
			want:   at.Add(-72 * time.Hour),
		},
		"a model cap wider than the provider windows": {
			budget: BudgetConfig{ProviderWindows: []Window{{Period: 5 * time.Hour}}, ModelCaps: map[string]Window{"m": {Period: 72 * time.Hour}}},
			want:   at.Add(-72 * time.Hour),
		},
		"a model cap narrower than the provider windows": {
			budget: BudgetConfig{ProviderWindows: []Window{{Period: 72 * time.Hour}}, ModelCaps: map[string]Window{"m": {Period: 24 * time.Hour}}},
			want:   at.Add(-72 * time.Hour),
		},
		"the calendar month of the cash ceiling": {
			budget: BudgetConfig{ProviderWindows: []Window{{Period: 5 * time.Hour}}, Cash: Window{Calendar: true}},
			want:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := (Config{Budget: tc.budget}).historySince(at); !got.Equal(tc.want) {
				t.Fatalf("the poll's horizon is %v, want %v", got, tc.want)
			}
		})
	}
}
