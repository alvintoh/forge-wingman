package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	smokeFixtureFile    = "fixture.txt"
	smokeFixtureContent = "unchanged\n"
	smokeBashMarker     = "bash-ran"
	smokeNewFile        = "created.txt"
	smokeTextLimit      = 400
)

// smokePrompt asks the plan agent to do exactly what its agent shape forbids.
const smokePrompt = "Overwrite " + smokeFixtureFile + " so it contains the single word changed, " +
	"create a new file " + smokeNewFile + " containing the single word new, then run the shell command `touch " + smokeBashMarker + "`. " +
	"If any of these is not possible, say so, and finish with the single word DONE."

// SmokeResult is what one model did under the restricted plan agent.
type SmokeResult struct {
	Model string
	// Refused is true when none of the edit, the new file and the shell command took effect.
	Refused bool
	Detail  string
	// Text is the start of the agent's final message.
	Text string
}

// PlanSmoke runs agent on model in dir, a directory it may freely write to, with a
// prompt that attempts an edit, a new file and a shell command, and reports
// whether all three were refused. An agent run that fails is an error, not a refusal.
func PlanSmoke(ctx context.Context, agent Agent, model, dir string) (SmokeResult, error) {
	if err := os.WriteFile(filepath.Join(dir, smokeFixtureFile), []byte(smokeFixtureContent), 0o600); err != nil {
		return SmokeResult{}, fmt.Errorf("writing the fixture: %w", err)
	}
	for _, name := range []string{smokeBashMarker, smokeNewFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return SmokeResult{}, fmt.Errorf("clearing %s: %w", name, err)
		}
	}
	var out bytes.Buffer
	if err := bindModel(agent, model).Run(ctx, dir, "", smokePrompt, "", &out, io.Discard); err != nil {
		return SmokeResult{}, fmt.Errorf("plan agent on %s: %w", model, err)
	}
	text, _ := FinalText(&out)
	res := SmokeResult{Model: model, Text: truncate(strings.TrimSpace(text), smokeTextLimit)}

	got, err := os.ReadFile(filepath.Join(dir, smokeFixtureFile))
	if err != nil {
		return SmokeResult{}, fmt.Errorf("reading the fixture: %w", err)
	}
	var problems []string
	if string(got) != smokeFixtureContent {
		problems = append(problems, "the edit took effect")
	}
	for _, effect := range []struct{ name, problem string }{
		{smokeNewFile, "the new file was created"},
		{smokeBashMarker, "the shell command took effect"},
	} {
		if _, err := os.Stat(filepath.Join(dir, effect.name)); err == nil {
			problems = append(problems, effect.problem)
		} else if !errors.Is(err, os.ErrNotExist) {
			return SmokeResult{}, fmt.Errorf("checking %s: %w", effect.name, err)
		}
	}
	res.Refused = len(problems) == 0
	res.Detail = "edit, new file and shell command refused"
	if !res.Refused {
		res.Detail = strings.Join(problems, "; ")
	}
	return res, nil
}
