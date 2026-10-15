package runner

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// guardFile is three functions: two whose guard returns on an error, and one
// whose if only increments, so its body leaves the block no earlier than the
// code after it.
const guardFile = `package x

func f(err error) error {
	if err != nil {
		return err
	}
	return nil
}

func g(err error) error {
	if err != nil {
		return err
	}
	return nil
}

func h(n int) int {
	if n > 0 {
		n++
	}
	return n
}
`

// guardedFile is a file whose one guard the worktree adds, which a scan probes.
const guardedFile = "package x\n\nfunc f(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n"

func TestGuardsInReadsTheGuardsTheDiffAdds(t *testing.T) {
	tests := []struct {
		name  string
		added []int
		want  []int
	}{
		{"an added guard", []int{4}, []int{4}},
		{"a guard on a line the diff leaves alone", []int{11}, []int{11}},
		{"an if whose body does not leave the block", []int{18}, nil},
		{"every added line", []int{3, 4, 5, 6, 7, 8, 11, 18}, []int{4, 11}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guards, err := guardsIn("x.go", []byte(guardFile), tt.added)
			if err != nil {
				t.Fatal(err)
			}
			var lines []int
			for _, g := range guards {
				lines = append(lines, g.line)
			}
			if !slices.Equal(lines, tt.want) {
				t.Fatalf("guards at %v, want %v", lines, tt.want)
			}
		})
	}
}

// TestGuardsInIgnoresAnIfWithAnElse covers the break that is not a deletion:
// with an else branch, forcing the condition false runs that branch, so the
// probe would be testing a program the diff never wrote.
func TestGuardsInIgnoresAnIfWithAnElse(t *testing.T) {
	const src = "package x\n\nfunc f(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\nfunc g(err error) error {\n\tif err != nil {\n\t\treturn err\n\t} else {\n\t\treturn nil\n\t}\n}\n"
	guards, err := guardsIn("x.go", []byte(src), []int{4, 11})
	if err != nil {
		t.Fatal(err)
	}
	if len(guards) != 1 || guards[0].line != 4 {
		t.Fatalf("guards = %+v, want the if at 4 alone: the one at 11 has an else branch", guards)
	}
}

func TestBreakGuardReplacesOnlyTheCondition(t *testing.T) {
	guards, err := guardsIn("a/b/c.go", []byte(guardFile), []int{4})
	if err != nil {
		t.Fatal(err)
	}
	if len(guards) != 1 {
		t.Fatalf("guards = %+v, want the one on line 4", guards)
	}
	if guards[0].pkg != "./a/b" {
		t.Fatalf("pkg = %q, want the go test pattern for the file's own package", guards[0].pkg)
	}
	broken := string(breakGuard([]byte(guardFile), guards[0]))
	if !strings.Contains(broken, "if false && (err != nil) {") {
		t.Fatalf("broken guard = %q, want the condition made false behind false &&", broken)
	}
	if got := strings.Count(broken, "false &&"); got != 1 {
		t.Fatalf("broken guard = %q, has %d broken conditions, want only the probed guard's", broken, got)
	}
}

// TestBreakGuardStillCompiles covers the guards whose condition is the only
// reader of a variable: replacing the condition outright would leave it
// declared and not used, the package would not build, and the probe would read
// that build failure as the guard being covered.
func TestBreakGuardStillCompiles(t *testing.T) {
	const src = "package x\n\nfunc f(s []int, m map[string]int) int {\n\tif n := len(s); n == 0 {\n\t\treturn -1\n\t}\n\tv, ok := m[\"a\"]\n\tif !ok {\n\t\treturn -2\n\t}\n\treturn v\n}\n"
	guards, err := guardsIn("x.go", []byte(src), []int{4, 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(guards) != 2 {
		t.Fatalf("guards = %+v, want the init-clause guard and the ok guard", guards)
	}
	for _, g := range guards {
		broken := breakGuard([]byte(src), g)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "x.go", broken, 0)
		if err != nil {
			t.Fatalf("guard at %d: broken source does not parse: %v\n%s", g.line, err, broken)
		}
		if _, err := (&types.Config{}).Check("x", fset, []*ast.File{file}, nil); err != nil {
			t.Fatalf("guard at %d: broken source does not compile: %v\n%s", g.line, err, broken)
		}
	}
}

// guardWorktree is a worktree whose base carries base, with extra written into
// the worktree after it — the pending change a scan reads.
func guardWorktree(t *testing.T, base, extra string) Worktree {
	t.Helper()
	repo := initRepo(t)
	if base != "" {
		if err := os.WriteFile(filepath.Join(repo, "base.go"), []byte(base), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGit(t, repo, "add", "base.go")
		mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "base")
	}
	w, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "wingman/t-1-1-1")
	if err != nil {
		t.Fatal(err)
	}
	if extra != "" {
		if err := os.WriteFile(filepath.Join(w.Dir, "extra.go"), []byte(extra), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// guardDeps are the deps and summary a scan needs: the package-test runner under
// test, a logger that goes nowhere, and a run that started now.
func guardDeps(tests PackageTestRunner) (BuildDeps, *Summary) {
	return BuildDeps{
		PackageTests: tests,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:          time.Now,
	}, &Summary{StartedAt: time.Now()}
}

func TestGuardScanReportsAGuardNoTestCovers(t *testing.T) {
	w := guardWorktree(t, "", guardedFile)
	var pkgs []string
	deps, sum := guardDeps(func(_ context.Context, dir, pkg string) (bool, string, error) {
		if dir != w.Dir {
			t.Errorf("probe ran in %q, want the worktree %q", dir, w.Dir)
		}
		pkgs = append(pkgs, pkg)
		return false, "", nil // every test passes: nothing covers the guard
	})

	findings, err := scanGuards(context.Background(), deps, w, sum)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pkgs, []string{"."}) {
		t.Fatalf("probed %v, want the file's own package once", pkgs)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].text, "extra.go:4") {
		t.Fatalf("findings = %+v, want the guard at extra.go:4", findings)
	}
	if !strings.Contains(findings[0].text, "can be removed and every test in . still passes") {
		t.Fatalf("finding = %q, want it to say what the probe established", findings[0].text)
	}
	if !findings[0].stable {
		t.Fatalf("finding = %+v, want a guard's finding keyed stably across a rewording", findings[0])
	}
	got, err := os.ReadFile(filepath.Join(w.Dir, "extra.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != guardedFile {
		t.Fatalf("extra.go = %q, want the file restored byte for byte", got)
	}
}

// TestGuardScanLogsABreakThatDoesNotBuild covers the probe whose broken tree
// fails to compile: no test ran, so it is neither a finding nor a covered
// guard, and the log has to say why the guard went untested.
func TestGuardScanLogsABreakThatDoesNotBuild(t *testing.T) {
	w := guardWorktree(t, "", guardedFile)
	var logs strings.Builder
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		return true, "FAIL\t. [build failed]\n", nil
	})
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	findings, err := scanGuards(context.Background(), deps, w, sum)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none from a probe that did not build", findings)
	}
	if !strings.Contains(logs.String(), "msg=guardProbeFailed") || !strings.Contains(logs.String(), "reason=build") {
		t.Fatalf("logs = %q, want the guard logged as unprobed because the break did not build", logs.String())
	}
}

