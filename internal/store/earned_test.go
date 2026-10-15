package store

import (
	"context"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

func TestEarnedIsAbsentUntilStoredThenReadBackBesideTheLevel(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	if _, found, err := a.Earned(ctx); err != nil || found {
		t.Fatalf("found = %v, %v, want no snapshot before one is stored", found, err)
	}
	if err := a.Set(ctx, 3, noticeAt); err != nil {
		t.Fatal(err)
	}
	if _, found, err := a.Earned(ctx); err != nil || found {
		t.Fatalf("found = %v, %v, want a level alone to be no snapshot", found, err)
	}
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	want := dispatcher.Earned{Level: 4, ComputedAt: at, Numbers: dispatcher.Numbers{
		PRs: []int{9, 8}, Rate: 0.02, AgentLines: 100, OwnerLines: 2, ReviewFoundLines: 1, OwnerChangeLines: 1, UnlabelledLines: 0, Reverts: 0,
	}}
	if err := a.SetEarned(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := a.Earned(ctx)
	if err != nil || !found || got.Level != 4 || !got.ComputedAt.Equal(at) || got.Stale ||
		len(got.Numbers.PRs) != 2 || got.Numbers.PRs[1] != 8 || got.Numbers.Rate != 0.02 || got.Numbers.ReviewFoundLines != 1 || got.Numbers.NotRecorded {
		t.Fatalf("Earned = %+v, %v, %v, want %+v", got, found, err, want)
	}
	if level, err := a.Level(ctx); err != nil || level != 3 {
		t.Fatalf("level = %d, %v, want the owner's level untouched by the snapshot", level, err)
	}
	snap, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Get(ctx)
	if err != nil || snap.Data()[levelField] != int64(3) {
		t.Fatalf("stored = %v, %v, want one document holding the level and the snapshot", snap.Data(), err)
	}
}

func TestSetKeepsTheEarnedSnapshot(t *testing.T) {
	a, _ := autonomiesOver(t)
	ctx := context.Background()
	if err := a.SetEarned(ctx, dispatcher.Earned{Level: 4, ComputedAt: noticeAt}); err != nil {
		t.Fatal(err)
	}
	if err := a.Set(ctx, 2, noticeAt); err != nil {
		t.Fatal(err)
	}
	if got, found, err := a.Earned(ctx); err != nil || !found || got.Level != 4 {
		t.Fatalf("Earned = %+v, %v, %v, want the owner's write to leave it", got, found, err)
	}
}

func TestMarkStaleIsClearedByTheNextSnapshot(t *testing.T) {
	a, _ := autonomiesOver(t)
	ctx := context.Background()
	if err := a.SetEarned(ctx, dispatcher.Earned{Level: 4, ComputedAt: noticeAt}); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkStale(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _, err := a.Earned(ctx); err != nil || !got.Stale {
		t.Fatalf("Earned = %+v, %v, want it stale", got, err)
	}
	if err := a.SetEarned(ctx, dispatcher.Earned{Level: 4, ComputedAt: noticeAt}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := a.Earned(ctx); err != nil || got.Stale {
		t.Fatalf("Earned = %+v, %v, want a recomputation to clear the mark", got, err)
	}
}

func TestEarnedWithUnreadableFieldsIsAnError(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	if _, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Set(ctx, map[string]any{earnedField: "four"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Earned(ctx); err == nil {
		t.Fatal("want a snapshot that does not read back reported, so a caller fails closed")
	}
}

func TestEarnedAndHistoryWithNoStoredEvidenceReadAsNotRecorded(t *testing.T) {
	a, client := autonomiesOver(t)
	ctx := context.Background()
	h := NewAutonomyHistory(client)
	clearHistory(t, h)
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	if _, err := client.Collection(dispatchCollection).Doc(autonomyDocID).Set(ctx, map[string]any{
		earnedField: int64(3), earnedEvidenceField: map[string]any{}, computedAtField: at,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.changes().NewDoc().Set(ctx, map[string]any{
		historyFromField: int64(3), historyToField: int64(4), historyCauseField: "advance", historyAtField: at,
	}); err != nil {
		t.Fatal(err)
	}
	got, found, err := a.Earned(ctx)
	if err != nil || !found || !got.Numbers.NotRecorded {
		t.Fatalf("Earned = %+v, %v, %v, want an empty evidence map read as not recorded", got, found, err)
	}
	changes, err := h.List(ctx, 10)
	if err != nil || len(changes) != 1 || !changes[0].Numbers.NotRecorded {
		t.Fatalf("List = %+v, %v, want a change with no evidence read as not recorded", changes, err)
	}
}

func TestShadowStartIsRecordedOnceAndCountsUp(t *testing.T) {
	a, _ := autonomiesOver(t)
	ctx := context.Background()
	first := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	got, err := a.ShadowStart(ctx, first)
	if err != nil || !got.Equal(first) {
		t.Fatalf("ShadowStart = %v, %v, want it recorded now", got, err)
	}
	if got, err := a.ShadowStart(ctx, first.Add(time.Hour)); err != nil || !got.Equal(first) {
		t.Fatalf("ShadowStart = %v, %v, want the first marker kept", got, err)
	}
	for want := 1; want <= 3; want++ {
		if n, err := a.CountShadow(ctx); err != nil || n != want {
			t.Fatalf("CountShadow = %d, %v, want %d", n, err, want)
		}
	}
}

func TestAutonomyHistoryAppendsOneDocumentPerChangeNewestFirst(t *testing.T) {
	_, client := autonomiesOver(t)
	ctx := context.Background()
	h := NewAutonomyHistory(client)
	clearHistory(t, h)
	first := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for i, c := range []dispatcher.Change{
		{From: 3, To: 4, Cause: dispatcher.ChangeAdvance, Numbers: dispatcher.Numbers{PRs: []int{5}, AgentLines: 19}, At: first},
		{From: 4, To: 3, Cause: string(dispatcher.RollbackReverted), At: first.Add(time.Hour)},
	} {
		if err := h.Append(ctx, c); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := h.List(ctx, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("List = %+v, %v, want two changes", got, err)
	}
	if got[0].Cause != string(dispatcher.RollbackReverted) || got[1].Cause != dispatcher.ChangeAdvance ||
		got[1].From != 3 || got[1].To != 4 || got[1].Numbers.AgentLines != 19 || len(got[1].Numbers.PRs) != 1 {
		t.Fatalf("List = %+v, want the rollback first and the advance read back whole", got)
	}
}

// clearHistory removes the history the emulator holds, before and after a test.
func clearHistory(t *testing.T, h *AutonomyHistory) {
	t.Helper()
	wipe := func() {
		snaps, _ := h.changes().Documents(context.Background()).GetAll()
		for _, s := range snaps {
			_, _ = s.Ref.Delete(context.Background())
		}
	}
	wipe()
	t.Cleanup(wipe)
}
