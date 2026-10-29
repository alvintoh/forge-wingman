package dispatcher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// CheckUnresolved is the result of a run whose checks can never be read: its
// pull request holds no commit by the runner's login, or its URL names no pull
// request.
const CheckUnresolved = "unresolved"

const (
	checksPerPoll    = 20
	checksStaleAfter = 24 * time.Hour
	checksTimeout    = 60 * time.Second
)

// UnsettledRun is a run whose pull request's checks have no recorded result.
type UnsettledRun struct {
	ID        string
	PRURL     string
	StartedAt time.Time
}

// ChecksRuns reads the runs awaiting a checks result and records one.
type ChecksRuns interface {
	// UnsettledRuns returns up to limit runs with a pull request and no checks
	// result, oldest start first.
	UnsettledRuns(ctx context.Context, limit int) ([]UnsettledRun, error)
	// RecordChecks writes the agent's head sha, when there is one, and the result
	// onto run id's row and reports whether it did: a row that already has a
	// result, or that no longer holds the pull request prURL, is left as it is.
	RecordChecks(ctx context.Context, id, prURL, sha, result string) (bool, error)
}

// ChecksGitHub reads what a checks result is decided from.
type ChecksGitHub interface {
	PullCommits(ctx context.Context, repo string, number int) ([]PullCommit, error)
	RequiredChecks(ctx context.Context, repo, branch string) ([]string, error)
	ChecksOutcome(ctx context.Context, repo, sha string, required []string) (string, error)
}

// ChecksDeps are what the checks phase reads and writes. Branch is the branch
// whose rules name the required checks.
type ChecksDeps struct {
	Runs   ChecksRuns
	GitHub ChecksGitHub
	Branch string
}

func (c ChecksDeps) ready() bool {
	return c.Runs != nil && c.GitHub != nil && c.Branch != ""
}

// requiredChecks is one repository's required contexts, or why they could not be read.
type requiredChecks struct {
	names []string
	err   error
}

// settleChecks records the result of the required checks of the agent's last
// commit on each run whose pull request has none yet, once they have all
// finished, and marks CheckUnresolved a run that can never be read. A run it
// cannot settle yet, or whose read failed, is left for the next poll; only a
// failure to list the runs is returned.
func settleChecks(ctx context.Context, d Deps) error {
	runs, err := d.Checks.Runs.UnsettledRuns(ctx, checksPerPoll)
	if err != nil {
		return fmt.Errorf("listing the runs with unsettled checks: %w", err)
	}
	required := map[string]requiredChecks{}
	settled, unresolved := 0, 0
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			d.Logger.Warn("checksStopped", "err", err)
			break
		}
		switch settleRunChecks(ctx, d, run, required) {
		case "":
		case CheckUnresolved:
			unresolved++
		default:
			settled++
		}
	}
	if settled > 0 {
		d.Logger.Info("checksSettled", "count", settled)
	}
	if unresolved > 0 {
		d.Logger.Info("checksUnresolvedMarked", "count", unresolved)
	}
	return nil
}

// settleRunChecks returns the result it recorded on run's row, or "" when it
// recorded none.
func settleRunChecks(ctx context.Context, d Deps, run UnsettledRun, required map[string]requiredChecks) string {
	repo, number, err := ParsePullURL(run.PRURL)
	if err != nil {
		if recordChecks(ctx, d, run, "", CheckUnresolved) {
			d.Logger.Warn("checksBadPRURL", "run", run.ID, "err", err)
			return CheckUnresolved
		}
		return ""
	}
	gh := d.Checks.GitHub
	commits, err := gh.PullCommits(ctx, repo, number)
	if err != nil {
		d.Logger.Warn("checksReadFailed", "run", run.ID, "repo", repo, "pr", number, "err", err)
		return ""
	}
	sha := lastAgentCommit(commits)
	if sha == "" {
		if recordChecks(ctx, d, run, "", CheckUnresolved) {
			d.Logger.Info("checksNoAgentCommit", "run", run.ID, "repo", repo, "pr", number)
			return CheckUnresolved
		}
		return ""
	}
	names, ok := required[repo]
	if !ok {
		names.names, names.err = gh.RequiredChecks(ctx, repo, d.Checks.Branch)
		required[repo] = names
	}
	if names.err != nil {
		d.Logger.Warn("checksReadFailed", "run", run.ID, "repo", repo, "pr", number, "err", names.err)
		return ""
	}
	outcome, err := gh.ChecksOutcome(ctx, repo, sha, names.names)
	if err != nil {
		d.Logger.Warn("checksReadFailed", "run", run.ID, "repo", repo, "pr", number, "sha", sha, "err", err)
		return ""
	}
	if outcome == CheckPending {
		if !run.StartedAt.IsZero() && d.Now().Sub(run.StartedAt) > checksStaleAfter {
			d.Logger.Warn("checksPendingStale", "run", run.ID, "repo", repo, "pr", number, "sha", sha)
		}
		return ""
	}
	if recordChecks(ctx, d, run, sha, outcome) {
		return outcome
	}
	return ""
}

// recordChecks writes result onto run's row and reports whether the row took it.
func recordChecks(ctx context.Context, d Deps, run UnsettledRun, sha, result string) bool {
	recorded, err := d.Checks.Runs.RecordChecks(ctx, run.ID, run.PRURL, sha, result)
	if err != nil {
		d.Logger.Error("checksRecordFailed", "run", run.ID, "result", result, "err", err)
		return false
	}
	return recorded
}

// lastAgentCommit is the sha of the last commit GitHub resolves to the runner's
// own login, or "" when the agent wrote none.
func lastAgentCommit(commits []PullCommit) string {
	for i := len(commits) - 1; i >= 0; i-- {
		if strings.EqualFold(commits[i].Login, runner.CommitAuthorLogin) {
			return commits[i].SHA
		}
	}
	return ""
}
