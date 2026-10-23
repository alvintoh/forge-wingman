package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

type recordReader map[string]runner.Record

func (r recordReader) GetRecord(_ context.Context, id string) (runner.Record, error) {
	rec, ok := r[id]
	if !ok {
		return runner.Record{}, runner.ErrRecordNotFound
	}
	return rec, nil
}

// reportedSummary is the summary a stage wrote to its $GITHUB_OUTPUT file at output.
func reportedSummary(t *testing.T, output string) runner.Summary {
	t.Helper()
	out, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "summary="); ok {
			raw = v
		}
	}
	sum, err := runner.ParseSummary(raw, "42-1", runner.Ticket{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// pinnedImageArgs are the image flags of a stage that has both images.
func pinnedImageArgs() []string {
	digest := "@sha256:" + strings.Repeat("a", 64)
	return []string{"-agent-image", "agent:1" + digest, "-checks-image", "golang:1.27.1" + digest}
}

// pinnedImagesOnPath puts the linter the checks container mounts, the bun its
// web gates mount, and a docker that logs on PATH, and returns the docker log.
func pinnedImagesOnPath(t *testing.T) string {
	t.Helper()
	logFile := fakeDockerLog(t)
	bin := t.TempDir()
	for _, name := range []string{"golangci-lint", "bun"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

// fakeDockerLog puts a docker on PATH that succeeds and logs each call's arguments on one line.
func fakeDockerLog(t *testing.T) string {
	t.Helper()
	bin, logFile := t.TempDir(), filepath.Join(t.TempDir(), "docker.log")
	script := "#!/bin/sh\necho \"$*\" >> \"" + logFile + "\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

// dockerLines is the calls fakeDockerLog has logged so far.
func dockerLines(t *testing.T, logFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}
