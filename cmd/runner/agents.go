package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// agentImageUsage describes the -agent-image flag of the subcommands that may run an agent on the host;
// build and plan-stage use agentImageRequiredUsage.
const agentImageUsage = "agent image, pinned by digest, to run the agent CLIs in; empty runs them on the host"

// agentImageRequiredUsage describes the -agent-image flag of build and plan-stage, whose output the trusted jobs read.
const agentImageRequiredUsage = "agent image, pinned by digest, to run the agent CLIs in; required"

// requireImages refuses a stage whose agent would run on the runner, where it
// has a shell and could write the outputs the trusted jobs read. images maps
// each flag name to its value.
func requireImages(images map[string]string) error {
	var missing []string
	for name, image := range images {
		if image == "" {
			missing = append(missing, "-"+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	return fmt.Errorf("%s required: the agent and its checks run in containers, never on the runner", strings.Join(missing, ", "))
}

// agentEgressUsage describes the -agent-egress flag of build and plan-stage.
const agentEgressUsage = "on confines the agent container to its models' API hosts through a proxy; empty or off leaves it on the default network"

// agentSettings is how a stage runs its agents: in a container of Image when it
// is not empty, and with Egress "on" reachable only to the API hosts of the
// plans Models name.
type agentSettings struct {
	Image  string
	Egress string
	// Model is the build model, empty for a stage that has none; Plan and Review
	// are the plan and review phases' model lists.
	Model  string
	Plan   []string
	Review []string
}

// models is every model the stage's agents may call.
func (s agentSettings) models() []string {
	var m []string
	if s.Model != "" {
		m = append(m, s.Model)
	}
	return slices.Concat(m, s.Plan, s.Review)
}

// agentRunner is the harnesses a stage runs agents through, in a container of
// s.Image when one is given, with repo's git common directory resolved now,
// before any agent runs, and the probe that proves the image runs every one of
// them; else the host harnesses unchanged and no probe. The returned stop
// removes the egress network and proxy, and is never nil.
func agentRunner(ctx context.Context, s agentSettings, repo string, harnesses []runner.Harness) ([]runner.Harness, func(context.Context, string) error, func(context.Context) error, error) {
	noStop := func(context.Context) error { return nil }
	if s.Egress != "" && s.Egress != "on" && s.Egress != "off" {
		return nil, nil, nil, fmt.Errorf("-agent-egress %q is not on or off", s.Egress)
	}
	if s.Image == "" {
		if s.Egress == "on" {
			return nil, nil, nil, errors.New("-agent-egress on needs -agent-image")
		}
		return harnesses, nil, noStop, nil
	}
	gitDir, err := runner.GitCommonDir(ctx, repo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("agent git common dir: %w", err)
	}
	c := runner.AgentContainer{Image: s.Image, GitCommonDir: gitDir}
	if err := c.Validate(); err != nil {
		return nil, nil, nil, err
	}
	stop := noStop
	if s.Egress == "on" {
		if c, stop, err = startEgress(ctx, c, s.models(), os.Stderr); err != nil {
			return nil, nil, nil, err
		}
	}
	contained, probe, err := runner.WithAgentContainer(harnesses, c)
	if err != nil {
		return nil, nil, nil, errors.Join(err, stop(ctx))
	}
	return contained, probe, stop, nil
}

// startEgress confines c to the API hosts of the plans models name, through
// this binary run as the proxy.
func startEgress(ctx context.Context, c runner.AgentContainer, models []string, logs io.Writer) (runner.AgentContainer, func(context.Context) error, error) {
	hosts, err := runner.EgressHosts(models)
	if err != nil {
		return runner.AgentContainer{}, nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return runner.AgentContainer{}, nil, fmt.Errorf("egress proxy binary: %w", err)
	}
	return runner.StartEgress(ctx, c, self, hosts, logs)
}

// withAgents runs stage with the harnesses contained as s says, and removes the
// egress network and proxy after it returns, however it ends. started is false
// when the agents could not be set up, so stage never ran.
func withAgents(ctx context.Context, logger *slog.Logger, s agentSettings, repo string, harnesses []runner.Harness,
	stage func(contained []runner.Harness, probe func(context.Context, string) error) error) (started bool, err error) {
	contained, probe, stop, err := agentRunner(ctx, s, repo, harnesses)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := stop(ctx); err != nil {
			logger.Warn("agentEgressStopFailed", "err", err.Error())
		}
	}()
	return true, stage(contained, probe)
}

// splitModels reads a comma-separated model list, using a copy of fallback when
// the value is blank — what a cleared workflow input passes. A blank entry inside a list
// is kept so the validator reports it.
func splitModels(s string, fallback []string) []string {
	if strings.TrimSpace(s) == "" {
		return slices.Clone(fallback)
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}
