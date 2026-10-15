package runner

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// guardScanMax bounds how many guards one scan breaks and tests. Every probe
// runs a package's whole test suite, so a diff adding more guards than this is
// probed from the top and the rest are logged as untested.
const guardScanMax = 5

// guardProbeTimeout bounds one probe, under whatever the run's budget leaves:
// one package's tests, with the module cache warm and the package built from
// scratch inside the checks container.
const guardProbeTimeout = 5 * time.Minute

// guard is an if statement the diff adds, carrying no else, whose body leaves
// its block. Disabling its condition is then the same as deleting the guard —
// the code after it runs either way — so breaking the guard is what tells
// whether a test covers what it guards, rather than the runner naming it and
// asking.
type guard struct {
	// file is the guard's path, relative to the worktree.
	file string
	// pkg is the go test pattern for the package that holds it.
	pkg string
	// line is the line the `if` starts on, in the file as it now stands.
	line int
	// header is the statement's `if …` source with its whitespace collapsed:
	// it names the guard across scans, once a fix round has moved its line.
	header string
	// cond is the byte range the condition occupies, which the break replaces
	// with false.
	cond [2]int
}

// key identifies the guard across scans.
func (g guard) key() string { return g.file + "\x00" + g.header }

// guardFinding is the finding one uncovered guard is reported as.
func guardFinding(g guard) string {
	return fmt.Sprintf("guard at %s:%d can be removed and every test in %s still passes", g.file, g.line, g.pkg)
}

// scanGuards breaks each guard the diff adds in turn and reports the ones whose
// package's tests still pass: a guard no test covers, which the walkthrough
// finds by breaking it rather than by naming it. Every probe runs through
// d.PackageTests, in the checks container, so no repository code runs on the
// host that the checks themselves keep off it. A scan with no package-test
// runner, or one with no budget left, probes nothing.
func scanGuards(ctx context.Context, d BuildDeps, wt Worktree, sum *Summary) ([]pendingFinding, error) {
	if d.PackageTests == nil {
		return nil, nil
	}
	added, err := wt.PendingAddedLines(ctx)
	if err != nil {
		return nil, stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	guards := changedGuards(d.Logger, wt.Dir, added)
	if len(guards) > guardScanMax {
		d.Logger.Info("guardsUntested", "count", len(guards)-guardScanMax, "reason", "cap")
		guards = guards[:guardScanMax]
	}
	var findings []pendingFinding
	for i, g := range guards {
		if !withinBudget(sum.StartedAt, d.Now()) {
			d.Logger.Info("guardsUntested", "count", len(guards)-i, "reason", "budget")
			break
		}
		uncovered, err := probeGuard(ctx, d, wt, sum, g)
		if err != nil {
			return nil, stopWith(OutcomeInfraFailure, StopChecksRun, err)
		}
		if !uncovered {
			continue
		}
		findings = append(findings, pendingFinding{key: g.key(), text: guardFinding(g)})
		d.Logger.Info("guardFinding", "file", g.file, "line", g.line)
	}
	return findings, nil
}

// changedGuards is every guard the changed files add, in path order and then in
// source order within a file, so a capped scan always probes the same ones. A
// file it cannot read or parse — a deleted one, or one this run never wrote —
// is skipped and logged.
func changedGuards(logger *slog.Logger, dir string, added map[string][]int) []guard {
	files := make([]string, 0, len(added))
	for name := range added {
		if isGoSource(name) {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	var guards []guard
	for _, name := range files {
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			logger.Warn("guardScanSkipped", "file", name, "reason", "unreadable")
			continue
		}
		found, err := guardsIn(name, src, added[name])
		if err != nil {
			logger.Warn("guardScanSkipped", "file", name, "reason", "unparsable")
			continue
		}
		guards = append(guards, found...)
	}
	return guards
}

// guardsIn is every guard added on one of added's lines, in source order: an if
// with no else whose own last statement is a return, a break, a continue or a
// panic call. An if with an else is not one: forcing its condition false runs
// the else branch instead of deleting the guard, which is a different program
// and so tests something the diff never wrote.
func guardsIn(name string, src []byte, added []int) ([]guard, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	lines := make(map[int]bool, len(added))
	for _, l := range added {
		lines[l] = true
	}
	var guards []guard
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		line := fset.Position(stmt.If).Line
		if lines[line] && stmt.Else == nil && blockExits(stmt.Body) {
			guards = append(guards, guard{
				file:   name,
				pkg:    guardPackage(name),
				line:   line,
				header: guardHeader(stmt, src),
				cond:   [2]int{int(stmt.Cond.Pos()) - 1, int(stmt.Cond.End()) - 1},
			})
		}
		return true
	})
	return guards, nil
}

