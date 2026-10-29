package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// defaultGateTimeout bounds one containerised gate when ContainerChecks sets
// none: above go test's own 10-minute default, so a hung test fails there first.
const defaultGateTimeout = 15 * time.Minute

// containerKillTimeout bounds the docker kill that stops a timed-out gate.
const containerKillTimeout = 30 * time.Second

// Paths inside the checks container.
const (
	containerWorkDir  = "/work"
	containerModCache = "/gomodcache"
	containerCache    = "/cache"
	containerLinter   = "/usr/local/bin/golangci-lint"
	containerBun      = "/usr/local/bin/bun"
	containerBunCache = "/cache/bun"
	containerWebDir   = containerWorkDir + "/" + webDir
)

// checksContainerPrefix begins every gate container's name, so a timed-out gate
// can be killed by it.
const checksContainerPrefix = "wingman-checks-"

// dockerEnvNames are the variables the docker CLI itself runs under; none
// reaches a container, which is given only the --env values its run names.
var dockerEnvNames = slices.Concat(checkEnvNames, []string{"DOCKER_"})

// ContainerChecks runs RunChecks's gates, in its order and with its commands,
// each in a fresh container with no network whose only mounts are the
// worktree, a read-only module cache, a throwaway build cache, a read-only
// linter binary and the read-only git common directory; its temporary
// directory is the container's own memory. When the run's diff touches webDir
// it runs the web job's steps too, in containers of the same image that hold
// the worktree, the host's bun and a bun cache warmed beforehand.
type ContainerChecks struct {
	// Image is the Go toolchain image, pinned by digest.
	Image string
	// ModCache is the host module cache the runner fills before each round.
	ModCache string
	// Linter is the host golangci-lint binary; it must run in Image.
	Linter string
	// Bun is the host bun binary the web gates run, and it must run in Image
	// too. The image cannot carry bun: it is the image go.mod's toolchain pins,
	// named by the workflow this repository's App may not push to, so the web
	// steps' interpreter is mounted as the linter is. bun needs nothing beyond
	// glibc to run.
	Bun string
	// BunCache is the host bun cache the runner fills before each round, which
	// the web gates' install resolves from: their containers have no network.
	BunCache string
	// GitCommonDir is the worktree's git common directory, mounted at its own
	// path so the worktree's .git file resolves; resolve it before an agent runs.
	GitCommonDir string
	// GateTimeout bounds one gate; zero means defaultGateTimeout.
	GateTimeout time.Duration
}

// Validate reports a configuration Run cannot use safely: an image not pinned
// by digest, or a path that is not absolute or that a --mount value cannot carry.
func (c ContainerChecks) Validate() error {
	if err := checkPinnedImage("checks", c.Image); err != nil {
		return err
	}
	paths := map[string]string{
		"module cache": c.ModCache, "linter": c.Linter, "bun": c.Bun,
		"bun cache": c.BunCache, "git common dir": c.GitCommonDir,
	}
	for name, p := range paths {
		if err := checkMountPath("checks "+name, p); err != nil {
			return err
		}
	}
	return nil
}

// Run is a CheckRunner with RunChecks's contract. Before the gates it
// downloads the module's dependencies into ModCache on the host, and — when
// changed touches webDir — web's dependencies into BunCache, running no
// repository code either way; inside, the module proxy is off and the web
// install resolves from that cache, so a dependency neither download fetched
// fails a gate rather than reaching the network. A gate that times out is
// killed and reported as failed; docker failing to start one is an error.
func (c ContainerChecks) Run(ctx context.Context, dir string, changed []string) (string, string, error) {
	dir, err := c.gateDir(ctx, dir)
	if err != nil {
		return "", "", err
	}
	web := touchesWeb(changed)
	if web {
		if err := c.downloadWebModules(ctx, dir); err != nil {
			return "", "", err
		}
	}
	cache, cleanup, err := gateCache()
	if err != nil {
		return "", "", err
	}
	defer cleanup()

	stdout, stderr, failed, err := c.runGate(ctx, dir, cache, checkGofmt, gofmtArgs...)
	if err != nil {
		return "", "", err
	}
	if failed || gofmtListed([]byte(stdout)) {
		return checkGofmt, stdout + stderr, nil
	}
	for _, g := range exitGates {
		stdout, stderr, failed, err := c.runGate(ctx, dir, cache, g.gate, append([]string{g.bin}, g.args...)...)
		if err != nil {
			return "", "", err
		}
		if failed {
			return g.gate, stdout + stderr, nil
		}
	}
	if !web {
		return "", "", nil
	}
	for _, s := range webSteps(true) {
		stdout, stderr, failed, err := c.runWebGate(ctx, dir, s.gate, append([]string{containerBun}, s.args...)...)
		if err != nil {
			return "", "", err
		}
		if failed {
			return s.gate, stdout + stderr, nil
		}
	}
	return "", "", nil
}

