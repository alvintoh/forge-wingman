package runner

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// guardProbeTimeout bounds one probe, under whatever the run's budget leaves:
// one package's tests, with the module cache warm and the package built from
// scratch inside the checks container.
const guardProbeTimeout = 5 * time.Minute

// guardDiscoveryTimeout bounds the one container run that reads every changed
// web source's guards, under whatever the scan's budget leaves.
const guardDiscoveryTimeout = time.Minute

// guardsScript is the program bun runs in the checks container to list the
// guards in the web sources it is given, with the branch's own TypeScript
// compiler.
//
//go:embed guards_ts.js
var guardsScript string

// bunRan matches the summary bun test ends a run that ran tests with.
var bunRan = regexp.MustCompile(`(?m)^Ran [1-9][0-9]* tests? across `)

// bunTestFailed matches the line bun test prints for a test that failed, which
// a file that fails to load does not produce.
var bunTestFailed = regexp.MustCompile(`(?m)^\(fail\) `)

// guardVerdict is what probing one guard established.
type guardVerdict int

const (
	// guardCovered is a guard whose break failed a test.
	guardCovered guardVerdict = iota
	// guardUncovered is a guard whose break every test survived.
	guardUncovered
	// guardUnprobed is a guard no verdict was reached for: its probe could not
	// run, or the budget ran out first.
	guardUnprobed
)

