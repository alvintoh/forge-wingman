package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/linear"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	defaultPRReviewTimeout   = 8 * time.Minute
	prReviewRequestTimeout   = 30 * time.Second
	prReviewMarker           = "<!-- wingman-review -->"
	prReviewStatusContext    = "wingman-review"
	prReviewCommentAuthor    = "github-actions[bot]"
	autoMergeEligibleLabel   = "wingman:auto-merge-eligible"
	maxStatusDescription     = 140
	maxPRReviewArtifactBytes = 1 << 20
)

// The reasons a pull request is not reviewed.
const (
	skipNoTicketID     = "no-ticket-id"
	skipTicketNotFound = "ticket-not-found"
	skipDiffTooLarge   = "diff-too-large"
)

var skipDescriptions = map[string]string{
	skipNoTicketID:     "Not reviewed: no ticket id in the title or a ref/closes line",
	skipTicketNotFound: "Not reviewed: Linear has no issue for the ticket id",
	skipDiffTooLarge:   "Not reviewed: the diff is too large to review",
}

var (
	shaPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	runURLPattern = regexp.MustCompile(`^https://[A-Za-z0-9./_-]+$`)
)

// prReviewContext writes the context a pull request's review reads: the
// ticket its title or body names, read from Linear as the read-only review app,
// and its diff, pinned to the head sha it read. A pull request naming no
// ticket, naming one Linear lacks, or too large to review is skipped, with
// the reason as an output.
func prReviewContext(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("pr-review-context", flag.ContinueOnError)
	number := fs.Int("pr", 0, "the pull request to review")
	out := fs.String("out", "", "path to write the review context to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *number <= 0 || *out == "" {
		return errors.New("pr-review-context needs -pr and -out")
	}
	client := &http.Client{Timeout: prReviewRequestTimeout}
	gh := pullRequests{API: getenv("GITHUB_API_URL"), Token: getenv("GH_TOKEN"), Repo: getenv("GITHUB_REPOSITORY"), Client: client}
	issue := func(ctx context.Context, id string) (linear.Issue, error) {
		return readIssue(ctx, logger, getenv("GOOGLE_CLOUD_PROJECT"), client, id)
	}
	return writePRReviewContext(ctx, logger, gh, issue, *number, *out, getenv("GITHUB_OUTPUT"))
}

// writePRReviewContext is prReviewContext's work, with issue reading the ticket.
func writePRReviewContext(ctx context.Context, logger *slog.Logger, gh pullRequests, issue func(context.Context, string) (linear.Issue, error), number int, out, output string) error {
	p, err := gh.pull(ctx, number)
	if err != nil {
		return err
	}
	id, ok := runner.PRTicketID(p.Title, p.Body)
	if !ok {
		return skipReview(logger, output, number, skipNoTicketID)
	}
	diff, tooLarge, err := gh.diff(ctx, p.Base.SHA, p.Head.SHA, runner.MaxPRReviewDiffBytes)
	if err != nil {
		return err
	}
	if tooLarge {
		return skipReview(logger, output, number, skipDiffTooLarge)
	}
	ticket, err := issue(ctx, id)
	if errors.Is(err, linear.ErrIssueNotFound) {
		return skipReview(logger, output, number, skipTicketNotFound)
	}
	if err != nil {
		return err
	}
	c := runner.PRReviewContext{PR: number, HeadSHA: p.Head.SHA, Diff: diff,
		Ticket: runner.PRReviewTicket(id, ticket.Title, ticket.Description, ticket.Labels)}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err := runner.ParsePRReviewContext(raw); err != nil {
		return err
	}
	if err := os.WriteFile(out, raw, 0o600); err != nil {
		return fmt.Errorf("writing the review context: %w", err)
	}
	logger.Info("prReviewContext", "pr", number, "ticket", id, "headSHA", p.Head.SHA, "diffBytes", len(diff))
	return writeOutputs(output, map[string]string{"skip": "false", "head_sha": p.Head.SHA})
}

