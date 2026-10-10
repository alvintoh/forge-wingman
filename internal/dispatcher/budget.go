package dispatcher

import (
	"context"
	"slices"
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
	// ceilings, from the provider configuration. Checked INSTEAD OF Cash for
	// provider cost, per NFR-1 — a windowed subscription already satisfies the
	// cash cap. Empty for a per-token provider, whose cost is then checked
	// against Cash directly.
	ProviderWindows []Window
	// ModelCaps are the selected provider's own monthly ceiling per model,
	// keyed by the full model id a run names. A run on a model that carries one
	// is bounded by it beside ProviderWindows; a model absent from the
	// map is bounded by the windows alone. A run records one settled cost, not
	// one per model, so the cap is checked against every model's spend: it binds
	// early, never late, until runs record cost per model.
	ModelCaps map[string]Window
	// Cash is NFR-1's $30/month ceiling.
	Cash Window
	// Runner is the runner-minutes ceiling, checked independently (FR-22).
	Runner RunnerMinutes
	// ProviderFree is set for a run on a plan's free tier, whose provider cost
	// Cash does not bound.
	ProviderFree bool
}

// Reservation is one candidate run's claim on the budget: its estimated
// provider cost, and its estimated runner minutes — zeroed by the caller for
// a public target, which spends none (FR-22, NFR-1).
type Reservation struct {
	ProviderCost  money.Micros
	RunnerMinutes int64
	// Stage is the stage this reserve claims — plan or build; empty for a
	// single-stage claim, which plans and builds in one job.
	Stage string
	// Files is the write set this claim may touch: the plan-stage file list
	// of a build-stage claim, and empty for a plan-stage or single-stage
	// one, which plans its own.
	Files []string
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

// DefaultPlanEstimate is what a plan stage is assumed to cost and take before
// any run has settled a plan phase to average: one round of a plan model over
// a repository. The samples come from the inline plan phase of single-stage
// runs, since the plan job reports no summary.
var DefaultPlanEstimate = Estimate{ProviderCost: money.FromUSD(0.05), Minutes: 5}

// Estimator reads past runs' settled cost and duration to estimate an
// unclaimed run's own.
type Estimator interface {
	Estimate(ctx context.Context, size string) (Estimate, error)
	// EstimatePlan estimates the run's plan stage the way Estimate does,
	// averaged over the plan phases of past settled runs and falling back to
	// DefaultPlanEstimate where none has settled yet.
	EstimatePlan(ctx context.Context, size string) (Estimate, error)
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
	// Model is the build model the run's ticket named, empty when it named
	// none and the run's own default applies.
	Model string
	// Stage is the stage this row is ready for: StageBuild for a run whose
	// plan stage has recorded a plan, empty for one that has not.
	Stage string
	// PlanFiles is the run's planned write set, recorded by its plan stage
	// and empty until one has settled.
	PlanFiles []string
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
// settled.Windows pairs each of cfg.ProviderWindows, in the same order, with
// the settled provider cost of runs that ended inside it, and settled.CashCost
// and settled.CashMinutes are the equivalent totals summed over Cash's own
// window. model names the run's build model, selecting which of cfg.ModelCaps
// (if any) bounds it, with settled.ModelCap the plan's cost inside that cap's
// own window. It reports whether estimate fits every ceiling, and names the
// first one it would breach.
func Decide(cfg BudgetConfig, model string, reserved Totals, settled Settled, estimate Reservation) (fits bool, binding string) {
	for i, w := range cfg.ProviderWindows {
		var windowSettled money.Micros
		if i < len(settled.Windows) {
			windowSettled = settled.Windows[i]
		}
		if reserved.ProviderCost+windowSettled+estimate.ProviderCost > w.Limit {
			return false, w.Name
		}
	}

	// A run whose model carries its own monthly cap is bounded by it beside the
	// plan's windows: a month with room under the plan-wide window may still
	// have spent that model's own allowance.
	if modelCap, ok := cfg.ModelCaps[model]; ok {
		if reserved.ProviderCost+settled.ModelCap+estimate.ProviderCost > modelCap.Limit {
			return false, modelCap.Name
		}
	}

	// A candidate contributing zero runner minutes (a public target) can
	// never newly breach this ceiling, however much prior private-target
	// work has already consumed — so it is skipped entirely rather than
	// being blocked by an overage it played no part in (FR-22, NFR-1: "a
	// public target's [runner minutes] count as zero").
	var runnerCost money.Micros
	if estimate.RunnerMinutes > 0 {
		totalMinutes := reserved.RunnerMinutes + settled.CashMinutes + estimate.RunnerMinutes
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
	if len(cfg.ProviderWindows) == 0 && !cfg.ProviderFree {
		providerCash = reserved.ProviderCost + settled.CashCost + estimate.ProviderCost
	}
	if providerCash+runnerCost > cfg.Cash.Limit {
		return false, CeilingCash
	}
	return true, ""
}

// isProviderCeiling reports whether binding names one of the plan's own windows or model caps.
func (cfg BudgetConfig) isProviderCeiling(binding string) bool {
	if slices.ContainsFunc(cfg.ProviderWindows, func(w Window) bool { return w.Name == binding }) {
		return true
	}
	for _, w := range cfg.ModelCaps {
		if w.Name == binding {
			return true
		}
	}
	return false
}

// lastResort is the budget a run on a plan's free tier is admitted against:
// cash and runner minutes, with none of the plan's own windows or caps.
func (cfg BudgetConfig) lastResort() BudgetConfig {
	return BudgetConfig{Cash: cfg.Cash, Runner: cfg.Runner, ProviderFree: true}
}

// stillBinds reports whether ceiling, the budget ceiling that withheld a run,
// would still withhold it given the spend as it will stand at the next poll.
func stillBinds(cfg BudgetConfig, model string, reserved Totals, settled Settled, estimate Reservation, ceiling string) bool {
	fits, binding := Decide(cfg, model, reserved, settled, estimate)
	return !fits && binding == ceiling
}

// ceilingStart is the start of the window ceiling meters at at, false when cfg
// has no such ceiling. A rolling window's start is the Period-long span at
// falls in, so it names one window as long as the ceiling binds inside it.
func (cfg BudgetConfig) ceilingStart(ceiling string, at time.Time) (time.Time, bool) {
	if ceiling == CeilingCash || ceiling == CeilingRunnerMinutes {
		return cfg.Cash.Since(at), true
	}
	windows := slices.Clone(cfg.ProviderWindows)
	for _, w := range cfg.ModelCaps {
		windows = append(windows, w)
	}
	for _, w := range windows {
		if w.Name != ceiling {
			continue
		}
		if w.Calendar {
			return w.Since(at), true
		}
		return at.UTC().Truncate(w.Period), true
	}
	return time.Time{}, false
}