// TestGuardScanRestoresTheFileWhateverTheProbeAnswers covers the probe that
// cannot run at all: the file must come back either way, since a mutation left
// behind would corrupt everything the run does next.
func TestGuardScanRestoresTheFileWhateverTheProbeAnswers(t *testing.T) {
	tests := []struct {
		name  string
		tests PackageTestRunner
		want  int
	}{
		{"every test passes", func(context.Context, string, string) (bool, string, error) { return false, "", nil }, 1},
		{"a test fails", func(context.Context, string, string) (bool, string, error) { return true, "boom", nil }, 0},
		{"the runner errors", func(context.Context, string, string) (bool, string, error) {
			return false, "", errors.New("no docker")
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := guardWorktree(t, "", guardedFile)
			deps, sum := guardDeps(tt.tests)
			findings, err := scanGuards(context.Background(), deps, w, sum)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != tt.want {
				t.Fatalf("findings = %+v, want %d", findings, tt.want)
			}
			got, err := os.ReadFile(filepath.Join(w.Dir, "extra.go"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != guardedFile {
				t.Fatalf("extra.go = %q, want the file restored byte for byte", got)
			}
		})
	}
}

func TestGuardScanProbesAtMostItsCap(t *testing.T) {
	var src strings.Builder
	src.WriteString("package x\n")
	for i := 0; i <= guardScanMax; i++ {
		fmt.Fprintf(&src, "\nfunc f%d(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n", i)
	}
	w := guardWorktree(t, "", src.String())
	calls := 0
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		calls++
		return true, "", nil
	})

	findings, err := scanGuards(context.Background(), deps, w, sum)
	if err != nil {
		t.Fatal(err)
	}
	if calls != guardScanMax {
		t.Fatalf("probed %d guards, want the cap of %d", calls, guardScanMax)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none: every probe was covered", findings)
	}
}

func TestGuardScanProbesNothingOnceTheBudgetIsSpent(t *testing.T) {
	w := guardWorktree(t, "", guardedFile)
	calls := 0
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		calls++
		return false, "", nil
	})
	sum.StartedAt = time.Now().Add(-checkLoopBudget - time.Minute)

	findings, err := scanGuards(context.Background(), deps, w, sum)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(findings) != 0 {
		t.Fatalf("probed %d guards and found %+v, want neither: the budget was spent", calls, findings)
	}
}

func TestGuardScanReadsOnlyAddedLinesAndSourceFiles(t *testing.T) {
	// base.go's guard is committed, so the diff adds none of its lines; the
	// added file is a test file, which a probe has nothing to say about.
	w := guardWorktree(t, guardedFile, "")
	const testFile = "package x\n\nfunc TestF(t *testing.T) error {\n\tif err := f(nil); err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n"
	if err := os.WriteFile(filepath.Join(w.Dir, "extra_test.go"), []byte(testFile), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		calls++
		return false, "", nil
	})

	findings, err := scanGuards(context.Background(), deps, w, sum)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(findings) != 0 {
		t.Fatalf("probed %d guards and found %+v, want neither: nothing the diff added is a guard", calls, findings)
	}
}
