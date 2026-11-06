package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrPorcelain reports `git status --porcelain -z` output that does not parse.
var ErrPorcelain = errors.New("malformed porcelain output")

// ParsePorcelain returns every path named by `git status --porcelain=v1 -z`,
// including both sides of a rename or copy.
func ParsePorcelain(out []byte) ([]string, error) {
	var paths []string
	entries := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) == 0 {
			continue
		}
		if len(e) < 4 || e[2] != ' ' {
			return nil, fmt.Errorf("%w: entry of %d bytes", ErrPorcelain, len(e))
		}
		paths = append(paths, string(e[3:]))
		if e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C' {
			i++
			if i >= len(entries) {
				return nil, fmt.Errorf("%w: rename without its source", ErrPorcelain)
			}
			paths = append(paths, string(entries[i]))
		}
	}
	return paths, nil
}

// uncommitted is every path git reports as changed and not yet committed.
func (w Worktree) uncommitted(ctx context.Context) ([]string, error) {
	out, err := w.git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	return ParsePorcelain(out)
}

// PendingFiles is every file that differs from the base, committed by the agent
// or not, without committing anything.
func (w Worktree) PendingFiles(ctx context.Context) ([]string, error) {
	paths, err := w.uncommitted(ctx)
	if err != nil {
		return nil, err
	}
	diff, err := w.git(ctx, "diff", "--name-only", "-z", w.Base, "HEAD")
	if err != nil {
		return nil, err
	}
	all := append(paths, splitNUL(diff)...)
	slices.Sort(all)
	return slices.Compact(all), nil
}

const (
	diffStatTimeout     = 20 * time.Second
	maxCountedFileBytes = 1 << 20
	maxCountedFiles     = 1000
	binarySniffBytes    = 8000
)

// DiffStat counts the lines the worktree adds and removes against the base,
// committed or not, without taking the index lock the agent may hold. An
// untracked file counts as added lines, read up to maxCountedFileBytes; a
// binary file counts as none.
func (w Worktree) DiffStat(ctx context.Context) (DiffLines, error) {
	ctx, cancel := context.WithTimeout(ctx, diffStatTimeout)
	defer cancel()
	out, err := w.git(ctx, "--no-optional-locks", "diff", "--numstat", "-z", w.Base)
	if err != nil {
		return DiffLines{}, err
	}
	lines := parseNumstat(out)
	untracked, err := w.git(ctx, "--no-optional-locks", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return DiffLines{}, err
	}
	added, err := countUntracked(ctx, w.Dir, splitNUL(untracked))
	if err != nil {
		return DiffLines{}, err
	}
	lines.Added += added
	return lines, nil
}

// countUntracked sums the lines of the first maxCountedFiles paths under dir,
// stopping as soon as ctx is done.
func countUntracked(ctx context.Context, dir string, paths []string) (int64, error) {
	var total int64
	for _, p := range paths[:min(len(paths), maxCountedFiles)] {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := countFileLines(filepath.Join(dir, p))
		if err != nil {
			return 0, fmt.Errorf("counting an untracked file: %w", err)
		}
		total += n
	}
	return total, nil
}

// parseNumstat sums `git diff --numstat -z`, where a binary file's "-" counts
// as no lines and a rename's two NUL-terminated paths follow its counts.
func parseNumstat(out []byte) DiffLines {
	var d DiffLines
	for _, e := range bytes.Split(out, []byte{0}) {
		fields := bytes.SplitN(e, []byte{'\t'}, 3)
		if len(fields) < 3 {
			continue
		}
		added, _ := strconv.ParseInt(string(fields[0]), 10, 64)
		removed, _ := strconv.ParseInt(string(fields[1]), 10, 64)
		d.Added += added
		d.Removed += removed
	}
	return d
}

