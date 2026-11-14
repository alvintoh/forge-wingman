package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pushBranch = "wingman/abc-12-100-1"

// pushFixture is a pull request's branch on a bare origin and a checkout of it,
// with a round's commit not yet made.
type pushFixture struct {
	origin, work string
	// first is the commit before the expected head, expected the head a round is
	// made on.
	first, expected string
}

func newPushFixture(t *testing.T) pushFixture {
	t.Helper()
	root := t.TempDir()
	f := pushFixture{origin: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work")}
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", f.origin)
	mustGit(t, root, "init", "-q", "-b", pushBranch, f.work)
	writeRepoFile(t, f.work, "README.md", "x\n")
	commitAs(t, f.work, "the owner", "owner@example.com", "init")
	f.first = rev(t, f.work, "HEAD")
	writeRepoFile(t, f.work, ".github/workflows/ci.yml", "name: ci\n")
	writeRepoFile(t, f.work, "a.go", "package a\n")
	commitAs(t, f.work, "the owner", "owner@example.com", "owner work")
	f.expected = rev(t, f.work, "HEAD")
	mustGit(t, f.work, "remote", "add", "origin", f.origin)
	mustGit(t, f.work, "push", "-q", "origin", pushBranch)
	return f
}

func rev(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(mustGit(t, dir, "rev-parse", ref))
}

// commitAs stages everything and commits it with the author name and email.
func commitAs(t *testing.T, dir, name, email, msg string) {
	t.Helper()
	mustGit(t, dir, "add", "--all")
	mustGit(t, dir, "-c", "user.name="+name, "-c", "user.email="+email, "commit", "-qm", msg)
}

func runnerCommit(t *testing.T, dir, msg string) {
	t.Helper()
	commitAs(t, dir, CommitAuthorName, CommitAuthorEmail, msg)
}

// round makes the commits build makes on a branch cut from base, bundles base
// to the branch's tip under ref, and leaves the checkout on the PR branch at the
// expected head.
func (f pushFixture) round(t *testing.T, base, ref string, build func()) string {
	t.Helper()
	mustGit(t, f.work, "checkout", "-q", "-B", "round", base)
	build()
	mustGit(t, f.work, "branch", "-f", ref, "round")
	bundle := filepath.Join(t.TempDir(), "fix.bundle")
	mustGit(t, f.work, "bundle", "create", bundle, "^"+base, ref)
	mustGit(t, f.work, "checkout", "-q", pushBranch)
	return bundle
}

// goodRound is a round whose one commit passes every check.
func (f pushFixture) goodRound(t *testing.T) string {
	return f.round(t, f.expected, pushBranch, func() {
		writeRepoFile(t, f.work, "b.go", "package b\n")
		runnerCommit(t, f.work, "fix")
	})
}

func (f pushFixture) input(bundle string) FixPushInput {
	return FixPushInput{Repo: f.work, Bundle: bundle, Branch: pushBranch, Expected: f.expected, Remote: "origin",
		Head: func(context.Context) (FixPushHead, error) { return FixPushHead{Ref: pushBranch, SHA: f.expected}, nil }}
}

func (f pushFixture) originHead(t *testing.T) string {
	return rev(t, f.origin, "refs/heads/"+pushBranch)
}

// gitPushes counts the pushes git runs from now on, by putting a logging git
// first on PATH.
func gitPushes(t *testing.T) func() int {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir, log := t.TempDir(), filepath.Join(t.TempDir(), "log")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		raw, _ := os.ReadFile(log)
		return strings.Count(string(raw), " push ")
	}
}

func wantRefusal(t *testing.T, err error, want FixPushReason) {
	t.Helper()
	var r *FixPushRefusal
	if !errors.As(err, &r) || r.Reason != want {
		t.Fatalf("err = %v, want a refusal for %s", err, want)
	}
}

