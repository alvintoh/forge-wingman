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

// TestAddWorktreeReusesItsFixedBranchOnARetry is the regression for a fixed
// local branch failing a second run in the same clone: the branch already
// exists and its previous worktree is still registered, so AddWorktree must
// prune the stale registration and reset the branch rather than refuse.
func TestAddWorktreeReusesItsFixedBranchOnARetry(t *testing.T) {
	repo := initRepo(t)
	dir := filepath.Join(t.TempDir(), "wt")
	if _, err := AddWorktree(context.Background(), repo, dir, "wingman/wt"); err != nil {
		t.Fatal(err)
	}
	// The first run's worktree is gone, but git still registers it.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "wingman/wt"); err != nil {
		t.Fatalf("a retry in the same clone failed: %v", err)
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

func TestDirty(t *testing.T) {
	_, w := newWorktree(t)
	dirty, err := w.Dirty(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("a fresh worktree reports dirty")
	}
	writeFile(t, filepath.Join(w.Dir, "a.go"), "package a\n")
	dirty, err = w.Dirty(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("an untracked file was not reported as dirty")
	}
}

// TestDiffPendingSeesUncommittedEdits is the regression for the pre-PR
// loop's review pass having always seen an empty diff: it ran before
// PhaseCommit, when nothing had been committed yet, so Diff's comparison
// against HEAD never had anything to show. DiffPending must see the edit
// while it is still only in the working tree.
func TestDiffPendingSeesUncommittedEdits(t *testing.T) {
	_, w := newWorktree(t)
	writeFile(t, filepath.Join(w.Dir, "a.go"), "package a\n")
	diff, err := w.DiffPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "a.go") || !strings.Contains(diff, "package a") {
		t.Fatalf("diff = %q, want it to carry the uncommitted edit", diff)
	}
}

// TestDiffPendingSeesAModifiedTrackedFile covers the other half: an edit to
// a file the branch already committed, still uncommitted itself.
func TestDiffPendingSeesAModifiedTrackedFile(t *testing.T) {
	_, w := newWorktree(t)
	writeFile(t, filepath.Join(w.Dir, "a.go"), "package a\n")
	if _, err := w.Commit(context.Background(), "t-1: add"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(w.Dir, "a.go"), "package a\n\nvar x = 1\n")
	diff, err := w.DiffPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "a.go") || !strings.Contains(diff, "var x = 1") {
		t.Fatalf("diff = %q, want it to carry the uncommitted modification", diff)
	}
}

// TestBundleCarriesThePushedBranchNotTheWorktreesOwn pins Bundle naming the
// branch a run pushes — so run.yml can fetch it — even though the worktree
// itself sits on the fixed localBranch.
func TestBundleCarriesThePushedBranchNotTheWorktreesOwn(t *testing.T) {
	_, w := newWorktree(t)
	writeFile(t, filepath.Join(w.Dir, "a.go"), "package a\n")
	if _, err := w.Commit(context.Background(), "t-1: add"); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "out.bundle")
	if err := w.Bundle(context.Background(), bundle, "wingman/pushed-9-9-9"); err != nil {
		t.Fatal(err)
	}
	heads, err := w.git(context.Background(), "bundle", "list-heads", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(heads), "refs/heads/wingman/pushed-9-9-9") {
		t.Fatalf("bundle carries %q, want the pushed branch", heads)
	}
	if strings.Contains(string(heads), "refs/heads/"+w.Branch) {
		t.Fatalf("bundle carries %q, the worktree's own fixed branch", heads)
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
