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
	"regexp"
	"slices"
	"strconv"
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
)

// pinnedImage matches an image reference pinned by its sha256 digest.
var pinnedImage = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)

// dockerEnvNames are the variables the docker CLI itself runs under; none
// reaches a container, which is given only the --env values gateArgs names.
var dockerEnvNames = slices.Concat(checkEnvNames, []string{"DOCKER_"})

// ContainerChecks runs RunChecks's gates, in its order and with its commands,
// each in a fresh container with no network whose only mounts are the
// worktree, a read-only module cache, a throwaway build cache, a read-only
// linter binary and the read-only git common directory; its temporary
// directory is the container's own memory.
type ContainerChecks struct {
	// Image is the Go toolchain image, pinned by digest.
	Image string
	// ModCache is the host module cache the runner fills before each round.
	ModCache string
	// Linter is the host golangci-lint binary; it must run in Image.
	Linter string
	// GitCommonDir is the worktree's git common directory, mounted at its own
	// path so the worktree's .git file resolves; resolve it before an agent runs.
	GitCommonDir string
	// GateTimeout bounds one gate; zero means defaultGateTimeout.
	GateTimeout time.Duration
}

// Validate reports a configuration Run cannot use safely: an image not pinned
// by digest, or a path that is not absolute or that a --mount value cannot carry.
func (c ContainerChecks) Validate() error {
	if !pinnedImage.MatchString(c.Image) {
		return fmt.Errorf("checks image %q is not pinned by a sha256 digest", c.Image)
	}
	for name, p := range map[string]string{"module cache": c.ModCache, "linter": c.Linter, "git common dir": c.GitCommonDir} {
		if !filepath.IsAbs(p) || strings.ContainsRune(p, ',') {
			return fmt.Errorf("checks %s %q is not an absolute path without a comma", name, p)
		}
	}
	return nil
}

// Run is a CheckRunner with RunChecks's contract. Before the gates it
// downloads the module's dependencies into ModCache on the host, running no
// repository code; inside, the module proxy is off, so a dependency the
// download did not fetch fails a gate rather than reaching the network. A gate
// that times out is killed and reported as failed; docker failing to start one
// is an error.
func (c ContainerChecks) Run(ctx context.Context, dir string) (string, string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	if err := c.downloadModules(ctx, dir); err != nil {
		return "", "", err
	}
	cache, err := os.MkdirTemp("", "wingman-checks-")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(cache) }()
	for _, sub := range []string{"go-build", "lint", "home"} {
		if err := os.Mkdir(filepath.Join(cache, sub), 0o700); err != nil {
			return "", "", err
		}
	}

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
	return "", "", nil
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

// runGate runs argv in a fresh container against dir, reporting whether it
// exited non-zero or timed out. An exit of 125 to 127 is docker or the
// container failing to start argv, which is an error, not a gate failure.
func (c ContainerChecks) runGate(ctx context.Context, dir, cache, gate string, argv ...string) (stdout, stderr string, failed bool, err error) {
	timeout := c.GateTimeout
	if timeout <= 0 {
		timeout = defaultGateTimeout
	}
	gctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, err := containerName()
	if err != nil {
		return "", "", false, err
	}
	var outBuf, errBuf bytes.Buffer
	cmd := exec.CommandContext(gctx, "docker", c.gateArgs(name, dir, cache, argv)...)
	cmd.Env = filterEnv(os.Environ(), dockerEnvNames)
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	ownProcessGroup(cmd)
	stopGroup := cmd.Cancel
	cmd.Cancel = func() error { return errors.Join(killContainer(ctx, name), stopGroup()) }
	cmd.WaitDelay = containerKillTimeout
	runErr := cmd.Run()

	stdout, stderr = outBuf.String(), errBuf.String()
	var exitErr *exec.ExitError
	switch {
	case gctx.Err() != nil:
		return stdout, stderr + fmt.Sprintf("\n%s stopped after %s: %v\n", gate, timeout, gctx.Err()), true, nil
	case runErr == nil:
		return stdout, stderr, false, nil
	case errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 125 && exitErr.ExitCode() <= 127:
		return "", "", false, fmt.Errorf("docker run %s: exit %d: %s", gate, exitErr.ExitCode(), truncate(stderr, logErrorLimit))
	case errors.As(runErr, &exitErr):
		return stdout, stderr, true, nil
	default:
		return "", "", false, fmt.Errorf("docker run %s: %w", gate, runErr)
	}
}

// gateArgs is the docker run command line for one gate.
func (c ContainerChecks) gateArgs(name, dir, cache string, argv []string) []string {
	args := []string{
		"run", "--rm", "--name", name, "--init",
		"--network", "none",
		"--read-only", "--tmpfs", "/tmp:exec", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--mount", bindMount(dir, containerWorkDir, false),
		"--mount", bindMount(c.ModCache, containerModCache, true),
		"--mount", bindMount(cache, containerCache, false),
		"--mount", bindMount(c.Linter, containerLinter, true),
		"--mount", bindMount(c.GitCommonDir, c.GitCommonDir, true),
		"--workdir", containerWorkDir,
	}
	if uid, gid := os.Getuid(), os.Getgid(); uid >= 0 {
		args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid))
	}
	for _, kv := range []string{
		"GOMODCACHE=" + containerModCache,
		"GOCACHE=" + containerCache + "/go-build",
		"GOLANGCI_LINT_CACHE=" + containerCache + "/lint",
		"TMPDIR=/tmp",
		"HOME=" + containerCache + "/home",
		"GOPROXY=off",
		"GOFLAGS=-mod=readonly -buildvcs=false",
		"GOTOOLCHAIN=local",
	} {
		args = append(args, "--env", kv)
	}
	return append(append(args, c.Image), argv...)
}

// bindMount is a --mount value binding host src at dst.
func bindMount(src, dst string, readOnly bool) string {
	m := "type=bind,src=" + src + ",dst=" + dst
	if readOnly {
		m += ",readonly"
	}
	return m
}

// containerName is a name unique to one gate's container, so a timeout can
// kill it by name.
func containerName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "wingman-checks-" + hex.EncodeToString(b), nil
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
