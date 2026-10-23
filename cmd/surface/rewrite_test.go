package main

import (
	"encoding/json"
	"maps"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// seriesOf is the rolling series the autonomy endpoint served.
func seriesOf(t *testing.T, rec *httptest.ResponseRecorder) []any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	series, ok := rewrite["series"].([]any)
	if !ok {
		t.Fatalf("series = %v, want a list", rewrite["series"])
	}
	return series
}

// TestAutonomyReportsTheRewriteFiguresThePageDraws asserts the endpoint serves
// the tiles, the PRs the rate is measured on and the same figures split by size
// and by repository, every rate pooled over the lines rather than a mean of the
// PRs' own rates (AC1, AC3, AC4, AC5, AC6).
func TestAutonomyReportsTheRewriteFiguresThePageDraws(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	reader := &stubMeasured{merges: []dispatcher.Sample{
		{Run: "run-2", Repo: "octo/scratch", Size: "M", PR: 143, MergedAt: at,
			PRURL:   "https://github.com/octo/scratch/pull/143",
			Title:   "feat(runner): FRG-143 add the widget",
			Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 50}},
		{Run: "run-1", Repo: "octo/other", Size: "S", PR: 142, MergedAt: at.Add(-time.Hour),
			Rewrite: dispatcher.Rewrite{AgentLines: 10, OwnerLines: 10}},
		{Run: "run-0", PR: 141, MergedAt: at.Add(-2 * time.Hour),
			Rewrite: dispatcher.Rewrite{OwnerLines: 4}},
	}}
	rec := getAutonomy(t, reader)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	if got, want := slices.Sorted(maps.Keys(rewrite)), []string{"by_repo", "by_size", "prs", "series", "tiles"}; !slices.Equal(got, want) {
		t.Errorf("rewrite keys = %v, want %v", got, want)
	}

	tiles, ok := rewrite["tiles"].([]any)
	if !ok || len(tiles) != 3 {
		t.Fatalf("tiles = %v, want the trailing, five and one windows", rewrite["tiles"])
	}
	trailing, ok := tiles[0].(map[string]any)
	if !ok {
		t.Fatalf("trailing tile = %v, want an object", tiles[0])
	}
	if got, want := slices.Sorted(maps.Keys(trailing)), []string{"agent_lines", "asked", "owner_lines", "prs", "rate"}; !slices.Equal(got, want) {
		t.Errorf("tile keys = %v, want %v", got, want)
	}
	// The three PRs' own rates are 50%, 100% and +Inf, which a mean of them
	// would not read as the 64 owner lines over the 110 agent lines (AC6).
	if got, want := trailing["rate"], "58.2%"; got != want {
		t.Errorf("trailing rate = %v, want %v", got, want)
	}
	if got, want := trailing["asked"], 10.0; got != want {
		t.Errorf("trailing asked = %v, want %v", got, want)
	}
	if got, want := trailing["agent_lines"], 110.0; got != want {
		t.Errorf("trailing agent lines = %v, want %v", got, want)
	}
	if got, want := trailing["owner_lines"], 64.0; got != want {
		t.Errorf("trailing owner lines = %v, want %v", got, want)
	}
	if prs, ok := trailing["prs"].([]any); !ok || len(prs) != 3 || prs[0] != 143.0 || prs[2] != 141.0 {
		t.Errorf("trailing prs = %v, want the three PRs newest first", trailing["prs"])
	}
	// The one-PR window holds the newest merge and says it asked for one.
	last, ok := tiles[2].(map[string]any)
	if !ok || last["asked"] != 1.0 || last["rate"] != "50.0%" {
		t.Errorf("one-PR tile = %v, want the newest merge's own window", tiles[2])
	}
	if prs, ok := last["prs"].([]any); !ok || len(prs) != 1 || prs[0] != 143.0 {
		t.Errorf("one-PR window = %v, want the newest PR alone", last["prs"])
	}
	// Three measured PRs fill no window, so the series has no point (AC2).
	if series, ok := rewrite["series"].([]any); !ok || len(series) != 0 {
		t.Errorf("series = %v, want no point under ten measured PRs", rewrite["series"])
	}

	prs, ok := rewrite["prs"].([]any)
	if !ok || len(prs) != 3 {
		t.Fatalf("prs = %v, want the three measured merges", rewrite["prs"])
	}
	newest, ok := prs[0].(map[string]any)
	if !ok {
		t.Fatalf("newest PR = %v, want an object", prs[0])
	}
	if got, want := slices.Sorted(maps.Keys(newest)), []string{"agent_lines", "merged_at", "owner_lines", "pr", "pr_url", "rate", "repo", "run", "size", "title"}; !slices.Equal(got, want) {
		t.Errorf("PR keys = %v, want %v", got, want)
	}
	if newest["run"] != "run-2" || newest["repo"] != "octo/scratch" || newest["size"] != "M" ||
		newest["pr"] != 143.0 || newest["agent_lines"] != 100.0 || newest["owner_lines"] != 50.0 ||
		newest["rate"] != "50.0%" || newest["merged_at"] != "2026-10-15T09:00:00Z" ||
		newest["title"] != "feat(runner): FRG-143 add the widget" ||
		newest["pr_url"] != "https://github.com/octo/scratch/pull/143" {
		t.Errorf("newest PR = %v, want its run, repository, size, title, lines, own rate and merge", newest)
	}
	if oldest, ok := prs[2].(map[string]any); !ok || oldest["rate"] != "no agent lines" || oldest["title"] != "" {
		t.Errorf("oldest PR = %v, want a PR with no agent lines and the empty title a row measured before titles reads as", prs[2])
	}

	sizes, ok := rewrite["by_size"].([]any)
	if !ok || len(sizes) != 4 {
		t.Fatalf("by_size = %v, want the S, M, L and unsized rows", rewrite["by_size"])
	}
	for i, want := range []string{"S", "M", "L", "unsized"} {
		row, ok := sizes[i].(map[string]any)
		if !ok || row["size"] != want {
			t.Fatalf("by_size[%d] = %v, want %s", i, sizes[i], want)
		}
		// Under ten PRs a row carries its count and no rates.
		if row["rate"] != nil || row["median"] != nil || row["p90"] != nil {
			t.Errorf("%s = %v, want no rates below ten PRs", want, row)
		}
	}
	if m, _ := sizes[1].(map[string]any); m["prs"] != 1.0 {
		t.Errorf("M = %v, want its single measured PR counted", sizes[1])
	}
	if unsized, _ := sizes[3].(map[string]any); unsized["prs"] != 1.0 {
		t.Errorf("unsized = %v, want the PR naming no size counted", sizes[3])
	}

	repos, ok := rewrite["by_repo"].([]any)
	if !ok || len(repos) != 3 {
		t.Fatalf("by_repo = %v, want the two repositories and the PR naming none", rewrite["by_repo"])
	}
	for i, want := range []string{"octo/other", "octo/scratch", "unattributed"} {
		row, ok := repos[i].(map[string]any)
		if !ok || row["repo"] != want {
			t.Fatalf("by_repo[%d] = %v, want %s", i, repos[i], want)
		}
		if _, ok := row["tiles"].([]any); !ok {
			t.Errorf("%s = %v, want its own tiles", want, row)
		}
		if _, ok := row["series"].([]any); !ok {
			t.Errorf("%s = %v, want its own series", want, row)
		}
	}
	// Each repository is split over its own merges: the scratch one holds one PR
	// where the whole set holds three.
	scratch, _ := repos[1].(map[string]any)
	own, _ := scratch["tiles"].([]any)
	one, _ := own[0].(map[string]any)
	if one["asked"] != 10.0 || one["agent_lines"] != 100.0 || one["owner_lines"] != 50.0 || one["rate"] != "50.0%" {
		t.Errorf("scratch trailing tile = %v, want its own PR alone", own[0])
	}
}

