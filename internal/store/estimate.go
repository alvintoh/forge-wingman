package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Estimates reads past runs' settled cost and duration to estimate an
// unclaimed run's own (PRD Q4): the mean of every settled run of the
// requested size, falling back to the first ever settled run — any size —
// when none exist for that size yet. A brand new system with no settled runs
// at all estimates zero, so its own first runs are what seeds every estimate
// after them, rather than blocking on data that cannot exist yet.
type Estimates struct {
	client *firestore.Client
}

// NewEstimates returns an Estimates backed by client.
func NewEstimates(client *firestore.Client) *Estimates { return &Estimates{client: client} }

// Estimate implements dispatcher.Estimator.
func (e *Estimates) Estimate(ctx context.Context, size string) (dispatcher.Estimate, error) {
	recs, err := e.settledBySize(ctx, size)
	if err != nil {
		return dispatcher.Estimate{}, err
	}
	if len(recs) == 0 {
		recs, err = e.firstSettled(ctx)
		if err != nil {
			return dispatcher.Estimate{}, err
		}
	}
	if len(recs) == 0 {
		return dispatcher.Estimate{}, nil
	}
	var costSum money.Micros
	var minutesSum int64
	for _, r := range recs {
		costSum += r.SettledProviderCostMicros
		minutesSum += runner.BillableMinutes(r.DurationsMS)
	}
	n := int64(len(recs))
	return dispatcher.Estimate{ProviderCost: costSum / money.Micros(n), Minutes: minutesSum / n}, nil
}

// settledBySize is every settled run of size. A single equality filter on
// size needs no composite index — Firestore auto-creates a single-field one —
// so settlement is instead checked client-side; a personal project's run
// count never justifies adding a composite index for it.
func (e *Estimates) settledBySize(ctx context.Context, size string) ([]runner.Record, error) {
	iter := e.client.Collection(runsCollection).Where(sizeField, "==", size).Documents(ctx)
	defer iter.Stop()
	var out []runner.Record
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading runs of size %s: %w", size, err)
		}
		var rec runner.Record
		if err := snap.DataTo(&rec); err != nil {
			return nil, fmt.Errorf("decoding run %s: %w", snap.Ref.ID, err)
		}
		if !rec.SettledAt.IsZero() {
			out = append(out, rec)
		}
	}
	return out, nil
}

// firstSettled is the first run ever settled, regardless of size — PRD Q4's
// fallback for a size no run has settled yet.
func (e *Estimates) firstSettled(ctx context.Context) ([]runner.Record, error) {
	iter := e.client.Collection(runsCollection).
		Where(settledAtField, ">", time.Time{}).
		OrderBy(settledAtField, firestore.Asc).
		Limit(1).
		Documents(ctx)
	defer iter.Stop()
	snap, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the first settled run: %w", err)
	}
	var rec runner.Record
	if err := snap.DataTo(&rec); err != nil {
		return nil, fmt.Errorf("decoding run %s: %w", snap.Ref.ID, err)
	}
	return []runner.Record{rec}, nil
}
