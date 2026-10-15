package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// ompName is the key a plan's configuration selects the harness by, and the
// binary oh-my-pi installs (testdata/omp/PACKAGE.txt).
const ompName = "omp"

// ompLicence is omp's own licence: oh-my-pi is MIT.
const ompLicence = LicenceOpenSource

// ompPlan is how omp reaches one plan: its own built-in provider name, so the
// adapter reaches the plan's endpoint with the model id omp recognises, and the
// variable that provider reads its key from, which is the CLI's own name rather
// than the repository secret's.
type ompPlan struct {
	provider string
	keyEnv   string
}

// ompPlans are the plans omp can reach. A plan absent here is one omp cannot
// reach, and the adapter refuses its models before starting omp.
var ompPlans = map[string]ompPlan{
	"command-code": {provider: "commandcode", keyEnv: "COMMAND_CODE_API_KEY"},
	"gemini":       {provider: "google", keyEnv: "GEMINI_API_KEY"},
}

// ompAgentDir and ompSessionDir sit under the harness's HOME; omp reads its
// user rules and settings, ompRulesFile and ompConfigFile, from the agent
// directory.
const (
	ompAgentDir   = "agent"
	ompSessionDir = "sessions"
	ompRulesFile  = "AGENTS.md"
	ompConfigFile = "config.yml"
	// ompModelsFile holds the plan's per-model overrides: omp asks for the
	// output maximum its own registry states, and an API that caps lower
	// rejects the request outright.
	ompModelsFile = "models.yml"
)

// ompAgentPath is the agent directory omp reads its rules and settings from and
// writes its sessions to, under the run's home.
func ompAgentPath(home string) string { return filepath.Join(home, ompAgentDir) }

// ompEnv is the environment omp runs under: the run's own home, and the agent
// directory inside it, so its rules and sessions never reach another run.
func ompEnv(home string) []string {
	return []string{"HOME=" + home, "PI_CODING_AGENT_DIR=" + ompAgentPath(home)}
}

// ompConfig turns off the update check, project MCP servers and telemetry
// export; omp loads no other tool's user-level config unless opted in.
const ompConfig = `setupVersion: 2
startup:
  checkUpdate: false
mcp:
  enableProjectConfig: false
telemetry:
  otlpExportEnabled: false
`

// ompModels renders plan's output caps as omp's modelOverrides, keyed by the id
// omp itself knows the model by. Empty when the plan caps no model, so a plan
// without caps writes no file and keeps the harness's own default.
func ompModels(plan string, caps map[string]int) string {
	provider := ompPlans[plan].provider
	var overrides []string
	for model, maxTokens := range caps {
		id, ok := strings.CutPrefix(model, plan+"/")
		if !ok || id == "" {
			continue
		}
		overrides = append(overrides, fmt.Sprintf("      %q:\n        maxTokens: %d\n", id, maxTokens))
	}
	if len(overrides) == 0 {
		return ""
	}
	slices.Sort(overrides)
	return "providers:\n  " + provider + ":\n    modelOverrides:\n" + strings.Join(overrides, "")
}

func init() {
	registerHarness(ompName, func(getenv func(string) string) Harness {
		var home string
		if tmp := getenv("RUNNER_TEMP"); tmp != "" {
			home = filepath.Join(tmp, "omp-home")
		}
		return OmpHarness{Bin: ompName, Getenv: getenv, Home: home}
	})
}

// OmpHarness runs the oh-my-pi agent CLI on each plan's own key.
type OmpHarness struct {
	Bin string
	// Key is the key the harness runs on when it is built directly, without an
	// environment to resolve a plan's own secret from.
	Key string
	// Getenv resolves a plan's key secret when the harness is built from the
	// environment, so one harness serves every plan omp is configured for.
	Getenv func(string) string
	// Home is the CLI's HOME for the whole run: it holds the agent directory and
	// the sessions a later round resumes.
	Home string
	// Container, when it names an image, runs omp in a container.
	Container AgentContainer
}

// keyFor is the key the harness runs plan on: its own resolved key when built
// directly, else the plan's secret read from the environment.
func (h OmpHarness) keyFor(plan string) string {
	if h.Getenv == nil {
		return h.Key
	}
	return h.Getenv(providers.KeySecret(plan))
}

// Name is the harness's own name, the key a plan's configuration selects it by.
func (h OmpHarness) Name() string { return ompName }

// Licence is the harness's own licence, recorded here alone.
func (h OmpHarness) Licence() Licence { return ompLicence }

