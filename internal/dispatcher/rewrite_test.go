package dispatcher

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestRewriteOfCountsTheRunnersCommitsForTheAgent(t *testing.T) {
	for name, tt := range map[string]struct {
		commits []Commit
		want    Rewrite
	}{
		"the runner's pair": {
			[]Commit{
				{Login: runner.CommitAuthorLogin, Additions: 10, Deletions: 2},
				{Name: "the owner", Email: "owner@example.com", Additions: 3, Deletions: 1},
			},
			Rewrite{AgentLines: 12, OwnerLines: 4, UnlabelledLines: 4},
		},
		"the bot's name and email on an owner's login": {
			[]Commit{{Name: runner.CommitAuthorName, Email: runner.CommitAuthorEmail, Login: "the-owner", Additions: 10}},
			Rewrite{OwnerLines: 10, UnlabelledLines: 10},
		},
		"the bot's name and email with no login": {
			[]Commit{{Name: runner.CommitAuthorName, Email: runner.CommitAuthorEmail, Additions: 7}},
			Rewrite{OwnerLines: 7, UnlabelledLines: 7},
		},
		"a login differing only in case": {
			[]Commit{{Login: "GitHub-Actions[bot]", Additions: 5}},
			Rewrite{AgentLines: 5},
		},
		"the owner alone": {
			[]Commit{{Name: "the owner", Email: "owner@example.com", Additions: 20, Deletions: 4}},
			Rewrite{OwnerLines: 24, UnlabelledLines: 24},
		},
		"a commit with no author": {
			[]Commit{{Additions: 3}},
			Rewrite{OwnerLines: 3, UnlabelledLines: 3},
		},
		"no commits at all": {nil, Rewrite{}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := RewriteOf(tt.commits); got != tt.want {
				t.Fatalf("RewriteOf = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRateIsTheOwnersLinesOverTheAgents(t *testing.T) {
	for name, tt := range map[string]struct {
		lines Rewrite
		want  float64
	}{
		"nothing rewritten":   {Rewrite{AgentLines: 100}, 0},
		"a tenth rewritten":   {Rewrite{AgentLines: 100, OwnerLines: 10}, 0.1},
		"all of it rewritten": {Rewrite{AgentLines: 100, OwnerLines: 100}, 1},
		"no lines at all":     {Rewrite{}, 0},
		"no agent lines":      {Rewrite{OwnerLines: 5}, math.Inf(1)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tt.lines.Rate(); got != tt.want {
				t.Fatalf("Rate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWindowCapsTheTrailingSamples(t *testing.T) {
	trailing := samples(scratch, 5, 100, 0)
	for name, tt := range map[string]struct {
		n    int
		want int
	}{
		"a window under five": {3, 3},
		"a window over five":  {10, 5},
		"a window of none":    {0, 0},
		"a window of five":    {5, 5},
	} {
		t.Run(name, func(t *testing.T) {
			got := Evidence{Trailing: trailing}.Window(tt.n)
			if len(got) != tt.want {
				t.Fatalf("Window(%d) = %d samples, want %d", tt.n, len(got), tt.want)
			}
		})
	}
}

func TestLinesAndRateSumTheWindow(t *testing.T) {
	window := []Sample{
		sample(scratch, 100, 10, true, false),
		sample(scratch, 100, 0, true, false),
	}
	if got := Lines(window); got != (Rewrite{AgentLines: 200, OwnerLines: 10}) {
		t.Fatalf("Lines() = %+v, want 200 agent lines and 10 owner lines", got)
	}
	rate, prs := Lines(window).Rate(), len(window)
	if rate != 0.05 || prs != 2 {
		t.Fatalf("Rate() = %v over %d PRs, want 0.05 over 2", rate, prs)
	}
}

func TestRevertsCountsTheAutoMergedRevertsAlone(t *testing.T) {
	window := []Sample{
		sample(scratch, 100, 0, true, true),
		sample(scratch, 100, 0, false, true),
	}
	if got := Reverts(window); got != 1 {
		t.Fatalf("Reverts() = %d, want the auto-merged revert alone", got)
	}
}

func TestWindowRatePoolsTheLinesRatherThanMeanTheRates(t *testing.T) {
	// The PRs' own rates are 50% and 100%, which their mean would read as 75%;
	// the window reads the 60 owner lines over the 110 agent lines (AC6).
	window := []Sample{
		{PR: 12, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 50}},
		{PR: 11, Rewrite: Rewrite{AgentLines: 10, OwnerLines: 10}},
	}
	w := WindowRate(window, 10)
	if w.Rate != 60.0/110.0 {
		t.Fatalf("Rate() = %v, want the pooled 60 owner lines over 110", w.Rate)
	}
	if w.Lines != (Rewrite{AgentLines: 110, OwnerLines: 60}) {
		t.Fatalf("Lines = %+v, want the window's lines pooled", w.Lines)
	}
	// The window asks for ten PRs and holds two, so it says how many it holds.
	if w.Asked != 10 || !slices.Equal(w.PRs, []int{12, 11}) {
		t.Fatalf("window = %+v, want the ten asked for over the two it holds", w)
	}
	if one := WindowRate(window, 1); len(one.PRs) != 1 || one.PRs[0] != 12 || one.Rate != 0.5 {
		t.Fatalf("WindowRate(window, 1) = %+v, want the newest PR alone at 50%%", one)
	}
	if none := WindowRate(nil, 10); none.Asked != 10 || len(none.PRs) != 0 || none.Rate != 0 {
		t.Fatalf("WindowRate(nil, 10) = %+v, want the ten asked for over no PRs", none)
	}
	if none := WindowRate(window, -1); none.Asked != 0 || len(none.PRs) != 0 || none.Rate != 0 {
		t.Fatalf("WindowRate(window, -1) = %+v, want a window of none", none)
	}
	if inf := WindowRate([]Sample{{PR: 9, Rewrite: Rewrite{OwnerLines: 4}}}, 10); !math.IsInf(inf.Rate, 1) {
		t.Fatalf("Rate() = %v, want +Inf for a window with no agent lines", inf.Rate)
	}
}

func TestRollingSeriesIsEachWindowsOwnPooledRate(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	// Newest first: PR 12 merged last, each PR carrying its own number in owner
	// lines, so every window's pooled rate is its own.
	var samples []Sample
	for pr := 12; pr >= 1; pr-- {
		samples = append(samples, Sample{
			PR:       pr,
			MergedAt: at.Add(-time.Duration(12-pr) * time.Hour),
			Rewrite:  Rewrite{AgentLines: 100, OwnerLines: int64(pr)},
		})
	}
	// Nine measured PRs fill no window, so the series has no point.
	if points := RollingSeries(samples[:9], 10, SeriesPoints); len(points) != 0 {
		t.Fatalf("RollingSeries of nine PRs = %+v, want no point", points)
	}
	points := RollingSeries(samples, 10, SeriesPoints)
	if len(points) != 3 {
		t.Fatalf("RollingSeries = %+v, want a point for each of the three PRs with ten measured up to it", points)
	}
	// Oldest first, each point the rate pooled over its own ten PRs.
	for i, want := range []struct {
		pr   int
		rate float64
	}{{10, 55.0 / 1000.0}, {11, 65.0 / 1000.0}, {12, 75.0 / 1000.0}} {
		if points[i].PR != want.pr || points[i].Rate != want.rate || points[i].AgentLines != 1000 {
			t.Fatalf("point %d = %+v, want PR %d at %v", i, points[i], want.pr, want.rate)
		}
	}
	if !points[0].MergedAt.Before(points[2].MergedAt) {
		t.Fatalf("series runs %v then %v, want the oldest window first", points[0].MergedAt, points[2].MergedAt)
	}
	// A cap shorter than the history keeps the newest points.
	if capped := RollingSeries(samples, 10, 2); len(capped) != 2 || capped[0].PR != 11 || capped[1].PR != 12 {
		t.Fatalf("RollingSeries capped at two = %+v, want the newest two points", capped)
	}
	// A window of no PRs is no rate, and a cap below none is an empty series.
	if none := RollingSeries(samples, 0, SeriesPoints); len(none) != 0 {
		t.Fatalf("RollingSeries over no window = %+v, want no point", none)
	}
	if none := RollingSeries(samples, 10, -1); len(none) != 0 {
		t.Fatalf("RollingSeries capped below none = %+v, want no point", none)
	}
}

func TestBySizeGroupsTheSizesAndTheirRates(t *testing.T) {
	var samples []Sample
	// Ten M PRs, each eight agent lines and one more owner line than the last:
	// the per-PR rates run 0%, 12.5% … 112.5%.
	for owner := range int64(10) {
		samples = append(samples, Sample{Size: SizeMedium, Rewrite: Rewrite{AgentLines: 8, OwnerLines: owner}})
	}
	// A PR the agent wrote nothing in is measured, so it counts, but its rate is
	// +Inf and no median or percentile holds it.
	samples = append(samples, Sample{Size: SizeMedium, Rewrite: Rewrite{OwnerLines: 5}})
	// A size at exactly the count its rates need, and ones below it.
	for range MinPRsForSpread {
		samples = append(samples, Sample{Size: SizeLarge, Rewrite: Rewrite{AgentLines: 8, OwnerLines: 1}})
	}
	// The unsized row holds one PR below the count its rates need: one naming a
	// size the ladder does not carry, which counts with those naming none.
	samples = append(samples, Sample{Size: aboveCeiling, Rewrite: Rewrite{AgentLines: 8, OwnerLines: 1}})
	for range MinPRsForSpread - 2 {
		samples = append(samples, Sample{Rewrite: Rewrite{AgentLines: 8, OwnerLines: 1}})
	}

	rows := BySize(samples)
	if len(rows) != 4 {
		t.Fatalf("BySize = %+v, want the S, M, L and unsized rows", rows)
	}
	for i, want := range []struct {
		size   string
		prs    int
		spread bool
	}{
		{SizeSmall, 0, false},
		{SizeMedium, 11, true},
		{SizeLarge, MinPRsForSpread, true},
		{SizeUnsized, MinPRsForSpread - 1, false},
	} {
		if row := rows[i]; row.Size != want.size || row.PRs != want.prs || row.Spread != want.spread {
			t.Errorf("BySize[%d] = %+v, want %s with %d PRs and its spread %v", i, row, want.size, want.prs, want.spread)
		}
	}
	// The eleven M PRs' 50 owner lines over their 80 agent lines; the median of
	// the ten measured rates is the mean of its two middle ones, and the p90 is
	// the ninth of them.
	m := rows[1]
	if m.Rate != 50.0/80.0 || m.Median != 0.5625 || m.P90 != 1 {
		t.Fatalf("M = %+v, want a 62.5%% pooled rate, a 56.25%% median and a 100%% p90", m)
	}
	// A size under ten PRs still carries its pooled rate, with no median or p90:
	// the nine unsized PRs hold nine owner lines over seventy-two agent lines.
	if unsized := rows[3]; unsized.Rate != 9.0/72.0 || unsized.Median != 0 || unsized.P90 != 0 {
		t.Errorf("unsized = %+v, want its pooled rate and no spread under ten PRs", unsized)
	}
}

func TestBySizeTakesTheMiddleRateOfAnOddCount(t *testing.T) {
	var samples []Sample
	// Eleven S PRs of eight agent lines, owner lines 0 … 10: the middle rate is
	// the sixth, 5/8, and the p90 is the tenth, 9/8.
	for owner := range int64(11) {
		samples = append(samples, Sample{Size: SizeSmall, Rewrite: Rewrite{AgentLines: 8, OwnerLines: owner}})
	}
	if s := BySize(samples)[0]; s.Median != 0.625 || s.P90 != 1.125 {
		t.Fatalf("S = %+v, want a 62.5%% median and a 112.5%% p90", s)
	}
}

func TestSizeBucketFoldsAnythingButTheLadderIntoUnsized(t *testing.T) {
	for size, want := range map[string]string{"S": SizeSmall, "M": SizeMedium, "L": SizeLarge, "": SizeUnsized, "XL": SizeUnsized, "s": SizeUnsized} {
		if got := SizeBucket(size); got != want {
			t.Errorf("SizeBucket(%q) = %q, want %q", size, got, want)
		}
	}
}

// TestBySizeReadsNoSpreadForASizeTheAgentWroteNothingIn asserts a size at ten PRs
// whose measured PRs all have +Inf rates carries its count and the rate pooled
// over it, with no spread of its own for a median or a p90 to rank.
func TestBySizeReadsNoSpreadForASizeTheAgentWroteNothingIn(t *testing.T) {
	samples := make([]Sample, 0, MinPRsForSpread)
	for range MinPRsForSpread {
		samples = append(samples, Sample{Size: SizeLarge, Rewrite: Rewrite{OwnerLines: 10}})
	}
	rows := BySize(samples)
	if lazy := rows[2]; lazy.PRs != MinPRsForSpread || lazy.Spread || !math.IsInf(lazy.Rate, 1) {
		t.Fatalf("L = %+v, want its count, its +Inf pooled rate and no spread", lazy)
	}
}

func TestByRepoSeparatesTheRepositoriesAndTheUnattributedRows(t *testing.T) {
	samples := []Sample{
		{Repo: "octo/scratch", PR: 141, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 5}},
		{Repo: "octo/other", PR: 140, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 20}},
		{Repo: "octo/scratch", PR: 139, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 10}},
		{PR: 138, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 30}},
	}
	rows := ByRepo(samples)
	if len(rows) != 3 {
		t.Fatalf("ByRepo = %+v, want the two repositories and the unattributed rows", rows)
	}
	if got := []string{rows[0].Repo, rows[1].Repo, rows[2].Repo}; !slices.Equal(got, []string{"octo/other", "octo/scratch", RepoUnattributed}) {
		t.Fatalf("repos = %v, want the repositories sorted and unattributed last", got)
	}
	scratch := rows[1]
	if len(scratch.Tiles) != 3 || scratch.Tiles[0].Asked != TrailingPRs ||
		scratch.Tiles[1].Asked != ShortPRs || scratch.Tiles[2].Asked != 1 {
		t.Fatalf("scratch tiles = %+v, want the trailing, five and one windows", scratch.Tiles)
	}
	// Its own two PRs pooled: 15 owner lines over 200 agent lines.
	if !slices.Equal(scratch.Tiles[0].PRs, []int{141, 139}) || scratch.Tiles[0].Rate != 15.0/200.0 {
		t.Fatalf("scratch trailing = %+v, want its own two PRs pooled", scratch.Tiles[0])
	}
	// Two measured PRs fill no window, so the repository has no point either.
	if len(scratch.Series) != 0 {
		t.Fatalf("scratch series = %+v, want no point under ten measured PRs", scratch.Series)
	}
	if other := rows[0]; other.Tiles[0].Lines.OwnerLines != 20 || len(other.Tiles[0].PRs) != 1 {
		t.Fatalf("other = %+v, want its own PR alone", other)
	}
	if unattributed := rows[2]; unattributed.Tiles[0].Lines.OwnerLines != 30 || len(unattributed.Tiles[0].PRs) != 1 {
		t.Fatalf("unattributed = %+v, want the PR naming no repository", unattributed)
	}
	if unattributed := rows[2]; unattributed.Average.PRs != 1 || unattributed.Average.Lines.OwnerLines != 30 {
		t.Fatalf("unattributed average = %+v, want the PR naming no repository", unattributed.Average)
	}
	// No unattributed entry while every measured PR names a repository.
	if named := ByRepo(samples[:3]); len(named) != 2 {
		t.Fatalf("ByRepo = %+v, want no unattributed entry when every PR names one", named)
	}
}

func TestGroupOfCarriesTheSplitAndTheNoAgentFigures(t *testing.T) {
	g := GroupOf([]Sample{
		{Rewrite: Rewrite{AgentLines: 100, OwnerLines: 10, ReviewFoundLines: 6, OwnerChangeLines: 3, UnlabelledLines: 1}},
		{Rewrite: Rewrite{AgentLines: 50, OwnerLines: 5, UnlabelledLines: 5}},
		{Rewrite: Rewrite{OwnerLines: 7, UnlabelledLines: 7}},
		{},
	})
	want := Rewrite{AgentLines: 150, OwnerLines: 22, ReviewFoundLines: 6, OwnerChangeLines: 3, UnlabelledLines: 13}
	if g.PRs != 4 || g.Lines != want || g.Rate != 22.0/150.0 {
		t.Fatalf("group = %+v, want four PRs with their lines and the split pooled", g)
	}
	if g.NoAgentLinesPRs != 2 || g.NoAgentOwnerLines != 7 {
		t.Fatalf("group = %+v, want the two PRs the agent wrote nothing in, and the owner's 7 lines in them", g)
	}

	// Eight PRs with rates 0 … 7/8 and two the agent wrote nothing in: the PRs
	// with no lines count in the ten, and the ranking holds only the eight.
	var ranked []Sample
	for owner := range int64(8) {
		ranked = append(ranked, Sample{Rewrite: Rewrite{AgentLines: 8, OwnerLines: owner}})
	}
	ranked = append(ranked, Sample{Rewrite: Rewrite{OwnerLines: 3}}, Sample{Rewrite: Rewrite{OwnerLines: 4}})
	g = GroupOf(ranked)
	if g.PRs != 10 || g.NoAgentLinesPRs != 2 || g.NoAgentOwnerLines != 7 {
		t.Fatalf("group = %+v, want ten PRs of which two have no agent lines", g)
	}
	if !g.Spread || g.Median != 7.0/16.0 || g.P90 != 7.0/8.0 {
		t.Fatalf("group = %+v, want the median and p90 of the eight ranked rates", g)
	}
}

func TestGroupOfWithholdsMedianAndP90UnderTenPRs(t *testing.T) {
	measured := func(n int) []Sample {
		out := make([]Sample, 0, n)
		for range n {
			out = append(out, Sample{Rewrite: Rewrite{AgentLines: 8, OwnerLines: 1}})
		}
		return out
	}
	unmeasurable := make([]Sample, 0, MinPRsForSpread)
	for range MinPRsForSpread {
		unmeasurable = append(unmeasurable, Sample{Rewrite: Rewrite{OwnerLines: 1}})
	}
	for name, tt := range map[string]struct {
		samples []Sample
		spread  bool
	}{
		"nine PRs":                        {measured(MinPRsForSpread - 1), false},
		"ten PRs":                         {measured(MinPRsForSpread), true},
		"ten PRs none of them has a rate": {unmeasurable, false},
		"no PRs at all":                   {nil, false},
	} {
		g := GroupOf(tt.samples)
		if g.Spread != tt.spread {
			t.Errorf("%s: spread = %v, want %v", name, g.Spread, tt.spread)
		}
		if !g.Spread && (g.Median != 0 || g.P90 != 0) {
			t.Errorf("%s: group = %+v, want no median or p90", name, g)
		}
	}
}

func TestByModelGroupsEachPhaseByItsOwnModel(t *testing.T) {
	phased := func(owner int64, plan, build, review string) Sample {
		return Sample{PlanModel: plan, BuildModel: build, ReviewModel: review, Rewrite: Rewrite{AgentLines: 10, OwnerLines: owner}}
	}
	got := ByModel([]Sample{
		phased(1, "a", "x", "r1"),
		phased(2, "a", "x", ""),
		phased(4, "", "y", ""),
		phased(8, "b", "", "r1"),
		phased(16, "b", "x", "r2"),
	})
	names := func(rows []ModelRow) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Model)
		}
		return out
	}
	// A tie on PRs reads by name, and the unknown group is last whatever its count.
	for phase, want := range map[string]struct {
		rows   []ModelRow
		models []string
		owner  []int64
	}{
		"plan":   {got.Plan, []string{"a", "b", ModelUnknown}, []int64{3, 24, 4}},
		"build":  {got.Build, []string{"x", "y", ModelUnknown}, []int64{19, 4, 8}},
		"review": {got.Review, []string{"r1", "r2", ModelUnknown}, []int64{9, 16, 6}},
	} {
		if !slices.Equal(names(want.rows), want.models) {
			t.Errorf("%s models = %v, want %v", phase, names(want.rows), want.models)
			continue
		}
		for i, r := range want.rows {
			if r.Lines.OwnerLines != want.owner[i] {
				t.Errorf("%s %s = %+v, want %d owner lines over its own PRs", phase, r.Model, r, want.owner[i])
			}
		}
	}
	if empty := ByModel(nil); len(empty.Plan) != 0 || len(empty.Build) != 0 || len(empty.Review) != 0 {
		t.Fatalf("ByModel(nil) = %+v, want no model in any phase", empty)
	}
}

