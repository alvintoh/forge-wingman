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
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// opencodeName is the key a plan's configuration selects the harness by, and
// the binary opencode-ai installs (testdata/opencode/PACKAGE.txt).
const opencodeName = "opencode"

// opencodePlan is the plan whose model ids the harness runs, and the id of the
// provider its config defines for them, so a model id passes through unchanged.
const opencodePlan = "command-code"

// opencodeKeyEnv is the variable the config's provider reads the plan's key from.
const opencodeKeyEnv = "COMMAND_CODE_API_KEY"

// opencodeReadOnlyAgent is the agent the plan and review profiles select: a
// primary agent whose edit, bash and task permissions are denied.
const opencodeReadOnlyAgent = "wingman-readonly"

const (
	opencodeConfigFile = "opencode.json"
	opencodeRulesFile  = "rules.md"
)

func init() {
	keyEnv := providers.KeySecret(opencodePlan)
	registerHarness(opencodeName, func(getenv func(string) string) Harness {
		var home string
		if tmp := getenv("RUNNER_TEMP"); tmp != "" {
			home = filepath.Join(tmp, "opencode-home")
		}
		return OpencodeHarness{Bin: opencodeName, Key: getenv(keyEnv), Home: home}
	}, keyEnv)
}

// OpencodeHarness runs the opencode agent CLI on the plan's key.
type OpencodeHarness struct {
	Bin string
	Key string
	// Home is the CLI's HOME and every XDG directory for the whole run: it holds
	// the config and the sessions a later round resumes.
	Home string
}

// Name is the harness's own name, the key a plan's configuration selects it by.
func (h OpencodeHarness) Name() string { return opencodeName }

// Agent returns the opencode agent for profile p.
func (h OpencodeHarness) Agent(p Profile, model string) Agent {
	return OpencodeAgent{Bin: h.Bin, Key: h.Key, Home: h.Home, Model: model, Profile: p}
}

// Ready reports the harness usable only when its key is present (AC7).
func (h OpencodeHarness) Ready() error {
	if h.Key == "" {
		return fmt.Errorf("the opencode harness needs the %s secret", providers.KeySecret(opencodePlan))
	}
	return nil
}

// Classify classifies a failed run by the assumed provider markers: opencode
// exits 1 for every failure, so its exit code carries no class of its own.
func (h OpencodeHarness) Classify(stderrTail string, _ error) (Outcome, StopReason) {
	return classifyMarkers(stderrTail)
}

// OpencodeAgent runs the opencode CLI for one profile.
type OpencodeAgent struct {
	Bin     string
	Key     string
	Home    string
	Model   string
	Profile Profile
}

// WithModel returns a copy of a that runs model, keeping its profile.
func (a OpencodeAgent) WithModel(model string) Agent { a.Model = model; return a }

