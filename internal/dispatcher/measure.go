package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// ClosedPR is one closed pull request of a target repository, as the rewrite
// rate is measured from it.
type ClosedPR struct {
	Number int
	Title  string
	Body   string
	// Head is the PR's head branch, the run's own branch for a run's PR.
	Head string
	// URL is the PR's own page, the value the run recorded when it opened it.
	URL string
	// MergedAt is when the PR merged, zero for one that closed unmerged.
	MergedAt time.Time
	// MergedBy is the account GitHub resolved the merge to, empty when it
	// resolved none or the reply carried none: the rate tells an auto-merged PR
	// from the owner's by it.
	MergedBy string
}

// PullRequests reads the pull requests the rewrite rate is measured from.
type PullRequests interface {
	// Closed returns repo's closed pull requests, most recently updated first,
	// which is the window a new merge and a revert are both seen in.
	Closed(ctx context.Context, repo string) ([]ClosedPR, error)
	// Pull returns one pull request of repo.
	Pull(ctx context.Context, repo string, number int) (ClosedPR, error)
	// Commits returns the commits of pull request number in repo, each with who
	// authored it and how many lines it changed.
	Commits(ctx context.Context, repo string, number int) ([]Commit, error)
}

// Rewrites records each merged agent PR's measured lines on its run row and
// reads the samples the rewrite rate is computed from.
type Rewrites interface {
	// RunForMerged returns the run row merged PR pr belongs to, whichever
	// attempt opened it.
	RunForMerged(ctx context.Context, repo string, pr ClosedPR) (RunRow, bool, error)
	// MarkUnfound records that merged PR number of repo names no run row,
	// reporting whether this is the first poll to record it.
	MarkUnfound(ctx context.Context, repo string, number int, at time.Time) (bool, error)
	// Record writes m onto run id's row.
	Record(ctx context.Context, id string, m Measured) error
	// Recent returns the merged PRs' samples, newest merge first, up to limit.
	Recent(ctx context.Context, limit int) ([]Sample, error)
}

// The ways an auto-merged PR escapes the owner's reading.
const (
	// EscapeReverted is an auto-merged PR a merged revert takes back.
	EscapeReverted = "reverted"
	// EscapeFixed is an auto-merged PR a later merged PR names as fixed.
	EscapeFixed = "fixed"
)

// EscapeWindow is how long after an auto-merge a merge that takes it back, or
// names it as fixed, still counts as the escape.
const EscapeWindow = 7 * 24 * time.Hour

// MergeEscape is an auto-merged runner PR a later merge reverted or named as
// fixed: a defect nobody read, which holds auto-merge off until the owner
// switches it on again.
type MergeEscape struct {
	Repo string
	// PR is the auto-merged PR's number in Repo.
	PR int
	// By is the number of the PR that reverted it or named it as fixed.
	By     int
	Reason string
	At     time.Time
}

// Words names the escaped PR and how it escaped, as the run's wait reason and
// its PR body word it.
func (e MergeEscape) Words() string {
	how := "escaped the owner's read"
	switch e.Reason {
	case EscapeReverted:
		how = "was reverted"
	case EscapeFixed:
		how = fmt.Sprintf("was named as fixed by #%d", e.By)
	}
	return fmt.Sprintf("auto-merge is off until you switch it on again: %s#%d %s within %d days of merging",
		e.Repo, e.PR, how, int(EscapeWindow/(24*time.Hour)))
}

// EscapeKey names an escaped PR the way the owner's acknowledgement of it is kept.
func EscapeKey(repo string, pr int) string { return fmt.Sprintf("%s#%d", repo, pr) }

// EscapeState is what the owner's document holds about escapes.
type EscapeState struct {
	// Latched is whether an escape holds auto-merge off now.
	Latched bool
	// Cleared are the escaped PRs the owner acknowledged by switching auto-merge
	// on again, as EscapeKey names them.
	Cleared []string
}

// Settled reports whether recording an escape of repo#pr can change nothing: one
// is latched already, or the owner has acknowledged this PR's.
func (s EscapeState) Settled(repo string, pr int) bool {
	return s.Latched || slices.Contains(s.Cleared, EscapeKey(repo, pr))
}

