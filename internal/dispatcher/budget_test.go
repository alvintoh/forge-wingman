package dispatcher

import (
	"testing"
	"time"

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

// settledWindows pairs a settled provider cost with each of windowed's windows.
func settledWindows(costs ...money.Micros) Settled { return Settled{Windows: costs} }

func TestDecideAdmitsARunThatFitsEveryCeiling(t *testing.T) {
	fits, binding := Decide(windowed, "", Totals{}, settledWindows(0, 0, 0),
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
			fits, binding := Decide(windowed, "", Totals{}, settledWindows(tt.windowSettled...),
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
	fits, binding := Decide(windowed, "", Totals{ProviderCost: 11 * money.Dollar}, settledWindows(0, 0, 0),
		Reservation{ProviderCost: 2 * money.Dollar})
	if fits || binding != "5h" {
		t.Fatalf("fits = %v, binding = %q, want deferred on 5h", fits, binding)
	}
}

func TestDecideZeroesRunnerMinutesForAPublicTarget(t *testing.T) {
	// The estimate holds a large minute figure, but the caller is expected to
	// zero it for a public target before calling Decide; with it zeroed, a
	// month's worth of minutes settled elsewhere still fits.
	fits, binding := Decide(windowed, "", Totals{}, Settled{Windows: []money.Micros{0, 0, 0}, CashMinutes: 1900},
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
	fits, binding := Decide(windowed, "", Totals{}, Settled{Windows: []money.Micros{0, 0, 0}, CashMinutes: 2500},
		Reservation{ProviderCost: money.Dollar, RunnerMinutes: 0})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want a zero-minute candidate to fit despite the ceiling already being exhausted", fits, binding)
	}
}

func TestDecideHardStopsOnRunnerMinutesWithNoPaymentMethodConfigured(t *testing.T) {
	// A private target pushing total minutes past the free tier, with
	// RatePerMinute left at its zero default, must defer rather than spend.
	fits, binding := Decide(windowed, "", Totals{}, Settled{Windows: []money.Micros{0, 0, 0}, CashMinutes: 1950},
		Reservation{ProviderCost: money.Dollar, RunnerMinutes: 100})
	if fits || binding != CeilingRunnerMinutes {
		t.Fatalf("fits = %v, binding = %q, want deferred on %q", fits, binding, CeilingRunnerMinutes)
	}
}

func TestDecideConvertsRunnerMinutesToCashWhenARateIsConfigured(t *testing.T) {
	cfg := windowed
	cfg.Runner.RatePerMinute = 6000 // $0.006/minute
	cfg.Cash.Limit = money.Dollar   // a tight cash ceiling so the overage alone breaches it

	fits, binding := Decide(cfg, "", Totals{}, Settled{Windows: []money.Micros{0, 0, 0}, CashMinutes: 1990},
		Reservation{ProviderCost: 0, RunnerMinutes: 300}) // 290 minutes over the 2,000 free, at $0.006 = $1.74
	if fits || binding != CeilingCash {
		t.Fatalf("fits = %v, binding = %q, want deferred on %q", fits, binding, CeilingCash)
	}

	cfg.Cash.Limit = money.Dollar * 10
	fits, binding = Decide(cfg, "", Totals{}, Settled{Windows: []money.Micros{0, 0, 0}, CashMinutes: 1990},
		Reservation{ProviderCost: 0, RunnerMinutes: 300})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want the small overage to fit a looser cash ceiling", fits, binding)
	}
}

func TestDecideChecksAPerTokenProvidersCostAgainstCashDirectly(t *testing.T) {
	// No provider windows configured: the provider's own cost is what Cash
	// exists to bound, so it counts even with no runner minutes at all.
	cfg := BudgetConfig{Cash: Window{Name: "cash", Calendar: true, Limit: 20 * money.Dollar}, Runner: RunnerMinutes{FreeMinutes: 2000}}
	fits, binding := Decide(cfg, "", Totals{}, Settled{CashCost: 15 * money.Dollar}, Reservation{ProviderCost: 6 * money.Dollar})
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
	fits, binding := Decide(cfg, "", Totals{}, settledWindows(0, 0, 0), Reservation{ProviderCost: 5 * money.Dollar})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want a windowed provider's cost to bypass cash", fits, binding)
	}
}

// capped is a budget whose plan has a roomy monthly window but a $60 monthly
// cap on one model, as GOAT's DeepSeek V4.1 Flash runs are metered.
func capped() BudgetConfig {
	cfg := windowed
	cfg.ModelCaps = map[string]Window{"p/flash": {Name: "p/flash", Period: 720 * time.Hour, Limit: 60 * money.Dollar}}
	return cfg
}

func TestDecideDefersOnTheRunModelsOwnCapBesideItsPlanWindows(t *testing.T) {
	// The plan's month window ($60) has room for one more run, but the model's
	// own cap ($60) is already spent: the run is admitted only while BOTH have
	// room, so it defers on the model's cap.
	cfg := capped()
	settled := Settled{Windows: []money.Micros{0, 0, 0}, ModelCap: 58 * money.Dollar}
	fits, binding := Decide(cfg, "p/flash", Totals{}, settled, Reservation{ProviderCost: 5 * money.Dollar})
	if fits || binding != "p/flash" {
		t.Fatalf("fits = %v, binding = %q, want deferred on the model cap", fits, binding)
	}
}

func TestDecideAdmitsWhileTheModelsCapStillHasRoom(t *testing.T) {
	cfg := capped()
	settled := Settled{Windows: []money.Micros{0, 0, 0}, ModelCap: 40 * money.Dollar}
	fits, binding := Decide(cfg, "p/flash", Totals{}, settled, Reservation{ProviderCost: 5 * money.Dollar})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want a run inside the model cap admitted", fits, binding)
	}
}

