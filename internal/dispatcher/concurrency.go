package dispatcher

import (
	"cmp"
	"time"
)

// Tuning holds the owner-tunable numbers of the concurrency rule.
type Tuning struct {
	// StableRuns is how many consecutive stable observations raise N by one.
	StableRuns int
	// RiseWithin is the largest ratio to the solo median that counts as stable.
	RiseWithin float64
	// HalveBeyond is the ratio to the solo median past which N halves.
	HalveBeyond float64
}

// DefaultTuning is the starting point, not a measured value.
var DefaultTuning = Tuning{StableRuns: 5, RiseWithin: 1.25, HalveBeyond: 1.5}

// OrDefault fills every zero field from DefaultTuning.
func (t Tuning) OrDefault() Tuning {
	t.StableRuns = cmp.Or(t.StableRuns, DefaultTuning.StableRuns)
	t.RiseWithin = cmp.Or(t.RiseWithin, DefaultTuning.RiseWithin)
	t.HalveBeyond = cmp.Or(t.HalveBeyond, DefaultTuning.HalveBeyond)
	return t
}

// The owner's runs-at-once setting N: how many runs may be in flight at once
// across every project. Its upper bound is the platform cap, which is
// configured, so it is not a constant here.
const (
	MinRunsAtOnce     = 1
	DefaultRunsAtOnce = 2
)

// Concurrency is a limit N and how many stable observations have accumulated
// toward raising it.
type Concurrency struct {
	N      int
	Stable int
}

// Observation is what one settled run says about running at the current N.
type Observation struct {
	// Solo is the median duration of runs of the size that ran alone, and
	// Loaded the median of the latest runs of that size claimed at the same
	// concurrency as the settled one; either is zero when there is none.
	Solo   time.Duration
	Loaded time.Duration
	// RateLimited is a provider infrastructure stop, the signal that the
	// provider is refusing the load.
	RateLimited bool
}

// NextConcurrency applies the rule: N halves (never below 1) on a rate-limit
// stop or a loaded median past HalveBeyond times the solo median, and rises by
// one, up to platformCap, after StableRuns consecutive loaded medians within
// RiseWithin times it. Anything in between holds N and restarts the count.
func NextConcurrency(cur Concurrency, obs Observation, t Tuning, platformCap int) Concurrency {
	next := Concurrency{N: min(max(cur.N, 1), platformCap), Stable: cur.Stable}
	halved := Concurrency{N: max(next.N/2, 1)}
	if obs.RateLimited {
		return halved
	}
	if obs.Solo <= 0 || obs.Loaded <= 0 {
		return next
	}
	ratio := float64(obs.Loaded) / float64(obs.Solo)
	switch {
	case ratio > t.HalveBeyond:
		return halved
	case ratio <= t.RiseWithin:
		next.Stable++
		if next.Stable >= t.StableRuns {
			return Concurrency{N: min(next.N+1, platformCap)}
		}
		return next
	default:
		next.Stable = 0
		return next
	}
}

// The bases of a Recommendation: what the latest evidence at the owner's N was.
const (
	// BasisNone is no evidence at N yet.
	BasisNone = ""
	// BasisSteady counts the stable observations in a row toward raising N.
	BasisSteady = "steady"
	BasisRise   = "rise"
	// BasisSlow is a loaded median past HalveBeyond times the solo median.
	BasisSlow        = "slow"
	BasisRateLimited = "rate-limited"
)

// Recommendation is the N the concurrency rule recommends beside the owner's
// N, the stable observations in a row behind it, and its basis. It changes
// nothing by itself: admission runs under the owner's N.
type Recommendation struct {
	// N is zero while Basis is BasisNone, which recommends the owner's N.
	N      int
	Stable int
	Basis  string
}

// Recommend applies NextConcurrency at the owner's n to an observation and
// returns what it would have set N to, without setting it. A rise holds its
// count full, so it keeps recommending one more until evidence says otherwise;
// an observation carrying no medians and no rate-limit stop changes nothing.
func Recommend(prev Recommendation, n int, obs Observation, t Tuning, platformCap int) Recommendation {
	if !obs.RateLimited && (obs.Solo <= 0 || obs.Loaded <= 0) {
		return prev
	}
	next := NextConcurrency(Concurrency{N: n, Stable: prev.Stable}, obs, t, platformCap)
	switch {
	case next.N < n && obs.RateLimited:
		return Recommendation{N: next.N, Basis: BasisRateLimited}
	case next.N < n:
		return Recommendation{N: next.N, Basis: BasisSlow}
	case next.N > n:
		return Recommendation{N: next.N, Stable: t.StableRuns, Basis: BasisRise}
	default:
		return Recommendation{N: n, Stable: next.Stable, Basis: BasisSteady}
	}
}
