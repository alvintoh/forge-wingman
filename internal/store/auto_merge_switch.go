package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	autoMergeSwitchDocID = "auto-merge"
	enabledField         = "enabled"
	barField             = "bar"
	escapedPRField       = "escaped_pr"
	escapedRepoField     = "escaped_repo"
	escapedByField       = "escaped_by"
	escapedReasonField   = "escaped_reason"
	escapedAtField       = "escaped_at"
)

// The bar's own bounds and the value it takes while the owner has set none.
const (
	DefaultBarPercent = dispatcher.DefaultAutoMergeBarPercent
	MinBarPercent     = 1
	MaxBarPercent     = 100
)

// ErrBarNotInRange reports a stored bar that is not a whole number from 1 to
// 100. It holds auto-merge off, and a caller says so rather than guessing a bar.
var ErrBarNotInRange = errors.New("the bar is not a whole number from 1 to 100")

// AutoMergeSetting is the owner's auto-merge document: the switch, the bar the
// trailing rewrite rate is measured against, and the auto-merged PR a later
// merge reverted or fixed, which holds auto-merge off until the owner switches
// it on again.
type AutoMergeSetting struct {
	// Enabled is the owner's switch, on while no document is stored.
	Enabled bool
	// BarPercent is the bar as a percentage, 1 to 100.
	BarPercent int
	// Escaped is the escape latch, its PR zero when none holds.
	Escaped dispatcher.MergeEscape
}

// AutoMergeSwitches keeps the dispatch/auto-merge document the owner sets and every pr-meta and the surface read.
type AutoMergeSwitches struct {
	client *firestore.Client
}

// NewAutoMergeSwitches returns an AutoMergeSwitches backed by client.
func NewAutoMergeSwitches(client *firestore.Client) *AutoMergeSwitches {
	return &AutoMergeSwitches{client: client}
}

func (a *AutoMergeSwitches) doc() *firestore.DocumentRef {
	return a.client.Collection(dispatchCollection).Doc(autoMergeSwitchDocID)
}

// Read returns the owner's switch, the bar and the escape latch in one read, on,
// at DefaultBarPercent and holding nothing while no document is stored. A stored
// value that is not what it says is an error rather than a guess: a bar of the
// wrong shape wraps ErrBarNotInRange, and an unreadable document is returned as
// it is.
func (a *AutoMergeSwitches) Read(ctx context.Context) (AutoMergeSetting, error) {
	out := AutoMergeSetting{Enabled: true, BarPercent: DefaultBarPercent}
	snap, err := a.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return out, nil
	}
	if err != nil {
		return AutoMergeSetting{}, fmt.Errorf("reading the auto-merge settings: %w", err)
	}
	row := snap.Data()
	// A document written by the escape latch alone carries no switch; absent is
	// on, as no document is, while a stored value that is not a boolean is an
	// error so a caller does not guess what it meant.
	if raw, stored := row[enabledField]; stored {
		enabled, ok := raw.(bool)
		if !ok {
			return AutoMergeSetting{}, fmt.Errorf("reading the auto-merge settings: %q is not a boolean", enabledField)
		}
		out.Enabled = enabled
	}
	if raw, stored := row[barField]; stored && raw != nil {
		n, whole := wholeNumber(raw)
		if !whole || n < MinBarPercent || n > MaxBarPercent {
			return AutoMergeSetting{}, fmt.Errorf("reading the auto-merge settings: %w (%v)", ErrBarNotInRange, raw)
		}
		out.BarPercent = n
	}
	out.Escaped = escapedOf(row)
	return out, nil
}

// wholeNumber reads a stored number as a whole one: Firestore returns whole
// numbers as int64, and a float is accepted only when it is one.
func wholeNumber(value any) (int, bool) {
	switch n := value.(type) {
	case int64:
		return int(n), true
	case int:
		return n, true
	case float64:
		if math.IsInf(n, 0) || math.IsNaN(n) || n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

// escapedOf reads the escape latch a stored document carries, its PR zero when
// none holds.
func escapedOf(row map[string]any) dispatcher.MergeEscape {
	pr, _ := row[escapedPRField].(int64)
	by, _ := row[escapedByField].(int64)
	e := dispatcher.MergeEscape{
		Repo:   str(row, escapedRepoField),
		PR:     int(pr),
		By:     int(by),
		Reason: str(row, escapedReasonField),
	}
	e.At, _ = row[escapedAtField].(time.Time)
	return e
}

// SetEnabled stores the switch, effective from the next pr-meta, and clears the
// escape latch when it is switched on: the owner switching it on again is what
// acknowledges the escaped PR.
func (a *AutoMergeSwitches) SetEnabled(ctx context.Context, enabled bool, at time.Time) error {
	write := map[string]any{enabledField: enabled, setAtField: at}
	if enabled {
		for _, field := range []string{escapedPRField, escapedRepoField, escapedByField, escapedReasonField, escapedAtField} {
			write[field] = firestore.Delete
		}
	}
	if _, err := a.doc().Set(ctx, write, firestore.MergeAll); err != nil {
		return fmt.Errorf("setting the auto-merge switch: %w", err)
	}
	return nil
}

// SetBar stores the bar the trailing rewrite rate is measured against, as a
// percentage from 1 to 100, effective from the next pr-meta.
func (a *AutoMergeSwitches) SetBar(ctx context.Context, percent int, at time.Time) error {
	if percent < MinBarPercent || percent > MaxBarPercent {
		return fmt.Errorf("setting the auto-merge bar: %d is %w", percent, ErrBarNotInRange)
	}
	if _, err := a.doc().Set(ctx, map[string]any{barField: percent, setAtField: at}, firestore.MergeAll); err != nil {
		return fmt.Errorf("setting the auto-merge bar: %w", err)
	}
	return nil
}

// RecordEscape records the escaped PR the latch names, reporting whether this is
// the first record of it: the first escape holds auto-merge off until the owner
// switches it on again, so a later one does not replace it.
func (a *AutoMergeSwitches) RecordEscape(ctx context.Context, e dispatcher.MergeEscape) (bool, error) {
	fresh := false
	err := a.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		fresh = false
		snap, err := tx.Get(a.doc())
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if err == nil {
			if pr, _ := snap.Data()[escapedPRField].(int64); pr > 0 {
				return nil
			}
		}
		fresh = true
		return tx.Set(a.doc(), map[string]any{
			escapedPRField:     int64(e.PR),
			escapedRepoField:   e.Repo,
			escapedByField:     int64(e.By),
			escapedReasonField: e.Reason,
			escapedAtField:     e.At,
		}, firestore.MergeAll)
	})
	if err != nil {
		return false, fmt.Errorf("recording the escaped pull request: %w", err)
	}
	return fresh, nil
}
