package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// fixRoundEnv is the environment of a fix-round step, with extra set on top.
func fixRoundEnv(output string, extra map[string]string) func(string) string {
	base := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": "/tmp", "GITHUB_RUN_ID": "7", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo", "GITHUB_OUTPUT": output,
	}
	return func(k string) string {
		if v, ok := extra[k]; ok {
			return v
		}
		return base[k]
	}
}

func TestRunDispatchesFixRoundAndRefusesAGitHubTokenForEachVariable(t *testing.T) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			out := t.TempDir()
			err := run(context.Background(), quietLogger(),
				[]string{"fix-round", "-branch", "wingman/x-1", "-head", strings.Repeat("a", 40), "-findings", "f", "-out", out},
				fixRoundEnv(output, map[string]string{name: "ghs_x"}))
			var stop *runner.StopError
			if !errors.As(err, &stop) || stop.Reason != runner.StopIdentityMismatch || !errors.Is(err, errRunFailed) {
				t.Fatalf("err = %v, want the identity-mismatch stop as a failed run", err)
			}
			if code := exitCode(err); code != 1 {
				t.Fatalf("exit code = %d, want the step red", code)
			}
			raw, _ := os.ReadFile(output)
			if got := string(raw); !strings.Contains(got, "reason=identity-mismatch\n") || !strings.Contains(got, "commit=\n") {
				t.Fatalf("outputs = %q, want reason=identity-mismatch and an empty commit", got)
			}
			if files, _ := os.ReadDir(out); len(files) != 0 {
				t.Fatalf("wrote %v, want no bundle or state", files)
			}
		})
	}
}

func TestRunFixRoundNeedsTheTierTwoEnvironment(t *testing.T) {
	err := run(context.Background(), quietLogger(), []string{"fix-round"}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "missing environment") {
		t.Fatalf("err = %v, want the missing environment named", err)
	}
}

func TestFinishFixRoundEndsALoopWithoutFailingTheProcess(t *testing.T) {
	tests := []struct {
		name string
		res  runner.FixRoundResult
		want []string
	}{
		{"budget", runner.FixRoundResult{Round: 1, Stop: runner.FixStopBudget, Detail: "no room"},
			[]string{"commit=\n", "round=1\n", "stop=budget\n", "reason=\n"}},
		{"a round with no file changed", runner.FixRoundResult{Round: 2, Stop: runner.FixStopNoChange},
			[]string{"commit=\n", "round=2\n", "stop=no-change\n"}},
		{"a commit", runner.FixRoundResult{Round: 1, Commit: "abc123"},
			[]string{"commit=abc123\n", "round=1\n", "stop=\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			if err := finishFixRound(quietLogger(), output, 0, tt.res, nil); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(output)
			for _, w := range tt.want {
				if !strings.Contains(string(raw), w) {
					t.Fatalf("outputs = %q, want %q", raw, w)
				}
			}
		})
	}
}
