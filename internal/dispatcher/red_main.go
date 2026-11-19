package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// MainReverts is GitHub as a red main is traced and an auto-merge reverted.
type MainReverts interface {
	// FirstParents returns branch's first-parent chain, its head first, at most
	// mainWalkLimit commits.
	FirstParents(ctx context.Context, repo, branch string) ([]string, error)
	// OpenPulls returns repo's open pull requests.
	OpenPulls(ctx context.Context, repo string) ([]ClosedPR, error)
	// RevertPull opens a pull request that reverts pr, titled title with body,
	// and returns its number.
	RevertPull(ctx context.Context, pr ClosedPR, title, body string) (int, error)
}

// revertRedMain handles a main that fails its required checks. It traces main
// back to the commit that turned it red, the oldest of the failing commits from
// its head whose own first parent passes. When that commit is the merge of a run
// PR the product merged itself, it opens a revert of that PR, latches auto-merge
// off naming it, and records the revert on the PR's run row; any other first red
// commit is the owner's to mend, and opens and latches nothing.
//
// A merge is reverted once: a row naming its revert opens no second one, and a
// revert already open is taken rather than another opened. A row naming its
// revert is still latched while the escape is not settled, so an escape an
// earlier latch absorbed latches once the owner clears that one.
//
// A GitHub read that fails, or checks still pending on the way, leave main for
// the next poll; a revert GitHub refuses is logged as revertNotOpened. Neither
// fails the poll. A store write that fails is returned.
func revertRedMain(ctx context.Context, d Deps, repo string, prs []ClosedPR, state EscapeState) error {
	byCommit := make(map[string]ClosedPR)
	for _, pr := range prs {
		// A PR carries its merge commit only once it merged.
		if pr.MergeCommit != "" && strings.HasPrefix(pr.Head, runner.BranchPrefix) {
			byCommit[pr.MergeCommit] = pr
		}
	}
	if len(byCommit) == 0 {
		// No run PR merged in the window, so none can have turned main red.
		return nil
	}
	branch := d.Checks.Branch
	chain, err := d.Reverts.FirstParents(ctx, repo, branch)
	if err != nil {
		d.Logger.Warn("redMainUnread", "reason", "commits", "repo", repo, "err", err)
		return nil
	}
	if !anyMerged(chain, byCommit) {
		// The commit that turned main red is one of these, so none is a run's.
		return nil
	}
	firstRed, found := firstRedCommit(ctx, d, repo, branch, chain)
	if !found {
		return nil
	}
	merged, ok := byCommit[firstRed]
	if !ok {
		d.Logger.Info("mainRedNotAutoMerged", "repo", repo, "sha", firstRed)
		return nil
	}
	row, found, err := d.Rewrites.RunForMerged(ctx, repo, merged)
	if err != nil {
		return err
	}
	if !found || (row.RevertedBy > 0 && state.Settled(repo, merged.Number)) {
		return nil
	}
	itself, err := rowMergedItself(ctx, d, repo, row, merged)
	if err != nil {
		return err
	}
	if !itself {
		// The owner's own merge is the owner's to take back.
		d.Logger.Info("mainRedNotAutoMerged", "repo", repo, "sha", firstRed, "pr", merged.Number)
		return nil
	}
	d.Logger.Warn("mainRedAfterAutoMerge", "run", row.RunID, "repo", repo, "pr", merged.Number, "sha", firstRed)
	revert := row.RevertedBy
	// A refusal that could not be kept is returned after the latch, which must
	// land whatever became of the revert.
	var refusalErr error
	if revert == 0 {
		revert, refusalErr = openRevert(ctx, d, repo, row, merged, firstRed)
	}
	// The latch lands before the revert is recorded: a row that names its revert
	// is skipped once its escape is settled, so a latch that failed here would
	// otherwise never be retried. The store keeps an escape already latched or
	// cleared, and names the revert on this one when it opened later.
	if err := recordEscape(ctx, d, MergeEscape{Repo: repo, PR: merged.Number, By: revert, Reason: EscapeRedMain, At: d.Now()}); err != nil {
		return errors.Join(err, refusalErr)
	}
	if refusalErr != nil {
		return refusalErr
	}
	// revert is 0 only while the row names none, so this also skips a revert
	// that could not be found or opened.
	if revert == row.RevertedBy {
		return nil
	}
	return d.Rewrites.RecordRevert(ctx, row.RunID, revert)
}

// anyMerged reports whether any commit of chain is one of byCommit's merges.
func anyMerged(chain []string, byCommit map[string]ClosedPR) bool {
	for _, sha := range chain {
		if _, ok := byCommit[sha]; ok {
			return true
		}
	}
	return false
}

