package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Autonomy is the owner's autonomy level, 0 to 5: how much the product may do
// without them.
type Autonomy int

const (
	// AutonomyAutoMerge is the lowest level at which an eligible PR may merge
	// itself.
	AutonomyAutoMerge Autonomy = 4
	// AutonomyRolledBack is the level a rollback drops to: the last level that
	// does not auto-merge, since every rollback condition faults the auto-merge
	// itself.
	AutonomyRolledBack Autonomy = AutonomyAutoMerge - 1
)

var (
	// ErrUnknownAutonomy is a level outside 0 to 5.
	ErrUnknownAutonomy = errors.New("unknown autonomy level")
	// ErrAutonomyNotEarned is a level whose advance condition does not hold.
	ErrAutonomyNotEarned = errors.New("autonomy level not earned")
)

// autonomyLevels holds, per level, what it allows and, for a level Settable
// refuses, the PRD's condition for advancing to it. The runs a day the product
// delegates to itself are not a level's: the cap is configured once
// (Config.DailyRuns) and the delegation runs at every level.
var autonomyLevels = [...]struct {
	allows  string
	advance string
}{
	{"the product delegates and you merge", ""},
	{"the product delegates and you merge", ""},
	{"the product delegates and you merge", ""},
	{"the product delegates and you merge", ""},
	{"as level 3, and eligible S and M PRs auto-merge",
		"level 3 held, a rewrite rate of 5% or less over 10 PRs, zero gate misses, and CD on the target repository"},
	{"the product delegates and eligible PRs auto-merge",
		"level 4 held over 20 sampled PRs with zero reverts"},
}

// Valid reports whether a names a level.
func (a Autonomy) Valid() bool { return a >= 0 && int(a) < len(autonomyLevels) }

// Known reports why a names no level, or nil when it does.
func (a Autonomy) Known() error {
	if !a.Valid() {
		return fmt.Errorf("%w %d: levels run 0 to %d", ErrUnknownAutonomy, int(a), len(autonomyLevels)-1)
	}
	return nil
}

// Describe is what level a allows, or "" for a level that does not exist.
func (a Autonomy) Describe() string {
	if !a.Valid() {
		return ""
	}
	return autonomyLevels[a].allows
}

// AutoMerges reports whether a lets an eligible PR merge itself.
func (a Autonomy) AutoMerges() bool { return a.Valid() && a >= AutonomyAutoMerge }

// Condition is one clause of a level's advance condition, with the numbers it
// was checked against.
type Condition struct {
	// Text is the clause and its numbers, e.g. "a rewrite rate of 0.6% over 10
	// of 10 PRs, 5% or less".
	Text string
	// Held is whether the clause holds.
	Held bool
}

// String is the clause as the owner reads it: what it is, then whether it holds.
func (c Condition) String() string {
	if c.Held {
		return c.Text + ": held"
	}
	return c.Text + ": not held"
}

// AllHeld reports whether every clause holds, and is true for no clauses at all:
// a level the owner may set outright has nothing to fail.
func AllHeld(conds []Condition) bool {
	for _, c := range conds {
		if !c.Held {
			return false
		}
	}
	return true
}

// Advance is the level's advance condition as clauses, each with the numbers it
// was checked against, and empty for a level the owner may set outright.
func (a Autonomy) Advance(ev Evidence) []Condition {
	switch a {
	case AutonomyAutoMerge:
		trailing := ev.Window(TrailingPRs)
		return []Condition{
			levelHeld(AutonomyRolledBack, ev.Level),
			rateAtMost(trailing, TrailingPRs, AdvanceRate),
			noGateMisses(trailing, TrailingPRs),
			cdRecorded(trailing, ev.CD),
			allMeasured(trailing, TrailingPRs),
			notRolledBack(ev),
		}
	case AutonomyAutoMerge + 1:
		return []Condition{
			levelHeld(AutonomyAutoMerge, ev.Level),
			notRolledBack(ev),
			autoMergedAll(ev.Window(SampledPRs), SampledPRs),
			allMeasured(ev.Window(SampledPRs), SampledPRs),
			zeroReverts(ev.Window(SampledPRs), SampledPRs),
		}
	case AutonomyRolledBack:
		// Level 3 is set outright, like every level below it.
		return nil
	}
	return nil
}

// Settable reports why the owner may not set a, or nil when they may. A level
// that auto-merges is refused unless every clause of its advance condition holds
// and the refusal names the clauses that did not, with their numbers.
func (a Autonomy) Settable(ev Evidence) error {
	if err := a.Known(); err != nil {
		return err
	}
	conds := a.Advance(ev)
	if AllHeld(conds) {
		return nil
	}
	return fmt.Errorf("%w: level %d needs %s, measured as: %s",
		ErrAutonomyNotEarned, int(a), autonomyLevels[a].advance, unheld(conds))
}

// unheld renders the clauses that did not hold.
func unheld(conds []Condition) string {
	var failed []string
	for _, c := range conds {
		if !c.Held {
			failed = append(failed, c.Text)
		}
	}
	return strings.Join(failed, "; ")
}

// levelHeld is the clause naming the level below, which the store must hold now.
func levelHeld(want, now Autonomy) Condition {
	return Condition{
		Text: fmt.Sprintf("level %d held (level %d is set)", int(want), int(now)),
		Held: now >= want,
	}
}

// rateAtMost is the clause the rate must sit at or under across the window's
// whole width: a rate measured over fewer PRs than the window holds advances
// nothing.
func rateAtMost(samples []Sample, window int, atMost float64) Condition {
	rate, prs := Rate(samples)
	return Condition{
		Text: fmt.Sprintf("a rewrite rate of %.1f%% over %d of %d PRs, %s or less",
			rate*100, prs, window, percent(atMost)),
		Held: prs >= window && rate <= atMost,
	}
}