// TestAutonomyReportsTheRollingRateOverTheMerges asserts the series a page plots:
// one point per merged PR with ten measured up to it, oldest first, each the rate
// pooled over its own window (AC2).
func TestAutonomyReportsTheRollingRateOverTheMerges(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	// Newest first: PR 110 merged last, and its ten PRs carry one more owner line
	// each, so the window they fill pools 55 lines over 1000.
	measured := make([]dispatcher.Sample, 0, 10)
	for i := range 10 {
		measured = append(measured, dispatcher.Sample{
			PR:       110 - i,
			MergedAt: at.Add(-time.Duration(i) * time.Hour),
			Rewrite:  dispatcher.Rewrite{AgentLines: 100, OwnerLines: int64(i + 1)},
		})
	}
	series := seriesOf(t, getAutonomy(t, &stubMeasured{merges: measured}))
	if len(series) != 1 {
		t.Fatalf("series = %v, want the one window ten measured PRs fill", series)
	}
	point, ok := series[0].(map[string]any)
	if !ok {
		t.Fatalf("point = %v, want an object", series[0])
	}
	if got, want := slices.Sorted(maps.Keys(point)), []string{"merged_at", "pr", "rate"}; !slices.Equal(got, want) {
		t.Errorf("point keys = %v, want %v", got, want)
	}
	if point["pr"] != 110.0 || point["rate"] != "5.5%" || point["merged_at"] != "2026-10-15T09:00:00Z" {
		t.Errorf("point = %v, want the newest merge at its own window's pooled rate", point)
	}

	// One merge more than the window fills adds the point before it, covering
	// that merge and the nine after it, while the newest window reads the same:
	// the series runs oldest first.
	measured = append(measured, dispatcher.Sample{PR: 100, MergedAt: at.Add(-10 * time.Hour),
		Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 100}})
	series = seriesOf(t, getAutonomy(t, &stubMeasured{merges: measured}))
	if len(series) != 2 {
		t.Fatalf("series = %v, want a point per merge with ten measured up to it", series)
	}
	for i, want := range []struct {
		pr   float64
		rate string
	}{{109, "15.4%"}, {110, "5.5%"}} {
		point, _ := series[i].(map[string]any)
		if point["pr"] != want.pr || point["rate"] != want.rate {
			t.Errorf("point %d = %v, want PR %v at %s", i, series[i], want.pr, want.rate)
		}
	}
}