func TestByRepoCarriesTheRepositorysAverageOverAllItsPRs(t *testing.T) {
	// Twelve PRs of one repository: its trailing window holds ten of them and
	// its average all twelve.
	rows := ByRepo(samples("octo/scratch", TrailingPRs+2, 100, 10))
	if len(rows) != 1 {
		t.Fatalf("ByRepo = %+v, want the one repository", rows)
	}
	row := rows[0]
	if len(row.Tiles[0].PRs) != TrailingPRs || row.Tiles[0].Lines.AgentLines != 1000 {
		t.Fatalf("trailing = %+v, want the newest ten PRs", row.Tiles[0])
	}
	if row.Average.PRs != TrailingPRs+2 || row.Average.Lines.AgentLines != 1200 || row.Average.Rate != 0.1 {
		t.Fatalf("average = %+v, want all twelve PRs pooled", row.Average)
	}
}

func TestTilesTrendComparesTheWindowWithItsPredecessors(t *testing.T) {
	// Each PR carries 1000 agent lines, so its owner lines are its rate in
	// thousandths; the owners run newest first, and the last-PR window moves
	// from the oldest to the newest.
	prs := func(owners ...int64) []Sample {
		out := make([]Sample, 0, len(owners))
		for i, owner := range owners {
			out = append(out, Sample{PR: 100 - i, Rewrite: Rewrite{AgentLines: 1000, OwnerLines: owner}})
		}
		return out
	}
	for name, tt := range map[string]struct {
		samples     []Sample
		trend       Trend
		hasPrevious bool
	}{
		"two falls of 0.6 points":                       {prs(100, 106, 112), TrendDown, true},
		"two rises of 0.6 points":                       {prs(112, 106, 100), TrendUp, true},
		"a fall then a rise":                            {prs(100, 106, 100), TrendFlat, true},
		"a rise then a fall":                            {prs(106, 100, 106), TrendFlat, true},
		"a latest fall of 0.4 points":                   {prs(100, 104, 110), TrendFlat, true},
		"a latest fall of exactly 0.5 points":           {prs(100, 105, 110), TrendDown, true},
		"a latest rise of exactly 0.5 points":           {prs(110, 105, 100), TrendUp, true},
		"an earlier move of none":                       {prs(100, 105, 105), TrendFlat, true},
		"one PR has no predecessor":                     {prs(100), TrendFlat, false},
		"two PRs have no second move":                   {prs(100, 106), TrendFlat, true},
		"a predecessor with no agent lines":             {append(prs(100), Sample{Rewrite: Rewrite{OwnerLines: 4}}, prs(112)[0]), TrendFlat, false},
		"an earlier PR with no agent lines":             {append(prs(100, 106), Sample{Rewrite: Rewrite{OwnerLines: 4}}), TrendFlat, true},
		"a rise over an earlier PR with no agent lines": {append(prs(112, 106), Sample{Rewrite: Rewrite{OwnerLines: 4}}), TrendFlat, true},
		"a latest PR with no agent lines":               {append([]Sample{{Rewrite: Rewrite{OwnerLines: 4}}}, prs(106, 112)...), TrendFlat, false},
	} {
		w := Tiles(tt.samples)[2]
		if w.Trend != tt.trend || w.HasPrevious != tt.hasPrevious {
			t.Errorf("%s: trend %q, has previous %v, want %q and %v", name, w.Trend, w.HasPrevious, tt.trend, tt.hasPrevious)
		}
	}

	if w := Tiles(prs(100, 106, 112))[2]; w.Previous != 0.106 {
		t.Errorf("previous = %v, want the rate one PR earlier, 0.106", w.Previous)
	}

	// The trailing window needs ten PRs before it has a predecessor and eleven
	// before the predecessor has one: the owners run newest first, each PR 1000
	// agent lines, 200 owner lines in the newest and 100 in every other.
	owners := func(n int) []Sample {
		out := []int64{200}
		for len(out) < n {
			out = append(out, 100)
		}
		return prs(out...)
	}
	if w := Tiles(owners(10))[0]; w.HasPrevious || w.Trend != TrendFlat || len(w.PRs) != 10 {
		t.Errorf("ten PRs: trailing = %+v, want no predecessor, as a window of nine is not a full one", w)
	}
	if w := Tiles(owners(11))[0]; !w.HasPrevious || w.Previous != 0.1 || w.Rate != 1100.0/10000.0 || w.Trend != TrendFlat {
		t.Errorf("eleven PRs: trailing = %+v, want its rate against the full ten after the newest and no trend yet", w)
	}
	// Eleven PRs leave the third window one PR short: owner lines 160 in the
	// oldest would read as a fall against it, were it compared.
	if w := Tiles(prs(100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 160))[0]; !w.HasPrevious || w.Trend != TrendFlat {
		t.Errorf("eleven PRs: trailing = %+v, want its previous rate and no trend over a short third window", w)
	}
	// Twelve PRs have three full windows: owner lines 100 in the ten newest and
	// 160 in the two oldest move the rate down by 0.6 points twice.
	twelve := prs(100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 160, 160)
	if w := Tiles(twelve)[0]; !w.HasPrevious || w.Trend != TrendDown {
		t.Errorf("twelve PRs: trailing = %+v, want a fall across three full windows", w)
	}
}

