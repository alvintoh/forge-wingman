package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	var logs strings.Builder
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	findings, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "msg=guardFinding file=extra.go line=4") {
		t.Fatalf("logs = %q, want the finding logged by file and line", logs.String())
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

	findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	untested := len(unprobed)
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
	const guards = 4
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
		// of the four guards and the others find the budget spent.
		PackageTests: func(context.Context, string, string) (bool, string, error) {
			calls++
			now = now.Add(nfr1BuildBudget / 2)
			return true, "", nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
	}

	findings, unprobed, err := scanGuards(context.Background(), deps, w, now, nfr1BuildBudget, nil)
	untested := len(unprobed)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("probed %d guards, want the 2 the budget lasted for", calls)
	}
	if untested != 2 {
		t.Fatalf("untested = %d, want the 2 guards the budget left", untested)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none: every probe was covered", findings)
	}
}

// TestGuardScanSettlesOnlyTheGuardsATestCovers covers the set a
// later scan shares: a covered guard is settled and not probed again, while a
// finding and a guard the budget left stay unsettled so the next scan
// re-probes it.
func TestGuardScanSettlesOnlyTheGuardsATestCovers(t *testing.T) {
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

	findings, unprobed, err := scanGuards(context.Background(), deps, w, now, nfr1BuildBudget, settled)
	untested := len(unprobed)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || untested != 1 || len(settled) != 1 {
		t.Fatalf("findings = %+v, untested = %d, settled = %d, want 1 finding, 1 untested, 1 settled", findings, untested, len(settled))
	}
	if settled[findings[0].key] || settled[unprobed[0].key] {
		t.Fatalf("settled = %v, want the finding and the unprobed guard left for a later scan to probe", settled)
	}

	calls = 0
	if _, _, err := scanGuards(context.Background(), deps, w, now, nfr1BuildBudget*4, settled); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("second scan probed %d guards, want the earlier finding and the guard the budget left", calls)
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

	findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	untested := len(unprobed)
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

// webGuardedFile is a TypeScript source with a braceless guard, a throwing one
// and one with an else, whose first guard follows a two-byte and a four-byte
// character so its offsets only come out right in bytes.
const webGuardedFile = "export function f(a: string, b?: number) {\n" +
	"  const s = \"é😀\"; if (!a) return s;\n" +
	"  if (b === undefined)\n" +
	"    throw new Error(\"no b\");\n" +
	"  if (a === \"x\") { return a; } else { return s; }\n" +
	"  return a;\n" +
	"}\n"

// webWorktree is a worktree that adds the web sources in files.
func webWorktree(t *testing.T, files map[string]string) Worktree {
	t.Helper()
	w := guardWorktree(t, "", "")
	for name, src := range files {
		if err := os.MkdirAll(filepath.Join(w.Dir, filepath.Dir(name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.Dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// oneWebGuard is a discovery answer naming the first guard of webGuardedFile.
const oneWebGuard = `[{"file":"web/src/f.ts","line":2,"header":"if (!a)","cond":[1,2]}]`

// bunRunSummary is the line bun test ends a run that ran tests with.
const bunRunSummary = " 3 pass\n 0 fail\nRan 3 tests across 1 file. [2.00ms]\n"

// bunInWorktree is a WebBunRunner that runs the host's bun in dir's web
// directory, where the test links the repository's own node_modules: the
// TypeScript compiler the container would use. tests answers a `bun test`.
func bunInWorktree(tests func(dir string) (output string, failed bool)) WebBunRunner {
	return func(ctx context.Context, dir string, args ...string) (string, string, bool, error) {
		if args[0] == "test" {
			out, failed := tests(dir)
			return "", out, failed, nil
		}
		cmd := exec.CommandContext(ctx, "bun", args...)
		cmd.Dir = filepath.Join(dir, webDir)
		out, err := cmd.Output()
		return string(out), "", err != nil, nil
	}
}

// requireWebCompiler skips a test that needs bun and the TypeScript compiler
// installed in web, and links that compiler into the web directory of the
// worktree dir.
func requireWebCompiler(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not installed")
	}
	_, file, _, _ := runtime.Caller(0)
	modules := filepath.Join(filepath.Dir(file), "..", "..", webDir, "node_modules")
	if _, err := os.Stat(filepath.Join(modules, "typescript")); err != nil {
		t.Skip("web's dependencies are not installed")
	}
	if err := os.MkdirAll(filepath.Join(dir, webDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(modules, filepath.Join(dir, webDir, "node_modules")); err != nil {
		t.Fatal(err)
	}
}

func TestGuardScanBreaksEachTypeScriptGuardTheDiffAddsAtTheCompilersOffsets(t *testing.T) {
	const name = webDir + "/src/f.ts"
	w := webWorktree(t, map[string]string{name: webGuardedFile})
	requireWebCompiler(t, w.Dir)
	var broken []string
	deps, sum := guardDeps(nil)
	deps.WebBun = bunInWorktree(func(dir string) (string, bool) {
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Error(err)
		}
		broken = append(broken, string(src))
		return bunRunSummary, false
	})

	findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	untested := len(unprobed)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		strings.Replace(webGuardedFile, "if (!a)", "if (false && (!a))", 1),
		strings.Replace(webGuardedFile, "if (b === undefined)", "if (false && (b === undefined))", 1),
	}
	if !slices.Equal(broken, want) {
		t.Fatalf("probed %q, want the two guards without an else, each broken in turn", broken)
	}
	if untested != 0 || len(findings) != 2 {
		t.Fatalf("findings = %+v, untested = %d, want both guards reported: every test passed with them broken", findings, untested)
	}
	if want := "guard at " + name + ":2 can be removed and every test in web still passes"; findings[0].text != want {
		t.Fatalf("finding = %q, want %q", findings[0].text, want)
	}
	got, err := os.ReadFile(filepath.Join(w.Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != webGuardedFile {
		t.Fatalf("%s = %q, want the file restored byte for byte", name, got)
	}
}

func TestGuardScanReadsOnlyTheTypeScriptLinesTheDiffAdds(t *testing.T) {
	const name = webDir + "/src/f.ts"
	const committed = "export function f(n: number) {\n  if (n < 0) return;\n}\n"
	repo := initRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, webDir, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, name), []byte(committed), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", name)
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "web")
	w, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "wingman/t-1-1-1")
	if err != nil {
		t.Fatal(err)
	}
	requireWebCompiler(t, w.Dir)
	added := committed + "export function g(n: number) {\n  if (n > 0) return;\n}\n"
	if err := os.WriteFile(filepath.Join(w.Dir, name), []byte(added), 0o600); err != nil {
		t.Fatal(err)
	}
	deps, sum := guardDeps(nil)
	probes := 0
	deps.WebBun = bunInWorktree(func(string) (string, bool) { probes++; return bunRunSummary, true })

	if _, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("probed %d guards, want the one the diff adds", probes)
	}
}

func TestIsWebSourceTakesOnlyTypeScriptSourceUnderWeb(t *testing.T) {
	tests := map[string]bool{
		"web/src/a.ts":         true,
		"web/src/a.tsx":        true,
		"web/src/a.test.ts":    false,
		"web/src/a.test.tsx":   false,
		"web/src/a.spec.ts":    false,
		"web/src/types.d.ts":   false,
		"web/src/a.js":         false,
		"web/src/a.css":        false,
		"tools/shots/main.ts":  false,
		"webx/src/a.ts":        false,
		"internal/runner/a.go": false,
	}
	for name, want := range tests {
		if got := isWebSource(name); got != want {
			t.Errorf("isWebSource(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestGuardScanProbesNoWebGuardWithoutAWebRunner covers a build with no checks
// container: the host never reads or tests a web source, and the Go guards are
// still probed.
func TestGuardScanProbesNoWebGuardWithoutAWebRunner(t *testing.T) {
	w := webWorktree(t, map[string]string{webDir + "/src/f.ts": webGuardedFile, "extra.go": guardedFile})
	var pkgs []string
	deps, sum := guardDeps(func(_ context.Context, _, pkg string) (bool, string, error) {
		pkgs = append(pkgs, pkg)
		return false, "", nil
	})

	if _, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pkgs, []string{"."}) {
		t.Fatalf("probed %v, want the Go guard alone", pkgs)
	}
}

// TestGuardScanProbesNoGoGuardWithoutAPackageTestRunner is the converse: a
// build that can run only the web tests reads its web sources and probes no Go
// guard.
func TestGuardScanProbesNoGoGuardWithoutAPackageTestRunner(t *testing.T) {
	w := webWorktree(t, map[string]string{webDir + "/src/f.ts": webGuardedFile, "extra.go": guardedFile})
	var runs [][]string
	deps, sum := guardDeps(nil)
	deps.WebBun = func(_ context.Context, _ string, args ...string) (string, string, bool, error) {
		runs = append(runs, args[2:])
		return "[]", "", false, nil
	}

	if _, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{webDir + "/src/f.ts"}}; !slices.EqualFunc(runs, want, slices.Equal) {
		t.Fatalf("the compiler was given %v, want the one web source", runs)
	}
}

// TestGuardScanReadsWebGuardsInOneRunWithTheEmbeddedScript pins what the
// container is asked to run: bun evaluating the embedded script over exactly
// the web sources the diff changed, test files and declaration files left out.
func TestGuardScanReadsWebGuardsInOneRunWithTheEmbeddedScript(t *testing.T) {
	files := map[string]string{}
	for _, name := range []string{"web/src/a.ts", "web/src/b.tsx", "web/src/a.test.ts", "web/src/a.spec.tsx", "web/src/types.d.ts"} {
		files[name] = webGuardedFile
	}
	w := webWorktree(t, files)
	var runs [][]string
	deps, sum := guardDeps(nil)
	deps.WebBun = func(_ context.Context, dir string, args ...string) (string, string, bool, error) {
		if dir != w.Dir {
			t.Errorf("web run in %q, want the worktree %q", dir, w.Dir)
		}
		runs = append(runs, args)
		return "[]", "", false, nil
	}

	if _, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"-e", guardsScript, "web/src/a.ts", "web/src/b.tsx"}}
	if !slices.EqualFunc(runs, want, slices.Equal) {
		t.Fatalf("web runs = %q, want one evaluating the script over %v", runs, want[0][2:])
	}
}

// TestGuardScanReadsNoWebGuardFromARunThatCannotBeTrusted covers the answers
// that are not guards inside the files the run was given: each is dropped, and
// a failing run is logged, never probed and never reported.
func TestGuardScanReadsNoWebGuardFromARunThatCannotBeTrusted(t *testing.T) {
	const name = webDir + "/src/f.ts"
	tests := []struct {
		name   string
		stdout string
		failed bool
		err    error
		reason string
	}{
		{"a failing run", oneWebGuard, true, nil, "web-run"},
		{"a run that cannot start", oneWebGuard, false, errors.New("no docker"), "web-run"},
		{"output that is not JSON", "oops", false, nil, "web-output"},
		{"a guard in a file it was not given", `[{"file":"web/src/other.ts","line":2,"header":"if (x)","cond":[1,2]}]`, false, nil, ""},
		{"a guard on a line the diff did not add", `[{"file":"web/src/f.ts","line":99,"header":"if (x)","cond":[1,2]}]`, false, nil, ""},
		{"a condition past the end of the file", `[{"file":"web/src/f.ts","line":2,"header":"if (x)","cond":[1,100000]}]`, false, nil, ""},
		{"an empty condition", `[{"file":"web/src/f.ts","line":2,"header":"if (x)","cond":[5,5]}]`, false, nil, ""},
		{"a negative offset", `[{"file":"web/src/f.ts","line":2,"header":"if (x)","cond":[-1,5]}]`, false, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := webWorktree(t, map[string]string{name: webGuardedFile})
			var logs strings.Builder
			deps, sum := guardDeps(nil)
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			probes := 0
			deps.WebBun = func(_ context.Context, _ string, args ...string) (string, string, bool, error) {
				if args[0] == "test" {
					probes++
					return "", bunRunSummary, false, nil
				}
				return tt.stdout, "", tt.failed, tt.err
			}

			findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
			untested := len(unprobed)
			if err != nil {
				t.Fatal(err)
			}
			if probes != 0 || len(findings) != 0 || untested != 0 {
				t.Fatalf("probed %d, found %+v, left %d, want none of it", probes, findings, untested)
			}
			if tt.reason != "" && !strings.Contains(logs.String(), "reason="+tt.reason) {
				t.Fatalf("logs = %q, want reason=%s", logs.String(), tt.reason)
			}
		})
	}
}

// TestGuardScanAcceptsAConditionSpanningTheWholeFile pins the offsets the run
// may answer with at both ends: a condition from the first byte to the last is
// inside the file.
func TestGuardScanAcceptsAConditionSpanningTheWholeFile(t *testing.T) {
	const name = webDir + "/src/f.ts"
	w := webWorktree(t, map[string]string{name: webGuardedFile})
	deps, sum := guardDeps(nil)
	deps.WebBun = func(_ context.Context, _ string, args ...string) (string, string, bool, error) {
		if args[0] == "test" {
			return "", bunRunSummary, false, nil
		}
		return fmt.Sprintf(`[{"file":%q,"line":2,"header":"if (!a)","cond":[0,%d]}]`, name, len(webGuardedFile)), "", false, nil
	}

	findings, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want the one guard whose condition spans the file", findings)
	}
}

// TestGuardScanDoesNotReadWebGuardsOnceTheBudgetIsSpent keeps the discovery
// run inside the stage that owns it, including the instant its budget is
// exactly spent.
func TestGuardScanDoesNotReadWebGuardsOnceTheBudgetIsSpent(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
	}{
		{"a budget spent a minute ago", nfr1BuildBudget + time.Minute},
		{"a budget spent this instant", nfr1BuildBudget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := webWorktree(t, map[string]string{webDir + "/src/f.ts": webGuardedFile})
			deps, sum := guardDeps(nil)
			now := time.Now()
			deps.Now = func() time.Time { return now }
			sum.StartedAt = now.Add(-tt.elapsed)
			runs := 0
			deps.WebBun = func(context.Context, string, ...string) (string, string, bool, error) {
				runs++
				return "[]", "", false, nil
			}

			if _, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil); err != nil {
				t.Fatal(err)
			}
			if runs != 0 {
				t.Fatalf("web ran %d times, want none: the budget was spent", runs)
			}
		})
	}
}

