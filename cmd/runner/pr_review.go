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

	"cloud.google.com/go/firestore"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/linear"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
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
	// reviewSHALen is how much of a head sha a review comment names.
	reviewSHALen = 12
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

// The descriptions a review posts with its status. fix-await classifies a status
// by their prefixes, so the poster and the reader share the words rather than
// each holding its own copy. A status opens a sentence, so the no-verdict cause
// is capitalised there — "No verdict: allowance exhausted" — while the review
// comment's heading carries the same words lower-case.
const (
	cleanStatusDescription    = "Clean: no findings from "
	findingsStatusDescription = "Findings from "
	// noVerdictStatusPrefix opens both no-verdict descriptions, which differ in
	// the cause they name and not in what they ask of the owner.
	noVerdictStatusPrefix               = "No verdict: "
	noVerdictAllowanceStatusDescription = noVerdictStatusPrefix + "allowance exhausted; waits for the owner"
	noVerdictFailedStatusDescription    = noVerdictStatusPrefix + "failed; waits for the owner"
	// noVerdictAllowanceMergeStatusDescription is the same cause once the review
	// left the merge to the workflow, so the status never says it waits while the
	// pull request is set to merge.
	noVerdictAllowanceMergeStatusDescription = noVerdictStatusPrefix + "allowance exhausted; merges without review"
)

// errReviewAppNoTeam fails a review whose ticket reads as missing because the
// review app's Linear user has lost its team, so it is not skipped as absent.
var errReviewAppNoTeam = errors.New("reading the ticket: the review app cannot see the team; is its Linear user suspended? Recover it as infra/README.md's \"Pull request review\" section says")

