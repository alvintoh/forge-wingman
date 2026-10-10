package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// branchPattern is the name a run's pushed branch carries — the ticket's own
// segment, then the workflow run and the attempt that built it — with
// ${GITHUB_RUN_ID} standing for the run. run.yml's check and pr jobs check the
// same shape in shell.
const branchPattern = `^wingman/[a-z0-9]+(-[a-z0-9]+)*-${GITHUB_RUN_ID}-[0-9]+$`

// errBranchNotOurs reports a branch name that does not name this run's branch.
var errBranchNotOurs = errors.New("branch is not this run's")

// ValidBranch reports whether branch names the branch run runID builds on. The
// name arrives as the model job's output and is handed to git as a ref, so a
// name of any other run is refused before git runs.
func ValidBranch(runID, branch string) bool {
	return branchRe(runID).MatchString(branch)
}

// branchRe is branchPattern with the run's own id in place of its placeholder.
func branchRe(runID string) *regexp.Regexp {
	return regexp.MustCompile(strings.ReplaceAll(branchPattern, "${GITHUB_RUN_ID}", regexp.QuoteMeta(runID)))
}

// Bundle is the git bundle the model job uploads: the branch's commits since the
// base they were cut from. Nothing on GitHub carries the branch until the pr job
// pushes it, so the bundle is how a job holding no GitHub credential reads the
// write set the run actually produced.
type Bundle struct {
	// Path is where the artifact was downloaded to.
	Path string
	// Repo is the repository the bundle is fetched into, which holds the base.
	Repo string
	// Branch is the branch it carries, as the model job named it.
	Branch string
	// Base is the ref in Repo the branch's changes are measured against.
	Base string
	// RunID is the workflow run whose branch it must be.
	RunID string
}

// ChangedFiles checks Branch out of the bundle and returns the files it changes
// since the merge base of Base and Branch — the write set the pull request
// carries, which the branch's own base leaves untouched even where main has
// moved on since. The bundle and the branch name both come from the model job,
// which is untrusted, so the branch is checked against RunID first and the
// fetched objects are checked as they arrive.
func (b Bundle) ChangedFiles(ctx context.Context) ([]string, error) {
	if !ValidBranch(b.RunID, b.Branch) {
		return nil, fmt.Errorf("%w: %q", errBranchNotOurs, truncate(b.Branch, logErrorLimit))
	}
	if b.Path == "" {
		return nil, errors.New("no bundle to read the branch from")
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("finding git: %w", err)
	}
	ref := "refs/heads/" + b.Branch
	fetch := []string{"-c", "transfer.fsckObjects=true", "fetch", b.Path, ref + ":" + ref}
	if _, err := runGit(ctx, bin, b.Repo, nil, fetch...); err != nil {
		return nil, fmt.Errorf("fetching the branch from the bundle: %s", detail(err))
	}
	out, err := runGit(ctx, bin, b.Repo, nil, "diff", "--name-only", "-z", b.Base+"..."+b.Branch)
	if err != nil {
		return nil, fmt.Errorf("diffing the branch against %s: %s", b.Base, detail(err))
	}
	files := splitNUL(out)
	slices.Sort(files)
	return slices.Compact(files), nil
}
