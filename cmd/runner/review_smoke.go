package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	defaultReviewSmokeTimeout = 8 * time.Minute
	reviewSmokePass           = "pass"
)

const reviewSmokeHeader = "| model | outcome | cost | defect | detail |\n|---|---|---|---|---|\n"

// reviewSmoke runs the restricted review agent on each model of the review list
// against a fixture diff, one at a time, appending each model's row to the job
// summary as it finishes. A model passes when its findings parse and the plan
// meters its run at exactly $0; the job fails only when none passes. It needs
// only the agent binary and its key, no GCP identity.
func reviewSmoke(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("review-smoke", flag.ContinueOnError)
	reviewModels := fs.String("review-models", strings.Join(runner.DefaultReviewModels(), ","), "the review models to try, comma-separated provider/model")
	buildModel := fs.String("build-model", runner.DefaultModel(), "the build model, which no review model may be or fall back to")
	timeout := fs.Duration("model-timeout", defaultReviewSmokeTimeout, "how long each model's review may run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("-model-timeout %s is not positive", *timeout)
	}
	build := strings.TrimSpace(*buildModel)
	if build == "" {
		build = runner.DefaultModel()
	}
	list := splitModels(*reviewModels, runner.DefaultReviewModels())
	if err := runner.ValidateReviewModels(list, build); err != nil {
		return err
	}
	agent := runner.NewRouter(runner.ProfileReview, runner.Harnesses(getenv)...)
	return runReviewSmoke(ctx, logger, agent, list, *timeout, getenv("GITHUB_STEP_SUMMARY"), runner.HarnessSecrets(getenv))
}

// runReviewSmoke is reviewSmoke's loop over models, each in a fresh directory
// under its own timeout, with the summary written to summaryPath when it is set.
func runReviewSmoke(ctx context.Context, logger *slog.Logger, agent runner.Agent, models []string, timeout time.Duration, summaryPath string, secrets []string) error {
	write := func(s string) error {
		if summaryPath == "" {
			return nil
		}
		return appendFile(summaryPath, s)
	}
	if err := write(reviewSmokeHeader); err != nil {
		return err
	}
	passed := 0
	for _, model := range models {
		outcome, row, err := reviewSmokeModel(ctx, agent, model, timeout, secrets)
		if err != nil {
			return err
		}
		logger.Info("reviewSmokeModel", "model", model, "outcome", outcome)
		if outcome == reviewSmokePass {
			passed++
		}
		if err := write(row); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if passed == 0 {
		logger.Error("reviewSmokeNonePassed", "models", len(models))
		return fmt.Errorf("review smoke: none of %s passed", strings.Join(models, ", "))
	}
	return nil
}

// reviewSmokeModel runs one model's review and returns its outcome and summary row.
func reviewSmokeModel(ctx context.Context, agent runner.Agent, model string, timeout time.Duration, secrets []string) (outcome, row string, err error) {
	dir, err := os.MkdirTemp("", "review-smoke-")
	if err != nil {
		return "", "", fmt.Errorf("creating the fixture directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	mctx, cancel := context.WithTimeout(ctx, timeout)
	res, runErr := runner.ReviewSmoke(mctx, agent, model, dir, secrets)
	cancel()

	cost, defect, detail := "—", "—", findingsDetail(res.Findings)
	if res.Priced {
		cost = "$" + strconv.FormatFloat(res.Cost, 'f', -1, 64)
	}
	if runErr == nil {
		defect = "missed"
		if res.Flagged {
			defect = "flagged"
		}
	}
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		outcome, cost, detail = "timed out", "—", "exceeded "+timeout.String()
	case errors.Is(runErr, runner.ErrReviewOutput):
		outcome, detail = "findings unreadable", res.Text
	case runErr != nil:
		outcome, cost, detail = "agent failed", "—", runErr.Error()
	case !res.Priced:
		outcome, detail = "unpriced", "no rate card in providers.json"
	case res.Cost != 0:
		outcome = "cost > $0"
	default:
		outcome = reviewSmokePass
	}
	return outcome, fmt.Sprintf("| %s | %s | %s | %s | %s |\n", model, outcome, cost, defect, cell(detail)), nil
}

// findingsDetail is a review's findings, or a note that it found none.
func findingsDetail(findings string) string {
	if findings == "" {
		return "no findings"
	}
	return findings
}
