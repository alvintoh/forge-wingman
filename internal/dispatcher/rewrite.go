package dispatcher

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// The windows the rewrite rate is measured over and the rates that bind. The
// PRD advances level 4 on a rate of 5% or less over 10 PRs and level 5 on 20
// sampled PRs with zero reverts; a sampled rate over 10% or any trailing-10
// rate over the 20% hard ceiling drops the level again.
const (
	// TrailingPRs is the window the trailing rate covers.
	TrailingPRs = 10
	// SampledPRs is the window the sampled rate and the reverts cover.
	SampledPRs = 20
	// AdvanceRate is the rate level 4 advances at or under.
	AdvanceRate = 0.05
	// RollbackRate is the sampled rate a level may not pass.
	RollbackRate = 0.10
	// CeilingRate is the hard ceiling no trailing rate may pass.
	CeilingRate = 0.20
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
	// the lines above undercount and the PR cannot earn a level.
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

// Numbers is the evidence a level is computed from and a change is recorded
// with: the rate, the PRs it covers and the lines behind it.
type Numbers struct {
	// PRs are the numbers of the PRs measured, newest first.
	PRs  []int
	Rate float64
	// AgentLines and OwnerLines are the rate's denominator and numerator.
	AgentLines int64
	OwnerLines int64
	// The owner lines by their Rework label.
	ReviewFoundLines int64
	OwnerChangeLines int64
	UnlabelledLines  int64
	// Reverts is how many of the PRs were reverted after auto-merging.
	Reverts int
	// Unavailable is set when the evidence could not be read, so the rest is empty.
	Unavailable bool
	// NotRecorded is set when the stored record carried no evidence at all, so the
	// zeros in the rest are absent rather than measured.
	NotRecorded bool
}

// NumbersOf is the evidence samples carry.
func NumbersOf(samples []Sample) Numbers {
	lines := Lines(samples)
	n := Numbers{
		Rate: lines.Rate(), AgentLines: lines.AgentLines, OwnerLines: lines.OwnerLines,
		ReviewFoundLines: lines.ReviewFoundLines, OwnerChangeLines: lines.OwnerChangeLines,
		UnlabelledLines: lines.UnlabelledLines, Reverts: Reverts(samples),
	}
	for _, s := range samples {
		n.PRs = append(n.PRs, s.PR)
	}
	return n
}

// RunRow is the run row a merged PR's head branch belongs to.
type RunRow struct {
	// RunID is the row's id.
	RunID string
	// Measured is the merge the row already carries a measurement for, the zero
	// time when none: a merge is measured once.
	Measured time.Time
}

// Sample is one merged agent PR as the level's conditions read it.
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
	Rewrite
	// AutoMerged is whether the run's PR merged itself, recorded on the run row
	// when the claim was made.
	AutoMerged bool
	// Reverted is whether a merged revert names the PR.
	Reverted bool
	// GateMiss is whether the run's own check job reported a failed gate, so the
	// gate did not hold the PR.
	GateMiss bool
}

