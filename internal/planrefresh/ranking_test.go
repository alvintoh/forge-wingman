package planrefresh

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// rankedS is a ranking with two models ranked for S and one below the bar.
func rankedS(first, second string) []providers.SizeRanking {
	return []providers.SizeRanking{{
		Size:     "S",
		Ranked:   []providers.RankedModel{{Model: first, Settled: 5, Passed: 5}, {Model: second, Settled: 6, Passed: 5}},
		BelowBar: []providers.RankedModel{{Model: "command-code/glm-7", Settled: 2, Passed: 1}},
	}}
}

// rankWith is a ranker answering sizes, or err, and counting its calls.
func rankWith(sizes []providers.SizeRanking, err error, calls *[]time.Time) Ranker {
	return func(_ context.Context, _ string, now time.Time) ([]providers.SizeRanking, error) {
		*calls = append(*calls, now)
		return sizes, err
	}
}

// lastRanked is facts whose ranking was read well on 2026-10-07.
func lastRanked() providers.Facts {
	return providers.Facts{ReadOn: "2026-10-07", Ranking: providers.RankingFacts{
		Source: providers.Source{State: providers.ReadUnchanged, LastGoodReadOn: "2026-10-07"},
		Sizes:  rankedS("command-code/glm-5", "command-code/glm-6"),
	}}
}

func TestRefreshRecordsTheRankingTheRankerGivesWithItsCountsAndChanges(t *testing.T) {
	s, base := newSources(t)
	r := s.refresher(t, base, readOn)
	var calls []time.Time
	r.Rank = rankWith(rankedS("command-code/glm-6", "command-code/glm-5"), nil, &calls)

	got := refresh(t, r, plan(base), lastRanked()).Ranking

	if !slices.Equal(calls, []time.Time{readOn}) {
		t.Fatalf("ranker read at %v, want once at the refresh's own time", calls)
	}
	if want := rankedS("command-code/glm-6", "command-code/glm-5"); got.Sizes[0].Ranked[0] != want[0].Ranked[0] ||
		got.Sizes[0].Ranked[1] != want[0].Ranked[1] || got.Sizes[0].BelowBar[0] != want[0].BelowBar[0] {
		t.Fatalf("sizes = %+v, want the ranker's order with each model's counts and the one below the bar", got.Sizes)
	}
	wantSource := providers.Source{
		State: providers.ReadChanged, LastGoodReadOn: "2026-10-14",
		Changes: []string{"size S ranked command-code/glm-5, command-code/glm-6 -> command-code/glm-6, command-code/glm-5"},
	}
	if got.Source.State != wantSource.State || got.Source.LastGoodReadOn != wantSource.LastGoodReadOn ||
		!slices.Equal(got.Source.Changes, wantSource.Changes) {
		t.Fatalf("source = %+v, want %+v", got.Source, wantSource)
	}
}

func TestRefreshRecordsAnUnchangedRankingAsUnchanged(t *testing.T) {
	s, base := newSources(t)
	r := s.refresher(t, base, readOn)
	var calls []time.Time
	r.Rank = rankWith(rankedS("command-code/glm-5", "command-code/glm-6"), nil, &calls)
	if got := refresh(t, r, plan(base), lastRanked()).Ranking.Source; got.State != providers.ReadUnchanged {
		t.Fatalf("source = %+v, want a repeat of the same order unchanged", got)
	}
}

func TestRefreshKeepsTheLastRankingWhenTheSettledRunsCannotBeRead(t *testing.T) {
	s, base := newSources(t)
	r := s.refresher(t, base, readOn)
	var calls []time.Time
	r.Rank = rankWith(nil, errors.New("firestore unavailable"), &calls)
	last := lastRanked()

	got := refresh(t, r, plan(base), last).Ranking

	if got.Source.State != providers.ReadCouldNotRead || got.Source.LastGoodReadOn != "2026-10-07" ||
		!strings.Contains(got.Source.Note, "firestore unavailable") {
		t.Fatalf("source = %+v, want could-not-read with the last good date and the cause", got.Source)
	}
	if len(got.Sizes) != 1 || got.Sizes[0].Ranked[0].Model != "command-code/glm-5" {
		t.Fatalf("sizes = %+v, want the last good ranking carried forward", got.Sizes)
	}
}

func TestRefreshKeepsTheLastRankingAndReadsNoRunsWhenTheModelListIsRefused(t *testing.T) {
	s, base := newSources(t)
	s.models.set(`{"data":[{"id":"New"}]}`)
	r := s.refresher(t, base, readOn)
	var calls []time.Time
	r.Rank = rankWith(rankedS("command-code/glm-6", "command-code/glm-5"), nil, &calls)

	facts, err := r.Refresh(context.Background(), "command-code", plan(base), lastRanked())
	if err == nil {
		t.Fatal("Refresh accepted a list holding an id that is not a model id")
	}
	got := facts.Ranking
	if got.Source.State != providers.ReadCouldNotRead || got.Source.LastGoodReadOn != "2026-10-07" ||
		!strings.Contains(got.Source.Note, "not a model id") {
		t.Fatalf("source = %+v, want could-not-read with the last good date and the list's refusal", got.Source)
	}
	if len(got.Sizes) != 1 || got.Sizes[0].Ranked[0].Model != "command-code/glm-5" {
		t.Fatalf("sizes = %+v, want the last good ranking carried forward", got.Sizes)
	}
	if len(calls) != 0 {
		t.Fatalf("the ranker was called %d times, want none for a refused list", len(calls))
	}
}

func TestRefreshWithNoRankerLeavesTheRankingAsItStood(t *testing.T) {
	s, base := newSources(t)
	last := lastRanked()
	got := refresh(t, s.refresher(t, base, readOn), plan(base), last).Ranking
	if !reflect.DeepEqual(got, last.Ranking) {
		t.Fatalf("ranking = %+v, want %+v untouched", got, last.Ranking)
	}
}

// rankedModels is n models, each ranked with counts.
func rankedModels(n int) []providers.RankedModel {
	models := make([]providers.RankedModel, n)
	for i := range models {
		models[i] = providers.RankedModel{Model: "command-code/m-" + strconv.Itoa(i), Settled: 5, Passed: 5}
	}
	return models
}

func TestRefreshCutsAndSaysSoWhenARankedListIsOverFifty(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		ranked, below         int
		wantCut               bool
		wantRanked, wantBelow int
	}{
		{"the ranked list over", 51, 2, true, 50, 2},
		{"the below-bar list over", 2, 51, true, 2, 50},
		{"both at fifty", 50, 50, false, 50, 50},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, base := newSources(t)
			r := s.refresher(t, base, readOn)
			var calls []time.Time
			r.Rank = rankWith([]providers.SizeRanking{{Size: "S", Ranked: rankedModels(tt.ranked), BelowBar: rankedModels(tt.below)}}, nil, &calls)

			ranking := refresh(t, r, plan(base), providers.Facts{}).Ranking
			if cut := strings.Contains(ranking.Source.Note, "cut"); cut != tt.wantCut {
				t.Fatalf("note = %q, want a cut reported: %t", ranking.Source.Note, tt.wantCut)
			}
			if got := ranking.Sizes[0]; len(got.Ranked) != tt.wantRanked || len(got.BelowBar) != tt.wantBelow {
				t.Fatalf("lists hold %d and %d models, want %d and %d", len(got.Ranked), len(got.BelowBar), tt.wantRanked, tt.wantBelow)
			}
		})
	}
}