func TestFixPushPushesTheRoundsOneCommitAndLeavesNoRefBehind(t *testing.T) {
	f := newPushFixture(t)
	tip := ""
	bundle := f.round(t, f.expected, pushBranch, func() {
		writeRepoFile(t, f.work, "b.go", "package b\n")
		runnerCommit(t, f.work, "fix")
		tip = rev(t, f.work, "HEAD")
	})
	sha, err := FixPush(context.Background(), f.input(bundle))
	if err != nil {
		t.Fatal(err)
	}
	if sha != tip || f.originHead(t) != tip {
		t.Fatalf("pushed %s, origin at %s, want %s", sha, f.originHead(t), tip)
	}
	if out := mustGit(t, f.work, "for-each-ref", "refs/fix-push"); out != "" {
		t.Fatalf("throwaway ref left behind: %s", out)
	}
}

func TestFixPushRefusesWhatItShouldNotPush(t *testing.T) {
	tests := []struct {
		name   string
		want   FixPushReason
		bundle func(t *testing.T, f pushFixture) string
		mod    func(in *FixPushInput)
	}{
		{"two refs", FixPushBundleRefs, func(t *testing.T, f pushFixture) string {
			mustGit(t, f.work, "checkout", "-q", "-B", "round", f.expected)
			writeRepoFile(t, f.work, "b.go", "package b\n")
			runnerCommit(t, f.work, "fix")
			mustGit(t, f.work, "branch", "-f", pushBranch+"-a", "round")
			mustGit(t, f.work, "branch", "-f", pushBranch+"-b", "round")
			bundle := filepath.Join(t.TempDir(), "fix.bundle")
			mustGit(t, f.work, "bundle", "create", bundle, "^"+f.expected, pushBranch+"-a", pushBranch+"-b")
			mustGit(t, f.work, "checkout", "-q", pushBranch)
			return bundle
		}, nil},
		{"not a bundle", FixPushBundleRefs, func(t *testing.T, f pushFixture) string {
			p := filepath.Join(t.TempDir(), "fix.bundle")
			if err := os.WriteFile(p, []byte("not a bundle"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, nil},
		{"ref is a tag", FixPushBundleRefs, func(t *testing.T, f pushFixture) string {
			mustGit(t, f.work, "checkout", "-q", "-B", "round", f.expected)
			writeRepoFile(t, f.work, "b.go", "package b\n")
			runnerCommit(t, f.work, "fix")
			mustGit(t, f.work, "tag", "v1", "round")
			bundle := filepath.Join(t.TempDir(), "fix.bundle")
			mustGit(t, f.work, "bundle", "create", bundle, "^"+f.expected, "v1")
			mustGit(t, f.work, "checkout", "-q", pushBranch)
			return bundle
		}, nil},
		{"another branch", FixPushWrongBranch, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, "wingman/other-1-1", func() {
				writeRepoFile(t, f.work, "b.go", "package b\n")
				runnerCommit(t, f.work, "fix")
			})
		}, nil},
		{"two commits", FixPushNotOneCommit, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, pushBranch, func() {
				writeRepoFile(t, f.work, "b.go", "package b\n")
				runnerCommit(t, f.work, "one")
				writeRepoFile(t, f.work, "c.go", "package c\n")
				runnerCommit(t, f.work, "two")
			})
		}, nil},
		{"a merge", FixPushNotOneCommit, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, pushBranch, func() {
				tree := rev(t, f.work, f.expected+"^{tree}")
				merge := strings.TrimSpace(mustGit(t, f.work, "-c", "user.name="+CommitAuthorName, "-c", "user.email="+CommitAuthorEmail,
					"commit-tree", tree, "-p", f.expected, "-p", f.first, "-m", "merge"))
				mustGit(t, f.work, "reset", "-q", "--hard", merge)
			})
		}, nil},
		{"another parent", FixPushWrongParent, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.first, pushBranch, func() {
				writeRepoFile(t, f.work, "b.go", "package b\n")
				runnerCommit(t, f.work, "fix")
			})
		}, nil},
		{"another author", FixPushNotRunner, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, pushBranch, func() {
				writeRepoFile(t, f.work, "b.go", "package b\n")
				mustGit(t, f.work, "add", "--all")
				mustGit(t, f.work, "-c", "user.name="+CommitAuthorName, "-c", "user.email="+CommitAuthorEmail,
					"commit", "-qm", "fix", "--author=Agent <agent@example.com>")
			})
		}, nil},
		{"another committer", FixPushNotRunner, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, pushBranch, func() {
				writeRepoFile(t, f.work, "b.go", "package b\n")
				mustGit(t, f.work, "add", "--all")
				mustGit(t, f.work, "-c", "user.name=Agent", "-c", "user.email=agent@example.com",
					"commit", "-qm", "fix", "--author="+CommitAuthorName+" <"+CommitAuthorEmail+">")
			})
		}, nil},
		{"a new workflow file", FixPushWorkflowFile, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, pushBranch, func() {
				writeRepoFile(t, f.work, ".github/workflows/new.yml", "name: new\n")
				runnerCommit(t, f.work, "fix")
			})
		}, nil},
		{"a workflow file renamed out", FixPushWorkflowFile, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, pushBranch, func() {
				mustGit(t, f.work, "mv", ".github/workflows/ci.yml", "ci.yml")
				runnerCommit(t, f.work, "fix")
			})
		}, nil},
		{"a given secret", FixPushSecret, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, pushBranch, func() {
				writeRepoFile(t, f.work, "b.go", "var k = \"plan-key-value\"\n")
				runnerCommit(t, f.work, "fix")
			})
		}, func(in *FixPushInput) { in.Secrets = []string{"plan-key-value"} }},
		{"a token-shaped string", FixPushSecret, func(t *testing.T, f pushFixture) string {
			return f.round(t, f.expected, pushBranch, func() {
				writeRepoFile(t, f.work, "b.go", "var k = \"AKIA"+strings.Repeat("Q7", 8)+"\"\n")
				runnerCommit(t, f.work, "fix")
			})
		}, nil},
		{"a head that moved", FixPushHeadMoved, func(t *testing.T, f pushFixture) string {
			return f.goodRound(t)
		}, func(in *FixPushInput) {
			in.Head = func(context.Context) (FixPushHead, error) {
				return FixPushHead{Ref: pushBranch, SHA: strings.Repeat("a", 40)}, nil
			}
		}},
		{"a pull request on another branch", FixPushWrongBranch, func(t *testing.T, f pushFixture) string {
			return f.goodRound(t)
		}, func(in *FixPushInput) {
			in.Head = func(context.Context) (FixPushHead, error) {
				return FixPushHead{Ref: "main", SHA: in.Expected}, nil
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPushFixture(t)
			in := f.input(tt.bundle(t, f))
			if tt.mod != nil {
				tt.mod(&in)
			}
			pushes := gitPushes(t)
			_, err := FixPush(context.Background(), in)
			wantRefusal(t, err, tt.want)
			if got := f.originHead(t); got != f.expected {
				t.Fatalf("origin moved to %s, want %s", got, f.expected)
			}
			if n := pushes(); n != 0 {
				t.Fatalf("git push ran %d times, want 0", n)
			}
		})
	}
}

