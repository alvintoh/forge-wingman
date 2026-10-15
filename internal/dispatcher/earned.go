package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The tunables the earned level reads.
const (
	// DefaultShadowPRs is how many merged runner PRs after the shadow began pass
	// without the advance into level 4 happening.
	DefaultShadowPRs = 10
)

// The causes a level change is recorded under besides a Rollback's own.
const (
	// ChangeAdvance is a level the dispatcher advanced to once it was earned.
	ChangeAdvance = "advance"
	// ChangeOwnerSet is a level the owner set.
	ChangeOwnerSet = "owner-set"
)

// Earned is the level the measured PRs have earned, as last computed.
type Earned struct {
	// Level is the highest level whose advance condition holds.
	Level Autonomy
	// Numbers is the evidence it was computed from.
	Numbers Numbers
	// ComputedAt is when it was computed.
	ComputedAt time.Time
	// Stale is set when a recomputation failed: it allows no auto-merge.
	Stale bool
}

// EarnedSnapshots stores the earned level beside the owner's level.
type EarnedSnapshots interface {
	// Earned returns the snapshot, false when none is stored.
	Earned(ctx context.Context) (Earned, bool, error)
	// SetEarned replaces the snapshot, clearing a stale mark.
	SetEarned(ctx context.Context, e Earned) error
	// MarkStale marks the stored snapshot stale.
	MarkStale(ctx context.Context) error
}

// ShadowCounter counts the merged runner PRs measured since the shadow began.
type ShadowCounter interface {
	// ShadowStart returns when the shadow began, recording at when it has not.
	ShadowStart(ctx context.Context, at time.Time) (time.Time, error)
	// CountShadow counts one more merged PR and returns the count with it.
	CountShadow(ctx context.Context) (int, error)
}

// CDFlags reads whether each repository was recorded as deploying on merge.
type CDFlags interface {
	Recorded(ctx context.Context) (map[string]bool, error)
}

// Change is one change of the autonomy level, as the history records it.
type Change struct {
	From, To Autonomy
	// Cause is ChangeAdvance, ChangeOwnerSet or a Rollback.
	Cause string
	// Numbers is the evidence at the moment of the change.
	Numbers Numbers
	At      time.Time
}

// AutonomyHistory is the append-only record of level changes.
type AutonomyHistory interface {
	Append(ctx context.Context, c Change) error
}

// EarnedLevel is the highest level whose advance condition holds over ev, and
// the numbers of its trailing window. The recorded level is not a condition
// here: the snapshot says what is earned, not what is set.
func EarnedLevel(ev Evidence) (Autonomy, Numbers) {
	numbers := NumbersOf(ev.Window(TrailingPRs))
	for _, level := range []Autonomy{AutonomyAutoMerge + 1, AutonomyAutoMerge} {
		at := ev
		at.Level = level - 1
		if AllHeld(level.Advance(at)) {
			return level, numbers
		}
	}
	return AutonomyRolledBack, numbers
}

// snapshotWriter is what a recomputation stores through.
type snapshotWriter interface {
	SetEarned(ctx context.Context, e Earned) error
	MarkStale(ctx context.Context) error
}

// Recompute computes the earned level from ev and stores it. A failure to store
// it marks the stored snapshot stale at once, so the level it allows drops to 3
// rather than waiting on a number nobody could refresh.
func Recompute(ctx context.Context, w snapshotWriter, ev Evidence, at time.Time) (Earned, error) {
	level, numbers := EarnedLevel(ev)
	e := Earned{Level: level, Numbers: numbers, ComputedAt: at}
	if err := w.SetEarned(ctx, e); err != nil {
		err = fmt.Errorf("storing the earned level: %w", err)
		if staleErr := w.MarkStale(ctx); staleErr != nil {
			err = errors.Join(err, fmt.Errorf("marking it stale: %w", staleErr))
		}
		return Earned{}, err
	}
	return e, nil
}

