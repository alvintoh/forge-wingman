package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// The earned-level snapshot and the shadow's marker and count live in the
// dispatch/autonomy document, beside the level the owner sets; the owner's
// writes merge into it and never replace it.
const (
	earnedField         = "earned"
	earnedEvidenceField = "evidence"
	computedAtField     = "computed_at"
	staleField          = "stale"
	shadowStartField    = "shadow_started_at"
	shadowPRsField      = "shadow_prs"
)

// Earned returns the stored earned-level snapshot, false when none is stored. A
// snapshot whose fields do not read back is an error, so a caller fails closed
// rather than guess what it meant.
func (a *Autonomies) Earned(ctx context.Context) (dispatcher.Earned, bool, error) {
	snap, err := a.doc().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return dispatcher.Earned{}, false, nil
	}
	if err != nil {
		return dispatcher.Earned{}, false, fmt.Errorf("reading the earned level: %w", err)
	}
	data := snap.Data()
	raw, present := data[earnedField]
	if !present {
		return dispatcher.Earned{}, false, nil
	}
	level, ok := raw.(int64)
	at, atOK := data[computedAtField].(time.Time)
	evidence, evOK := data[earnedEvidenceField].(map[string]any)
	if !ok || !atOK || !evOK {
		return dispatcher.Earned{}, false, fmt.Errorf("reading the earned level: fields %v do not read back", data)
	}
	stale, _ := data[staleField].(bool)
	return dispatcher.Earned{Level: dispatcher.Autonomy(level), Numbers: numbersFrom(evidence), ComputedAt: at, Stale: stale}, true, nil
}

// SetEarned replaces the earned-level snapshot, clearing a stale mark, and
// leaves the owner's level as it is.
func (a *Autonomies) SetEarned(ctx context.Context, e dispatcher.Earned) error {
	_, err := a.doc().Set(ctx, map[string]any{
		earnedField:         int64(e.Level),
		earnedEvidenceField: numbersMap(e.Numbers),
		computedAtField:     e.ComputedAt,
		staleField:          false,
	}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("storing the earned level: %w", err)
	}
	return nil
}

// MarkStale marks the snapshot stale, so it allows no auto-merge until it is
// recomputed.
func (a *Autonomies) MarkStale(ctx context.Context) error {
	_, err := a.doc().Set(ctx, map[string]any{staleField: true}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("marking the earned level stale: %w", err)
	}
	return nil
}

// ShadowStart returns when the shadow began, recording at when none is recorded.
func (a *Autonomies) ShadowStart(ctx context.Context, at time.Time) (time.Time, error) {
	start := at
	err := a.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(a.doc())
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if err == nil {
			if recorded, ok := snap.Data()[shadowStartField].(time.Time); ok {
				start = recorded
				return nil
			}
		}
		return tx.Set(a.doc(), map[string]any{shadowStartField: at}, firestore.MergeAll)
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("reading when the shadow began: %w", err)
	}
	return start, nil
}

// CountShadow counts one more merged PR since the shadow began and returns the
// count with it.
func (a *Autonomies) CountShadow(ctx context.Context) (int, error) {
	var count int64
	err := a.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(a.doc())
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		count = 0
		if err == nil {
			count, _ = snap.Data()[shadowPRsField].(int64)
		}
		count++
		return tx.Set(a.doc(), map[string]any{shadowPRsField: count}, firestore.MergeAll)
	})
	if err != nil {
		return 0, fmt.Errorf("counting a merge toward the shadow: %w", err)
	}
	return int(count), nil
}

// numbersMap is the stored form of n.
func numbersMap(n dispatcher.Numbers) map[string]any {
	prs := make([]int64, len(n.PRs))
	for i, pr := range n.PRs {
		prs[i] = int64(pr)
	}
	return map[string]any{
		"rate": n.Rate, "prs": prs,
		"agent_lines": n.AgentLines, "owner_lines": n.OwnerLines,
		"review_found_lines": n.ReviewFoundLines, "owner_change_lines": n.OwnerChangeLines,
		"unlabelled_lines": n.UnlabelledLines, "reverts": int64(n.Reverts), "unavailable": n.Unavailable,
	}
}

// numbersFrom reads n back from its stored form; a field it lacks reads as zero.
func numbersFrom(m map[string]any) dispatcher.Numbers {
	i64 := func(k string) int64 { v, _ := m[k].(int64); return v }
	n := dispatcher.Numbers{
		AgentLines: i64("agent_lines"), OwnerLines: i64("owner_lines"),
		ReviewFoundLines: i64("review_found_lines"), OwnerChangeLines: i64("owner_change_lines"),
		UnlabelledLines: i64("unlabelled_lines"), Reverts: int(i64("reverts")),
	}
	n.Rate, _ = m["rate"].(float64)
	n.Unavailable, _ = m["unavailable"].(bool)
	if prs, ok := m["prs"].([]any); ok {
		for _, pr := range prs {
			if v, ok := pr.(int64); ok {
				n.PRs = append(n.PRs, int(v))
			}
		}
	}
	return n
}
