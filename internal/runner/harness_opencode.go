package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// opencodeName is the key a plan's configuration selects the harness by, and
// the binary opencode-ai installs (testdata/opencode/PACKAGE.txt).
const opencodeName = "opencode"

// opencodeLicence is opencode's own licence: opencode-ai is MIT
// (tools/opencode/package-lock.json).
const opencodeLicence = LicenceOpenSource

// opencodeReadOnlyAgent is the agent the plan and review profiles select: a
// primary agent whose edit, bash and task permissions are denied.
const opencodeReadOnlyAgent = "wingman-readonly"

// opencodeKeyEnvFor maps a plan to the variable opencode's config names its key
// by. It is the CLI's own name for the plans that ran before the plan became a
// configuration entry; a plan absent here reads its key under the plan's own
// secret name, which the config names without opencode knowing the plan.
var opencodeKeyEnvFor = map[string]string{
	"command-code": "COMMAND_CODE_API_KEY",
}

// opencodeKeyEnv is the variable opencode's config names plan's key by, and the
// one the run sets in its environment.
func opencodeKeyEnv(plan string) string {
	if name, ok := opencodeKeyEnvFor[plan]; ok {
		return name
	}
	return providers.KeySecret(plan)
}

const (
	opencodeConfigFile = "opencode.json"
	opencodeRulesFile  = "rules.md"
)

// opencodeConfigPath is the config a round writes under the run's home, and the
// file OPENCODE_CONFIG names so opencode reads no other.
func opencodeConfigPath(home string) string { return filepath.Join(home, opencodeConfigFile) }

// opencodeEnv is the environment opencode runs under: the run's own home, every
// XDG directory inside it, the config a round writes there, and the switches
// that keep it from reading another's config, agents and updates.
func opencodeEnv(home string) []string {
	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"OPENCODE_CONFIG=" + opencodeConfigPath(home),
		"OPENCODE_DISABLE_CLAUDE_CODE=1",
		// A repository's own opencode.json or .opencode directory would
		// otherwise override this config, the read-only agent's denies included.
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
	}
}

func init() {
	registerHarness(opencodeName, func(getenv func(string) string) Harness {
		var home string
		if tmp := getenv("RUNNER_TEMP"); tmp != "" {
			home = filepath.Join(tmp, "opencode-home")
		}
		return OpencodeHarness{Bin: opencodeName, Getenv: getenv, Home: home}
	})
}

// OpencodeHarness runs the opencode agent CLI on each plan's own key.
type OpencodeHarness struct {
	Bin string
	// Key is the key the harness runs on when it is built directly, without an
	// environment to resolve a plan's own secret from.
	Key string
	// Getenv resolves a plan's key secret when the harness is built from the
	// environment, so one harness serves every plan opencode is configured for.
	Getenv func(string) string
	// Home is the CLI's HOME and every XDG directory for the whole run: it holds
	// the config and the sessions a later round resumes.
	Home string
	// Container, when it names an image, runs opencode in a container.
	Container AgentContainer
}

// keyFor is the key the harness runs plan on: its own resolved key when built
// directly, else the plan's secret read from the environment.
func (h OpencodeHarness) keyFor(plan string) string {
	if h.Getenv == nil {
		return h.Key
	}
	return h.Getenv(providers.KeySecret(plan))
}

// Name is the harness's own name, the key a plan's configuration selects it by.
func (h OpencodeHarness) Name() string { return opencodeName }

// Licence is the harness's own licence, recorded here alone.
func (h OpencodeHarness) Licence() Licence { return opencodeLicence }

// Agent returns the opencode agent for profile p, keyed for the model's plan.
func (h OpencodeHarness) Agent(p Profile, model string) Agent {
	return OpencodeAgent{Bin: h.Bin, Key: h.keyFor(Provider(model)), Home: h.Home, Model: model, Profile: p, Container: h.Container}
}

func (h OpencodeHarness) withContainer(c AgentContainer) Harness { h.Container = c; return h }

func (h OpencodeHarness) startUpCheck() agentSpec {
	return agentSpec{Bin: h.Bin, Args: []string{"--version"}, Home: h.Home, Env: opencodeEnv(h.Home)}
}

