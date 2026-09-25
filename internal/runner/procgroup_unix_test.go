//go:build unix

package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeOpencode(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat > /dev/null\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestOpencodeRunLeavesNoChildBehind(t *testing.T) {
	bin, dir := fakeOpencode(t, "sleep 300 &\necho $! > child.pid\n")
	out, err := os.Create(filepath.Join(t.TempDir(), "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()

	if err := (Opencode{Bin: bin, Model: "p/m"}).Run(context.Background(), dir, "x", out, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("the agent's background child outlived Run")
	}
}

func TestOpencodeRunStopsAtTheDeadline(t *testing.T) {
	bin, dir := fakeOpencode(t, "sleep 300\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := (Opencode{Bin: bin, Model: "p/m"}).Run(ctx, dir, "x", io.Discard, io.Discard)
	if err == nil {
		t.Fatal("Run returned nil past its deadline")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("Run took %s after a 300ms deadline", took)
	}
}
