package main

import (
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// rewriteView is the measured merges' rewrite figures as a page draws them: the
// pooled rate over each trailing window, the rolling rate behind them, the PRs
// they are measured on, and the same figures split by size, by the model of each
// phase and by repository. Every headline rate is the pooled owner lines over the
// agent lines, never a mean of the PRs' own rates; a group's median and p90 are
// the spread of those own rates. Each rate printed as text has a number beside it.
type rewriteView struct {
	Tiles   []rewriteTile  `json:"tiles"`
	Series  []rewritePoint `json:"series"`
	PRs     []rewritePR    `json:"prs"`
	BySize  []sizeRate     `json:"by_size"`
	ByModel modelsView     `json:"by_model"`
	ByRepo  []repoRewrite  `json:"by_repo"`
	Bars    bars           `json:"bars"`
}

// bars are the lines a chart of the rate draws.
type bars struct {
	// Rollback is the bar the trailing rate is drawn against.
	Rollback float64 `json:"rollback"`
}

// rewriteTile is one trailing window: the PRs it covers, the count it asked for
// — which a shorter history may not reach — and the rate pooled over them, with
// the rate one PR earlier and the trend.
type rewriteTile struct {
	PRs               []int    `json:"prs"`
	Asked             int      `json:"asked"`
	AgentLines        int64    `json:"agent_lines"`
	OwnerLines        int64    `json:"owner_lines"`
	Rate              string   `json:"rate"`
	RateValue         *float64 `json:"rate_value"`
	PreviousRateValue *float64 `json:"previous_rate_value"`
	Trend             string   `json:"trend"`
}

// rewritePoint is one point of the rolling series: the merge the window ends at
// and the rate pooled over that window.
type rewritePoint struct {
	Repo      string    `json:"repo"`
	PR        int       `json:"pr"`
	MergedAt  time.Time `json:"merged_at"`
	Rate      string    `json:"rate"`
	RateValue *float64  `json:"rate_value"`
}

// rewritePR is one merged runner PR as the list shows it, with its own rate
// beside the lines the rate is read from, and the rate its trailing window ends at.
type rewritePR struct {
	PR         int       `json:"pr"`
	Run        string    `json:"run"`
	Repo       string    `json:"repo"`
	Size       string    `json:"size"`
	Title      string    `json:"title"`
	AgentLines int64     `json:"agent_lines"`
	OwnerLines int64     `json:"owner_lines"`
	Rate       string    `json:"rate"`
	RateValue  *float64  `json:"rate_value"`
	MergedAt   time.Time `json:"merged_at"`
	PRURL      string    `json:"pr_url"`
	// PlanModel, BuildModel and ReviewModel are the phase's one model, or "unknown".
	PlanModel   string `json:"plan_model"`
	BuildModel  string `json:"build_model"`
	ReviewModel string `json:"review_model"`
	// Trailing10 is the rate pooled over this PR and the nine merged before it,
	// null for a PR with fewer than a window's PRs behind it.
	Trailing10      *string  `json:"trailing_10"`
	Trailing10Value *float64 `json:"trailing_10_value"`
}

// groupFigures is one group of PRs, the same object for a size, a model and a
// repository's average. The rate is null for no PRs, and the median and p90 for
// a group with no spread, since a rate of zero is a real measurement.
type groupFigures struct {
	PRs               int      `json:"prs"`
	Rate              *string  `json:"rate"`
	RateValue         *float64 `json:"rate_value"`
	Median            *string  `json:"median"`
	MedianValue       *float64 `json:"median_value"`
	P90               *string  `json:"p90"`
	P90Value          *float64 `json:"p90_value"`
	AgentLines        int64    `json:"agent_lines"`
	OwnerLines        int64    `json:"owner_lines"`
	ReviewFoundLines  int64    `json:"review_found_lines"`
	OwnerChangeLines  int64    `json:"owner_change_lines"`
	UnlabelledLines   int64    `json:"unlabelled_lines"`
	NoAgentLinesPRs   int      `json:"no_agent_lines_prs"`
	NoAgentOwnerLines int64    `json:"no_agent_owner_lines"`
}

// sizeRate is one size's figures.
type sizeRate struct {
	Size string `json:"size"`
	groupFigures
}

// modelRate is one model's figures.
type modelRate struct {
	Model string `json:"model"`
	groupFigures
}

// modelsView is each phase's models.
type modelsView struct {
	Plan   []modelRate `json:"plan"`
	Build  []modelRate `json:"build"`
	Review []modelRate `json:"review"`
}

// repoRewrite is one repository's own windows and rolling rate, and its average over all its PRs.
type repoRewrite struct {
	Repo    string         `json:"repo"`
	Tiles   []rewriteTile  `json:"tiles"`
	Series  []rewritePoint `json:"series"`
	Average groupFigures   `json:"average"`
}

// renderRewrite renders the rewrite figures of the measured merges, newest
// first. Every list is rendered whether or not it holds anything, so a view
// narrowing to one of them reads an empty list rather than a null.
func renderRewrite(samples []dispatcher.Sample) rewriteView {
	return rewriteView{
		Tiles:   renderTiles(dispatcher.Tiles(samples)),
		Series:  renderSeries(dispatcher.RollingSeries(samples, dispatcher.TrailingPRs, dispatcher.SeriesPoints)),
		PRs:     renderPRs(samples),
		BySize:  renderSizes(dispatcher.BySize(samples)),
		ByModel: renderModels(dispatcher.ByModel(samples)),
		ByRepo:  renderRepos(dispatcher.ByRepo(samples)),
		Bars:    bars{Rollback: dispatcher.AutoMergeRate},
	}
}

// renderTiles renders each trailing window.
func renderTiles(windows []dispatcher.RateWindow) []rewriteTile {
	out := make([]rewriteTile, 0, len(windows))
	for _, w := range windows {
		prs := w.PRs
		if prs == nil {
			prs = []int{}
		}
		tile := rewriteTile{
			PRs:        prs,
			Asked:      w.Asked,
			AgentLines: w.Lines.AgentLines,
			OwnerLines: w.Lines.OwnerLines,
			Rate:       linesRateText(w.Lines.AgentLines, w.Rate),
			RateValue:  linesRateNumber(w.Lines.AgentLines, w.Rate),
			Trend:      string(w.Trend),
		}
		if w.HasPrevious {
			tile.PreviousRateValue = &w.Previous
		}
		out = append(out, tile)
	}
	return out
}

// renderSeries renders the rolling rate, oldest merge first.
func renderSeries(points []dispatcher.SeriesPoint) []rewritePoint {
	out := make([]rewritePoint, 0, len(points))
	for _, p := range points {
		out = append(out, rewritePoint{Repo: p.Repo, PR: p.PR, MergedAt: p.MergedAt, Rate: linesRateText(p.AgentLines, p.Rate), RateValue: linesRateNumber(p.AgentLines, p.Rate)})
	}
	return out
}

// renderPRs renders every merged PR the read holds, newest first, each with the
// rate pooled over its trailing window: the same window the rolling series ends
// at, and null for a PR with fewer than the window's PRs behind it.
func renderPRs(samples []dispatcher.Sample) []rewritePR {
	out := make([]rewritePR, 0, len(samples))
	for i, s := range samples {
		pr := rewritePR{
			PR:          s.PR,
			Run:         s.Run,
			Repo:        s.Repo,
			Size:        dispatcher.SizeBucket(s.Size),
			Title:       s.Title,
			AgentLines:  s.AgentLines,
			OwnerLines:  s.OwnerLines,
			Rate:        linesRateText(s.AgentLines, s.Rate()),
			RateValue:   linesRateNumber(s.AgentLines, s.Rate()),
			MergedAt:    s.MergedAt,
			PRURL:       s.PRURL,
			PlanModel:   modelOrUnknown(s.PlanModel),
			BuildModel:  modelOrUnknown(s.BuildModel),
			ReviewModel: modelOrUnknown(s.ReviewModel),
		}
		if i+dispatcher.TrailingPRs <= len(samples) {
			window := dispatcher.Lines(samples[i : i+dispatcher.TrailingPRs])
			text := linesRateText(window.AgentLines, window.Rate())
			pr.Trailing10 = &text
			pr.Trailing10Value = linesRateNumber(window.AgentLines, window.Rate())
		}
		out = append(out, pr)
	}
	return out
}

// renderGroup renders one group's figures, withholding a spread the group has
// none of, so a withheld figure reads as absent rather than as a clean zero.
func renderGroup(g dispatcher.Group) groupFigures {
	out := groupFigures{
		PRs:               g.PRs,
		RateValue:         linesRateNumber(g.Lines.AgentLines, g.Rate),
		AgentLines:        g.Lines.AgentLines,
		OwnerLines:        g.Lines.OwnerLines,
		ReviewFoundLines:  g.Lines.ReviewFoundLines,
		OwnerChangeLines:  g.Lines.OwnerChangeLines,
		UnlabelledLines:   g.Lines.UnlabelledLines,
		NoAgentLinesPRs:   g.NoAgentLinesPRs,
		NoAgentOwnerLines: g.NoAgentOwnerLines,
	}
	if g.PRs > 0 {
		rate := linesRateText(g.Lines.AgentLines, g.Rate)
		out.Rate = &rate
	}
	if g.Spread {
		out.Median, out.MedianValue = ratePtr(g.Median), rateNumber(g.Median)
		out.P90, out.P90Value = ratePtr(g.P90), rateNumber(g.P90)
	}
	return out
}

// modelOrUnknown is a phase's model as the view names it: the model, or the
// unknown group's name for a phase that ran none or more than one.
func modelOrUnknown(model string) string {
	if model == "" {
		return dispatcher.ModelUnknown
	}
	return model
}

// renderSizes renders each size's figures.
func renderSizes(rows []dispatcher.SizeRow) []sizeRate {
	out := make([]sizeRate, 0, len(rows))
	for _, r := range rows {
		out = append(out, sizeRate{Size: r.Size, groupFigures: renderGroup(r.Group)})
	}
	return out
}

// renderModels renders each phase's models, every phase a list even when empty.
func renderModels(models dispatcher.PhaseModels) modelsView {
	return modelsView{
		Plan:   renderModelRows(models.Plan),
		Build:  renderModelRows(models.Build),
		Review: renderModelRows(models.Review),
	}
}

// renderModelRows renders one phase's models, most PRs first.
func renderModelRows(rows []dispatcher.ModelRow) []modelRate {
	out := make([]modelRate, 0, len(rows))
	for _, r := range rows {
		out = append(out, modelRate{Model: r.Model, groupFigures: renderGroup(r.Group)})
	}
	return out
}

// renderRepos renders each repository's own windows and rolling rate, and its
// average over all of its measured PRs.
func renderRepos(rows []dispatcher.RepoRow) []repoRewrite {
	out := make([]repoRewrite, 0, len(rows))
	for _, r := range rows {
		out = append(out, repoRewrite{
			Repo: r.Repo, Tiles: renderTiles(r.Tiles), Series: renderSeries(r.Series), Average: renderGroup(r.Average),
		})
	}
	return out
}

// ratePtr returns the rate as the view prints it, for a figure that carries one.
func ratePtr(rate float64) *string {
	text := rateText(rate)
	return &text
}
