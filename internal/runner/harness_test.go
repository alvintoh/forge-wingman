package runner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fakeHarness serves provider "p" through agent, or a fresh fakeAgent when unset.
type fakeHarness struct{ agent Agent }

func (fakeHarness) Providers() []string { return []string{"p"} }
func (h fakeHarness) Agent(Profile, string) Agent {
	if h.agent == nil {
		return &fakeAgent{}
	}
	return h.agent
}
func (fakeHarness) Classify(stderrTail string, _ error) (Outcome, StopReason) {
	return classifyMarkers(stderrTail)
}
func (fakeHarness) Ready() error { return nil }

func TestCommandCodeHarnessServesItsProvider(t *testing.T) {
	if got := (CommandCodeHarness{}).Providers(); !slices.Equal(got, []string{"command-code"}) {
		t.Fatalf("providers = %v", got)
	}
}

func TestRouterRunsTheHarnessItsModelNames(t *testing.T) {
	ccBin, ccAttempts := scriptedCommandCode(t, "normal")
	other := &fakeAgent{}
	r := NewRouter(ProfileBuild,
		CommandCodeHarness{Bin: ccBin, Key: "k", Home: t.TempDir()},
		fakeHarness{agent: other},
	)

	var out, errBuf strings.Builder
	if err := r.WithModel("command-code/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if len(ccAttempts()) != 1 || other.calls != 0 {
		t.Fatalf("the command-code model did not route to the command-code harness alone")
	}
	if err := r.WithModel("p/y").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err != nil {
		t.Fatal(err)
	}
	if len(ccAttempts()) != 1 || other.calls != 1 {
		t.Fatalf("the p model did not route to its own harness alone")
	}
}

func TestRouterRefusesAnUnservedModel(t *testing.T) {
	r := NewRouter(ProfileBuild, fakeHarness{agent: &fakeAgent{}})
	if err := r.Gate("other/x"); err == nil {
		t.Fatal("the router served a model no harness serves")
	}
	var out, errBuf strings.Builder
	if err := r.WithModel("other/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf); err == nil {
		t.Fatal("the router ran a model no harness serves")
	}
}

func TestRouterRunRefusesAnUnreadyHarness(t *testing.T) {
	r := NewRouter(ProfileBuild, CommandCodeHarness{Bin: "cmd"})
	var out, errBuf strings.Builder
	err := r.WithModel("command-code/x").Run(context.Background(), t.TempDir(), "", "p", "", &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "COMMANDCODE_API_KEY") {
		t.Fatalf("err = %v, want the harness's missing-key refusal", err)
	}
}

// TestCommandCodeHarnessIsReadyWithItsKey asserts the harness is refused
// without its key and ready with it (AC7).
func TestCommandCodeHarnessIsReadyWithItsKey(t *testing.T) {
	for name, tt := range map[string]struct {
		h     CommandCodeHarness
		ready bool
	}{
		"no key": {CommandCodeHarness{}, false},
		"a key":  {CommandCodeHarness{Key: "k"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tt.h.Ready(); (err == nil) != tt.ready {
				t.Fatalf("Ready() = %v, want ready %v", err, tt.ready)
			}
		})
	}
}

// TestRouterClassifiesAnUnservedModelAsAnAgentFailure asserts a model no
// harness serves never reads as a budget or availability stop.
func TestRouterClassifiesAnUnservedModelAsAnAgentFailure(t *testing.T) {
	r := NewRouter(ProfileBuild, fakeHarness{}).WithModel("other/x").(Router)
	if outcome, reason := r.Classify("allowance exhausted", nil); outcome != OutcomeAgentFailed || reason != StopAgentExit {
		t.Fatalf("Classify = %s/%s, want the ordinary agent failure", outcome, reason)
	}
}

// TestFilterEnvMatchesAPrefixOnlyForAnUnderscoreName asserts a plain allowed
// name admits that variable alone, never one it happens to prefix.
func TestFilterEnvMatchesAPrefixOnlyForAnUnderscoreName(t *testing.T) {
	got := filterEnv([]string{"CI=true", "CI_JOB_TOKEN=t", "HOME=/h", "HOMEBREW_GITHUB_API_TOKEN=t", "LC_ALL=C"},
		[]string{"CI", "HOME", "LC_"})
	if want := []string{"CI=true", "HOME=/h", "LC_ALL=C"}; !slices.Equal(got, want) {
		t.Fatalf("filterEnv = %v, want %v", got, want)
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
		if strings.Contains(lower, "commandcode") || strings.Contains(lower, "command-code") {
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
	switch rel {
	case "internal/runner/harness_commandcode.go",
		"internal/runner/build.go",
		"cmd/runner/main.go",
		"cmd/dispatcher/main.go":
		return true
	}
	return false
}

// TestNoRetiredHarnessIsNamed asserts no tracked file outside the vault copies
// and the historical probe results names the retired harness or its model set.
func TestNoRetiredHarnessIsNamed(t *testing.T) {
	root := filepath.Join("..", "..")
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".",
		":!docs/adr", ":!docs/tech-design-v1.md", ":!probe/results")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	retired := regexp.MustCompile(`(?i:open[c]ode|\bz[e]n\b)`)
	var offenders []string
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if retired.Match(b) {
			offenders = append(offenders, rel)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("the retired harness is named in: %v", offenders)
	}
}
