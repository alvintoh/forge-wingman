package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestFixRoundPreflightFailureWritesTheOutputsAndFailsTheStep(t *testing.T) {
	output := filepath.Join(t.TempDir(), "output")
	err := run(context.Background(), quietLogger(), []string{"fix-round", "-branch", "wingman/x-1", "-head", "abc", "-findings", "f"},
		fixRoundEnv(output, nil))
	if !errors.Is(err, errRunFailed) || exitCode(err) != 1 {
		t.Fatalf("err = %v, want a failed run", err)
	}
	raw, _ := os.ReadFile(output)
	if got := string(raw); !strings.Contains(got, "reason=error\n") || !strings.Contains(got, "round=0\n") || !strings.Contains(got, "-out") {
		t.Fatalf("outputs = %q, want reason=error, round=0 and a detail naming -out", got)
	}
}

func TestFixRoundRefusesAStartThatWouldNotMeasureTheLoop(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	tests := []struct {
		name    string
		started string
		want    string
	}{
		{"no start on a fresh loop", "", "-started on a fresh loop"},
		{"a start it cannot read", "yesterday", "-started as an RFC3339 time"},
		{"a start ahead of the clock", future, "-started is ahead of this step's clock"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			out := t.TempDir()
			args := []string{"fix-round", "-branch", "wingman/x-1", "-head", strings.Repeat("a", 40),
				"-findings", "f", "-out", out}
			if tt.started != "" {
				args = append(args, "-started", tt.started)
			}
			err := run(context.Background(), quietLogger(), args, fixRoundEnv(output, nil))
			if !errors.Is(err, errRunFailed) || exitCode(err) != 1 {
				t.Fatalf("err = %v, want a failed run", err)
			}
			raw, _ := os.ReadFile(output)
			if got := string(raw); !strings.Contains(got, "reason=error\n") || !strings.Contains(got, tt.want) {
				t.Fatalf("outputs = %q, want reason=error and a detail holding %q", got, tt.want)
			}
			if files, _ := os.ReadDir(out); len(files) != 0 {
				t.Fatalf("wrote %v, want no bundle or state", files)
			}
		})
	}
}

func TestFixLoopStateSeedsOrKeepsTheLoopStart(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	loopStart := now.Add(-45 * time.Minute)
	prev := runner.NewFixRoundsState(2, 2, now.Add(-20*time.Minute), "ses_1", []string{"a finding"}, nil)
	tests := []struct {
		name    string
		prev    *runner.FixRoundsState
		started string
		want    time.Time
		wantErr bool
	}{
		{"a fresh loop starts where the caller says", nil, loopStart.Format(time.RFC3339), loopStart, false},
		{"a fresh loop with no start", nil, "", time.Time{}, true},
		{"a fresh loop with a start it cannot read", nil, "yesterday", time.Time{}, true},
		{"a later round keeps its own start", &prev, "", prev.StartedAt, false},
		{"a later round keeps its own start over one it is given", &prev, loopStart.Format(time.RFC3339), prev.StartedAt, false},
		{"a later round with a start it cannot read", &prev, "yesterday", time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fixLoopState(tt.prev, tt.started, now)
			if tt.wantErr {
				if err == nil || got != nil {
					t.Fatalf("state = %+v, err = %v; want the start refused", got, err)
				}
				return
			}
			if err != nil || !got.StartedAt.Equal(tt.want) {
				t.Fatalf("state = %+v, err = %v; want a start at %s", got, err, tt.want.Format(time.RFC3339))
			}
		})
	}
}

func TestFixLoopStateRefusesAStartAheadOfItsClock(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	future := now.Add(runner.FixRoundsClockSkew + time.Minute).Format(time.RFC3339)
	prev := runner.NewFixRoundsState(1, 1, now.Add(-time.Minute), "", nil, nil)
	for _, tt := range []struct {
		name string
		prev *runner.FixRoundsState
	}{
		{"a fresh loop", nil},
		{"a later round", &prev},
	} {
		if _, err := fixLoopState(tt.prev, future, now); err == nil {
			t.Errorf("%s: accepted a start ahead of the clock", tt.name)
		}
	}
}

func TestFixRoundRefusesToRunTheAgentOnTheHost(t *testing.T) {
	output := filepath.Join(t.TempDir(), "output")
	out := t.TempDir()
	err := run(context.Background(), quietLogger(),
		[]string{"fix-round", "-branch", "wingman/x-1", "-head", strings.Repeat("a", 40), "-findings", "f", "-out", out,
			"-started", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		fixRoundEnv(output, nil))
	if !errors.Is(err, errRunFailed) || exitCode(err) != 1 {
		t.Fatalf("err = %v, want a failed run", err)
	}
	raw, _ := os.ReadFile(output)
	if got := string(raw); !strings.Contains(got, "reason=error\n") || !strings.Contains(got, "-agent-image") {
		t.Fatalf("outputs = %q, want reason=error and a detail naming -agent-image", got)
	}
	if files, _ := os.ReadDir(out); len(files) != 0 {
		t.Fatalf("wrote %v, want no bundle or state", files)
	}
}
