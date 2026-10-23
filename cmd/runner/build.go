package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

func build(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	model := fs.String("model", "", "model, as provider/model")
	planModels := fs.String("plan-models", runner.DefaultPlanModel(), "the plan phase's models in order, comma-separated provider/model; later ones are backups")
	reviewModels := fs.String("review-models", strings.Join(runner.DefaultReviewModels(), ","), "the review phase's models in order, comma-separated provider/model; later ones are backups (FR-14)")
	pointer := fs.String("pointer", runner.DefaultPointer, "object naming the current rule-stack sha")
	ticketFile := fs.String("ticket-file", "", "path to the run's ticket, as the ticket subcommand wrote it")
	checksImage := fs.String("checks-image", "", "Go image, pinned by digest, to run the pre-PR checks in; required")
	agentImage := fs.String("agent-image", "", agentImageRequiredUsage)
	agentEgress := fs.String("agent-egress", "", agentEgressUsage)
	if err := fs.Parse(args); err != nil {
		return setupFailed(logger, e.output, err)
	}
	if err := requireImages(map[string]string{"agent-image": *agentImage, "checks-image": *checksImage}); err != nil {
		return setupFailed(logger, e.output, err)
	}
	const repo = "."
	checks, packageTests, err := checkRunners(ctx, *checksImage, e.tempDir, repo)
	if err != nil {
		return setupFailed(logger, e.output, err)
	}
	plan := splitModels(*planModels, []string{runner.DefaultPlanModel()})
	review := splitModels(*reviewModels, runner.DefaultReviewModels())
	started, err := withAgents(ctx, logger, agentSettings{Image: *agentImage, Egress: *agentEgress, Model: *model, Plan: plan, Review: review},
		repo, e.harnesses, func(contained []runner.Harness, probe func(context.Context, string) error) error {
			e.harnesses = contained
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
			// These routers are given no harness opt-in: a run job holds no plan
			// record, so it runs the plan's next harness rather than one that is
			// not open-source, which only the owner's own commands admit.
			res, err := runner.Build(ctx, runner.BuildDeps{
				Projections:  store.NewBucket(gcs, e.project+"-projections"),
				Completions:  store.NewBucket(gcs, e.project+"-completions"),
				Agent:        runner.NewRouter(runner.ProfileBuild, e.harnesses...),
				PlanAgent:    runner.NewRouter(runner.ProfilePlan, e.harnesses...),
				ReviewAgent:  runner.NewRouter(runner.ProfileReview, e.harnesses...),
				Checks:       checks,
				PackageTests: packageTests,
				AgentProbe:   probe,
				Report:       func(s runner.Summary) error { return writeSummary(e.output, s) },
				Logger:       logger,
				Now:          time.Now,
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
			}); err != nil {
				return err
			}
			return writeMultilineOutput(e.output, "loop_detail", res.LoopDetail)
		})
	if !started {
		return setupFailed(logger, e.output, err)
	}
	return err
}

// checkRunners are the pre-PR loop's checks: the repository's own gates, and one
// package's tests, which the guard probe breaks a changed guard against. Both
// run in a container of image when one is given, with the module cache under
// tempDir and repo's git common directory resolved now, before any agent runs;
// else on the host.
func checkRunners(ctx context.Context, image, tempDir, repo string) (runner.CheckRunner, runner.PackageTestRunner, error) {
	if image == "" {
		return runner.RunChecks, runner.RunPackageTest, nil
	}
	linter, err := exec.LookPath("golangci-lint")
	if err != nil {
		return nil, nil, fmt.Errorf("checks linter: %w", err)
	}
	if linter, err = filepath.Abs(linter); err != nil {
		return nil, nil, fmt.Errorf("checks linter: %w", err)
	}
	// The checks image pins a Go toolchain alone, so the web gates run on the
	// bun model.yml installs for its harnesses, mounted into their container.
	bun, err := exec.LookPath("bun")
	if err != nil {
		return nil, nil, fmt.Errorf("checks bun: %w", err)
	}
	if bun, err = filepath.Abs(bun); err != nil {
		return nil, nil, fmt.Errorf("checks bun: %w", err)
	}
	gitDir, err := runner.GitCommonDir(ctx, repo)
	if err != nil {
		return nil, nil, fmt.Errorf("checks git common dir: %w", err)
	}
	c := runner.ContainerChecks{Image: image, ModCache: filepath.Join(tempDir, "checks-modcache"), Linter: linter,
		Bun: bun, BunCache: filepath.Join(tempDir, "checks-bun-cache"), GitCommonDir: gitDir}
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	return c.Run, c.RunPackageTest, nil
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