// blockExits reports whether the block's own last statement leaves it: a
// return, a break, a continue or a panic call. What the guard protects is then
// invisible to the flow through it, which is what makes deleting the guard
// something only a test can catch.
func blockExits(b *ast.BlockStmt) bool {
	if len(b.List) == 0 {
		return false
	}
	switch last := b.List[len(b.List)-1].(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return last.Tok == token.BREAK || last.Tok == token.CONTINUE
	case *ast.ExprStmt:
		call, ok := last.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		fn, ok := call.Fun.(*ast.Ident)
		return ok && fn.Name == "panic"
	}
	return false
}

// guardHeader is the guard's `if …` source with its whitespace collapsed.
func guardHeader(stmt *ast.IfStmt, src []byte) string {
	start, end := int(stmt.If)-1, int(stmt.Body.Lbrace)-1
	if start < 0 || end > len(src) || start >= end {
		return ""
	}
	return strings.Join(strings.Fields(string(src[start:end])), " ")
}

// guardPackage is the go test pattern for the package that holds name: the
// directory the file sits in, as `go test` takes it.
func guardPackage(name string) string {
	dir := path.Dir(name)
	if dir == "." {
		return "."
	}
	return "./" + dir
}

// isGoSource reports whether name is Go source a probe can test: a .go file
// that is not a test file, since the guard has to be code the tests exercise.
func isGoSource(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// breakGuard is src with the guard's condition replaced by false, so its body
// never runs: for a body that leaves the block, that is src with the guard
// deleted — the smallest change that disables it and leaves the rest of the file
// as the tests were written against it.
func breakGuard(src []byte, g guard) []byte {
	out := make([]byte, 0, len(src)+len("false"))
	out = append(out, src[:g.cond[0]]...)
	out = append(out, "false"...)
	return append(out, src[g.cond[1]:]...)
}

// probeGuard breaks g in the worktree, runs its package's tests, and restores
// the file — always, even on error: a probe that left the mutation behind would
// corrupt the run. It reports whether the guard is uncovered: true when every
// test still passed with it broken, which is the finding, and false when
// breaking it failed a test or when the probe could not run at all.
func probeGuard(ctx context.Context, d BuildDeps, wt Worktree, sum *Summary, g guard) (uncovered bool, err error) {
	name := filepath.Join(wt.Dir, g.file)
	src, err := os.ReadFile(name)
	if err != nil {
		d.Logger.Warn("guardProbeFailed", "file", g.file, "line", g.line, "reason", "unreadable")
		return false, nil
	}
	if err := os.WriteFile(name, breakGuard(src, g), 0o600); err != nil {
		return false, fmt.Errorf("breaking the guard at %s:%d: %w", g.file, g.line, err)
	}
	defer func() {
		if rerr := os.WriteFile(name, src, 0o600); rerr != nil && err == nil {
			err = fmt.Errorf("restoring %s: %w", g.file, rerr)
		}
	}()

	timeout := guardProbeTimeout
	if remaining := checkLoopBudget - d.Now().Sub(sum.StartedAt); remaining < timeout {
		timeout = remaining
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	failed, _, perr := d.PackageTests(tctx, wt.Dir, g.pkg)
	switch {
	case perr != nil:
		d.Logger.Warn("guardProbeFailed", "file", g.file, "line", g.line, "reason", "runner")
		return false, nil
	case tctx.Err() != nil:
		d.Logger.Warn("guardProbeFailed", "file", g.file, "line", g.line, "reason", "timeout")
		return false, nil
	}
	return !failed, nil
}