// Ready reports the harness usable only when plan's key is present (AC7).
func (h OpencodeHarness) Ready(plan string) error {
	if h.keyFor(plan) == "" {
		return fmt.Errorf("the opencode harness needs the %s secret", providers.KeySecret(plan))
	}
	return nil
}

// Classify classifies a failed run by the assumed provider markers: opencode
// exits 1 for every failure, so its exit code carries no class of its own, bar
// docker failing to run it in a container.
func (h OpencodeHarness) Classify(stderrTail string, exitErr error) (Outcome, StopReason) {
	if h.Container.failedToRun(exitErr) {
		return OutcomeInfraFailure, StopAgentContainer
	}
	return classifyMarkers(stderrTail)
}

// OpencodeAgent runs the opencode CLI for one profile.
type OpencodeAgent struct {
	Bin     string
	Key     string
	Home    string
	Model   string
	Profile Profile
	// Container, when it names an image, runs opencode in a container.
	Container AgentContainer
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
	plan := Provider(a.Model)
	rest, ok := strings.CutPrefix(a.Model, plan+"/")
	if !ok || rest == "" || providers.KeySecret(plan) == "" {
		return fmt.Errorf("the opencode harness has no configuration for plan %q", plan)
	}
	// opencode reads config, agents and plugins from several places under HOME,
	// any of which an earlier build round could have written; only the
	// sessions a resume needs survive.
	if err := removeAllExcept(a.Home, filepath.Join(a.Home, opencodeSessionDir)); err != nil {
		return fmt.Errorf("opencode home: %w", err)
	}
	rulesPath, configPath := filepath.Join(a.Home, opencodeRulesFile), opencodeConfigPath(a.Home)
	if err := writeAgentFile(rulesPath, rules); err != nil {
		return fmt.Errorf("opencode rules: %w", err)
	}
	if rules == "" {
		rulesPath = ""
	}
	config, err := opencodeConfig(plan, rest, rulesPath, filepath.Join(dir, "CLAUDE.md"), a.Profile != ProfileBuild)
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
	cmd, err := agentCommand(ctx, a.Container, agentSpec{Bin: a.Bin, Args: args, Dir: dir, Home: a.Home,
		Env: opencodeEnv(a.Home), Secret: opencodeKeyEnv(plan) + "=" + a.Key})
	if err != nil {
		return fmt.Errorf("opencode command: %w", err)
	}
	cmd.Stdin = strings.NewReader(prompt)
	watched := &markerWriter{w: stderr, marker: []byte(opencodeFallbackMarker)}
	return runAgentCLI(cmd, a.Home, watched, func(events io.Reader, _ error) error {
		if err := translateOpencodeEvents(events, dir, stdout, stderr); err != nil {
			return err
		}
		if watched.seen {
			return errOpencodeFallback
		}
		return nil
	})
}

// opencodeSessionDir is where opencode keeps its sessions under Home: its
// XDG_DATA_HOME directory.
var opencodeSessionDir = filepath.Join(".local", "share", "opencode")

// opencodeFallbackMarker is what opencode prints when the agent a run names
// does not exist, before running its default agent, which may edit.
const opencodeFallbackMarker = "Falling back to default agent"

var errOpencodeFallback = errors.New("opencode ran its default agent in place of the one named")

// removeAllExcept removes everything under root but keep, a path inside it. A
// symlink on the way to keep is removed rather than followed.
func removeAllExcept(root, keep string) error {
	rel, err := filepath.Rel(root, keep)
	if err != nil {
		return err
	}
	dir := root
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Name() != name {
				if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
					return err
				}
			}
		}
		dir = filepath.Join(dir, name)
		if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
			return os.Remove(dir)
		}
	}
	return nil
}

// markerWriter passes writes through to w and records whether marker appeared,
// even split across writes.
type markerWriter struct {
	w      io.Writer
	marker []byte
	tail   []byte
	seen   bool
}

func (m *markerWriter) Write(p []byte) (int, error) {
	buf := append(m.tail, p...)
	if bytes.Contains(buf, m.marker) {
		m.seen = true
	}
	m.tail = append([]byte(nil), buf[max(0, len(buf)-len(m.marker)+1):]...)
	return m.w.Write(p)
}