// Evidence is what a level's advance conditions are checked against.
type Evidence struct {
	// Level is the level the store holds now.
	Level Autonomy
	// Trailing is the merged PRs' samples, newest merge first, one read serving
	// every window.
	Trailing []Sample
	// CD is whether each repository was recorded as deploying on merge, keyed by
	// its lowercased name.
	CD map[string]bool
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

// Rate is the rewrite rate over samples, and how many PRs it covers.
func Rate(samples []Sample) (rate float64, prs int) {
	return Lines(samples).Rate(), len(samples)
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

// FirstReverted is the first sample in samples a merged revert names, or nil.
func FirstReverted(samples []Sample) *Sample {
	for i, s := range samples {
		if s.AutoMerged && s.Reverted {
			return &samples[i]
		}
	}
	return nil
}

// GateMisses is how many of samples a failed gate the run's own check job
// reported did not hold.
func GateMisses(samples []Sample) int {
	n := 0
	for _, s := range samples {
		if s.GateMiss {
			n++
		}
	}
	return n
}

// Repos is every repository samples name, sorted, so a clause naming them reads
// the same way twice.
func Repos(samples []Sample) []string {
	var repos []string
	seen := map[string]bool{}
	for _, s := range samples {
		if s.Repo == "" || seen[s.Repo] {
			continue
		}
		seen[s.Repo] = true
		repos = append(repos, s.Repo)
	}
	sort.Strings(repos)
	return repos
}

// The windows the autonomy view draws as tiles and the length of its series.
const (
	// ShortPRs is the shorter window drawn beside the trailing one, showing how
	// much the last few merges moved the rate.
	ShortPRs = 5
	// SeriesPoints is how many points the rolling series carries, the newest
	// ones while the measured history is longer.
	SeriesPoints = 25
)

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

// Tiles is the windows a view draws, widest first: the level's own trailing
// window, the last few merges, and the last merge alone.
func Tiles(samples []Sample) []RateWindow {
	return []RateWindow{
		WindowRate(samples, TrailingPRs),
		WindowRate(samples, ShortPRs),
		WindowRate(samples, 1),
	}
}

// SeriesPoint is one point of the rolling rate: the merge the window ends at and
// the rate pooled over that window.
type SeriesPoint struct {
	// PR and MergedAt are the merge the window ends at, the newest PR in it.
	PR       int
	MergedAt time.Time
	Rate     float64
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
		out = append(out, SeriesPoint{
			PR:       samples[i].PR,
			MergedAt: samples[i].MergedAt,
			Rate:     Lines(samples[i : i+n]).Rate(),
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

// MinPRsPerSize is how many measured PRs a size needs before its rates are
// reported: below it a row carries its count alone, since a rate over a handful
// of PRs says more about which ones merged than about the size.
const MinPRsPerSize = 10

// SizeRow is one size's rewrite figures: how many measured PRs it holds and,
// once it holds enough, the rate pooled over them with the spread of their own
// rates.
type SizeRow struct {
	// Size is the T-shirt size, or SizeUnsized for the PRs naming none.
	Size string
	// PRs is how many measured PRs the size holds.
	PRs int
	// Rated is whether the size holds MinPRsPerSize PRs, the count its rates
	// need: a rate of zero is a real measurement, so Rated is what tells a size
	// with no rates from one measured clean.
	Rated bool
	// Spread is whether any of the size's PRs has a rate of its own to rank: a PR
	// the agent wrote no lines in has none, so a size whose PRs are all +Inf
	// carries its pooled rate with no Median or P90 behind it.
	Spread bool
	// Rate is the pooled owner lines over the pooled agent lines.
	Rate float64
	// Median and P90 are the size's per-PR rates, nearest rank, over the PRs the
	// agent wrote lines in.
	Median float64
	P90    float64
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
	rows := []SizeRow{{Size: SizeSmall}, {Size: SizeMedium}, {Size: SizeLarge}, {Size: SizeUnsized}}
	buckets := map[string]int{SizeSmall: 0, SizeMedium: 1, SizeLarge: 2, SizeUnsized: 3}
	grouped := make([][]Sample, len(rows))
	for _, s := range samples {
		at := buckets[SizeBucket(s.Size)]
		grouped[at] = append(grouped[at], s)
	}
	for i, group := range grouped {
		rows[i].PRs = len(group)
		if len(group) < MinPRsPerSize {
			continue
		}
		rows[i].Rated = true
		rows[i].Rate = Lines(group).Rate()
		if rates := measuredRates(group); len(rates) > 0 {
			rows[i].Spread = true
			rows[i].Median = medianOf(rates)
			rows[i].P90 = nearestRank(rates, 0.9)
		}
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

// RepoRow is one repository's rewrite figures: the same tiles and series the
// whole set carries, over its own measured PRs.
type RepoRow struct {
	// Repo is the repository, or RepoUnattributed for the PRs naming none.
	Repo string
	// Tiles are the repository's own windows, and Series its own rolling rate.
	Tiles  []RateWindow
	Series []SeriesPoint
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
			Repo:   name,
			Tiles:  Tiles(grouped[name]),
			Series: RollingSeries(grouped[name], TrailingPRs, SeriesPoints),
		}
		if name == "" {
			row.Repo = RepoUnattributed
		}
		rows = append(rows, row)
	}
	return rows
}
