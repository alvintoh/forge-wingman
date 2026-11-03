package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
)

// runsAtOnceOver returns RunsAtOnce over the emulator with no document, and
// removes the document afterwards.
func runsAtOnceOver(t *testing.T) (*RunsAtOnce, *firestore.Client) {
	t.Helper()
	_, client := queue(t)
	doc := client.Collection(settingsCollection).Doc(runsAtOnceDocID)
	_, _ = doc.Delete(context.Background())
	t.Cleanup(func() { _, _ = doc.Delete(context.Background()) })
	return NewRunsAtOnce(client), client
}

func TestRunsAtOnceDefaultsToTwoWhileNoneIsSet(t *testing.T) {
	r, _ := runsAtOnceOver(t)
	if n, err := r.Value(context.Background()); err != nil || n != 2 {
		t.Fatalf("value = %d, %v, want 2 with no document", n, err)
	}
}

func TestRunsAtOnceSetIsReadBackAndRecordsWhen(t *testing.T) {
	r, client := runsAtOnceOver(t)
	ctx := context.Background()
	for _, n := range []int{1, 3, 20} {
		if err := r.Set(ctx, n, noticeAt); err != nil {
			t.Fatal(err)
		}
		if got, err := r.Value(ctx); err != nil || got != n {
			t.Fatalf("value = %d, %v, want %d", got, err, n)
		}
	}
	snap, err := client.Collection(settingsCollection).Doc(runsAtOnceDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored := snap.Data(); stored[runsAtOnceField] != int64(20) || !stored[setAtField].(time.Time).Equal(noticeAt) {
		t.Fatalf("stored = %v, want 20 set at %v", stored, noticeAt)
	}
}

func TestRunsAtOnceRefusesAValueUnderOne(t *testing.T) {
	r, client := runsAtOnceOver(t)
	ctx := context.Background()
	for _, n := range []int{0, -1} {
		if err := r.Set(ctx, n, noticeAt); err == nil {
			t.Fatalf("Set(%d) succeeded, want it refused", n)
		}
	}
	if _, err := client.Collection(settingsCollection).Doc(runsAtOnceDocID).Get(ctx); err == nil {
		t.Fatal("a refused Set wrote the document")
	}
	for _, stored := range []any{int64(0), "3"} {
		if _, err := client.Collection(settingsCollection).Doc(runsAtOnceDocID).Set(ctx, map[string]any{runsAtOnceField: stored}); err != nil {
			t.Fatal(err)
		}
		if n, err := r.Value(ctx); err == nil {
			t.Fatalf("Value = %d with %v stored, want an error", n, stored)
		}
	}
}

// claimN enqueues a run in its own repository and tries to claim it.
func claimN(t *testing.T, q *Queue, client *firestore.Client, id string) (bool, string) {
	t.Helper()
	run := queuedRun(id, 1)
	run.Repo = "octo/" + id
	forget(t, client, id)
	if err := q.Enqueue(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	ok, binding, err := q.TryClaim(context.Background(), id, queueAt, generousBudget, openFacts, dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	return ok, binding
}

// TestAdmissionClaimsUnderTheAdoptedNAndLoweringItStopsNothing is AC2: the N
// adopted at a poll is the one the claims after it run under, and lowering it
// below the runs in flight holds new claims without touching those runs.
func TestAdmissionClaimsUnderTheAdoptedNAndLoweringItStopsNothing(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	resetLedger(t, client, 2)
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = fresh(fmt.Sprintf("adopt-%d", i))
	}
	for _, id := range ids[:2] {
		if ok, why := claimN(t, q, client, id); !ok {
			t.Fatalf("claim %s held by %q under N 2", id, why)
		}
	}
	if ok, why := claimN(t, q, client, ids[2]); ok || why != dispatcher.ConditionConcurrency {
		t.Fatalf("third claim ok %v by %q, want it held by %s", ok, why, dispatcher.ConditionConcurrency)
	}

	if err := q.AdoptRunsAtOnce(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 1 || len(got.Reservations) != 2 {
		t.Fatalf("N %d with %d reservations, want N 1 and both runs still in flight", got.N, len(got.Reservations))
	}

	if err := q.AdoptRunsAtOnce(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, ids[2], queueAt, generousBudget, openFacts, dispatcher.Reservation{ProviderCost: money.Dollar}); err != nil || !ok {
		t.Fatalf("claim under N 3: ok %v, err %v", ok, err)
	}
}

func TestAdoptingANewNRestartsTheRecommendationAfterTheRunsInFlight(t *testing.T) {
	q, client := queue(t)
	resetLedger(t, client, 2)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 5, Recommended: 3, RecommendBasis: dispatcher.BasisRise},
		map[string]time.Time{"adopt-a": queueAt, "adopt-b": queueAt.Add(time.Minute)})
	if err := q.AdoptRunsAtOnce(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got := readLedgerDoc(t, client)
	if got.N != 3 || got.StableRuns != 0 || got.Recommended != 0 || got.RecommendBasis != dispatcher.BasisNone {
		t.Fatalf("N %d stable %d recommended %d basis %q, want N 3 with nothing observed at it",
			got.N, got.StableRuns, got.Recommended, got.RecommendBasis)
	}
	if !got.ChangedAfter.Equal(queueAt.Add(time.Minute)) || len(got.Reservations) != 2 {
		t.Fatalf("changed_after %v with %d reservations, want the latest claim in flight and both kept", got.ChangedAfter, len(got.Reservations))
	}
}

func TestAdoptingTheSameNKeepsTheRecommendation(t *testing.T) {
	q, client := queue(t)
	resetLedger(t, client, 2)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 3, Recommended: 2, RecommendBasis: dispatcher.BasisSteady},
		map[string]time.Time{"adopt-same": queueAt})
	if err := q.AdoptRunsAtOnce(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.StableRuns != 3 || got.Recommended != 2 || !got.ChangedAfter.IsZero() {
		t.Fatalf("stable %d recommended %d changed_after %v, want all three kept", got.StableRuns, got.Recommended, got.ChangedAfter)
	}
}

func TestRecommendedRunsAtOnceReadsTheLedger(t *testing.T) {
	q, client := queue(t)
	resetLedger(t, client, 2)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 5, Recommended: 3, RecommendBasis: dispatcher.BasisRise, TuneStableRuns: 4},
		map[string]time.Time{})
	got, err := q.RecommendedRunsAtOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := RecommendedRunsAtOnce{
		At:             2,
		Recommendation: dispatcher.Recommendation{N: 3, Stable: 5, Basis: dispatcher.BasisRise},
		Tuning:         dispatcher.Tuning{StableRuns: 4, RiseWithin: 1.25, HalveBeyond: 1.5},
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