// TestGuardScanLogsNoWebSkipWhenNoWebSourceChanged keeps a spent budget from
// being logged as a skipped web read when there was no web source to read.
func TestGuardScanLogsNoWebSkipWhenNoWebSourceChanged(t *testing.T) {
	w := guardWorktree(t, "", guardedFile)
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) { return true, "", nil })
	var logs strings.Builder
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	deps.WebBun = func(context.Context, string, ...string) (string, string, bool, error) {
		t.Error("web ran for a diff that changes no web source")
		return "[]", "", false, nil
	}
	sum.StartedAt = time.Now().Add(-nfr1BuildBudget - time.Minute)

	if _, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "guardScanSkipped") {
		t.Fatalf("logs = %q, want no skipped web read", logs.String())
	}
}

// TestGuardScanReadsATypeScriptProbeAsAFindingOnlyWhenTestsRan covers the bun
// answers: a red run proves a test caught the break, a green one that ran
// tests is a finding, and a green one that shows no test ran is neither.
func TestGuardScanReadsATypeScriptProbeAsAFindingOnlyWhenTestsRan(t *testing.T) {
	const name = webDir + "/src/f.ts"
	tests := []struct {
		name   string
		out    string
		failed bool
		err    error
		want   int
		reason string
	}{
		{"every test passes", bunRunSummary, false, nil, 1, ""},
		{"a test fails", "(fail) t [1.00ms]\n 2 pass\n 1 fail\nRan 3 tests across 1 file.\n", true, nil, 0, ""},
		{"a green run with no summary", "", false, nil, 0, "no-tests"},
		{"a green run that ran no test", "Ran 0 tests across 0 files.\n", false, nil, 0, "no-tests"},
		{"a red run with no summary", "error: 0 test files matching\n", true, nil, 0, "no-tests"},
		{"a failing test whose output mentions a failed build", "(fail) t [1.00ms]\n[build failed]\nRan 1 test across 1 file.\n", true, nil, 0, ""},
		{"a red run that failed no test", " 0 pass\n 1 fail\n 1 error\nRan 1 test across 1 file.\n", true, nil, 0, "no-test-failure"},
		{"the runner errors", "", false, errors.New("no docker"), 0, "runner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := webWorktree(t, map[string]string{name: webGuardedFile})
			var logs strings.Builder
			deps, sum := guardDeps(nil)
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			deps.WebBun = func(_ context.Context, _ string, args ...string) (string, string, bool, error) {
				if args[0] == "test" {
					return "", tt.out, tt.failed, tt.err
				}
				return oneWebGuard, "", false, nil
			}
			settled := map[string]bool{}

			findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, settled)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != tt.want {
				t.Fatalf("findings = %+v, want %d", findings, tt.want)
			}
			if tt.reason != "" {
				if !strings.Contains(logs.String(), "reason="+tt.reason) || len(unprobed) != 1 || len(settled) != 0 {
					t.Fatalf("logs = %q, unprobed = %d, settled = %d, want reason=%s, one unprobed guard and nothing settled", logs.String(), len(unprobed), len(settled), tt.reason)
				}
			} else if len(unprobed) != 0 || strings.Contains(logs.String(), "guardProbeFailed") {
				t.Fatalf("logs = %q, unprobed = %d, want a probe that ran", logs.String(), len(unprobed))
			}
			got, err := os.ReadFile(filepath.Join(w.Dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != webGuardedFile {
				t.Fatalf("%s = %q, want the file restored byte for byte", name, got)
			}
		})
	}
}

