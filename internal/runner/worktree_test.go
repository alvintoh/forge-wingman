package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newWorktree(t *testing.T) (repo string, w Worktree) {
	t.Helper()
	repo = initRepo(t)
	w, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "wingman/t-1-1-1")
	if err != nil {
		t.Fatal(err)
	}
	return repo, w
}

func TestVerifyDetectsTampering(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(t *testing.T, repo string, w Worktree)
		want   error
	}{
		{"untouched", func(*testing.T, string, Worktree) {}, nil},
		{"shared config gains an fsmonitor", func(t *testing.T, repo string, _ Worktree) {
			appendFile(t, filepath.Join(repo, ".git", "config"), "[core]\n\tfsmonitor = /bin/true\n")
		}, ErrGitTampered},
		{"info/attributes names a filter", func(t *testing.T, repo string, _ Worktree) {
			if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
				t.Fatal(err)
			}
			appendFile(t, filepath.Join(repo, ".git", "info", "attributes"), "* filter=x\n")
		}, ErrGitTampered},
		{"commondir is redirected", func(t *testing.T, _ string, w Worktree) {
			if err := os.WriteFile(filepath.Join(w.GitDir, "commondir"), []byte("/elsewhere\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ErrGitTampered},
		{"HEAD moved to another branch", func(t *testing.T, _ string, w Worktree) {
			mustGit(t, w.Dir, "checkout", "-q", "-b", "other")
		}, ErrHeadMoved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, w := newWorktree(t)
			tt.tamper(t, repo, w)
			if err := w.Verify(context.Background()); !errors.Is(err, tt.want) {
				t.Fatalf("Verify = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCommitIgnoresTheAgentsGitSideChannels(t *testing.T) {
	repo, w := newWorktree(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(repo, ".git", "hooks", "post-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	decoy := initRepo(t)
	if err := os.WriteFile(filepath.Join(w.Dir, ".git"), []byte("gitdir: "+filepath.Join(decoy, ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Dir, "version.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := w.Commit(context.Background(), "t-1: add")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a hook ran during the commit")
	}
	if !strings.Contains(strings.Join(files, ","), "version.go") {
		t.Fatalf("files = %q", files)
	}
	if got := mustGit(t, repo, "log", "--format=%s", "-1", w.Branch); got != "t-1: add\n" {
		t.Fatalf("branch tip in the real repo = %q, the commit went elsewhere", got)
	}
}

func TestCheckSecret(t *testing.T) {
	const secret = "sk-test-0123456789"
	tests := []struct {
		name  string
		agent func(t *testing.T, w Worktree)
		want  error
	}{
		{"clean change", func(t *testing.T, w Worktree) {
			writeFile(t, filepath.Join(w.Dir, "a.go"), "package a\n")
		}, nil},
		{"secret in a file", func(t *testing.T, w Worktree) {
			writeFile(t, filepath.Join(w.Dir, "a.go"), "const k = \""+secret+"\"\n")
		}, ErrSecretInBranch},
		{"secret committed then deleted", func(t *testing.T, w Worktree) {
			writeFile(t, filepath.Join(w.Dir, "leak.txt"), secret)
			mustGit(t, w.Dir, "add", "leak.txt")
			mustGit(t, w.Dir, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-qm", "wip")
			if err := os.Remove(filepath.Join(w.Dir, "leak.txt")); err != nil {
				t.Fatal(err)
			}
		}, ErrSecretInBranch},
		{"secret in a commit message", func(t *testing.T, w Worktree) {
			writeFile(t, filepath.Join(w.Dir, "a.go"), "package a\n")
			mustGit(t, w.Dir, "add", "a.go")
			mustGit(t, w.Dir, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-qm", "key "+secret)
		}, ErrSecretInBranch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, w := newWorktree(t)
			tt.agent(t, w)
			if _, err := w.Commit(context.Background(), "t-1: add"); err != nil {
				t.Fatal(err)
			}
			if err := w.CheckSecret(context.Background(), secret); !errors.Is(err, tt.want) {
				t.Fatalf("CheckSecret = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestGitErrorKeepsStderrOutOfItsMessage(t *testing.T) {
	_, w := newWorktree(t)
	_, err := w.git(context.Background(), "add", "--", "no-such-file-named-by-the-agent")
	var g *GitError
	if !errors.As(err, &g) {
		t.Fatalf("err = %v, want a GitError", err)
	}
	if strings.Contains(err.Error(), "no-such-file") || !strings.Contains(g.Stderr, "no-such-file") {
		t.Fatalf("message %q / stderr %q", err.Error(), g.Stderr)
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}