var (
	shaPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	runURLPattern = regexp.MustCompile(`^https://[A-Za-z0-9./_-]+$`)
	// reviewAtLine is the line a findings comment names its head on, and
	// reviewFenceLine the fence renderReviewComment opens the findings with.
	reviewAtLine    = regexp.MustCompile("^At `([0-9a-f]{" + strconv.Itoa(reviewSHALen) + "})`:$")
	reviewFenceLine = regexp.MustCompile("^(`{3,})text$")
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
	if errors.Is(err, linear.ErrNoTeam) {
		return errReviewAppNoTeam
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
	agentImage := fs.String("agent-image", "", agentImageUsage)
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
	_, err := withAgents(ctx, logger, agentSettings{Image: *agentImage}, ".", runner.Harnesses(getenv),
		func(contained []runner.Harness, _ func(context.Context, string) error) error {
			// No harness opt-in: a PR review runs unattended, so it admits no
			// harness that only the owner's own commands may opt in.
			agent := runner.NewRouter(runner.ProfileReview, contained...)
			return runPRReview(ctx, logger, agent, list, *timeout, *contextPath, *out, getenv("GITHUB_STEP_SUMMARY"), runner.HarnessSecrets(getenv))
		})
	return err
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

// withStore runs fn with a Firestore client for the job's project, opened by open
// for the one read and closed with it.
func withStore(ctx context.Context, getenv func(string) string, open func(context.Context, string) (*firestore.Client, error), fn func(*firestore.Client) error) error {
	project := getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		return errors.New("missing environment: GOOGLE_CLOUD_PROJECT")
	}
	fsc, err := open(ctx, project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	return fn(fsc)
}

// openProjectStore is firestore.NewClient in the fixed-arity shape withStore's seam
// takes, its options dropped: every read here opens a client without one.
func openProjectStore(ctx context.Context, project string) (*firestore.Client, error) {
	return firestore.NewClient(ctx, project)
}

// autoMergeSettings reads the owner's auto-merge settings.
type autoMergeSettings func(ctx context.Context) (store.AutoMergeSetting, error)

// storedAutoMergeSettings reads the settings from Firestore with the job's own
// credentials, opening the client for the one read: only a review that would
// otherwise request the merge gets here.
func storedAutoMergeSettings(getenv func(string) string) autoMergeSettings {
	return func(ctx context.Context) (store.AutoMergeSetting, error) {
		var setting store.AutoMergeSetting
		err := withStore(ctx, getenv, openProjectStore, func(fsc *firestore.Client) error {
			var err error
			setting, err = store.NewAutoMergeSwitches(fsc).Read(ctx)
			return err
		})
		return setting, err
	}
}

// rewriteRate reads the trailing rewrite rate over the PRs a human merged.
type rewriteRate func(ctx context.Context) dispatcher.AutoMergeDecision

// storedRewriteRate reads the rate from Firestore with the job's own credentials,
// opening the client for the one read: only an allowance-exhausted review of an
// eligible PR whose checks pass gets here.
func storedRewriteRate(logger *slog.Logger, getenv func(string) string) rewriteRate {
	return func(ctx context.Context) dispatcher.AutoMergeDecision {
		var d dispatcher.AutoMergeDecision
		err := withStore(ctx, getenv, openProjectStore, func(fsc *firestore.Client) error {
			var err error
			d, err = autoMergeRateOf(ctx, store.NewRewrites(fsc))
			return err
		})
		if err != nil {
			logger.Warn("prReviewRateUnread", "detail", err.Error())
			return dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonEvidenceUnread}
		}
		return d
	}
}

// settingsGate is what the owner's auto-merge settings hold a merge on, in the
// shape the review comment and the decline log both read: the zero note when the
// switch is on and no escaped PR holds it. Settings that cannot be read hold:
// eligibility was decided at pr-meta, and this is the last look before the
// request.
func settingsGate(setting store.AutoMergeSetting, err error) mergeNote {
	switch {
	case err != nil:
		return mergeNote{Reason: settingsUnreadReason, Words: "the owner's auto-merge settings could not be read", Detail: err.Error()}
	case !setting.Enabled:
		return mergeNote{Reason: dispatcher.AutoMergeReasonSwitchOff, Words: "the owner's auto-merge switch is off"}
	case setting.Escaped.PR > 0:
		return mergeNote{Reason: dispatcher.AutoMergeReasonEscaped, Words: setting.Escaped.Words()}
	}
	return mergeNote{}
}

// logDetail is what a decline logs: the evidence behind it, or its words when
// there is none.
func (n mergeNote) logDetail() string {
	if n.Detail != "" {
		return n.Detail
	}
	return n.Words
}

// The decline reasons pr-review-post logs for a gate of its own; the others are
// the dispatcher's auto-merge tokens.
const (
	settingsUnreadReason = "settings-unread"
	pullUnreadReason     = "pull-unread"
	notEligibleReason    = "not-eligible"
	checksUnreadReason   = "checks-unread"
	checksNotGreenReason = "checks-not-green"
)

// mergeChecks are the checks a merge must see green before an
// allowance-exhausted review requests it: the pair branch protection requires
// on main. `.github/workflows/pr-review.yml`'s auto-merge step guards the same
// two contexts before it requests the merge, so a rename here is half a change.
var mergeChecks = []string{"go", "web"}

// mergeNote is what an allowance-exhausted review decided about the merge, and
// what its comment says about it: whether the request was made, and the gate
// that held it when it was not.
type mergeNote struct {
	// Requested is set when auto-merge was requested at the reviewed head.
	Requested bool
	// Reason is the holding gate's stable token, empty when the request was made.
	Reason string
	// Words is that gate for the review comment, empty when the request was made.
	Words string
	// Detail is the evidence behind the refusal for the log line, empty when none holds.
	Detail string
}

// cleanMerge reports whether a clean review requests the merge: the pull request
// is eligible at the reviewed head, and the owner's switch is still on with no
// escaped PR holding it. A pull request that cannot be read requests nothing, so
// the review still posts.
func cleanMerge(ctx context.Context, logger *slog.Logger, gh pullRequests, number int, reviewedSHA string, read autoMergeSettings) bool {
	p, err := gh.pull(ctx, number)
	if err != nil {
		logger.Warn("prReviewMergeDeclined", "pr", number, "reason", pullUnreadReason, "detail", err.Error())
		return false
	}
	if !mergeReady(p, reviewedSHA) {
		return false
	}
	// The eligibility label was set when the PR opened, and the review took
	// minutes: a switch turned off or an escape latched since must still stop it.
	readCtx, cancel := context.WithTimeout(ctx, prReviewRequestTimeout)
	setting, err := read(readCtx)
	cancel()
	if hold := settingsGate(setting, err); hold.Reason != "" {
		logger.Warn("prReviewMergeDeclined", "pr", number, "reason", hold.Reason, "detail", hold.logDetail())
		return false
	}
	return true
}

// exhaustedMerge decides whether a review whose every model reported an
// exhausted allowance requests the merge: the owner's switch is on with no
// escaped PR, the pull request is eligible at the reviewed head, its go and web
// checks pass there, and the trailing rewrite rate is measured and at or under
// the guideline. Every refusal names the gate that held it, in the words the
// review comment carries; a rate that cannot be measured holds, because merging
// without a review is the one case the rate must not be guessed at. A check read
// that fails or finds no result is read as not green, and a pull request that
// cannot be read requests nothing so the review still posts.
//
// The settings come first even though eligibility is also decided at pr-meta: a
// switch turned off or an escape latched before the PR opened leaves no label, so
// reading the label first would name the missing label and not the cause.
func exhaustedMerge(ctx context.Context, gh pullRequests, number int, reviewedSHA string, read autoMergeSettings, rate rewriteRate) mergeNote {
	p, err := gh.pull(ctx, number)
	if err != nil {
		return mergeNote{Reason: pullUnreadReason, Words: "the pull request could not be read", Detail: err.Error()}
	}
	setting, err := read(ctx)
	if hold := settingsGate(setting, err); hold.Reason != "" {
		return hold
	}
	if !mergeReady(p, reviewedSHA) {
		return mergeNote{Reason: notEligibleReason, Words: "the pull request is not eligible for auto-merge"}
	}
	state, err := dispatcher.GitHub{Endpoint: gh.API, Token: gh.Token, Client: gh.Client}.
		ChecksOutcome(ctx, gh.Repo, reviewedSHA, mergeChecks)
	switch {
	case err != nil:
		return mergeNote{Reason: checksUnreadReason, Words: "the `go` and `web` check results could not be read", Detail: err.Error()}
	case state != dispatcher.CheckGreen:
		return mergeNote{Reason: checksNotGreenReason, Words: "the `go` and `web` checks are not both green at this head"}
	}
	if d := rate(ctx); !d.WithinGuideline {
		return mergeNote{Reason: d.Reason, Words: rateWords(d)}
	}
	return mergeNote{Requested: true}
}

// prReviewPost publishes a review: one marked comment, created or edited, and
// the wingman-review status on the reviewed sha. It writes merge=true when the
// review is clean, or its every model reported an exhausted allowance, and the
// pull request is a run's, labelled eligible, open, not a draft, its head is
// still the reviewed sha; the clean review then needs the owner's switch still
// on with no escaped PR holding it, and the exhausted one its go and web checks
// green and a rewrite rate measured and at or under the guideline.
func prReviewPost(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	return prReviewPostWith(ctx, logger, getenv, storedAutoMergeSettings(getenv), storedRewriteRate(logger, getenv), args)
}

// prReviewPostWith is prReviewPost with settings and rate reading the owner's
// auto-merge evidence.
func prReviewPostWith(ctx context.Context, logger *slog.Logger, getenv func(string) string, settings autoMergeSettings, rate rewriteRate, args []string) error {
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
	merge := false
	switch {
	case post.clean:
		merge = cleanMerge(ctx, logger, gh, *number, *sha, settings)
	case post.exhausted:
		// The comment and the status both state the decision, so the decision comes
		// first: an allowance-exhausted review merges only where every gate allows it.
		// One prReviewRequestTimeout window covers the decision's four reads — the pull
		// request, the settings, the check runs and the rate, each a round trip, and the
		// HTTP ones bounded again by the client's own timeout. A step that cannot
		// finish inside it fails, which is a refusal, never a merge.
		readCtx, cancel := context.WithTimeout(ctx, prReviewRequestTimeout)
		note := exhaustedMerge(readCtx, gh, *number, *sha, settings, rate)
		cancel()
		merge = note.Requested
		if !merge {
			logger.Warn("prReviewMergeDeclined", "pr", *number, "reason", note.Reason, "detail", note.logDetail())
		}
		post.comment = renderReviewComment(v, *sha, *runURL, note)
		post.status.Description = noVerdictAllowanceStatusDescription
		if merge {
			post.status.Description = noVerdictAllowanceMergeStatusDescription
		}
	}
	if post.comment != "" {
		if err := upsertComment(ctx, gh, *number, post.comment); err != nil {
			return err
		}
	}
	if err := gh.setStatus(ctx, *sha, post.status); err != nil {
		return err
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
// status, whether the review was clean, and whether it ended on an exhausted
// allowance, which decides a merge of its own.
type postPlan struct {
	comment   string
	status    commitStatus
	clean     bool
	exhausted bool
}

// planPost decides what a review publishes. A skipped or failed context sets
// an error status and no comment; an unreadable or absent verdict is no verdict
// that failed, and a verdict whose every model reported an exhausted allowance
// is no verdict that says so.
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
	comment := renderReviewComment(v, sha, runURL, mergeNote{})
	switch v.Outcome {
	case runner.PRReviewClean:
		return postPlan{comment: comment, status: status("success", cleanStatusDescription+v.Model), clean: true}
	case runner.PRReviewFindings:
		return postPlan{comment: comment, status: status("failure", findingsStatusDescription+v.Model+"; see the review comment")}
	default:
		if v.AllowanceExhausted() {
			return postPlan{comment: comment, status: status("failure", noVerdictAllowanceStatusDescription), exhausted: true}
		}
		return postPlan{comment: comment, status: status("failure", noVerdictFailedStatusDescription)}
	}
}

// mergeReady reports whether a review may request auto-merge on p: a run's pull
// request, still open, labelled eligible by pr-meta, not a draft, and at the head
// this review read.
func mergeReady(p pull, reviewedSHA string) bool {
	return p.State == "open" && !p.Draft && strings.HasPrefix(p.Head.Ref, runner.BranchPrefix) &&
		p.hasLabel(autoMergeEligibleLabel) && p.Head.SHA == reviewedSHA
}

// markedComment is the review comment this workflow posted: the first comment by
// its own author carrying the marker, which is the one upsertComment edits and
// fix-await reads, so both sides look at the same comment.
func markedComment(comments []comment) (comment, bool) {
	for _, c := range comments {
		if c.User.Login == prReviewCommentAuthor && strings.HasPrefix(c.Body, prReviewMarker) {
			return c, true
		}
	}
	return comment{}, false
}

// upsertComment edits the marked comment this workflow posted before, or creates one.
func upsertComment(ctx context.Context, gh pullRequests, number int, body string) error {
	comments, err := gh.comments(ctx, number)
	if err != nil {
		return err
	}
	if c, ok := markedComment(comments); ok {
		return gh.editComment(ctx, c.ID, body)
	}
	return gh.createComment(ctx, number, body)
}

// renderReviewComment is the review comment: the marker, the outcome, the
// findings in a code fence, where GitHub renders no HTML, markdown or mention,
// and a footer naming the model and its cost. note states what an
// allowance-exhausted review decided about the merge; it is the zero note for
// every other review, which decides none.
func renderReviewComment(v runner.PRVerdict, sha, runURL string, note mergeNote) string {
	var b strings.Builder
	b.WriteString(prReviewMarker + "\n")
	switch v.Outcome {
	case runner.PRReviewClean:
		fmt.Fprintf(&b, "### Wingman review: clean\n\nNo findings against the ticket's acceptance criteria at `%s`.\n", shortSHA(sha))
	case runner.PRReviewFindings:
		fmt.Fprintf(&b, "### Wingman review: findings\n\nAt `%s`:\n\n%s\n", shortSHA(sha), fenced(neutralise(v.Findings)))
	default:
		heading, opening := "no verdict: failed", "No review model returned a verdict at `"+shortSHA(sha)+"`, so this pull request waits for the owner."
		if v.AllowanceExhausted() {
			heading = "no verdict: allowance exhausted"
			opening = "No review model returned a verdict at `" + shortSHA(sha) + "`: every one reported its plan's allowance spent."
			switch {
			case note.Requested:
				opening += " This pull request is to be merged without review, because the `go` and `web` checks pass at this head."
			case note.Words != "":
				opening += " Auto-merge is not requested, so this pull request waits for the owner: " + note.Words + "."
			}
		}
		fmt.Fprintf(&b, "### Wingman review: %s\n\n%s\n", heading, opening)
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

// reviewComment is what a posted review comment says: the head its At line names
// — empty when it names none, as a clean or no-verdict comment does — and the
// findings its fenced block holds, with the mention neutralisation reversed.
type reviewComment struct {
	SHA      string
	Findings string
}

// shortSHA is the head a review comment names, as renderReviewComment writes it.
func shortSHA(sha string) string {
	if len(sha) <= reviewSHALen {
		return sha
	}
	return sha[:reviewSHALen]
}

// parseReviewComment reads a comment renderReviewComment wrote: the head its At
// line names and the findings block under it, un-neutralised so a finding reads
// as the review wrote it. A comment naming no head reports no findings, which is
// what a clean or no-verdict comment says. It reports false when the findings
// block is unreadable — opened with no closing fence — and still names the head
// it read, so a caller can tell another head's comment from this one's.
func parseReviewComment(body string) (reviewComment, bool) {
	lines := strings.Split(body, "\n")
	at := -1
	var c reviewComment
	for i, line := range lines {
		if m := reviewAtLine.FindStringSubmatch(line); m != nil {
			at, c.SHA = i, m[1]
			break
		}
	}
	if at < 0 {
		return reviewComment{}, true
	}
	open, fence := -1, ""
	for i := at + 1; i < len(lines); i++ {
		if m := reviewFenceLine.FindStringSubmatch(lines[i]); m != nil {
			open, fence = i, m[1]
			break
		}
	}
	if open < 0 {
		return c, false
	}
	for i := open + 1; i < len(lines); i++ {
		if lines[i] == fence {
			c.Findings = unneutralise(strings.Join(lines[open+1:i], "\n"))
			return c, true
		}
	}
	return c, false
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

// unneutralise restores the mentions neutralise broke, so a finding read back
// from a comment is the one the review wrote.
func unneutralise(s string) string {
	return strings.ReplaceAll(s, "@\u200b", "@")
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