// TestGuardScanBoundsTheWebGuardReadByWhatTheBudgetLeaves pins the deadline of
// the discovery run: a minute at most, and less when the scan's budget leaves
// less.
func TestGuardScanBoundsTheWebGuardReadByWhatTheBudgetLeaves(t *testing.T) {
	tests := []struct {
		name   string
		budget time.Duration
		want   time.Duration
	}{
		{"a budget with plenty left", nfr1BuildBudget, time.Minute},
		{"a budget with twenty seconds left", 20 * time.Second, 20 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := webWorktree(t, map[string]string{webDir + "/src/f.ts": webGuardedFile})
			deps, sum := guardDeps(nil)
			var left time.Duration
			deps.WebBun = func(ctx context.Context, _ string, _ ...string) (string, string, bool, error) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Error("the web discovery run has no deadline")
				}
				left = time.Until(deadline)
				return "[]", "", false, nil
			}

			if _, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, tt.budget, nil); err != nil {
				t.Fatal(err)
			}
			if left > tt.want || left < tt.want-5*time.Second {
				t.Fatalf("discovery had %s, want about %s", left, tt.want)
			}
		})
	}
}

// webProject is a worktree whose web directory holds f.ts, with its guard on
// line 2, and the test files in tests, with the repository's TypeScript
// compiler linked in.
func webProject(t *testing.T, source string, tests map[string]string) Worktree {
	t.Helper()
	files := map[string]string{webDir + "/src/f.ts": source}
	for name, src := range tests {
		files[webDir+"/src/"+name] = src
	}
	w := webWorktree(t, files)
	requireWebCompiler(t, w.Dir)
	return w
}

