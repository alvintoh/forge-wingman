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
// after them, rather than blocking on data that cannot exist yet. A run claimed
// on a plan's free tier is left out: its $0 says nothing of what a paid run
// costs, and averaging it in would under-reserve the paid windows.
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
		if !rec.SettledAt.IsZero() && !rec.LastResort {
			out = append(out, rec)
		}
	}
	return out, nil
}

// EstimatePlan implements dispatcher.Estimator's plan half: the mean cost and
// duration of the plan phase over past settled runs, gathered the way Estimate
// gathers the run itself and falling back the same way — the size's own
// settled runs, then the first run that ever settled a plan phase of any size,
// then DefaultPlanEstimate. The samples are the plan phase single-stage runs
// ran inside their build, since the plan job reports no summary.
func (e *Estimates) EstimatePlan(ctx context.Context, size string) (dispatcher.Estimate, error) {
	recs, err := e.settledBySize(ctx, size)
	if err != nil {
		return dispatcher.Estimate{}, err
	}
	cost, ms, n := planSteps(recs)
	if n == 0 {
		recs, err = e.firstSettled(ctx)
		if err != nil {
			return dispatcher.Estimate{}, err
		}
		cost, ms, n = planSteps(recs)
	}
	if n == 0 {
		return dispatcher.DefaultPlanEstimate, nil
	}
	return dispatcher.Estimate{
		ProviderCost: money.FromUSD(cost / float64(n)),
		Minutes:      runner.BillableMinutes(map[string]int64{string(runner.PhasePlan): ms / n}),
	}, nil
}

// planSteps sums the cost and duration of every plan-phase step across recs.
func planSteps(recs []runner.Record) (cost float64, ms int64, n int64) {
	for _, r := range recs {
		for _, s := range r.Steps {
			if s.Phase != runner.PhasePlan {
				continue
			}
			cost += s.Tokens.Cost
			ms += s.DurationMS
			n++
		}
	}
	return cost, ms, n
}

// firstSettled is the first run ever settled on a paid plan, regardless of
// size — PRD Q4's fallback for a size no run has settled yet.
func (e *Estimates) firstSettled(ctx context.Context) ([]runner.Record, error) {
	iter := e.client.Collection(runsCollection).
		Where(settledAtField, ">", time.Time{}).
		OrderBy(settledAtField, firestore.Asc).
		Documents(ctx)
	defer iter.Stop()
	for {
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
		if !rec.LastResort {
			return []runner.Record{rec}, nil
		}
	}
}