// RunPackageTest runs one package's tests in a fresh container of the checks
// image: Run's counterpart for the single-package probe the pre-PR loop's
// guard scan makes. It is the same image, the same mounts and no network, so
// the branch's tests run where the gates run them and not on the host; the
// module cache is the one Run fills, and a container of its own means a probe
// never answers from, or leaves anything in, another gate's build.
//
// Its caller bounds it: the probe's own context deadline is what stops one, so
// a package whose tests do not finish never spends the run's whole budget.
func (c ContainerChecks) RunPackageTest(ctx context.Context, dir, pkg string) (bool, string, error) {
	dir, err := c.gateDir(ctx, dir)
	if err != nil {
		return false, "", err
	}
	cache, cleanup, err := gateCache()
	if err != nil {
		return false, "", err
	}
	defer cleanup()

	stdout, stderr, failed, err := c.runGate(ctx, dir, cache, pkg, "go", "test", pkg)
	if err != nil {
		return false, "", err
	}
	return failed, stdout + stderr, nil
}

// Format is a Formatter that runs the gate's fixing command in a fresh
// container of the checks image with no network: gofmt -w for checkGofmt, the
// host's bun running web's fmt script for checkWebFmtCheck. As RunFormat does,
// it reports only a formatter that could not be run, not one that exited
// non-zero.
func (c ContainerChecks) Format(ctx context.Context, dir, gate string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("formatting %s: %w", gate, err)
	}
	switch gate {
	case checkGofmt:
		cache, cleanup, cerr := gateCache()
		if cerr != nil {
			return fmt.Errorf("formatting %s: %w", gate, cerr)
		}
		defer cleanup()
		_, _, _, err = c.runGate(ctx, dir, cache, gate, gofmtFixArgs...)
	case checkWebFmtCheck:
		_, _, _, err = c.runContainer(ctx, gate, func(name string) hardenedRun {
			return c.webFormatRun(name, dir, append([]string{containerBun}, webFmtFixArgs...))
		})
	default:
		return fmt.Errorf("formatting %s: no formatter for this gate", gate)
	}
	if err != nil {
		return fmt.Errorf("formatting %s: %w", gate, err)
	}
	return nil
}

// gateDir prepares dir for one gate run: absolute, with the module cache
// filled. A download that runs and fails is not an error there — the gate then
// fails on the same missing module, with output the next round can act on.
func (c ContainerChecks) gateDir(ctx context.Context, dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := c.downloadModules(ctx, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// gateCache makes the throwaway cache one gate run writes to — its compiled
// packages, its lint cache and its home — and returns it with the function
// that removes it.
func gateCache() (string, func(), error) {
	cache, err := os.MkdirTemp("", "wingman-checks-")
	if err != nil {
		return "", nil, err
	}
	for _, sub := range []string{"go-build", "lint", "home"} {
		if err := os.Mkdir(filepath.Join(cache, sub), 0o700); err != nil {
			_ = os.RemoveAll(cache)
			return "", nil, err
		}
	}
	return cache, func() { _ = os.RemoveAll(cache) }, nil
}

// gateTimeout is the bound on one gate, and on the web dependency download.
func (c ContainerChecks) gateTimeout() time.Duration {
	if c.GateTimeout <= 0 {
		return defaultGateTimeout
	}
	return c.GateTimeout
}

// downloadModules fills ModCache with dir's dependencies. A download that runs
// and fails is not an error: the gates then fail on the same missing module,
// with output the next round can act on.
func (c ContainerChecks) downloadModules(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "go", "mod", "download")
	cmd.Dir = dir
	cmd.Env = append(checkCmdEnv(), "GOMODCACHE="+c.ModCache)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return fmt.Errorf("go mod download: %w", err)
	}
	return nil
}

// downloadWebModules fills BunCache with web's dependencies before the web
// gates, installing where the web job's install does but with --ignore-scripts:
// an install runs the project's lifecycle scripts, which are the branch's code
// and would otherwise run on the host rather than in the container every gate
// runs in. A download that runs and fails is not an error: the web-install gate
// then fails on the same problem, with output the next round can act on. The
// host install has network, so it is bounded like a gate; a hung one is an
// error, since killing it leaves nothing for a gate to fail on.
func (c ContainerChecks) downloadWebModules(ctx context.Context, dir string) error {
	if err := os.MkdirAll(c.BunCache, 0o700); err != nil {
		return err
	}
	timeout := c.gateTimeout()
	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ictx, c.Bun, "install", "--frozen-lockfile", "--ignore-scripts", "--cache-dir", c.BunCache)
	cmd.Dir = filepath.Join(dir, webDir)
	cmd.Env = checkCmdEnv()
	var exitErr *exec.ExitError
	err := cmd.Run()
	switch {
	case ictx.Err() != nil && ctx.Err() == nil:
		return fmt.Errorf("bun install: stopped after %s: %w", timeout, ictx.Err())
	case err != nil && !errors.As(err, &exitErr):
		return fmt.Errorf("bun install: %w", err)
	}
	return nil
}

// runGate runs argv in a fresh container against dir, reporting whether it
// exited non-zero or timed out. An exit of 125 to 127 is docker or the
// container failing to start argv, which is an error, not a gate failure.
func (c ContainerChecks) runGate(ctx context.Context, dir, cache, gate string, argv ...string) (stdout, stderr string, failed bool, err error) {
	return c.runContainer(ctx, gate, func(name string) hardenedRun {
		return c.gateRun(name, dir, cache, argv)
	})
}

