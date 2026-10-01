package modelprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// rosterHeader matches the "provider/model" line opencode prints before each model's JSON.
var rosterHeader = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/\S+$`)

// RosterModel is one model on the roster.
type RosterModel struct {
	ID string
	// Free is true only when the roster states a cost and both input and output are zero.
	Free bool
}

// ParseRoster reads the output of `opencode models <provider> --verbose`: a
// "provider/model" line, then that model's JSON object at column zero.
//
// A missing cost block leaves a model not free, never free.
func ParseRoster(out []byte) ([]RosterModel, error) {
	var (
		models []RosterModel
		id     string
		body   strings.Builder
		inBody bool
	)
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case inBody:
			body.WriteString(line + "\n")
			if line == "}" {
				m, err := decodeRosterModel(id, body.String())
				if err != nil {
					return nil, err
				}
				models = append(models, m)
				inBody = false
			}
		case line == "{":
			if id == "" {
				return nil, fmt.Errorf("roster object with no model line before it")
			}
			body.Reset()
			body.WriteString(line + "\n")
			inBody = true
		case rosterHeader.MatchString(line):
			id = line
		}
	}
	if inBody {
		return nil, fmt.Errorf("roster ends inside the object for %s", id)
	}
	return models, nil
}

func decodeRosterModel(id, body string) (RosterModel, error) {
	var m struct {
		Cost *struct {
			Input  *float64 `json:"input"`
			Output *float64 `json:"output"`
		} `json:"cost"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return RosterModel{}, fmt.Errorf("decoding roster entry %s: %w", id, err)
	}
	free := m.Cost != nil && m.Cost.Input != nil && m.Cost.Output != nil && *m.Cost.Input == 0 && *m.Cost.Output == 0
	return RosterModel{ID: id, Free: free}, nil
}

// FreeModels returns the roster's free models on the Zen provider, in roster order.
func FreeModels(roster []RosterModel) []string {
	var free []string
	for _, m := range roster {
		if m.Free && runner.Provider(m.ID) == runner.ZenProvider {
			free = append(free, m.ID)
		}
	}
	return free
}

// ListRoster runs the opencode binary's roster listing for the Zen provider.
func ListRoster(ctx context.Context, bin string) ([]RosterModel, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "models", runner.ZenProvider, "--verbose")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("listing models: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseRoster(stdout.Bytes())
}
