package dispatcher

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// The window the rewrite rate is measured over, and the rate that binds.
const (
	// TrailingPRs is the window the trailing rate covers.
	TrailingPRs = 10
	// AutoMergeRate is the trailing rate auto-merge is on at or under.
	AutoMergeRate = 0.10
)

// Rewrite is one merged agent PR's measured lines: what the agent wrote, and
// what the owner changed after the agent's PR — the rate's numerator and its
// denominator.
type Rewrite struct {
	// AgentLines is every line a commit authored as the runner added or removed.
	AgentLines int64
	// OwnerLines is every line a commit by anyone else added or removed.
	OwnerLines int64
	// ReviewFoundLines is the part of OwnerLines a Rework: review-found trailer
	// marks, fixes for what review found.
	ReviewFoundLines int64
	// OwnerChangeLines is the part of OwnerLines a Rework: owner-change trailer
	// marks.
	OwnerChangeLines int64
	// UnlabelledLines is the rest of OwnerLines: commits with neither trailer.
	UnlabelledLines int64
	// UnmeasuredCommits counts commits whose line counts GitHub did not give, so
	// the lines above undercount the change.
	UnmeasuredCommits int64
}

// The values a commit's Rework trailer takes.
const (
	// ReworkReviewFound marks a commit that fixes what review found.
	ReworkReviewFound = "review-found"
	// ReworkOwnerChange marks a commit that is the owner's own change.
	ReworkOwnerChange = "owner-change"
)

// reworkTrailer matches a Rework trailer line in a commit message.
var reworkTrailer = regexp.MustCompile(`(?mi)^Rework:[ \t]*(review-found|owner-change)[ \t]*\r?$`)

