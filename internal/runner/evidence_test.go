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

// evidenceRemote is a bare repository whose evidence branch holds files, keyed
// by their path on the branch.
func evidenceRemote(t *testing.T, files map[string]string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "evidence.git")
	mustGit(t, filepath.Dir(remote), "init", "-q", "--bare", remote)
	seed := t.TempDir()
	mustGit(t, seed, "init", "-q", "-b", EvidenceBranch)
	for name, content := range files {
		p := filepath.Join(seed, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "seed")
	mustGit(t, seed, "remote", "add", "origin", remote)
	mustGit(t, seed, "push", "-q", "origin", EvidenceBranch+":"+EvidenceBranch)
	return remote
}

// evidenceFiles is every path the evidence branch holds in remote.
func evidenceFiles(t *testing.T, remote string) []string {
	t.Helper()
	return strings.Fields(mustGit(t, remote, "ls-tree", "-r", "--name-only", EvidenceBranch))
}

// shotsArtifact writes a shots artifact holding manifest and the named images.
func shotsArtifact(t *testing.T, manifest string, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, shotsManifest), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestEvidencePathRefusesAPathOutsideThePRsFolder is AC5's guard: a shot whose
// name would land outside pr-<n>/ is refused, so a publish cannot write there.
func TestEvidencePathRefusesAPathOutsideThePRsFolder(t *testing.T) {
	for _, name := range []string{"../evil.png", "a/../../evil.png", "sub/../../x.png", "/etc/passwd", "..", ".", ""} {
		if _, err := EvidencePath(12, name); !errors.Is(err, ErrEvidencePath) {
			t.Errorf("EvidencePath(12, %q) = %v, want ErrEvidencePath", name, err)
		}
	}
	got, err := EvidencePath(12, "sub/checkout-s.png")
	if err != nil || got != "pr-12/sub/checkout-s.png" {
		t.Fatalf("EvidencePath(12, sub/checkout-s.png) = %q, %v", got, err)
	}
}

// TestPublishEvidenceReplacesThePRsFolderOnTheEvidenceBranch is AC1 and AC4: the
// shots land under pr-<n>/ on the evidence branch, and the folder's previous
// contents are replaced while other pull requests' folders stay.
func TestPublishEvidenceReplacesThePRsFolderOnTheEvidenceBranch(t *testing.T) {
	remote := evidenceRemote(t, map[string]string{"pr-1/old.png": "old", "pr-2/keep.png": "keep"})
	manifest := `{"shots":[` +
		`{"route":"/checkout","band":"s","file":"checkout-s.png"},` +
		`{"route":"/checkout","band":"xl","file":"checkout-xl.png"}]}`
	dir := shotsArtifact(t, manifest, "checkout-s.png", "checkout-xl.png")
	shots, err := PublishEvidence(context.Background(), Evidence{PR: 1, Dir: dir, Remote: remote})
	if err != nil {
		t.Fatal(err)
	}
	want := []Screenshot{
		{Route: "/checkout", Band: "s", Path: "pr-1/checkout-s.png"},
		{Route: "/checkout", Band: "xl", Path: "pr-1/checkout-xl.png"},
	}
	if len(shots) != len(want) {
		t.Fatalf("published %d shots, want %d", len(shots), len(want))
	}
	for i := range want {
		if shots[i].Route != want[i].Route || shots[i].Band != want[i].Band || shots[i].Path != want[i].Path {
			t.Errorf("shot %d = %+v, want %+v", i, shots[i], want[i])
		}
	}
	if got := evidenceFiles(t, remote); !slices.Equal(got, []string{"pr-1/checkout-s.png", "pr-1/checkout-xl.png", "pr-2/keep.png"}) {
		t.Fatalf("evidence branch holds %v", got)
	}
}

// TestPublishEvidenceAgainWithTheSameShotsPublishesNothing keeps a re-run from
// failing on a commit it does not need to make.
func TestPublishEvidenceAgainWithTheSameShotsPublishesNothing(t *testing.T) {
	remote := evidenceRemote(t, map[string]string{"pr-2/keep.png": "keep"})
	dir := shotsArtifact(t, `{"shots":[{"route":"/checkout","band":"s","file":"checkout-s.png"}]}`, "checkout-s.png")
	for i := range 2 {
		if _, err := PublishEvidence(context.Background(), Evidence{PR: 1, Dir: dir, Remote: remote}); err != nil {
			t.Fatalf("publish %d: %v", i+1, err)
		}
	}
	if got := evidenceFiles(t, remote); !slices.Equal(got, []string{"pr-1/checkout-s.png", "pr-2/keep.png"}) {
		t.Fatalf("evidence branch holds %v", got)
	}
}

// TestPublishEvidencePublishesNothingWithoutAnArtifact is AC3's first half: a
// run that captured none has no remote to reach and no shots to publish.
func TestPublishEvidencePublishesNothingWithoutAnArtifact(t *testing.T) {
	shots, err := PublishEvidence(context.Background(), Evidence{PR: 1, Dir: t.TempDir(), Remote: "unreachable"})
	if err != nil || len(shots) != 0 {
		t.Fatalf("PublishEvidence = %v, %v; want no shots and no error", shots, err)
	}
}

// TestPublishEvidenceRefusesAPathOutsideThePRsFolder is AC5's refusal: the
// manifest naming a path that escapes pr-<n>/ fails the publish, and nothing
// reaches the branch.
func TestPublishEvidenceRefusesAPathOutsideThePRsFolder(t *testing.T) {
	remote := evidenceRemote(t, map[string]string{"pr-1/keep.png": "keep"})
	dir := shotsArtifact(t, `{"shots":[{"route":"/x","band":"s","file":"../evil.png"}]}`, "evil.png")
	if _, err := PublishEvidence(context.Background(), Evidence{PR: 1, Dir: dir, Remote: remote}); !errors.Is(err, ErrEvidencePath) {
		t.Fatalf("PublishEvidence = %v, want ErrEvidencePath", err)
	}
	if got := evidenceFiles(t, remote); !slices.Equal(got, []string{"pr-1/keep.png"}) {
		t.Fatalf("evidence branch holds %v", got)
	}
}
