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
// row: the lines it measured, and when the PR merged.
type Measured struct {
	Rewrite
	MergedAt time.Time
	PR       int
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
	// Repo is the repository the PR merged into.
	Repo string
	// PRURL is the PR's own page, empty for a row that recorded none.
	PRURL string
	// MergedAt is when the PR merged; the windows order by it.
	MergedAt time.Time
	// PR is the PR's number in Repo, 0 for a row measured before it was kept.
	PR int
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