// reworkOf is the Rework trailer value in message, or "" when it carries none.
func reworkOf(message string) string {
	m := reworkTrailer.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// Rate is the rewrite rate: the owner's lines over the agent's. No lines at all
// is 0, unmeasured; owner lines over none of the agent's is +Inf, so a misread
// author never reads as a clean rate.
func (r Rewrite) Rate() float64 {
	if r.AgentLines <= 0 {
		if r.OwnerLines > 0 {
			return math.Inf(1)
		}
		return 0
	}
	return float64(r.OwnerLines) / float64(r.AgentLines)
}

// Commit is one commit of a merged PR: who authored it, and how many lines it
// changed.
type Commit struct {
	// Name and Email are the free text git recorded, which anyone can set.
	Name  string
	Email string
	// Login is the account GitHub resolved the author to, empty when it resolved
	// none; it is the only identity a commit is attributed by.
	Login string
	// Message is the full commit message, whose trailers mark what a rewrite is.
	Message string
	// Unmeasured is set when GitHub gave no line counts for the commit.
	Unmeasured bool
	Additions  int64
	Deletions  int64
}

// Lines is what the commit changed either way.
func (c Commit) Lines() int64 { return c.Additions + c.Deletions }

// RewriteOf sums commits into the lines the agent wrote and the lines the owner
// changed: a commit is the agent's only when GitHub resolved its author to the
// runner's own login, since a name and an email are free text anyone can set.
func RewriteOf(commits []Commit) Rewrite {
	var r Rewrite
	for _, c := range commits {
		if c.Unmeasured {
			r.UnmeasuredCommits++
		}
		if c.Login != "" && strings.EqualFold(c.Login, runner.CommitAuthorLogin) {
			r.AgentLines += c.Lines()
			continue
		}
		r.OwnerLines += c.Lines()
		switch reworkOf(c.Message) {
		case ReworkReviewFound:
			r.ReviewFoundLines += c.Lines()
		case ReworkOwnerChange:
			r.OwnerChangeLines += c.Lines()
		default:
			r.UnlabelledLines += c.Lines()
		}
	}
	return r
}

// Measured is one merged agent PR's measurement as it is written onto its run
// row: the lines it measured, the PR's own title and number, and when the PR
// merged.
type Measured struct {
	Rewrite
	MergedAt time.Time
	PR       int
	Title    string
}

// RunRow is the run row a merged PR's head branch belongs to.
type RunRow struct {
	// RunID is the row's id.
	RunID string
	// Measured is the merge the row already carries a measurement for, the zero
	// time when none: a merge is measured once.
	Measured time.Time
}

// Sample is one merged agent PR as the auto-merge rule reads it.
type Sample struct {
	// Run is the id of the run row the PR was measured on.
	Run string
	// Size is the T-shirt size the ticket was built at, empty for a row that
	// names none.
	Size string
	// Repo is the repository the PR merged into.
	Repo string
	// PRURL is the PR's own page, empty for a row that recorded none.
	PRURL string
	// MergedAt is when the PR merged; the windows order by it.
	MergedAt time.Time
	// PR is the PR's number in Repo, 0 for a row measured before it was kept.
	PR int
	// Title is the PR's own title as its row holds it, empty for a row measured
	// before it was kept.
	Title string
	// PlanModel, BuildModel and ReviewModel are the phase's one model, empty for none or several.
	PlanModel, BuildModel, ReviewModel string
	Rewrite
	// AutoMerged is whether the run's PR merged itself, recorded on the run row
	// when the claim was made.
	AutoMerged bool
	// Reverted is whether a merged revert names the PR.
	Reverted bool
}

// Evidence is what the auto-merge rule is decided from.
type Evidence struct {
	// Trailing is the merged PRs' samples, newest merge first, one read serving
	// every window.
	Trailing []Sample
}

// Window is the samples the trailing n merged PRs cover, newest first: a window
// caps what is measured and never claims n merged.
func (e Evidence) Window(n int) []Sample {
	if n < 0 || n > len(e.Trailing) {
		n = len(e.Trailing)
	}
	return e.Trailing[:n]
}

// Lines is the measured lines of samples summed.
func Lines(samples []Sample) Rewrite {
	var lines Rewrite
	for _, s := range samples {
		lines.AgentLines += s.AgentLines
		lines.OwnerLines += s.OwnerLines
		lines.ReviewFoundLines += s.ReviewFoundLines
		lines.OwnerChangeLines += s.OwnerChangeLines
		lines.UnlabelledLines += s.UnlabelledLines
		lines.UnmeasuredCommits += s.UnmeasuredCommits
	}
	return lines
}

// Reverts is how many of samples were reverted after they auto-merged: a revert
// of a PR the owner merged by hand is the owner's own doing, so it counts none.
func Reverts(samples []Sample) int {
	n := 0
	for _, s := range samples {
		if s.AutoMerged && s.Reverted {
			n++
		}
	}
	return n
}

// The windows the Autonomy page draws as tiles and the length of its series.
const (
	// ShortPRs is the shorter window drawn beside the trailing one, showing how
	// much the last few merges moved the rate.
	ShortPRs = 5
	// SeriesPoints is how many points the rolling series carries, the newest
	// ones while the measured history is longer.
	SeriesPoints = 25
)

// Trend is which way a window's rate is heading; lower is better.
type Trend string

// The directions a window's rate takes.
const (
	TrendUp   Trend = "up"
	TrendDown Trend = "down"
	TrendFlat Trend = "flat"
)

// trendMinMove is the smallest latest move, in tenths of a point, that shows as an arrow.
const trendMinMove = 5

// RateWindow is one trailing window of merged PRs as a view draws it: the PRs
// it covers, the number of them it was asked for, and the rate pooled over their
// lines.
type RateWindow struct {
	// PRs are the numbers of the PRs the window covers, newest first.
	PRs []int
	// Asked is how many PRs the window asked for, which PRs may fall short of:
	// a short window holds a rate over fewer merges than it was read over.
	Asked int
	// Lines are the pooled measured lines of the PRs it covers.
	Lines Rewrite
	// Rate is Lines' rate: the owner's lines over the agent's, pooled rather
	// than a mean of the PRs' own rates.
	Rate float64
	// Previous is the rate of the same window ending one PR earlier; HasPrevious
	// needs the trend to be computable: three full windows, each with agent
	// lines. Without it there is no trend yet, which TrendFlat does not say.
	Previous    float64
	HasPrevious bool
	// Trend is TrendFlat unless three full windows with agent lines show an arrow.
	Trend Trend
}

// WindowRate is the trailing n merged PRs of samples, newest first. A window of
// no PRs holds none, so a caller asking for none measures nothing.
func WindowRate(samples []Sample, n int) RateWindow {
	if n < 0 {
		n = 0
	}
	covered := samples
	if n < len(covered) {
		covered = covered[:n]
	}
	w := RateWindow{Asked: n, Lines: Lines(covered)}
	w.Rate = w.Lines.Rate()
	for _, s := range covered {
		w.PRs = append(w.PRs, s.PR)
	}
	return w
}

// Tiles is the trailing, last-few and last-merge windows, widest first, each with its trend.
func Tiles(samples []Sample) []RateWindow {
	return []RateWindow{
		trendedWindow(samples, TrailingPRs),
		trendedWindow(samples, ShortPRs),
		trendedWindow(samples, 1),
	}
}

// trendedWindow reads the window beside the same window one and two PRs earlier.
// A shorter window would compare different quantities, as the series has no
// point for one, so a trend needs all three full.
func trendedWindow(samples []Sample, n int) RateWindow {
	w := WindowRate(samples, n)
	w.Trend = TrendFlat
	if w.Lines.AgentLines <= 0 || len(samples) < n+2 {
		return w
	}
	prev := WindowRate(samples[1:], n)
	before := WindowRate(samples[2:], n)
	if prev.Lines.AgentLines <= 0 || before.Lines.AgentLines <= 0 {
		return w
	}
	w.Previous, w.HasPrevious = prev.Rate, true
	w.Trend = trendOf(w.Rate, prev.Rate, before.Rate)
	return w
}

// trendOf is an arrow only when the last two moves go the same way and the
// latest is at least 0.5 points, on the rates as printed.
func trendOf(rate, previous, earlier float64) Trend {
	latest := printedTenths(rate) - printedTenths(previous)
	before := printedTenths(previous) - printedTenths(earlier)
	switch {
	case latest >= trendMinMove && before > 0:
		return TrendUp
	case latest <= -trendMinMove && before < 0:
		return TrendDown
	}
	return TrendFlat
}

// PercentText is rate as a percentage to one decimal: the one rounding the view
// prints and the trend compares, so an arrow never contradicts the text.
func PercentText(rate float64) string {
	return strconv.FormatFloat(rate*100, 'f', 1, 64)
}

// printedTenths is rate in tenths of a point as PercentText prints it.
func printedTenths(rate float64) int {
	n, _ := strconv.Atoi(strings.Replace(PercentText(rate), ".", "", 1))
	return n
}

// SeriesPoint is one point of the rolling rate: the merge the window ends at and
// the rate pooled over that window.
type SeriesPoint struct {
	// Repo, PR and MergedAt are the merge the window ends at, the newest PR in it;
	// a PR's number is only its own within Repo.
	Repo     string
	PR       int
	MergedAt time.Time
	// AgentLines is the window's agent lines: with none the rate has no figure.
	AgentLines int64
	Rate       float64
}

// RollingSeries is the rolling trailing-n rate over the merged PRs in samples:
// one point per PR that has n measured PRs up to and including it, each the rate
// pooled over those n, oldest first and at most points of them — the newest,
// since a view draws the latest stretch of a long history.
func RollingSeries(samples []Sample, n, points int) []SeriesPoint {
	if n <= 0 || len(samples) < n {
		return nil
	}
	// samples are newest first, so the oldest PR with n measured PRs up to and
	// including it is the nth from the end, and each earlier index is the same
	// window one merge later.
	var out []SeriesPoint
	for i := len(samples) - n; i >= 0; i-- {
		lines := Lines(samples[i : i+n])
		out = append(out, SeriesPoint{
			Repo:       samples[i].Repo,
			PR:         samples[i].PR,
			MergedAt:   samples[i].MergedAt,
			AgentLines: lines.AgentLines,
			Rate:       lines.Rate(),
		})
	}
	if points < 0 {
		points = 0
	}
	if len(out) > points {
		out = out[len(out)-points:]
	}
	return out
}

// The T-shirt sizes the queue admits, and the bucket for a measured PR naming
// none of them.
const (
	SizeSmall   = "S"
	SizeMedium  = "M"
	SizeLarge   = "L"
	SizeUnsized = "unsized"
)

// MinPRsForSpread is how many measured PRs a group needs before its median and
// p90 are reported: a spread over fewer says more about which PRs merged than about the group.
const MinPRsForSpread = 10

// Group is the rewrite figures of a set of measured PRs: a size, a model or a repository.
type Group struct {
	PRs int
	// Lines are the pooled lines and Rate their pooled rate.
	Lines Rewrite
	Rate  float64
	// Spread is whether Median and P90 exist: MinPRsForSpread PRs, one with agent lines.
	Spread bool
	Median float64
	P90    float64
	// NoAgentLinesPRs counts the PRs with no agent lines, which are in PRs but
	// not in the ranking, and NoAgentOwnerLines their owner lines.
	NoAgentLinesPRs   int
	NoAgentOwnerLines int64
}

// GroupOf is the figures of samples as one group; no samples is the zero group.
func GroupOf(samples []Sample) Group {
	g := Group{PRs: len(samples), Lines: Lines(samples)}
	g.Rate = g.Lines.Rate()
	for _, s := range samples {
		if s.AgentLines <= 0 {
			g.NoAgentLinesPRs++
			g.NoAgentOwnerLines += s.OwnerLines
		}
	}
	if len(samples) >= MinPRsForSpread {
		if rates := measuredRates(samples); len(rates) > 0 {
			g.Spread = true
			g.Median = medianOf(rates)
			g.P90 = nearestRank(rates, 0.9)
		}
	}
	return g
}

// SizeRow is one size's rewrite figures.
type SizeRow struct {
	// Size is the T-shirt size, or SizeUnsized for the PRs naming none.
	Size string
	Group
}

// SizeBucket is the size row a PR built at size belongs to: the ladder's own
// size, or SizeUnsized for any other text.
func SizeBucket(size string) string {
	switch size {
	case SizeSmall, SizeMedium, SizeLarge:
		return size
	}
	return SizeUnsized
}

// BySize splits samples by the size they were built at, always the S, M, L and
// unsized rows in that order, so a view draws the same table whatever merged. A
// size the ladder does not name is counted with the unsized ones.
func BySize(samples []Sample) []SizeRow {
	sizes := []string{SizeSmall, SizeMedium, SizeLarge, SizeUnsized}
	grouped := map[string][]Sample{}
	for _, s := range samples {
		size := SizeBucket(s.Size)
		grouped[size] = append(grouped[size], s)
	}
	rows := make([]SizeRow, 0, len(sizes))
	for _, size := range sizes {
		rows = append(rows, SizeRow{Size: size, Group: GroupOf(grouped[size])})
	}
	return rows
}

// measuredRates is the per-PR rates of samples sorted ascending, with the PRs
// the agent wrote no lines in left out: that rate is +Inf and has no place in a
// median or a percentile, though the PR still counts.
func measuredRates(samples []Sample) []float64 {
	var rates []float64
	for _, s := range samples {
		if s.AgentLines <= 0 {
			continue
		}
		rates = append(rates, s.Rate())
	}
	sort.Float64s(rates)
	return rates
}

// medianOf is the middle of the sorted rates: the one middle value of an odd
// count, the mean of the two middle ones of an even count.
func medianOf(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// nearestRank is the sorted rates' nearest-rank percentile: the value at rank
// ceil(p*n), so a rate is picked rather than interpolated between two.
func nearestRank(sorted []float64, p float64) float64 {
	return sorted[int(math.Ceil(p*float64(len(sorted))))-1]
}

// RepoUnattributed is the entry the measured PRs naming no repository are
// reported under.
const RepoUnattributed = "unattributed"

// ModelUnknown is the group of PRs whose phase has no one model.
const ModelUnknown = "unknown"

// ModelRow is one model's figures over the PRs it ran a phase of.
type ModelRow struct {
	Model string
	Group
}

// PhaseModels is the rows of each phase's models.
type PhaseModels struct {
	Plan, Build, Review []ModelRow
}

// ByModel groups samples by each phase's model, so a PR is in three groups.
func ByModel(samples []Sample) PhaseModels {
	return PhaseModels{
		Plan:   groupByModel(samples, func(s Sample) string { return s.PlanModel }),
		Build:  groupByModel(samples, func(s Sample) string { return s.BuildModel }),
		Review: groupByModel(samples, func(s Sample) string { return s.ReviewModel }),
	}
}

// groupByModel is one phase's rows, most PRs first then by name, ModelUnknown last.
func groupByModel(samples []Sample, pick func(Sample) string) []ModelRow {
	grouped := map[string][]Sample{}
	for _, s := range samples {
		model := pick(s)
		if model == "" {
			model = ModelUnknown
		}
		grouped[model] = append(grouped[model], s)
	}
	rows := make([]ModelRow, 0, len(grouped))
	for model, group := range grouped {
		rows = append(rows, ModelRow{Model: model, Group: GroupOf(group)})
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Model == ModelUnknown) != (b.Model == ModelUnknown) {
			return b.Model == ModelUnknown
		}
		if a.PRs != b.PRs {
			return a.PRs > b.PRs
		}
		return a.Model < b.Model
	})
	return rows
}

// RepoRow is one repository's rewrite figures: the same tiles and series the
// whole set carries, over its own measured PRs.
type RepoRow struct {
	// Repo is the repository, or RepoUnattributed for the PRs naming none.
	Repo string
	// Tiles are the repository's own windows, and Series its own rolling rate.
	Tiles  []RateWindow
	Series []SeriesPoint
	// Average is over all its measured PRs, not its trailing window.
	Average Group
}

// ByRepo splits samples by the repository they merged into: the repositories
// sorted, and the PRs naming none in a RepoUnattributed entry after them.
func ByRepo(samples []Sample) []RepoRow {
	grouped := map[string][]Sample{}
	for _, s := range samples {
		grouped[s.Repo] = append(grouped[s.Repo], s)
	}
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(grouped[""]) > 0 {
		names = append(names, "")
	}
	rows := make([]RepoRow, 0, len(names))
	for _, name := range names {
		row := RepoRow{
			Repo:    name,
			Tiles:   Tiles(grouped[name]),
			Series:  RollingSeries(grouped[name], TrailingPRs, SeriesPoints),
			Average: GroupOf(grouped[name]),
		}
		if name == "" {
			row.Repo = RepoUnattributed
		}
		rows = append(rows, row)
	}
	return rows
}
