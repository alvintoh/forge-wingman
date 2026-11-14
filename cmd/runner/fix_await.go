package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// defaultFixAwaitTimeout is how long a fix round waits for the review of the
	// head it pushed: long enough for the review job's own model pass, short
	// enough to leave the loop room for a round after it.
	defaultFixAwaitTimeout = 15 * time.Minute
	// defaultFixAwaitInterval is how long between the polls a wait makes.
	defaultFixAwaitInterval = 20 * time.Second
)

// The decisions fix-await reports, and the reasons it stops.
const (
	fixAwaitContinue = "continue"
	fixAwaitStop     = "stop"

	fixAwaitReasonFindings  = "findings"
	fixAwaitReasonClean     = "clean"
	fixAwaitReasonNoVerdict = "no-verdict"
	fixAwaitReasonError     = "error"
	fixAwaitReasonTimeout   = "timeout"
)

// fixAwaitWait bounds a wait: how long it may last, how often it asks, and when
// the run it belongs to started, which caps it at the run's shared budget.
type fixAwaitWait struct {
	Timeout  time.Duration
	Interval time.Duration
	Started  time.Time
}

// fixAwaitOutcome is what a review of one head says: whether the fix-round loop
// continues with findings or stops, why, and the findings it parsed.
type fixAwaitOutcome struct {
	// Decision is fixAwaitContinue when there are findings to fix and
	// fixAwaitStop otherwise.
	Decision string
	// Reason is the finding of the review: findings for a loop that continues,
	// and clean, no-verdict, error or timeout for one that stops.
	Reason string
	// Findings are the findings the review reported, one per line, capped by
	// runner.CapReviewFindings; empty unless Decision is fixAwaitContinue.
	Findings string
	// Omitted is how many findings the count cap left out, and Cut how many
	// lines the length cap shortened, so neither is a silent loss.
	Omitted int
	Cut     int
	// Detail is the status description, or why the wait ended, in words.
	Detail string
}

// reviewRead asks GitHub what the review of one head says: the outcome and
// whether a verdict has posted yet, false while the poll continues.
type reviewRead func(ctx context.Context) (fixAwaitOutcome, bool, error)

// fixAwait waits, bounded, for the review of one head and reports what it says.
// The wait lasts its own timeout at most and never past what the run's shared
// budget leaves from -started, so a review that never posts ends it with a stop
// reason rather than an error.
func fixAwait(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("fix-await", flag.ContinueOnError)
	number := fs.Int("pr", 0, "the pull request whose review to await")
	sha := fs.String("sha", "", "the head sha to await the review of")
	timeout := fs.Duration("timeout", defaultFixAwaitTimeout, "how long to wait for the review")
	interval := fs.Duration("interval", defaultFixAwaitInterval, "how long between polls")
	started := fs.String("started", "", "when the run started, RFC3339, which the wait never outlasts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *number <= 0 || !shaPattern.MatchString(*sha) {
		return errors.New("fix-await needs -pr and a -sha")
	}
	if *timeout <= 0 || *interval <= 0 {
		return errors.New("fix-await needs a positive -timeout and -interval")
	}
	runStarted, err := time.Parse(time.RFC3339, *started)
	if err != nil {
		return fmt.Errorf("fix-await needs -started as an RFC3339 time: %w", err)
	}
	gh := pullRequests{API: getenv("GITHUB_API_URL"), Token: getenv("GH_TOKEN"), Repo: getenv("GITHUB_REPOSITORY"),
		Client: &http.Client{Timeout: prReviewRequestTimeout}}
	read := func(ctx context.Context) (fixAwaitOutcome, bool, error) {
		return reviewAt(ctx, gh, *number, *sha)
	}
	out, err := awaitReview(ctx, read, fixAwaitWait{Timeout: *timeout, Interval: *interval, Started: runStarted}, time.Now, sleepCtx)
	if err != nil {
		return err
	}
	logger.Info("fixAwait", "pr", *number, "sha", *sha, "decision", out.Decision, "reason", out.Reason,
		"findings", out.Findings, "omitted", out.Omitted, "cut", out.Cut)
	return writeFixAwaitOutputs(getenv("GITHUB_OUTPUT"), out)
}

// awaitReview polls read until it reports a verdict, the wait ends, or ctx is
// done. The wait's deadline is its own timeout, brought forward when the run's
// shared budget leaves less.
func awaitReview(ctx context.Context, read reviewRead, w fixAwaitWait, now func() time.Time, sleep func(context.Context, time.Duration) error) (fixAwaitOutcome, error) {
	start := now()
	budget := runner.RunBudgetLeft(w.Started, start)
	deadline := start.Add(w.Timeout)
	if budget < w.Timeout {
		deadline = start.Add(budget)
	}
	for {
		out, ok, err := read(ctx)
		if err != nil {
			return fixAwaitOutcome{}, err
		}
		if ok {
			return out, nil
		}
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return fixAwaitOutcome{Decision: fixAwaitStop, Reason: fixAwaitReasonTimeout,
				Detail: fixAwaitTimeoutDetail(w.Timeout, budget)}, nil
		}
		if err := sleep(ctx, min(w.Interval, remaining)); err != nil {
			return fixAwaitOutcome{}, err
		}
	}
}

