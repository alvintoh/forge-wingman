package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	runsAtOnceDocID = "runs-at-once"
	runsAtOnceField = "runs_at_once"
)

// RunsAtOnce keeps the owner's N in the owner database: how many runs
// admission allows in flight at once across every project, which the surface
// sets and every poll reads. Its upper bound is the configured platform cap,
// which the reader holds it to, so the store refuses only a value under one.
type RunsAtOnce struct {
	setting intSetting
}

// NewRunsAtOnce returns a RunsAtOnce backed by client, which must be one
// opened on OwnerDatabase.
func NewRunsAtOnce(client *firestore.Client) *RunsAtOnce {
	return &RunsAtOnce{setting: intSetting{
		client: client, docID: runsAtOnceDocID, field: runsAtOnceField, name: "runs at once",
		min: dispatcher.MinRunsAtOnce, def: dispatcher.DefaultRunsAtOnce,
	}}
}

// Value returns the saved N, DefaultRunsAtOnce while none is saved.
func (r *RunsAtOnce) Value(ctx context.Context) (int, error) { return r.setting.value(ctx) }

// Set saves N, effective from the next poll.
func (r *RunsAtOnce) Set(ctx context.Context, n int, at time.Time) error {
	return r.setting.set(ctx, n, at)
}

// RecommendedRunsAtOnce is the concurrency rule's recommendation as the
// ledger holds it: At is the N it was observed at, the owner's N as the
// dispatcher last adopted it, and Tuning the rule's numbers it applied.
type RecommendedRunsAtOnce struct {
	At             int
	Recommendation dispatcher.Recommendation
	Tuning         dispatcher.Tuning
}

// RecommendedRunsAtOnce reads the recommendation from the ledger.
func (q *Queue) RecommendedRunsAtOnce(ctx context.Context) (RecommendedRunsAtOnce, error) {
	ledger, err := q.ledgerOutsideTx(ctx)
	if err != nil {
		return RecommendedRunsAtOnce{}, err
	}
	return RecommendedRunsAtOnce{
		At: ledger.concurrency().N, Recommendation: ledger.recommendation(), Tuning: ledger.tuning(),
	}, nil
}

// AdoptRunsAtOnce makes n the N admission claims under from this poll on. When
// n differs from the ledger's N the recommendation starts over at n, and a run
// already in flight, claimed under the old N, says nothing about the new one.
// Lowering N stops no run in flight: admission holds only new claims.
func (q *Queue) AdoptRunsAtOnce(ctx context.Context, n int) error {
	ref := q.ledgerRef()
	err := q.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		ledger, err := q.readLedger(tx, ref)
		if err != nil {
			return err
		}
		if ledger.N == n {
			return nil
		}
		return tx.Set(ref, map[string]any{
			"n":               n,
			"stable_runs":     0,
			"recommended_n":   0,
			"recommend_basis": dispatcher.BasisNone,
			"changed_after":   ledger.latestClaim(ledger.ChangedAfter),
		}, firestore.MergeAll)
	})
	if err != nil {
		return fmt.Errorf("adopting runs at once %d: %w", n, err)
	}
	return nil
}
