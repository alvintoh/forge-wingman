package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// checkGofmt, checkVet, checkLint and checkTest name RunChecks's four Go gates,
// in the order Makefile's check target runs them; gates in record.go is built
// from these.
const (
	checkGofmt = "gofmt"
	checkVet   = "vet"
	checkLint  = "golangci-lint"
	checkTest  = "test"
)

// checkWebInstall, checkWebLint, checkWebFmtCheck and checkWebBuild name the
// web job's steps (ci.yml), in its order. RunChecks runs them after the Go
// gates when the run's diff touches webDir, and names a failing one as it names
// a Go gate. They are deliberately not in record.go's gates, which are the
// gates run.yml's check job runs: that job runs the Go gates alone.
const (
	checkWebInstall  = "web-install"
	checkWebLint     = "web-lint"
	checkWebFmtCheck = "web-fmt-check"
	checkWebBuild    = "web-build"
)

// webDir is the directory the repository's front end lives under. A run whose
// diff touches it also runs the web job's steps, so the run's own verdict
// covers everything that job would fail on.
const webDir = "web"

// webStep is one of the web job's steps: the gate a failure in it is reported
// as, and the arguments bun runs for it with webDir as the working directory.
type webStep struct {
	gate string
	args []string
}

// webSteps are the web job's steps in CI's order. offline makes the install
// resolve and install from bun's cache alone, which the checks container needs
// — it runs with no network — and which the host path, whose bun fetches over
// the network CI's does, is not given.
func webSteps(offline bool) []webStep {
	install := []string{"install", "--frozen-lockfile"}
	if offline {
		install = append(install, "--offline")
	}
	return []webStep{
		{checkWebInstall, install},
		{checkWebLint, []string{"run", "lint"}},
		{checkWebFmtCheck, []string{"run", "fmt:check"}},
		{checkWebBuild, []string{"run", "build"}},
	}
}

// touchesWeb reports whether any of the run's changed paths is webDir or sits
// under it, which is when the web job's steps apply.
func touchesWeb(changed []string) bool {
	return slices.ContainsFunc(changed, func(p string) bool {
		return p == webDir || strings.HasPrefix(p, webDir+"/")
	})
}

// gofmtArgs is the gofmt gate's command line, run before exitGates.
var gofmtArgs = []string{"gofmt", "-l", "."}

// exitGates are the gates after gofmt, each judged by its exit status, in
// Makefile's check order.
var exitGates = []struct {
	gate string
	bin  string
	args []string
}{
	{checkVet, "go", []string{"vet", "./..."}},
	{checkLint, "golangci-lint", []string{"run"}},
	{checkTest, "go", []string{"test", "-race", "-shuffle=on", "-cover", "./..."}},
}

// checkEnvNames are the variables a check gate's subprocess inherits: the Go
// toolchain's, golangci-lint's and bun's own needs, deliberately not an agent's
// API key or the workload-identity credentials model.yml's auth
// step exports. An allow-list, not an exclude-list, so a secret added to
// that job later isn't carried in by default. TEMP/TMP/LocalAppData are
// Windows' equivalents of TMPDIR/GOCACHE's own fallback path, needed for
// `make check` to run locally on Windows (see the repo's other Windows fixes).
var checkEnvNames = []string{
	"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "LOCALAPPDATA", "LANG",
	"GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE", "GOTOOLCHAIN", "GOFLAGS",
	"LC_", "XDG_",
}

// checkCmdEnv is the environment every RunChecks subprocess runs under. It
// withholds what the job holds in its environment, not what it holds on disk:
// the boundary for a credentials file and the network is ContainerChecks, whose
// gates run in a container with no network that mounts the worktree and the
// caches and binaries that gate needs — the module and build caches and the
// linter for a Go gate, the host's bun and its cache for a web step — and none
// of the job's home, temp or workspace directories.
func checkCmdEnv() []string {
	return filterEnv(os.Environ(), checkEnvNames)
}

// CheckRunner runs the target repository's own quality gates against dir,
// whose changed paths are the run's diff against its base — ContainerChecks.Run
// in production, RunChecks where no checks image is configured, a fake in a
// test that exercises the pre-PR loop's wiring without shelling out to a real
// Go toolchain.
type CheckRunner func(ctx context.Context, dir string, changed []string) (failedGate, output string, err error)

