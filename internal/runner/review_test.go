package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReviewCLIAgentRunPassesTheRestrictedAgentAndItsConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake opencode is a shell script, which Windows cannot execute")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > args.txt\nenv > env.txt\ncat > /dev/null\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	agent := ReviewCLIAgent(bin, "p/r")
	if err := agent.Run(context.Background(), dir, "", "prompt", &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	gotArgs, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := "run\n--format\njson\n--auto\n-m\np/r\n--dir\n" + dir + "\n--agent\n" + reviewAgentName + "\n"
	if string(gotArgs) != wantArgs {
		t.Fatalf("args = %q, want %q", gotArgs, wantArgs)
	}
	gotEnv, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotEnv), "OPENCODE_CONFIG_CONTENT="+reviewAgentConfig) {
		t.Fatalf("agent env carries no restricted config:\n%s", gotEnv)
	}
}

func TestReviewPromptCarriesTheDiffAndTheTicket(t *testing.T) {
	got := ReviewPrompt("diff --git a/x.go\n+x", testTicket)
	if !strings.Contains(got, "```diff\ndiff --git a/x.go\n+x\n```") || !strings.Contains(got, testTicket.Text()) ||
		!strings.HasSuffix(got, "```review-findings\n```") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestParseReviewFindings(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"clean", "```review-findings\n```", ""},
		{"one finding", "```review-findings\nthe retry never bounds its attempts\n```", "the retry never bounds its attempts"},
		{"only the last block counts", "```review-findings\nstale\n```\n\nmore reasoning\n\n```review-findings\n```", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReviewFindings(tt.text)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("findings = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseReviewFindingsRejectsAResponseWithNoBlock(t *testing.T) {
	if _, err := ParseReviewFindings("looks fine to me"); !errors.Is(err, errReviewFindings) {
		t.Fatalf("err = %v, want errReviewFindings", err)
	}
}
