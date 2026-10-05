package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// opencodeGoProvider is the Go plan's model-id prefix, served by the same CLI.
const opencodeGoProvider = "opencode-go"

// opencodeEnvNames are the variables the opencode process inherits: the base
// set plus its own API key, which no other harness receives (AC6).
var opencodeEnvNames = append(slices.Clone(agentBaseEnvNames), "OPENCODE_API_KEY")

const agentKillGrace = 30 * time.Second

// OpencodeHarness runs the opencode CLI, serving the Zen and Go providers.
type OpencodeHarness struct {
	Bin string
}

// Providers are the model-id prefixes the opencode CLI serves.
func (h OpencodeHarness) Providers() []string { return []string{ZenProvider, opencodeGoProvider} }

// Agent returns the opencode agent for profile p, restricted for plan and
// review the way those phases require.
func (h OpencodeHarness) Agent(p Profile, model string) Agent {
	switch p {
	case ProfilePlan:
		return PlanCLIAgent(h.Bin, model)
	case ProfileReview:
		return ReviewCLIAgent(h.Bin, model)
	case ProfileBuild:
		return CLIAgent{Bin: h.Bin, Model: model}
	default:
		return CLIAgent{Bin: h.Bin, Model: model}
	}
}

// Classify classifies a failed run by the assumed provider markers (AC4).
func (h OpencodeHarness) Classify(stderrTail string, _ error) (Outcome, StopReason) {
	return classifyMarkers(stderrTail)
}

// Ready always reports the opencode harness usable; its key is optional.
func (h OpencodeHarness) Ready() error { return nil }

// CLIAgent runs the agent CLI non-interactively.
//
// Agent and ConfigContent together select a restricted agent profile: Agent
// names it on the command line, and ConfigContent (opencode's own
// OPENCODE_CONFIG_CONTENT variable) defines its permissions inline, since
// there is no file to point opencode at. Both are empty for the default,
// unrestricted agent.
type CLIAgent struct {
	Bin           string
	Model         string
	Agent         string
	ConfigContent string
}

// WithModel returns a copy of o that runs model, keeping its agent profile.
func (o CLIAgent) WithModel(model string) Agent {
	o.Model = model
	return o
}

// Run sends the prompt on stdin and streams the JSON events to stdout,
// continuing session when it is non-empty rather than starting a fresh one.
//
// The agent runs in its own process group, terminated when ctx ends and killed
// once Run returns.
func (o CLIAgent) Run(ctx context.Context, dir, session, prompt string, stdout, stderr io.Writer) error {
	args := []string{"run", "--format", "json", "--auto", "-m", o.Model, "--dir", dir}
	if o.Agent != "" {
		args = append(args, "--agent", o.Agent)
	}
	if session != "" {
		args = append(args, "-s", session)
	}
	cmd := exec.CommandContext(ctx, o.Bin, args...)
	cmd.Dir = dir
	cmd.Env = filterEnv(os.Environ(), opencodeEnvNames)
	if o.ConfigContent != "" {
		cmd.Env = append(cmd.Env, "OPENCODE_CONFIG_CONTENT="+o.ConfigContent)
	}
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = agentKillGrace
	ownProcessGroup(cmd)
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}
	err = cmd.Wait()
	killProcessGroup(cmd)
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}
	return nil
}

// planAgentName is the restricted agent the plan phase runs under.
const planAgentName = "wingman-plan"

// planAgentConfig is OPENCODE_CONFIG_CONTENT for planAgentName: denying edit and
// bash disables both tools outright rather than merely gating them behind a
// prompt, confirmed against the installed agent CLI's own resolved agent
// config (`opencode debug agent <name>`).
const planAgentConfig = `{"agent":{"` + planAgentName + `":{"mode":"primary","permission":{"edit":"deny","bash":"deny"}}}}`

// PlanCLIAgent is the restricted opencode agent the plan phase runs: the agent
// CLI with edit and bash denied, so it can read and reason but not touch the
// worktree.
func PlanCLIAgent(bin, model string) CLIAgent {
	return CLIAgent{Bin: bin, Model: model, Agent: planAgentName, ConfigContent: planAgentConfig}
}

// reviewAgentName is the restricted agent the review pass runs under.
const reviewAgentName = "wingman-review"

// reviewAgentConfig is OPENCODE_CONFIG_CONTENT for reviewAgentName: a review
// reads the diff and reasons about it, so edit and bash are denied the same
// way planAgentConfig denies them for the plan phase.
const reviewAgentConfig = `{"agent":{"` + reviewAgentName + `":{"mode":"primary","permission":{"edit":"deny","bash":"deny"}}}}`

// ReviewCLIAgent is the restricted opencode agent the review pass runs: the
// agent CLI with edit and bash denied, so it can read the diff and reason
// about it but not touch the worktree.
func ReviewCLIAgent(bin, model string) CLIAgent {
	return CLIAgent{Bin: bin, Model: model, Agent: reviewAgentName, ConfigContent: reviewAgentConfig}
}
