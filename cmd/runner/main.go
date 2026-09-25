// Command runner executes one run inside a repository's GitHub Actions workflow.
//
//	runner build  -model <provider/model>   fetch the projection, run the agent, bundle the branch, emit a summary
//	runner record -summary <json> ...       validate the build's summary and write the run record
//
// build exits 0 when it stops short of a branch but reported why; record exits 1
// after writing any record whose run did not succeed, so the workflow stays red.
package main

import (
	"context"
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

// errRunFailed is record's result once it has written a record for a run that did not succeed.
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
	project  string
	runID    string
	attempt  uint64
	recordID string
	tempDir  string
	output   string
	secret   string
}

func loadEnv(getenv func(string) string) (env, error) {
	e := env{
		project: getenv("GOOGLE_CLOUD_PROJECT"),
		tempDir: getenv("RUNNER_TEMP"),
		output:  getenv("GITHUB_OUTPUT"),
		secret:  getenv("OPENCODE_API_KEY"),
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
	e.recordID = runID + "-" + attempt
	return e, nil
}

func run(ctx context.Context, logger *slog.Logger, args []string, getenv func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: runner build|record [flags]")
	}
	e, err := loadEnv(getenv)
	if err != nil {
		if args[0] == "build" {
			return setupFailed(logger, getenv("GITHUB_OUTPUT"), err)
		}
		return err
	}
	switch args[0] {
	case "build":
		return build(ctx, logger, e, args[1:])
	case "record":
		return record(ctx, logger, e, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func build(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	model := fs.String("model", "", "opencode model, as provider/model")
	pointer := fs.String("pointer", runner.DefaultPointer, "object naming the current rule-stack sha")
	if err := fs.Parse(args); err != nil {
		return setupFailed(logger, e.output, err)
	}
	if err := writeOutputs(e.output, map[string]string{"record_id": e.recordID}); err != nil {
		return setupFailed(logger, e.output, err)
	}

	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return setupFailed(logger, e.output, fmt.Errorf("storage client: %w", err))
	}
	defer func() { _ = gcs.Close() }()

	logger = logger.With("record", e.recordID, "ticket", runner.Tracer.ID)
	res, err := runner.Build(ctx, runner.BuildDeps{
		Projections: store.NewBucket(gcs, e.project+"-projections"),
		Completions: store.NewBucket(gcs, e.project+"-completions"),
		Agent:       runner.Opencode{Bin: "opencode", Model: *model},
		Report:      func(s runner.Summary) error { return writeSummary(e.output, s) },
		Logger:      logger,
		Now:         time.Now,
	}, runner.BuildConfig{
		RecordID: e.recordID,
		Repo:     ".",
		TempDir:  e.tempDir,
		Pointer:  *pointer,
		Model:    *model,
		Secret:   e.secret,
		Ticket:   runner.Tracer,
	})
	if err != nil {
		return err
	}
	return writeOutputs(e.output, map[string]string{
		"branch":  res.Branch,
		"changed": strconv.FormatBool(res.Changed),
	})
}

func record(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	recordID := fs.String("record-id", e.recordID, "run record to finalize")
	summary := fs.String("summary", "", "the build's summary, as JSON")
	unreadable := fs.Bool("summary-unreadable", false, "the summary could not be passed in")
	prURL := fs.String("pr-url", "", "URL of the opened PR")
	runResult := fs.String("run-result", "", "result of the run job")
	prResult := fs.String("pr-result", "", "result of the pr job")
	prSeconds := fs.Int("pr-duration-s", 0, "wall-clock seconds the pr job took")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !ownRecordID(*recordID, e.runID, e.attempt) {
		logger.Warn("recordIDRejected", "length", len(*recordID))
		*recordID = e.recordID
	}

	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()

	rec, err := runner.Finalize(ctx, store.NewRecords(fsc), runner.FinalizeInput{
		RecordID:          *recordID,
		Ticket:            runner.Tracer,
		Summary:           *summary,
		SummaryUnreadable: *unreadable,
		PRURL:             *prURL,
		RunResult:         *runResult,
		PRResult:          *prResult,
		PRDuration:        time.Duration(*prSeconds) * time.Second,
	}, time.Now())
	if err != nil {
		return err
	}
	if rec.StopReason == runner.StopSummaryInvalid {
		logger.Warn("summaryRejected", "record", *recordID, "err", rec.StopDetail)
	}
	logger.Info("recordFinalized", "record", *recordID, "outcome", string(rec.Outcome), "reason", string(rec.StopReason))
	if err := writeOutputs(e.output, map[string]string{"recorded": "true"}); err != nil {
		return err
	}
	if !rec.Succeeded() {
		return errRunFailed
	}
	return nil
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

// ownRecordID reports whether id names an attempt of this workflow run up to the current one.
func ownRecordID(id, runID string, current uint64) bool {
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
