// Command runner executes one run inside a repository's GitHub Actions workflow.
//
//	runner ticket          -run-id <id> -out <path> write the run record's ticket to a file for the model job
//	runner build           -model <provider/model>  fetch the projection, run the agent, bundle the branch, emit a summary
//	runner plan-stage      -ticket-file <path> -out <path>  run the plan phase alone and write its file list for the record job
//	runner record-plan-stage -run-id <id> -plan-file <path>  re-check the plan, record it, settle it and wake the dispatcher
//	runner pr-meta         -run-id <id> ...          render the PR's title and body from the run record
//	runner record          -run-id <id> -summary ... validate the build's summary and merge it into the run record
//	runner enable-provider -provider <name>          admit a halted provider back to dispatch (AC5)
//	runner reset-breaker                             clear the breaker a systemic stop tripped, admitting dispatch again
//	runner plan-define     -provider <name> ...      record a provider plan's price, billing, limit behaviour, pages and harnesses
//	runner plan-optin      -provider <name>          record the owner's consent to a per-token provider's spend, or with -private to private repos on its free tier
//	runner plan-verdict    -provider <name> ...      record the verdict on a plan's terms with its wording and source
//	runner plan-reply      -provider <name> ...      record a vendor reply and the verdict it leads to
//	runner plan-list                                 print every plan side by side, flagging the ones to look at
//	runner plan-smoke      -plan-models <list>       prove the plan agent refuses edit, a new file and bash on the list's first and last model
//	runner review-smoke    -review-models <list>     try the review agent on a fixture diff with each model, passing those whose findings parse at $0
//	runner pr-review-context -pr <n> -out <path>     write a pull request's diff and the ticket it names, read from Linear
//	runner pr-review       -context <path> ...       review the context on each model in turn and write the verdict
//	runner pr-review-post  -pr <n> -verdict <path>   post the review comment and status, and decide the auto-merge request
//
// build exits 0 when it stops short of a branch but reported why; ticket, pr-meta
// and record exit 1 for any run that cannot or did not succeed, so the workflow
// stays red.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, logger, os.Args[1:], os.Getenv)
	stop()
	code := exitCode(err)
	var stopped *runner.StopError
	if code != 0 && !errors.As(err, &stopped) && !errors.Is(err, errRunFailed) {
		logger.Error("runnerFailed", "err", err)
	}
	os.Exit(code)
}

// errRunFailed is the result of ticket, plan-stage, record-plan-stage, pr-meta or
// record for a run that cannot or did not succeed.
var errRunFailed = errors.New("run did not succeed")

// exitCode is 0 for a build stop whose summary was reported, which the record
// job then judges, and 1 for every other error.
func exitCode(err error) int {
	var stopped *runner.StopError
	if err == nil || (errors.As(err, &stopped) && !errors.Is(err, runner.ErrSummaryUnreported)) {
		return 0
	}
	return 1
}

// env is the workflow context every subcommand reads.
type env struct {
	project   string
	runID     string
	attempt   uint64
	attemptID string
	tempDir   string
	output    string
	secrets   []string
	harnesses []runner.Harness
	identity  runner.Identity
}

// requiredEnv names the variables a subcommand cannot run without. Only the two
// that act on a workflow run itself need its identity and temp directory; the
// owner commands need the project alone, so they run from a terminal with no
// Actions environment.
func requiredEnv(command string) ([]string, bool) {
	switch command {
	case "ticket", "pr-meta", "enable-provider", "reset-breaker",
		"plan-define", "plan-optin", "plan-verdict", "plan-reply", "plan-list":
		return []string{"GOOGLE_CLOUD_PROJECT"}, true
	case "record", "record-plan-stage":
		return []string{"GOOGLE_CLOUD_PROJECT", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}, true
	case "build", "plan-stage":
		return []string{"GOOGLE_CLOUD_PROJECT", "RUNNER_TEMP", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}, true
	}
	return nil, false
}

