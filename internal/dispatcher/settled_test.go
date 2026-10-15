package dispatcher

import (
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
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
