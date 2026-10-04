package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// agentEnvNames are the variables the agent inherits; names ending in _ are prefixes.
var agentEnvNames = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "CI", "USER", "SHELL",
	"OPENCODE_API_KEY", "GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE", "GOTOOLCHAIN", "GOFLAGS",
	"LC_", "XDG_",
}

const agentKillGrace = 30 * time.Second

var maxEventLine = 64 << 20

// Usage is the token and cost total across every model step of one agent run.
type Usage struct {
	Input      int64   `firestore:"input" json:"input"`
	Output     int64   `firestore:"output" json:"output"`
	Reasoning  int64   `firestore:"reasoning" json:"reasoning"`
	CacheRead  int64   `firestore:"cache_read" json:"cache_read"`
	CacheWrite int64   `firestore:"cache_write" json:"cache_write"`
	Cost       float64 `firestore:"cost" json:"cost"`
	Steps      int     `firestore:"steps" json:"steps"`
}

type event struct {
	Type string `json:"type"`
	Part struct {
		Tokens struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			Reasoning int64 `json:"reasoning"`
			Cache     struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		Cost float64 `json:"cost"`
	} `json:"part"`
}

// SumUsage totals the step_finish events in the agent's JSON event stream.
//
// Lines that are not JSON events are skipped.
func SumUsage(r io.Reader) (Usage, error) {
	var u Usage
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != "step_finish" {
			continue
		}
		t := e.Part.Tokens
		u.Input += t.Input
		u.Output += t.Output
		u.Reasoning += t.Reasoning
		u.CacheRead += t.Cache.Read
		u.CacheWrite += t.Cache.Write
		u.Cost += e.Part.Cost
		u.Steps++
	}
	if err := sc.Err(); err != nil {
		return u, fmt.Errorf("reading events: %w", err)
	}
	return u, nil
}

// FinalText returns the last text part in the agent's JSON event stream, empty
// when none appeared.
//
// A text part carries the message accumulated so far rather than a delta, so
// the last one seen holds the final text; lines that are not JSON events are
// skipped, the same as SumUsage.
func FinalText(r io.Reader) (string, error) {
	var text string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	for sc.Scan() {
		var e struct {
			Type string `json:"type"`
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != "text" {
			continue
		}
		text = e.Part.Text
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading events: %w", err)
	}
	return text, nil
}

// SessionID returns the session id the agent's JSON event stream reports,
// read from the first event that carries one — every event does, including
// an error, so this works even on a run that never completed. Empty when
// none appeared.
func SessionID(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64*1024, maxEventLine)), maxEventLine)
	for sc.Scan() {
		var e struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.SessionID == "" {
			continue
		}
		return e.SessionID, nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading events: %w", err)
	}
	return "", nil
}

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
	cmd.Env = agentEnv(os.Environ())
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

func agentEnv(environ []string) []string {
	return filterEnv(environ, agentEnvNames)
}

// filterEnv narrows environ to the variables named in allowed — a name in
// allowed ending in "_" matches any variable it prefixes.
func filterEnv(environ []string, allowed []string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for _, a := range allowed {
			if name == a || (strings.HasSuffix(a, "_") && strings.HasPrefix(name, a)) {
				env = append(env, kv)
				break
			}
		}
	}
	return env
}
