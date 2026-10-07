// Command runner executes one run inside a repository's GitHub Actions workflow.
//
//	runner ticket          -run-id <id> -out <path> write the run record's ticket to a file for the model job
//	runner build           -model <provider/model>  fetch the projection, run the agent, bundle the branch, emit a summary
//	runner pr-meta         -run-id <id> ...          render the PR's title and body from the run record
//	runner record          -run-id <id> -summary ... validate the build's summary and merge it into the run record
//	runner enable-provider -provider <name>          admit a halted provider back to dispatch (AC5)
//	runner plan-define     -provider <name> ...      record a provider plan's price, billing, limit behaviour, pages and harnesses
//	runner plan-optin      -provider <name>          record the owner's consent to a per-token provider's spend
//	runner plan-verdict    -provider <name> ...      record the verdict on a plan's terms with its wording and source
//	runner plan-reply      -provider <name> ...      record a vendor reply and the verdict it leads to
//	runner plan-list                                 print every plan side by side, flagging the ones to look at
//	runner plan-smoke      -plan-models <list>       prove the plan agent refuses edit and bash on the list's first and last model
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
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"

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

// errRunFailed is the result of ticket, pr-meta or record for a run that cannot or did not succeed.
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
	case "ticket", "pr-meta", "enable-provider",
		"plan-define", "plan-optin", "plan-verdict", "plan-reply", "plan-list":
		return []string{"GOOGLE_CLOUD_PROJECT"}, true
	case "record":
		return []string{"GOOGLE_CLOUD_PROJECT", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}, true
	case "build":
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
		return errors.New("usage: runner ticket|build|pr-meta|record|enable-provider|plan-define|plan-verdict|plan-reply|plan-list|plan-smoke [flags]")
	}
	if args[0] == "plan-smoke" {
		return planSmoke(ctx, logger, getenv, args[1:])
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
	case "pr-meta":
		return prMeta(ctx, logger, e, args[1:])
	case "record":
		return record(ctx, logger, e, args[1:])
	case "enable-provider":
		return enableProvider(ctx, logger, e, args[1:])
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

// ticket writes the run record's ticket to the file -out names, and fails when
// the identity does not match, the record is missing or its ticket cannot be
// built.
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
	rec, err := readRunRecord(ctx, logger, e.project, *runID)
	if err != nil {
		return err
	}
	t := rec.Ticket()
	logger.Info("ticketRead", "run", *runID, "ticket", t.ID)
	return writeTicket(*out, e.output, t, rec.ModelLabels)
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
	reviewModels := fs.String("review-models", runner.DefaultReviewModel(), "the review phase's models in order, comma-separated provider/model; later ones are backups (FR-14)")
	pointer := fs.String("pointer", runner.DefaultPointer, "object naming the current rule-stack sha")
	ticketFile := fs.String("ticket-file", "", "path to the run's ticket, as the ticket subcommand wrote it")
	if err := fs.Parse(args); err != nil {
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
	plan := splitModels(*planModels, runner.DefaultPlanModel())
	review := splitModels(*reviewModels, runner.DefaultReviewModel())
	res, err := runner.Build(ctx, runner.BuildDeps{
		Projections: store.NewBucket(gcs, e.project+"-projections"),
		Completions: store.NewBucket(gcs, e.project+"-completions"),
		Agent:       runner.NewRouter(runner.ProfileBuild, e.harnesses...),
		PlanAgent:   runner.NewRouter(runner.ProfilePlan, e.harnesses...),
		ReviewAgent: runner.NewRouter(runner.ProfileReview, e.harnesses...),
		Checks:      runner.RunChecks,
		Report:      func(s runner.Summary) error { return writeSummary(e.output, s) },
		Logger:      logger,
		Now:         time.Now,
	}, runner.BuildConfig{
		AttemptID:    e.attemptID,
		Repo:         ".",
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

// splitModels reads a comma-separated model list, using fallback when the value
// is blank — what a cleared workflow input passes. A blank entry inside a list
// is kept so the validator reports it.
func splitModels(s, fallback string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{fallback}
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// prMeta writes the PR's title, body and the branch segment the pushed branch must
// carry as outputs, rendered from the run record's ticket and the build's summary.
func prMeta(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("pr-meta", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	attemptID := fs.String("attempt-id", e.attemptID, "workflow attempt the build ran in")
	summary := fs.String("summary", "", "the build's summary, as JSON")
	checkReport := fs.String("failed-gate", "", "the check job's failed_gate output")
	runURL := fs.String("run-url", "", "URL of the workflow run")
	loopDetail := fs.String("loop-detail", "", "the pre-PR loop's report of why the PR is a draft (FR-5)")
	template := fs.String("template", runner.DefaultPRTemplate, "pull request template to render")
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
	title, body, err := renderPR(string(tmpl), rec, sum, runner.FailedGate(*checkReport), *runURL, *loopDetail)
	if err != nil {
		return err
	}
	if err := writeOutputs(e.output, map[string]string{"title": title, "branch_segment": rec.Ticket().BranchSegment()}); err != nil {
		return err
	}
	return writeMultilineOutput(e.output, "body", body)
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

	rec, err := runner.Finalize(ctx, store.NewRecords(fsc), store.NewQueue(fsc), runner.FinalizeInput{
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