// Agent returns the omp agent for profile p, keyed for the model's plan.
func (h OmpHarness) Agent(p Profile, model string) Agent {
	return OmpAgent{Bin: h.Bin, Key: h.keyFor(Provider(model)), Home: h.Home, Model: model, Profile: p, Container: h.Container}
}

func (h OmpHarness) withContainer(c AgentContainer) Harness { h.Container = c; return h }

func (h OmpHarness) startUpCheck() agentSpec {
	return agentSpec{Bin: h.Bin, Args: []string{"--version"}, Home: h.Home, Env: ompEnv(h.Home)}
}

// Ready reports the harness usable only when plan's key is present (AC7).
func (h OmpHarness) Ready(plan string) error {
	if h.keyFor(plan) == "" {
		return fmt.Errorf("the omp harness needs the %s secret", providers.KeySecret(plan))
	}
	return nil
}

// Classify classifies a failed run by the assumed provider markers: omp exits 1
// for every turn-fatal error, so its exit code carries no class of its own, bar
// docker failing to run it in a container.
func (h OmpHarness) Classify(stderrTail string, exitErr error) (Outcome, StopReason) {
	if h.Container.failedToRun(exitErr) {
		return OutcomeInfraFailure, StopAgentContainer
	}
	return classifyMarkers(stderrTail)
}

// OmpAgent runs the omp CLI for one profile.
type OmpAgent struct {
	Bin     string
	Key     string
	Home    string
	Model   string
	Profile Profile
	// Container, when it names an image, runs omp in a container.
	Container AgentContainer
}

// WithModel returns a copy of a that runs model, keeping its profile.
func (a OmpAgent) WithModel(model string) Agent { a.Model = model; return a }

// Run sends the prompt on stdin and translates omp's JSON events to the
// runner's own event shape on stdout. rules, when non-empty, is the agent
// directory's user rules, ahead of the per-run context; an empty rules removes
// them. A non-empty session is resumed from the sessions under Home. The key
// reaches omp in its environment alone, never on disk.
func (a OmpAgent) Run(ctx context.Context, dir, session, prompt, rules string, stdout, stderr io.Writer) error {
	if a.Home == "" {
		return errors.New("the omp harness has no home directory")
	}
	plan := Provider(a.Model)
	rest, ok := strings.CutPrefix(a.Model, plan+"/")
	reach, reachable := ompPlans[plan]
	if !ok || rest == "" || !reachable {
		return fmt.Errorf("the omp harness has no provider for plan %q", plan)
	}
	agentDir := ompAgentPath(a.Home)
	if err := writeAgentFile(filepath.Join(agentDir, ompRulesFile), rules); err != nil {
		return fmt.Errorf("omp rules: %w", err)
	}
	if err := writeAgentFile(filepath.Join(agentDir, ompConfigFile), ompConfig); err != nil {
		return fmt.Errorf("omp config: %w", err)
	}
	if body := ompModels(plan, providers.OutputCaps(plan)); body != "" {
		if err := writeAgentFile(filepath.Join(agentDir, ompModelsFile), body); err != nil {
			return fmt.Errorf("omp models: %w", err)
		}
	}
	approval := "always-ask"
	if a.Profile == ProfileBuild {
		approval = "yolo"
	}
	args := []string{"-p", "--mode", "json", "--model", reach.provider + "/" + rest, "--no-extensions", "--no-skills",
		"--session-dir", filepath.Join(a.Home, ompSessionDir), "--approval-mode", approval}
	if session != "" {
		args = append(args, "--resume", session)
	}
	cmd, err := agentCommand(ctx, a.Container, agentSpec{Bin: a.Bin, Args: args, Dir: dir, Home: a.Home,
		Env: ompEnv(a.Home), Secret: reach.keyEnv + "=" + a.Key})
	if err != nil {
		return fmt.Errorf("omp command: %w", err)
	}
	cmd.Stdin = strings.NewReader(prompt)
	return runAgentCLI(cmd, a.Home, stderr, func(events io.Reader, _ error) error {
		return translateOmpEvents(events, dir, stdout, stderr)
	})
}

// ompEvent is one line of omp's JSON output: the session line, an event
// carrying a finished message, or one tool call's start or end.
type ompEvent struct {
	Type       string      `json:"type"`
	ID         string      `json:"id"`
	Message    *ompMessage `json:"message"`
	ToolCallID string      `json:"toolCallId"`
	ToolName   string      `json:"toolName"`
	IsError    bool        `json:"isError"`
	Args       struct {
		Path string `json:"path"`
	} `json:"args"`
}

