package dispatcher

import (
	"testing"

	"github.com/alvintoh/forge-wingman/internal/money"
)

// windowed is a provider allowance over three rolling windows — $12/5h,
// $30/week, $60/month — checked instead of the $20 cash ceiling for provider
// cost, plus GitHub's 2,000 free runner-minutes/month with no payment method
// on file (a hard stop past it).
var windowed = BudgetConfig{
	ProviderWindows: []Window{
		{Name: "5h", Limit: 12 * money.Dollar},
		{Name: "week", Limit: 30 * money.Dollar},
		{Name: "month", Limit: 60 * money.Dollar},
	},
	Cash:   Window{Name: "cash", Calendar: true, Limit: 20 * money.Dollar},
	Runner: RunnerMinutes{FreeMinutes: 2000},
}

func TestDecideAdmitsARunThatFitsEveryCeiling(t *testing.T) {
	fits, binding := Decide(windowed, Totals{}, []money.Micros{0, 0, 0}, 0, 0,
		Reservation{ProviderCost: 5 * money.Dollar, RunnerMinutes: 100})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q", fits, binding)
	}
}

func TestDecideDefersOnTheFirstProviderWindowItWouldBreach(t *testing.T) {
	for _, tt := range []struct {
		name          string
		windowSettled []money.Micros
		want          string
	}{
		{"the 5-hour window", []money.Micros{8 * money.Dollar, 0, 0}, "5h"},
		{"the week window, with the 5h window still fitting", []money.Micros{0, 26 * money.Dollar, 0}, "week"},
		{"the month window, with 5h and week still fitting", []money.Micros{0, 0, 56 * money.Dollar}, "month"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fits, binding := Decide(windowed, Totals{}, tt.windowSettled, 0, 0,
				Reservation{ProviderCost: 5 * money.Dollar})
			if fits || binding != tt.want {
				t.Fatalf("fits = %v, binding = %q, want deferred on %q", fits, binding, tt.want)
			}
		})
	}
}

func TestDecideCountsInFlightReservationsTowardEveryWindow(t *testing.T) {
	// Nothing settled yet, but another run's reservation alone already fills
	// the 5-hour window: the ledger's in-flight total must be counted, not
	// only what has settled.
	fits, binding := Decide(windowed, Totals{ProviderCost: 11 * money.Dollar}, []money.Micros{0, 0, 0}, 0, 0,
		Reservation{ProviderCost: 2 * money.Dollar})
	if fits || binding != "5h" {
		t.Fatalf("fits = %v, binding = %q, want deferred on 5h", fits, binding)
	}
}

func TestDecideZeroesRunnerMinutesForAPublicTarget(t *testing.T) {
	// The estimate holds a large minute figure, but the caller is expected to
	// zero it for a public target before calling Decide; with it zeroed, a
	// month's worth of minutes settled elsewhere still fits.
	fits, binding := Decide(windowed, Totals{}, []money.Micros{0, 0, 0}, 0, 1900,
		Reservation{ProviderCost: money.Dollar, RunnerMinutes: 0})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want a public target's zeroed minutes to fit", fits, binding)
	}
}

// TestDecideNeverBlocksAPublicTargetOnAnAlreadyExhaustedRunnerCeiling is the
// regression test for a candidate contributing zero minutes being blocked by
// an overage entirely caused by OTHER, private-target runs this month — a
// public target's minutes count as zero, so it can never newly breach a
// ceiling it never touches, however exhausted that ceiling already is.
func TestDecideNeverBlocksAPublicTargetOnAnAlreadyExhaustedRunnerCeiling(t *testing.T) {
	fits, binding := Decide(windowed, Totals{}, []money.Micros{0, 0, 0}, 0, 2500,
		Reservation{ProviderCost: money.Dollar, RunnerMinutes: 0})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want a zero-minute candidate to fit despite the ceiling already being exhausted", fits, binding)
	}
}

func TestDecideHardStopsOnRunnerMinutesWithNoPaymentMethodConfigured(t *testing.T) {
	// A private target pushing total minutes past the free tier, with
	// RatePerMinute left at its zero default, must defer rather than spend.
	fits, binding := Decide(windowed, Totals{}, []money.Micros{0, 0, 0}, 0, 1950,
		Reservation{ProviderCost: money.Dollar, RunnerMinutes: 100})
	if fits || binding != CeilingRunnerMinutes {
		t.Fatalf("fits = %v, binding = %q, want deferred on %q", fits, binding, CeilingRunnerMinutes)
	}
}

func TestDecideConvertsRunnerMinutesToCashWhenARateIsConfigured(t *testing.T) {
	cfg := windowed
	cfg.Runner.RatePerMinute = 6000 // $0.006/minute
	cfg.Cash.Limit = money.Dollar   // a tight cash ceiling so the overage alone breaches it

	fits, binding := Decide(cfg, Totals{}, []money.Micros{0, 0, 0}, 0, 1990,
		Reservation{ProviderCost: 0, RunnerMinutes: 300}) // 290 minutes over the 2,000 free, at $0.006 = $1.74
	if fits || binding != CeilingCash {
		t.Fatalf("fits = %v, binding = %q, want deferred on %q", fits, binding, CeilingCash)
	}

	cfg.Cash.Limit = money.Dollar * 10
	fits, binding = Decide(cfg, Totals{}, []money.Micros{0, 0, 0}, 0, 1990,
		Reservation{ProviderCost: 0, RunnerMinutes: 300})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want the small overage to fit a looser cash ceiling", fits, binding)
	}
}

func TestDecideChecksAPerTokenProvidersCostAgainstCashDirectly(t *testing.T) {
	// No provider windows configured: the provider's own cost is what Cash
	// exists to bound, so it counts even with no runner minutes at all.
	cfg := BudgetConfig{Cash: Window{Name: "cash", Calendar: true, Limit: 20 * money.Dollar}, Runner: RunnerMinutes{FreeMinutes: 2000}}
	fits, binding := Decide(cfg, Totals{}, nil, 15*money.Dollar, 0, Reservation{ProviderCost: 6 * money.Dollar})
	if fits || binding != CeilingCash {
		t.Fatalf("fits = %v, binding = %q, want deferred on %q", fits, binding, CeilingCash)
	}
}

func TestDecideNeverChecksAWindowedProvidersCostAgainstCashToo(t *testing.T) {
	// NFR-1: the allowance windows are checked INSTEAD OF the cash ceiling for
	// provider cost, never in addition — so an enormous provider-cost
	// estimate that still fits every window must not be re-checked against a
	// tight cash ceiling.
	cfg := windowed
	cfg.Cash.Limit = money.Dollar // would fail instantly if provider cost were double-counted
	fits, binding := Decide(cfg, Totals{}, []money.Micros{0, 0, 0}, 0, 0, Reservation{ProviderCost: 5 * money.Dollar})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want a windowed provider's cost to bypass cash", fits, binding)
	}
}
