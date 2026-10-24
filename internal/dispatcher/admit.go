package dispatcher

import (
	"cmp"
	"slices"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// The conditions besides a budget ceiling that withhold a candidate; a
// Deferral names one of these, a Ceiling* or a provider window in its Ceiling.
const (
	ConditionPlatformCap = "platform-cap"
	ConditionConcurrency = "concurrency-limit"
	ConditionLargeCap    = "large-cap"
	ConditionRepoBusy    = "repo-busy"
	ConditionBlocked     = "blocked"
	ConditionReviewWIP   = "review-wip"
	// ConditionEstimateFailed withholds a candidate whose estimate could not be
	// read, before admission is asked.
	ConditionEstimateFailed = "estimate-failed"
	// ConditionCircuitBreaker withholds every candidate while a systemic stop holds dispatch.
	ConditionCircuitBreaker = "circuit-breaker"
	// ConditionOwnerPaused withholds a new claim of a project the owner has paused.
	ConditionOwnerPaused = "owner-paused"
	// ConditionPeakHours withholds a candidate that would run in its model's peak-price hours.
	ConditionPeakHours = "peak-hours"
)

// largeSize is the ticket size whose concurrent runs are capped separately.
const largeSize = "L"

// The stages of a run the two-stage dispatch splits it into: `plan` runs the
// plan phase alone under the read-only profile, `build` builds from a plan
// already recorded. A claim of neither — the shape a single-stage run takes —
// plans and builds in one job.
const (
	StagePlan  = "plan"
	StageBuild = "build"
)

// Limits are the concurrency ceilings admission holds beside N.
type Limits struct {
	// PlatformCap is the concurrent jobs the platform allows one account.
	PlatformCap int
	// LargeCap is how many size-L runs may be in flight at once.
	LargeCap int
	// ReviewWIP is how many open agent PRs may await review across every
	// allowlisted repository before another run starts.
	ReviewWIP int
}

// DefaultLimits are the owner-tunable defaults a zero Config.Limits stands for.
var DefaultLimits = Limits{PlatformCap: 20, LargeCap: 1, ReviewWIP: 3}

// OrDefault fills every zero field from DefaultLimits.
func (l Limits) OrDefault() Limits {
	l.PlatformCap = cmp.Or(l.PlatformCap, DefaultLimits.PlatformCap)
	l.LargeCap = cmp.Or(l.LargeCap, DefaultLimits.LargeCap)
	l.ReviewWIP = cmp.Or(l.ReviewWIP, DefaultLimits.ReviewWIP)
	return l
}

// InFlight is one run whose planned write set is known and whose run is in
// flight: the ledger holds a reservation for a claimed one, and a run whose
// plan stage has settled waits in planned for its build with none.
type InFlight struct {
	Ticket string
	Repo   string
	Size   string
	// Files is the write set the run's plan recorded, and empty until one
	// exists: a run still in flight whose plan has not settled writes an
	// unknown set, not an empty one.
	Files []string
	// Stage is the stage the reservation was claimed for; empty for a
	// single-stage claim, and for a run no reservation covers.
	Stage string
}

// Subject is the candidate admission is deciding, as its run row records it.
type Subject struct {
	Ticket    string
	Repo      string
	Size      string
	BlockedBy []string
	Blocks    []string
	// BlockedByOpen names the blocked-by issues Linear still holds open, which
	// withhold the candidate whether or not a run is in flight.
	BlockedByOpen []string
	// Files is the candidate's planned write set, from the plan its plan
	// stage recorded; empty when nothing is planned yet, which is the write
	// set that is unknown.
	Files []string
	// Stage is the stage this claim would dispatch.
	Stage string
}

// Relations are a ticket's blocking relations as Linear reports them now;
// Known is unset when this poll did not read the ticket.
type Relations struct {
	Known     bool
	BlockedBy []string
	Blocks    []string
	// BlockedByOpen names the blocked-by issues Linear reads as still open,
	// which withhold the candidate; empty when this poll did not read the
	// ticket.
	BlockedByOpen []string
}

// Facts are what admission learns outside the ledger.
type Facts struct {
	Limits         Limits
	Tuning         Tuning
	Relations      Relations
	ProviderHalted bool
	// BreakerTripped is set while a systemic stop holds every dispatch.
	BreakerTripped bool
	// OwnerPaused is set while the owner holds the candidate's project, for a claim
	// that is not a planned ticket's build.
	OwnerPaused bool
	// PeakHeld is set when the run would run in its model's peak-price hours.
	PeakHeld bool
	// Model is the run's build model — the model its ticket named, or the run's
	// own default — which selects any per-model cap that bounds it.
	Model string
	// LastResort names the models of a claim on Model's plan's free tier,
	// written onto the run record with the claim; nil otherwise.
	LastResort *runner.ModelLabels
	// Moved names the paid plan whose ceiling bound and the models of the plan
	// the claim moved to, written onto the run record with the claim; nil
	// otherwise.
	Moved *Move
	// OpenPRs is the count of open agent PRs across the allowlisted
	// repositories, meaningful only when OpenPRsKnown is set.
	OpenPRs      int
	OpenPRsKnown bool
	// DecisionMode is how the run's plan decisions are settled, recorded on the run when it is claimed.
	DecisionMode runner.DecisionMode
	// History loads the poll's snapshot of the settled run history, which the
	// budget ceilings are summed from: read once for a whole poll however many
	// candidates it walks, and only once a candidate reaches its ceilings, so a
	// candidate a non-budget limit holds is withheld without reading it (AC1,
	// AC5). Nil where the claimant holds no snapshot, which then reads the
	// history in its own transaction.
	History HistoryLoader
}

// Move is a run's move from a paid plan whose ceiling bound to the next.
type Move struct {
	From   string
	Models runner.ModelLabels
}

// Settled is the settled cost of runs inside each window Decide checks.
type Settled struct {
	Windows []money.Micros
	// ModelCap is the settled provider cost inside the run model's own cap
	// window; zero when the model carries no cap.
	ModelCap    money.Micros
	CashCost    money.Micros
	CashMinutes int64
}

// AdmitInput is everything Admit decides from.
type AdmitInput struct {
	Facts       Facts
	N           int
	InFlight    []InFlight
	Candidate   Subject
	Budget      BudgetConfig
	Reserved    Totals
	Settled     Settled
	Reservation Reservation
}

// Admit reports whether the candidate may start now, and otherwise the first
// condition that withholds it, checked in this order: the circuit breaker,
// owner pause, provider halt, peak hours, platform cap, N, the concurrent-L cap,
// one run per repository, a blocker, review WIP, then the budget ceilings. It
// does no IO.
//
// The blocker condition holds a candidate back while a blocked-by issue Linear
// has not completed or canceled still holds it, or while a run in flight bears
// a blocking relation to it in either direction.
//
// The one-run-per-repository condition holds a candidate back only where an
// in-flight run in its repository has an unknown write set, or the candidate
// does, and neither holds a plan-stage claim: a plan stage writes nothing, its
// agent holding edit and shell denied. Runs whose write sets are both known
// share the repository whether or not the sets intersect, and the runner
// requeues a run whose merge conflicts.
//
// When the open-PR count could not be read, a candidate is admitted only if
// nothing is in flight.
func Admit(in AdmitInput) (ok bool, binding string) {
	if why := HeldBeforeBudget(in); why != "" {
		return false, why
	}
	if fits, why := Decide(in.Budget, in.Facts.Model, in.Reserved, in.Settled, in.Reservation); !fits {
		return false, why
	}
	return true, ""
}

// HeldBeforeBudget reports the first condition withholding in before its budget
// ceilings are checked, and "" when it reaches them: the conditions Admit
// checks ahead of the ceilings, in the same order. A claimant asks it first, so
// a candidate a non-budget limit holds is withheld without the settled run
// history its ceilings would have been summed from (AC5). It does no IO.
func HeldBeforeBudget(in AdmitInput) string {
	in.N = max(in.N, 1)
	running := len(in.InFlight)
	switch {
	case in.Facts.BreakerTripped:
		return ConditionCircuitBreaker
	case in.Facts.OwnerPaused:
		return ConditionOwnerPaused
	case in.Facts.ProviderHalted:
		return CeilingProviderHalted
	case in.Facts.PeakHeld:
		return ConditionPeakHours
	case running >= in.Facts.Limits.PlatformCap:
		return ConditionPlatformCap
	case running >= in.N:
		return ConditionConcurrency
	case in.Candidate.Size == largeSize && countSize(in.InFlight, largeSize) >= in.Facts.Limits.LargeCap:
		return ConditionLargeCap
	case slices.ContainsFunc(in.InFlight, func(f InFlight) bool {
		return f.Repo == in.Candidate.Repo && writeSetsCollide(f, in.Candidate)
	}):
		return ConditionRepoBusy
	case blocked(in.Candidate, in.InFlight):
		return ConditionBlocked
	case in.Facts.OpenPRsKnown && in.Facts.OpenPRs >= in.Facts.Limits.ReviewWIP,
		!in.Facts.OpenPRsKnown && running > 0:
		return ConditionReviewWIP
	}
	return ""
}

func countSize(runs []InFlight, size string) int {
	n := 0
	for _, r := range runs {
		if r.Size == size {
			n++
		}
	}
	return n
}

// writeSetsCollide reports whether an in-flight run's write set and the
// candidate's rule out sharing one repository. A plan-stage claim collides with
// nothing, on either side: it writes nothing, its agent holding edit and shell
// denied. An unknown write set — no plan recorded, which is every run before
// two-stage dispatch — collides with any candidate, which is the interim
// one-run-per-repository rule; two known sets never collide.
func writeSetsCollide(f InFlight, c Subject) bool {
	if f.Stage == StagePlan || c.Stage == StagePlan {
		return false
	}
	return len(f.Files) == 0 || len(c.Files) == 0
}

// OverlappingRuns names the tickets of the in-flight runs in the candidate's
// repository whose known write set shares a path with the candidate's. It does
// no IO.
func OverlappingRuns(in AdmitInput) []string {
	var tickets []string
	for _, f := range in.InFlight {
		if f.Repo != in.Candidate.Repo {
			continue
		}
		if slices.ContainsFunc(f.Files, func(name string) bool { return slices.Contains(in.Candidate.Files, name) }) {
			tickets = append(tickets, f.Ticket)
		}
	}
	return tickets
}

// blocked reports a blocker of the candidate: a blocked-by issue Linear still
// holds open, or a blocking relation, in either direction, with a run in
// flight.
func blocked(c Subject, runs []InFlight) bool {
	if len(c.BlockedByOpen) > 0 {
		return true
	}
	return slices.ContainsFunc(runs, func(r InFlight) bool {
		return slices.Contains(c.BlockedBy, r.Ticket) || slices.Contains(c.Blocks, r.Ticket)
	})
}
