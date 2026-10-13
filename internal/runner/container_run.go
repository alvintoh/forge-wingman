package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
)

// pinnedImage matches an image reference pinned by its sha256 digest.
var pinnedImage = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)

// hardeningFlags are the docker run flags every runner container gets.
var hardeningFlags = []string{"--read-only", "--tmpfs", "/tmp:exec", "--cap-drop", "ALL", "--security-opt", "no-new-privileges"}

// hardenedRun is one hardened container: what differs per container, with the
// shared flags and the host user added by args.
type hardenedRun struct {
	Name        string
	Interactive bool
	// Network is the --network value; empty adds no flag.
	Network string
	// Mounts are --mount values.
	Mounts  []string
	Workdir string
	// Env are --env values, NAME=value or a bare NAME inherited from docker's environment.
	Env   []string
	Image string
	Argv  []string
}

// args is the docker run command line for r.
func (r hardenedRun) args() []string {
	args := []string{"run", "--rm", "--name", r.Name, "--init"}
	if r.Interactive {
		args = append(args, "--interactive")
	}
	args = append(args, hardeningFlags...)
	if r.Network != "" {
		args = append(args, "--network", r.Network)
	}
	for _, m := range r.Mounts {
		args = append(args, "--mount", m)
	}
	args = append(args, "--workdir", r.Workdir)
	if uid, gid := os.Getuid(), os.Getgid(); uid >= 0 {
		args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid))
	}
	for _, kv := range r.Env {
		args = append(args, "--env", kv)
	}
	return append(append(args, r.Image), r.Argv...)
}

// hardenedRunCommand is the docker command running r, whose cancel kills the
// container as well as the docker client's process group.
func hardenedRunCommand(ctx context.Context, r hardenedRun) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", r.args()...)
	cmd.Env = filterEnv(os.Environ(), dockerEnvNames)
	ownProcessGroup(cmd)
	stopGroup := cmd.Cancel
	cmd.Cancel = func() error { return errors.Join(killContainer(ctx, r.Name), stopGroup()) }
	cmd.WaitDelay = containerKillTimeout
	return cmd
}

// checkPinnedImage rejects an image not pinned by a sha256 digest.
func checkPinnedImage(label, image string) error {
	if !pinnedImage.MatchString(image) {
		return fmt.Errorf("%s image %q is not pinned by a sha256 digest", label, image)
	}
	return nil
}