// skipReview reports a pull request that is not reviewed, and why.
func skipReview(logger *slog.Logger, output string, number int, reason string) error {
	logger.Info("prReviewSkipped", "pr", number, "reason", reason)
	return writeOutputs(output, map[string]string{"skip": "true", "skip_reason": reason})
}

// readIssue reads issue id with a read-scoped token minted from the review
// app's client credentials, revoking it before returning.
func readIssue(ctx context.Context, logger *slog.Logger, project string, client *http.Client, id string) (linear.Issue, error) {
	if project == "" {
		return linear.Issue{}, errors.New("missing environment: GOOGLE_CLOUD_PROJECT")
	}
	secretsClient, err := secretmanager.NewClient(ctx)
	if err != nil {
		return linear.Issue{}, fmt.Errorf("secret manager client: %w", err)
	}
	defer func() { _ = secretsClient.Close() }()
	return readIssueAs(ctx, logger, dispatcher.NewSecrets(project, secretsClient).Token, client, id)
}

// readIssueAs is readIssue with read reading the review app's secrets.
func readIssueAs(ctx context.Context, logger *slog.Logger, read func(context.Context, string) (string, error), client *http.Client, id string) (linear.Issue, error) {
	creds, err := linear.ReadCredentials(ctx, linear.ReviewApp, read, client)
	if err != nil {
		return linear.Issue{}, err
	}
	creds.Scope = linear.ReadScope
	return issueWith(ctx, logger, linear.NewPollSource(creds), client, "", id)
}

// issueWith reads issue id with tokens from src, revoking them before returning.
func issueWith(ctx context.Context, logger *slog.Logger, src *linear.PollSource, client *http.Client, endpoint, id string) (linear.Issue, error) {
	defer linear.RevokeAll(ctx, src, logger)
	var issue linear.Issue
	err := linear.DoRefreshing(ctx, src, func(token string) error {
		var err error
		issue, err = linear.Client{Endpoint: endpoint, Token: token, HTTP: client}.Issue(ctx, id)
		return err
	})
	return issue, err
}

// prReview reviews the context file on each model of the list in turn and
// writes the verdict. A review no model completes is a no-verdict verdict, not
// a failure; it needs only the agent binary and its key.
func prReview(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("pr-review", flag.ContinueOnError)
	contextPath := fs.String("context", "", "the review context pr-review-context wrote")
	out := fs.String("out", "", "path to write the verdict to")
	models := fs.String("models", "", "the review models to try in order, comma-separated provider/model")
	timeout := fs.Duration("model-timeout", defaultPRReviewTimeout, "how long each model's review may run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *contextPath == "" || *out == "" || strings.TrimSpace(*models) == "" || *timeout <= 0 {
		return errors.New("pr-review needs -context, -out, -models and a positive -model-timeout")
	}
	list := splitModels(*models, nil)
	if err := runner.ValidateReviewModels(list, runner.DefaultModel()); err != nil {
		return err
	}
	agent := runner.NewRouter(runner.ProfileReview, runner.Harnesses(getenv)...)
	return runPRReview(ctx, logger, agent, list, *timeout, *contextPath, *out, getenv("GITHUB_STEP_SUMMARY"), runner.HarnessSecrets(getenv))
}

// runPRReview is prReview's review of the context at contextPath, with the
// verdict written to out and its footer to summaryPath when that is set.
func runPRReview(ctx context.Context, logger *slog.Logger, agent runner.Agent, models []string, timeout time.Duration, contextPath, out, summaryPath string, secrets []string) error {
	raw, err := readCapped(contextPath)
	if err != nil {
		return err
	}
	c, err := runner.ParsePRReviewContext(raw)
	if err != nil {
		return err
	}
	v, err := runner.ReviewPR(ctx, agent, models, timeout, c, secrets)
	if err != nil {
		return err
	}
	for _, a := range v.Attempts {
		logger.Info("prReviewAttempt", "pr", c.PR, "model", a.Model, "outcome", a.Outcome, "detail", a.Detail)
	}
	logger.Info("prReviewVerdict", "pr", c.PR, "outcome", v.Outcome, "model", v.Model, "cost", v.Usage.Cost)
	encoded, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, encoded, 0o600); err != nil {
		return fmt.Errorf("writing the verdict: %w", err)
	}
	if summaryPath == "" {
		return nil
	}
	return appendFile(summaryPath, "Wingman review: "+v.Outcome+". "+verdictFooter(v)+"\n")
}

