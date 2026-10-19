package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
// reads the samples the auto-merge rule is decided from.
type Rewrites interface {
	// RunForMerged returns the run row merged PR pr belongs to, whichever
	// attempt opened it.
	RunForMerged(ctx context.Context, repo string, pr ClosedPR) (RunRow, bool, error)
	// MarkUnfound records that merged PR number of repo names no run row,
	// reporting whether this is the first poll to record it.
	MarkUnfound(ctx context.Context, repo string, number int, at time.Time) (bool, error)
	// Record writes m onto run id's row.
	Record(ctx context.Context, id string, m Measured) error
	// FlagReverted flags run id's row as one a merged revert names.
	FlagReverted(ctx context.Context, id string) error
	// Recent returns the merged PRs' samples, newest merge first, up to limit.
	Recent(ctx context.Context, limit int) ([]Sample, error)
}

// revertBody matches the line GitHub's Revert button writes into the body of a
// revert, naming the repository and pull request it takes back.
var revertBody = regexp.MustCompile(`(?m)^Reverts ([^/[:space:]]+/[^#[:space:]]+)#(\d+)\r?$`)

// revertTitle matches the title a revert carries, quoting the title it takes
// back.
var revertTitle = regexp.MustCompile(`^Revert "(.*)"$`)

// measureRewrites records each merged run PR's measured lines on its run row and
// flags the rows of the PRs a merged revert names. It runs after the dispatch
// walk, so the samples the next poll's auto-merge rule reads are already written.
func measureRewrites(ctx context.Context, d Deps, c Config) error {
	var errs []error
	for _, repo := range c.Repos {
		prs, err := d.PullRequests.Closed(ctx, repo)
		truncated := errors.Is(err, ErrClosedTruncated)
		if truncated {
			err = nil
		}
		if err != nil {
			// One repository's listing failing must not stop the others', and
			// the next poll asks again.
			errs = append(errs, fmt.Errorf("listing the closed pull requests of %s: %w", repo, err))
			continue
		}
		if err := measureRepo(ctx, d, repo, prs, truncated); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// measureRepo measures the merged run PRs of one repository's listing, and flags
// the run rows of the PRs its merged reverts name. A failure against one PR is
// gathered rather than returned, so the rest of the listing is still measured
// and the next poll retries what failed.
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
	for _, pr := range prs {
		if reverted := revertedPR(pr, repo, byTitle); reverted > 0 {
			if err := flagReverted(ctx, d, repo, pr, reverted, byNumber); err != nil {
				errs = append(errs, err)
			}
		}
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
		return false, nil
	}
	commits, err := d.PullRequests.Commits(ctx, repo, pr.Number)
	if err != nil {
		return false, err
	}
	measured := Measured{
		Rewrite:  RewriteOf(commits),
		MergedAt: pr.MergedAt,
		PR:       pr.Number,
		Title:    pr.Title,
	}
	if err := d.Rewrites.Record(ctx, row.RunID, measured); err != nil {
		return false, err
	}
	d.Logger.Info("rewriteMeasured", "run", row.RunID, "repo", repo, "pr", pr.Number,
		"agentLines", measured.AgentLines, "ownerLines", measured.OwnerLines, "rate", measured.Rate())
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

// flagReverted marks the run row of the PR a merged revert names. The PR must be
// one of repo's own, a run's, and have merged before the revert did.
func flagReverted(ctx context.Context, d Deps, repo string, revert ClosedPR, reverted int, byNumber map[int]ClosedPR) error {
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
	if err := d.Rewrites.FlagReverted(ctx, row.RunID); err != nil {
		return err
	}
	d.Logger.Warn("runPRReverted", "run", row.RunID, "repo", repo, "pr", reverted)
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
