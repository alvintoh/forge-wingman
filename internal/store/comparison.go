package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Comparison is what the store's settled runs say about the models and
// providers that produced them and about each run's own prompt-cache share
// (AC2, AC3): every number is read from a record the product already wrote, so
// comparing models costs no spend of its own.
type Comparison struct {
	Models    []ModelComparison
	Providers []ProviderComparison
	Runs      []RunCache
	Drifted   []DriftedRun
}

// ModelComparison is one build model's own record over the window: the tickets
// it built, how many of those landed, the mean provider cost and wall-clock time
// of each, and the rewrite rate measured over the ones a merge has reached.
type ModelComparison struct {
	Model string
	// Tickets is how many settled runs built on this model, Passed how many
	// of them succeeded, so the rate is Passed/Tickets.
	Tickets       int
	Passed        int
	CostPerTicket money.Micros
	// MinutesPerTicket is the mean wall-clock minutes of the runs built on
	// this model, from the build's start to the run's settlement.
	MinutesPerTicket float64
	// RewriteRate is the rewrite rate of those runs: the owner's lines over
	// the agent's, +Inf when they measured none of the agent's lines at all.
	// A model no merge has measured reports zero.
	RewriteRate float64
	// RewriteSample is how many of the model's runs carry a measurement.
	RewriteSample int
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

// RunCache is one settled run's own prompt-cache share, read from the input and
// cache-read tokens the record already holds, with each phase's own figures as a
// view prints them.
type RunCache struct {
	RunID    string
	Provider string
	Model    string
	// CachedShare is the share of the run's input tokens — fresh and cached
	// together — that came from the provider's cache.
	CachedShare float64
	// BuildCache and PlanCache are the phase's own figures as a view prints
	// them: the share of its first request and of its whole input, or the word
	// that stands in for a share the run recorded none of.
	BuildCache string
	PlanCache  string
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

// PlanCache is one plan's trailing runs' prompt-cache figures, keyed by phase:
// the same figures one run of it records, summed over the runs that recorded
// them. A phase none of them recorded is absent.
type PlanCache map[string]runner.PhaseCache

// ProviderTrailingCost implements runner.ProviderCostReader: the mean settled
// cost of plan's own runs settled since since, and how many runs that mean
// covers. Runs with no steps, claims on a free tier and excludeRunID's own run
// are left out, so a run is compared against its provider's history and never
// against itself.
func (r *Records) ProviderTrailingCost(ctx context.Context, plan string, since time.Time, excludeRunID string) (money.Micros, int, error) {
	settled, err := r.settledSince(ctx, since)
	if err != nil {
		return 0, 0, err
	}
	var sum money.Micros
	sample := 0
	for _, run := range settled {
		rec := run.Record
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
// per-provider records, lists each run that read input with the share of it the
// provider served from cache, and lists the runs that were flagged as cost drift.
func (r *Records) Comparison(ctx context.Context, since time.Time) (Comparison, error) {
	settled, err := r.settledSince(ctx, since)
	if err != nil {
		return Comparison{}, err
	}
	// Oldest first, so the drifted runs can be listed newest first.
	slices.SortFunc(settled, func(a, b settledRun) int { return a.SettledAt.Compare(b.SettledAt) })

	models := map[string]*modelTotal{}
	providers := map[string]*providerTotal{}
	var runs []RunCache
	var drifted []DriftedRun
	for _, run := range settled {
		rec := run.Record
		if len(rec.Steps) == 0 {
			continue
		}
		model := creditedModel(rec)
		// A record with no build step and no label carries no model to
		// credit, so it is left out of the model totals rather than reported
		// under an empty name.
		if model != "" {
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
			m.minutes += wallClockMinutes(rec)
			// Only a run a merge has measured says anything about its model's
			// rewrite rate: one still in the open has no lines to count and no
			// measurement to cover.
			if run.measured {
				m.lines.AgentLines += run.rewrite.AgentLines
				m.lines.OwnerLines += run.rewrite.OwnerLines
				m.rewriteSample++
			}
		}

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
		// A run that read no input at all has no share to report, so it is left
		// out rather than printed as a cold one.
		if rec.Tokens.Input+rec.Tokens.CacheRead > 0 {
			runs = append(runs, RunCache{RunID: rec.RunID, Provider: rec.Plan, Model: model,
				CachedShare: rec.CachedShare(),
				BuildCache:  runner.PhaseCacheCell(rec.Cache, runner.PhaseBuild),
				PlanCache:   runner.PhaseCacheCell(rec.Cache, runner.PhasePlan)})
		}
	}
	slices.Reverse(drifted)
	slices.Reverse(runs)

	out := Comparison{Runs: runs, Drifted: drifted}
	for model, m := range models {
		out.Models = append(out.Models, ModelComparison{
			Model: model, Tickets: m.tickets, Passed: m.passed,
			CostPerTicket: m.cost / money.Micros(m.tickets), MinutesPerTicket: m.minutes / float64(m.tickets),
			RewriteRate: m.lines.Rate(), RewriteSample: m.rewriteSample,
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

// PlanCaches sums every settled run in the window into its plan's own
// prompt-cache figures per phase, so a plan reads the way one run of it does but
// covers every run that recorded the phase. Runs with no provider, and phases a
// run recorded no figure for, are left out.
func (r *Records) PlanCaches(ctx context.Context, since time.Time) (map[string]PlanCache, error) {
	settled, err := r.settledSince(ctx, since)
	if err != nil {
		return nil, err
	}
	totals := map[string]map[string]*phaseTotal{}
	for _, run := range settled {
		rec := run.Record
		if rec.Plan == "" || len(rec.Cache) == 0 { // a run with no plan has no row to join to
			continue
		}
		byPhase := totals[rec.Plan]
		if byPhase == nil {
			byPhase = map[string]*phaseTotal{}
			totals[rec.Plan] = byPhase
		}
		// The phase's whole input is summed from its steps, the way one run's
		// figures are read from them, because the record keeps that share
		// without the tokens behind it.
		fresh, cached := map[string]int64{}, map[string]int64{}
		for _, step := range rec.Steps {
			phase := string(step.Phase)
			fresh[phase] += step.Tokens.Input
			cached[phase] += step.Tokens.CacheRead
		}
		for phase, c := range rec.Cache {
			t := byPhase[phase]
			if t == nil {
				t = &phaseTotal{}
				byPhase[phase] = t
			}
			t.firstFresh += c.FirstFresh
			t.firstCached += c.FirstCached
			t.fresh += fresh[phase]
			t.cached += cached[phase]
		}
	}
	out := map[string]PlanCache{}
	for plan, byPhase := range totals {
		cache := make(PlanCache, len(byPhase))
		for phase, t := range byPhase {
			cache[phase] = runner.PhaseCache{FirstFresh: t.firstFresh, FirstCached: t.firstCached,
				CachedShare: runner.CachedShare(t.fresh, t.cached)}
		}
		out[plan] = cache
	}
	return out, nil
}

// phaseTotal is one plan phase's running token totals, before its share is taken.
type phaseTotal struct {
	firstFresh, firstCached int64
	fresh, cached           int64
}

// modelTotal is one model's running totals, before the mean is taken.
type modelTotal struct {
	tickets, passed int
	cost            money.Micros
	minutes         float64
	// lines is the measured lines of the model's own runs a merge has
	// measured, and rewriteSample how many of them carry one.
	lines dispatcher.Rewrite
	// rewriteSample is how many of the model's runs carry a measurement.
	rewriteSample int
}

// creditedModel is the model a run is credited to: the model its first
// build-phase step ran on, so a run whose ticket named none is still credited to
// the model that built it. A record with no build step at all (a run that
// stopped before its build) falls back to the label the ticket named, empty when
// it named none.
func creditedModel(rec runner.Record) string {
	for _, step := range rec.Steps {
		if step.Phase == runner.PhaseBuild && step.Model != "" {
			return step.Model
		}
	}
	return rec.RunModels().Build
}

// wallClockMinutes is how long a run took, from its start to its settlement. The
// start comes from the build's own host, which the summary's validation lets sit
// slightly ahead of the settling clock, so a run that settles at once counts
// zero rather than backwards.
func wallClockMinutes(rec runner.Record) float64 {
	return math.Max(0, rec.SettledAt.Sub(rec.StartedAt).Minutes())
}

// providerTotal is one provider's running totals, before the mean is taken.
type providerTotal struct {
	sample, peakTickets, offPeakTickets int
	cost, peakCost, offPeakCost         money.Micros
}

// countsForProviderSpend reports whether rec's settled cost belongs in its
// provider's own paid totals. A run claimed on a free tier cost nothing, and a
// record with no provider has no one to attribute its cost to, so neither says
// anything about what a provider's paid runs cost — the estimator leaves a free
// run out for the same reason. Every provider figure the comparison reports and
// the baseline Finalize flags a run against is this one rule, so it is written
// once here rather than at each aggregation.
func countsForProviderSpend(rec runner.Record) bool { return rec.Plan != "" && !rec.LastResort }

// countsForTrailingCost reports whether rec belongs in plan's trailing average.
func countsForTrailingCost(rec runner.Record, plan, excludeRunID string) bool {
	return rec.Plan == plan && rec.RunID != excludeRunID && countsForProviderSpend(rec) && len(rec.Steps) > 0
}

// settledRun is one settled run's record beside the rewrite measurement the
// dispatcher writes onto the same row, which the record itself does not carry.
type settledRun struct {
	runner.Record
	// rewrite is the run's measured lines, which it has none of until a merge
	// has reached it: measured is false then.
	rewrite dispatcher.Rewrite
	// measured is whether a merge has written a measurement onto the row.
	measured bool
}

// settledSince is every run that has settled at or after since, in the order
// the store returns them, each with the rewrite measurement its own row carries.
// A single range filter on settled_at needs no composite index — Firestore
// auto-creates a single-field one — so the rest of each run's eligibility is
// checked by the caller; a personal project's run count never justifies adding a
// composite index for it, and a full-window rescan is the accepted cost (see
// Queue.settledSince).
func (r *Records) settledSince(ctx context.Context, since time.Time) ([]settledRun, error) {
	iter := r.client.Collection(runsCollection).Where(settledAtField, ">=", since).Documents(ctx)
	defer iter.Stop()
	var out []settledRun
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading runs settled since %s: %w", since.UTC().Format(time.RFC3339), err)
		}
		// The measurement sits beside the record on the row rather than among
		// the fields the record job rebuilds, so the row is read as a map for
		// it and into the record the callers take.
		data := snap.Data()
		var rec runner.Record
		if err := snap.DataTo(&rec); err != nil {
			return nil, fmt.Errorf("decoding run %s: %w", snap.Ref.ID, err)
		}
		rewrite, measured := rewriteOf(data)
		out = append(out, settledRun{Record: rec, rewrite: rewrite, measured: measured})
	}
	return out, nil
}
