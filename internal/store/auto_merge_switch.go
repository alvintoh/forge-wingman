package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	autoMergeSwitchDocID = "auto-merge"
	enabledField         = "enabled"
	escapedPRField       = "escaped_pr"
	escapedRepoField     = "escaped_repo"
	escapedByField       = "escaped_by"
	escapedReasonField   = "escaped_reason"
	escapedAtField       = "escaped_at"
	clearedEscapesField  = "cleared_escapes"
)

// AutoMergeSetting is the owner's auto-merge document: the switch, and the
// auto-merged PR a later merge reverted or fixed, which holds auto-merge off
// until the owner switches it on again.
type AutoMergeSetting struct {
	// Enabled is the owner's switch, on while no document is stored.
	Enabled bool
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

// Read returns the owner's switch and the escape latch in one read, on and
// holding nothing while no document is stored. Any other field a stored document
// carries is left unread, a bar among them: the rewrite rate is a guideline and
// the switch alone gates auto-merge. A stored value that is not what it says is
// an error rather than a guess — a switch of the wrong shape, or one missing from
// a document — and an unreadable document is returned as it is.
func (a *AutoMergeSwitches) Read(ctx context.Context) (AutoMergeSetting, error) {
	out := AutoMergeSetting{Enabled: true}
	snap, err := a.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return out, nil
	}
	if err != nil {
		return AutoMergeSetting{}, fmt.Errorf("reading the auto-merge settings: %w", err)
	}
	row := snap.Data()
	// A stored document always carries the switch: the writers materialise it
	// when they create the document. One without it is a switch key lost or
	// renamed, which reads as an error rather than as on.
	if raw, stored := row[enabledField]; stored {
		enabled, ok := raw.(bool)
		if !ok {
			return AutoMergeSetting{}, fmt.Errorf("reading the auto-merge settings: %q is not a boolean", enabledField)
		}
		out.Enabled = enabled
	} else if len(row) > 0 {
		return AutoMergeSetting{}, fmt.Errorf("reading the auto-merge settings: %q is missing from a stored document", enabledField)
	}
	out.Escaped = escapedOf(row)
	return out, nil
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

// escapeSnapshot reads the stored document inside tx: its fields, and whether a
// document exists at all.
func (a *AutoMergeSwitches) escapeSnapshot(tx *firestore.Transaction) (map[string]any, bool, error) {
	snap, err := tx.Get(a.doc())
	if status.Code(err) == codes.NotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return snap.Data(), true, nil
}

// clearedOf reads the escaped PRs the owner has acknowledged, as EscapeKey names them.
func clearedOf(row map[string]any) []string {
	list, _ := row[clearedEscapesField].([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if key, ok := v.(string); ok {
			out = append(out, key)
		}
	}
	return out
}

// State reads what the document holds about escapes: whether one is latched and
// which escaped PRs the owner has acknowledged.
func (a *AutoMergeSwitches) State(ctx context.Context) (dispatcher.EscapeState, error) {
	snap, err := a.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return dispatcher.EscapeState{}, nil
	}
	if err != nil {
		return dispatcher.EscapeState{}, fmt.Errorf("reading the escaped pull requests: %w", err)
	}
	row := snap.Data()
	return dispatcher.EscapeState{Latched: escapedOf(row).PR > 0, Cleared: clearedOf(row)}, nil
}

// SetEnabled stores the switch, effective from the next pr-meta. Switching on
// clears the escape latch and remembers the PR it named as acknowledged: the
// owner switching it on again is what accepts that escape, and the same revert or
// fix is read again on every poll, so without the memory it would latch again.
func (a *AutoMergeSwitches) SetEnabled(ctx context.Context, enabled bool, at time.Time) error {
	err := a.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		write := map[string]any{enabledField: enabled, setAtField: at}
		if enabled {
			row, _, err := a.escapeSnapshot(tx)
			if err != nil {
				return err
			}
			if latched := escapedOf(row); latched.PR > 0 {
				write[clearedEscapesField] = firestore.ArrayUnion(dispatcher.EscapeKey(latched.Repo, latched.PR))
			}
			for _, field := range []string{escapedPRField, escapedRepoField, escapedByField, escapedReasonField, escapedAtField} {
				write[field] = firestore.Delete
			}
		}
		return tx.Set(a.doc(), write, firestore.MergeAll)
	})
	if err != nil {
		return fmt.Errorf("setting the auto-merge switch: %w", err)
	}
	return nil
}

// RecordEscape records the escaped PR the latch names, reporting whether this is
// the first record of it: the first escape holds auto-merge off until the owner
// switches it on again, so a later one does not replace it, and a PR the owner
// has already acknowledged does not latch again.
func (a *AutoMergeSwitches) RecordEscape(ctx context.Context, e dispatcher.MergeEscape) (bool, error) {
	fresh := false
	err := a.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		fresh = false
		row, exists, err := a.escapeSnapshot(tx)
		if err != nil {
			return err
		}
		if escapedOf(row).PR > 0 || slices.Contains(clearedOf(row), dispatcher.EscapeKey(e.Repo, e.PR)) {
			return nil
		}
		fresh = true
		write := map[string]any{
			escapedPRField:     int64(e.PR),
			escapedRepoField:   e.Repo,
			escapedByField:     int64(e.By),
			escapedReasonField: e.Reason,
			escapedAtField:     e.At,
		}
		if !exists {
			write[enabledField] = true
		}
		return tx.Set(a.doc(), write, firestore.MergeAll)
	})
	if err != nil {
		return false, fmt.Errorf("recording the escaped pull request: %w", err)
	}
	return fresh, nil
}