// countFileLines counts a regular file's lines as git would, reading at most
// maxCountedFileBytes; anything else, or a binary file, counts as none.
func countFileLines(path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, nil
	}
	f, err := openForCount(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = f.Close() }()
	// The path can change between Lstat and open, so the opened file decides.
	if opened, err := f.Stat(); err != nil || !opened.Mode().IsRegular() {
		return 0, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxCountedFileBytes))
	if err != nil {
		return 0, err
	}
	if bytes.IndexByte(b[:min(len(b), binarySniffBytes)], 0) >= 0 {
		return 0, nil
	}
	n := int64(bytes.Count(b, []byte{'\n'}))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n, nil
}

// ErrSecretInBranch reports a branch whose objects contain a withheld secret.
var ErrSecretInBranch = errors.New("branch contains a secret value")

// Commit stages exactly the paths git reports as changed, commits them, and
// returns every file that differs from the base, the agent's own commits included.
func (w Worktree) Commit(ctx context.Context, message string) ([]string, error) {
	paths, err := w.uncommitted(ctx)
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		if _, err := w.git(ctx, append([]string{"add", "--all", "--"}, paths...)...); err != nil {
			return nil, err
		}
		if _, err := w.git(ctx, "commit", "--no-verify", "-m", message); err != nil {
			return nil, err
		}
	}
	diff, err := w.git(ctx, "diff", "--name-only", "-z", w.Base, "HEAD")
	if err != nil {
		return nil, err
	}
	return splitNUL(diff), nil
}

// TouchedPaths returns every path the branch adds, changes or removes since the
// base, counting a rename as both its source and its destination.
func (w Worktree) TouchedPaths(ctx context.Context) ([]string, error) {
	diff, err := w.git(ctx, "diff", "--name-only", "--no-renames", "-z", w.Base, "HEAD")
	if err != nil {
		return nil, err
	}
	return splitNUL(diff), nil
}

// splitNUL splits NUL-terminated git output into its non-empty entries.
func splitNUL(out []byte) []string {
	var entries []string
	for _, e := range bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		if len(e) > 0 {
			entries = append(entries, string(e))
		}
	}
	return entries
}

var (
	insertionsPattern = regexp.MustCompile(`(\d+) insertions?\(\+\)`)
	deletionsPattern  = regexp.MustCompile(`(\d+) deletions?\(-\)`)
)

// DiffLines measures the branch's diff against the base.
func (w Worktree) DiffLines(ctx context.Context) (DiffLines, error) {
	out, err := w.git(ctx, "diff", "--shortstat", w.Base, "HEAD")
	if err != nil {
		return DiffLines{}, err
	}
	return parseShortstat(out), nil
}

// parseShortstat reads `git diff --shortstat`, which omits a side with no lines.
func parseShortstat(out []byte) DiffLines {
	count := func(p *regexp.Regexp) int64 {
		m := p.FindSubmatch(out)
		if m == nil {
			return 0
		}
		n, _ := strconv.ParseInt(string(m[1]), 10, 64)
		return n
	}
	return DiffLines{Added: count(insertionsPattern), Removed: count(deletionsPattern)}
}

// CheckSecret fails if any object the branch adds since the base contains secret.
func (w Worktree) CheckSecret(ctx context.Context, secret string) error {
	if secret == "" {
		return nil
	}
	list, err := w.git(ctx, "rev-list", "--objects", w.Base+".."+w.Branch)
	if err != nil {
		return err
	}
	var ids bytes.Buffer
	for _, line := range bytes.Split(list, []byte{'\n'}) {
		if id, _, _ := bytes.Cut(line, []byte{' '}); len(id) > 0 {
			ids.Write(id)
			ids.WriteByte('\n')
		}
	}
	if ids.Len() == 0 {
		return nil
	}
	objects, err := w.gitStdin(ctx, &ids, "cat-file", "--batch")
	if err != nil {
		return err
	}
	if bytes.Contains(objects, []byte(secret)) {
		return ErrSecretInBranch
	}
	return nil
}

