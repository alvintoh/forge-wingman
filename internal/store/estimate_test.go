package store

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// settledRecord writes id as a settled run of size, with the given cost and
// duration, as runner.Finalize would leave it.
func settledRecord(t *testing.T, client *firestore.Client, id, size string, cost money.Micros, durationMS int64, settledAt time.Time) {
	t.Helper()
	tk := runner.Ticket{ID: id, Title: "feat(x): add a file", Size: size, Body: "Add a file."}
	rec := runner.NewRecord(id, tk, settledAt.Add(-time.Hour))
	rec.SettledAt = settledAt
	rec.SettledProviderCostMicros = cost
	rec.DurationsMS = map[string]int64{"build": durationMS}
	if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), fields(rec)); err != nil {
		t.Fatal(err)
	}
}

func TestEstimateIsTheMeanOfSettledRunsOfTheRequestedSize(t *testing.T) {
	_, client := queue(t)
	size := "S-" + fresh("mean")
	a, b, other := fresh("est-s-a"), fresh("est-s-b"), fresh("est-m")
	forget(t, client, a, b, other)
	settledRecord(t, client, a, size, 2*money.Dollar, 120_000, queueAt)                // 2 minutes
	settledRecord(t, client, b, size, 4*money.Dollar, 180_000, queueAt.Add(time.Hour)) // 3 minutes
	settledRecord(t, client, other, "M-"+size, 100*money.Dollar, 60_000, queueAt)      // a different size, must not be averaged in

	got, err := NewEstimates(client).Estimate(context.Background(), size)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderCost != 3*money.Dollar || got.Minutes != 2 {
		t.Fatalf("estimate = %+v, want the mean of the two runs of this size (3 dollars, 2.5m rounded down to 2)", got)
	}
}

func TestEstimateFallsBackToTheFirstEverSettledRunForAnUnseenSize(t *testing.T) {
	_, client := queue(t)
	// Earlier than any other test in this suite settles a run, so the
	// "first ever" query is deterministic despite the collection being
	// shared across the whole suite.
	veryFirst := queueAt.Add(-100 * 365 * 24 * time.Hour)
	first, later := fresh("est-fallback-first"), fresh("est-fallback-later")
	forget(t, client, first, later)
	settledRecord(t, client, first, "M", 5*money.Dollar, 60_000, veryFirst)
	settledRecord(t, client, later, "M", 9*money.Dollar, 60_000, veryFirst.Add(time.Hour))

	size := "L-" + fresh("unseen") // a size no run has ever been settled at
	got, err := NewEstimates(client).Estimate(context.Background(), size)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderCost != 5*money.Dollar {
		t.Fatalf("estimate = %+v, want the first-ever settled run's cost as the fallback", got)
	}
}

func TestEstimateIgnoresARunThatHasNotSettledYet(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	size := "S-" + fresh("mix")
	settledID := fresh("est-mix-settled")
	unsettled := queuedRun(fresh("est-mix-unsettled"), 1)
	unsettled.Ticket.Size = size
	forget(t, client, settledID, unsettled.RunID)
	settledRecord(t, client, settledID, size, 6*money.Dollar, 60_000, queueAt)
	if err := q.Enqueue(ctx, unsettled); err != nil {
		t.Fatal(err)
	}

	got, err := NewEstimates(client).Estimate(ctx, size)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderCost != 6*money.Dollar {
		t.Fatalf("estimate = %+v, want only the settled run's cost, unaveraged with the unsettled one", got)
	}
}
