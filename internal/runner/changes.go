package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
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
	untracked, err := w.git(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	if paths := splitNUL(untracked); len(paths) > 0 {
		if _, err := w.git(ctx, append([]string{"add", "-N", "--"}, paths...)...); err != nil {
			return "", err
		}
	}
	out, err := w.git(ctx, "diff", w.Base)
	if err != nil {
		return "", err
	}
	return truncate(string(out), maxReviewDiffBytes), nil
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
