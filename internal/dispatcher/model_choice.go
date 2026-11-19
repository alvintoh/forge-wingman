package dispatcher

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// ModelRecord is one build model's settled runs of one size, credited the way
// the store's model comparison credits them: how many settled, how many of
// those passed, and the input tokens they read fresh and from the provider's
// cache.
type ModelRecord struct {
	Model   string
	Size    string
	Settled int
	Passed  int
	// FreshInput and CacheRead are the runs' input tokens read uncached and
	// served from the provider's cache, which the model's cache share is
	// measured from.
	FreshInput int64
	CacheRead  int64
}

// ModelRecords reads every build model's settled record per size over the runs
// settled at or after since.
type ModelRecords interface {
	ModelRecords(ctx context.Context, since time.Time) ([]ModelRecord, error)
}

// The bar a model clears for a size: at least minEligibleRuns settled runs of
// that size built on it, and at least minPassPercent of them passed.
const (
	minEligibleRuns = 5
	minPassPercent  = 80
)

// The reference token mix a model is priced over: one million input tokens and
// output at 2% of input, the shape measured for this product's runs on its
// vendor's plan. Only the ratio decides the ranking, since every model is
// priced over the same mix; the cache share splits the input per model.
const (
	referenceInputTokens  = 1_000_000
	referenceOutputTokens = 20_000
)

// eligible reports whether r clears the bar: enough settled runs, and enough of
// them passed. The rate is compared in whole numbers so 4 of 5 is exactly 80%.
func eligible(r ModelRecord) bool {
	return r.Settled >= minEligibleRuns && r.Passed*100 >= r.Settled*minPassPercent
}

// RankEligible is the models among candidates that clear the bar for size,
// cheapest first: each priced over the reference mix at the rates in force at
// at, its input split by the model's own cache share across every size, ties
// broken by name. A candidate with no rate card, or no record clearing the bar
// for size, is left out.
//
// Worked example, off-peak, both models at a 95% cache share over the
// reference mix of 1M input and 20k output tokens:
//
//	deepseek-v4.1-flash: 1M x (0.95 x 0.003 + 0.05 x 0.15) + 20k x 0.6 = $0.02235
//	claude-haiku-5-5:    1M x (0.95 x 0.01  + 0.05 x 0.10) + 20k x 0.5 = $0.02450
//
// so Flash ranks first; at a weekday peak hour its card doubles to $0.04470
// and Haiku ranks first.
func RankEligible(records []ModelRecord, size string, candidates []string, rates func(model string) (providers.Rates, bool), at time.Time) []string {
	fresh, cached := map[string]int64{}, map[string]int64{}
	passed := map[string]bool{}
	for _, r := range records {
		fresh[r.Model] += r.FreshInput
		cached[r.Model] += r.CacheRead
		if r.Size == size && eligible(r) {
			passed[r.Model] = true
		}
	}
	type priced struct {
		model string
		cost  float64
	}
	var ranked []priced
	for _, model := range candidates {
		card, ok := rates(model)
		if !ok || !passed[model] {
			continue
		}
		share := runner.CachedShare(fresh[model], cached[model])
		r := card.At(at)
		perInputToken := share*r.CacheRead + (1-share)*r.Input
		ranked = append(ranked, priced{model: model,
			cost: (referenceInputTokens*perInputToken + referenceOutputTokens*r.Output) / 1_000_000})
	}
	slices.SortFunc(ranked, func(a, b priced) int {
		return cmp.Or(cmp.Compare(a.cost, b.cost), strings.Compare(a.model, b.model))
	})
	out := make([]string, len(ranked))
	for i, p := range ranked {
		out[i] = p.model
	}
	return out
}

// modelChooser gives one poll's unlabelled candidates their build model: the
// settled records are read once, the first time a candidate needs them, and
// each model admission is asked about once per set of labels.
type modelChooser struct {
	ctx     context.Context
	d       Deps
	c       Config
	read    bool
	records []ModelRecord
	refused map[string]bool
}

func newModelChooser(ctx context.Context, d Deps, c Config) *modelChooser {
	return &modelChooser{ctx: ctx, d: d, c: c, refused: map[string]bool{}}
}

// choose is the build model a claim of cand at stage records, empty to record
// none: a ticket that named a build model keeps it, a planned run's build keeps
// what its plan-stage claim recorded, and a run no eligible model admission
// allows builds on the default. A free model is never chosen, since moving a run
// onto a free tier is the owner's choice. An unreadable record chooses none.
//
// Two known limits matter once a second paid plan exists. A chosen model that
// the budget, peak-hour or Go-usage checks then hold defers the run rather than
// falling back to the default. And a chosen model on another plan is judged on
// the default plan's budget, since tierOf reads a non-build claim's budget from
// Config.Budget.
func (m *modelChooser) choose(cand Candidate, stage string, at time.Time) string {
	if m.d.ModelRecords == nil || len(m.c.BuildCandidates) == 0 || cand.Model != "" || stage == StageBuild {
		return ""
	}
	if !m.read {
		m.read = true
		records, err := m.d.ModelRecords.ModelRecords(m.ctx, at.Add(-runner.TrailingWindow))
		if err != nil {
			m.d.Logger.Warn("modelRecordsUnread", "run", cand.RunID, "err", err.Error())
			records = nil
		}
		m.records = records
	}
	for _, model := range RankEligible(m.records, cand.Size, m.c.BuildCandidates, providers.RatesFor, at) {
		if !m.c.isFree(model) && m.allows(cand, model) {
			m.d.Logger.Info("buildModelChosen", "run", cand.RunID, "size", cand.Size, "model", model)
			return model
		}
	}
	return ""
}

// allows reports whether admission lets cand's run build on model beside the
// review and plan models its ticket named. A failed plan read allows nothing.
func (m *modelChooser) allows(cand Candidate, model string) bool {
	labels := runner.ModelLabels{Build: model, Review: cand.ReviewModel, Plan: cand.PlanModels}
	key := strings.Join(append([]string{labels.Build, labels.Review}, labels.Plan...), "\x00")
	if refused, ok := m.refused[key]; ok {
		return !refused
	}
	reason, _, err := checkModels(m.ctx, m.d.ModelPlans, labels, m.c.Model, m.c.Plans)
	if err != nil {
		m.d.Logger.Info("modelConfigLookupFailed", "run", cand.RunID, "model", model, "err", err.Error())
		return false
	}
	m.refused[key] = reason != ""
	return reason == ""
}
