package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// holdsBase is later than any claim or hold another test writes, so a window
// read counts only the rows these tests write.
var holdsBase = time.Date(2090, 1, 1, 12, 0, 0, 0, time.UTC)

// holdsNow is one test's own read time, each 100 days apart so no two windows
// overlap, after clearing what an interrupted run left in its window: the
// emulator outlives one run of the suite, and this read counts every row in it.
func holdsNow(t *testing.T, client *firestore.Client) time.Time {
	t.Helper()
	now := holdsBase.Add(time.Duration(queueSeq.Add(1)) * 100 * 24 * time.Hour)
	since := dispatcher.HoldWindowStart(now).Add(-48 * time.Hour)
	ctx := context.Background()
	for _, field := range []string{claimedAtField, heldAtField} {
		iter := client.Collection(runsCollection).Where(field, ">=", since).Where(field, "<", now.Add(48*time.Hour)).Documents(ctx)
		for {
			snap, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := snap.Ref.Delete(ctx); err != nil {
				t.Fatal(err)
			}
		}
		iter.Stop()
	}
	return now
}

// seedClaim writes id as a run claimed at at while claimConcurrency runs were
// in flight, itself included.
func seedClaim(t *testing.T, client *firestore.Client, id string, claimConcurrency int, at time.Time) {
	t.Helper()
	if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), map[string]any{
		ticketIDField: id, repoField: "octo/scratch", stateField: stateClaimed,
		claimedAtField: at, claimConcurrencyField: int64(claimConcurrency),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordHoldKeepsOneHoldPerRunReasonAndDay(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("held")
	forget(t, client, id)
	if err := q.Enqueue(ctx, queuedRun(id, 1)); err != nil {
		t.Fatal(err)
	}
	at := queueAt.Add(time.Hour)
	for _, hold := range []struct{ reason, day string }{
		{dispatcher.HoldConcurrency, "2026-09-27"},
		{dispatcher.HoldConcurrency, "2026-09-27"},
		{dispatcher.HoldReviewWIP, "2026-09-27"},
		{dispatcher.HoldConcurrency, "2026-09-28"},
	} {
		if err := q.RecordHold(ctx, id, hold.reason, hold.day, at); err != nil {
			t.Fatal(err)
		}
	}
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(cands, func(c dispatcher.Candidate) bool { return c.RunID == id })
	if i < 0 {
		t.Fatalf("candidates %+v leave out %s", cands, id)
	}
	want := map[string][]string{
		"2026-09-27": {dispatcher.HoldConcurrency, dispatcher.HoldReviewWIP},
		"2026-09-28": {dispatcher.HoldConcurrency},
	}
	if !maps.EqualFunc(cands[i].Held, want, slices.Equal[[]string]) {
		t.Fatalf("held = %v, want %v", cands[i].Held, want)
	}
	snap, err := client.Collection(runsCollection).Doc(id).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := snap.Data()[heldAtField].(time.Time); !got.Equal(at) {
		t.Fatalf("held_at = %v, want %v", got, at)
	}
}

func TestRecordHoldFailsForARunWithNoRow(t *testing.T) {
	q, _ := queue(t)
	if err := q.RecordHold(context.Background(), fresh("held-missing"), dispatcher.HoldBlocked, "2026-09-27", queueAt); err == nil {
		t.Fatal("recorded a hold on a run that has no row")
	}
}

func TestConcurrencyStatsReadsThePeakAndTheHoldsOfTheTrailingWindow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	now := holdsNow(t, client)
	since := dispatcher.HoldWindowStart(now)
	seedLedger(t, client, 2, nil)

	early, first, peak, late := fresh("peak-early"), fresh("peak-first"), fresh("peak-top"), fresh("peak-late")
	held, other, stale := fresh("held-a"), fresh("held-b"), fresh("held-stale")
	forget(t, client, early, first, peak, late, held, other, stale)
	// A claim just before the window counts nothing however many it saw.
	seedClaim(t, client, early, 7, since.Add(-time.Second))
	seedClaim(t, client, first, 1, since)
	seedClaim(t, client, peak, 3, now.Add(-48*time.Hour))
	seedClaim(t, client, late, 2, now)

	today, firstDay := dispatcher.HoldDay(now), dispatcher.HoldDay(since)
	dayBefore := dispatcher.HoldDay(since.Add(-time.Hour))
	for _, id := range []string{held, other, stale} {
		seedRow(t, client, id, map[string]any{ticketIDField: id, stateField: stateQueued})
	}
	for _, hold := range []struct{ id, reason, day string }{
		{held, dispatcher.HoldConcurrency, today},
		{held, dispatcher.HoldConcurrency, firstDay},
		{held, dispatcher.HoldRepoBusy, today},
		{other, dispatcher.HoldConcurrency, today},
		// A day before the window is not counted, though its row was held since.
		{other, dispatcher.HoldBudget, dayBefore},
		{stale, dispatcher.HoldBlocked, dayBefore},
	} {
		at := now
		if hold.id == stale {
			at = since.Add(-time.Hour)
		}
		if err := q.RecordHold(ctx, hold.id, hold.reason, hold.day, at); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := q.ConcurrencyStats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Peak != 3 || stats.N != 2 {
		t.Fatalf("peak %d at N %d, want 3 at 2", stats.Peak, stats.N)
	}
	want := map[string]int{dispatcher.HoldConcurrency: 3, dispatcher.HoldRepoBusy: 1}
	if !maps.Equal(stats.Held, want) {
		t.Fatalf("held = %v, want %v", stats.Held, want)
	}
}

func TestConcurrencyStatsReadsNothingFromAnEmptyWindow(t *testing.T) {
	q, client := queue(t)
	now := holdsNow(t, client)
	seedLedger(t, client, 0, nil)
	stats, err := q.ConcurrencyStats(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	// An unset N reads as the one run the rule admits.
	if stats.Peak != 0 || stats.N != 1 || len(stats.Held) != 0 {
		t.Fatalf("stats = %+v, want no peak, N 1 and no holds", stats)
	}
}
