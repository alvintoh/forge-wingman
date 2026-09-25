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

// SumUsage totals the step_finish events in opencode's JSON event stream.
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

// Opencode runs the opencode CLI non-interactively.
type Opencode struct {
	Bin   string
	Model string
}

// Run sends the prompt on stdin and streams the JSON events to stdout.
//
// The agent runs in its own process group, terminated when ctx ends and killed
// once Run returns.
func (o Opencode) Run(ctx context.Context, dir, prompt string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, o.Bin, "run", "--format", "json", "--auto", "-m", o.Model, "--dir", dir)
	cmd.Dir = dir
	cmd.Env = agentEnv(os.Environ())
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = agentKillGrace
	ownProcessGroup(cmd)
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("opencode run: %w", err)
	}
	err = cmd.Wait()
	killProcessGroup(cmd)
	if err != nil {
		return fmt.Errorf("opencode run: %w", err)
	}
	return nil
}

func agentEnv(environ []string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for _, allowed := range agentEnvNames {
			if name == allowed || (strings.HasSuffix(allowed, "_") && strings.HasPrefix(name, allowed)) {
				env = append(env, kv)
				break
			}
		}
	}
	return env
}
