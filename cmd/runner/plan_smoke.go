package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// smokeModels is the models plan-smoke proves: the list's first and last, so the
// main model and the last backup are both covered.
func smokeModels(list []string) []string {
	if len(list) == 1 {
		return list
	}
	return []string{list[0], list[len(list)-1]}
}

// planSmoke runs the restricted plan agent on the first and last model of the plan
// list against a fixture, appends each model and outcome to the job summary, and
// fails unless every one refused both the edit and the shell command. It needs
// only the agent binary and its key, no GCP identity.
func planSmoke(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("plan-smoke", flag.ContinueOnError)
	planModels := fs.String("plan-models", runner.DefaultPlanModel, "the plan phase's models in order, comma-separated provider/model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	list := splitModels(*planModels)
	if err := runner.ValidatePlanModels(list); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "plan-smoke-")
	if err != nil {
		return fmt.Errorf("creating the fixture directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var summary strings.Builder
	summary.WriteString("| model | outcome | detail |\n|---|---|---|\n")
	var failed []string
	agent := runner.NewRouter(runner.ProfilePlan, harnesses(getenv)...)
	for _, model := range smokeModels(list) {
		res, err := runner.PlanSmoke(ctx, agent, model, dir)
		switch {
		case err != nil:
			logger.Error("planSmokeFailed", "model", model, "err", err.Error())
			fmt.Fprintf(&summary, "| %s | agent failed | %s |\n", model, cell(err.Error()))
			failed = append(failed, model)
		case !res.Refused:
			logger.Error("planSmokeNotRefused", "model", model, "detail", res.Detail)
			fmt.Fprintf(&summary, "| %s | not refused | %s |\n", model, cell(res.Detail))
			failed = append(failed, model)
		default:
			logger.Info("planSmokeRefused", "model", model)
			fmt.Fprintf(&summary, "| %s | refused | %s |\n", model, cell(res.Text))
		}
	}
	if path := getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if err := appendFile(path, summary.String()); err != nil {
			return err
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("plan smoke failed on %s", strings.Join(failed, ", "))
	}
	return nil
}

// cell makes s safe inside one markdown table cell.
func cell(s string) string {
	return strings.NewReplacer("|", "/", "\n", " ", "\r", " ").Replace(s)
}
