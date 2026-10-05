package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIAgentRunPassesPromptOnStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake opencode is a shell script, which Windows cannot execute")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > args.txt\nenv > env.txt\ncat > stdin.txt\necho '{\"type\":\"step_finish\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_OUTPUT", filepath.Join(dir, "out"))
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")
	t.Setenv("OPENCODE_API_KEY", "k")
	t.Setenv("LC_ALL", "C")
	prompt := strings.Repeat("rule line\n", 20000) + "## t-1\n\nlast"

	var stdout, stderr strings.Builder
	if err := (CLIAgent{Bin: bin, Model: "p/m"}).Run(context.Background(), dir, "", prompt, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	gotStdin, err := os.ReadFile(filepath.Join(dir, "stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotStdin) != prompt {
		t.Fatalf("stdin differs from the prompt: got %d bytes, want %d", len(gotStdin), len(prompt))
	}
	gotArgs, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := "run\n--format\njson\n--auto\n-m\np/m\n--dir\n" + dir + "\n"
	if string(gotArgs) != wantArgs {
		t.Fatalf("args = %q, want %q", gotArgs, wantArgs)
	}
	gotEnv, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(gotEnv)
	for _, withheld := range []string{"GITHUB_OUTPUT=", "ACTIONS_ID_TOKEN_REQUEST_TOKEN="} {
		if strings.Contains(env, withheld) {
			t.Fatalf("agent inherited %s", withheld)
		}
	}
	if !strings.Contains(env, "OPENCODE_API_KEY=k") || !strings.Contains(env, "LC_ALL=C") || !strings.Contains(env, "PATH=") {
		t.Fatalf("agent env not filtered as expected:\n%s", gotEnv)
	}
	if !strings.Contains(stdout.String(), "step_finish") {
		t.Fatal("events did not reach stdout")
	}
}

func TestCLIAgentRunPassesTheSessionToContinue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake opencode is a shell script, which Windows cannot execute")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > args.txt\ncat > /dev/null\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := (CLIAgent{Bin: bin, Model: "p/m"}).Run(context.Background(), dir, "ses_abc", "prompt", &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	gotArgs, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := "run\n--format\njson\n--auto\n-m\np/m\n--dir\n" + dir + "\n-s\nses_abc\n"
	if string(gotArgs) != wantArgs {
		t.Fatalf("args = %q, want %q", gotArgs, wantArgs)
	}
}

func TestCLIAgentRunPassesTheRestrictedAgentAndItsConfig(t *testing.T) {
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
	agent := PlanCLIAgent(bin, "p/m")
	if err := agent.Run(context.Background(), dir, "", "prompt", &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	gotArgs, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := "run\n--format\njson\n--auto\n-m\np/m\n--dir\n" + dir + "\n--agent\n" + planAgentName + "\n"
	if string(gotArgs) != wantArgs {
		t.Fatalf("args = %q, want %q", gotArgs, wantArgs)
	}
	gotEnv, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotEnv), "OPENCODE_CONFIG_CONTENT="+planAgentConfig) {
		t.Fatalf("agent env carries no restricted config:\n%s", gotEnv)
	}
}

func TestCLIAgentWithModelKeepsTheAgentProfile(t *testing.T) {
	got := PlanCLIAgent("bin", "p/a").WithModel("p/b")
	if want := PlanCLIAgent("bin", "p/b"); got != want {
		t.Fatalf("rebound agent = %+v, want %+v", got, want)
	}
}
