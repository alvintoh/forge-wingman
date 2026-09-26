// Command runner executes one run inside a repository's GitHub Actions workflow.
//
//	runner ticket  -run-id <id>                      read the run record's ticket for the model job
//	runner build   -model <provider/model> -ticket   fetch the projection, run the agent, bundle the branch, emit a summary
//	runner pr-meta -run-id <id> -failed-gate <g>     render the PR's title and body from the run record
//	runner record  -run-id <id> -summary <json> ...  validate the build's summary and merge it into the run record
//	runner seed    -run-id <id> -id <ticket> ...     write a run record for a ticket
//
// build exits 0 when it stops short of a branch but reported why; ticket, pr-meta
// and record exit 1 for any run that cannot or did not succeed, so the workflow
// stays red.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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

// env is the workflow context every subcommand but seed reads.
type env struct {
	project   string
	runID     string
	attempt   uint64
	attemptID string
	tempDir   string
	output    string
	secret    string
	identity  runner.Identity
}

func loadEnv(getenv func(string) string) (env, error) {
	e := env{
		project:  getenv("GOOGLE_CLOUD_PROJECT"),
		tempDir:  getenv("RUNNER_TEMP"),
		output:   getenv("GITHUB_OUTPUT"),
		secret:   getenv("OPENCODE_API_KEY"),
		identity: runner.IdentityFromEnv(getenv),
	}
	runID, attempt := getenv("GITHUB_RUN_ID"), getenv("GITHUB_RUN_ATTEMPT")
	var missing []string
	for _, kv := range [][2]string{
		{"GOOGLE_CLOUD_PROJECT", e.project},
		{"RUNNER_TEMP", e.tempDir},
		{"GITHUB_RUN_ID", runID},
		{"GITHUB_RUN_ATTEMPT", attempt},
	} {
		if kv[1] == "" {
			missing = append(missing, kv[0])
		}
	}
	if len(missing) > 0 {
		return env{}, fmt.Errorf("missing environment: %v", missing)
	}
	n, err := parseAttempt(attempt)
	if err != nil {
		return env{}, fmt.Errorf("GITHUB_RUN_ATTEMPT: %w", err)
	}
	e.runID, e.attempt = runID, n
	e.attemptID = runID + "-" + attempt
	return e, nil
}

func run(ctx context.Context, logger *slog.Logger, args []string, getenv func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: runner ticket|build|pr-meta|record|seed [flags]")
	}
	if args[0] == "seed" {
		return seed(ctx, getenv("GOOGLE_CLOUD_PROJECT"), args[1:])
	}
	e, err := loadEnv(getenv)
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
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// ticket writes the run record's ticket as the ticket output, and fails when the
// identity does not match, the record is missing or its ticket cannot be built.
func ticket(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("ticket", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	t, err := readRecordTicket(ctx, logger, e.project, *runID)
	if err != nil {
		return err
	}
	v, err := t.Encode()
	if err != nil {
		return err
	}
	logger.Info("ticketRead", "run", *runID, "ticket", t.ID)
	return writeOutputs(e.output, map[string]string{"ticket": v})
}

func build(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	model := fs.String("model", "", "opencode model, as provider/model")
	pointer := fs.String("pointer", runner.DefaultPointer, "object naming the current rule-stack sha")
	rawTicket := fs.String("ticket", "", "the run's ticket, as the ticket subcommand wrote it")
	if err := fs.Parse(args); err != nil {
		return setupFailed(logger, e.output, err)
	}
	if err := writeOutputs(e.output, map[string]string{"attempt_id": e.attemptID}); err != nil {
		return setupFailed(logger, e.output, err)
	}
	t, err := runner.ParseTicket(*rawTicket)
	if err != nil {
		logger.Warn("ticketRejected", "err", err.Error())
	}

	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return setupFailed(logger, e.output, fmt.Errorf("storage client: %w", err))
	}
	defer func() { _ = gcs.Close() }()

	logger = logger.With("attempt", e.attemptID, "ticket", t.ID)
	res, err := runner.Build(ctx, runner.BuildDeps{
		Projections: store.NewBucket(gcs, e.project+"-projections"),
		Completions: store.NewBucket(gcs, e.project+"-completions"),
		Agent:       runner.Opencode{Bin: "opencode", Model: *model},
		Report:      func(s runner.Summary) error { return writeSummary(e.output, s) },
		Logger:      logger,
		Now:         time.Now,
	}, runner.BuildConfig{
		AttemptID: e.attemptID,
		Repo:      ".",
		TempDir:   e.tempDir,
		Pointer:   *pointer,
		Model:     *model,
		Secret:    e.secret,
		Identity:  e.identity,
		Ticket:    t,
	})
	if err != nil {
		return err
	}
	return writeOutputs(e.output, map[string]string{
		"branch":  res.Branch,
		"changed": strconv.FormatBool(res.Changed),
	})
}

