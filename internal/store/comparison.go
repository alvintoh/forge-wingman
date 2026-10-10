package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Comparison is what the store's settled runs say about the models and
// providers that produced them (AC2, AC3): every number is read from a record
// the product already wrote, so comparing models costs no spend of its own.
type Comparison struct {
	Models    []ModelComparison
	Providers []ProviderComparison
	Drifted   []DriftedRun
}

// ModelComparison is one build model's own record over the window: the tickets
// it built, how many of those landed, and the mean provider cost of each.
type ModelComparison struct {
	Model string
	// Tickets is how many settled runs built on this model, Passed how many
	// of them succeeded, so the rate is Passed/Tickets.
	Tickets       int
	Passed        int
	CostPerTicket money.Micros
}

// ProviderComparison is one provider's own record over the window. Its
// trailing average is the same baseline a run's cost drift is measured against
// (AC1), and its time-of-use split sums what the provider's hourly pricing
// charged at peak against off-peak (AC3).
type ProviderComparison struct {
	Provider string
	// TrailingCostPerTicket is the mean settled cost of the provider's own
	// runs, and Sample how many runs that mean covers. A run claimed on a
	// free tier is left out of both, as the estimator leaves it out: its $0
	// says nothing of what a paid run costs.
	TrailingCostPerTicket money.Micros
	Sample                int
	PeakTickets           int
	PeakCost              money.Micros
	OffPeakTickets        int
	OffPeakCost           money.Micros
}

// DriftedRun is one recent run whose settled cost was flagged as sitting above
// its provider's trailing average (AC1).
type DriftedRun struct {
	RunID    string
	Provider string
	Model    string
	Cost     money.Micros
	// CostDrift is the flag as Finalize wrote it.
	CostDrift string
}

// ProviderTrailingCost implements runner.ProviderCostReader: the mean settled
// cost of plan's own runs settled since since, and how many runs that mean
// covers. Runs with no steps, claims on a free tier and excludeRunID's own run
// are left out, so a run is compared against its provider's history and never
// against itself.
func (r *Records) ProviderTrailingCost(ctx context.Context, plan string, since time.Time, excludeRunID string) (money.Micros, int, error) {
	recs, err := r.settledSince(ctx, since)
	if err != nil {
		return 0, 0, err
	}
	var sum money.Micros
	sample := 0
	for _, rec := range recs {
		if !countsForTrailingCost(rec, plan, excludeRunID) {
			continue
		}
		sum += rec.SettledProviderCostMicros
		sample++
	}
	if sample == 0 {
		return 0, 0, nil
	}
	return sum / money.Micros(sample), sample, nil
}

// Comparison aggregates every settled run in the window into per-model and
// per-provider records, and lists the runs that were flagged as cost drift.
func (r *Records) Comparison(ctx context.Context, since time.Time) (Comparison, error) {
	recs, err := r.settledSince(ctx, since)
	if err != nil {
		return Comparison{}, err
	}
	// Oldest first, so the drifted runs can be listed newest first.
	slices.SortFunc(recs, func(a, b runner.Record) int { return a.SettledAt.Compare(b.SettledAt) })

	models := map[string]*modelTotal{}
	providers := map[string]*providerTotal{}
	var drifted []DriftedRun
	for _, rec := range recs {
		if len(rec.Steps) == 0 {
			continue
		}
		model := rec.RunModels().Build
		m := models[model]
		if m == nil {
			m = &modelTotal{}
			models[model] = m
		}
		m.tickets++
		if rec.Succeeded() {
			m.passed++
		}
		m.cost += rec.SettledProviderCostMicros

		if countsForProviderSpend(rec) {
			p := providers[rec.Plan]
			if p == nil {
				p = &providerTotal{}
				providers[rec.Plan] = p
			}
			p.sample++
			p.cost += rec.SettledProviderCostMicros
			switch rec.TimeOfUse {
			case runner.TimeOfUsePeak:
				p.peakTickets++
				p.peakCost += rec.SettledProviderCostMicros
			case runner.TimeOfUseOffPeak:
				p.offPeakTickets++
				p.offPeakCost += rec.SettledProviderCostMicros
			}
		}
		if rec.CostDrift != "" {
			drifted = append(drifted, DriftedRun{
				RunID: rec.RunID, Provider: rec.Plan, Model: model,
				Cost: rec.SettledProviderCostMicros, CostDrift: rec.CostDrift,
			})
		}
	}
	slices.Reverse(drifted)

	out := Comparison{Drifted: drifted}
	for model, m := range models {
		out.Models = append(out.Models, ModelComparison{
			Model: model, Tickets: m.tickets, Passed: m.passed,
			CostPerTicket: m.cost / money.Micros(m.tickets),
		})
	}
	for provider, p := range providers {
		out.Providers = append(out.Providers, ProviderComparison{
			Provider: provider, TrailingCostPerTicket: p.cost / money.Micros(p.sample), Sample: p.sample,
			PeakTickets: p.peakTickets, PeakCost: p.peakCost,
			OffPeakTickets: p.offPeakTickets, OffPeakCost: p.offPeakCost,
		})
	}
	slices.SortFunc(out.Models, func(a, b ModelComparison) int { return strings.Compare(a.Model, b.Model) })
	slices.SortFunc(out.Providers, func(a, b ProviderComparison) int { return strings.Compare(a.Provider, b.Provider) })
	return out, nil
}

// modelTotal is one model's running totals, before the mean is taken.
type modelTotal struct {
	tickets, passed int
	cost            money.Micros
}

// providerTotal is one provider's running totals, before the mean is taken.
type providerTotal struct {
	sample, peakTickets, offPeakTickets int
	cost, peakCost, offPeakCost         money.Micros
}

// countsForProviderSpend reports whether rec's settled cost belongs in its
// provider's own paid totals. A run claimed on a free tier cost nothing, so its
// zero says nothing about what that provider's paid runs cost — the estimator
// leaves it out for the same reason. Every provider figure the comparison
// reports and the baseline Finalize flags a run against is this one rule, so it
// is written once here rather than at each aggregation.
func countsForProviderSpend(rec runner.Record) bool { return !rec.LastResort }

// countsForTrailingCost reports whether rec belongs in plan's trailing average.
func countsForTrailingCost(rec runner.Record, plan, excludeRunID string) bool {
	return rec.Plan == plan && rec.RunID != excludeRunID && countsForProviderSpend(rec) && len(rec.Steps) > 0
}

// settledSince is every run that has settled at or after since, in the order
// the store returns them. A single range filter on settled_at needs no
// composite index — Firestore auto-creates a single-field one — so the rest of
// each run's eligibility is checked by the caller; a personal project's run
// count never justifies adding a composite index for it, and a full-window
// rescan is the accepted cost (see Queue.settledSince).
func (r *Records) settledSince(ctx context.Context, since time.Time) ([]runner.Record, error) {
	iter := r.client.Collection(runsCollection).Where(settledAtField, ">=", since).Documents(ctx)
	defer iter.Stop()
	var out []runner.Record
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading runs settled since %s: %w", since.UTC().Format(time.RFC3339), err)
		}
		var rec runner.Record
		if err := snap.DataTo(&rec); err != nil {
			return nil, fmt.Errorf("decoding run %s: %w", snap.Ref.ID, err)
		}
		out = append(out, rec)
	}
	return out, nil
}