// TestAutonomyCapsThePRListAtTheTrailingWindow asserts the list of PRs stops at
// the ten merges the tiles are read over, newest first, so a page lists the same
// window the rate is pooled over (AC3).
func TestAutonomyPrintsNoAgentLinesForAnEmptyWindowAndAPRWithNoLines(t *testing.T) {
	empty := getAutonomy(t, &stubMeasured{})
	if strings.Contains(empty.Body.String(), `"rate":"0.0%"`) {
		t.Fatalf("body = %s, want no measured zero over no merges", empty.Body.String())
	}
	var body struct {
		Rewrite map[string][]map[string]any `json:"rewrite"`
	}
	if err := json.Unmarshal(empty.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	tiles := body.Rewrite["tiles"]
	if len(tiles) == 0 {
		t.Fatal("tiles empty, want the windows")
	}
	for _, tile := range tiles {
		if tile["rate"] != "no agent lines" {
			t.Errorf("empty tile = %v, want the words for no agent lines", tile)
		}
	}

	rec := getAutonomy(t, &stubMeasured{merges: []dispatcher.Sample{{PR: 7}}})
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if pr := body.Rewrite["prs"][0]; pr["rate"] != "no agent lines" {
		t.Errorf("PR with no lines = %v, want the words for no agent lines", pr)
	}
}

func TestAutonomyFoldsAPRsSizeIntoTheBucketBySizeUses(t *testing.T) {
	measured := []dispatcher.Sample{
		{PR: 4, Size: "XL", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
		{PR: 3, Size: "s", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
		{PR: 2, Size: "", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
		{PR: 1, Size: "L", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
	}
	rec := getAutonomy(t, &stubMeasured{merges: measured})
	var body struct {
		Rewrite map[string][]map[string]any `json:"rewrite"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"unsized", "unsized", "unsized", "L"} {
		if got := body.Rewrite["prs"][i]["size"]; got != want {
			t.Errorf("prs[%d].size = %v, want %s", i, got, want)
		}
	}
}

func TestAutonomyCapsThePRListAtTheTrailingWindow(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	measured := make([]dispatcher.Sample, 0, dispatcher.TrailingPRs+1)
	for i := range dispatcher.TrailingPRs + 1 {
		measured = append(measured, dispatcher.Sample{
			PR:       200 - i,
			MergedAt: at.Add(-time.Duration(i) * time.Hour),
			Rewrite:  dispatcher.Rewrite{AgentLines: 100},
		})
	}
	rec := getAutonomy(t, &stubMeasured{merges: measured})

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	prs, ok := rewrite["prs"].([]any)
	if !ok || len(prs) != dispatcher.TrailingPRs {
		t.Fatalf("prs = %v, want the newest %d merges", rewrite["prs"], dispatcher.TrailingPRs)
	}
	newest, _ := prs[0].(map[string]any)
	oldest, _ := prs[len(prs)-1].(map[string]any)
	if newest["pr"] != 200.0 || oldest["pr"] != 191.0 {
		t.Errorf("prs run %v to %v, want the newest ten", newest["pr"], oldest["pr"])
	}
}

// TestAutonomyReportsARatedSizesSpreadAndWithholdsTheOneItLacks asserts a size
// with the PRs its rates need carries the median and p90 of their own rates, and
// a size whose PRs all have +Inf rates carries its pooled rate with neither,
// which a zero would otherwise read as a clean spread (AC4).
func TestAutonomyReportsARatedSizesSpreadAndWithholdsTheOneItLacks(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	var measured []dispatcher.Sample
	for range dispatcher.MinPRsPerSize {
		measured = append(measured, dispatcher.Sample{
			Size: dispatcher.SizeMedium, MergedAt: at,
			Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 20},
		})
	}
	// A rated size whose PRs the agent wrote nothing in: +Inf rates, no spread.
	for range dispatcher.MinPRsPerSize {
		measured = append(measured, dispatcher.Sample{
			Size: dispatcher.SizeLarge, MergedAt: at,
			Rewrite: dispatcher.Rewrite{OwnerLines: 10},
		})
	}
	rec := getAutonomy(t, &stubMeasured{merges: measured})

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	sizes, ok := rewrite["by_size"].([]any)
	if !ok || len(sizes) != 4 {
		t.Fatalf("by_size = %v, want the S, M, L and unsized rows", rewrite["by_size"])
	}
	if m, _ := sizes[1].(map[string]any); m["size"] != dispatcher.SizeMedium ||
		m["prs"] != float64(dispatcher.MinPRsPerSize) ||
		m["rate"] != "20.0%" || m["median"] != "20.0%" || m["p90"] != "20.0%" {
		t.Errorf("M = %v, want its pooled rate with the median and p90 of its own rates", sizes[1])
	}
	if lazy, _ := sizes[2].(map[string]any); lazy["size"] != dispatcher.SizeLarge ||
		lazy["prs"] != float64(dispatcher.MinPRsPerSize) ||
		lazy["rate"] != "no agent lines" || lazy["median"] != nil || lazy["p90"] != nil {
		t.Errorf("L = %v, want its +Inf pooled rate with no spread", sizes[2])
	}
}