// fixAwaitTimeoutDetail says why the wait ended: the run's budget cut it short,
// or the wait's own timeout ran out.
func fixAwaitTimeoutDetail(timeout, budget time.Duration) string {
	if budget < timeout {
		return fmt.Sprintf("the run's shared budget left %s of the %s wait", budget.Round(time.Second), timeout.Round(time.Second))
	}
	return fmt.Sprintf("no review posted within %s", timeout.Round(time.Second))
}

// reviewAt reads the review of sha: its latest wingman-review status and, for a
// findings status, the comment at the head the review names. ok is false while
// no review of sha has posted, which is the poll continuing.
func reviewAt(ctx context.Context, gh pullRequests, number int, sha string) (fixAwaitOutcome, bool, error) {
	statuses, err := gh.statuses(ctx, sha)
	if err != nil {
		return fixAwaitOutcome{}, false, err
	}
	status := latestReviewStatus(statuses)
	switch {
	case status.State == "success" && strings.HasPrefix(status.Description, cleanStatusDescription):
		return fixAwaitOutcome{Decision: fixAwaitStop, Reason: fixAwaitReasonClean, Detail: status.Description}, true, nil
	case status.State == "failure" && strings.HasPrefix(status.Description, findingsStatusDescription):
		return findingsAt(ctx, gh, number, sha, status.Description)
	case status.State == "failure" && strings.HasPrefix(status.Description, noVerdictStatusDescription):
		return fixAwaitOutcome{Decision: fixAwaitStop, Reason: fixAwaitReasonNoVerdict, Detail: status.Description}, true, nil
	case status.State == "error":
		return fixAwaitOutcome{Decision: fixAwaitStop, Reason: fixAwaitReasonError, Detail: status.Description}, true, nil
	}
	return fixAwaitOutcome{}, false, nil
}

// findingsAt reads the findings a findings status reports. The review comment
// holds them only when its At line names sha: a comment for another head is not
// this review, so the poll continues. A pull request with no review comment yet
// reads as one naming no head.
func findingsAt(ctx context.Context, gh pullRequests, number int, sha, description string) (fixAwaitOutcome, bool, error) {
	comments, err := gh.comments(ctx, number)
	if err != nil {
		return fixAwaitOutcome{}, false, err
	}
	c, _ := markedComment(comments)
	parsed, readable := parseReviewComment(c.Body)
	if parsed.SHA != shortSHA(sha) {
		return fixAwaitOutcome{}, false, nil
	}
	if !readable {
		return fixAwaitOutcome{Decision: fixAwaitStop, Reason: fixAwaitReasonError,
			Detail: "the review comment for " + shortSHA(sha) + " has an unclosed findings fence"}, true, nil
	}
	if strings.TrimSpace(parsed.Findings) == "" {
		return fixAwaitOutcome{Decision: fixAwaitStop, Reason: fixAwaitReasonError,
			Detail: "the review comment for " + shortSHA(sha) + " lists no findings"}, true, nil
	}
	capped, omitted, cut := runner.CapReviewFindings(parsed.Findings)
	return fixAwaitOutcome{Decision: fixAwaitContinue, Reason: fixAwaitReasonFindings, Findings: capped,
		Omitted: omitted, Cut: cut, Detail: description}, true, nil
}

// latestReviewStatus is the newest wingman-review status, or the zero status
// when the head has none, which no case reads as a verdict. The API answers
// latest first and created_at decides among them, so a stale success after a
// newer failure is never read as clean; of two created in the same second the
// earlier answer wins, which is the later post.
func latestReviewStatus(statuses []commitStatusEntry) commitStatusEntry {
	var reviews []commitStatusEntry
	for _, s := range statuses {
		if s.Context == prReviewStatusContext {
			reviews = append(reviews, s)
		}
	}
	if len(reviews) == 0 {
		return commitStatusEntry{}
	}
	latest := reviews[0]
	for _, s := range reviews[1:] {
		if s.CreatedAt.After(latest.CreatedAt) {
			latest = s
		}
	}
	return latest
}

// writeFixAwaitOutputs reports the outcome to the step that reads it, the
// findings spanning lines as their own output.
func writeFixAwaitOutputs(path string, out fixAwaitOutcome) error {
	if err := writeOutputs(path, map[string]string{
		"decision":         out.Decision,
		"reason":           out.Reason,
		"detail":           out.Detail,
		"findings_omitted": strconv.Itoa(out.Omitted),
		"findings_cut":     strconv.Itoa(out.Cut),
	}); err != nil {
		return err
	}
	return writeMultilineOutput(path, "findings", out.Findings)
}

// sleepCtx waits d, or returns early with the context's error.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
