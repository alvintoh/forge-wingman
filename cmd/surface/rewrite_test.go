package main

import (
	"encoding/json"
	"maps"
	"math"
	"net/http/httptest"
	"slices"
	"strconv"
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
// PRs' own rates.
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
	if got, want := slices.Sorted(maps.Keys(rewrite)), []string{"bars", "by_model", "by_repo", "by_size", "prs", "series", "tiles"}; !slices.Equal(got, want) {
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
	if got, want := slices.Sorted(maps.Keys(trailing)), []string{"agent_lines", "asked", "owner_lines", "previous_rate_value", "prs", "rate", "rate_value", "trend"}; !slices.Equal(got, want) {
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
	if got, want := slices.Sorted(maps.Keys(newest)), []string{"agent_lines", "build_model", "merged_at", "owner_lines", "plan_model", "pr", "pr_url", "rate", "rate_value", "repo", "review_model", "run", "size", "title"}; !slices.Equal(got, want) {
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
	for i, want := range []struct {
		size string
		rate any
	}{{"S", "100.0%"}, {"M", "50.0%"}, {"L", nil}, {"unsized", "no agent lines"}} {
		row, ok := sizes[i].(map[string]any)
		if !ok || row["size"] != want.size {
			t.Fatalf("by_size[%d] = %v, want %s", i, sizes[i], want.size)
		}
		// Under ten PRs a row carries its pooled rate, null for a size holding no
		// PR, and no median or p90.
		if row["rate"] != want.rate || row["median"] != nil || row["p90"] != nil ||
			row["median_value"] != nil || row["p90_value"] != nil {
			t.Errorf("%s = %v, want the pooled rate %v and no spread below ten PRs", want.size, row, want.rate)
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
	if avg, _ := scratch["average"].(map[string]any); avg["prs"] != 1.0 || avg["rate"] != "50.0%" {
		t.Errorf("scratch average = %v, want its own PR's figures", scratch["average"])
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
			Repo:     "octo/scratch",
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
	if got, want := slices.Sorted(maps.Keys(point)), []string{"merged_at", "pr", "rate", "rate_value", "repo"}; !slices.Equal(got, want) {
		t.Errorf("point keys = %v, want %v", got, want)
	}
	if point["repo"] != "octo/scratch" || point["pr"] != 110.0 || point["rate"] != "5.5%" || point["merged_at"] != "2026-10-15T09:00:00Z" {
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
		Rewrite struct {
			Tiles []map[string]any `json:"tiles"`
			PRs   []map[string]any `json:"prs"`
		} `json:"rewrite"`
	}
	if err := json.Unmarshal(empty.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	tiles := body.Rewrite.Tiles
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
	if pr := body.Rewrite.PRs[0]; pr["rate"] != "no agent lines" {
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
		Rewrite struct {
			Tiles []map[string]any `json:"tiles"`
			PRs   []map[string]any `json:"prs"`
		} `json:"rewrite"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"unsized", "unsized", "unsized", "L"} {
		if got := body.Rewrite.PRs[i]["size"]; got != want {
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
	for range dispatcher.MinPRsForSpread {
		measured = append(measured, dispatcher.Sample{
			Size: dispatcher.SizeMedium, MergedAt: at,
			Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 20},
		})
	}
	// A rated size whose PRs the agent wrote nothing in: +Inf rates, no spread.
	for range dispatcher.MinPRsForSpread {
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
		m["prs"] != float64(dispatcher.MinPRsForSpread) ||
		m["rate"] != "20.0%" || m["median"] != "20.0%" || m["p90"] != "20.0%" {
		t.Errorf("M = %v, want its pooled rate with the median and p90 of its own rates", sizes[1])
	}
	if lazy, _ := sizes[2].(map[string]any); lazy["size"] != dispatcher.SizeLarge ||
		lazy["prs"] != float64(dispatcher.MinPRsForSpread) ||
		lazy["rate"] != "no agent lines" || lazy["median"] != nil || lazy["p90"] != nil {
		t.Errorf("L = %v, want its +Inf pooled rate with no spread", sizes[2])
	}
}

// sortedKeys is the keys of an object, sorted.
func sortedKeys(v any) []string {
	m, _ := v.(map[string]any)
	return slices.Sorted(maps.Keys(m))
}

// rewriteFigures is the rewrite object the endpoint served for merges.
func rewriteFigures(t *testing.T, merges []dispatcher.Sample) map[string]any {
	t.Helper()
	rec := getAutonomy(t, &stubMeasured{merges: merges})
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rewrite, ok := body["rewrite"].(map[string]any)
	if !ok {
		t.Fatalf("rewrite = %v, want the figures", body["rewrite"])
	}
	return rewrite
}

// groupKeys are the keys of a group's figures, the same for a size, a model and a
// repository's average.
var groupKeys = []string{
	"agent_lines", "median", "median_value", "no_agent_lines_prs", "no_agent_owner_lines",
	"owner_change_lines", "owner_lines", "p90", "p90_value", "prs", "rate", "rate_value",
	"review_found_lines", "unlabelled_lines",
}

// withModel is the group keys and the model a by_model entry names.
func withModel() []string {
	return slices.Sorted(slices.Values(append([]string{"model"}, groupKeys...)))
}

func TestAutonomyServesTheByModelFigures(t *testing.T) {
	phased := func(owner int64, plan, build, review string) dispatcher.Sample {
		return dispatcher.Sample{PlanModel: plan, BuildModel: build, ReviewModel: review,
			Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: owner,
				ReviewFoundLines: owner / 2, OwnerChangeLines: owner / 4, UnlabelledLines: owner - owner/2 - owner/4}}
	}
	rewrite := rewriteFigures(t, []dispatcher.Sample{
		phased(40, "p/a", "p/x", "p/r"),
		phased(20, "p/a", "p/x", ""),
		phased(8, "", "p/y", "p/r"),
	})
	models, ok := rewrite["by_model"].(map[string]any)
	if !ok || !slices.Equal(sortedKeys(models), []string{"build", "plan", "review"}) {
		t.Fatalf("by_model = %v, want exactly the plan, build and review phases", rewrite["by_model"])
	}
	build, _ := models["build"].([]any)
	if len(build) != 2 {
		t.Fatalf("build = %v, want the two build models", models["build"])
	}
	for _, row := range build {
		if got := sortedKeys(row); !slices.Equal(got, withModel()) {
			t.Errorf("build entry keys = %v, want %v", got, withModel())
		}
	}
	x, _ := build[0].(map[string]any)
	// The two PRs built on p/x: 60 owner lines split 30, 15 and 15 over 200 agent lines.
	if x["model"] != "p/x" || x["prs"] != 2.0 || x["agent_lines"] != 200.0 || x["owner_lines"] != 60.0 ||
		x["review_found_lines"] != 30.0 || x["owner_change_lines"] != 15.0 || x["unlabelled_lines"] != 15.0 ||
		x["rate"] != "30.0%" || x["median"] != nil {
		t.Errorf("p/x = %v, want its two PRs' lines, split and pooled rate", x)
	}
	// The PR whose review ran no model is the unknown group, listed last.
	review, _ := models["review"].([]any)
	if last, _ := review[len(review)-1].(map[string]any); len(review) != 2 || last["model"] != "unknown" || last["prs"] != 1.0 {
		t.Errorf("review = %v, want p/r then the unknown group", models["review"])
	}
	if plan, _ := models["plan"].([]any); len(plan) != 2 {
		t.Errorf("plan = %v, want p/a and the unknown group", models["plan"])
	}

	// No merge serves an empty list per phase, never a null.
	empty := getAutonomy(t, &stubMeasured{})
	if !strings.Contains(empty.Body.String(), `"by_model":{"plan":[],"build":[],"review":[]}`) {
		t.Errorf("body = %s, want every phase an empty list", empty.Body.String())
	}
}

// assertRateNumbers checks every rate printed as text in v has the number the
// chart plots beside it, and returns how many pairs it checked.
func assertRateNumbers(t *testing.T, path string, v any) int {
	t.Helper()
	checked := 0
	switch v := v.(type) {
	case []any:
		for i, item := range v {
			checked += assertRateNumbers(t, path+"["+strconv.Itoa(i)+"]", item)
		}
	case map[string]any:
		for key, child := range v {
			checked += assertRateNumbers(t, path+"."+key, child)
			if key != "rate" && key != "median" && key != "p90" {
				continue
			}
			number, ok := v[key+"_value"]
			if !ok {
				t.Errorf("%s has no %s_value beside it", path, key)
				continue
			}
			checked++
			text, isText := child.(string)
			switch {
			case !isText && child == nil && number == nil:
			case isText && text == rateNoAgentLines && number == nil:
			case isText && strings.HasSuffix(text, "%"):
				percent, err := strconv.ParseFloat(strings.TrimSuffix(text, "%"), 64)
				value, isNumber := number.(float64)
				// The text prints one decimal of a percent, so the number sits within
				// half of that of it.
				if err != nil || !isNumber || math.Abs(value-percent/100) > 0.0005+1e-9 {
					t.Errorf("%s.%s_value = %v, want the number behind %q", path, key, number, text)
				}
			default:
				t.Errorf("%s.%s = %v beside %v, want a number for a percentage and null for none", path, key, child, number)
			}
		}
	}
	return checked
}

func TestAutonomyEveryRateTextHasItsNumber(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	var merges []dispatcher.Sample
	for i := range 12 {
		merges = append(merges, dispatcher.Sample{PR: 200 - i, Repo: "octo/scratch", Size: "M", MergedAt: at.Add(-time.Duration(i) * time.Hour),
			Rewrite: dispatcher.Rewrite{AgentLines: 90 + int64(i), OwnerLines: 7 + int64(i)}})
	}
	// A PR the agent wrote nothing in, and a size of ten of them: null numbers
	// beside the words, and no spread.
	merges = append(merges, dispatcher.Sample{PR: 150, Rewrite: dispatcher.Rewrite{OwnerLines: 4}})
	for range dispatcher.MinPRsForSpread {
		merges = append(merges, dispatcher.Sample{Size: "L", Rewrite: dispatcher.Rewrite{OwnerLines: 3}})
	}
	rewrite := rewriteFigures(t, merges)
	if checked := assertRateNumbers(t, "rewrite", rewrite); checked < 50 {
		t.Errorf("checked %d rates, want every tile, point, PR, group and average of the figures", checked)
	}
	if empty := rewriteFigures(t, nil); assertRateNumbers(t, "empty", empty) == 0 {
		t.Error("checked no rate over no merges, want the tiles' words to read null beside them")
	}
	tiles, _ := rewrite["tiles"].([]any)
	if trailing, _ := tiles[0].(map[string]any); math.Abs(trailing["rate_value"].(float64)-trailing["owner_lines"].(float64)/trailing["agent_lines"].(float64)) > 1e-9 {
		t.Errorf("trailing = %v, want its rate_value the owner lines over the agent lines", trailing)
	}
}

func TestAutonomyServesTheBarFromTheGoConstant(t *testing.T) {
	bars, _ := rewriteFigures(t, nil)["bars"].(map[string]any)
	if !slices.Equal(sortedKeys(bars), []string{"rollback"}) || bars["rollback"] != 0.1 {
		t.Errorf("bars = %v, want the one bar, the trailing rate auto-merge is on at or under", bars)
	}
}

func TestAutonomyTilesCarryTheirTrend(t *testing.T) {
	// Each PR carries 1000 agent lines and ten more owner lines than the one
	// after it, so every window's rate falls as the history grows newer.
	merges := make([]dispatcher.Sample, 0, 12)
	for i := range 12 {
		merges = append(merges, dispatcher.Sample{PR: 300 - i, Repo: "octo/scratch",
			Rewrite: dispatcher.Rewrite{AgentLines: 1000, OwnerLines: int64(100 + 10*i)}})
	}
	rewrite := rewriteFigures(t, merges)
	repos, _ := rewrite["by_repo"].([]any)
	repo, _ := repos[0].(map[string]any)
	for name, tiles := range map[string]any{"all": rewrite["tiles"], "repository": repo["tiles"]} {
		for i, want := range []struct{ rate, previous float64 }{{0.145, 0.155}, {0.12, 0.13}, {0.1, 0.11}} {
			tile, _ := tiles.([]any)[i].(map[string]any)
			rate, _ := tile["rate_value"].(float64)
			previous, _ := tile["previous_rate_value"].(float64)
			if math.Abs(rate-want.rate) > 1e-9 || math.Abs(previous-want.previous) > 1e-9 || tile["trend"] != "down" {
				t.Errorf("%s tile %d = %v, want %v from %v and down", name, i, tile, want.rate, want.previous)
			}
		}
	}

	// One merge has no window before it, so it is flat with no previous rate.
	tiles, _ := rewriteFigures(t, merges[:1])["tiles"].([]any)
	if only, _ := tiles[2].(map[string]any); only["trend"] != "flat" || only["previous_rate_value"] != nil {
		t.Errorf("one-PR tile = %v, want flat with no previous rate", tiles[2])
	}

	// Eleven merges have a window before the trailing one but none before that,
	// so its trend is not computable and it serves no previous rate, which flat
	// would otherwise read as a measured one.
	tiles, _ = rewriteFigures(t, merges[:11])["tiles"].([]any)
	if trailing, _ := tiles[0].(map[string]any); trailing["trend"] != "flat" || trailing["previous_rate_value"] != nil {
		t.Errorf("eleven-PR trailing tile = %v, want flat with no previous rate", tiles[0])
	}
}

// A PR's number is its own only within its repository, so two repositories'
// PRs of one number are two points of the series, each naming its repository.
func TestAutonomyNamesTheRepositoryOfEachSeriesPoint(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	var merges []dispatcher.Sample
	for i := range 11 {
		repo := "octo/one"
		if i%2 == 1 {
			repo = "octo/two"
		}
		merges = append(merges, dispatcher.Sample{Repo: repo, PR: 50 - i/2, MergedAt: at.Add(-time.Duration(i) * time.Hour),
			Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 10}})
	}
	series := seriesOf(t, getAutonomy(t, &stubMeasured{merges: merges}))
	if len(series) != 2 {
		t.Fatalf("series = %v, want a point for each of the two merges with ten measured up to it", series)
	}
	first, _ := series[0].(map[string]any)
	last, _ := series[1].(map[string]any)
	if first["repo"] != "octo/two" || first["pr"] != 50.0 || last["repo"] != "octo/one" || last["pr"] != 50.0 {
		t.Errorf("series = %v, want PR 50 of octo/two then PR 50 of octo/one", series)
	}
}

// TestAutonomyKeepsTheKeysThePageAlreadyReads asserts every key the page read
// before the new figures is still served, a string where it was one: the new
// figures are siblings, so a page built on the old ones keeps working.
func TestAutonomyKeepsTheKeysThePageAlreadyReads(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	var merges []dispatcher.Sample
	for i := range dispatcher.TrailingPRs {
		merges = append(merges, dispatcher.Sample{Run: "run", PR: 100 - i, Repo: "octo/scratch", Size: "M", Title: "t", PRURL: "u",
			MergedAt: at.Add(-time.Duration(i) * time.Hour), Rewrite: dispatcher.Rewrite{AgentLines: 100, OwnerLines: 20}})
	}
	rewrite := rewriteFigures(t, merges)

	subset := func(what string, old []string, v any) map[string]any {
		t.Helper()
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s = %v, want an object", what, v)
		}
		for _, key := range old {
			if _, ok := m[key]; !ok {
				t.Errorf("%s lost the key %q: %v", what, key, sortedKeys(m))
			}
		}
		return m
	}
	subset("rewrite", []string{"by_repo", "by_size", "prs", "series", "tiles"}, rewrite)
	first := func(what string) any {
		list, _ := rewrite[what].([]any)
		if len(list) == 0 {
			t.Fatalf("%s = %v, want entries", what, rewrite[what])
		}
		return list[0]
	}
	tile := subset("tile", []string{"agent_lines", "asked", "owner_lines", "prs", "rate"}, first("tiles"))
	point := subset("point", []string{"merged_at", "pr", "rate"}, first("series"))
	pr := subset("pr", []string{"agent_lines", "merged_at", "owner_lines", "pr", "pr_url", "rate", "repo", "run", "size", "title"}, first("prs"))
	repo := subset("repo", []string{"repo", "series", "tiles"}, first("by_repo"))
	sizes, _ := rewrite["by_size"].([]any)
	size := subset("size", []string{"median", "p90", "prs", "rate", "size"}, sizes[1])
	for what, v := range map[string]any{"tile rate": tile["rate"], "point rate": point["rate"], "PR rate": pr["rate"],
		"size rate": size["rate"], "size median": size["median"], "size p90": size["p90"], "repo": repo["repo"]} {
		if _, ok := v.(string); !ok {
			t.Errorf("%s = %v, want the string the page reads", what, v)
		}
	}
}

func TestAutonomyServesEachPRsPhaseModels(t *testing.T) {
	rewrite := rewriteFigures(t, []dispatcher.Sample{
		{PR: 3, PlanModel: "p/plan", BuildModel: "p/build", ReviewModel: "p/review", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
		// A phase that ran no one model reads as unknown: no plan stage on an S
		// ticket, and a build that moved to a fallback part way.
		{PR: 2, BuildModel: "", ReviewModel: "p/review", Rewrite: dispatcher.Rewrite{AgentLines: 1}},
		{PR: 1, Rewrite: dispatcher.Rewrite{AgentLines: 1}},
	})
	prs, _ := rewrite["prs"].([]any)
	for i, want := range []struct{ plan, build, review string }{
		{"p/plan", "p/build", "p/review"},
		{"unknown", "unknown", "p/review"},
		{"unknown", "unknown", "unknown"},
	} {
		pr, _ := prs[i].(map[string]any)
		if pr["plan_model"] != want.plan || pr["build_model"] != want.build || pr["review_model"] != want.review {
			t.Errorf("prs[%d] = %v, want plan %s, build %s and review %s", i, pr, want.plan, want.build, want.review)
		}
	}
}

// A window of merges with no lines at all has no rate: the series says so as the
// tiles do, rather than plotting a zero.
func TestAutonomyPrintsNoAgentLinesForASeriesWindowWithNoLines(t *testing.T) {
	merges := make([]dispatcher.Sample, 0, dispatcher.TrailingPRs)
	for i := range dispatcher.TrailingPRs {
		merges = append(merges, dispatcher.Sample{PR: 50 - i})
	}
	series := seriesOf(t, getAutonomy(t, &stubMeasured{merges: merges}))
	point, _ := series[0].(map[string]any)
	if len(series) != 1 || point["rate"] != "no agent lines" || point["rate_value"] != nil {
		t.Errorf("series = %v, want one point reading no agent lines with a null number", series)
	}
}
