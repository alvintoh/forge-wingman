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

// autonomyLevels holds, per level, what it allows and, for a level Settable
// refuses, the PRD's condition for advancing to it.
var autonomyLevels = [...]struct{ allows, advance string }{
	{"you delegate and you merge", ""},
	{"the product delegates up to 1 run a day and you merge", ""},
	{"the product delegates up to 3 runs a day and you merge", ""},
	{"the product delegates up to 5 runs a day and you merge", ""},
	{"as level 3, and eligible S and M PRs auto-merge",
		"level 3 held, a rewrite rate of 5% or less over 10 PRs, zero gate misses, and CD on the target repository"},
	{"the product delegates up to 10 runs a day and eligible PRs auto-merge",
		"level 4 held over 20 sampled PRs with zero reverts"},
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
