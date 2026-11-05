package dispatcher

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
	// The zone database is linked into the binary, so the distroless image,
	// which may carry none, still resolves the owner's zone.
	_ "time/tzdata"
)

// The reasons a held run is counted under, in the order the Statistics page
// breaks a tie in. Every condition that withholds a candidate has one, bar a
// retry whose models could not be checked (ConditionModelsUnchecked); several
// share it: a repository at its cap and a write set overlapping a run in flight
// both read as repo-busy, and every budget ceiling as budget.
const (
	HoldConcurrency  = "concurrency"
	HoldRepoBusy     = "repo-busy"
	HoldReviewWIP    = "review-wip"
	HoldLargeCap     = "large-cap"
	HoldBlocked      = "blocked"
	HoldBudget       = "budget"
	HoldJobLimit     = "job-limit"
	HoldBreaker      = "breaker"
	HoldPaused       = "paused"
	HoldPeakHours    = "peak-hours"
	HoldProviderDown = "provider-down"
	HoldNoEstimate   = "no-estimate"
	HoldPrivateOptIn = "private-opt-in"
)

// HoldReasons are the reasons a hold is recorded under, in tie order.
var HoldReasons = []string{
	HoldConcurrency, HoldRepoBusy, HoldReviewWIP, HoldLargeCap, HoldBlocked, HoldBudget,
	HoldJobLimit, HoldBreaker, HoldPaused, HoldPeakHours, HoldProviderDown, HoldNoEstimate, HoldPrivateOptIn,
}

// holdZone is the owner's zone, whose calendar days a hold is counted on.
var holdZone = mustLoadZone("Australia/Sydney")

// mustLoadZone loads name, panicking at start-up rather than counting holds on
// the wrong days.
func mustLoadZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(fmt.Sprintf("loading time zone %s: %v", name, err))
	}
	return loc
}

// HoldWindowDays is how many days, today among them, the held counts and the
// peak in flight are read over.
const HoldWindowDays = 30

// HoldRecorder records that a poll held runID for reason on day, the Sydney
// date HoldDay names, at at. Recording the same run, reason and day twice leaves one
// hold.
type HoldRecorder interface {
	RecordHold(ctx context.Context, runID, reason, day string, at time.Time) error
}

// HoldDay is the Sydney date a hold at at is counted on.
func HoldDay(at time.Time) string {
	return at.In(holdZone).Format(time.DateOnly)
}

// HoldWindowStart is the midnight, in Sydney, that opens the day
// HoldWindowDays-1 days before now's: the first instant the trailing window
// counts. A day is a calendar day, so one that DST shortens or lengthens is
// still one.
func HoldWindowStart(now time.Time) time.Time {
	y, m, d := now.In(holdZone).Date()
	return time.Date(y, m, d+1-HoldWindowDays, 0, 0, 0, 0, holdZone)
}

// holdReason is the reason binding, the condition that withheld a candidate
// judged against budget, is counted under; "" for a name no condition carries.
func holdReason(binding string, budget BudgetConfig) string {
	switch binding {
	case ConditionConcurrency:
		return HoldConcurrency
	case ConditionRepoCap, ConditionRepoOverlap:
		return HoldRepoBusy
	case ConditionReviewWIP:
		return HoldReviewWIP
	case ConditionLargeCap:
		return HoldLargeCap
	case ConditionBlocked:
		return HoldBlocked
	case CeilingCash, CeilingRunnerMinutes:
		return HoldBudget
	case ConditionPlatformCap:
		return HoldJobLimit
	case ConditionCircuitBreaker:
		return HoldBreaker
	case ConditionOwnerPaused:
		return HoldPaused
	case ConditionPeakHours:
		return HoldPeakHours
	case CeilingProviderHalted:
		return HoldProviderDown
	case ConditionEstimateFailed:
		return HoldNoEstimate
	case ConditionPrivateOptIn:
		return HoldPrivateOptIn
	}
	if budget.isProviderCeiling(binding) {
		return HoldBudget
	}
	return ""
}

// recordHold records the hold of cand on binding, once per run, reason and day:
// a reason the candidate's row already holds for today is not written again, so
// a run held every poll costs one write a day. A failed write is logged and the
// poll goes on, since a count must never stop dispatch.
func recordHold(ctx context.Context, d Deps, cand Candidate, binding string, budget BudgetConfig) {
	reason := holdReason(binding, budget)
	if d.Holds == nil || reason == "" {
		return
	}
	at := d.Now()
	day := HoldDay(at)
	if slices.Contains(cand.Held[day], reason) {
		return
	}
	if err := d.Holds.RecordHold(ctx, cand.RunID, reason, day, at); err != nil {
		d.Logger.Warn("holdNotRecorded", "run", cand.RunID, "reason", reason, "err", err.Error())
	}
}

// HeldCount is how many holds one reason counts.
type HeldCount struct {
	Reason string
	Count  int
}

// RankHolds lists the reasons counts holds a hold for, largest first, a tie in
// HoldReasons order, and leaves out a reason with none.
func RankHolds(counts map[string]int) []HeldCount {
	out := make([]HeldCount, 0, len(HoldReasons))
	for _, reason := range HoldReasons {
		if n := counts[reason]; n > 0 {
			out = append(out, HeldCount{Reason: reason, Count: n})
		}
	}
	slices.SortStableFunc(out, func(a, b HeldCount) int { return cmp.Compare(b.Count, a.Count) })
	return out
}
