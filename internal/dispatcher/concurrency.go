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

// Concurrency is the discovered limit N and how many stable observations have
// accumulated toward raising it.
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