// EscapeRecorder records the escaped PR beside the auto-merge switch, and
// reports whether this is the first record of it: the first escape holds until
// the owner clears it, and a PR the owner cleared does not latch again.
type EscapeRecorder interface {
	RecordEscape(ctx context.Context, e MergeEscape) (bool, error)
	// State reads what is latched and cleared, so a poll skips the lookups an
	// escape it would not record needs.
	State(ctx context.Context) (EscapeState, error)
}

// MergedItself reports whether login is a bot account, which is how a merge the
// runner's own token made is told from the owner's.
func MergedItself(login string) bool {
	return strings.HasSuffix(strings.ToLower(login), "[bot]")
}

// revertBody matches the line GitHub's Revert button writes into the body of a
// revert, naming the repository and pull request it takes back.
var revertBody = regexp.MustCompile(`(?m)^Reverts ([^/[:space:]]+/[^#[:space:]]+)#(\d+)\r?$`)

// revertTitle matches the title a revert carries, quoting the title it takes
// back.
var revertTitle = regexp.MustCompile(`^Revert "(.*)"$`)

// measureRewrites records each merged run PR's measured lines on its run row and
// records the escape an auto-merged PR leaves when a later merge reverts it or
// names it as fixed. It runs after the dispatch walk, so the samples the next
// poll's rewrite rate is computed from are already written.
//
// A repository whose listing fails is logged and skipped while another one
// listed, since the reachability record already reports it; only when every
// listed repository fails does the phase return the failures.
func measureRewrites(ctx context.Context, d Deps, c Config) error {
	var errs []error
	var failures []listingFailure
	for _, repo := range c.Repos {
		prs, err := d.PullRequests.Closed(ctx, repo)
		truncated := errors.Is(err, ErrClosedTruncated)
		if truncated {
			err = nil
		}
		if err != nil {
			failures = append(failures, listingFailure{repo, err})
			continue
		}
		if err := measureRepo(ctx, d, repo, prs, truncated); err != nil {
			errs = append(errs, err)
		}
	}
	for _, f := range failures {
		if len(failures) == len(c.Repos) {
			errs = append(errs, fmt.Errorf("listing the closed pull requests of %s: %w", f.repo, f.err))
		} else {
			d.Logger.Warn("rewriteListingFailed", "repo", f.repo, "err", f.err)
		}
	}
	return errors.Join(errs...)
}

// listingFailure is one repository whose closed pull requests could not be listed.
type listingFailure struct {
	repo string
	err  error
}

