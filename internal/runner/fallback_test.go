package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fallbackAttempt is one invocation the scripted CLI recorded: our model id and
// the approval mode it ran under.
type fallbackAttempt struct{ model, flag string }

// unavailableCLIAgent is scriptedCLIAgent failing every model as an unavailable one does.
func unavailableCLIAgent(t *testing.T) (bin string, attempts func() []fallbackAttempt) {
	t.Helper()
	return scriptedCLIAgent(t, "Error: no endpoints found for this model", "")
}

// scriptedCLIAgent writes a fake omp CLI that records each invocation's model
// and approval mode to a log, then exits 1 printing stderrMsg, except for
// okModel, which answers with a plan naming version.go.
func scriptedCLIAgent(t *testing.T, stderrMsg, okModel string) (bin string, attempts func() []fallbackAttempt) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake omp CLI is a shell script, which Windows cannot execute")
	}
	dir := t.TempDir()
	bin, logPath, planPath := filepath.Join(dir, "omp"), filepath.Join(dir, "attempts.log"), filepath.Join(dir, "plan.ndjson")
	script := "#!/bin/sh\nmodel=; flag=\n" +
		"while [ $# -gt 0 ]; do\n  case \"$1\" in --model) model=command-code/${2#commandcode/}; shift;; --approval-mode) flag=$2; shift;; esac\n  shift\ndone\n" +
		"printf '%s|%s\\n' \"$model\" \"$flag\" >> '" + logPath + "'\n" +
		"cat > /dev/null\n" +
		"if [ \"$model\" = '" + okModel + "' ]; then cat '" + planPath + "'; exit 0; fi\n" +
		"echo '" + stderrMsg + "' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	result := `{"type":"session","id":"ses_1"}` + "\n" +
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` +
		strconv.Quote("plan\n\n```plan-files\nversion.go\n```") + `}]}}` + "\n"
	if err := os.WriteFile(planPath, []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
	return bin, func() []fallbackAttempt {
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		var got []fallbackAttempt
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			p := strings.SplitN(line, "|", 2)
			got = append(got, fallbackAttempt{p[0], p[1]})
		}
		return got
	}
}

// scriptedAgent is the real omp agent for profile p, run through bin.
func scriptedAgent(t *testing.T, bin string, p Profile, model string) OmpAgent {
	t.Helper()
	return OmpAgent{Bin: bin, Key: "k", Home: t.TempDir(), Model: model, Profile: p}
}

func TestBuildFallbackRunsEachSubstitutedModelUnderTheSameAgentShape(t *testing.T) {
	wantModels := append([]string{DefaultModel()}, fallbackModels(DefaultModel())...)
	tests := []struct {
		name     string
		phase    Phase
		setup    func(bin string, deps *BuildDeps, c *BuildConfig)
		wantFlag string
	}{
		{"build", PhaseBuild, func(bin string, deps *BuildDeps, c *BuildConfig) {
			deps.Agent = scriptedAgent(t, bin, ProfileBuild, DefaultModel())
		}, "yolo"},
		{"plan", PhasePlan, func(bin string, deps *BuildDeps, c *BuildConfig) {
			deps.PlanAgent = scriptedAgent(t, bin, ProfilePlan, DefaultModel())
			c.Ticket.Size = "M"
			c.PlanModels = wantModels
		}, "always-ask"},
		{"review", PhaseReview, func(bin string, deps *BuildDeps, c *BuildConfig) {
			deps.ReviewAgent = scriptedAgent(t, bin, ProfileReview, DefaultModel())
			c.Model, c.ReviewModels = "p/m", wantModels
		}, "always-ask"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, attempts := unavailableCLIAgent(t)
			objects := validObjects()
			objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
			deps, _, reported := testDeps(objects, &fakeAgent{edit: edit("version.go", "package x\n")})
			c := testConfig(t, initRepo(t))
			c.Model = DefaultModel()
			tt.setup(bin, &deps, &c)
			if tt.phase == PhaseReview {
				deps.Agent = &fakeAgent{edit: edit("version.go", "package x\n")}
			}

			_, _ = Build(context.Background(), deps, c)
			got := attempts()
			var gotModels, stepModels []string
			for _, a := range got {
				gotModels = append(gotModels, a.model)
				if a.flag != tt.wantFlag {
					t.Fatalf("attempt %+v, want %s on every model", a, tt.wantFlag)
				}
			}
			if !slices.Equal(gotModels, wantModels) {
				t.Fatalf("the CLI ran models %v, want %v", gotModels, wantModels)
			}
			for _, st := range reported.last(t).Steps {
				if st.Phase == tt.phase {
					stepModels = append(stepModels, st.Model)
				}
			}
			if !slices.Equal(stepModels, gotModels) {
				t.Fatalf("recorded step models %v, want the models actually run %v", stepModels, gotModels)
			}
		})
	}
}

func TestBuildGivesEachPlanAttemptWhatRemainsOfTheRunBudget(t *testing.T) {
	objects := validObjects()
	objects["projections/"+testSHA+"/"+planProjectionFile] = []byte("# Plan rules\n\n" + ticketSentinel)
	planAgent := &fakeAgent{stderr: "Error: no endpoints found for this model", err: errors.New("exit 1")}
	deps, _, _ := testDeps(objects, &fakeAgent{edit: edit("version.go", "package x\n")})
	deps.PlanAgent = planAgent
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	deps.Now = func() time.Time { return now }
	// The first attempt consumes 20 minutes of the 55 the check loop may use.
	planAgent.errFn = func(int) error { now = now.Add(20 * time.Minute); return errors.New("exit 1") }
	c := testConfig(t, initRepo(t))
	c.Ticket.Size = "M"
	c.PlanModels = []string{"p/a", "p/b"}

	_, _ = Build(context.Background(), deps, c)

	if len(planAgent.deadlines) != 2 {
		t.Fatalf("plan agent ran %d times, want 2", len(planAgent.deadlines))
	}
	// Both deadlines were set within milliseconds of each other in real time,
	// so their gap is the difference between the two attempts' timeouts.
	if gap := planAgent.deadlines[0].Sub(planAgent.deadlines[1]); gap < 14*time.Minute || gap > 16*time.Minute {
		t.Fatalf("first attempt's deadline is %s past the second's, want the 15m the first attempt's 20m cost off a 50m window", gap)
	}
}