// realBun is a WebBunRunner that runs the host's bun in the worktree's web
// directory for discovery and for the tests alike.
func realBun() WebBunRunner {
	return bunInWorktree(func(dir string) (string, bool) {
		cmd := exec.Command("bun", "test")
		cmd.Dir = filepath.Join(dir, webDir)
		out, err := cmd.CombinedOutput()
		return string(out), err != nil
	})
}

const (
	guardSource = "export function f(a: string) {\n  if (!a) return \"empty\";\n  return a;\n}\n"
	// missTest never reaches the guard, so no test covers it.
	missTest = "import { expect, test } from \"bun:test\";\nimport { f } from \"./f\";\ntest(\"f\", () => { expect(f(\"x\")).toBe(\"x\"); });\n"
	// hitTest reaches the guard, so breaking it fails this test.
	hitTest = "import { expect, test } from \"bun:test\";\nimport { f } from \"./f\";\ntest(\"f\", () => { expect(f(\"\")).toBe(\"empty\"); });\n"
)

func TestGuardScanRunsTheRealWebTestsAndReadsTheirAnswerPerGuard(t *testing.T) {
	tests := []struct {
		name      string
		tests     map[string]string
		findings  int
		unprobed  int
		wantInLog string
	}{
		{"a test never reaches the guard", map[string]string{"f.test.ts": missTest}, 1, 0, ""},
		{"a test reaches the guard", map[string]string{"f.test.ts": hitTest}, 0, 0, ""},
		{"no test exists", nil, 0, 1, "reason=no-tests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := webProject(t, guardSource, tt.tests)
			var logs strings.Builder
			deps, sum := guardDeps(nil)
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			deps.WebBun = realBun()

			findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != tt.findings || len(unprobed) != tt.unprobed || !strings.Contains(logs.String(), tt.wantInLog) {
				t.Fatalf("findings = %+v, unprobed = %d, logs = %q, want %d findings, %d unprobed and %q", findings, len(unprobed), logs.String(), tt.findings, tt.unprobed, tt.wantInLog)
			}
		})
	}
}