// opencodeConfigDoc is the part of opencode's config file the adapter writes.
type opencodeConfigDoc struct {
	Autoupdate   bool                           `json:"autoupdate"`
	Share        string                         `json:"share"`
	Provider     map[string]opencodeProviderDoc `json:"provider"`
	Instructions []string                       `json:"instructions"`
	Permission   map[string]string              `json:"permission,omitempty"`
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
// opencode no longer finds a relative instruction file in the repository. A
// readOnly config denies at the top level too, so the default agent opencode
// falls back to cannot edit either.
func opencodeConfig(plan, model, rulesPath, claudeMD string, readOnly bool) (string, error) {
	provider := opencodeProviderDoc{NPM: "@ai-sdk/openai-compatible", Models: map[string]struct{}{model: {}}}
	provider.Options.BaseURL = providers.BaseURL(plan)
	provider.Options.APIKey = "{env:" + opencodeKeyEnv(plan) + "}"
	doc := opencodeConfigDoc{
		Share:        "disabled",
		Provider:     map[string]opencodeProviderDoc{plan: provider},
		Instructions: []string{claudeMD},
		Agent: map[string]opencodeAgentDoc{opencodeReadOnlyAgent: {
			Mode:       "primary",
			Permission: opencodeReadOnlyDenies(),
		}},
	}
	if readOnly {
		doc.Permission = opencodeReadOnlyDenies()
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

func opencodeReadOnlyDenies() map[string]string {
	return map[string]string{"edit": "deny", "bash": "deny", "task": "deny"}
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
		Tool   string          `json:"tool"`
		State  struct {
			Status string `json:"status"`
			Input  struct {
				FilePath string `json:"filePath"`
			} `json:"input"`
		} `json:"state"`
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

// opencodeToolCompleted is the state a tool call's part reaches once it has
// run: one still running, or one that failed, is not what the request did.
const opencodeToolCompleted = "completed"

// translateOpencodeEvents rewrites opencode's events as the runner's own event
// shape: the session, each text part, and one step per step_finish, stamped
// with the event's time, with reasoning counted in its output and with the tool
// calls that request made — this harness reports a call before the step it
// belongs to, so the calls seen since the last step go onto the next one. An
// error event is echoed to stderr, where Classify reads it (AC3); the fallback
// warning on a line of its own fails the run.
func translateOpencodeEvents(r io.Reader, dir string, stdout, stderr io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	enc := json.NewEncoder(stdout)
	var session string
	var fellBack bool
	var tools []ToolUse
	for sc.Scan() {
		var ev opencodeEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			fellBack = fellBack || bytes.Contains(sc.Bytes(), []byte(opencodeFallbackMarker))
			continue
		}
		if session == "" && ev.SessionID != "" {
			session = ev.SessionID
			if err := enc.Encode(sessionEvent{Type: "session", SessionID: session}); err != nil {
				return err
			}
		}
		switch {
		case ev.Type == "tool_use":
			if tool, ok := opencodeToolUse(ev, dir); ok {
				tools = append(tools, tool)
			}
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
			e.Part.Tools = tools
			e.Part.ContextTokens = t.Input + t.Cache.Read
			tools = nil
			if err := enc.Encode(e); err != nil {
				return err
			}
		case ev.Type == "text" && ev.Part.Text != "":
			if err := enc.Encode(newTextEvent(ev.Part.Text)); err != nil {
				return err
			}
		case ev.Type == "error" && ev.Error != nil:
			if _, err := fmt.Fprintf(stderr, "opencode error: %s: %s\n", ev.Error.Name, ev.Error.Data.Message); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if fellBack {
		return errOpencodeFallback
	}
	return nil
}

// opencodeToolUse is one tool call a step made, when the call ran to
// completion. A tool that names no file is reported without a path.
func opencodeToolUse(ev opencodeEvent, dir string) (ToolUse, bool) {
	if ev.Part.Tool == "" || ev.Part.State.Status != opencodeToolCompleted {
		return ToolUse{}, false
	}
	return ToolUse{Tool: ev.Part.Tool, Path: repoPath(dir, ev.Part.State.Input.FilePath)}, true
}
