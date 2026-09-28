package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// checkGofmt, checkVet, checkLint and checkTest name RunChecks's four gates,
// in the order Makefile's check target runs them; gates in record.go is built
// from these.
const (
	checkGofmt = "gofmt"
	checkVet   = "vet"
	checkLint  = "golangci-lint"
	checkTest  = "test"
)

// checkEnvNames are the variables a check gate's subprocess inherits: the Go
// toolchain's and golangci-lint's own needs, deliberately not
// OPENCODE_API_KEY or the workload-identity credentials model.yml's auth
// step exports. An allow-list, not an exclude-list, so a secret added to
// that job later isn't carried in by default. TEMP/TMP/LocalAppData are
// Windows' equivalents of TMPDIR/GOCACHE's own fallback path, needed for
// `make check` to run locally on Windows (see the repo's other Windows fixes).
var checkEnvNames = []string{
	"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "LOCALAPPDATA", "LANG",
	"GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE", "GOTOOLCHAIN", "GOFLAGS",
	"LC_", "XDG_",
}

// checkCmdEnv is the environment every RunChecks subprocess runs under. This
// is a partial mitigation, not full isolation: a credentials file already on
// disk from an earlier auth step could still be read by its path; full
// isolation is tracked in FRG-30.
func checkCmdEnv() []string {
	return filterEnv(os.Environ(), checkEnvNames)
}

// CheckRunner runs the target repository's own quality gates against dir —
// RunChecks in production, a fake in a test that exercises the pre-PR loop's
// wiring without shelling out to a real Go toolchain.
type CheckRunner func(ctx context.Context, dir string) (failedGate, output string, err error)

// RunChecks runs the repository's own quality gates against dir, in the same
// order and with the same commands as Makefile's check target, stopping at
// the first failure. It returns ("", "", nil) when every gate passes;
// otherwise the failing gate's name — one of gates in record.go — and its
// output. An error reports a gate that could not be run at all, never a gate
// that ran and failed.
//
// This in-job pass and run.yml's separate check job both run these same four
// gates: deliberately, not redundantly — this one gives the pre-PR loop
// fast, cheap feedback to rebuild against, while the separate job (no
// secrets, its own runner) stays the trusted, final gate before merge.
func RunChecks(ctx context.Context, dir string) (string, string, error) {
	if failed, output, err := runGofmt(ctx, dir); err != nil {
		return "", "", err
	} else if failed {
		return checkGofmt, output, nil
	}
	for _, g := range []struct {
		gate string
		bin  string
		args []string
	}{
		{checkVet, "go", []string{"vet", "./..."}},
		{checkLint, "golangci-lint", []string{"run"}},
		{checkTest, "go", []string{"test", "-race", "-shuffle=on", "-cover", "./..."}},
	} {
		output, failed, err := runGate(ctx, dir, g.bin, g.args...)
		if err != nil {
			return "", "", fmt.Errorf("running %s: %w", g.gate, err)
		}
		if failed {
			return g.gate, output, nil
		}
	}
	return "", "", nil
}

// runGofmt lists the files gofmt would reformat, which — unlike the other
// three gates — is a content check rather than an exit status: gofmt -l
// exits 0 whether or not it lists anything.
func runGofmt(ctx context.Context, dir string) (failed bool, output string, err error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gofmt", "-l", ".")
	cmd.Dir = dir
	cmd.Env = checkCmdEnv()
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return len(bytes.TrimSpace(out)) > 0, string(out), nil
	case errors.As(runErr, &exitErr):
		return true, string(out) + stderr.String(), nil
	default:
		return false, "", fmt.Errorf("gofmt: %w", runErr)
	}
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
