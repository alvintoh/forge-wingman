package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

const (
	commitAuthorName  = "github-actions[bot]"
	commitAuthorEmail = "41898282+github-actions[bot]@users.noreply.github.com"
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
		if e[0] == 'R' || e[0] == 'C' {
			i++
			if i >= len(entries) {
				return nil, fmt.Errorf("%w: rename without its source", ErrPorcelain)
			}
			paths = append(paths, string(entries[i]))
		}
	}
	return paths, nil
}

// ErrSecretInBranch reports a branch whose objects contain a withheld secret.
var ErrSecretInBranch = errors.New("branch contains a secret value")

// Commit stages exactly the paths git reports as changed, commits them, and
// returns every file that differs from the base, the agent's own commits included.
func (w Worktree) Commit(ctx context.Context, message string) ([]string, error) {
	out, err := w.git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	paths, err := ParsePorcelain(out)
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
	var files []string
	for _, f := range bytes.Split(bytes.TrimSuffix(diff, []byte{0}), []byte{0}) {
		if len(f) > 0 {
			files = append(files, string(f))
		}
	}
	return files, nil
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

// Bundle writes the branch's commits since the base to a git bundle at path,
// then verifies it.
func (w Worktree) Bundle(ctx context.Context, path string) error {
	if _, err := w.git(ctx, "bundle", "create", path, w.Base+".."+w.Branch); err != nil {
		return err
	}
	_, err := w.git(ctx, "bundle", "verify", "--quiet", path)
	return err
}
