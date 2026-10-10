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

// orDefault fills every zero field from DefaultLimits.
func (l Limits) orDefault() Limits {
	l.PlatformCap = cmp.Or(l.PlatformCap, DefaultLimits.PlatformCap)
	l.LargeCap = cmp.Or(l.LargeCap, DefaultLimits.LargeCap)
	l.ReviewWIP = cmp.Or(l.ReviewWIP, DefaultLimits.ReviewWIP)
	return l
}

// InFlight is one run the ledger holds a reservation for.
type InFlight struct {
	Ticket string
	Repo   string
	Size   string
	// Files is the write set the run's plan recorded, and empty until one
	// exists: a run still in flight whose plan has not settled writes an
	// unknown set, not an empty one.
	Files []string
	// Stage is the stage the reservation was claimed for; empty for a
	// single-stage claim.
	Stage string
}

// Subject is the candidate admission is deciding, as its run row records it.
type Subject struct {
	Ticket    string
	Repo      string
	Size      string
	BlockedBy []string
	Blocks    []string
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
}

// Facts are what admission learns outside the ledger.
type Facts struct {
	Limits         Limits
	Tuning         Tuning
	Relations      Relations
	ProviderHalted bool
	// BreakerTripped is set while a systemic stop holds every dispatch.
	BreakerTripped bool
	// Model is the run's build model — the model its ticket named, or the run's
	// own default — which selects any per-model cap that bounds it.
	Model string
	// LastResort names the models of a claim on Model's plan's free tier,
	// written onto the run record with the claim; nil otherwise.
	LastResort *runner.ModelLabels
	// OpenPRs is the count of open agent PRs across the allowlisted
	// repositories, meaningful only when OpenPRsKnown is set.
	OpenPRs      int
	OpenPRsKnown bool
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
// provider halt, platform cap, N, the concurrent-L cap, one run per
// repository, a blocking relation with a run in flight, review WIP, then the
// budget ceilings. It does no IO.
//
// The one-run-per-repository condition holds a candidate back only where an
// in-flight run in its repository writes what the candidate may also write:
// runs with known, disjoint write sets share the repository, and a run whose
// write set is unknown holds it as the interim rule always did.
//
// When the open-PR count could not be read, a candidate is admitted only if
// nothing is in flight.
func Admit(in AdmitInput) (ok bool, binding string) {
	in.N = max(in.N, 1)
	running := len(in.InFlight)
	switch {
	case in.Facts.BreakerTripped:
		return false, ConditionCircuitBreaker
	case in.Facts.ProviderHalted:
		return false, CeilingProviderHalted
	case running >= in.Facts.Limits.PlatformCap:
		return false, ConditionPlatformCap
	case running >= in.N:
		return false, ConditionConcurrency
	case in.Candidate.Size == largeSize && countSize(in.InFlight, largeSize) >= in.Facts.Limits.LargeCap:
		return false, ConditionLargeCap
	case slices.ContainsFunc(in.InFlight, func(f InFlight) bool {
		return f.Repo == in.Candidate.Repo && writeSetsCollide(f, in.Candidate)
	}):
		return false, ConditionRepoBusy
	case blockedByInFlight(in.Candidate, in.InFlight):
		return false, ConditionBlocked
	case in.Facts.OpenPRsKnown && in.Facts.OpenPRs >= in.Facts.Limits.ReviewWIP,
		!in.Facts.OpenPRsKnown && running > 0:
		return false, ConditionReviewWIP
	}
	s := in.Settled
	if fits, why := Decide(in.Budget, in.Facts.Model, in.Reserved, s, in.Reservation); !fits {
		return false, why
	}
	return true, ""
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
// candidate's rule out sharing one repository. A plan-stage run collides with
// nothing: its agent holds edit and shell denied. An unknown write set — no
// plan recorded, which is every run before two-stage dispatch — collides with
// any candidate, which is the interim one-run-per-repository rule; two known
// sets collide where they intersect.
func writeSetsCollide(f InFlight, c Subject) bool {
	if f.Stage == StagePlan || c.Stage == StagePlan {
		return false
	}
	if len(f.Files) == 0 || len(c.Files) == 0 {
		return true
	}
	return slices.ContainsFunc(f.Files, func(name string) bool { return slices.Contains(c.Files, name) })
}

// blockedByInFlight reports a blocking relation, in either direction, between
// the candidate and a run in flight.
func blockedByInFlight(c Subject, runs []InFlight) bool {
	return slices.ContainsFunc(runs, func(r InFlight) bool {
		return slices.Contains(c.BlockedBy, r.Ticket) || slices.Contains(c.Blocks, r.Ticket)
	})
}
