package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// agentProbeTimeout bounds the container start-up probe, an image pull included.
var agentProbeTimeout = 5 * time.Minute

// AgentContainer runs each agent CLI in a container whose only mounts are the
// worktree and the harness home, read-write, and the git common directory,
// read-only. The zero value runs agents on the host.
type AgentContainer struct {
	// Image is the agent image, pinned by digest; empty runs agents on the host.
	Image string
	// GitCommonDir is the worktree's git common directory, mounted at its own
	// path so the worktree's .git file resolves; resolve it before an agent runs.
	GitCommonDir string
	// Network is the internal network the container joins; empty uses the default network.
	Network string
	// ProxyURL is the egress proxy on Network, set in the container's environment.
	ProxyURL string
}

// Validate reports a configuration agents cannot use safely: an image not
// pinned by digest, or a git common directory a --mount value cannot carry.
func (c AgentContainer) Validate() error {
	if err := checkPinnedImage("agent", c.Image); err != nil {
		return err
	}
	return checkMountPath("agent git common dir", c.GitCommonDir)
}

// Probe starts the agent image over dir and runs the default harness's binary
// in it, so a missing image, binary or mount fails before the first agent round.
func (c AgentContainer) Probe(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, agentProbeTimeout)
	defer cancel()
	var out bytes.Buffer
	if err := c.Exec(ctx, dir, "", []string{ompName, "--version"}, &out, &out); err != nil {
		return fmt.Errorf("agent container probe: %w: %s", err, truncate(strings.TrimSpace(out.String()), logErrorLimit))
	}
	return nil
}

// Exec runs argv in the agent container over worktree dir and, when home is
// not empty, harness home home, waiting for it to exit.
func (c AgentContainer) Exec(ctx context.Context, dir, home string, argv []string, stdout, stderr io.Writer) error {
	if len(argv) == 0 {
		return errors.New("no command to run in the agent container")
	}
	cmd, err := c.command(ctx, agentSpec{Bin: argv[0], Args: argv[1:], Dir: dir, Home: home})
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// agentSpec is one agent CLI invocation.
type agentSpec struct {
	Bin  string
	Args []string
	// Dir is the worktree the agent runs in.
	Dir string
	// Home is the harness home the agent reads and writes.
	Home string
	// Env is the variables the agent runs under beyond the host's base set.
	Env []string
	// Secret is the model key as NAME=value; in a container it is inherited by
	// name from the docker client's environment, never put on its command line.
	Secret string
}

// agentCommand is the prepared command for s: the agent itself on the host when
// c has no image, else docker running it in a container. Its Cancel stops the
// agent's process group, and the container too.
func agentCommand(ctx context.Context, c AgentContainer, s agentSpec) (*exec.Cmd, error) {
	if c.Image != "" {
		return c.command(ctx, s)
	}
	cmd := exec.CommandContext(ctx, s.Bin, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = slices.Concat(filterEnv(os.Environ(), agentBaseEnvNames), s.Env, []string{s.Secret})
	ownProcessGroup(cmd)
	return cmd, nil
}

func (c AgentContainer) command(ctx context.Context, s agentSpec) (*exec.Cmd, error) {
	dir, err := filepath.Abs(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("agent worktree: %w", err)
	}
	mounts := []struct{ label, path string }{{"agent worktree", dir}, {"agent git common dir", c.GitCommonDir}}
	if s.Home != "" {
		mounts = append(mounts, struct{ label, path string }{"agent home", s.Home})
	}
	for _, m := range mounts {
		if err := checkMountPath(m.label, m.path); err != nil {
			return nil, err
		}
	}
	name, err := containerName("wingman-agent-")
	if err != nil {
		return nil, err
	}
	s.Dir = dir
	secretName, _, _ := strings.Cut(s.Secret, "=")
	cmd := hardenedRunCommand(ctx, c.containerRun(name, secretName, s))
	if s.Secret != "" {
		cmd.Env = append(cmd.Env, s.Secret)
	}
	return cmd, nil
}

// runArgs is the docker run command line for s.
func (c AgentContainer) runArgs(name, secretName string, s agentSpec) []string {
	return c.containerRun(name, secretName, s).args()
}

// containerRun is the hardened container for s, whose binary runs by its base
// name: the image's own PATH finds it.
func (c AgentContainer) containerRun(name, secretName string, s agentSpec) hardenedRun {
	mounts := []string{bindMount(s.Dir, s.Dir, false)}
	if s.Home != "" {
		mounts = append(mounts, bindMount(s.Home, s.Home, false))
	}
	mounts = append(mounts, bindMount(c.GitCommonDir, c.GitCommonDir, true))
	env := append([]string{"TMPDIR=/tmp"}, s.Env...)
	if c.ProxyURL != "" {
		const noProxy = "127.0.0.1,localhost"
		env = append(env, "HTTPS_PROXY="+c.ProxyURL, "HTTP_PROXY="+c.ProxyURL, "NO_PROXY="+noProxy,
			"https_proxy="+c.ProxyURL, "http_proxy="+c.ProxyURL, "no_proxy="+noProxy)
	}
	if secretName != "" {
		env = append(env, secretName)
	}
	return hardenedRun{
		Name: name, Interactive: true, Network: c.Network, Mounts: mounts, Workdir: s.Dir,
		Env: env, Image: c.Image, Argv: append([]string{filepath.Base(s.Bin)}, s.Args...),
	}
}

// dockerCannotRun reports whether a docker run exit code is docker or the
// container failing to start the command (125 to 127) rather than the command
// failing. 137 is deliberately not one: docker kill, which stops a cancelled or
// timed-out run, exits 137 too, as does an out-of-memory kill.
func dockerCannotRun(code int) bool { return code >= 125 && code <= 127 }

// failedToRun reports whether err is docker failing to run an agent in c; it is
// never true on the host.
func (c AgentContainer) failedToRun(err error) bool {
	var exit *exec.ExitError
	return c.Image != "" && errors.As(err, &exit) && dockerCannotRun(exit.ExitCode())
}

// checkMountPath rejects a path a --mount value cannot carry.
func checkMountPath(label, p string) error {
	if !filepath.IsAbs(p) || strings.ContainsRune(p, ',') {
		return fmt.Errorf("%s %q is not an absolute path without a comma", label, p)
	}
	return nil
}

// containerHarness is a Harness that can run its agent in a container.
type containerHarness interface {
	Harness
	withContainer(AgentContainer) Harness
}

// WithAgentContainer returns harnesses running their agents in c, and an error
// for a harness that cannot be contained, which would otherwise run on the host.
func WithAgentContainer(harnesses []Harness, c AgentContainer) ([]Harness, error) {
	out := make([]Harness, len(harnesses))
	for i, h := range harnesses {
		ch, ok := h.(containerHarness)
		if !ok {
			return nil, fmt.Errorf("harness %s cannot run in the agent container", h.Name())
		}
		out[i] = ch.withContainer(c)
	}
	return out, nil
}
