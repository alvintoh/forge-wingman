package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// ompName is the key a plan's configuration selects the harness by, and the
// binary oh-my-pi installs (testdata/omp/PACKAGE.txt).
const ompName = "omp"

// ompPlan is the plan whose model ids the harness runs, ompProvider omp's own
// built-in provider for that plan.
const (
	ompPlan     = "command-code"
	ompProvider = "commandcode"
)

// ompKeyEnv is the variable omp's provider reads the plan's key from.
const ompKeyEnv = "COMMAND_CODE_API_KEY"

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

// ompModels renders the plan's output caps as omp's modelOverrides, keyed by the
// id omp itself knows the model by. Empty when the plan caps no model, so a plan
// without caps writes no file and keeps the harness's own default.
func ompModels(caps map[string]int) string {
	var overrides []string
	for model, maxTokens := range caps {
		id, ok := strings.CutPrefix(model, ompPlan+"/")
		if !ok || id == "" {
			continue
		}
		overrides = append(overrides, fmt.Sprintf("      %q:\n        maxTokens: %d\n", id, maxTokens))
	}
	if len(overrides) == 0 {
		return ""
	}
	slices.Sort(overrides)
	return "providers:\n  " + ompProvider + ":\n    modelOverrides:\n" + strings.Join(overrides, "")
}

func init() {
	keyEnv := providers.KeySecret(ompPlan)
	registerHarness(ompName, func(getenv func(string) string) Harness {
		var home string
		if tmp := getenv("RUNNER_TEMP"); tmp != "" {
			home = filepath.Join(tmp, "omp-home")
		}
		return OmpHarness{Bin: ompName, Key: getenv(keyEnv), Home: home}
	}, keyEnv)
}

// OmpHarness runs the oh-my-pi agent CLI on the plan's key.
type OmpHarness struct {
	Bin string
	Key string
	// Home is the CLI's HOME for the whole run: it holds the agent directory and
	// the sessions a later round resumes.
	Home string
}

// Name is the harness's own name, the key a plan's configuration selects it by.
func (h OmpHarness) Name() string { return ompName }

// Agent returns the omp agent for profile p.
func (h OmpHarness) Agent(p Profile, model string) Agent {
	return OmpAgent{Bin: h.Bin, Key: h.Key, Home: h.Home, Model: model, Profile: p}
}

// Ready reports the harness usable only when its key is present (AC7).
func (h OmpHarness) Ready() error {
	if h.Key == "" {
		return fmt.Errorf("the omp harness needs the %s secret", providers.KeySecret(ompPlan))
	}
	return nil
}

// Classify classifies a failed run by the assumed provider markers: omp exits 1
// for every turn-fatal error, so its exit code carries no class of its own.
func (h OmpHarness) Classify(stderrTail string, _ error) (Outcome, StopReason) {
	return classifyMarkers(stderrTail)
}

// OmpAgent runs the omp CLI for one profile.
type OmpAgent struct {
	Bin     string
	Key     string
	Home    string
	Model   string
	Profile Profile
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
	rest, ok := strings.CutPrefix(a.Model, ompPlan+"/")
	if !ok || rest == "" {
		return fmt.Errorf("the omp harness runs only %s models, not %q", ompPlan, a.Model)
	}
	agentDir := filepath.Join(a.Home, ompAgentDir)
	if err := writeAgentFile(filepath.Join(agentDir, ompRulesFile), rules); err != nil {
		return fmt.Errorf("omp rules: %w", err)
	}
	if err := writeAgentFile(filepath.Join(agentDir, ompConfigFile), ompConfig); err != nil {
		return fmt.Errorf("omp config: %w", err)
	}
	if body := ompModels(providers.OutputCaps(ompPlan)); body != "" {
		if err := writeAgentFile(filepath.Join(agentDir, ompModelsFile), body); err != nil {
			return fmt.Errorf("omp models: %w", err)
		}
	}
	approval := "always-ask"
	if a.Profile == ProfileBuild {
		approval = "yolo"
	}
	args := []string{"-p", "--mode", "json", "--model", ompProvider + "/" + rest, "--no-extensions", "--no-skills",
		"--session-dir", filepath.Join(a.Home, ompSessionDir), "--approval-mode", approval}
	if session != "" {
		args = append(args, "--resume", session)
	}
	cmd := exec.CommandContext(ctx, a.Bin, args...)
	cmd.Dir = dir
	cmd.Env = append(filterEnv(os.Environ(), agentBaseEnvNames),
		"HOME="+a.Home, "PI_CODING_AGENT_DIR="+agentDir, ompKeyEnv+"="+a.Key)
	cmd.Stdin = strings.NewReader(prompt)
	return runAgentCLI(cmd, a.Home, stderr, func(events io.Reader, _ error) error {
		return translateOmpEvents(events, stdout, stderr)
	})
}

// ompEvent is one line of omp's JSON output: the session line, or an event
// carrying a finished message.
type ompEvent struct {
	Type    string      `json:"type"`
	ID      string      `json:"id"`
	Message *ompMessage `json:"message"`
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
// stamped with the request's time. A turn-fatal error, which omp reports only
// in the message, is echoed to stderr, where Classify reads it (AC3).
func translateOmpEvents(r io.Reader, stdout, stderr io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	enc := json.NewEncoder(stdout)
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
		case ev.Type == "message_end" && ev.Message != nil && ev.Message.Role == "assistant":
			if err := emitOmpMessage(enc, stderr, *ev.Message); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func emitOmpMessage(enc *json.Encoder, stderr io.Writer, m ompMessage) error {
	for _, c := range m.Content {
		if c.Type == "text" && c.Text != "" {
			if err := enc.Encode(newTextEvent(c.Text)); err != nil {
				return err
			}
		}
	}
	if m.StopReason == "error" && m.ErrorMessage != "" {
		if _, err := fmt.Fprintf(stderr, "omp error: %s\n", m.ErrorMessage); err != nil {
			return err
		}
	}
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
	return enc.Encode(e)
}