// TestGuardScanReadsABreakThatLeavesTheFileUnparsableAsUnprobed breaks the guard
// at offsets that make the mutation a syntax error, as a wrong offset would:
// bun fails the run without failing a test, which proves nothing.
func TestGuardScanReadsABreakThatLeavesTheFileUnparsableAsUnprobed(t *testing.T) {
	w := webProject(t, guardSource, map[string]string{"f.test.ts": hitTest})
	start := strings.Index(guardSource, "return")
	discovery := fmt.Sprintf(`[{"file":"web/src/f.ts","line":2,"header":"if (!a)","cond":[%d,%d]}]`, start, start+6)
	real := realBun()
	deps, sum := guardDeps(nil)
	deps.WebBun = func(ctx context.Context, dir string, args ...string) (string, string, bool, error) {
		if args[0] == "test" {
			return real(ctx, dir, args...)
		}
		return discovery, "", false, nil
	}

	findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unprobed) != 1 {
		t.Fatalf("findings = %+v, unprobed = %d, want the guard left without a verdict, not covered", findings, len(unprobed))
	}
}

func TestGuardScanSkipsAWebSourceThatIsNotValidUTF8(t *testing.T) {
	w := webProject(t, "// \xff\n"+guardSource, map[string]string{"f.test.ts": hitTest})
	var logs strings.Builder
	deps, sum := guardDeps(nil)
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	deps.WebBun = func(context.Context, string, ...string) (string, string, bool, error) {
		t.Error("bun ran for a source whose offsets cannot be trusted")
		return "", "", false, nil
	}

	findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unprobed) != 0 || !strings.Contains(logs.String(), "reason=not-utf8") {
		t.Fatalf("findings = %+v, unprobed = %d, logs = %q, want the file skipped as not-utf8", findings, len(unprobed), logs.String())
	}
}

