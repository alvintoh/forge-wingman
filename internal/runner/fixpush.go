package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// FixPushReason names why a fix push was refused.
type FixPushReason string

// The reasons a fix push is refused; none of them pushes anything.
const (
	// FixPushBundleRefs: the bundle is unreadable, or does not list exactly one
	// branch ref.
	FixPushBundleRefs FixPushReason = "bundle-refs"
	// FixPushWrongBranch: the bundle's one ref, or the pull request's head, is a
	// branch other than the pull request's own.
	FixPushWrongBranch FixPushReason = "wrong-branch"
	// FixPushNotOneCommit: the range from the expected head to the tip is not one
	// commit, or holds a merge.
	FixPushNotOneCommit FixPushReason = "not-one-commit"
	// FixPushWrongParent: the one commit's parent is not the expected head.
	FixPushWrongParent FixPushReason = "wrong-parent"
	// FixPushNotRunner: the commit's author or committer is not the runner.
	FixPushNotRunner FixPushReason = "not-runner"
	// FixPushWorkflowFile: the commit touches a path under the workflow directory.
	FixPushWorkflowFile FixPushReason = "workflow-file"
	// FixPushSecret: a new object holds a given secret or a token-shaped string.
	FixPushSecret FixPushReason = "secret"
	// FixPushHeadMoved: the pull request's head is no longer the expected head.
	FixPushHeadMoved FixPushReason = "head-moved"
	// FixPushLeaseLost: the push was refused because the branch moved.
	FixPushLeaseLost FixPushReason = "lease-lost"
)

// FixPushRefusal is a bundle FixPush declined to push, with the reason.
type FixPushRefusal struct {
	Reason FixPushReason
	Detail string
}

func (e *FixPushRefusal) Error() string {
	return "fix push refused: " + string(e.Reason) + ": " + e.Detail
}

func refuse(reason FixPushReason, format string, args ...any) error {
	return &FixPushRefusal{Reason: reason, Detail: truncate(fmt.Sprintf(format, args...), logErrorLimit)}
}

// FixPushHead is a pull request's head as GitHub reports it.
type FixPushHead struct {
	Ref string
	SHA string
}

// FixPushInput is what one fix push needs. Head reads the pull request's current
// head from GitHub, and is called once, just before the push.
type FixPushInput struct {
	// Repo is the checkout the bundle is fetched into and pushed from; its own
	// git config carries the push credential.
	Repo string
	// Bundle is the path of the round's bundle.
	Bundle string
	// Branch is the pull request's own branch.
	Branch string
	// Expected is the head the round was made on.
	Expected string
	// Remote is the remote the branch is pushed to.
	Remote string
	// Secrets are the credential values the push job holds (its plan keys and its
	// GitHub token), which no commit may carry; empty values are skipped.
	Secrets []string
	Head    func(ctx context.Context) (FixPushHead, error)
}

var (
	fullSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	branchChars = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// incomingRef is the throwaway ref the bundle's commit is fetched to, so no
// check ever runs against the pull request's own branch.
const incomingRef = "refs/fix-push/incoming"

// errFixPushInput reports a fix push input that is not usable.
var errFixPushInput = errors.New("invalid fix push input")

// Validate checks the input before git runs.
func (in FixPushInput) Validate() error {
	switch {
	case in.Repo == "" || in.Bundle == "" || in.Remote == "":
		return fmt.Errorf("%w: a repository, a bundle and a remote are required", errFixPushInput)
	case !fullSHA.MatchString(in.Expected):
		return fmt.Errorf("%w: the expected head must be a full sha", errFixPushInput)
	case !branchChars.MatchString(in.Branch) || strings.Contains(in.Branch, "..") || strings.HasSuffix(in.Branch, "/") || strings.HasSuffix(in.Branch, ".lock"):
		return fmt.Errorf("%w: %q is not an accepted branch name", errFixPushInput, truncate(in.Branch, logErrorLimit))
	case in.Head == nil:
		return fmt.Errorf("%w: a way to read the pull request's head is required", errFixPushInput)
	}
	return nil
}

// FixPush pushes the bundle's one commit onto the pull request's branch when it
// passes every check, and returns the pushed sha. Anything else is refused with
// a *FixPushRefusal and nothing is pushed. The bundle is untrusted: the agent
// built it with a shell over its own git directory, so the commit's author and
// committer are forgeable and check only that the bundle is the runner's
// intended output; the shape checks (one commit, on the expected head, no
// workflow file, no secret) are the protection. The checks run against a
// throwaway ref fetched with fsck, the push is a force-with-lease on the
// expected head, and a refused lease is final: the commit is neither retried nor
// rebased.
func FixPush(ctx context.Context, in FixPushInput) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("finding git: %w", err)
	}
	tip, err := fetchIncoming(ctx, bin, in)
	if err != nil {
		return "", err
	}
	defer func() { _, _ = runGit(context.WithoutCancel(ctx), bin, in.Repo, nil, "update-ref", "-d", incomingRef) }()
	if err := checkIncoming(ctx, bin, in, tip); err != nil {
		return "", err
	}
	head, err := in.Head(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the pull request's head: %w", err)
	}
	if head.Ref != in.Branch {
		return "", refuse(FixPushWrongBranch, "the pull request's branch is %s, not %s", head.Ref, in.Branch)
	}
	if head.SHA != in.Expected {
		return "", refuse(FixPushHeadMoved, "the pull request is at %s, the round was made on %s", head.SHA, in.Expected)
	}
	if err := push(ctx, bin, in); err != nil {
		return "", err
	}
	return tip, nil
}