// readCapped reads at most maxPRReviewArtifactBytes of the file at path,
// leaving a longer file for its parser to refuse.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxPRReviewArtifactBytes+1))
}

// prReviewPost publishes a review: one marked comment, created or edited, and
// the wingman-review status on the reviewed sha. It writes merge=true only when
// the review is clean, the pull request is labelled eligible, open, not a
// draft, and its head is still the reviewed sha.
func prReviewPost(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("pr-review-post", flag.ContinueOnError)
	number := fs.Int("pr", 0, "the reviewed pull request")
	sha := fs.String("reviewed-sha", "", "the head sha the review read")
	verdictPath := fs.String("verdict", "", "the verdict pr-review wrote; absent when the review did not finish")
	skipReason := fs.String("skip-reason", "", "why pr-review-context skipped the review, empty when it did not")
	contextResult := fs.String("context-result", "", "the result of the context job")
	runURL := fs.String("run-url", "", "URL of the workflow run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *number <= 0 || !shaPattern.MatchString(*sha) {
		return errors.New("pr-review-post needs -pr and a -reviewed-sha")
	}
	if !runURLPattern.MatchString(*runURL) {
		*runURL = ""
	}
	v, verdictErr := readVerdict(*verdictPath)
	if verdictErr != nil {
		logger.Warn("prReviewVerdictRejected", "pr", *number, "err", verdictErr)
	}
	post := planPost(*skipReason, *contextResult, v, verdictErr, *sha, *runURL)
	gh := pullRequests{API: getenv("GITHUB_API_URL"), Token: getenv("GH_TOKEN"), Repo: getenv("GITHUB_REPOSITORY"),
		Client: &http.Client{Timeout: prReviewRequestTimeout}}
	if post.comment != "" {
		if err := upsertComment(ctx, gh, *number, post.comment); err != nil {
			return err
		}
	}
	if err := gh.setStatus(ctx, *sha, post.status); err != nil {
		return err
	}
	merge := false
	if post.clean {
		p, err := gh.pull(ctx, *number)
		if err != nil {
			return err
		}
		merge = mergeReady(p, *sha)
	}
	logger.Info("prReviewPosted", "pr", *number, "state", post.status.State, "merge", merge)
	return writeOutputs(getenv("GITHUB_OUTPUT"), map[string]string{"merge": strconv.FormatBool(merge)})
}

// readVerdict reads and validates the verdict at path.
func readVerdict(path string) (runner.PRVerdict, error) {
	if path == "" {
		return runner.PRVerdict{}, errors.New("no verdict")
	}
	raw, err := readCapped(path)
	if err != nil {
		return runner.PRVerdict{}, err
	}
	return runner.ParsePRVerdict(raw)
}

// commitStatus is one commit status as GitHub's statuses API takes it.
type commitStatus struct {
	State       string `json:"state"`
	Description string `json:"description"`
	Context     string `json:"context"`
	TargetURL   string `json:"target_url,omitempty"`
}

// postPlan is what pr-review-post publishes: a comment (empty for none), the
// status, and whether the review was clean.
type postPlan struct {
	comment string
	status  commitStatus
	clean   bool
}

