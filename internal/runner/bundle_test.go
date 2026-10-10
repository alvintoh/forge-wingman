package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// bundleRepo builds a repository whose main has moved on past the base a
// branch was cut from, bundles that branch's commits since the base, and returns
// the bundle's path beside a clone holding main's history whole — the checkout
// pr-meta's job has, which is what makes the bundle fetchable at all.
func bundleRepo(t *testing.T, runID string) (bundle, work, branch, repo string) {
	t.Helper()
	repo = initRepo(t)
	base := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	branch = BranchName("abc-12", runID+"-1")
	mustGit(t, repo, "checkout", "-q", "-b", branch, base)
	for path, body := range map[string]string{"a.go": "package a\n", "internal/b.go": "package internal\n"} {
		writeRepoFile(t, repo, path, body)
	}
	mustGit(t, repo, "add", "--all")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "the run")
	// Main advances after the run is dispatched, as any merged PR leaves it.
	mustGit(t, repo, "checkout", "-q", "main")
	writeRepoFile(t, repo, "README.md", "moved on\n")
	mustGit(t, repo, "add", "--all")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "main moves")
	bundle = filepath.Join(t.TempDir(), bundleName)
	mustGit(t, repo, "bundle", "create", bundle, base+".."+branch)
	work = filepath.Join(t.TempDir(), "work")
	mustGit(t, filepath.Dir(work), "clone", "-q", repo, filepath.Base(work))
	return bundle, work, branch, repo
}

func writeRepoFile(t *testing.T, repo, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestBundleChangedFilesReadsTheRunsOwnChangesPastAMovedMain is the design's
// load-bearing case: the PR's changed files are the branch's own, measured
// against main — which has moved since the branch was cut, so a diff against
// main's tip would name main's files as the run's.
func TestBundleChangedFilesReadsTheRunsOwnChangesPastAMovedMain(t *testing.T) {
	bundle, work, branch, _ := bundleRepo(t, "42")
	got, err := Bundle{Path: bundle, Repo: work, Branch: branch, Base: "origin/main", RunID: "42"}.
		ChangedFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"a.go", "internal/b.go"}) {
		t.Fatalf("changed files = %q, want the branch's own", got)
	}
}

func TestValidBranch(t *testing.T) {
	for _, tt := range []struct {
		runID, branch string
		want          bool
	}{
		{"42", BranchName("abc-12", "42-1"), true},
		{"42", "wingman/abc-12-42-2", true},
		{"42", "wingman/abc-12-43-1", false},
		{"42", "wingman/abc-12-42-1x", false},
		{"42", "refs/heads/wingman/abc-12-42-1", false},
		{"42", "wingman/abc-12-42-1 --upload-pack=x", false},
		{"42", "", false},
	} {
		if got := ValidBranch(tt.runID, tt.branch); got != tt.want {
			t.Errorf("ValidBranch(%q, %q) = %v, want %v", tt.runID, tt.branch, got, tt.want)
		}
	}
}

// TestBundleChangedFilesRefusesAnotherRunsBranch is the guard on git being
// handed a ref the model job named: the branch arrives as the model job's own
// output, so a name of any other run is refused before a fetch.
func TestBundleChangedFilesRefusesAnotherRunsBranch(t *testing.T) {
	bundle, work, branch, _ := bundleRepo(t, "42")
	_, err := Bundle{Path: bundle, Repo: work, Branch: branch, Base: "origin/main", RunID: "43"}.
		ChangedFiles(context.Background())
	if !errors.Is(err, errBranchNotOurs) {
		t.Fatalf("err = %v, want errBranchNotOurs", err)
	}
}

func TestBundleChangedFilesNeedsABundle(t *testing.T) {
	_, work, branch, _ := bundleRepo(t, "42")
	_, err := Bundle{Repo: work, Branch: branch, Base: "origin/main", RunID: "42"}.
		ChangedFiles(context.Background())
	if err == nil {
		t.Fatal("read the branch with no bundle")
	}
}

// TestBundleChangedFilesReportsABaseTheRepositoryDoesNotHold records why
// pr-meta's job checks main out whole: the bundle carries the commits since the
// base the branch was cut from, so a repository holding main's tip alone cannot
// fetch it. The clone goes over the transport, since a local one carries every
// object whether its history reaches them or not.
func TestBundleChangedFilesReportsABaseTheRepositoryDoesNotHold(t *testing.T) {
	bundle, _, branch, repo := bundleRepo(t, "42")
	shallow := filepath.Join(t.TempDir(), "shallow")
	mustGit(t, filepath.Dir(shallow), "clone", "-q", "--depth", "1", "file://"+repo, filepath.Base(shallow))
	_, err := Bundle{Path: bundle, Repo: shallow, Branch: branch, Base: "origin/main", RunID: "42"}.
		ChangedFiles(context.Background())
	if err == nil || !strings.Contains(err.Error(), "prerequisite") {
		t.Fatalf("err = %v, want the missing prerequisite named", err)
	}
}