func loadEnv(getenv func(string) string, required ...string) (env, error) {
	e := env{
		project:  getenv("GOOGLE_CLOUD_PROJECT"),
		tempDir:  getenv("RUNNER_TEMP"),
		output:   getenv("GITHUB_OUTPUT"),
		identity: runner.IdentityFromEnv(getenv),
	}
	e.secrets = runner.HarnessSecrets(getenv)
	e.harnesses = runner.Harnesses(getenv)
	var missing []string
	for _, name := range required {
		if getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return env{}, fmt.Errorf("missing environment: %v", missing)
	}
	attempt := getenv("GITHUB_RUN_ATTEMPT")
	if attempt != "" {
		n, err := parseAttempt(attempt)
		if err != nil {
			return env{}, fmt.Errorf("GITHUB_RUN_ATTEMPT: %w", err)
		}
		e.attempt = n
	}
	e.runID = getenv("GITHUB_RUN_ID")
	e.attemptID = e.runID + "-" + attempt
	return e, nil
}

func run(ctx context.Context, logger *slog.Logger, args []string, getenv func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: runner ticket|build|plan-stage|record-plan-stage|pr-meta|record|enable-provider|reset-breaker|plan-define|plan-verdict|plan-reply|plan-list|plan-smoke|review-smoke|pr-review-context|pr-review|pr-review-post [flags]")
	}
	if args[0] == "plan-smoke" {
		return planSmoke(ctx, logger, getenv, args[1:])
	}
	switch args[0] {
	case "review-smoke":
		return reviewSmoke(ctx, logger, getenv, args[1:])
	case "pr-review-context":
		return prReviewContext(ctx, logger, getenv, args[1:])
	case "pr-review":
		return prReview(ctx, logger, getenv, args[1:])
	case "pr-review-post":
		return prReviewPost(ctx, logger, getenv, args[1:])
	}
	required, known := requiredEnv(args[0])
	if !known {
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
	e, err := loadEnv(getenv, required...)
	if err != nil {
		if args[0] == "build" {
			return setupFailed(logger, getenv("GITHUB_OUTPUT"), err)
		}
		return err
	}
	switch args[0] {
	case "ticket":
		return ticket(ctx, logger, e, args[1:])
	case "build":
		return build(ctx, logger, e, args[1:])
	case "plan-stage":
		return planStage(ctx, logger, e, args[1:])
	case "record-plan-stage":
		return recordPlanStage(ctx, logger, e, args[1:])
	case "pr-meta":
		return prMeta(ctx, logger, e, args[1:])
	case "record":
		return record(ctx, logger, e, args[1:])
	case "enable-provider":
		return enableProvider(ctx, logger, e, args[1:])
	case "reset-breaker":
		return resetBreaker(ctx, logger, e, args[1:])
	case "plan-define":
		return planDefine(ctx, logger, e, args[1:])
	case "plan-optin":
		return planOptIn(ctx, logger, e, args[1:])
	case "plan-verdict":
		return planVerdict(ctx, logger, e, args[1:])
	case "plan-reply":
		return planReply(ctx, logger, e, args[1:])
	case "plan-list":
		return planList(ctx, logger, e, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// ticket writes the run record's ticket to the file -out names — carrying the
// plan stage's file list when one is recorded, so the build skips planning —
// and fails when the identity does not match, the record is missing or its
// ticket cannot be built.
func ticket(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("ticket", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	out := fs.String("out", "", "path to write the run's ticket to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	records := store.NewRecords(fsc)
	rec, err := runRecord(ctx, logger, records, *runID)
	if err != nil {
		return err
	}
	t := rec.Ticket()
	logger.Info("ticketRead", "run", *runID, "ticket", t.ID)
	if rec.LastResort && rec.LastResortModels.Build == "" {
		return fmt.Errorf("run %s was claimed on a free tier but names no free model", *runID)
	}
	plan, err := records.GetPlan(ctx, *runID)
	if err != nil {
		return err
	}
	t.PlanFiles = plan.Files
	return writeTicket(*out, e.output, t, rec.RunModels())
}

// writeTicket writes the ticket to path and the models it named to $GITHUB_OUTPUT.
// The ticket never travels as a job output: GitHub drops any output holding a
// value masked in this job, which is how a run reaches the model job empty, so
// only the model labels — never ticket text — are outputs.
func writeTicket(path, output string, t runner.Ticket, m runner.ModelLabels) error {
	if path == "" {
		return errors.New("no -out path to write the ticket to")
	}
	v, err := t.Encode()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
		return fmt.Errorf("writing the ticket: %w", err)
	}
	return writeOutputs(output, modelOutputs(m))
}

// modelOutputs are the models the ticket named, each empty when it named none,
// so a run.yml expression falls back to its own default.
func modelOutputs(m runner.ModelLabels) map[string]string {
	return map[string]string{
		"override_model":         m.Build,
		"override_review_models": m.Review,
		"override_plan_models":   strings.Join(m.Plan, ","),
	}
}

// errTicketNotDelivered reports no ticket file where the build expected one: the
// hand-off a masked job output silently dropped, or an artifact that never came.
var errTicketNotDelivered = errors.New("ticket not delivered")

// readTicket reads the ticket the ticket job wrote to path. An absent or empty
// file is errTicketNotDelivered, a hand-off failure distinct from a file that is
// present but does not decode, which is runner.ErrTicketInvalid.
func readTicket(path string) (runner.Ticket, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return runner.Ticket{}, fmt.Errorf("%w: %w", errTicketNotDelivered, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return runner.Ticket{}, fmt.Errorf("%w: %s is empty", errTicketNotDelivered, path)
	}
	return runner.ParseTicket(string(raw))
}

func build(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	model := fs.String("model", "", "model, as provider/model")
	planModels := fs.String("plan-models", runner.DefaultPlanModel(), "the plan phase's models in order, comma-separated provider/model; later ones are backups")
	reviewModels := fs.String("review-models", strings.Join(runner.DefaultReviewModels(), ","), "the review phase's models in order, comma-separated provider/model; later ones are backups (FR-14)")
	pointer := fs.String("pointer", runner.DefaultPointer, "object naming the current rule-stack sha")
	ticketFile := fs.String("ticket-file", "", "path to the run's ticket, as the ticket subcommand wrote it")
	checksImage := fs.String("checks-image", "", "Go image, pinned by digest, to run the pre-PR checks in; empty runs them on the host")
	if err := fs.Parse(args); err != nil {
		return setupFailed(logger, e.output, err)
	}
	const repo = "."
	checks, err := checkRunner(ctx, *checksImage, e.tempDir, repo)
	if err != nil {
		return setupFailed(logger, e.output, err)
	}
	if err := writeOutputs(e.output, map[string]string{"attempt_id": e.attemptID}); err != nil {
		return setupFailed(logger, e.output, err)
	}
	t, err := readTicket(*ticketFile)
	switch {
	case errors.Is(err, errTicketNotDelivered):
		return ticketNotDelivered(logger, e.output, err)
	case err != nil:
		logger.Warn("ticketRejected", "err", err.Error())
	}

	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return setupFailed(logger, e.output, fmt.Errorf("storage client: %w", err))
	}
	defer func() { _ = gcs.Close() }()

	logger = logger.With("attempt", e.attemptID, "ticket", t.ID)
	plan := splitModels(*planModels, []string{runner.DefaultPlanModel()})
	review := splitModels(*reviewModels, runner.DefaultReviewModels())
	res, err := runner.Build(ctx, runner.BuildDeps{
		Projections: store.NewBucket(gcs, e.project+"-projections"),
		Completions: store.NewBucket(gcs, e.project+"-completions"),
		Agent:       runner.NewRouter(runner.ProfileBuild, e.harnesses...),
		PlanAgent:   runner.NewRouter(runner.ProfilePlan, e.harnesses...),
		ReviewAgent: runner.NewRouter(runner.ProfileReview, e.harnesses...),
		Checks:      checks,
		Report:      func(s runner.Summary) error { return writeSummary(e.output, s) },
		Logger:      logger,
		Now:         time.Now,
	}, runner.BuildConfig{
		AttemptID:    e.attemptID,
		Repo:         repo,
		TempDir:      e.tempDir,
		Pointer:      *pointer,
		Model:        *model,
		PlanModels:   plan,
		ReviewModels: review,
		Secrets:      e.secrets,
		Identity:     e.identity,
		Ticket:       t,
	})
	if err != nil {
		return err
	}
	if err := writeOutputs(e.output, map[string]string{
		"branch":  res.Branch,
		"changed": strconv.FormatBool(res.Changed),
		"ready":   strconv.FormatBool(res.Ready),
	}); err != nil {
		return err
	}
	return writeMultilineOutput(e.output, "loop_detail", res.LoopDetail)
}

// checkRunner is the pre-PR loop's checks: in a container of image when one is
// given, with its module cache under tempDir and repo's git common directory
// resolved now, before any agent runs; else on the host.
func checkRunner(ctx context.Context, image, tempDir, repo string) (runner.CheckRunner, error) {
	if image == "" {
		return runner.RunChecks, nil
	}
	linter, err := exec.LookPath("golangci-lint")
	if err != nil {
		return nil, fmt.Errorf("checks linter: %w", err)
	}
	if linter, err = filepath.Abs(linter); err != nil {
		return nil, fmt.Errorf("checks linter: %w", err)
	}
	gitDir, err := runner.GitCommonDir(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("checks git common dir: %w", err)
	}
	c := runner.ContainerChecks{Image: image, ModCache: filepath.Join(tempDir, "checks-modcache"), Linter: linter,
		GitCommonDir: gitDir}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c.Run, nil
}

// splitModels reads a comma-separated model list, using a copy of fallback when
// the value is blank — what a cleared workflow input passes. A blank entry inside a list
// is kept so the validator reports it.
func splitModels(s string, fallback []string) []string {
	if strings.TrimSpace(s) == "" {
		return slices.Clone(fallback)
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// prMeta writes the PR's title, body, the branch segment the pushed branch must
// carry and whether to mark it eligible for auto-merge as outputs, from the run record's ticket
// and the build's summary.
func prMeta(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("pr-meta", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	attemptID := fs.String("attempt-id", e.attemptID, "workflow attempt the build ran in")
	summary := fs.String("summary", "", "the build's summary, as JSON")
	checkReport := fs.String("failed-gate", "", "the check job's failed_gate output")
	runURL := fs.String("run-url", "", "URL of the workflow run")
	loopDetail := fs.String("loop-detail", "", "the pre-PR loop's report of why the PR is a draft (FR-5)")
	template := fs.String("template", runner.DefaultPRTemplate, "pull request template to render")
	autoMergeSwitch := fs.String("auto-merge-switch", "", "the WINGMAN_AUTO_MERGE variable; auto-merge is off unless it is \"on\"")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	tmpl, err := os.ReadFile(*template)
	if err != nil {
		return fmt.Errorf("reading the PR template: %w", err)
	}
	rec, err := readRunRecord(ctx, logger, e.project, *runID)
	if err != nil {
		return err
	}
	sum := buildSummary(logger, *summary, buildAttempt(logger, *attemptID, e), rec.Ticket(), time.Now())
	if err := refuseWorkflowPush(logger, sum); err != nil {
		return err
	}
	title, body, err := renderPR(string(tmpl), rec, sum, runner.FailedGate(*checkReport), *runURL, *loopDetail)
	if err != nil {
		return err
	}
	merge := autoMerge(*autoMergeSwitch, *runID, rec.Ticket(), sum, runner.FailedGate(*checkReport))
	logger.Info("autoMergeDecided", "run", *runID, "autoMerge", merge, "sampled", runner.Sampled(*runID))
	if err := writeOutputs(e.output, map[string]string{
		"title":          title,
		"branch_segment": rec.Ticket().BranchSegment(),
		"auto_merge":     strconv.FormatBool(merge),
	}); err != nil {
		return err
	}
	return writeMultilineOutput(e.output, "body", body)
}

// autoMergeOn is the only WINGMAN_AUTO_MERGE value that lets a run request auto-merge.
const autoMergeOn = "on"

// autoMerge reports whether the run's PR is marked eligible for auto-merge,
// which pr-review.yml requests once its review is clean: the switch is on and
// nothing in the run needs the owner.
func autoMerge(switchValue, runID string, t runner.Ticket, sum runner.Summary, failedGate string) bool {
	return switchValue == autoMergeOn && !ownerAttention(runID, t, sum, failedGate)
}

// ownerAttention reports whether the run's PR must wait for the owner rather than
// merge itself (FR-5, FR-17): not ready, sized L, in the review sample, edited
// outside the plan, touching a workflow, or failing a check gate.
func ownerAttention(runID string, t runner.Ticket, sum runner.Summary, failedGate string) bool {
	return !sum.Ready || (t.Size != "S" && t.Size != "M") || runner.Sampled(runID) ||
		len(sum.OutOfPlanFiles) > 0 || sum.StopReason == runner.StopWorkflowChange || failedGate != ""
}

// refuseWorkflowPush fails a build that stopped for touching a workflow file,
// so the pr job, which could not push it, never runs.
func refuseWorkflowPush(logger *slog.Logger, sum runner.Summary) error {
	if sum.StopReason != runner.StopWorkflowChange {
		return nil
	}
	logger.Warn("pushRefused", "reason", string(sum.StopReason), "detail", sum.StopDetail)
	return errRunFailed
}

// buildSummary is raw parsed as attemptID's build of t, or the zero Summary when
// raw is empty or rejected, so the PR falls back to the ticket rather than
// failing to open.
func buildSummary(logger *slog.Logger, raw, attemptID string, t runner.Ticket, now time.Time) runner.Summary {
	if raw == "" {
		return runner.Summary{}
	}
	sum, err := runner.ParseSummary(raw, attemptID, t, now)
	if err != nil {
		logger.Warn("summaryRejected", "err", err.Error())
		return runner.Summary{}
	}
	return sum
}

// renderPR is the PR's title and body for rec's ticket, taken from the build's
// summary: the record gains the build's fields only after the PR opens.
func renderPR(tmpl string, rec runner.Record, sum runner.Summary, failedGate, runURL, loopDetail string) (title, body string, err error) {
	t := rec.Ticket()
	body, err = runner.PRBody(tmpl, t, sum.PRSummary, failedGate, runURL, loopDetail, sum.OutOfPlanFiles)
	if err != nil {
		return "", "", err
	}
	return prTitle(sum, t), body, nil
}

// prTitle is the subject the build committed with, or the ticket's Subject for
// a summary that carries none.
func prTitle(sum runner.Summary, t runner.Ticket) string {
	if sum.CommitSubject != "" {
		return sum.CommitSubject
	}
	return t.Subject()
}

// buildAttempt is id when it names an attempt of this run, and the current
// attempt otherwise.
func buildAttempt(logger *slog.Logger, id string, e env) string {
	if id == "" {
		return e.attemptID
	}
	if !ownAttemptID(id, e.runID, e.attempt) {
		logger.Warn("attemptIDRejected", "length", len(id))
		return e.attemptID
	}
	return id
}

// readRunRecord reads run runID's record, logging why and failing the run when
// the record is missing or its ticket cannot be built.
func readRunRecord(ctx context.Context, logger *slog.Logger, project, runID string) (runner.Record, error) {
	fsc, err := recordsClient(ctx, project, runID)
	if err != nil {
		return runner.Record{}, err
	}
	defer func() { _ = fsc.Close() }()
	return runRecord(ctx, logger, store.NewRecords(fsc), runID)
}

func runRecord(ctx context.Context, logger *slog.Logger, r runner.RecordReader, runID string) (runner.Record, error) {
	rec, err := runner.ReadRun(ctx, r, runID)
	var stopped *runner.StopError
	if errors.As(err, &stopped) {
		logger.Error("ticketUnavailable", "run", runID, "reason", string(stopped.Reason),
			"err", stopped.Err.Error())
		return runner.Record{}, fmt.Errorf("%w: %s", errRunFailed, stopped.Reason)
	}
	if err != nil {
		return runner.Record{}, err
	}
	return rec, nil
}

func record(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to finalize")
	attemptID := fs.String("attempt-id", e.attemptID, "workflow attempt the build ran in")
	summary := fs.String("summary", "", "the build's summary, as JSON")
	unreadable := fs.Bool("summary-unreadable", false, "the summary could not be passed in")
	prURL := fs.String("pr-url", "", "URL of the opened PR")
	runResult := fs.String("run-result", "", "result of the run job")
	prResult := fs.String("pr-result", "", "result of the pr job")
	prStopReason := fs.String("pr-stop-reason", "", "the pr job's stop_reason output, empty when it pushed")
	prSeconds := fs.Int("pr-duration-s", 0, "wall-clock seconds the pr job took")
	checkReport := fs.String("failed-gate", "", "the check job's failed_gate output, empty when it did not run")
	runURL := fs.String("run-url", "", "URL of the workflow run")
	autoMerge := fs.Bool("auto-merge", false, "the pr job marked the PR eligible for auto-merge")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// started tells run.yml's fallback that the summary arrived, so a later failure
	// is not mistaken for one too large to pass in.
	if err := writeOutputs(e.output, map[string]string{"started": "true"}); err != nil {
		return err
	}
	*attemptID = buildAttempt(logger, *attemptID, e)

	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()

	records := store.NewRecords(fsc)
	rec, err := finalize(ctx, logger, records, store.NewQueue(fsc), records, store.NewNotices(fsc), store.NewBreaker(fsc), runner.FinalizeInput{
		RunID:             *runID,
		Identity:          e.identity,
		AttemptID:         *attemptID,
		Summary:           *summary,
		SummaryUnreadable: *unreadable,
		PRURL:             *prURL,
		RunResult:         *runResult,
		PRResult:          *prResult,
		PRStopReason:      *prStopReason,
		PRDuration:        time.Duration(*prSeconds) * time.Second,
		CheckReport:       *checkReport,
		RunURL:            *runURL,
		AutoMerge:         *autoMerge,
	}, time.Now())
	if err != nil {
		return err
	}
	if rec.StopReason == runner.StopSummaryInvalid {
		logger.Warn("summaryRejected", "run", *runID, "err", rec.StopDetail)
	}
	logger.Info("recordFinalized", "run", *runID, "attempt", *attemptID, "outcome", string(rec.Outcome),
		"reason", string(rec.StopReason), "failedGate", rec.FailedGate)
	if rec.Outcome == runner.OutcomeInfraFailure && rec.StopReason == runner.StopModelUnavailable && len(rec.Steps) > 0 {
		provider := runner.Provider(rec.Steps[len(rec.Steps)-1].Model)
		if perr := store.NewProviders(fsc).RecordInfraStop(ctx, provider, time.Now()); perr != nil {
			logger.Error("providerInfraStopNotRecorded", "run", *runID, "provider", provider, "err", perr.Error())
		}
	}
	if rec.Succeeded() && len(rec.Steps) > 0 {
		provider := runner.Provider(rec.Steps[len(rec.Steps)-1].Model)
		if perr := store.NewProviders(fsc).RecordSuccess(ctx, provider); perr != nil {
			logger.Error("providerSuccessNotRecorded", "run", *runID, "provider", provider, "err", perr.Error())
		}
	}
	if !rec.Succeeded() {
		return errRunFailed
	}
	return nil
}

// enableProvider clears a halted provider's stop count, admitting it to
// dispatch again (AC5) — a manual operator action, never automatic.
func enableProvider(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("enable-provider", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider to admit again")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if !providerPattern.MatchString(*provider) {
		return fmt.Errorf("provider %q is not a provider name", *provider)
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	if err := store.NewProviders(fsc).ClearHalt(ctx, *provider); err != nil {
		return err
	}
	logger.Info("providerEnabled", "provider", *provider)
	return nil
}

// noticeRaiser and breakerTripper are what record writes for a systemic stop.
type noticeRaiser interface {
	Raise(ctx context.Context, n dispatcher.Notice, at time.Time) error
}

type breakerTripper interface {
	Trip(ctx context.Context, runID, class string, at time.Time) error
}

// finalize writes the run's outcome into its record and raises its systemic
// notice, even when the record or ledger write then fails, since the stop is
// known by then.
func finalize(ctx context.Context, logger *slog.Logger, records runner.RecordStore, ledger runner.Ledger,
	reader runner.ProviderCostReader, notices noticeRaiser, breaker breakerTripper, in runner.FinalizeInput, at time.Time) (runner.Record, error) {
	rec, err := runner.Finalize(ctx, records, ledger, reader, in, at)
	raiseSystemic(ctx, logger, notices, breaker, rec, in.AttemptID, at)
	return rec, err
}

// attemptIDPattern is what a workflow attempt id looks like: run, then attempt.
var attemptIDPattern = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,10}$`)

// raiseSystemic raises the notice for a systemic stop, linked to the run that
// recorded it, and trips the breaker when its class does. The notice is keyed
// by run and attempt, so a later attempt after a reset raises its own; an
// attempt id that cannot name a document falls back to the run alone. Both
// writes create only what is absent, so a record job run twice raises and
// trips once.
func raiseSystemic(ctx context.Context, logger *slog.Logger, notices noticeRaiser, breaker breakerTripper, rec runner.Record, attemptID string, at time.Time) {
	notify, trips := rec.Systemic()
	if !notify {
		return
	}
	class := string(rec.StopReason)
	id := rec.RunID + "-" + attemptID
	if !attemptIDPattern.MatchString(attemptID) {
		logger.Error("noticeAttemptInvalid", "run", rec.RunID, "length", len(attemptID))
		id = rec.RunID
	}
	if err := notices.Raise(ctx, dispatcher.Notice{ID: id, Class: class, Link: rec.RunURL}, at); err != nil {
		logger.Error("noticeNotRaised", "run", rec.RunID, "class", class, "err", err.Error())
	}
	if !trips {
		return
	}
	if err := breaker.Trip(ctx, rec.RunID, class, at); err != nil {
		logger.Error("breakerNotTripped", "run", rec.RunID, "class", class, "err", err.Error())
		return
	}
	logger.Warn("breakerTripped", "run", rec.RunID, "class", class)
}

// resetBreaker clears the breaker a systemic stop tripped, admitting dispatch
// again — a manual operator action, never automatic. Its account check only
// guards against a run on the wrong account by mistake: Firestore IAM decides
// who may clear the breaker.
func resetBreaker(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("reset-breaker", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	if err := store.NewBreaker(fsc).Reset(ctx); err != nil {
		return err
	}
	logger.Info("breakerReset")
	return nil
}

// providerPattern is what runner.Provider extracts from a model string: the
// same character class modelPattern requires of a model's provider segment.
var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// recordsClient opens Firestore for a subcommand that reads or writes record runID.
func recordsClient(ctx context.Context, project, runID string) (*firestore.Client, error) {
	if !runner.ValidRunID(runID) {
		return nil, fmt.Errorf("run id %q cannot name a record", runID)
	}
	if project == "" {
		return nil, errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("firestore client: %w", err)
	}
	return fsc, nil
}

// setupFailed reports a build that could not start, so the record says why.
func setupFailed(logger *slog.Logger, output string, err error) error {
	return reportStop(logger, output, "setupFailed", runner.SetupSummary(err, time.Now()), err)
}

// ticketNotDelivered reports a build the ticket job handed no ticket, so the
// record blames the hand-off rather than the ticket.
func ticketNotDelivered(logger *slog.Logger, output string, err error) error {
	return reportStop(logger, output, "ticketNotDelivered",
		runner.StoppedSummary(runner.StopTicketNotDelivered, err, time.Now()), err)
}

// reportStop writes sum to $GITHUB_OUTPUT and returns the StopError it reports.
// The returned StopError exits 0 only when that report was written.
func reportStop(logger *slog.Logger, output, event string, sum runner.Summary, err error) error {
	logger.Error(event, "err", err)
	stopped := &runner.StopError{Outcome: sum.Outcome, Reason: sum.StopReason, Err: err}
	if output == "" {
		return errors.Join(stopped, runner.ErrSummaryUnreported)
	}
	if werr := writeSummary(output, sum); werr != nil {
		return errors.Join(stopped, fmt.Errorf("%w: %w", runner.ErrSummaryUnreported, werr))
	}
	return stopped
}

// ownAttemptID reports whether id names an attempt of this workflow run up to the current one.
func ownAttemptID(id, runID string, current uint64) bool {
	attempt, ok := strings.CutPrefix(id, runID+"-")
	if !ok {
		return false
	}
	n, err := parseAttempt(attempt)
	return err == nil && n <= current
}

// parseAttempt reads a run attempt: a positive decimal with no sign or leading zero.
func parseAttempt(s string) (uint64, error) {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return 0, fmt.Errorf("attempt %q is not a positive number", s)
	}
	return strconv.ParseUint(s, 10, 32)
}

// errNoOutput is a summary with nowhere to go: outside Actions nothing reports it.
var errNoOutput = errors.New("no GITHUB_OUTPUT to report the summary to")

func writeSummary(path string, s runner.Summary) error {
	if path == "" {
		return errNoOutput
	}
	v, err := s.Encode()
	if err != nil {
		return err
	}
	return writeOutputs(path, map[string]string{"summary": v})
}

// writeOutputs appends single-line step outputs to $GITHUB_OUTPUT, or does nothing
// outside Actions.
func writeOutputs(path string, kv map[string]string) error {
	for k, v := range kv {
		if strings.ContainsAny(k+v, "\r\n") {
			return fmt.Errorf("output %s spans lines", k)
		}
	}
	if path == "" {
		return nil
	}
	var out strings.Builder
	for k, v := range kv {
		fmt.Fprintf(&out, "%s=%s\n", k, v)
	}
	return appendFile(path, out.String())
}

// writeMultilineOutput appends one step output that may span lines to
// $GITHUB_OUTPUT, between random delimiters the value does not contain.
func writeMultilineOutput(path, key, value string) error {
	if path == "" {
		return nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	delim := "EOF_" + hex.EncodeToString(b)
	if strings.Contains(value, delim) {
		return fmt.Errorf("output %s contains its delimiter", key)
	}
	return appendFile(path, fmt.Sprintf("%s<<%s\n%s\n%s\n", key, delim, value, delim))
}

// appendFile appends content to the file at path, creating it owner-only if absent.
func appendFile(path, content string) (err error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
