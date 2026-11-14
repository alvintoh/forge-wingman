package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrGitTampered reports git configuration that changed while the agent ran.
var ErrGitTampered = errors.New("git configuration changed during the agent run")

// ErrHeadMoved reports a worktree whose HEAD is no longer the run's branch.
var ErrHeadMoved = errors.New("worktree HEAD is not the run's branch")

// Worktree is the run's own git worktree on its own branch.
//
// Its paths are captured before the agent runs, and git is invoked through
// them rather than through anything inside the worktree.
type Worktree struct {
	Dir       string
	Branch    string
	Base      string
	GitDir    string
	CommonDir string
	gitBin    string
	snapshot  [sha256.Size]byte
}

// AddWorktree creates a worktree at dir on a branch cut from repo's HEAD,
// resetting the branch when it already exists. Stale registrations are pruned
// first, so a retry in the same clone reuses its fixed branch instead of failing
// on a leftover worktree that still claims it.
func AddWorktree(ctx context.Context, repo, dir, branch string) (Worktree, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return Worktree{}, fmt.Errorf("finding git: %w", err)
	}
	return addWorktree(ctx, bin, repo, dir, branch, "-B", branch, dir)
}

// ErrBranchHeadMismatch reports a branch that is not at the head its caller
// named, so a worktree cut there would commit on something other than that head.
var ErrBranchHeadMismatch = errors.New("the branch is not at the head the caller named")

// CheckoutWorktree creates a worktree at dir on branch, which repo must hold at
// head: the pull request's own head, so a round commits on the commits it was
// given rather than on a stale local branch. A branch repo does not hold, and one
// at any other commit, are refused before a worktree exists. The worktree is
// added without resetting the branch, and a round's own commit refuses any base
// other than the head it was given, so a branch that moves afterwards stops the
// round instead of carrying it onto another base.
func CheckoutWorktree(ctx context.Context, repo, dir, branch, head string) (Worktree, error) {
	if branch == "" || head == "" {
		return Worktree{}, errors.New("a worktree needs the branch and the head it is cut at")
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return Worktree{}, fmt.Errorf("finding git: %w", err)
	}
	at, err := runGit(ctx, bin, repo, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return Worktree{}, fmt.Errorf("%w: %s holds no branch %s: %v", ErrBranchHeadMismatch, repo, branch, err)
	}
	if got := strings.TrimSpace(string(at)); got != head {
		return Worktree{}, fmt.Errorf("%w: %s is at %s, not %s", ErrBranchHeadMismatch, branch, got, head)
	}
	return addWorktree(ctx, bin, repo, dir, branch, dir, branch)
}

// addWorktree registers a worktree at dir on branch, whose add arguments are git
// worktree add's own, and resolves it.
func addWorktree(ctx context.Context, bin, repo, dir, branch string, add ...string) (Worktree, error) {
	// A registration a crashed attempt left behind would make the add below
	// refuse the branch, so the prune is best effort: one that cannot run leaves
	// that refusal to name the real problem.
	_, _ = runGit(ctx, bin, repo, nil, "worktree", "prune")
	if _, err := runGit(ctx, bin, repo, nil, append([]string{"worktree", "add"}, add...)...); err != nil {
		return Worktree{}, err
	}
	return openWorktree(ctx, bin, dir, branch)
}

// openWorktree resolves the worktree just created at dir on branch: its git
// directories, the commit its HEAD holds, and a snapshot of the configuration it
// executes.
func openWorktree(ctx context.Context, bin, dir, branch string) (Worktree, error) {
	out, err := runGit(ctx, bin, dir, nil, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "HEAD")
	if err != nil {
		return Worktree{}, err
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(fields) != 3 {
		return Worktree{}, fmt.Errorf("rev-parse returned %d fields, want 3", len(fields))
	}
	w := Worktree{Dir: dir, Branch: branch, GitDir: fields[0], CommonDir: fields[1], Base: fields[2], gitBin: bin}
	if w.snapshot, err = w.configHash(); err != nil {
		return Worktree{}, err
	}
	return w, nil
}

// GitCommonDir is repo's git common directory as an absolute path, resolved
// as AddWorktree resolves a worktree's.
func GitCommonDir(ctx context.Context, repo string) (string, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("finding git: %w", err)
	}
	out, err := runGit(ctx, bin, repo, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// HeadSHA is repo's current HEAD commit: the base a plan is made against, which
// the record keeps so a later re-plan can tell what changed under it.
func HeadSHA(ctx context.Context, repo string) (string, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("finding git: %w", err)
	}
	out, err := runGit(ctx, bin, repo, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// protectedFiles are the files whose content decides what git executes.
func (w Worktree) protectedFiles() []string {
	return []string{
		filepath.Join(w.CommonDir, "config"),
		filepath.Join(w.CommonDir, "info", "attributes"),
		filepath.Join(w.GitDir, "commondir"),
		filepath.Join(w.GitDir, "config.worktree"),
	}
}

func (w Worktree) configHash() ([sha256.Size]byte, error) {
	h := sha256.New()
	for _, f := range w.protectedFiles() {
		b, err := os.ReadFile(f)
		switch {
		case errors.Is(err, os.ErrNotExist):
			_, _ = fmt.Fprintf(h, "%s absent\n", f)
		case err != nil:
			return [sha256.Size]byte{}, fmt.Errorf("reading %s: %w", f, err)
		default:
			_, _ = fmt.Fprintf(h, "%s %d\n", f, len(b))
			h.Write(b)
		}
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// Verify fails if the git configuration changed since the worktree was created,
// or if HEAD no longer names the run's branch.
func (w Worktree) Verify(ctx context.Context) error {
	sum, err := w.configHash()
	if err != nil {
		return err
	}
	if sum != w.snapshot {
		return ErrGitTampered
	}
	head, err := w.git(ctx, "symbolic-ref", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != "refs/heads/"+w.Branch {
		return ErrHeadMoved
	}
	return nil
}

func (w Worktree) git(ctx context.Context, args ...string) ([]byte, error) {
	return w.gitStdin(ctx, nil, args...)
}

func (w Worktree) gitStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	return runGit(ctx, w.gitBin, w.Dir, stdin, append([]string{"--git-dir=" + w.GitDir, "--work-tree=" + w.Dir}, args...)...)
}

// GitError is a failed git invocation whose message names only the subcommand.
type GitError struct {
	Subcommand string
	Err        error
	Stderr     string
}

func (e *GitError) Error() string { return "git " + e.Subcommand + ": " + e.Err.Error() }
func (e *GitError) Unwrap() error { return e.Err }

// gitWaitDelay is how long a cancelled git's output pipes are held open for a
// child process (a remote helper, an alias's shell) before they are closed.
const gitWaitDelay = 5 * time.Second

func runGit(ctx context.Context, bin, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.WaitDelay = gitWaitDelay
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"LANG=C",
		"GIT_AUTHOR_NAME=" + CommitAuthorName,
		"GIT_AUTHOR_EMAIL=" + CommitAuthorEmail,
		"GIT_COMMITTER_NAME=" + CommitAuthorName,
		"GIT_COMMITTER_EMAIL=" + CommitAuthorEmail,
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, &GitError{Subcommand: subcommand(args), Err: err, Stderr: stderr.String()}
	}
	return out, nil
}

func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++
		case !strings.HasPrefix(args[i], "-"):
			return args[i]
		}
	}
	return ""
}