// prMeta writes the PR's title, body and the branch segment the pushed branch must
// carry as outputs, rendered from the run record's ticket.
func prMeta(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("pr-meta", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to read the ticket from")
	checkReport := fs.String("failed-gate", "", "the check job's failed_gate output")
	runURL := fs.String("run-url", "", "URL of the workflow run")
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
	t, err := readRecordTicket(ctx, logger, e.project, *runID)
	if err != nil {
		return err
	}
	body, err := runner.PRBody(string(tmpl), t, runner.FailedGate(*checkReport), *runURL)
	if err != nil {
		return err
	}
	if err := writeOutputs(e.output, map[string]string{"title": t.Subject(), "branch_segment": t.BranchSegment()}); err != nil {
		return err
	}
	return writeMultilineOutput(e.output, "body", body)
}

// readRecordTicket reads run runID's ticket, logging why and failing the run when
// the record is missing or its ticket cannot be built.
func readRecordTicket(ctx context.Context, logger *slog.Logger, project, runID string) (runner.Ticket, error) {
	fsc, err := recordsClient(ctx, project, runID)
	if err != nil {
		return runner.Ticket{}, err
	}
	defer func() { _ = fsc.Close() }()
	return ticketFromRecord(ctx, logger, store.NewRecords(fsc), runID)
}

func ticketFromRecord(ctx context.Context, logger *slog.Logger, r runner.RecordReader, runID string) (runner.Ticket, error) {
	rec, err := runner.ReadRun(ctx, r, runID)
	var stopped *runner.StopError
	if errors.As(err, &stopped) {
		logger.Error("ticketUnavailable", "run", runID, "reason", string(stopped.Reason),
			"err", stopped.Err.Error())
		return runner.Ticket{}, fmt.Errorf("%w: %s", errRunFailed, stopped.Reason)
	}
	if err != nil {
		return runner.Ticket{}, err
	}
	return rec.Ticket(), nil
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
	if *attemptID == "" {
		*attemptID = e.attemptID
	} else if !ownAttemptID(*attemptID, e.runID, e.attempt) {
		logger.Warn("attemptIDRejected", "length", len(*attemptID))
		*attemptID = e.attemptID
	}

	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()

	rec, err := runner.Finalize(ctx, store.NewRecords(fsc), runner.FinalizeInput{
		RunID:             *runID,
		Identity:          e.identity,
		AttemptID:         *attemptID,
		Summary:           *summary,
		SummaryUnreadable: *unreadable,
		PRURL:             *prURL,
		RunResult:         *runResult,
		PRResult:          *prResult,
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
	if !rec.Succeeded() {
		return errRunFailed
	}
	return nil
}

// seed writes a run record for the ticket its flags describe.
func seed(ctx context.Context, project string, args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	runID := fs.String("run-id", "", "id of the run record to create")
	var t runner.Ticket
	fs.StringVar(&t.ID, "id", "", "ticket id, as TEAM-n")
	fs.StringVar(&t.Title, "title", "", "ticket title, which the PR title carries after the id")
	fs.StringVar(&t.Size, "size", "", "ticket size: S, M or L")
	fs.StringVar(&t.SizedBy, "sized-by", "seed", "what sized the ticket")
	bodyFile := fs.String("body-file", "", "file holding the ticket body")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bodyFile == "" {
		return errors.New("-body-file is required")
	}
	body, err := os.ReadFile(*bodyFile)
	if err != nil {
		return fmt.Errorf("reading the ticket body: %w", err)
	}
	t.Body = string(body)
	fsc, err := recordsClient(ctx, project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	return runner.Seed(ctx, store.NewRecords(fsc), *runID, t, time.Now())
}

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

// setupFailed reports a build that could not start, so the record says why. The
// returned StopError exits 0 only when that report was written.
func setupFailed(logger *slog.Logger, output string, err error) error {
	logger.Error("setupFailed", "err", err)
	stopped := &runner.StopError{Outcome: runner.OutcomeInfraFailure, Reason: runner.StopSetup, Err: err}
	if output == "" {
		return errors.Join(stopped, runner.ErrSummaryUnreported)
	}
	if werr := writeSummary(output, runner.SetupSummary(err, time.Now())); werr != nil {
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
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening GITHUB_OUTPUT: %w", err)
	}
	for k, v := range kv {
		if _, err := fmt.Fprintf(f, "%s=%s\n", k, v); err != nil {
			_ = f.Close()
			return fmt.Errorf("writing GITHUB_OUTPUT: %w", err)
		}
	}
	return f.Close()
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
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening GITHUB_OUTPUT: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s<<%s\n%s\n%s\n", key, delim, value, delim); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing GITHUB_OUTPUT: %w", err)
	}
	return f.Close()
}
