package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
)

var noticeAt = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

// noticesOver returns Notices over the emulator, removing the notices with ids
// afterwards.
func noticesOver(t *testing.T, ids ...string) (*Notices, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = client.Collection(dispatchCollection).Doc(noticePrefix + id).Delete(context.Background())
		}
	})
	return NewNotices(client), client
}

// claimedIDs claims at at and returns the ids of the claimed notices among ids,
// so a notice another test left pending cannot decide the result.
func claimedIDs(t *testing.T, n *Notices, at, staleBefore time.Time, ids ...string) []string {
	t.Helper()
	got, err := n.Claim(context.Background(), at, staleBefore)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, notice := range got {
		if slices.Contains(ids, notice.ID) {
			out = append(out, notice.ID)
		}
	}
	return out
}

func TestBreakerTripsOnceAndResets(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	b := NewBreaker(client)
	t.Cleanup(func() { _ = b.Reset(context.Background()) })
	if err := b.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if tripped, err := b.Tripped(ctx); err != nil || tripped {
		t.Fatalf("tripped = %v, %v before any trip", tripped, err)
	}
	if err := b.Trip(ctx, "ABC-1", "identity-mismatch", noticeAt); err != nil {
		t.Fatal(err)
	}
	if err := b.Trip(ctx, "ABC-2", "credential-absent", noticeAt.Add(time.Minute)); err != nil {
		t.Fatalf("a second trip = %v, want it absorbed", err)
	}
	snap, err := client.Collection(dispatchCollection).Doc(breakerDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Data()[runIDField]; got != "ABC-1" {
		t.Fatalf("breaker names run %v, want the first trip kept", got)
	}
	if err := b.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if tripped, err := b.Tripped(ctx); err != nil || tripped {
		t.Fatalf("tripped = %v, %v after a reset", tripped, err)
	}
}

func TestNoticeIsRaisedOnceAndClaimedByOnePoll(t *testing.T) {
	id := fresh("ABC-30")
	n, _ := noticesOver(t, id)
	ctx := context.Background()
	notice := dispatcher.Notice{ID: id, Class: "identity-mismatch", Link: "https://github.com/o/r/actions/runs/7"}
	for range 2 {
		if err := n.Raise(ctx, notice, noticeAt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := n.Claim(ctx, noticeAt, noticeAt.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(got, func(c dispatcher.Notice) bool { return c.ID == id }); i < 0 || got[i] != notice {
		t.Fatalf("claimed %+v, want %+v", got, notice)
	}
	if again := claimedIDs(t, n, noticeAt.Add(time.Minute), noticeAt.Add(-time.Hour), id); len(again) != 0 {
		t.Fatalf("a second poll claimed %v while the first held it", again)
	}
}

func TestAStaleClaimIsTakenAgain(t *testing.T) {
	id := fresh("ABC-31")
	n, _ := noticesOver(t, id)
	if err := n.Raise(context.Background(), dispatcher.Notice{ID: id, Class: "credential-absent"}, noticeAt); err != nil {
		t.Fatal(err)
	}
	if got := claimedIDs(t, n, noticeAt, noticeAt, id); len(got) != 1 {
		t.Fatalf("claimed %v", got)
	}
	if got := claimedIDs(t, n, noticeAt.Add(time.Hour), noticeAt.Add(time.Second), id); len(got) != 1 {
		t.Fatalf("claimed %v, want the stale claim taken again", got)
	}
}

func TestUnclaimReturnsANoticeAndPostedRetiresIt(t *testing.T) {
	unclaimed, posted := fresh("ABC-32"), fresh("ABC-33")
	n, _ := noticesOver(t, unclaimed, posted)
	ctx := context.Background()
	for _, id := range []string{unclaimed, posted} {
		if err := n.Raise(ctx, dispatcher.Notice{ID: id, Class: "allowance-exhausted"}, noticeAt); err != nil {
			t.Fatal(err)
		}
	}
	if got := claimedIDs(t, n, noticeAt, noticeAt.Add(-time.Hour), unclaimed, posted); len(got) != 2 {
		t.Fatalf("claimed %v", got)
	}
	if err := n.Unclaim(ctx, unclaimed); err != nil {
		t.Fatal(err)
	}
	if err := n.Posted(ctx, posted, noticeAt); err != nil {
		t.Fatal(err)
	}
	// A stale-before far in the future would retake any claim: only state decides.
	if got := claimedIDs(t, n, noticeAt.Add(time.Minute), noticeAt.Add(24*time.Hour), unclaimed, posted); !slices.Equal(got, []string{unclaimed}) {
		t.Fatalf("claimed %v, want only the unclaimed notice", got)
	}
}

func TestSpendReadsTheReservedAndTheSettledAtTheMomentAsked(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	settledAt := time.Date(2031, 1, 1, 10, 0, 0, 0, time.UTC)
	settled := queuedRun(fresh("spend-settled"), 1)
	forget(t, client, settled.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, settled); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(settled.RunID).Set(ctx, map[string]any{
		settledAtField:           settledAt,
		settledProviderCostField: int64(4 * money.Dollar),
	}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Set(ctx, ledgerDoc{
		Reservations: map[string]reservationEntry{"in-flight": {ProviderCostMicros: 3 * money.Dollar}}, N: 1,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := dispatcher.BudgetConfig{ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour}}}

	reserved, inside, err := q.Spend(ctx, cfg, "", settledAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reserved.ProviderCost != 3*money.Dollar || inside.Windows[0] != 4*money.Dollar {
		t.Fatalf("reserved %v, settled %v", reserved, inside.Windows)
	}
	_, later, err := q.Spend(ctx, cfg, "", settledAt.Add(6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if later.Windows[0] != 0 {
		t.Fatalf("settled %v, want the run rolled out of the window", later.Windows)
	}
}

func TestALaterAttemptAfterAResetRaisesAndTripsAgain(t *testing.T) {
	run := fresh("ABC-34")
	first, second := run+"-41-1", run+"-42-1"
	n, client := noticesOver(t, first, second)
	ctx := context.Background()
	b := NewBreaker(client)
	t.Cleanup(func() { _ = b.Reset(context.Background()) })
	for _, id := range []string{first, second} {
		if err := n.Raise(ctx, dispatcher.Notice{ID: id, Class: "credential-absent"}, noticeAt); err != nil {
			t.Fatal(err)
		}
		if err := b.Trip(ctx, run, "credential-absent", noticeAt); err != nil {
			t.Fatal(err)
		}
		if got := claimedIDs(t, n, noticeAt, noticeAt.Add(-time.Hour), id); len(got) != 1 {
			t.Fatalf("claimed %v, want %s pending", got, id)
		}
		if tripped, err := b.Tripped(ctx); err != nil || !tripped {
			t.Fatalf("tripped = %v, %v after %s", tripped, err, id)
		}
		if err := b.Reset(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFreshClaimsNeverCrowdOutAPendingNotice(t *testing.T) {
	var held []string
	for range noticeClaimLimit + 1 {
		held = append(held, fresh("ABC-35"))
	}
	pending := fresh("ABC-36")
	n, client := noticesOver(t, append(held, pending)...)
	ctx := context.Background()
	for _, id := range held {
		if _, err := client.Collection(dispatchCollection).Doc(noticePrefix+id).Set(ctx, map[string]any{
			classField: "identity-mismatch", noticeStateField: noticeClaimed, claimedAtField: noticeAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.Raise(ctx, dispatcher.Notice{ID: pending, Class: "credential-absent"}, noticeAt); err != nil {
		t.Fatal(err)
	}
	if got := claimedIDs(t, n, noticeAt.Add(time.Minute), noticeAt.Add(-time.Hour), append(held, pending)...); !slices.Equal(got, []string{pending}) {
		t.Fatalf("claimed %v, want the pending notice alone", got)
	}
}
