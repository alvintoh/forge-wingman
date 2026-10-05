package runner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const cleanGoSource = "package x\n\n// Add returns a plus b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"

const cleanGoTest = "package x\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n"

// initModule creates a minimal Go module at a temp directory whose gofmt, go
// vet, golangci-lint and go test all pass untouched.
func initModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeModuleFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.22\n")
	writeModuleFile(t, dir, "x.go", cleanGoSource)
	writeModuleFile(t, dir, "x_test.go", cleanGoTest)
	return dir
}

func writeModuleFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunChecksPassesOnACleanModule(t *testing.T) {
	dir := initModule(t)
	gate, output, err := RunChecks(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if gate != "" || output != "" {
		t.Fatalf("gate = %q, output = %q, want a clean pass", gate, output)
	}
}

func TestRunChecksNamesTheFirstFailingGate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(dir string)
		want   string
	}{
		{"gofmt", func(dir string) {
			writeModuleFile(t, dir, "x.go", "package x\nfunc Add(a,b int) int { return a+b }\n")
		}, checkGofmt},
		{"vet", func(dir string) {
			writeModuleFile(t, dir, "x.go", "package x\n\nimport \"fmt\"\n\n// Add returns a plus b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n"+
				"// Bad has a printf mismatch.\nfunc Bad() {\n\tfmt.Printf(\"%d\", \"oops\")\n}\n")
		}, checkVet},
		{"golangci-lint", func(dir string) {
			writeModuleFile(t, dir, "x.go", "package x\n\nimport \"os\"\n\n// Add returns a plus b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n"+
				"// Bad ignores an error.\nfunc Bad() {\n\tos.Open(\"x\")\n}\n")
		}, checkLint},
		{"test", func(dir string) {
			writeModuleFile(t, dir, "x_test.go", "package x\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tt.Fatal(\"boom\")\n}\n")
		}, checkTest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initModule(t)
			tt.mutate(dir)
			gate, output, err := RunChecks(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if gate != tt.want {
				t.Fatalf("gate = %q, want %q (output: %s)", gate, tt.want, output)
			}
			if output == "" {
				t.Fatal("no output captured for the failing gate")
			}
		})
	}
}

// TestRunChecksStopsAtTheFirstFailure is the regression test for a module
// broken at more than one gate: only the earliest — gofmt — is reported.
func TestRunChecksStopsAtTheFirstFailure(t *testing.T) {
	dir := initModule(t)
	writeModuleFile(t, dir, "x.go", "package x\nfunc Add(a,b int) int { return a+b }\n") // gofmt AND vet would both fail
	writeModuleFile(t, dir, "x_test.go", "package x\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tt.Fatal(\"boom\")\n}\n")
	gate, _, err := RunChecks(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if gate != checkGofmt {
		t.Fatalf("gate = %q, want %q", gate, checkGofmt)
	}
}

func TestRunChecksFailsClosedWhenAGateCannotRunAtAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if _, _, err := RunChecks(context.Background(), dir); err == nil {
		t.Fatal("RunChecks succeeded against a directory that does not exist")
	}
}

// TestRunChecksStripsSensitiveEnvFromItsSubprocesses is the regression for
// RunChecks executing arbitrary agent-written code (an init() or TestMain)
// with the model job's full environment inherited — COMMANDCODE_API_KEY and the
// workload-identity credentials among it. It sets both in the test process's
// own environment, the same way the model job's steps do, and has the
// module's own test assert neither reached it: if checks.go ever stops
// filtering the subprocess environment, this module's own gate fails, which
// RunChecks reports as gate == checkTest rather than a clean pass.
func TestRunChecksStripsSensitiveEnvFromItsSubprocesses(t *testing.T) {
	t.Setenv("COMMANDCODE_API_KEY", "should-not-leak")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/should/not/leak.json")
	dir := initModule(t)
	writeModuleFile(t, dir, "leak_test.go", `package x

import (
	"os"
	"testing"
)

func TestNoSecretLeaked(t *testing.T) {
	for _, k := range []string{"COMMANDCODE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if v := os.Getenv(k); v != "" {
			t.Fatalf("%s leaked into the check subprocess: %q", k, v)
		}
	}
}
`)
	gate, output, err := RunChecks(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if gate != "" {
		t.Fatalf("gate = %q, output = %q, want a clean pass with the secrets stripped", gate, output)
	}
}

func TestCheckEnvNamesExcludeTheModelJobsSecretsButKeepTheGoToolchain(t *testing.T) {
	got := filterEnv([]string{
		"COMMANDCODE_API_KEY=k",
		"GOOGLE_APPLICATION_CREDENTIALS=/tmp/creds.json",
		"GOOGLE_GHA_CREDS_PATH=/tmp/creds.json",
		"PATH=/usr/bin",
		"GOPATH=/home/runner/go",
	}, checkEnvNames)
	joined := strings.Join(got, "\n")
	for _, withheld := range []string{"COMMANDCODE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_GHA_CREDS_PATH"} {
		if strings.Contains(joined, withheld) {
			t.Fatalf("checkEnvNames allows %s through: %v", withheld, got)
		}
	}
	for _, kept := range []string{"PATH=/usr/bin", "GOPATH=/home/runner/go"} {
		if !slices.Contains(got, kept) {
			t.Fatalf("checkEnvNames dropped %s: %v", kept, got)
		}
	}
}
