package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type fallbackAttempt struct{ model, agent, config string }

// unavailableOpencode writes a fake opencode that records each invocation's
// model, agent and config to a log, then fails as an unavailable model does.
func unavailableOpencode(t *testing.T) (bin string, attempts func() []fallbackAttempt) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake opencode is a shell script, which Windows cannot execute")
	}
	dir := t.TempDir()
	bin, logPath := filepath.Join(dir, "opencode"), filepath.Join(dir, "attempts.log")
	script := "#!/bin/sh\nmodel=; agent=\n" +
		"while [ $# -gt 0 ]; do\n  case \"$1\" in -m) model=$2; shift;; --agent) agent=$2; shift;; esac\n  shift\ndone\n" +
		"printf '%s|%s|%s\\n' \"$model\" \"$agent\" \"$OPENCODE_CONFIG_CONTENT\" >> '" + logPath + "'\n" +
		"cat > /dev/null\necho 'Error: no endpoints found for this model' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, func() []fallbackAttempt {
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		var got []fallbackAttempt
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			p := strings.SplitN(line, "|", 3)
			got = append(got, fallbackAttempt{p[0], p[1], p[2]})
		}
		return got
	}
}

func TestBuildFallbackRunsEachSubstitutedModelUnderTheSameAgentShape(t *testing.T) {
	wantModels := append([]string{DefaultModel()}, fallbackModels(DefaultModel())...)
	tests := []struct {
		name       string
		phase      Phase
		setup      func(bin string, deps *BuildDeps, c *BuildConfig)
		wantAgent  string
		wantConfig string
	}{
		{"build", PhaseBuild, func(bin string, deps *BuildDeps, c *BuildConfig) {
			deps.Agent = Opencode{Bin: bin, Model: DefaultModel()}
		}, "", ""},
		{"plan", PhasePlan, func(bin string, deps *BuildDeps, c *BuildConfig) {
			deps.PlanAgent = PlanOpencode(bin, DefaultModel())
			c.Ticket.Size = "M"
		}, planAgentName, planAgentConfig},
		{"review", PhaseReview, func(bin string, deps *BuildDeps, c *BuildConfig) {
			deps.ReviewAgent = ReviewOpencode(bin, DefaultModel())
			c.Model, c.ReviewModel = "p/m", DefaultModel()
		}, reviewAgentName, reviewAgentConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, attempts := unavailableOpencode(t)
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
				if a.agent != tt.wantAgent || a.config != tt.wantConfig {
					t.Fatalf("attempt %+v, want agent %q and its config on every model", a, tt.wantAgent)
				}
			}
			if !slices.Equal(gotModels, wantModels) {
				t.Fatalf("opencode ran with -m %v, want %v", gotModels, wantModels)
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