// measureRepo measures the merged run PRs of one repository's listing, and
// records the escapes its merged reverts and fix references name. A failure
// against one PR is gathered rather than returned, so the rest of the listing is
// still measured and the next poll retries what failed.
func measureRepo(ctx context.Context, d Deps, repo string, prs []ClosedPR, truncated bool) error {
	byNumber := make(map[int]ClosedPR, len(prs))
	byTitle := make(map[string]int, len(prs))
	for _, pr := range prs {
		byNumber[pr.Number] = pr
		if pr.MergedAt.IsZero() || !strings.HasPrefix(pr.Head, runner.BranchPrefix) {
			continue
		}
		byTitle[pr.Title] = pr.Number
	}
	var errs []error
	// Measurements land before any revert is flagged, so a measurement never
	// clears a flag set earlier in the same poll.
	tailFresh := false
	for i, pr := range prs {
		if !pr.MergedAt.IsZero() && strings.HasPrefix(pr.Head, runner.BranchPrefix) {
			fresh, err := measureMerged(ctx, d, repo, pr)
			if err != nil {
				errs = append(errs, err)
			}
			// A merge first measured on the last page may have older ones behind it.
			tailFresh = tailFresh || (fresh && i >= len(prs)-closedPRsPerPage)
		}
	}
	if truncated && tailFresh {
		errs = append(errs, fmt.Errorf("%s: %w with a merge first measured on its last page", repo, ErrClosedTruncated))
	}
	// A state that cannot be read leaves nothing settled here, and the store
	// still refuses a latch the owner cleared, so the escape is looked for rather
	// than skipped.
	state, err := d.Escapes.State(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	for _, pr := range prs {
		if reverted := revertedPR(pr, repo, byTitle); reverted > 0 && !state.Settled(repo, reverted) {
			if err := recordRevertEscape(ctx, d, repo, pr, reverted, byNumber); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := recordFixEscapes(ctx, d, repo, prs, byNumber, state); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// measureMerged records pr's measured lines on its run row, unless the row
// already carries the measurement of that same merge, and reports whether it
// measured.
func measureMerged(ctx context.Context, d Deps, repo string, pr ClosedPR) (bool, error) {
	row, ok, err := d.Rewrites.RunForMerged(ctx, repo, pr)
	if err != nil {
		return false, err
	}
	if !ok {
		// A merged PR no run row names is not this product's PR: a branch
		// pushed by hand, or a row removed since.
		return false, reportUnfoundPR(ctx, d, "mergedPRUnfound", repo, pr.Number, pr.Head)
	}
	if row.Measured.Equal(pr.MergedAt) {
		// A merge is measured once. A row measured before the merge account was
		// kept names none, and the rate leaves an unknown account out of its human
		// window rather than reading it as the owner's, so the row is not re-read
		// here.
		return false, nil
	}
	// Who merged it is what tells an auto-merged PR from the owner's, and a
	// listing that carried none is read again rather than measured as the
	// owner's: a merge read the wrong way dilutes the rate.
	mergedBy := pr.MergedBy
	if mergedBy == "" {
		one, err := d.PullRequests.Pull(ctx, repo, pr.Number)
		if err != nil {
			return false, err
		}
		mergedBy = one.MergedBy
	}
	commits, err := d.PullRequests.Commits(ctx, repo, pr.Number)
	if err != nil {
		return false, err
	}
	measured := Measured{
		Rewrite:  RewriteOf(commits),
		MergedAt: pr.MergedAt,
		MergedBy: mergedBy,
		PR:       pr.Number,
		Title:    pr.Title,
	}
	if err := d.Rewrites.Record(ctx, row.RunID, measured); err != nil {
		return false, err
	}
	d.Logger.Info("rewriteMeasured", "run", row.RunID, "repo", repo, "pr", pr.Number,
		"agentLines", measured.AgentLines, "runnerFixLines", measured.RunnerFixLines,
		"ownerLines", measured.OwnerLines, "rate", measured.Rate(),
		"mergedBy", mergedBy)
	return true, nil
}

// reportUnfoundPR reports a pull request that no run row names, and returns
// whether this poll is the first to report it: such a PR keeps naming no run, and
// every poll that reads the listing finds it again. token names the read
// that found it, since a merge and a revert of one pull request are two reads of
// a row that is not there, and the first of them to run reports it.
func reportUnfoundPR(ctx context.Context, d Deps, token, repo string, number int, head string) error {
	fresh, err := d.Rewrites.MarkUnfound(ctx, repo, number, d.Now())
	if err != nil {
		return err
	}
	if fresh {
		d.Logger.Warn(token, "repo", repo, "pr", number, "branch", head)
	}
	return nil
}

// recordRevertEscape records the escape a merged revert leaves on the run row of
// the PR it names, when that PR merged itself within the window. The PR must be
// one of repo's own, a run's, and have merged before the revert did.
func recordRevertEscape(ctx context.Context, d Deps, repo string, revert ClosedPR, reverted int, byNumber map[int]ClosedPR) error {
	original, ok := byNumber[reverted]
	if !ok {
		var err error
		original, err = d.PullRequests.Pull(ctx, repo, reverted)
		if err != nil {
			return err
		}
	}
	if original.MergedAt.IsZero() || !original.MergedAt.Before(revert.MergedAt) ||
		(original.Number != 0 && original.Number != reverted) || !strings.HasPrefix(original.Head, runner.BranchPrefix) {
		// A revert of a PR that never merged, merged later, is not the one named or
		// is not a run's takes back nothing this product did.
		return nil
	}
	row, ok, err := d.Rewrites.RunForMerged(ctx, repo, original)
	if err != nil {
		return err
	}
	if !ok {
		return reportUnfoundPR(ctx, d, "revertedPRUnfound", repo, reverted, original.Head)
	}
	d.Logger.Warn("runPRReverted", "run", row.RunID, "repo", repo, "pr", reverted)
	// A revert of a PR the owner merged is the owner's own doing, so only a merge
	// proven to be the product's latches.
	itself, err := rowMergedItself(ctx, d, repo, row, original)
	if err != nil {
		return err
	}
	if !itself {
		return nil
	}
	mergedAt := row.Measured
	if mergedAt.IsZero() {
		mergedAt = original.MergedAt
	}
	if revert.MergedAt.Sub(mergedAt) > EscapeWindow {
		return nil
	}
	return recordEscape(ctx, d, MergeEscape{Repo: repo, PR: reverted, By: revert.Number, Reason: EscapeReverted, At: d.Now()})
}

// fixesPR matches a line naming a pull request as closed or fixed: GitHub's
// closing keywords, each followed by the number.
var fixesPR = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+#(\d+)\b`)

// fixedPRs is the numbers a merged PR's body names as fixed.
func fixedPRs(body string) []int {
	var out []int
	for _, m := range fixesPR.FindAllStringSubmatch(body, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// rowMergedItself reports whether the PR a run row names merged itself. The row
// names the merge account once it is measured; a row measured before that was
// kept names none, so the account comes from GitHub's own record of the merge, and
// where that names none either the row's eligibility mark stands in for it: an
// escape that cannot be told from the owner's own merge latches rather than
// passes, which is the side that keeps auto-merge off.
func rowMergedItself(ctx context.Context, d Deps, repo string, row RunRow, pr ClosedPR) (bool, error) {
	if row.MergedBy != "" {
		return MergedItself(row.MergedBy), nil
	}
	mergedBy := pr.MergedBy
	if mergedBy == "" && pr.Number != 0 {
		one, err := d.PullRequests.Pull(ctx, repo, pr.Number)
		if err != nil {
			return false, err
		}
		mergedBy = one.MergedBy
	}
	if mergedBy != "" {
		return MergedItself(mergedBy), nil
	}
	return row.AutoMerge, nil
}

// recordFixEscapes records the escape each merged PR names as fixed within
// EscapeWindow of the fixed PR's merge: a defect nobody read is why a later
// merge came to fix it. A reference to an escape that is settled is not looked
// up, and one to a pull request no run opened costs one query and no GitHub read.
func recordFixEscapes(ctx context.Context, d Deps, repo string, prs []ClosedPR, byNumber map[int]ClosedPR, state EscapeState) error {
	var errs []error
	for _, fixing := range prs {
		for _, number := range fixedPRs(fixing.Body) {
			if state.Settled(repo, number) {
				continue
			}
			row, ok, err := d.Rewrites.RunForMerged(ctx, repo, ClosedPR{Number: number})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			// Only a measured merge inside the window can have escaped.
			if !ok || row.Measured.IsZero() || !row.Measured.Before(fixing.MergedAt) || fixing.MergedAt.Sub(row.Measured) > EscapeWindow {
				continue
			}
			fixed, listed := byNumber[number]
			if !listed {
				fixed = ClosedPR{Number: number}
			}
			itself, err := rowMergedItself(ctx, d, repo, row, fixed)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !itself {
				continue
			}
			if err := recordEscape(ctx, d, MergeEscape{Repo: repo, PR: number, By: fixing.Number, Reason: EscapeFixed, At: d.Now()}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// recordEscape records an escape beside the auto-merge switch, logging the
// first record of it: that one holds auto-merge off until the owner switches it
// on again.
func recordEscape(ctx context.Context, d Deps, e MergeEscape) error {
	fresh, err := d.Escapes.RecordEscape(ctx, e)
	if err != nil {
		return err
	}
	if fresh {
		d.Logger.Warn("autoMergeEscaped", "repo", e.Repo, "pr", e.PR, "by", e.By, "reason", e.Reason)
	}
	return nil
}

// revertedPR is the pull request number a merged revert takes back, or 0. It is
// the number GitHub's Revert button writes into the body, checked against the
// repository the revert merged into, or — for a revert made by hand and titled
// as git titles one — the pull request in the same listing whose own title the
// revert quotes.
func revertedPR(pr ClosedPR, repo string, byTitle map[string]int) int {
	if pr.MergedAt.IsZero() {
		// A revert that closed unmerged took nothing back.
		return 0
	}
	if m := revertBody.FindStringSubmatch(pr.Body); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil && strings.EqualFold(m[1], repo) {
			return n
		}
		return 0
	}
	m := revertTitle.FindStringSubmatch(pr.Title)
	if m == nil {
		return 0
	}
	return byTitle[m[1]]
}