// effectiveLevel is the lower of the recorded level and the earned one. A level
// that auto-merges is capped at AutonomyRolledBack when the snapshot is missing,
// unreadable, invalid or stale: it fails closed.
func effectiveLevel(ctx context.Context, d Deps, recorded Autonomy) Autonomy {
	if !recorded.AutoMerges() {
		return recorded
	}
	snap, ok, err := d.Earned.Earned(ctx)
	switch {
	case err != nil:
		d.Logger.Warn("earnedUnread", "err", err.Error())
		return AutonomyRolledBack
	case !ok || !snap.Level.Valid():
		d.Logger.Warn("earnedUnusable", "found", ok)
		return AutonomyRolledBack
	case snap.Stale:
		d.Logger.Warn("earnedStale", "computedAt", snap.ComputedAt)
		return AutonomyRolledBack
	}
	return min(recorded, snap.Level)
}

// freshMerge is a merged runner PR measured this poll, with its place in the
// shadow count, 0 for a PR that merged before the shadow began or was not counted.
type freshMerge struct {
	PR      int
	Ordinal int
}

// refreshEarned recomputes the earned level from the measured PRs and, when the
// level is 3 and a merge measured this poll was past the shadow, advances it to
// 4 if that level's condition holds. A merge inside the shadow only logs that it
// would have.
func refreshEarned(ctx context.Context, d Deps, level Autonomy, fresh []freshMerge) error {
	samples, err := d.Rewrites.Recent(ctx, SampledPRs)
	if err == nil {
		var cd map[string]bool
		if cd, err = d.CDs.Recorded(ctx); err != nil {
			err = fmt.Errorf("reading the recorded CD flags: %w", err)
		} else {
			return recomputeAndAdvance(ctx, d, Evidence{Level: level, Trailing: samples, CD: cd}, fresh)
		}
	}
	// The snapshot cannot be refreshed, so it stops granting auto-merge now.
	if staleErr := d.Earned.MarkStale(ctx); staleErr != nil {
		err = errors.Join(err, fmt.Errorf("marking the earned level stale: %w", staleErr))
	}
	return err
}

func recomputeAndAdvance(ctx context.Context, d Deps, ev Evidence, fresh []freshMerge) error {
	earned, err := Recompute(ctx, d.Earned, ev, d.Now())
	if err != nil {
		return err
	}
	if ev.Level != AutonomyRolledBack || !AllHeld(AutonomyAutoMerge.Advance(ev)) {
		return nil
	}
	advance := false
	for _, m := range fresh {
		if m.Ordinal <= 0 {
			// A merge that predates the shadow, or one with no marker to count
			// from, counts toward nothing and advances nothing.
			continue
		}
		if m.Ordinal <= DefaultShadowPRs {
			d.Logger.Info("autonomyWouldAdvance", "level", int(AutonomyAutoMerge), "pr", m.PR, "shadowPR", m.Ordinal,
				"rate", earned.Numbers.Rate, "agentLines", earned.Numbers.AgentLines, "ownerLines", earned.Numbers.OwnerLines)
			continue
		}
		advance = true
	}
	if !advance {
		return nil
	}
	return advanceLevel(ctx, d, ev.Level, AutonomyAutoMerge, earned.Numbers)
}

// advanceLevel sets the level to next, records the change and raises the one
// notice naming the level and the numbers.
func advanceLevel(ctx context.Context, d Deps, from, next Autonomy, numbers Numbers) error {
	if err := d.Autonomies.Set(ctx, next, d.Now()); err != nil {
		return fmt.Errorf("advancing the autonomy level to %d: %w", int(next), err)
	}
	d.Logger.Info("levelAdvanced", "from", int(from), "to", int(next))
	var errs []error
	if err := d.History.Append(ctx, Change{From: from, To: next, Cause: ChangeAdvance, Numbers: numbers, At: d.Now()}); err != nil {
		errs = append(errs, fmt.Errorf("recording the advance: %w", err))
	}
	notice := Notice{
		ID: fmt.Sprintf("level-advance-%d-%d", int(next), d.Now().Unix()),
		Class: fmt.Sprintf("level-advance: level %d to %d, rewrite rate %.1f%% over %d PRs (%d owner lines of %d agent lines)",
			int(from), int(next), numbers.Rate*100, len(numbers.PRs), numbers.OwnerLines, numbers.AgentLines),
	}
	if err := d.Notices.Raise(ctx, notice, d.Now()); err != nil {
		errs = append(errs, fmt.Errorf("raising the advance notice: %w", err))
	}
	return errors.Join(errs...)
}
