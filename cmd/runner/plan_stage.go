package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
	"github.com/alvintoh/forge-wingman/internal/webhook"
)

// cloudPlatform is the OAuth scope the runner's own Cloud Run calls need.
const cloudPlatform = "https://www.googleapis.com/auth/cloud-platform"

// planStage is the model job's half of the two-stage dispatch (FRG-33): it runs
// the plan phase alone, under the plan profile's read-only harnesses, and writes
// the plan artifact the trusted record-plan-stage job re-checks. It holds the
// model job's identity, so it can neither read nor write a run record.
func planStage(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("plan-stage", flag.ContinueOnError)
	planModels := fs.String("plan-models", runner.DefaultPlanModel(), "the plan phase's models in order, comma-separated provider/model; later ones are backups")
	pointer := fs.String("pointer", runner.DefaultPointer, "object naming the current rule-stack sha")
	ticketFile := fs.String("ticket-file", "", "path to the run's ticket, as the ticket subcommand wrote it")
	out := fs.String("out", "", "path to write the plan artifact to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("no -out path to write the plan to")
	}
	t, err := readTicket(*ticketFile)
	if err != nil {
		return err
	}
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("storage client: %w", err)
	}
	defer func() { _ = gcs.Close() }()

	logger = logger.With("attempt", e.attemptID, "ticket", t.ID)
	res, err := runner.PlanStage(ctx, runner.BuildDeps{
		Projections: store.NewBucket(gcs, e.project+"-projections"),
		Completions: store.NewBucket(gcs, e.project+"-completions"),
		PlanAgent:   runner.NewRouter(runner.ProfilePlan, e.harnesses...),
		Logger:      logger,
		Now:         time.Now,
	}, runner.BuildConfig{
		AttemptID:  e.attemptID,
		Repo:       ".",
		TempDir:    e.tempDir,
		Pointer:    *pointer,
		PlanModels: splitModels(*planModels, []string{runner.DefaultPlanModel()}),
		Secrets:    e.secrets,
		Identity:   e.identity,
		Ticket:     t,
	})
	if err != nil {
		// The plan stage reports no summary, so every failure is a failed run
		// rather than a stop the record job would judge.
		return fmt.Errorf("%w: %w", errRunFailed, err)
	}
	raw, err := runner.EncodePlanStage(res)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, raw, 0o600); err != nil {
		return fmt.Errorf("writing the plan: %w", err)
	}
	logger.Info("planStageDone", "files", len(res.Files), "base", res.BaseSHA)
	return nil
}

// recordPlanStage is the trusted half of the two-stage dispatch (FRG-33): it
// re-checks the plan the model job produced, writes it and its base onto the run
// record, settles the run's plan reservation, then wakes the dispatcher for the
// build stage. It is the only writer of the plan stage's data, since the model
// job cannot reach a run record.
func recordPlanStage(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("record-plan-stage", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to write the plan onto")
	planFile := fs.String("plan-file", "", "path to the plan artifact the plan-stage job wrote")
	dispatcherURI := fs.String("dispatcher-uri", "", "Cloud Run run endpoint of the dispatcher job to wake")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if *dispatcherURI == "" {
		return errors.New("no -dispatcher-uri to wake the dispatcher")
	}
	raw, err := os.ReadFile(*planFile)
	if err != nil {
		return fmt.Errorf("reading the plan artifact: %w", err)
	}
	plan, err := runner.ParsePlanStage(raw)
	if err != nil {
		return err
	}
	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	if err := store.NewRecords(fsc).WritePlan(ctx, *runID, runner.PlanRecord{
		Files:     plan.Files,
		BaseSHA:   plan.BaseSHA,
		SettledAt: time.Now(),
	}); err != nil {
		return err
	}
	if err := store.NewQueue(fsc).Settle(ctx, *runID+"#plan"); err != nil {
		return err
	}
	client, err := google.DefaultClient(ctx, cloudPlatform)
	if err != nil {
		return fmt.Errorf("google credentials: %w", err)
	}
	if err := (webhook.Job{RunURI: *dispatcherURI, Client: client}).Wake(ctx); err != nil {
		return err
	}
	logger.Info("planStageRecorded", "run", *runID, "files", len(plan.Files), "base", plan.BaseSHA)
	return nil
}
