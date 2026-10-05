package runner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOpencodeHarnessServesTheZenAndGoProviders(t *testing.T) {
	if got := (OpencodeHarness{}).Providers(); !slices.Equal(got, []string{ZenProvider, opencodeGoProvider}) {
		t.Fatalf("providers = %v", got)
	}
	if got := (CommandCodeHarness{}).Providers(); !slices.Equal(got, []string{"command-code"}) {
		t.Fatalf("providers = %v", got)
	}
}

// TestHarnessEnvSplit asserts each harness's process env carries its own key
// and neither the other's (AC6).
func TestHarnessEnvSplit(t *testing.T) {
	for name, tt := range map[string]struct {
		env        []string
		want       []string
		wantAbsent []string
	}{
		"opencode":     {OpencodeHarness{}.EnvNames(), []string{"OPENCODE_API_KEY"}, []string{"COMMANDCODE_API_KEY"}},
		"command-code": {CommandCodeHarness{}.EnvNames(), nil, []string{"OPENCODE_API_KEY", "COMMANDCODE_API_KEY"}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range tt.want {
				if !slices.Contains(tt.env, want) {
					t.Errorf("env %v lacks %s", tt.env, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if slices.Contains(tt.env, absent) {
					t.Errorf("env %v carries %s, which belongs to another harness", tt.env, absent)
				}
			}
		})
	}
}

func TestRouterRunsTheHarnessItsModelNames(t *testing.T) {
	ccBin, ccAttempts := scriptedCommandCode(t, "normal")
	dir := t.TempDir()
	ocBin, ocLog := filepath.Join(dir, "opencode"), filepath.Join(dir, "ran")
	if err := os.WriteFile(ocBin, []byte("#!/bin/sh\ntouch '"+ocLog+"'\ncat > /dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRouter(ProfileBuild,
		CommandCodeHarness{Bin: ccBin, Key: "k", OptIn: true},
		OpencodeHarness{Bin: ocBin},
	)

	var out, errBuf strings.Builder
	if err := r.WithModel("command-code/x").Run(context.Background(), t.TempDir(), "", "p", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if len(ccAttempts()) != 1 {
		t.Fatalf("the command-code model did not route to the command-code harness")
	}
	if err := r.WithModel("opencode/y").Run(context.Background(), t.TempDir(), "", "p", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ocLog); err != nil {
		t.Fatalf("the opencode model did not route to the opencode harness: %v", err)
	}
}

func TestRouterRefusesAnUnservedModel(t *testing.T) {
	r := NewRouter(ProfileBuild, OpencodeHarness{Bin: "opencode"})
	if err := r.Gate("other/x"); err == nil {
		t.Fatal("the router served a model no harness serves")
	}
	var out, errBuf strings.Builder
	if err := r.WithModel("other/x").Run(context.Background(), t.TempDir(), "", "p", &out, &errBuf); err == nil {
		t.Fatal("the router ran a model no harness serves")
	}
}

func TestRouterRunRefusesAnUnreadyHarness(t *testing.T) {
	r := NewRouter(ProfileBuild, CommandCodeHarness{Bin: "cmd"})
	var out, errBuf strings.Builder
	err := r.WithModel("command-code/x").Run(context.Background(), t.TempDir(), "", "p", &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "COMMAND_CODE_OPT_IN") {
		t.Fatalf("err = %v, want the harness's opt-in refusal", err)
	}
}

// TestNoHarnessIsNamedOutsideItsAdapter keeps the vendor vocabulary inside each
// adapter and the composition root, so a harness can be swapped without a
// sweep through the codebase (AC5).
func TestNoHarnessIsNamedOutsideItsAdapter(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if harnessNameAllowed(rel) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(b))
		if strings.Contains(lower, "opencode") || strings.Contains(lower, "commandcode") || strings.Contains(lower, "command-code") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("a harness is named outside its adapter: %v", offenders)
	}
}

// harnessNameAllowed lists where a harness name may appear: its own adapter, the
// composition root, and the vendor-defined values read where they live.
func harnessNameAllowed(rel string) bool {
	switch {
	case rel == "internal/runner/harness_opencode.go",
		rel == "internal/runner/harness_commandcode.go",
		rel == "internal/runner/models.go",
		rel == "internal/runner/build.go",
		rel == "cmd/runner/main.go",
		rel == "cmd/dispatcher/main.go",
		rel == "internal/dispatcher/budget.go",
		strings.HasPrefix(rel, "internal/modelprobe/"),
		strings.HasPrefix(rel, "cmd/modelprobe/"):
		return true
	}
	return false
}