func TestDecideAppliesNoCapToAModelWithoutOne(t *testing.T) {
	// A model the plan sets no cap for is bounded by the plan's windows alone:
	// another model's spent cap does not withhold it.
	cfg := capped()
	settled := Settled{Windows: []money.Micros{0, 0, 0}, ModelCap: 1000 * money.Dollar}
	fits, binding := Decide(cfg, "p/other", Totals{}, settled, Reservation{ProviderCost: 5 * money.Dollar})
	if !fits || binding != "" {
		t.Fatalf("fits = %v, binding = %q, want no cap applied to an uncapped model", fits, binding)
	}
}

func TestDecideNamesThePlanWindowWhenItBindsBeforeTheModelCap(t *testing.T) {
	// The plan's month window is breached first (it is checked ahead of the cap
	// and is the tighter of the two once the model cap is likewise spent), so
	// the deferral names the window.
	cfg := capped()
	cfg.ProviderWindows = []Window{{Name: "month", Period: 720 * time.Hour, Limit: 50 * money.Dollar}}
	settled := Settled{Windows: []money.Micros{48 * money.Dollar}, ModelCap: 58 * money.Dollar}
	_, binding := Decide(cfg, "p/flash", Totals{}, settled, Reservation{ProviderCost: 5 * money.Dollar})
	if binding != "month" {
		t.Fatalf("binding = %q, want the plan window checked first", binding)
	}
}

func TestStillBindsAtTheNextPoll(t *testing.T) {
	cfg := BudgetConfig{
		ProviderWindows: []Window{{Name: "5h", Period: 5 * time.Hour, Limit: 12 * money.Dollar}, {Name: "week", Period: 168 * time.Hour, Limit: 30 * money.Dollar}},
		Cash:            Window{Name: CeilingCash, Calendar: true, Limit: 20 * money.Dollar},
	}
	estimate := Reservation{ProviderCost: 2 * money.Dollar}
	for name, tt := range map[string]struct {
		settled Settled
		ceiling string
		want    bool
	}{
		"settled cost still fills the window":        {settledWindows(10*money.Dollar+1, 0), "5h", true},
		"exactly at the limit, so it clears":         {settledWindows(10*money.Dollar, 0), "5h", false},
		"another ceiling binds next poll":            {settledWindows(0, 30*money.Dollar), "5h", false},
		"a condition is never a budget ceiling here": {settledWindows(20*money.Dollar, 0), ConditionRepoBusy, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := stillBinds(cfg, "", Totals{}, tt.settled, estimate, tt.ceiling); got != tt.want {
				t.Fatalf("stillBinds = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCeilingStartNamesTheWindowEachCeilingMeters(t *testing.T) {
	at := time.Date(2026, 9, 27, 9, 15, 0, 0, time.UTC)
	cfg := BudgetConfig{
		ProviderWindows: []Window{{Name: "5h", Period: 5 * time.Hour}},
		ModelCaps:       map[string]Window{"p/m": {Name: "p/m", Period: 720 * time.Hour}},
		Cash:            Window{Name: CeilingCash, Calendar: true},
	}
	month := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for ceiling, want := range map[string]time.Time{
		"5h":                 time.Date(2026, 9, 27, 5, 0, 0, 0, time.UTC),
		"p/m":                time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
		CeilingCash:          month,
		CeilingRunnerMinutes: month,
	} {
		if got, ok := cfg.ceilingStart(ceiling, at); !ok || !got.Equal(want) {
			t.Errorf("%s: start = %v, %v; want %v", ceiling, got, ok, want)
		}
	}
	if _, ok := cfg.ceilingStart(ConditionRepoBusy, at); ok {
		t.Error("a condition named a window")
	}
}

func TestDecideChecksOnlyRunnerCostAgainstCashOnAFreeTier(t *testing.T) {
	free := windowed.lastResort()
	free.Runner.RatePerMinute = money.Dollar / 100
	spent := Settled{CashCost: 25 * money.Dollar}
	if fits, binding := Decide(free, "", Totals{ProviderCost: 5 * money.Dollar}, spent, Reservation{RunnerMinutes: 100}); !fits {
		t.Fatalf("binding = %q, want paid provider spend not counted against cash", binding)
	}
	spent.CashMinutes = 2000 + 2100
	if fits, binding := Decide(free, "", Totals{}, spent, Reservation{RunnerMinutes: 100}); fits || binding != CeilingCash {
		t.Fatalf("fits = %v, binding = %q, want runner overage still bound by cash", fits, binding)
	}
}