// planPost decides what a review publishes. A skipped or failed context sets
// an error status and no comment; an unreadable or absent verdict is no verdict.
func planPost(skipReason, contextResult string, v runner.PRVerdict, verdictErr error, sha, runURL string) postPlan {
	status := func(state, description string) commitStatus {
		return commitStatus{State: state, Description: truncateRunes(description, maxStatusDescription), Context: prReviewStatusContext, TargetURL: runURL}
	}
	if skipReason != "" {
		description, ok := skipDescriptions[skipReason]
		if !ok {
			description = "Not reviewed: the review context was unreadable"
		}
		return postPlan{status: status("error", description)}
	}
	if contextResult != "success" {
		return postPlan{status: status("error", "Not reviewed: the pull request or its ticket could not be read")}
	}
	if verdictErr != nil {
		v = runner.PRVerdict{Outcome: runner.PRReviewNoVerdict}
	}
	comment := renderReviewComment(v, sha, runURL)
	switch v.Outcome {
	case runner.PRReviewClean:
		return postPlan{comment: comment, status: status("success", "Clean: no findings from "+v.Model), clean: true}
	case runner.PRReviewFindings:
		return postPlan{comment: comment, status: status("failure", "Findings from "+v.Model+"; see the review comment")}
	default:
		return postPlan{comment: comment, status: status("failure", "No verdict: every review model failed; waits for the owner")}
	}
}

// mergeReady reports whether a clean review may request auto-merge on p.
func mergeReady(p pull, reviewedSHA string) bool {
	return p.State == "open" && !p.Draft && p.hasLabel(autoMergeEligibleLabel) && p.Head.SHA == reviewedSHA
}

// upsertComment edits the marked comment this workflow posted before, or creates one.
func upsertComment(ctx context.Context, gh pullRequests, number int, body string) error {
	comments, err := gh.comments(ctx, number)
	if err != nil {
		return err
	}
	for _, c := range comments {
		if c.User.Login == prReviewCommentAuthor && strings.HasPrefix(c.Body, prReviewMarker) {
			return gh.editComment(ctx, c.ID, body)
		}
	}
	return gh.createComment(ctx, number, body)
}

// renderReviewComment is the review comment: the marker, the outcome, the
// findings in a code fence, where GitHub renders no HTML, markdown or mention,
// and a footer naming the model and its cost.
func renderReviewComment(v runner.PRVerdict, sha, runURL string) string {
	var b strings.Builder
	b.WriteString(prReviewMarker + "\n")
	switch v.Outcome {
	case runner.PRReviewClean:
		fmt.Fprintf(&b, "### Wingman review: clean\n\nNo findings against the ticket's acceptance criteria at `%s`.\n", sha[:12])
	case runner.PRReviewFindings:
		fmt.Fprintf(&b, "### Wingman review: findings\n\nAt `%s`:\n\n%s\n", sha[:12], fenced(neutralise(v.Findings)))
	default:
		fmt.Fprintf(&b, "### Wingman review: no verdict\n\nNo review model returned a verdict at `%s`, so this pull request waits for the owner.\n", sha[:12])
		for _, a := range v.Attempts {
			fmt.Fprintf(&b, "\n- `%s`: %s", a.Model, a.Outcome)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n<sub>" + verdictFooter(v))
	if runURL != "" {
		b.WriteString(" · [run](" + runURL + ")")
	}
	b.WriteString("</sub>\n")
	return b.String()
}

// verdictFooter names the model that reviewed, its tokens and the cost of every attempt.
func verdictFooter(v runner.PRVerdict) string {
	cost := 0.0
	for _, a := range v.Attempts {
		cost += a.Cost
	}
	model := "none"
	if v.Model != "" {
		model = "`" + v.Model + "`"
	}
	return fmt.Sprintf("Model %s · %d in / %d out tokens · $%s across %d attempt(s)",
		model, v.Usage.Input, v.Usage.Output, strconv.FormatFloat(cost, 'f', -1, 64), len(v.Attempts))
}

// neutralise breaks every @mention, so a finding cannot notify anyone.
func neutralise(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "@", "@\u200b")
}

// fenced puts s in a code fence longer than any run of backticks it holds.
func fenced(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + "text\n" + s + "\n" + fence
}

// truncateRunes caps s at n bytes on a character boundary.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