// Dirty reports whether the worktree holds any uncommitted change.
func (w Worktree) Dirty(ctx context.Context) (bool, error) {
	out, err := w.git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// maxReviewDiffBytes bounds how much of the branch's diff the review prompt
// carries, since an unbounded diff would make the review call arbitrarily
// expensive.
const maxReviewDiffBytes = 200 << 10

// DiffPending returns the branch's diff against its base including the
// current working tree — staged and unstaged, but not yet committed —
// truncated to maxReviewDiffBytes. Used by the pre-PR loop's review pass
// (FR-28), which runs before PhaseCommit while HEAD has not moved yet, so a
// diff against HEAD would always be empty.
//
// A new, untracked file is marked intent-to-add first: plain `git diff`
// otherwise omits an untracked path entirely, and Commit's later `git add
// --all` stages its real content regardless of this marker.
func (w Worktree) DiffPending(ctx context.Context) (string, error) {
	if err := w.intentToAdd(ctx); err != nil {
		return "", err
	}
	out, err := w.git(ctx, "diff", w.Base)
	if err != nil {
		return "", err
	}
	return truncate(string(out), maxReviewDiffBytes), nil
}

// PendingAddedLines is the line numbers each changed file adds, as they stand
// in that file, including the current working tree. Its keys are the files the
// diff adds lines to: a file the branch only deletes is not one.
func (w Worktree) PendingAddedLines(ctx context.Context) (map[string][]int, error) {
	if err := w.intentToAdd(ctx); err != nil {
		return nil, err
	}
	out, err := w.git(ctx, "diff", "--unified=0", "--no-color", w.Base)
	if err != nil {
		return nil, err
	}
	return parseAddedLines(string(out)), nil
}

// intentToAdd marks every untracked file intent-to-add, so a plain `git diff`
// carries it: without the marker an untracked path is omitted entirely.
func (w Worktree) intentToAdd(ctx context.Context) error {
	untracked, err := w.git(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	paths := splitNUL(untracked)
	if len(paths) == 0 {
		return nil
	}
	_, err = w.git(ctx, append([]string{"add", "-N", "--"}, paths...)...)
	return err
}

// parseAddedLines reads a unified diff for the lines it adds to each file. A
// hunk header resets the file's new-side line counter, an added line records
// where it landed and advances it, and a context line only advances it — so a
// file with no added lines has no entry.
func parseAddedLines(diff string) map[string][]int {
	added := map[string][]int{}
	file, line := "", 0
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++ "):
			file, line = diffPath(l), 0
		case strings.HasPrefix(l, "@@"):
			start, ok := hunkStart(l)
			if !ok {
				file, line = "", 0
				continue
			}
			line = start
		case file == "":
		case strings.HasPrefix(l, "+"):
			added[file] = append(added[file], line)
			line++
		case strings.HasPrefix(l, "-"):
		case strings.HasPrefix(l, " "):
			line++
		}
	}
	return added
}

// diffPath is the path a `+++` line names, empty for /dev/null.
func diffPath(l string) string {
	p := strings.TrimSpace(strings.TrimPrefix(l, "+++ "))
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, "b/")
}

// hunkStart is the first line the hunk's new side covers, from `@@ -a,b +c,d @@`.
func hunkStart(l string) (int, bool) {
	_, after, ok := strings.Cut(l, "+")
	if !ok {
		return 0, false
	}
	end := strings.IndexAny(after, ", ")
	if end < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(after[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// Bundle writes the branch's commits since the base to a git bundle at path,
// naming them ref so the bundle carries the branch a run pushes — not the
// worktree's own fixed local branch — then verifies it.
func (w Worktree) Bundle(ctx context.Context, path, ref string) error {
	if _, err := w.git(ctx, "update-ref", "refs/heads/"+ref, w.Branch); err != nil {
		return err
	}
	if _, err := w.git(ctx, "bundle", "create", path, w.Base+".."+ref); err != nil {
		return err
	}
	_, err := w.git(ctx, "bundle", "verify", "--quiet", path)
	return err
}