// TestGuardScanCountsLinesTheWayGitDoes covers the separators git does not
// break a line on: U+2028 and a lone carriage return sit in line 1, so the
// guard is on line 3.
func TestGuardScanCountsLinesTheWayGitDoes(t *testing.T) {
	const source = "const s = \"a\u2028b\r\";\nexport function f(a: string) {\n  if (!a) return s;\n  return a;\n}\n"
	w := webProject(t, source, nil)
	deps, sum := guardDeps(nil)
	deps.WebBun = bunInWorktree(func(string) (string, bool) { return bunRunSummary, false })

	findings, _, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].text, "f.ts:3 ") {
		t.Fatalf("findings = %+v, want the guard on line 3", findings)
	}
}

// TestGuardScanReadsTheFilesAroundOneTheContainerCannotRead covers a symlink
// whose target the container lacks: the guard in the readable file is still
// found, and the link itself is never read or written on the host. The target
// holds a guard on its first line, the line the diff adds for the link, so a
// read through the link would find it.
func TestGuardScanReadsTheFilesAroundOneTheContainerCannotRead(t *testing.T) {
	const outsideSource = "if (!a) return 1;\n"
	w := webProject(t, guardSource, nil)
	outside := filepath.Join(t.TempDir(), "outside.ts")
	if err := os.WriteFile(outside, []byte(outsideSource), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(w.Dir, webDir, "src", "link.ts")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/host/path.ts", filepath.Join(w.Dir, webDir, "src", "dangling.ts")); err != nil {
		t.Fatal(err)
	}
	deps, sum := guardDeps(nil)
	bun := bunInWorktree(func(dir string) (string, bool) {
		if got, _ := os.ReadFile(outside); string(got) != outsideSource {
			t.Errorf("the probe wrote through a symlink: %q", got)
		}
		return bunRunSummary, false
	})
	var read []string
	deps.WebBun = func(ctx context.Context, dir string, args ...string) (string, string, bool, error) {
		if args[0] == "-e" {
			read = args[2:]
		}
		return bun(ctx, dir, args...)
	}

	findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].text, "f.ts:2 ") || len(unprobed) != 0 {
		t.Fatalf("findings = %+v, unprobed = %d, want the one guard in the regular file and none through a link", findings, len(unprobed))
	}
	if want := []string{webDir + "/src/f.ts"}; !slices.Equal(read, want) {
		t.Fatalf("the compiler was given %v, want %v: a link is never read on the host", read, want)
	}
}

