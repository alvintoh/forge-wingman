package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestSetupFailedReportsAValidSummary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "out")
	err := setupFailed(logger, path, errors.New("flag provided but not defined: -x"))
	if exitCode(err) != 0 {
		t.Fatalf("exit %d for a reported setup failure", exitCode(err))
	}
	out, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	raw, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "summary=")
	if !ok {
		t.Fatalf("GITHUB_OUTPUT = %q", out)
	}
	sum, perr := runner.ParseSummary(raw, "42-1", runner.Ticket{}, time.Now())
	if perr != nil || sum.StopReason != runner.StopSetup || sum.StopDetail == "" {
		t.Fatalf("summary %+v, err %v", sum, perr)
	}

	if exitCode(setupFailed(logger, "", errors.New("x"))) != 1 {
		t.Fatal("exit 0 with nowhere to report")
	}
}

func TestBuildStopsAsTicketNotDeliveredWithoutATicketFile(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	output := filepath.Join(dir, "out")
	pinnedImagesOnPath(t)
	err := build(context.Background(), logger, env{output: output, attemptID: "42-1", tempDir: dir},
		append(pinnedImageArgs(), "-model", "command-code/p/m", "-ticket-file", filepath.Join(dir, "absent.json")))
	if exitCode(err) != 0 {
		t.Fatalf("exit %d, err %v; want a reported stop", exitCode(err), err)
	}
	if sum := reportedSummary(t, output); sum.StopReason != runner.StopTicketNotDelivered {
		t.Fatalf("summary %+v; want ticket-not-delivered", sum)
	}
}

func TestBuildAndPlanStageRefuseAMissingImageBeforeAnyAgentRuns(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	digest := "@sha256:" + strings.Repeat("a", 64)
	agent, checks := []string{"-agent-image", "agent:1" + digest}, []string{"-checks-image", "golang:1.27.1" + digest}
	for _, tt := range []struct {
		name, stage string
		args        []string
		missing     string
	}{
		{"build with no agent image", "build", checks, "-agent-image"},
		{"build with no checks image", "build", agent, "-checks-image"},
		{"build with neither image", "build", nil, "-agent-image, -checks-image"},
		{"plan-stage with no agent image", "plan-stage", nil, "-agent-image"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dockerLog := fakeDockerLog(t)
			dir := t.TempDir()
			output := filepath.Join(dir, "out")
			args := append(slices.Clone(tt.args), "-ticket-file", filepath.Join(dir, "absent.json"))
			var err error
			if tt.stage == "build" {
				err = build(context.Background(), logger, env{output: output, attemptID: "42-1"}, args)
			} else {
				err = planStage(context.Background(), logger, env{output: output, attemptID: "42-1"},
					append(args, "-out", filepath.Join(dir, "plan.json")))
			}
			if err == nil || !strings.Contains(err.Error(), tt.missing+" required") {
				t.Fatalf("err = %v, want %s refused", err, tt.missing)
			}
			if tt.stage == "build" {
				if exitCode(err) != 0 {
					t.Fatalf("exit %d; want a reported stop", exitCode(err))
				}
				if sum := reportedSummary(t, output); sum.StopReason != runner.StopSetup || !strings.Contains(sum.StopDetail, tt.missing) {
					t.Fatalf("summary %+v, want a setup failure naming %s", sum, tt.missing)
				}
			} else if exitCode(err) != 1 {
				t.Fatalf("exit %d; want the plan stage to fail", exitCode(err))
			}
			if _, statErr := os.Stat(dockerLog); statErr == nil {
				t.Fatal("docker was called before the missing image was refused")
			}
		})
	}
}

func TestTicketNotDeliveredReportsAStoppedSummary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "out")
	err := ticketNotDelivered(logger, path, errors.New("no ticket file"))
	if exitCode(err) != 0 {
		t.Fatalf("exit %d for a reported stop", exitCode(err))
	}
	out, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	raw, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "summary=")
	if !ok {
		t.Fatalf("GITHUB_OUTPUT = %q", out)
	}
	sum, perr := runner.ParseSummary(raw, "42-1", runner.Ticket{}, time.Now())
	if perr != nil || sum.Outcome != runner.OutcomeStopped || sum.StopReason != runner.StopTicketNotDelivered {
		t.Fatalf("summary %+v, err %v", sum, perr)
	}

	if exitCode(ticketNotDelivered(logger, "", errors.New("x"))) != 1 {
		t.Fatal("exit 0 with nowhere to report")
	}
}

func TestCheckRunnersRefuseAnImageNotPinnedByDigest(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"golangci-lint", "bun"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	if _, _, err := checkRunners(ctx, "golang:1.27.1", t.TempDir(), "."); err == nil {
		t.Fatal("checkRunners accepted an image pinned by tag alone")
	}
	if _, _, err := checkRunners(ctx, "golang:1.27.1@sha256:"+strings.Repeat("a", 64), t.TempDir(), "."); err != nil {
		t.Fatal(err)
	}
}

// TestCheckRunnersRequireTheBunItsWebGatesMount holds checkRunners to failing
// before any agent runs when the interpreter its web gates mount is missing, as
// it does for the linter.
func TestCheckRunnersRequireTheBunItsWebGatesMount(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "golangci-lint"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// git stays on PATH, which checkRunners resolves the repository through, and
	// the directories after it are ones a runner's bun is not installed in.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	image := "golang:1.27.1@sha256:" + strings.Repeat("a", 64)
	if _, _, err := checkRunners(context.Background(), image, t.TempDir(), "."); err == nil || !strings.Contains(err.Error(), "checks bun") {
		t.Fatalf("checkRunners = %v, want an error naming the bun its web gates mount", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "bun"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := checkRunners(context.Background(), image, t.TempDir(), "."); err != nil {
		t.Fatalf("checkRunners = %v, want it usable with a bun on PATH", err)
	}
}

func TestCheckRunnersRunOnTheHostWithNoImage(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	checks, _, err := checkRunners(context.Background(), "", t.TempDir(), t.TempDir())
	if err != nil || checks == nil {
		t.Fatalf("checkRunners with no image = %v, %v; want the host runner and no lookups", checks != nil, err)
	}
}