// Three merges are no trailing window, so neither the trailing-ten nor the
// last-five tile has a previous rate or a trend, while the last-PR tile does:
// its windows are single PRs, which are always full.
func TestTilesTrendNeedsFullWindows(t *testing.T) {
	tiles := Tiles([]Sample{
		{PR: 3, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 5}},
		{PR: 2, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 10}},
		{PR: 1, Rewrite: Rewrite{AgentLines: 100, OwnerLines: 30}},
	})
	for _, w := range tiles[:2] {
		if w.HasPrevious || w.Trend != TrendFlat {
			t.Errorf("tile of %d = %+v, want no previous rate and a flat trend over a history shorter than its window", w.Asked, w)
		}
	}
	if last := tiles[2]; !last.HasPrevious || last.Trend != TrendDown {
		t.Errorf("last-PR tile = %+v, want a fall of 5%% from 10%% from 30%%", last)
	}
}

// The trend reads the rates as printed: 1/16 prints as 6.2%, so its moves are
// those of the text, not of a rounding half away from zero.
func TestTilesTrendReadsTheRatesAsPrinted(t *testing.T) {
	pr := func(agent, owner int64) Sample { return Sample{Rewrite: Rewrite{AgentLines: agent, OwnerLines: owner}} }
	for name, tt := range map[string]struct {
		newest, previous, earlier Sample
		want                      Trend
	}{
		// 5.8%, 6.2% (6.25 prints to the even tenth), 7.0%: moves of -0.4 and -0.8.
		"a printed fall of 0.4 points": {pr(1000, 58), pr(16, 1), pr(1000, 70), TrendFlat},
		// 6.2%, 6.7%, 7.5%: moves of -0.5 and -0.8, though a rounding of 6.25 up reads -0.4.
		"a printed fall of exactly 0.5 points": {pr(16, 1), pr(1000, 67), pr(1000, 75), TrendDown},
	} {
		if got := Tiles([]Sample{tt.newest, tt.previous, tt.earlier})[2].Trend; got != tt.want {
			t.Errorf("%s: trend = %q, want %q", name, got, tt.want)
		}
	}
}