type ompMessage struct {
	Role         string `json:"role"`
	Timestamp    int64  `json:"timestamp"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
	Content      []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage *ompUsage `json:"usage"`
}

// ompUsage is one request's tokens: Input excludes CacheRead, and Output
// includes ReasoningTokens.
type ompUsage struct {
	Input           int64 `json:"input"`
	Output          int64 `json:"output"`
	CacheRead       int64 `json:"cacheRead"`
	CacheWrite      int64 `json:"cacheWrite"`
	ReasoningTokens int64 `json:"reasoningTokens"`
	Cost            struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

// translateOmpEvents rewrites omp's events as the runner's own event shape: the
// session, one text part per finished assistant text, and one step per request,
// stamped with the request's time and carrying the tool calls that request
// made — this harness reports a request's usage before the calls it decided on,
// so the step is held until the tools that follow it have run. A turn-fatal
// error, which omp reports only in the message, is echoed to stderr, where
// Classify reads it (AC3).
func translateOmpEvents(r io.Reader, dir string, stdout, stderr io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	enc := json.NewEncoder(stdout)
	// paths are the calls started but not yet ended, by call id: the path is on
	// the start — which a call that is refused still gets — and the outcome on
	// the end.
	paths := map[string]string{}
	var step *event
	var carry []ToolUse
	flush := func() error {
		if step == nil {
			return nil
		}
		e := *step
		step = nil
		return enc.Encode(e)
	}
	for sc.Scan() {
		var ev ompEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "session" && ev.ID != "":
			if err := enc.Encode(sessionEvent{Type: "session", SessionID: ev.ID}); err != nil {
				return err
			}
		case ev.Type == "tool_execution_start" && ev.ToolCallID != "":
			paths[ev.ToolCallID] = repoPath(dir, ev.Args.Path)
		case ev.Type == "tool_execution_end" && ev.ToolName != "":
			path := paths[ev.ToolCallID]
			delete(paths, ev.ToolCallID)
			// A call that failed or was refused never ran, so it is not what
			// the request did.
			if ev.IsError {
				continue
			}
			use := ToolUse{Tool: ev.ToolName, Path: path}
			if step == nil {
				// No usage was reported for this request, so its calls ride on the next step.
				carry = append(carry, use)
				continue
			}
			step.Part.Tools = append(step.Part.Tools, use)
		case ev.Type == "message_end" && ev.Message != nil && ev.Message.Role == "assistant":
			if err := flush(); err != nil {
				return err
			}
			if err := emitOmpText(enc, stderr, *ev.Message); err != nil {
				return err
			}
			step = ompStep(*ev.Message)
			if step != nil && len(carry) > 0 {
				step.Part.Tools, carry = carry, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(carry) > 0 {
		if step == nil {
			step = &event{Type: "step_finish"}
		}
		step.Part.Tools = append(step.Part.Tools, carry...)
	}
	return flush()
}

// emitOmpText writes one assistant message's text parts, and the turn-fatal
// error it reports in the message alone, which Classify reads on stderr.
func emitOmpText(enc *json.Encoder, stderr io.Writer, m ompMessage) error {
	for _, c := range m.Content {
		if c.Type == "text" && c.Text != "" {
			if err := enc.Encode(newTextEvent(c.Text)); err != nil {
				return err
			}
		}
	}
	if m.StopReason != "error" || m.ErrorMessage == "" {
		return nil
	}
	_, err := fmt.Fprintf(stderr, "omp error: %s\n", m.ErrorMessage)
	return err
}

// ompStep is one assistant message's own step — the request's tokens and cost —
// or nil for a message that carries no usage.
func ompStep(m ompMessage) *event {
	if m.Usage == nil {
		return nil
	}
	var e event
	e.Type = "step_finish"
	e.Part.Tokens.Input = m.Usage.Input
	e.Part.Tokens.Output = m.Usage.Output
	e.Part.Tokens.Reasoning = m.Usage.ReasoningTokens
	e.Part.Tokens.Cache.Read = m.Usage.CacheRead
	e.Part.Tokens.Cache.Write = m.Usage.CacheWrite
	e.Part.Cost = m.Usage.Cost.Total
	e.Part.Time = m.Timestamp
	e.Part.ContextTokens = m.Usage.Input + m.Usage.CacheRead
	return &e
}
