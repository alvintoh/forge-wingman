package planrefresh

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// Ranker ranks a plan's models per ticket size from the settled runs of the
// window ending at now, as the claim's own ranking does: the models clearing the
// bar in order, and the rest below it.
type Ranker func(ctx context.Context, provider string, now time.Time) ([]providers.SizeRanking, error)

// ranking is the plan's models ranked from its settled runs, dated by on. A
// ranking that could not be read, because the settled runs could not or the
// vendor's model list was refused, keeps the last ranking beside its date. A
// refresher with no ranker reads none and keeps the last one as it stood.
func (r Refresher) ranking(ctx context.Context, provider string, last providers.Facts, on string, refusedList error) providers.RankingFacts {
	if r.Rank == nil {
		return last.Ranking
	}
	err := refusedList
	var sizes []providers.SizeRanking
	if err == nil {
		sizes, err = r.Rank(ctx, provider, r.now())
		if err != nil {
			err = fmt.Errorf("ranking the settled runs of %s: %w", provider, err)
		}
	}
	if err != nil {
		carried := last.Ranking
		carried.Source = classify(on, err, nil, last.Ranking.Source)
		return carried
	}
	capped, cut := capSizes(sizes)
	facts := providers.RankingFacts{Sizes: capped}
	facts.Source = classify(on, nil, rankingChanges(last.Ranking.Sizes, facts.Sizes), last.Ranking.Source)
	if cut {
		facts.Source.Note = fmt.Sprintf("a ranked list held more than %d models and was cut", maxRankedModels)
	}
	return facts
}

// capSizes caps each size's two lists at maxRankedModels, so a ranker cannot
// carry a facts document past what the store accepts, and reports whether any
// list was cut.
func capSizes(sizes []providers.SizeRanking) (capped []providers.SizeRanking, cut bool) {
	capped = make([]providers.SizeRanking, len(sizes))
	for i, s := range sizes {
		cut = cut || len(s.Ranked) > maxRankedModels || len(s.BelowBar) > maxRankedModels
		capped[i] = providers.SizeRanking{
			Size:     providers.CapText(s.Size, maxFieldBytes),
			Ranked:   capRanked(s.Ranked),
			BelowBar: capRanked(s.BelowBar),
		}
	}
	return capped, cut
}

// capRanked caps one list at maxRankedModels entries, each id at maxFieldBytes.
func capRanked(models []providers.RankedModel) []providers.RankedModel {
	if len(models) > maxRankedModels {
		models = models[:maxRankedModels]
	}
	capped := slices.Clone(models)
	for i := range capped {
		capped[i].Model = providers.CapText(capped[i].Model, maxFieldBytes)
	}
	return capped
}

// rankingChanges names each size whose ranked order differs from the last
// document's. The counts move every week and are not a change; a model entering
// or leaving the ranked list is.
func rankingChanges(before, now []providers.SizeRanking) []string {
	var changes []string
	for _, s := range now {
		var was []string
		for _, b := range before {
			if b.Size == s.Size {
				was = rankedIDs(b)
			}
		}
		if is := rankedIDs(s); !slices.Equal(was, is) {
			changes = append(changes, fmt.Sprintf("size %s ranked %s -> %s", s.Size,
				orNone(strings.Join(was, ", ")), orNone(strings.Join(is, ", "))))
		}
	}
	return changes
}

// rankedIDs is the ids of one size's ranked models, in rank order.
func rankedIDs(s providers.SizeRanking) []string {
	ids := make([]string, len(s.Ranked))
	for i, m := range s.Ranked {
		ids[i] = m.Model
	}
	return ids
}
