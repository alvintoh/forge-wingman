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

	findings, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
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

	findings, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
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
			findings, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
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

// TestGuardScanProbesEveryGuardTheDiffAdds is AC1: no fixed count stops the
// scan, so a diff adding more guards than the old cap probes every one of them
// while the stage's budget lasts.
func TestGuardScanProbesEveryGuardTheDiffAdds(t *testing.T) {
	const guards = 12
	var src strings.Builder
	src.WriteString("package x\n")
	for i := range guards {
		fmt.Fprintf(&src, "\nfunc f%d(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n", i)
	}
	w := guardWorktree(t, "", src.String())
	calls := 0
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		calls++
		return true, "", nil
	})

	findings, untested, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != guards {
		t.Fatalf("probed %d guards, want all %d the diff adds", calls, guards)
	}
	if untested != 0 {
		t.Fatalf("untested = %d, want 0: the budget lasted for every guard", untested)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none: every probe was covered", findings)
	}
}

// TestGuardScanProbesInPathThenSourceOrder is AC2: a scan stopped part-way
// probes the same guards first for the same diff, so the order is the paths'
// and then each file's own source order.
func TestGuardScanProbesInPathThenSourceOrder(t *testing.T) {
	const first = "package x\n\nfunc a1(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\nfunc a2(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n"
	const second = "package x\n\nfunc b1(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\nfunc b2(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n"
	w := guardWorktree(t, "", "")
	for name, src := range map[string]string{"b.go": second, "a.go": first} {
		if err := os.WriteFile(filepath.Join(w.Dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var probed []string
	deps, sum := guardDeps(func(_ context.Context, dir, _ string) (bool, string, error) {
		for _, name := range []string{"a.go", "b.go"} {
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return false, "", err
			}
			if i := strings.Index(string(body), "false && ("); i >= 0 {
				line := strings.Count(string(body[:i]), "\n") + 1
				probed = append(probed, fmt.Sprintf("%s:%d", name, line))
				break
			}
		}
		return true, "", nil
	})

	if _, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.go:4", "a.go:11", "b.go:4", "b.go:11"}; !slices.Equal(probed, want) {
		t.Fatalf("probed %v, want %v: path order, then each file's own source order", probed, want)
	}
}

// TestGuardScanStopsOnBudgetAndCountsTheGuardsItLeft is AC3: the scan stops
// when the stage's budget runs out and reports how many guards it left; the
// run logs that count once, after the build stage's scan.
func TestGuardScanStopsOnBudgetAndCountsTheGuardsItLeft(t *testing.T) {
	const guards = 3
	var src strings.Builder
	src.WriteString("package x\n")
	for i := range guards {
		fmt.Fprintf(&src, "\nfunc f%d(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n", i)
	}
	w := guardWorktree(t, "", src.String())
	now := time.Now()
	calls := 0
	deps := BuildDeps{
		// Each probe spends half the stage's budget, so the scan reaches two
		// of the three guards and the third finds the budget spent.
		PackageTests: func(context.Context, string, string) (bool, string, error) {
			calls++
			now = now.Add(nfr1BuildBudget / 2)
			return true, "", nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
	}

	findings, untested, err := scanGuards(context.Background(), deps, w, now, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("probed %d guards, want the 2 the budget lasted for", calls)
	}
	if untested != 1 {
		t.Fatalf("untested = %d, want the 1 guard the budget left", untested)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none: every probe was covered", findings)
	}
}

// TestGuardScanSkipsSettledGuardsAndSettlesAllButFindings covers the set a
// later scan shares: a covered guard and one the budget left are settled and
// not probed again, while a finding stays unsettled so the next scan
// re-probes it.
func TestGuardScanSkipsSettledGuardsAndSettlesAllButFindings(t *testing.T) {
	const guards = 3
	var src strings.Builder
	src.WriteString("package x\n")
	for i := range guards {
		fmt.Fprintf(&src, "\nfunc f%d(e%d error) error {\n\tif e%d != nil {\n\t\treturn e%d\n\t}\n\treturn nil\n}\n", i, i, i, i)
	}
	w := guardWorktree(t, "", src.String())
	now := time.Now()
	calls := 0
	deps := BuildDeps{
		// The first probe finds the guard uncovered, the second covered, and
		// together they spend the budget, so the third guard is left.
		PackageTests: func(context.Context, string, string) (bool, string, error) {
			calls++
			now = now.Add(nfr1BuildBudget / 2)
			return calls > 1, "", nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
	}
	settled := make(map[string]bool)

	findings, untested, err := scanGuards(context.Background(), deps, w, now, nfr1BuildBudget, settled)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || untested != 1 || len(settled) != 2 {
		t.Fatalf("findings = %+v, untested = %d, settled = %d, want 1 finding, 1 untested, 2 settled", findings, untested, len(settled))
	}
	if settled[findings[0].key] {
		t.Fatalf("settled = %v, want the finding left unsettled so a later scan re-probes it", settled)
	}

	calls = 0
	if _, _, err := scanGuards(context.Background(), deps, w, now, nfr1BuildBudget*4, settled); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("second scan probed %d guards, want only the earlier finding", calls)
	}
}

func TestGuardScanProbesNothingOnceTheBudgetIsSpent(t *testing.T) {
	w := guardWorktree(t, "", guardedFile)
	calls := 0
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		calls++
		return false, "", nil
	})
	sum.StartedAt = time.Now().Add(-nfr1BuildBudget - time.Minute)

	findings, untested, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(findings) != 0 {
		t.Fatalf("probed %d guards and found %+v, want neither: the budget was spent", calls, findings)
	}
	if untested != 1 {
		t.Fatalf("untested = %d, want the 1 guard the diff adds", untested)
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

	findings, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(findings) != 0 {
		t.Fatalf("probed %d guards and found %+v, want neither: nothing the diff added is a guard", calls, findings)
	}
}

// TestGuardScanKeepsAFindingsKeyUnsettledWhenAnotherGuardSharesIt covers two
// guards in one file with the same header, which share a key: one covered must
// not settle the other's finding, or the re-scan would never re-probe it.
func TestGuardScanKeepsAFindingsKeyUnsettledWhenAnotherGuardSharesIt(t *testing.T) {
	const src = "package x\n\nfunc a(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\nfunc b(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n"
	w := guardWorktree(t, "", src)
	calls := 0
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		calls++
		return calls > 1, "", nil // the first guard is uncovered, the second covered
	})
	settled := make(map[string]bool)

	findings, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, settled)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || settled[findings[0].key] {
		t.Fatalf("findings = %+v, settled = %v, want the finding's shared key left unsettled", findings, settled)
	}
}
