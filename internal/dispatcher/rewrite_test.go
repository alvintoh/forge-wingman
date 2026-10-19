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
		if points[i].PR != want.pr || points[i].Rate != want.rate {
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
	for range MinPRsPerSize {
		samples = append(samples, Sample{Size: SizeLarge, Rewrite: Rewrite{AgentLines: 8, OwnerLines: 1}})
	}
	// The unsized row holds one PR below the count its rates need: one naming a
	// size the ladder does not carry, which counts with those naming none.
	samples = append(samples, Sample{Size: aboveCeiling, Rewrite: Rewrite{AgentLines: 8, OwnerLines: 1}})
	for range MinPRsPerSize - 2 {
		samples = append(samples, Sample{Rewrite: Rewrite{AgentLines: 8, OwnerLines: 1}})
	}

	rows := BySize(samples)
	if len(rows) != 4 {
		t.Fatalf("BySize = %+v, want the S, M, L and unsized rows", rows)
	}
	for i, want := range []struct {
		size  string
		prs   int
		rated bool
	}{
		{SizeSmall, 0, false},
		{SizeMedium, 11, true},
		{SizeLarge, MinPRsPerSize, true},
		{SizeUnsized, MinPRsPerSize - 1, false},
	} {
		if row := rows[i]; row.Size != want.size || row.PRs != want.prs || row.Rated != want.rated {
			t.Errorf("BySize[%d] = %+v, want %s with %d PRs and its rates %v", i, row, want.size, want.prs, want.rated)
		}
	}
	// The eleven M PRs' 50 owner lines over their 80 agent lines; the median of
	// the ten measured rates is the mean of its two middle ones, and the p90 is
	// the ninth of them.
	m := rows[1]
	if m.Rate != 50.0/80.0 || m.Median != 0.5625 || m.P90 != 1 {
		t.Fatalf("M = %+v, want a 62.5%% pooled rate, a 56.25%% median and a 100%% p90", m)
	}
	for _, row := range rows {
		if !row.Rated && (row.Rate != 0 || row.Median != 0 || row.P90 != 0) {
			t.Errorf("%s = %+v, want a size below ten carrying its count and no rates", row.Size, row)
		}
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

// TestBySizeReadsNoSpreadForASizeTheAgentWroteNothingIn asserts a rated size
// whose measured PRs all have +Inf rates carries its count and the rate pooled
// over it, with no spread of its own for a median or a p90 to rank (AC4).
func TestBySizeReadsNoSpreadForASizeTheAgentWroteNothingIn(t *testing.T) {
	samples := make([]Sample, 0, MinPRsPerSize)
	for range MinPRsPerSize {
		samples = append(samples, Sample{Size: SizeLarge, Rewrite: Rewrite{OwnerLines: 10}})
	}
	rows := BySize(samples)
	if lazy := rows[2]; lazy.PRs != MinPRsPerSize || !lazy.Rated || lazy.Spread || !math.IsInf(lazy.Rate, 1) {
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
	// No unattributed entry while every measured PR names a repository.
	if named := ByRepo(samples[:3]); len(named) != 2 {
		t.Fatalf("ByRepo = %+v, want no unattributed entry when every PR names one", named)
	}
}