func TestFixPushFetchesWithFsck(t *testing.T) {
	f := newPushFixture(t)
	tree := strings.TrimSpace(mustGit(t, f.work, "rev-parse", f.expected+"^{tree}"))
	body := "tree " + tree + "\nparent " + f.expected + "\nauthor " + CommitAuthorName + " <" + CommitAuthorEmail +
		"\ncommitter " + CommitAuthorName + " <" + CommitAuthorEmail + "\n\nbroken\n"
	bad := f.bundleOfCrafted(t, body)
	_, err := FixPush(context.Background(), f.input(bad))
	wantRefusal(t, err, FixPushBundleRefs)
	if got := f.originHead(t); got != f.expected {
		t.Fatalf("origin moved to %s", got)
	}
}

// bundleOfCrafted bundles one commit named as the PR's branch whose raw body is
// given, written without git's own checks, from a clone so the checkout's branch
// stays put.
func (f pushFixture) bundleOfCrafted(t *testing.T, body string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "crafted")
	mustGit(t, filepath.Dir(clone), "clone", "-q", f.work, filepath.Base(clone))
	mustGit(t, clone, "checkout", "-q", "--detach")
	cmd := exec.Command("git", "hash-object", "-t", "commit", "--literally", "-w", "--stdin")
	cmd.Dir = clone
	cmd.Stdin = strings.NewReader(body)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, clone, "branch", "-f", pushBranch, strings.TrimSpace(string(out)))
	bundle := filepath.Join(t.TempDir(), "fix.bundle")
	mustGit(t, clone, "bundle", "create", bundle, "^"+f.expected, pushBranch)
	return bundle
}