// Run sends the prompt on stdin and translates opencode's JSON events to the
// runner's own event shape on stdout. rules, when non-empty, is the first
// instruction file the config names; an empty rules removes it. A non-empty
// session is resumed from the sessions under Home. Only the build profile
// passes --auto; the others select the read-only agent. The key reaches
// opencode in its environment alone, never on disk.
func (a OpencodeAgent) Run(ctx context.Context, dir, session, prompt, rules string, stdout, stderr io.Writer) error {
	if a.Home == "" {
		return errors.New("the opencode harness has no home directory")
	}
	rest, ok := strings.CutPrefix(a.Model, opencodePlan+"/")
	if !ok || rest == "" {
		return fmt.Errorf("the opencode harness runs only %s models, not %q", opencodePlan, a.Model)
	}
	// A global config left by an earlier round could add an MCP server to this
	// one; the sessions a resume needs live in the data directory instead.
	if err := os.RemoveAll(filepath.Join(a.Home, ".config", "opencode")); err != nil {
		return fmt.Errorf("opencode global config: %w", err)
	}
	rulesPath, configPath := filepath.Join(a.Home, opencodeRulesFile), filepath.Join(a.Home, opencodeConfigFile)
	if err := writeAgentFile(rulesPath, rules); err != nil {
		return fmt.Errorf("opencode rules: %w", err)
	}
	if rules == "" {
		rulesPath = ""
	}
	config, err := opencodeConfig(rest, rulesPath, filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		return fmt.Errorf("opencode config: %w", err)
	}
	if err := writeAgentFile(configPath, config); err != nil {
		return fmt.Errorf("opencode config: %w", err)
	}
	args := []string{"run", "--format", "json", "--pure", "-m", a.Model, "--dir", dir}
	if a.Profile == ProfileBuild {
		args = append(args, "--auto")
	} else {
		args = append(args, "--agent", opencodeReadOnlyAgent)
	}
	if session != "" {
		args = append(args, "-s", session)
	}
	cmd := exec.CommandContext(ctx, a.Bin, args...)
	cmd.Dir = dir
	cmd.Env = append(filterEnv(os.Environ(), agentBaseEnvNames),
		"HOME="+a.Home,
		"XDG_CONFIG_HOME="+filepath.Join(a.Home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(a.Home, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(a.Home, ".cache"),
		"XDG_STATE_HOME="+filepath.Join(a.Home, ".local", "state"),
		"OPENCODE_CONFIG="+configPath,
		"OPENCODE_DISABLE_CLAUDE_CODE=1",
		// A repository's own opencode.json or .opencode directory would
		// otherwise override this config, the read-only agent's denies included.
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		opencodeKeyEnv+"="+a.Key)
	cmd.Stdin = strings.NewReader(prompt)
	return runAgentCLI(cmd, a.Home, stderr, func(events io.Reader, _ error) error {
		return translateOpencodeEvents(events, stdout, stderr)
	})
}

// opencodeConfigDoc is the part of opencode's config file the adapter writes.
type opencodeConfigDoc struct {
	Autoupdate   bool                           `json:"autoupdate"`
	Share        string                         `json:"share"`
	Provider     map[string]opencodeProviderDoc `json:"provider"`
	Instructions []string                       `json:"instructions"`
	Agent        map[string]opencodeAgentDoc    `json:"agent"`
}

type opencodeProviderDoc struct {
	NPM     string `json:"npm"`
	Options struct {
		BaseURL string `json:"baseURL"`
		APIKey  string `json:"apiKey"`
	} `json:"options"`
	Models map[string]struct{} `json:"models"`
}

type opencodeAgentDoc struct {
	Mode       string            `json:"mode"`
	Permission map[string]string `json:"permission"`
}

// opencodeConfig is the config for a run of model, the plan's own id for it:
// the plan's provider, whose key the config names but never holds, the rules
// file when there is one ahead of the repository's CLAUDE.md, and the
// read-only agent. claudeMD is absolute: with project config disabled,
// opencode no longer finds a relative instruction file in the repository.
func opencodeConfig(model, rulesPath, claudeMD string) (string, error) {
	provider := opencodeProviderDoc{NPM: "@ai-sdk/openai-compatible", Models: map[string]struct{}{model: {}}}
	provider.Options.BaseURL = providers.BaseURL(opencodePlan)
	provider.Options.APIKey = "{env:" + opencodeKeyEnv + "}"
	doc := opencodeConfigDoc{
		Share:        "disabled",
		Provider:     map[string]opencodeProviderDoc{opencodePlan: provider},
		Instructions: []string{claudeMD},
		Agent: map[string]opencodeAgentDoc{opencodeReadOnlyAgent: {
			Mode:       "primary",
			Permission: map[string]string{"edit": "deny", "bash": "deny", "task": "deny"},
		}},
	}
	if rulesPath != "" {
		doc.Instructions = append([]string{rulesPath}, doc.Instructions...)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// opencodeEvent is one line of opencode's JSON output.
type opencodeEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Text   string          `json:"text"`
		Tokens *opencodeTokens `json:"tokens"`
		Cost   float64         `json:"cost"`
	} `json:"part"`
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// opencodeTokens is one step's tokens: Input excludes Cache.Read, and Output
// excludes Reasoning.
type opencodeTokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

// translateOpencodeEvents rewrites opencode's events as the runner's own event
// shape: the session, each text part, and one step per step_finish, stamped
// with the event's time and with reasoning counted in its output. An error
// event is echoed to stderr, where Classify reads it (AC3).
func translateOpencodeEvents(r io.Reader, stdout, stderr io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	enc := json.NewEncoder(stdout)
	var session string
	for sc.Scan() {
		var ev opencodeEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if session == "" && ev.SessionID != "" {
			session = ev.SessionID
			if err := enc.Encode(sessionEvent{Type: "session", SessionID: session}); err != nil {
				return err
			}
		}
		if err := emitOpencodeEvent(enc, stderr, ev); err != nil {
			return err
		}
	}
	return sc.Err()
}

func emitOpencodeEvent(enc *json.Encoder, stderr io.Writer, ev opencodeEvent) error {
	switch {
	case ev.Type == "text" && ev.Part.Text != "":
		return enc.Encode(newTextEvent(ev.Part.Text))
	case ev.Type == "step_finish" && ev.Part.Tokens != nil:
		t := ev.Part.Tokens
		var e event
		e.Type = "step_finish"
		e.Part.Tokens.Input = t.Input
		e.Part.Tokens.Output = t.Output + t.Reasoning
		e.Part.Tokens.Reasoning = t.Reasoning
		e.Part.Tokens.Cache.Read = t.Cache.Read
		e.Part.Tokens.Cache.Write = t.Cache.Write
		e.Part.Cost = ev.Part.Cost
		e.Part.Time = ev.Timestamp
		return enc.Encode(e)
	case ev.Type == "error" && ev.Error != nil:
		_, err := fmt.Fprintf(stderr, "opencode error: %s: %s\n", ev.Error.Name, ev.Error.Data.Message)
		return err
	}
	return nil
}