// firstRedCommit returns the commit of chain that turned branch red: the oldest
// of the commits failing their required checks without a break from the head,
// once its own first parent is read passing them. It reports false for a head
// that is not failing, and leaves for the next poll a walk that meets pending or
// unreadable checks, or that ends before a passing commit.
func firstRedCommit(ctx context.Context, d Deps, repo, branch string, chain []string) (string, bool) {
	required, err := d.Checks.GitHub.RequiredChecks(ctx, repo, branch)
	if err != nil {
		d.Logger.Warn("redMainUnread", "reason", "required-checks", "repo", repo, "err", err)
		return "", false
	}
	for i, sha := range chain {
		outcome, err := d.Checks.GitHub.ChecksOutcome(ctx, repo, sha, required)
		if err != nil {
			d.Logger.Warn("redMainUnread", "reason", "checks", "repo", repo, "sha", sha, "err", err)
			return "", false
		}
		switch {
		case outcome == CheckFailing:
			continue
		case outcome == CheckGreen && i > 0:
			return chain[i-1], true
		case outcome == CheckPending && i > 0:
			d.Logger.Info("redMainUnread", "reason", "parent-pending", "repo", repo, "sha", sha)
		}
		return "", false
	}
	d.Logger.Warn("redMainUnread", "reason", "walk-exhausted", "repo", repo, "commits", len(chain))
	return "", false
}

// openRevert returns the number of the open revert of merged, opening one when
// none is open, or 0 when none could be found or opened. Open pull requests
// that cannot be listed open nothing, since a revert may already be among them.
// A refusal is raised at error once and logged at warn while GitHub repeats it,
// since every poll asks again; only keeping a new refusal on row can fail.
func openRevert(ctx context.Context, d Deps, repo string, row RunRow, merged ClosedPR, sha string) (int, error) {
	open, err := d.Reverts.OpenPulls(ctx, repo)
	if err != nil {
		d.Logger.Warn("revertNotOpened", "reason", "open-pulls-unread", "repo", repo, "pr", merged.Number, "err", err)
		return 0, nil
	}
	title := revertTitleOf(merged.Title)
	for _, pr := range open {
		if !ownRevertBranch(pr, repo, merged.Number) {
			// Anyone can open a PR titled or worded as a revert from a fork; only
			// a branch in repo itself, named as GitHub names a revert, is one.
			continue
		}
		if n, _ := revertNamed(pr.Body, repo); n == merged.Number || pr.Title == title {
			d.Logger.Info("revertAlreadyOpen", "repo", repo, "pr", merged.Number, "revert", pr.Number)
			return pr.Number, nil
		}
	}
	body := fmt.Sprintf("Reverts %s#%d\n\nmain's required checks fail from %s, the commit this pull request's auto-merge made. "+
		"Merging this takes it back; auto-merge stays off until you switch it on again.", repo, merged.Number, sha)
	number, err := d.Reverts.RevertPull(ctx, merged, title, body)
	if err == nil {
		d.Logger.Warn("revertOpened", "repo", repo, "pr", merged.Number, "revert", number)
		return number, nil
	}
	refusal := refusalText(err)
	if refusal == row.RevertRefusal {
		d.Logger.Warn("revertNotOpened", "reason", "refused", "repo", repo, "pr", merged.Number, "repeated", true, "err", refusal)
		return 0, nil
	}
	d.Logger.Error("revertNotOpened", "reason", "refused", "repo", repo, "pr", merged.Number, "err", refusal)
	return 0, d.Rewrites.RecordRevertRefusal(ctx, row.RunID, refusal)
}

// refusalText is err's message as a run row keeps it: valid UTF-8, cut on a
// character boundary at maxErrorBytes.
func refusalText(err error) string {
	s := strings.ToValidUTF8(err.Error(), "�")
	if len(s) <= maxErrorBytes {
		return s
	}
	n := maxErrorBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// ownRevertBranch reports whether pr's head is a branch of repo itself named as
// GitHub's revertPullRequest names the revert of pull request number:
// revert-<number>-<its head branch>. A PR from a fork, or from any other branch,
// is not taken as that revert, since only someone who can push to repo can
// create such a branch.
func ownRevertBranch(pr ClosedPR, repo string, number int) bool {
	return strings.EqualFold(pr.HeadRepo, repo) && strings.HasPrefix(pr.Head, fmt.Sprintf("revert-%d-", number))
}

// revertTitleOf is the title git and GitHub give a revert of a change titled title.
func revertTitleOf(title string) string { return `Revert "` + title + `"` }
