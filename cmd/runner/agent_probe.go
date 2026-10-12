package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// agentProbe runs the command after "--" in the agent container, over a scratch
// worktree and home, and fails when the command does: the agent-probe
// workflow's way of trying what the container must not allow.
func agentProbe(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("agent-probe", flag.ContinueOnError)
	image := fs.String("agent-image", "", "agent image, pinned by digest, to run the command in")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *image == "" || fs.NArg() == 0 {
		return errors.New("usage: runner agent-probe -agent-image <ref> -- <command...>")
	}
	gitDir, err := runner.GitCommonDir(ctx, ".")
	if err != nil {
		return fmt.Errorf("agent git common dir: %w", err)
	}
	c := runner.AgentContainer{Image: *image, GitCommonDir: gitDir}
	if err := c.Validate(); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "wingman-agent-probe-")
	if err != nil {
		return fmt.Errorf("agent probe scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	dir, home := filepath.Join(scratch, "wt"), filepath.Join(scratch, "home")
	for _, d := range []string{dir, home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return fmt.Errorf("agent probe scratch: %w", err)
		}
	}
	return c.Exec(ctx, dir, home, fs.Args(), stdout, stderr)
}
