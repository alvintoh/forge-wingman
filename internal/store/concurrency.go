package store

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// observe reads what the settled run runID says about running at the ledger's
// N: whether it was a rate-limit stop, and how the latest runs of its size
// claimed at N compare with the latest that ran alone. A run claimed below N,
// or before N last moved, says nothing about the current N and yields the zero
// Observation. Only a run that reached a PR or found nothing to change carries
// medians, and only over StableRuns such runs at each concurrency.
func (q *Queue) observe(tx *firestore.Transaction, rowRef *firestore.DocumentRef, ledger ledgerDoc) (dispatcher.Observation, error) {
	snap, err := tx.Get(rowRef)
	if status.Code(err) == codes.NotFound {
		return dispatcher.Observation{}, nil
	}
	if err != nil {
		return dispatcher.Observation{}, err
	}
	var rec runner.Record
	if err := snap.DataTo(&rec); err != nil {
		return dispatcher.Observation{}, fmt.Errorf("decoding run %s: %w", snap.Ref.ID, err)
	}
	n := ledger.concurrency().N
	tuning := ledger.tuning()
	claimed, _ := snap.Data()[claimConcurrencyField].(int64)
	claimedAt, _ := snap.Data()[claimedAtField].(time.Time)
	if claimed < int64(n) || !claimedAt.After(ledger.ChangedAfter) {
		return dispatcher.Observation{}, nil
	}
	obs := dispatcher.Observation{
		RateLimited: rec.Outcome == runner.OutcomeInfraFailure && rec.StopReason == runner.StopModelUnavailable,
	}
	if !rec.Succeeded() {
		return obs, nil
	}
	solo, err := q.recentDurations(tx, rec.Size, 1, tuning.StableRuns)
	if err != nil {
		return dispatcher.Observation{}, err
	}
	loaded := solo
	if n > 1 {
		if loaded, err = q.recentDurations(tx, rec.Size, int64(n), tuning.StableRuns); err != nil {
			return dispatcher.Observation{}, err
		}
	}
	if need := tuning.StableRuns; len(solo) >= need && len(loaded) >= need {
		obs.Solo, obs.Loaded = medianDuration(solo), medianDuration(loaded)
	}
	return obs, nil
}

// recentDurations is the total phase time of the latest limit settled runs of
// size that reached a PR or found nothing to change, claimed at concurrency.
func (q *Queue) recentDurations(tx *firestore.Transaction, size string, concurrency int64, limit int) ([]time.Duration, error) {
	iter := tx.Documents(q.client.Collection(runsCollection).
		Where(sizeField, "==", size).
		Where(outcomeField, "in", []string{string(runner.OutcomePROpened), string(runner.OutcomeNoChanges)}).
		Where(claimConcurrencyField, "==", concurrency).
		OrderBy(settledAtField, firestore.Desc).
		Limit(limit))
	defer iter.Stop()
	var out []time.Duration
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading recent runs of size %s: %w", size, err)
		}
		var rec runner.Record
		if err := snap.DataTo(&rec); err != nil {
			return nil, fmt.Errorf("decoding run %s: %w", snap.Ref.ID, err)
		}
		if rec.SettledAt.IsZero() {
			continue
		}
		var ms int64
		for _, d := range rec.DurationsMS {
			ms += d
		}
		out = append(out, time.Duration(ms)*time.Millisecond)
	}
}

// medianDuration is the middle of ds, the lower one of the two for an even
// count, and zero for none.
func medianDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := slices.Sorted(slices.Values(ds))
	return sorted[(len(sorted)-1)/2]
}