// fetchIncoming reads the bundle's refs, refuses unless it lists exactly the
// pull request's branch, fetches that branch to incomingRef with its objects
// checked as they arrive and returns the commit it points at.
func fetchIncoming(ctx context.Context, bin string, in FixPushInput) (string, error) {
	out, err := runGit(ctx, bin, in.Repo, nil, "bundle", "list-heads", in.Bundle)
	if err != nil {
		return "", refuse(FixPushBundleRefs, "reading the bundle: %s", detail(err))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		return "", refuse(FixPushBundleRefs, "the bundle lists %d refs, want 1", len(lines))
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/heads/") {
		return "", refuse(FixPushBundleRefs, "the bundle's ref %q is not a branch", lines[0])
	}
	if name := strings.TrimPrefix(fields[1], "refs/heads/"); name != in.Branch {
		return "", refuse(FixPushWrongBranch, "the bundle carries %s, not %s", name, in.Branch)
	}
	fetch := []string{"-c", "transfer.fsckObjects=true", "fetch", "--no-tags", in.Bundle, "+refs/heads/" + in.Branch + ":" + incomingRef}
	if _, err := runGit(ctx, bin, in.Repo, nil, fetch...); err != nil {
		return "", refuse(FixPushBundleRefs, "fetching the bundle: %s", detail(err))
	}
	kind, err := runGit(ctx, bin, in.Repo, nil, "cat-file", "-t", incomingRef)
	if err != nil {
		return "", fmt.Errorf("reading the fetched ref's type: %s", detail(err))
	}
	if t := strings.TrimSpace(string(kind)); t != "commit" {
		return "", refuse(FixPushBundleRefs, "the bundle's ref names a %s, not a commit", t)
	}
	return fields[0], nil
}

// checkIncoming runs the shape checks on the commit at tip, in the order a
// reader would name them.
func checkIncoming(ctx context.Context, bin string, in FixPushInput, tip string) error {
	revs, err := runGit(ctx, bin, in.Repo, nil, "rev-list", "--parents", in.Expected+".."+incomingRef)
	if err != nil {
		return fmt.Errorf("listing the commits since %s: %s", in.Expected, detail(err))
	}
	var commits []string
	if listed := strings.TrimSpace(string(revs)); listed != "" {
		commits = strings.Split(listed, "\n")
	}
	if len(commits) != 1 {
		return refuse(FixPushNotOneCommit, "the range from %s holds %d commits, want 1", in.Expected, len(commits))
	}
	if len(strings.Fields(commits[0])) > 2 {
		return refuse(FixPushNotOneCommit, "the commit after %s is a merge", in.Expected)
	}
	if parents := strings.Fields(commits[0])[1:]; len(parents) != 1 || parents[0] != in.Expected {
		return refuse(FixPushWrongParent, "the commit's parent is not %s", in.Expected)
	}
	who, err := runGit(ctx, bin, in.Repo, nil, "log", "-1", "--format=%an%n%ae%n%cn%n%ce", incomingRef)
	if err != nil {
		return fmt.Errorf("reading the commit's author: %s", detail(err))
	}
	want := strings.Join([]string{CommitAuthorName, CommitAuthorEmail, CommitAuthorName, CommitAuthorEmail}, "\n") + "\n"
	if string(who) != want {
		return refuse(FixPushNotRunner, "the commit's author or committer is not the runner's")
	}
	files, err := runGit(ctx, bin, in.Repo, nil, "diff", "--no-renames", "--name-only", "-z", in.Expected, incomingRef)
	if err != nil {
		return fmt.Errorf("diffing the commit: %s", detail(err))
	}
	if hits := WorkflowFiles(splitNUL(files)); len(hits) > 0 {
		return refuse(FixPushWorkflowFile, "the commit touches %s", strings.Join(hits, ", "))
	}
	if err := RangeSecrets(ctx, in.Repo, in.Expected+".."+incomingRef, in.Secrets); err != nil {
		if errors.Is(err, ErrSecretInBranch) || errors.Is(err, ErrTokenShaped) {
			return refuse(FixPushSecret, "%s", err.Error())
		}
		return err
	}
	return nil
}

// push sends incomingRef to the branch under a lease on the expected head. A
// push the lease refuses is a refusal; any other failure is an error. It is run
// once, and git's own config (the checkout's credential) applies: runGit hides
// only the global and system config.
func push(ctx context.Context, bin string, in FixPushInput) error {
	lease := "--force-with-lease=refs/heads/" + in.Branch + ":" + in.Expected
	_, err := runGit(ctx, bin, in.Repo, nil, "push", lease, in.Remote, incomingRef+":refs/heads/"+in.Branch)
	if err == nil {
		return nil
	}
	var g *GitError
	if errors.As(err, &g) && bytes.Contains([]byte(g.Stderr), []byte("stale info")) {
		return refuse(FixPushLeaseLost, "the branch moved after the round was made")
	}
	return fmt.Errorf("pushing the branch: %s", detail(err))
}
