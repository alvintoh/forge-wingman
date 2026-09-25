// Command runner executes one run inside a repository's GitHub Actions workflow.
//
//	runner build  -model <provider/model>   fetch the projection, run the agent, bundle the branch
//	runner record -pr-url <url> ...         merge the PR job's outcome into the run record
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
	if err != nil {
		var stopped *runner.StopError
		if !errors.As(err, &stopped) {
			logger.Error("runnerFailed", "err", err)
		}
		os.Exit(1)
	}
}

// env is the workflow context every subcommand reads.
type env struct {
	project  string
	runID    string
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
	e.runID = runID
	e.recordID = runID + "-" + attempt
	return e, nil
}

func run(ctx context.Context, logger *slog.Logger, args []string, getenv func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: runner build|record [flags]")
	}
	e, err := loadEnv(getenv)
	if err != nil {
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
		return err
	}
	if err := writeOutputs(e.output, map[string]string{"record_id": e.recordID}); err != nil {
		return err
	}

	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("storage client: %w", err)
	}
	defer func() { _ = gcs.Close() }()
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()

	logger = logger.With("record", e.recordID, "ticket", runner.Tracer.ID)
	res, err := runner.Build(ctx, runner.BuildDeps{
		Projections: store.NewBucket(gcs, e.project+"-projections"),
		Completions: store.NewBucket(gcs, e.project+"-completions"),
		Records:     store.NewRecords(fsc),
		Agent:       runner.Opencode{Bin: "opencode", Model: *model},
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
	prURL := fs.String("pr-url", "", "URL of the opened PR")
	runResult := fs.String("run-result", "", "result of the run job")
	prResult := fs.String("pr-result", "", "result of the pr job")
	prSeconds := fs.Int("pr-duration-s", 0, "wall-clock seconds the pr job took")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !ownRecordID(*recordID, e.runID) {
		logger.Warn("recordIDRejected", "length", len(*recordID))
		*recordID = e.recordID
	}

	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()

	rec, err := runner.Finalize(ctx, store.NewRecords(fsc), runner.FinalizeInput{
		RecordID:   *recordID,
		Ticket:     runner.Tracer,
		PRURL:      *prURL,
		RunResult:  *runResult,
		PRResult:   *prResult,
		PRDuration: time.Duration(*prSeconds) * time.Second,
	}, time.Now())
	if err != nil {
		return err
	}
	logger.Info("recordFinalized", "record", *recordID, "outcome", string(rec.Outcome), "reason", string(rec.StopReason))
	return nil
}

// ownRecordID reports whether id names an attempt of this workflow run.
func ownRecordID(id, runID string) bool {
	attempt, ok := strings.CutPrefix(id, runID+"-")
	if !ok || attempt == "" {
		return false
	}
	_, err := strconv.ParseUint(attempt, 10, 32)
	return err == nil
}

// writeOutputs appends step outputs to $GITHUB_OUTPUT, or does nothing outside Actions.
func writeOutputs(path string, kv map[string]string) error {
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