// PackageTestRunner runs one package's tests against dir, and reports whether
// they failed. It is ContainerChecks.RunPackageTest in production and
// RunPackageTest where no checks image is configured, so a package's tests run
// under the same boundary as the gates; the pre-PR loop's guard probe is its
// only caller, and a build with none set skips that probe.
type PackageTestRunner func(ctx context.Context, dir, pkg string) (failed bool, output string, err error)

// RunChecks runs the repository's own quality gates against dir, in the same
// order and with the same commands as Makefile's check target and, when changed
// touches webDir, ci.yml's web job after them, stopping at the first failure.
// It returns ("", "", nil) when every gate passes; otherwise the failing gate's
// name — one of gates in record.go, or one of the web gates — and its output.
// An error reports a gate that could not be run at all, never a gate that ran
// and failed.
//
// This in-job pass and run.yml's separate check job both run these same Go
// gates: deliberately, not redundantly — this one gives the pre-PR loop
// fast, cheap feedback to rebuild against, while the separate job (no
// secrets, its own runner) stays the trusted, final gate before merge. The web
// job's steps are this pass's alone: run.yml's check job does not run them, so
// a web failure caught here is one the PR's own CI would otherwise be the first
// to report.
func RunChecks(ctx context.Context, dir string, changed []string) (string, string, error) {
	if failed, output, err := runGofmt(ctx, dir); err != nil {
		return "", "", err
	} else if failed {
		return checkGofmt, output, nil
	}
	for _, g := range exitGates {
		output, failed, err := runGate(ctx, dir, g.bin, g.args...)
		if err != nil {
			return "", "", fmt.Errorf("running %s: %w", g.gate, err)
		}
		if failed {
			return g.gate, output, nil
		}
	}
	if !touchesWeb(changed) {
		return "", "", nil
	}
	return runWebSteps(ctx, dir, webSteps(false))
}

// lookPath finds a binary on PATH; a test replaces it to make bun missing.
var lookPath = exec.LookPath

// RunPackageTest runs one package's tests against dir with the go on PATH: this
// pass's counterpart to RunChecks for a single package, which the guard probe
// breaks a guard against. A package that cannot be tested at all — a pattern
// naming no package, or a tree that does not build — is reported as a failure,
// so nothing is read into an answer the tests never gave.
func RunPackageTest(ctx context.Context, dir, pkg string) (bool, string, error) {
	output, failed, err := runGate(ctx, dir, "go", "test", pkg)
	if err != nil {
		return false, "", err
	}
	return failed, output, nil
}

// runWebSteps runs the web job's steps against dir's web directory with the
// bun on PATH, the way the web job runs them in its own job with the working
// directory set there. It reports the first failing step with its output, or an
// error for a step that could not be run at all — bun missing among them.
func runWebSteps(ctx context.Context, dir string, steps []webStep) (string, string, error) {
	bun, err := lookPath("bun")
	if err != nil {
		return "", "", fmt.Errorf("running %s: %w", steps[0].gate, err)
	}
	for _, s := range steps {
		output, failed, err := runGate(ctx, filepath.Join(dir, webDir), bun, s.args...)
		if err != nil {
			return "", "", fmt.Errorf("running %s: %w", s.gate, err)
		}
		if failed {
			return s.gate, output, nil
		}
	}
	return "", "", nil
}

// runGofmt lists the files gofmt would reformat, which — unlike the other
// three gates — is a content check rather than an exit status: gofmt -l
// exits 0 whether or not it lists anything.
func runGofmt(ctx context.Context, dir string) (failed bool, output string, err error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, gofmtArgs[0], gofmtArgs[1:]...)
	cmd.Dir = dir
	cmd.Env = checkCmdEnv()
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return gofmtListed(out), string(out), nil
	case errors.As(runErr, &exitErr):
		return true, string(out) + stderr.String(), nil
	default:
		return false, "", fmt.Errorf("gofmt: %w", runErr)
	}
}

// gofmtListed reports whether gofmt -l's stdout names a file to reformat.
func gofmtListed(stdout []byte) bool {
	return len(bytes.TrimSpace(stdout)) > 0
}

// runGate runs bin with args against dir, reporting a non-zero exit as a
// gate failure rather than an error running it.
func runGate(ctx context.Context, dir, bin string, args ...string) (output string, failed bool, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = checkCmdEnv()
	out, runErr := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return string(out), false, nil
	case errors.As(runErr, &exitErr):
		return string(out), true, nil
	default:
		return string(out), false, fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), runErr)
	}
}
