package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParsePorcelain(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{"clean", "", nil},
		{"modified, added and untracked", " M cmd/surface/main.go\x00A  internal/v.go\x00?? notes.txt\x00",
			[]string{"cmd/surface/main.go", "internal/v.go", "notes.txt"}},
		{"deleted", " D old.go\x00", []string{"old.go"}},
		{"rename names both sides", "R  new.go\x00old.go\x00 M x.go\x00", []string{"new.go", "old.go", "x.go"}},
		{"a working-tree rename names both sides", " R new.go\x00old.go\x00", []string{"new.go", "old.go"}},
		{"a working-tree copy names both sides", " C new.go\x00old.go\x00", []string{"new.go", "old.go"}},
		{"spaces are not quoted", "?? a file.go\x00", []string{"a file.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePorcelain([]byte(tt.out))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("paths = %q, want %q", got, tt.want)
			}
		})
	}

	for _, bad := range []string{"M\x00", "XYZpath\x00", "R  new.go"} {
		if _, err := ParsePorcelain([]byte(bad)); !errors.Is(err, ErrPorcelain) {
			t.Errorf("ParsePorcelain(%q) err = %v, want ErrPorcelain", bad, err)
		}
	}
}

func TestParseShortstat(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want DiffLines
	}{
		{" 2 files changed, 10 insertions(+), 3 deletions(-)\n", DiffLines{Added: 10, Removed: 3}},
		{" 1 file changed, 1 insertion(+)\n", DiffLines{Added: 1}},
		{" 1 file changed, 4 deletions(-)\n", DiffLines{Removed: 4}},
		{"", DiffLines{}},
	} {
		if got := parseShortstat([]byte(tc.out)); got != tc.want {
			t.Errorf("parseShortstat(%q) = %+v, want %+v", tc.out, got, tc.want)
		}
	}
}

func TestDiffStatCountsTrackedUntrackedAndBinaryChanges(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "logo.bin"), []byte{0, 1, 2, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "logo.bin")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "bin")
	wt, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "wingman/x")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt.Dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", []byte("y\nz\n"))
	write("logo.bin", []byte{0, 9, 9, 9, '\n', '\n'})
	write("new.go", []byte("package x\n\nfunc f() {}"))
	write("blob.dat", append([]byte{0}, []byte("a\nb\nc\n")...))
	if err := os.Symlink(filepath.Join(repo, "README.md"), filepath.Join(wt.Dir, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := wt.DiffStat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// README: +2 -1; new.go: +3 untracked, its last line unterminated; the
	// binaries and the symlink count none.
	if want := (DiffLines{Added: 5, Removed: 1}); got != want {
		t.Fatalf("DiffStat = %+v, want %+v", got, want)
	}
}

func TestDiffStatCapsAnUntrackedFile(t *testing.T) {
	repo := initRepo(t)
	wt, err := AddWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "wt"), "wingman/x")
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("\n"), maxCountedFileBytes+100)
	if err := os.WriteFile(filepath.Join(wt.Dir, "big.txt"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := wt.DiffStat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != maxCountedFileBytes {
		t.Fatalf("added = %d, want the file counted up to %d bytes", got.Added, maxCountedFileBytes)
	}
}

func TestParseNumstat(t *testing.T) {
	out := "3\t1\ta.go\x00-\t-\tlogo.png\x002\t0\t\x00old.go\x00new.go\x00"
	if got, want := parseNumstat([]byte(out)), (DiffLines{Added: 5, Removed: 1}); got != want {
		t.Fatalf("parseNumstat = %+v, want %+v: a binary counts none and a rename's paths are skipped", got, want)
	}
}

func TestCountUntrackedStopsWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := countUntracked(ctx, dir, []string{"a.txt"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancel", err)
	}
}
