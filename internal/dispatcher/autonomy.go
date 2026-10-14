package dispatcher

import (
	"context"
	"errors"
	"fmt"
)

// Autonomy is the owner's autonomy level, 0 to 5: how much the product may do
// without them.
type Autonomy int

// AutonomyAutoMerge is the lowest level at which an eligible PR may merge itself.
const AutonomyAutoMerge Autonomy = 4

var (
	// ErrUnknownAutonomy is a level outside 0 to 5.
	ErrUnknownAutonomy = errors.New("unknown autonomy level")
	// ErrAutonomyNotEarned is a level whose advance condition is not yet measured.
	ErrAutonomyNotEarned = errors.New("autonomy level not earned")
)

// delegatesAtLevel3And4 is the bar levels 3 and 4 share: the PRD states level 4
// as "as level 3", so the two delegate the same runs a day.
const delegatesAtLevel3And4 = 5

// autonomyLevels holds, per level, the runs a day it lets the product delegate
// to itself, what it allows and, for a level Settable refuses, the PRD's
// condition for advancing to it. A level that delegates has its description
// built from its bar, so the number the owner reads is the one enforced.
var autonomyLevels = [...]struct {
	delegates int
	allows    string
	advance   string
}{
	{0, "you delegate and you merge", ""},
	{1, delegatesDescription(1, "and you merge"), ""},
	{2, delegatesDescription(3, "and you merge"), ""},
	{delegatesAtLevel3And4, delegatesDescription(delegatesAtLevel3And4, "and you merge"), ""},
	{delegatesAtLevel3And4, "as level 3, and eligible S and M PRs auto-merge",
		"level 3 held, a rewrite rate of 5% or less over 10 PRs, zero gate misses, and CD on the target repository"},
	{10, delegatesDescription(10, "and eligible PRs auto-merge"),
		"level 4 held over 20 sampled PRs with zero reverts"},
}

// delegatesDescription is what a level that delegates allows, from the bar it
// delegates up to: a bar of one reads in the singular, as the PRD writes it.
func delegatesDescription(delegates int, rest string) string {
	runs := "runs"
	if delegates == 1 {
		runs = "run"
	}
	return fmt.Sprintf("the product delegates up to %d %s a day %s", delegates, runs, rest)
}

// Valid reports whether a names a level.
func (a Autonomy) Valid() bool { return a >= 0 && int(a) < len(autonomyLevels) }

// Describe is what level a allows, or "" for a level that does not exist.
func (a Autonomy) Describe() string {
	if !a.Valid() {
		return ""
	}
	return autonomyLevels[a].allows
}

// AutoMerges reports whether a lets an eligible PR merge itself.
func (a Autonomy) AutoMerges() bool { return a.Valid() && a >= AutonomyAutoMerge }

// Delegates is the runs a day a lets the product delegate to itself: 1, 3, 5, 5
// and 10 at levels 1 to 5, and none at level 0, which delegates nothing.
func (a Autonomy) Delegates() int {
	if !a.Valid() {
		return 0
	}
	return autonomyLevels[a].delegates
}

// Settable reports why the owner may not set a, or nil when they may. Levels
// that auto-merge are refused until their advance conditions are measured
// rather than trusted to the setting.
func (a Autonomy) Settable() error {
	if !a.Valid() {
		return fmt.Errorf("%w %d: levels run 0 to %d", ErrUnknownAutonomy, int(a), len(autonomyLevels)-1)
	}
	if a.AutoMerges() {
		return fmt.Errorf("%w: level %d needs %s, which is not yet measured", ErrAutonomyNotEarned, int(a), autonomyLevels[a].advance)
	}
	return nil
}

// Autonomies reads the owner's autonomy level.
type Autonomies interface {
	Level(ctx context.Context) (Autonomy, error)
}

// readAutonomy reads the level once for a poll. A level that cannot be read, or
// names no level, counts as 0, which grants nothing.
func readAutonomy(ctx context.Context, d Deps) Autonomy {
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
