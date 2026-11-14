package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestCheckoutWorktreeCutsTheBranchAtItsHead is AC4: a worktree is cut on a
// branch the repo holds at the head its caller names, and the branch itself is
// left where it was.
func TestCheckoutWorktreeCutsTheBranchAtItsHead(t *testing.T) {
	repo := initRepo(t)
	mustGit(t, repo, "branch", fixBranch)
	head := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	dir := filepath.Join(t.TempDir(), "wt")

	w, err := CheckoutWorktree(context.Background(), repo, dir, fixBranch, head)
	if err != nil {
		t.Fatal(err)
	}
	if w.Branch != fixBranch || w.Base != head {
		t.Fatalf("worktree = %+v, want branch %q cut at %q", w, fixBranch, head)
	}
	if got, err := w.HeadCommit(context.Background()); err != nil || got != head {
		t.Fatalf("worktree head = %q (%v), want %q", got, err, head)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("the worktree holds no checkout: %v", err)
	}
	if got := strings.TrimSpace(mustGit(t, repo, "rev-parse", fixBranch)); got != head {
		t.Fatalf("%s is at %q, want the head it was cut at, %q", fixBranch, got, head)
	}
}

// TestCheckoutWorktreeRefusesWhatItCannotCut is AC4's refusal: a branch the repo
// does not hold, or one at another commit, and a directory git will not add a
// worktree at, are all refused with nothing left registered for the caller.
func TestCheckoutWorktreeRefusesWhatItCannotCut(t *testing.T) {
	headOf := func(t *testing.T, repo string) string {
		t.Helper()
		return strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, repo, dir string) (branch, at string)
		// noGit hides git from the call itself, and made is a directory the
		// setup created, which the refusal must leave as it found it.
		noGit bool
		made  bool
		want  error
		text  string
	}{
		{
			"a branch at another commit",
			func(t *testing.T, repo, _ string) (string, string) {
				at := headOf(t, repo)
				mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com",
					"commit", "--allow-empty", "-qm", "the pull request's own commit")
				return "main", at
			},
			false, false, ErrBranchHeadMismatch, "is at",
		},
		{
			"a branch the repo does not hold",
			func(t *testing.T, repo, _ string) (string, string) { return fixBranch, headOf(t, repo) },
			false, false, ErrBranchHeadMismatch, "holds no branch",
		},
		{
			"no branch named",
			func(t *testing.T, repo, _ string) (string, string) { return "", headOf(t, repo) },
			false, false, nil, "needs the branch and the head",
		},
		{
			"no head named",
			func(t *testing.T, repo, _ string) (string, string) { return fixBranch, "" },
			false, false, nil, "needs the branch and the head",
		},
		{
			"a directory git refuses",
			func(t *testing.T, repo, dir string) (string, string) {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "taken"), []byte("x\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				mustGit(t, repo, "branch", fixBranch)
				return fixBranch, headOf(t, repo)
			},
			false, true, nil, "git worktree: ",
		},
		{
			"no git to run",
			func(t *testing.T, repo, _ string) (string, string) {
				mustGit(t, repo, "branch", fixBranch)
				return fixBranch, headOf(t, repo)
			},
			true, false, nil, "finding git",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initRepo(t)
			dir := filepath.Join(t.TempDir(), "wt")
			branch, at := tt.setup(t, repo, dir)
			if tt.noGit {
				t.Setenv("PATH", t.TempDir())
			}

			_, err := CheckoutWorktree(context.Background(), repo, dir, branch, at)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("err = %v, want it to name %q", err, tt.text)
			}
			if _, err := os.Stat(dir); !tt.made && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the refused worktree exists (%v), want nothing created", err)
			}
		})
	}
}

func TestRunGitReturnsSoonAfterItsContextEnds(t *testing.T) {
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	const deadline = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	start := time.Now()
	_, err = runGit(ctx, bin, t.TempDir(), nil, "-c", "alias.x=!sleep 30", "x")
	if err == nil {
		t.Fatal("a cancelled git reported success")
	}
	if took := time.Since(start); took > deadline+gitWaitDelay+3*time.Second {
		t.Fatalf("runGit returned after %s", took)
	}
}