// runWebGate runs one of the web job's steps in a fresh container against dir,
// reporting as runGate does.
func (c ContainerChecks) runWebGate(ctx context.Context, dir, gate string, argv ...string) (stdout, stderr string, failed bool, err error) {
	return c.runContainer(ctx, gate, func(name string) hardenedRun {
		return c.webGateRun(name, dir, argv)
	})
}

// runContainer runs one hardened gate container to completion, reporting a
// non-zero exit or a timeout as a failure of gate and docker or the container
// failing to start as an error.
func (c ContainerChecks) runContainer(ctx context.Context, gate string, build func(name string) hardenedRun) (stdout, stderr string, failed bool, err error) {
	timeout := c.gateTimeout()
	gctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, err := containerName(checksContainerPrefix)
	if err != nil {
		return "", "", false, err
	}
	var outBuf, errBuf bytes.Buffer
	cmd := hardenedRunCommand(gctx, build(name))
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	runErr := cmd.Run()

	stdout, stderr = outBuf.String(), errBuf.String()
	var exitErr *exec.ExitError
	switch {
	case gctx.Err() != nil:
		return stdout, stderr + fmt.Sprintf("\n%s stopped after %s: %v\n", gate, timeout, gctx.Err()), true, nil
	case runErr == nil:
		return stdout, stderr, false, nil
	case errors.As(runErr, &exitErr) && dockerCannotRun(exitErr.ExitCode()):
		return "", "", false, fmt.Errorf("docker run %s: exit %d: %s", gate, exitErr.ExitCode(), truncate(stderr, logErrorLimit))
	case errors.As(runErr, &exitErr):
		return stdout, stderr, true, nil
	default:
		return "", "", false, fmt.Errorf("docker run %s: %w", gate, runErr)
	}
}

// gateRun is the hardened container for one Go gate.
func (c ContainerChecks) gateRun(name, dir, cache string, argv []string) hardenedRun {
	return hardenedRun{
		Name:    name,
		Network: "none",
		Mounts: []string{
			bindMount(dir, containerWorkDir, false),
			bindMount(c.ModCache, containerModCache, true),
			bindMount(cache, containerCache, false),
			bindMount(c.Linter, containerLinter, true),
			bindMount(c.GitCommonDir, c.GitCommonDir, true),
		},
		Workdir: containerWorkDir,
		Env: []string{
			"GOMODCACHE=" + containerModCache,
			"GOCACHE=" + containerCache + "/go-build",
			"GOLANGCI_LINT_CACHE=" + containerCache + "/lint",
			"TMPDIR=/tmp",
			"HOME=" + containerCache + "/home",
			"GOPROXY=off",
			"GOFLAGS=-mod=readonly -buildvcs=false",
			"GOTOOLCHAIN=local",
		},
		Image: c.Image,
		Argv:  argv,
	}
}

// webGateRun is the hardened container for one of the web job's steps: the
// worktree, the host's bun mounted as the linter is, and the cache its install
// resolves from, with webDir as the working directory. It mounts no Go cache
// and no linter — a web step runs none of the repository's Go code.
func (c ContainerChecks) webGateRun(name, dir string, argv []string) hardenedRun {
	return hardenedRun{
		Name:    name,
		Network: "none",
		Mounts: []string{
			bindMount(dir, containerWorkDir, false),
			bindMount(c.Bun, containerBun, true),
			bindMount(c.BunCache, containerBunCache, false),
		},
		Workdir: containerWebDir,
		Env: []string{
			"TMPDIR=/tmp",
			"HOME=/tmp",
			"BUN_INSTALL_CACHE_DIR=" + containerBunCache,
		},
		Image: c.Image,
		Argv:  argv,
	}
}

// webFormatRun is webGateRun without the bun cache, which the fmt script never
// reads: the worktree and the host's bun alone.
func (c ContainerChecks) webFormatRun(name, dir string, argv []string) hardenedRun {
	return hardenedRun{
		Name:    name,
		Network: "none",
		Mounts: []string{
			bindMount(dir, containerWorkDir, false),
			bindMount(c.Bun, containerBun, true),
		},
		Workdir: containerWebDir,
		Env:     []string{"TMPDIR=/tmp", "HOME=/tmp"},
		Image:   c.Image,
		Argv:    argv,
	}
}

// bindMount is a --mount value binding host src at dst.
func bindMount(src, dst string, readOnly bool) string {
	m := "type=bind,src=" + src + ",dst=" + dst
	if readOnly {
		m += ",readonly"
	}
	return m
}

// containerName is a name unique to one container, beginning prefix, so a
// timeout can kill it by name.
func containerName(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// killContainer stops a gate's container, on a context of its own since the
// gate's has already ended.
func killContainer(ctx context.Context, name string) error {
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerKillTimeout)
	defer cancel()
	cmd := exec.CommandContext(kctx, "docker", "kill", name)
	cmd.Env = filterEnv(os.Environ(), dockerEnvNames)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker kill %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
