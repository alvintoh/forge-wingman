package dispatcher

import (
	"context"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
)

// Ceiling names the binding limit a deferral records (FR-22). Reused for
// CeilingProviderHalted, which is not a budget ceiling but withholds a
// candidate the same way — reconsidered next poll, never rejected outright.
const (
	CeilingRunnerMinutes  = "runner-minutes"
	CeilingCash           = "cash"
	CeilingProviderHalted = "provider-halted"
)

// Window is one rolling allowance ceiling FR-22 checks admission against: the
// reservations in flight plus the settled cost of runs that ended within
// Period of now must not exceed Limit. Calendar, when set, aligns the window
// to the current calendar month (UTC) instead — GitHub Actions' free-minutes
// allowance and NFR-1's cash ceiling both reset on that cycle, where a
// provider's own allowance windows do not (adr/0003: "a 5-hour window is not
// calendar-aligned; it's a rolling look-back from now" — generalised here to
// every provider window, and deliberately NOT applied to Cash, which tracks
// GitHub's own billing cycle instead).
type Window struct {
	Name     string
	Period   time.Duration
	Calendar bool
	Limit    money.Micros
}

// Since is the window's start, looking back from now.
func (w Window) Since(now time.Time) time.Time {
	if w.Calendar {
		u := now.UTC()
		return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return now.Add(-w.Period)
}

// RunnerMinutes is NFR-1's second, independent ceiling: GitHub Actions
// minutes spent building a private target repository (a public one spends
// none), metered on the same calendar month as Cash. The first FreeMinutes
// cost nothing. Past that, RatePerMinute zero — the default, since GitHub
// blocks usage outright once the free quota is spent with no payment method
// on file — hard-stops dispatch; a non-zero rate instead converts the
// overage to cash and checks it against Cash, for an operator who has since
// added one.
type RunnerMinutes struct {
	FreeMinutes   int64
	RatePerMinute money.Micros
}

// BudgetConfig is FR-22's admission input, entirely FR-14/NFR-3
// configuration.
type BudgetConfig struct {
	// ProviderWindows are the selected provider's own rolling allowance
	// ceilings (OpenCode Go: 5h/$12, week/$30, month/$60). Checked INSTEAD OF
	// Cash for provider cost, per NFR-1 — a windowed subscription already
	// satisfies the cash cap. Empty for a per-token provider, whose cost is
	// then checked against Cash directly.
	ProviderWindows []Window
	// Cash is NFR-1's $20/month ceiling.
	Cash Window
	// Runner is the runner-minutes ceiling, checked independently (FR-22).
	Runner RunnerMinutes
}

// Reservation is one candidate run's claim on the budget: its estimated
// provider cost, and its estimated runner minutes — zeroed by the caller for
// a public target, which spends none (FR-22, NFR-1).
type Reservation struct {
	ProviderCost  money.Micros
	RunnerMinutes int64
}

// Totals sums a set of reservations, or of settled runs, for one comparison.
type Totals struct {
	ProviderCost  money.Micros
	RunnerMinutes int64
}

// Estimate is what a run of a given ticket size is expected to cost and take,
// from the mean of past runs of that size (PRD Q4), falling back to the
// first ever recorded sample when none exist for the size yet.
type Estimate struct {
	ProviderCost money.Micros
	Minutes      int64
}

// Estimator reads past runs' settled cost and duration to estimate an
// unclaimed run's own.
type Estimator interface {
	Estimate(ctx context.Context, size string) (Estimate, error)
}

// RepoVisibility reports whether a repository is private, since a private
// target's runner minutes count toward NFR-1's cash ceiling and a public
// one's count as zero (FR-22). Read live rather than from a hand-maintained
// list, which goes stale.
type RepoVisibility interface {
	Private(ctx context.Context, repo string) (bool, error)
}

// Candidate is one queued run a poll may claim, in priority order.
type Candidate struct {
	RunID    string
	Repo     string
	Size     string
	Private  bool
	Priority int
}

// Deferral is a queued run's claim withheld this poll by a budget ceiling or an
// admission condition, which Ceiling names. Unlike a Rejection it names no
// permanent verdict against the ticket — the same run is reconsidered, at the
// same priority, next poll (FR-22).
type Deferral struct {
	RunID   string
	Ceiling string
	At      time.Time
}

// Decide is FR-22's admission check. It does no IO: reserved is what the
// ledger currently holds reserved for every run in flight (adr/0003);
// windowSettled pairs each of cfg.ProviderWindows, in the same order, with
// the settled provider cost of runs that ended inside it; cashSettledCost and
// cashSettledMinutes are the equivalent totals summed over Cash's own window.
// It reports whether estimate fits every ceiling, and names the first one it
// would breach.
func Decide(cfg BudgetConfig, reserved Totals, windowSettled []money.Micros, cashSettledCost money.Micros,
	cashSettledMinutes int64, estimate Reservation) (fits bool, binding string) {
	for i, w := range cfg.ProviderWindows {
		var settled money.Micros
		if i < len(windowSettled) {
			settled = windowSettled[i]
		}
		if reserved.ProviderCost+settled+estimate.ProviderCost > w.Limit {
			return false, w.Name
		}
	}

	// A candidate contributing zero runner minutes (a public target) can
	// never newly breach this ceiling, however much prior private-target
	// work has already consumed — so it is skipped entirely rather than
	// being blocked by an overage it played no part in (FR-22, NFR-1: "a
	// public target's [runner minutes] count as zero").
	var runnerCost money.Micros
	if estimate.RunnerMinutes > 0 {
		totalMinutes := reserved.RunnerMinutes + cashSettledMinutes + estimate.RunnerMinutes
		if over := totalMinutes - cfg.Runner.FreeMinutes; over > 0 {
			if cfg.Runner.RatePerMinute <= 0 {
				return false, CeilingRunnerMinutes
			}
			runnerCost = money.Micros(over) * cfg.Runner.RatePerMinute
		}
	}

	// A windowed provider's own cost is checked against its windows above,
	// never against Cash too (NFR-1: "instead of, not in addition to"). A
	// per-token provider has no windows, so its cost is what Cash exists to
	// bound.
	var providerCash money.Micros
	if len(cfg.ProviderWindows) == 0 {
		providerCash = reserved.ProviderCost + cashSettledCost + estimate.ProviderCost
	}
	if providerCash+runnerCost > cfg.Cash.Limit {
		return false, CeilingCash
	}
	return true, ""
}