func TestFixPushLeaseLostLeavesTheOwnersPushAndIsNotRetried(t *testing.T) {
	f := newPushFixture(t)
	bundle := f.round(t, f.expected, pushBranch, func() {
		writeRepoFile(t, f.work, "b.go", "package b\n")
		runnerCommit(t, f.work, "fix")
	})
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, filepath.Dir(other), "clone", "-q", "-b", pushBranch, f.origin, filepath.Base(other))
	writeRepoFile(t, other, "owner.go", "package owner\n")
	commitAs(t, other, "the owner", "owner@example.com", "owner push")
	mustGit(t, other, "push", "-q", "origin", pushBranch)
	ownerTip := rev(t, other, "HEAD")

	pushes := gitPushes(t)
	_, err := FixPush(context.Background(), f.input(bundle))
	wantRefusal(t, err, FixPushLeaseLost)
	if got := f.originHead(t); got != ownerTip {
		t.Fatalf("origin at %s, want the owner's %s", got, ownerTip)
	}
	if n := pushes(); n != 1 {
		t.Fatalf("git push ran %d times, want 1", n)
	}
}

func TestFixPushHeadReadFailureIsAnErrorNotARefusal(t *testing.T) {
	f := newPushFixture(t)
	in := f.input(f.goodRound(t))
	in.Head = func(context.Context) (FixPushHead, error) { return FixPushHead{}, errors.New("github is down") }
	_, err := FixPush(context.Background(), in)
	var r *FixPushRefusal
	if err == nil || errors.As(err, &r) {
		t.Fatalf("err = %v, want a plain error", err)
	}
	if got := f.originHead(t); got != f.expected {
		t.Fatalf("origin moved to %s", got)
	}
}

func TestFixPushChecksItsInputs(t *testing.T) {
	head := func(context.Context) (FixPushHead, error) { return FixPushHead{}, nil }
	good := FixPushInput{Repo: "r", Bundle: "b", Branch: pushBranch, Expected: strings.Repeat("a", 40), Remote: "origin", Head: head}
	tests := []struct {
		name string
		mod  func(in *FixPushInput)
	}{
		{"no repository", func(in *FixPushInput) { in.Repo = "" }},
		{"no bundle", func(in *FixPushInput) { in.Bundle = "" }},
		{"no remote", func(in *FixPushInput) { in.Remote = "" }},
		{"short expected", func(in *FixPushInput) { in.Expected = "abc123" }},
		{"option as branch", func(in *FixPushInput) { in.Branch = "--delete" }},
		{"dotted branch", func(in *FixPushInput) { in.Branch = "a..b" }},
		{"no head reader", func(in *FixPushInput) { in.Head = nil }},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good input refused: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := good
			tt.mod(&in)
			if _, err := FixPush(context.Background(), in); !errors.Is(err, errFixPushInput) {
				t.Fatalf("err = %v, want errFixPushInput", err)
			}
		})
	}
}
