package dispatcher

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
)

// MergeChecks are the checks main's rules must require for auto-merge to be
// safe, and the ones an allowance-exhausted review must see green before it
// requests a merge, as a fresh slice each call. `.github/workflows/pr-review.yml`'s
// auto-merge step tests the same two contexts, so a rename here is half a change.
func MergeChecks() []string { return []string{"go", "web"} }

const (
	rulesetClass     = "ruleset:"
	rulesetNoticeKey = "ruleset-"
	rulesetTimeout   = 30 * time.Second
)

// RulesetMarks remembers each repository whose branch rules a poll has seen
// require every one of MergeChecks, so only a repository that once required
// them can be said to have stopped.
type RulesetMarks interface {
	// MarkRequired records that repo's rules were seen to require them; marking
	// a repository already marked is not an error.
	MarkRequired(ctx context.Context, repo string, at time.Time) error
	// SeenRequired reports whether repo was ever marked.
	SeenRequired(ctx context.Context, repo string) (bool, error)
}

// noticeRulesetDrift checks each listed repository's branch rules against
// MergeChecks, continuing past a repository that fails so one does not hide the
// rest, and returns the store failures it met.
func noticeRulesetDrift(ctx context.Context, d Deps, repos []string) error {
	var errs []error
	for _, repo := range repos {
		errs = append(errs, checkRuleset(ctx, d, repo))
	}
	return errors.Join(errs...)
}

// checkRuleset raises a notice when repo's rules no longer require every one of
// MergeChecks, naming those missing, and clears it once they are all required
// again, so a later drift is noticed anew. The notice id names only the
// repository, so a repeat poll during one drift raises nothing new. Rules a poll
// has never seen require them are out of scope, since nothing was ever
// required to stop; rules that cannot be read are logged as rulesetUnread and
// change nothing.
func checkRuleset(ctx context.Context, d Deps, repo string) error {
	branch := d.Checks.Branch
	required, err := d.Checks.GitHub.RequiredChecks(ctx, repo, branch)
	if err != nil && !errors.Is(err, ErrNoRequiredChecks) {
		d.Logger.Warn("rulesetUnread", "repo", repo, "branch", branch,
			"deadline", errors.Is(err, context.DeadlineExceeded), "err", err.Error())
		return nil
	}
	id := rulesetNoticeKey + url.PathEscape(repo)
	missing := missingMergeChecks(required)
	if len(missing) == 0 {
		var errs []error
		if err := d.RulesetMarks.MarkRequired(ctx, repo, d.Now()); err != nil {
			d.Logger.Warn("rulesetMarkNotWritten", "repo", repo, "err", err.Error())
			errs = append(errs, err)
		}
		if err := d.Notices.Clear(ctx, id); err != nil {
			d.Logger.Warn("rulesetNoticeNotCleared", "repo", repo, "err", err.Error())
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	seen, err := d.RulesetMarks.SeenRequired(ctx, repo)
	if err != nil {
		d.Logger.Warn("rulesetMarkUnread", "repo", repo, "err", err.Error())
		return err
	}
	if !seen {
		d.Logger.Debug("rulesetOutOfScope", "repo", repo, "branch", branch, "missing", strings.Join(missing, ","))
		return nil
	}
	d.Logger.Warn("rulesetDrift", "repo", repo, "branch", branch, "missing", strings.Join(missing, ","))
	n := Notice{
		ID:     id,
		Class:  rulesetClass + repo,
		Detail: rulesetDetail(branch, missing),
		Link:   "https://github.com/" + repo + "/settings/rules",
	}
	if err := d.Notices.Raise(ctx, n, d.Now()); err != nil {
		d.Logger.Warn("rulesetNoticeNotRaised", "repo", repo, "err", err.Error())
		return err
	}
	return nil
}

// missingMergeChecks is each of MergeChecks that required does not name, in
// MergeChecks' order.
func missingMergeChecks(required []string) []string {
	var missing []string
	for _, check := range MergeChecks() {
		if !slices.Contains(required, check) {
			missing = append(missing, check)
		}
	}
	return missing
}

// rulesetDetail says which checks branch's rules stopped requiring and what
// that leaves unguarded.
func rulesetDetail(branch string, missing []string) string {
	noun, pronoun := "check", "it"
	if len(missing) > 1 {
		noun, pronoun = "checks", "them"
	}
	return branch + "'s rules no longer require the " + strings.Join(missing, " and ") + " " + noun +
		", so an auto-merge would not wait for " + pronoun
}
