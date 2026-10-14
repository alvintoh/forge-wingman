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
// only the agent binary and its key; with a project set it also reads each plan's
// harness opt-in, so the owner may smoke a harness no run job may select.
func planSmoke(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("plan-smoke", flag.ContinueOnError)
	planModels := fs.String("plan-models", runner.DefaultPlanModel(), "the plan phase's models in order, comma-separated provider/model")
	agentImage := fs.String("agent-image", "", agentImageUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	list := splitModels(*planModels, []string{runner.DefaultPlanModel()})
	if err := runner.ValidatePlanModels(list); err != nil {
		return err
	}
	optIn := harnessOptIn(ctx, logger, getenv, list)
	_, err := withAgents(ctx, logger, agentSettings{Image: *agentImage}, ".", runner.Harnesses(getenv),
		func(contained []runner.Harness, _ func(context.Context, string) error) error {
			router := runner.NewRouter(runner.ProfilePlan, contained...).WithOptIn(optIn)
			return runPlanSmoke(ctx, logger, router, list, getenv("GITHUB_STEP_SUMMARY"))
		})
	return err
}

// runPlanSmoke is planSmoke's loop over the first and last of list, with the
// summary appended to summaryPath when it is set.
func runPlanSmoke(ctx context.Context, logger *slog.Logger, agent runner.Agent, list []string, summaryPath string) error {
	dir, err := os.MkdirTemp("", "plan-smoke-")
	if err != nil {
		return fmt.Errorf("creating the fixture directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var summary strings.Builder
	summary.WriteString("| model | outcome | detail |\n|---|---|---|\n")
	var failed []string
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
	if summaryPath != "" {
		if err := appendFile(summaryPath, summary.String()); err != nil {
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
