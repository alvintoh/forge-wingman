package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
	"github.com/alvintoh/forge-wingman/internal/webhook"
)

// cloudPlatform is the OAuth scope the runner's own Cloud Run calls need.
const cloudPlatform = "https://www.googleapis.com/auth/cloud-platform"

// jobSuccess is the result GitHub reports for a job that completed. It is what
// record-plan-stage requires before it will read a plan artifact: any other
// result means the stage stopped, and there is no artifact to read.
const jobSuccess = "success"

// planStage is the model job's half of the two-stage dispatch: it runs
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
		Guided:     true,
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

// recordPlanStage is the trusted half of the two-stage dispatch: it
// re-checks the plan the model job produced, writes it and its base onto the run
// record, settles the run's plan reservation, then wakes the dispatcher for the
// build stage. It is the only writer of the plan stage's data, since the model
// job cannot reach a run record.
//
// A plan that asked the owner a decision takes the other branch: the run is
// suspended onto that decision, holding no runner and no reservation, and the
// dispatcher is woken to post the question in the run's agent session.
//
// A plan stage that ended any other way wrote no artifact to record, so the run
// is stopped here: no other job would release the reservation it holds.
func recordPlanStage(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("record-plan-stage", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to write the plan onto")
	planFile := fs.String("plan-file", "", "path to the plan artifact the plan-stage job wrote")
	dispatcherURI := fs.String("dispatcher-uri", "", "Cloud Run run endpoint of the dispatcher job to wake")
	runURL := fs.String("run-url", "", "URL of this plan stage's workflow run")
	planStageResult := fs.String("plan-stage-result", "", "how the plan-stage job ended, as GitHub reports it: success, failure or cancelled")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if *planStageResult == "" {
		return errors.New("no -plan-stage-result to say how the plan stage ended")
	}
	if *planStageResult == jobSuccess && *dispatcherURI == "" {
		return errors.New("no -dispatcher-uri to wake the dispatcher")
	}
	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	queue := store.NewQueue(fsc)
	if *planStageResult != jobSuccess {
		// GitHub reports a closed set of results here. An unrecognised one still
		// ends the run — anything else would leave it holding its reservation —
		// but it is named, since nothing else would show it.
		if *planStageResult != "failure" && *planStageResult != "cancelled" && *planStageResult != "skipped" {
			logger.Warn("planStageResultUnknown", "result", *planStageResult)
		}
		return stopPlanStage(ctx, logger, queue, *runID, "the plan stage "+*planStageResult, *runURL)
	}
	raw, err := os.ReadFile(*planFile)
	if err != nil {
		// The stage reported success, so its artifact is what this job records:
		// one it cannot read — an upload that never landed, an artifact past its
		// day — is a plan this run will never have. Stopping it keeps the
		// reservation from being the only thing left holding the run.
		return errors.Join(
			stopPlanStage(ctx, logger, queue, *runID, "the plan artifact could not be read", *runURL),
			fmt.Errorf("reading the plan artifact: %w", err),
		)
	}
	plan, err := runner.ParsePlanStage(raw)
	if err != nil {
		return errors.Join(stopPlanStage(ctx, logger, queue, *runID, "the plan artifact did not re-check", *runURL), err)
	}
	switch {
	case plan.Decision != nil:
		// The plan asked the owner a decision: park the run so it holds no
		// runner and no reservation, and let the dispatcher post the question.
		// The URL is what the waiting notice links to, and what the resumed run
		// reads back, so a decision carrying none is refused rather than stored
		// as a dead link.
		if err := validRunURL(*runURL); err != nil {
			return err
		}
		if err := queue.Suspend(ctx, *runID, *plan.Decision, *runURL, time.Now()); err != nil {
			return err
		}
	default:
		if err := store.NewRecords(fsc).WritePlan(ctx, *runID, runner.PlanRecord{
			Files:     plan.Files,
			BaseSHA:   plan.BaseSHA,
			SettledAt: time.Now(),
		}); err != nil {
			return err
		}
		if err := queue.Settle(ctx, store.PlanKey(*runID)); err != nil {
			return fmt.Errorf("settling %s's plan reservation: %w", *runID, err)
		}
	}
	client, err := google.DefaultClient(ctx, cloudPlatform)
	if err != nil {
		return fmt.Errorf("google credentials: %w", err)
	}
	if err := (webhook.Job{RunURI: *dispatcherURI, Client: client}).Wake(ctx); err != nil {
		return err
	}
	if plan.Decision != nil {
		logger.Info("planStageSuspended", "run", *runID, "options", len(plan.Decision.Options))
		return nil
	}
	logger.Info("planStageRecorded", "run", *runID, "files", len(plan.Files), "base", plan.BaseSHA)
	return nil
}

// stopPlanStage ends a run whose plan stage left nothing the trusted half can
// record: the row leaves the queue with the detail named, and the plan
// reservation is released. It returns an error so the step that stopped the run
// is red as well, which is the only way the reason reaches a reader of the job.
//
// The stage's own reason — a workflow change, a projection miss — stays in the
// plan job's log, so the detail carries that job's URL when the caller has one.
func stopPlanStage(ctx context.Context, logger *slog.Logger, queue *store.Queue, runID, detail, runURL string) error {
	if validRunURL(runURL) == nil {
		detail += " (" + runURL + ")"
	}
	if err := queue.Stop(ctx, runID, runner.StopPlanStageFailed, detail, time.Now()); err != nil {
		return err
	}
	logger.Info("planStageStopped", "run", runID, "detail", detail)
	return fmt.Errorf("%w: %s", errRunFailed, detail)
}

// validRunURL reports whether s is a plan stage's own workflow-run URL — the
// shape run.yml passes, and what the waiting notice links to. An https URL that
// is not an actions run is refused: it would be stored and carried into the
// notice as a link that goes nowhere useful.
func validRunURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || !strings.Contains(u.Path, "/actions/runs/") {
		return errors.New("-run-url must be this plan stage's https actions-run URL")
	}
	return nil
}