// TestGuardScanReadsAMissingFileThroughTheScriptWithoutLosingTheRest runs the
// real script over a path the container cannot read next to one it can.
func TestGuardScanReadsAMissingFileThroughTheScriptWithoutLosingTheRest(t *testing.T) {
	w := webProject(t, guardSource, nil)
	cmd := exec.Command("bun", "-e", guardsScript, "web/src/missing.ts", "web/src/f.ts")
	cmd.Dir = filepath.Join(w.Dir, webDir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the script failed over an unreadable file: %v", err)
	}
	if !strings.Contains(string(out), `"file":"web/src/f.ts"`) {
		t.Fatalf("script output = %q, want the readable file's guard", out)
	}
}

// scriptGuardSource has a guard of each exit kind, an empty block, a guard with
// an else and a guard whose header spans lines; the guards are on lines 3, 4, 5,
// 8 and 10.
const scriptGuardSource = "export function f(xs: number[], a: string) {\n" +
	"  for (const x of xs) {\n" +
	"    if (x < 0) continue;\n" +
	"    if (x > 9) { break; }\n" +
	"    if (x === 5) { const y = x; return y; }\n" +
	"  }\n" +
	"  if (!a) {}\n" +
	"  if (a === \"x\") throw new Error(\"x\");\n" +
	"  if (a === \"y\") { return 1; } else { return 2; }\n" +
	"  if (\n" +
	"    a === \"z\"\n" +
	"  ) return 3;\n" +
	"  return 0;\n" +
	"}\n"

// TestGuardsScriptReadsEveryGuardWithItsHeaderAndCondition runs the real script:
// a return, a throw, a break and a continue each make a guard whether the body
// is braced or not, an empty block and an else do not, and each guard's header
// has its whitespace collapsed and its condition offsets cover the condition
// alone.
func TestGuardsScriptReadsEveryGuardWithItsHeaderAndCondition(t *testing.T) {
	w := webProject(t, scriptGuardSource, nil)
	cmd := exec.Command("bun", "-e", guardsScript, webDir+"/src/f.ts")
	cmd.Dir = filepath.Join(w.Dir, webDir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the script failed: %v", err)
	}
	var found []struct {
		Line   int
		Header string
		Cond   [2]int
	}
	if err := json.Unmarshal(out, &found); err != nil {
		t.Fatalf("script output = %q: %v", out, err)
	}
	type guardRead struct {
		line         int
		header, cond string
	}
	var got []guardRead
	for _, f := range found {
		got = append(got, guardRead{f.Line, f.Header, scriptGuardSource[f.Cond[0]:f.Cond[1]]})
	}
	want := []guardRead{
		{3, "if (x < 0)", "x < 0"},
		{4, "if (x > 9)", "x > 9"},
		{5, "if (x === 5)", "x === 5"},
		{8, `if (a === "x")`, `a === "x"`},
		{10, `if ( a === "z" )`, `a === "z"`},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("script read %+v, want %+v", got, want)
	}
}

