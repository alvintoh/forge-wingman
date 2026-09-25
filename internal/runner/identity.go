package runner

import (
	"errors"
	"fmt"
	"strings"
)

const (
	commitAuthorName  = "github-actions[bot]"
	commitAuthorEmail = "41898282+github-actions[bot]@users.noreply.github.com"
)

// ErrIdentityMismatch reports a run acting for an account other than the configured one.
var ErrIdentityMismatch = errors.New("identity mismatch")

// Identity is the account a run is configured to act for and what its job holds.
type Identity struct {
	// Account is WINGMAN_ACCOUNT, the personal account every run must act for.
	Account string
	// Owner is GITHUB_REPOSITORY_OWNER, the account the run's repository belongs to.
	Owner string
	// GitHubToken is set when GH_TOKEN or GITHUB_TOKEN is in the job's environment.
	GitHubToken bool
}

// IdentityFromEnv reads a job's identity from its environment.
func IdentityFromEnv(getenv func(string) string) Identity {
	return Identity{
		Account:     getenv("WINGMAN_ACCOUNT"),
		Owner:       getenv("GITHUB_REPOSITORY_OWNER"),
		GitHubToken: getenv("GH_TOKEN") != "" || getenv("GITHUB_TOKEN") != "",
	}
}

// CheckAccount fails unless an account is configured and owns the repository.
// GitHub logins are case-insensitive, so the comparison is too.
func (i Identity) CheckAccount() error {
	switch {
	case i.Account == "":
		return fmt.Errorf("%w: WINGMAN_ACCOUNT is not set", ErrIdentityMismatch)
	case i.Owner == "":
		return fmt.Errorf("%w: GITHUB_REPOSITORY_OWNER is not set", ErrIdentityMismatch)
	case !strings.EqualFold(i.Account, i.Owner):
		return fmt.Errorf("%w: WINGMAN_ACCOUNT does not own the repository", ErrIdentityMismatch)
	}
	return nil
}

// CheckModel is CheckAccount for the model's job, which must also have no GH_TOKEN or
// GITHUB_TOKEN in its environment.
func (i Identity) CheckModel() error {
	if err := i.CheckAccount(); err != nil {
		return err
	}
	if i.GitHubToken {
		return fmt.Errorf("%w: the model's job holds a GitHub token", ErrIdentityMismatch)
	}
	return nil
}