// guard is an if statement the diff adds, carrying no else, whose body leaves
// its block. Disabling its condition is then the same as deleting the guard —
// the code after it runs either way — so breaking the guard is what tells
// whether a test covers what it guards, rather than the runner naming it and
// asking.
type guard struct {
	// file is the guard's path, relative to the worktree.
	file string
	// pkg names the tests that cover it: the go test pattern for the package
	// that holds it, or webDir for a web source.
	pkg string
	// web is set for a TypeScript guard, probed with the web tests.
	web bool
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
// tests still pass: a guard no test covers, which the walkthrough finds by
// breaking it rather than by naming it. Every probe runs in the checks
// container, through d.PackageTests for a Go guard and d.WebBun for a
// TypeScript one, so no repository code runs on the host that the checks
// themselves keep off it. The scan is charged to the stage that owns it —
// started and budget — so a scan with no runner, or one whose stage has spent
// its budget, probes nothing.
//
// It breaks every guard the diff adds while that budget lasts, in the order
// changedGuards returns them, skipping any whose key is in settled. unprobed is
// every guard it reached no verdict for, because the budget ran out or its
// probe could not run, which is never read as covered. A covered guard goes
// into settled, and a finding or an unprobed guard stays out of it, so a later
// scan sharing the set probes those again and the guards added since. Two
// guards in one file with the same header share a key, so a finding keeps both
// probed. A nil settled skips and records nothing.
func scanGuards(ctx context.Context, d BuildDeps, wt Worktree, started time.Time, budget time.Duration, settled map[string]bool) (findings, unprobed []pendingFinding, err error) {
	if d.PackageTests == nil && d.WebBun == nil {
		return nil, nil, nil
	}
	added, err := wt.PendingAddedLines(ctx)
	if err != nil {
		return nil, nil, stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	guards := slices.DeleteFunc(changedGuards(ctx, d, wt.Dir, added, budget-d.Now().Sub(started)), func(g guard) bool {
		return settled[g.key()]
	})
	var covered []guard
	for i, g := range guards {
		now := d.Now()
		if !withinBudget(started, now, budget) {
			for _, rest := range guards[i:] {
				unprobed = append(unprobed, guardPending(rest))
			}
			break
		}
		verdict, err := probeGuard(ctx, d, wt, g, budget-now.Sub(started))
		if err != nil {
			return nil, nil, stopWith(OutcomeInfraFailure, StopChecksRun, err)
		}
		switch verdict {
		case guardCovered:
			covered = append(covered, g)
		case guardUncovered:
			findings = append(findings, guardPending(g))
			d.Logger.Info("guardFinding", "file", g.file, "line", g.line)
		case guardUnprobed:
			unprobed = append(unprobed, guardPending(g))
		}
	}
	if settled != nil {
		for _, g := range covered {
			settled[g.key()] = true
		}
		for _, f := range slices.Concat(findings, unprobed) {
			delete(settled, f.key)
		}
	}
	return findings, unprobed, nil
}

// guardPending is the finding g is reported as when it is uncovered, and the
// key a later scan matches it by when it was left unprobed.
func guardPending(g guard) pendingFinding {
	return pendingFinding{key: g.key(), text: guardFinding(g), stable: true}
}

// changedGuards is every guard the changed files add that d can probe, in path
// order and then in source order within a file, so a scan stopped at its budget
// always probes the same ones first. A file it cannot read or parse — a deleted
// one, or one this run never wrote — is skipped and logged. left is what the
// scan's budget has left, which bounds reading the web sources.
func changedGuards(ctx context.Context, d BuildDeps, dir string, added map[string][]int, left time.Duration) []guard {
	files := make([]string, 0, len(added))
	for name := range added {
		if (d.PackageTests != nil && isGoSource(name)) || (d.WebBun != nil && isWebSource(name)) {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	web := slices.DeleteFunc(slices.Clone(files), func(name string) bool { return !isWebSource(name) })
	webFound := webGuards(ctx, d, dir, web, added, left)
	var guards []guard
	for _, name := range files {
		if isWebSource(name) {
			guards = append(guards, webFound[name]...)
			continue
		}
		src, err := readRegularFile(filepath.Join(dir, name))
		if err != nil {
			d.Logger.Warn("guardScanSkipped", "file", name, "reason", "unreadable")
			continue
		}
		found, err := guardsIn(name, src, added[name])
		if err != nil {
			d.Logger.Warn("guardScanSkipped", "file", name, "reason", "unparsable")
			continue
		}
		guards = append(guards, found...)
	}
	return guards
}

// webGuards is every guard the diff adds to files, the web sources it changed,
// by file. The TypeScript compiler reads them in one container run, so no
// branch code runs on the host; a run that fails, outlasts left or answers with
// anything but guards inside the files it was given yields none, and is logged.
func webGuards(ctx context.Context, d BuildDeps, dir string, files []string, added map[string][]int, left time.Duration) map[string][]guard {
	if len(files) == 0 {
		return nil
	}
	if left <= 0 {
		d.Logger.Warn("guardScanSkipped", "reason", "budget", "files", len(files))
		return nil
	}
	sources := make(map[string]int, len(files))
	args := []string{"-e", guardsScript}
	for _, name := range files {
		src, err := readRegularFile(filepath.Join(dir, name))
		switch {
		case err != nil:
			d.Logger.Warn("guardScanSkipped", "file", name, "reason", "unreadable")
			continue
		case !utf8.Valid(src):
			// The compiler counts offsets in characters, so a byte it cannot
			// decode would put every condition after it in the wrong place.
			d.Logger.Warn("guardScanSkipped", "file", name, "reason", "not-utf8")
			continue
		}
		sources[name] = len(src)
		args = append(args, name)
	}
	if len(sources) == 0 {
		return nil
	}
	dctx, cancel := context.WithTimeout(ctx, min(guardDiscoveryTimeout, left))
	defer cancel()
	stdout, _, failed, err := d.WebBun(dctx, dir, args...)
	var found []struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Header string `json:"header"`
		Cond   [2]int `json:"cond"`
	}
	switch {
	case err != nil || failed:
		d.Logger.Warn("guardScanSkipped", "reason", "web-run", "files", len(sources))
		return nil
	case json.Unmarshal([]byte(stdout), &found) != nil:
		d.Logger.Warn("guardScanSkipped", "reason", "web-output", "files", len(sources))
		return nil
	}
	guards := make(map[string][]guard)
	for _, f := range found {
		// A file the run was not given has size zero, so no condition fits in it.
		size := sources[f.File]
		if !slices.Contains(added[f.File], f.Line) || f.Cond[0] < 0 || f.Cond[0] >= f.Cond[1] || f.Cond[1] > size {
			continue
		}
		guards[f.File] = append(guards[f.File], guard{file: f.File, pkg: webDir, web: true, line: f.Line, header: f.Header, cond: f.Cond})
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

// isWebSource reports whether name is a TypeScript source under webDir that a
// probe can test: not a test file or a declaration file, since the guard has to
// be code the tests exercise.
func isWebSource(name string) bool {
	if !strings.HasPrefix(name, webDir+"/") || strings.HasSuffix(name, ".d.ts") {
		return false
	}
	base := path.Base(name)
	if !strings.HasSuffix(base, ".ts") && !strings.HasSuffix(base, ".tsx") {
		return false
	}
	return !strings.Contains(base, ".test.") && !strings.Contains(base, ".spec.")
}

// breakGuard is src with the guard's condition made false, so its body never
// runs: for a body that leaves the block, that is src with the guard deleted —
// the smallest change that disables it and leaves the rest of the file as the
// tests were written against it. The condition is kept behind `false &&` rather
// than replaced: a variable only the condition reads (an init clause's, or `ok`
// from a map lookup) would otherwise be declared and not used, the package would
// not build, and the guard would read as covered.
//
// For example, `if n := len(s); n == 0 {` becomes `if n := len(s); false && (n == 0) {`.
func breakGuard(src []byte, g guard) []byte {
	out := make([]byte, 0, len(src)+len("false && ()"))
	out = append(out, src[:g.cond[0]]...)
	out = append(out, "false && ("...)
	out = append(out, src[g.cond[0]:g.cond[1]]...)
	out = append(out, ')')
	return append(out, src[g.cond[1]:]...)
}

// runGuardTests runs the tests that cover g under dir: its package's for a Go
// guard, the web tests for a TypeScript one.
func runGuardTests(ctx context.Context, d BuildDeps, dir string, g guard) (failed bool, output string, err error) {
	if !g.web {
		return d.PackageTests(ctx, dir, g.pkg)
	}
	stdout, stderr, failed, err := d.WebBun(ctx, dir, "test")
	return failed, stdout + stderr, err
}

// readRegularFile is the contents of name, refusing anything but a regular
// file: a symlink in the worktree is the branch's own and could point outside it.
func readRegularFile(name string) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return os.ReadFile(name)
}

// probeGuard breaks g in the worktree, runs the tests that cover it, and restores
// the file — always, even on error: a probe that left the mutation behind would
// corrupt the run. left is what the owning stage's budget had left when the scan
// reached this guard, so the probe can never outlast that stage's own budget. It
// reports guardUncovered when every test still passed with it broken, which is
// the finding, guardCovered when breaking it failed a test, and guardUnprobed
// when the probe could not run or its failure shows no test failed.
func probeGuard(ctx context.Context, d BuildDeps, wt Worktree, g guard, left time.Duration) (verdict guardVerdict, err error) {
	name := filepath.Join(wt.Dir, g.file)
	src, err := readRegularFile(name)
	if err != nil {
		d.Logger.Warn("guardProbeFailed", "file", g.file, "line", g.line, "reason", "unreadable")
		return guardUnprobed, nil
	}
	if err := os.WriteFile(name, breakGuard(src, g), 0o600); err != nil {
		return guardUnprobed, fmt.Errorf("breaking the guard at %s:%d: %w", g.file, g.line, err)
	}
	defer func() {
		if rerr := os.WriteFile(name, src, 0o600); rerr != nil && err == nil {
			err = fmt.Errorf("restoring %s: %w", g.file, rerr)
		}
	}()

	timeout := guardProbeTimeout
	if left < timeout {
		timeout = left
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	failed, output, perr := runGuardTests(tctx, d, wt.Dir, g)
	reason := ""
	switch {
	case perr != nil:
		reason = "runner"
	case tctx.Err() != nil:
		reason = "timeout"
	case !g.web && failed && strings.Contains(output, "[build failed]"):
		// A break that does not build failed no test, so it proves nothing
		// either way.
		reason = "build"
	case g.web && !bunRan.MatchString(output):
		reason = "no-tests"
	case g.web && failed && !bunTestFailed.MatchString(output):
		// A file that no longer loads fails the run without failing a test.
		reason = "no-test-failure"
	}
	if reason != "" {
		d.Logger.Warn("guardProbeFailed", "file", g.file, "line", g.line, "reason", reason)
		return guardUnprobed, nil
	}
	if failed {
		return guardCovered, nil
	}
	return guardUncovered, nil
}
