package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSumUsage(t *testing.T) {
	step := func(in, out, reasoning, read, write, cost string) string {
		return `{"type":"step_finish","part":{"type":"step-finish","tokens":{"input":` + in + `,"output":` + out +
			`,"reasoning":` + reasoning + `,"cache":{"read":` + read + `,"write":` + write + `}},"cost":` + cost + `}}`
	}
	tests := []struct {
		name   string
		stream string
		want   Usage
	}{
		{"empty stream", "", Usage{}},
		{"one step", step("100", "20", "5", "1000", "7", "0.25"),
			Usage{Input: 100, Output: 20, Reasoning: 5, CacheRead: 1000, CacheWrite: 7, Cost: 0.25, Steps: 1}},
		{"steps sum and other events are ignored", strings.Join([]string{
			`{"type":"step_start","part":{}}`,
			step("100", "20", "0", "1000", "0", "0.5"),
			`{"type":"text","part":{"text":"hello","tokens":{"input":999}}}`,
			step("50", "10", "3", "2000", "4", "0.25"),
		}, "\n"), Usage{Input: 150, Output: 30, Reasoning: 3, CacheRead: 3000, CacheWrite: 4, Cost: 0.75, Steps: 2}},
		{"malformed lines are skipped", "not json\n" + step("1", "2", "0", "0", "0", "0") + "\n{",
			Usage{Input: 1, Output: 2, Steps: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SumUsage(strings.NewReader(tt.stream))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestOpencodeRunPassesPromptOnStdin(t *testing.T) {
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
	if err := (Opencode{Bin: bin, Model: "p/m"}).Run(context.Background(), dir, prompt, &stdout, &stderr); err != nil {
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