// oneLineGuarded is a Go source whose guard is on its first line, the line the
// diff adds for a symlink to it.
const oneLineGuarded = "package x; func f(err error) error { if err != nil { return err }; return nil }\n"

func TestGuardScanDoesNotReadOrProbeThroughASymlink(t *testing.T) {
	w := guardWorktree(t, "", "")
	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, []byte(oneLineGuarded), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.Dir, "linked.go")); err != nil {
		t.Fatal(err)
	}
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		t.Error("a package's tests ran for a symlinked source")
		return false, "", nil
	})

	findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unprobed) != 0 {
		t.Fatalf("findings = %+v, unprobed = %+v, want the link never read as a source", findings, unprobed)
	}
	if got, _ := os.ReadFile(outside); string(got) != oneLineGuarded {
		t.Fatalf("outside file = %q, want it untouched", got)
	}
}

func TestProbeGuardRefusesASymlinkedSourceWithoutWritingThroughIt(t *testing.T) {
	w := guardWorktree(t, "", "")
	outside := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(outside, []byte(guardedFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.Dir, "linked.go")); err != nil {
		t.Fatal(err)
	}
	deps, _ := guardDeps(func(context.Context, string, string) (bool, string, error) {
		t.Error("a package's tests ran for a symlinked source")
		return false, "", nil
	})
	guards, err := guardsIn("linked.go", []byte(guardedFile), []int{4})
	if err != nil {
		t.Fatal(err)
	}

	verdict, err := probeGuard(context.Background(), deps, w, guards[0], time.Minute)
	if err != nil || verdict != guardUnprobed {
		t.Fatalf("probeGuard = %v, %v, want the symlinked guard left unprobed", verdict, err)
	}
	if got, _ := os.ReadFile(outside); string(got) != guardedFile {
		t.Fatalf("outside file = %q, want it untouched", got)
	}
}

func TestGuardScanLeavesAGuardWhoseProbeCannotRunUnprobedAndUnsettled(t *testing.T) {
	w := guardWorktree(t, "", guardedFile)
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		return false, "", errors.New("no docker")
	})
	settled := map[string]bool{}

	findings, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, settled)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(unprobed) != 1 || len(settled) != 0 {
		t.Fatalf("findings = %+v, unprobed = %d, settled = %v, want one unprobed guard and nothing settled", findings, len(unprobed), settled)
	}
}

func TestCappedBufferKeepsOnlyTheFirstBytesAndReportsEveryWrite(t *testing.T) {
	b := cappedBuffer{limit: 5}
	for _, chunk := range []string{"abc", "defgh", "ijk"} {
		if n, err := b.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v, want the whole chunk reported written", chunk, n, err)
		}
	}
	if b.String() != "abcde" {
		t.Fatalf("kept %q, want the first 5 bytes", b.String())
	}
}

// TestGuardScanKeepsAnUnprobedGuardsKeyUnsettledWhenAnotherGuardSharesIt is the
// same sharing for a guard left without a verdict: a covered guard with its
// key must not hide it from the next scan.
func TestGuardScanKeepsAnUnprobedGuardsKeyUnsettledWhenAnotherGuardSharesIt(t *testing.T) {
	const src = "package x\n\nfunc a(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\nfunc b(err error) error {\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n"
	w := guardWorktree(t, "", src)
	calls := 0
	deps, sum := guardDeps(func(context.Context, string, string) (bool, string, error) {
		calls++
		if calls == 1 {
			return true, "", nil // the first guard is covered
		}
		return false, "", errors.New("no docker") // the second cannot be probed
	})
	settled := make(map[string]bool)

	_, unprobed, err := scanGuards(context.Background(), deps, w, sum.StartedAt, nfr1BuildBudget, settled)
	if err != nil {
		t.Fatal(err)
	}
	if len(unprobed) != 1 || settled[unprobed[0].key] {
		t.Fatalf("unprobed = %+v, settled = %v, want the shared key left unsettled", unprobed, settled)
	}
}