// noGateMisses is the clause a window's merged PRs must carry no failed gate in.
func noGateMisses(samples []Sample, window int) Condition {
	return Condition{
		Text: fmt.Sprintf("%d gate misses over %d of %d PRs, zero expected",
			GateMisses(samples), len(samples), window),
		Held: len(samples) >= window && GateMisses(samples) == 0,
	}
}

// cdRecorded is the clause every repository a window's PRs merged into must
// carry a recorded CD flag for. The flags are keyed by lowercased repository,
// the way the store records them.
func cdRecorded(samples []Sample, cd map[string]bool) Condition {
	repos := Repos(samples)
	var missing []string
	for _, repo := range repos {
		if !cd[strings.ToLower(repo)] {
			missing = append(missing, repo)
		}
	}
	named := repos
	if len(missing) > 0 {
		named = missing
	}
	text := "CD recorded for the target repository"
	if len(named) > 0 {
		text = "CD recorded for " + strings.Join(named, ", ")
	}
	return Condition{Text: text, Held: len(repos) > 0 && len(missing) == 0}
}

// notRolledBack is the clause that no rollback condition holds over the
// evidence, so a level the owner sets is not one the next poll takes back.
func notRolledBack(ev Evidence) Condition {
	cause := RollbackFor(AutonomyAutoMerge, ev)
	text := "no rollback condition holds"
	if cause != "" {
		text = fmt.Sprintf("no rollback condition holds, but %s does", cause)
	}
	return Condition{Text: text, Held: cause == ""}
}

// autoMergedAll is the clause a sampled window must be entirely auto-merged, so
// the level was held over it rather than the owner merging by hand.
func autoMergedAll(samples []Sample, window int) Condition {
	n := 0
	for _, s := range samples {
		if s.AutoMerged {
			n++
		}
	}
	return Condition{
		Text: fmt.Sprintf("%d auto-merged over %d of %d sampled PRs, all expected", n, len(samples), window),
		Held: len(samples) >= window && n == len(samples),
	}
}

// allMeasured is the clause no PR of a window carries a commit whose lines were
// not measured: the rate undercounts there, so it must not grant a level.
func allMeasured(samples []Sample, window int) Condition {
	n := 0
	for _, s := range samples {
		if s.UnmeasuredCommits > 0 {
			n++
		}
	}
	return Condition{
		Text: fmt.Sprintf("%d PRs with unmeasured commits over %d of %d PRs, zero expected", n, len(samples), window),
		Held: n == 0,
	}
}

// zeroReverts is the clause a sampled window must carry no revert of an
// auto-merged PR in.
func zeroReverts(samples []Sample, window int) Condition {
	return Condition{
		Text: fmt.Sprintf("%d reverts over %d of %d sampled PRs, zero expected",
			Reverts(samples), len(samples), window),
		Held: len(samples) >= window && Reverts(samples) == 0,
	}
}

// percent renders a rate threshold the way the PRD writes it: "5%", "10%", "20%".
func percent(rate float64) string { return fmt.Sprintf("%g%%", rate*100) }

// Rollback names the condition that returned a level to AutonomyRolledBack.
type Rollback string

const (
	// RollbackReverted is an auto-merged PR a later merged revert names.
	RollbackReverted Rollback = "reverted-auto-merge"
	// RollbackSampledRate is a sampled rewrite rate over RollbackRate.
	RollbackSampledRate Rollback = "sampled-rate"
	// RollbackCeiling is a trailing rewrite rate over CeilingRate.
	RollbackCeiling Rollback = "ceiling-rate"
	// RollbackUnreadable is rollback evidence that could not be read, so the
	// level cannot be shown to hold.
	RollbackUnreadable Rollback = "evidence-unreadable"
)

// RollbackFor reports the condition that returns level to AutonomyRolledBack, or
// "" when none holds. A level that does not auto-merge is never rolled back:
// every condition names a failure of the auto-merge itself. The conditions are
// checked in the order the PRD names them, so a level whose window holds more
// than one is dropped under the first.
func RollbackFor(level Autonomy, ev Evidence) Rollback {
	if !level.AutoMerges() {
		return ""
	}
	if Reverts(ev.Window(SampledPRs)) > 0 {
		return RollbackReverted
	}
	if rate, prs := Rate(ev.Window(SampledPRs)); prs > 0 && rate > RollbackRate {
		return RollbackSampledRate
	}
	if rate, prs := Rate(ev.Window(TrailingPRs)); prs > 0 && rate > CeilingRate {
		return RollbackCeiling
	}
	return ""
}

// Autonomies reads the owner's autonomy level, and drops it when a rollback
// condition holds.
type Autonomies interface {
	Level(ctx context.Context) (Autonomy, error)
	Set(ctx context.Context, level Autonomy, at time.Time) error
}

// readAutonomy is the level a poll's claims run at: the recorded level, capped by
// what the measured PRs have earned.
func readAutonomy(ctx context.Context, d Deps) Autonomy {
	return effectiveLevel(ctx, d, readRecorded(ctx, d))
}

// readRecorded reads the level the owner or a rollback set. A level that cannot
// be read, or names no level, counts as 0, which grants nothing.
func readRecorded(ctx context.Context, d Deps) Autonomy {
	level, err := d.Autonomies.Level(ctx)
	if err == nil && !level.Valid() {
		err = fmt.Errorf("%w %d", ErrUnknownAutonomy, int(level))
	}
	if err != nil {
		d.Logger.Warn("autonomyUnread", "err", err.Error())
		return 0
	}
	return level
}
