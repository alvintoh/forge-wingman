package main

import (
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

// rewriteView is the measured merges' rewrite figures as a page draws them: the
// pooled rate over each trailing window, the rolling rate behind them, the PRs
// they are measured on, and the same figures split by size and by repository.
// Every headline rate is the pooled owner lines over the agent lines, never a
// mean of the PRs' own rates; a size's median and p90 are the spread of those
// own rates.
type rewriteView struct {
	Tiles  []rewriteTile  `json:"tiles"`
	Series []rewritePoint `json:"series"`
	PRs    []rewritePR    `json:"prs"`
	BySize []sizeRate     `json:"by_size"`
	ByRepo []repoRewrite  `json:"by_repo"`
}

// rewriteTile is one trailing window: the PRs it covers, the count it asked for
// — which a shorter history may not reach — and the rate pooled over them.
type rewriteTile struct {
	PRs        []int  `json:"prs"`
	Asked      int    `json:"asked"`
	AgentLines int64  `json:"agent_lines"`
	OwnerLines int64  `json:"owner_lines"`
	Rate       string `json:"rate"`
}

// rewritePoint is one point of the rolling series: the merge the window ends at
// and the rate pooled over that window.
type rewritePoint struct {
	PR       int       `json:"pr"`
	MergedAt time.Time `json:"merged_at"`
	Rate     string    `json:"rate"`
}

// rewritePR is one merged runner PR as the list shows it, with its own rate
// beside the lines the rate is read from.
type rewritePR struct {
	PR         int       `json:"pr"`
	Run        string    `json:"run"`
	Repo       string    `json:"repo"`
	Size       string    `json:"size"`
	Title      string    `json:"title"`
	AgentLines int64     `json:"agent_lines"`
	OwnerLines int64     `json:"owner_lines"`
	Rate       string    `json:"rate"`
	MergedAt   time.Time `json:"merged_at"`
	PRURL      string    `json:"pr_url"`
}

// sizeRate is one size's figures: its count, the rate pooled over its PRs, and
// the median and p90 of their own rates — null below the PRs a rate needs, and
// for a size whose PRs have no rate of their own, since a rate of zero is a real
// measurement.
type sizeRate struct {
	Size   string  `json:"size"`
	PRs    int     `json:"prs"`
	Rate   *string `json:"rate"`
	Median *string `json:"median"`
	P90    *string `json:"p90"`
}

// repoRewrite is one repository's own windows and rolling rate.
type repoRewrite struct {
	Repo   string         `json:"repo"`
	Tiles  []rewriteTile  `json:"tiles"`
	Series []rewritePoint `json:"series"`
}

// renderRewrite renders the rewrite figures of the measured merges, newest
// first. Every list is rendered whether or not it holds anything, so a view
// narrowing to one of them reads an empty list rather than a null.
func renderRewrite(samples []dispatcher.Sample) rewriteView {
	return rewriteView{
		Tiles:  renderTiles(dispatcher.Tiles(samples)),
		Series: renderSeries(dispatcher.RollingSeries(samples, dispatcher.TrailingPRs, dispatcher.SeriesPoints)),
		PRs:    renderPRs(samples),
		BySize: renderSizes(dispatcher.BySize(samples)),
		ByRepo: renderRepos(dispatcher.ByRepo(samples)),
	}
}

// renderTiles renders each trailing window, its rate as the view prints it.
func renderTiles(windows []dispatcher.RateWindow) []rewriteTile {
	out := make([]rewriteTile, 0, len(windows))
	for _, w := range windows {
		prs := w.PRs
		if prs == nil {
			prs = []int{}
		}
		out = append(out, rewriteTile{
			PRs:        prs,
			Asked:      w.Asked,
			AgentLines: w.Lines.AgentLines,
			OwnerLines: w.Lines.OwnerLines,
			Rate:       linesRateText(w.Lines.AgentLines, w.Rate),
		})
	}
	return out
}

// renderSeries renders the rolling rate, oldest merge first.
func renderSeries(points []dispatcher.SeriesPoint) []rewritePoint {
	out := make([]rewritePoint, 0, len(points))
	for _, p := range points {
		out = append(out, rewritePoint{PR: p.PR, MergedAt: p.MergedAt, Rate: rateText(p.Rate)})
	}
	return out
}

// renderPRs renders the newest merged PRs, up to the trailing window the tiles
// read over.
func renderPRs(samples []dispatcher.Sample) []rewritePR {
	shown := samples
	if len(shown) > dispatcher.TrailingPRs {
		shown = shown[:dispatcher.TrailingPRs]
	}
	out := make([]rewritePR, 0, len(shown))
	for _, s := range shown {
		out = append(out, rewritePR{
			PR:         s.PR,
			Run:        s.Run,
			Repo:       s.Repo,
			Size:       dispatcher.SizeBucket(s.Size),
			Title:      s.Title,
			AgentLines: s.AgentLines,
			OwnerLines: s.OwnerLines,
			Rate:       linesRateText(s.AgentLines, s.Rate()),
			MergedAt:   s.MergedAt,
			PRURL:      s.PRURL,
		})
	}
	return out
}

// renderSizes renders each size's figures, withholding a rate below the PRs it
// needs and a spread the size has none of, so a withheld figure reads as absent
// rather than as a clean zero.
func renderSizes(rows []dispatcher.SizeRow) []sizeRate {
	out := make([]sizeRate, 0, len(rows))
	for _, r := range rows {
		entry := sizeRate{Size: r.Size, PRs: r.PRs}
		if r.Rated {
			entry.Rate = ratePtr(r.Rate)
		}
		if r.Spread {
			entry.Median, entry.P90 = ratePtr(r.Median), ratePtr(r.P90)
		}
		out = append(out, entry)
	}
	return out
}

// renderRepos renders each repository's own windows and rolling rate.
func renderRepos(rows []dispatcher.RepoRow) []repoRewrite {
	out := make([]repoRewrite, 0, len(rows))
	for _, r := range rows {
		out = append(out, repoRewrite{Repo: r.Repo, Tiles: renderTiles(r.Tiles), Series: renderSeries(r.Series)})
	}
	return out
}

// ratePtr returns the rate as the view prints it, for a figure that carries one.
func ratePtr(rate float64) *string {
	text := rateText(rate)
	return &text
}
