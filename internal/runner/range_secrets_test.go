package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// rangeRepo is a repository whose one commit after "init" adds a file holding
// body, and the range that commit makes.
func rangeRepo(t *testing.T, body string) (repo, rng string) {
	t.Helper()
	repo = initRepo(t)
	base := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	writeRepoFile(t, repo, "notes.txt", body)
	mustGit(t, repo, "add", "notes.txt")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "add notes")
	return repo, base + "..HEAD"
}

func TestRangeSecretsFindsAGivenSecretValue(t *testing.T) {
	repo, rng := rangeRepo(t, "key = hunter2-plan-key\n")
	err := RangeSecrets(context.Background(), repo, rng, []string{"", "hunter2-plan-key"})
	if !errors.Is(err, ErrSecretInBranch) {
		t.Fatalf("err = %v, want ErrSecretInBranch", err)
	}
	if err := RangeSecrets(context.Background(), repo, rng, []string{"", "another-key"}); err != nil {
		t.Fatalf("err = %v, want nil for a secret the range does not hold", err)
	}
}

func TestRangeSecretsCatchesEachTokenShape(t *testing.T) {
	tests := []struct{ name, body string }{
		{"github-token", "t = ghp_" + strings.Repeat("a1", 12)},
		{"github-pat", "t = github_pat_" + strings.Repeat("B2", 15)},
		{"api-key", "k = sk-" + strings.Repeat("c3", 12)},
		{"aws-access-key", "k = AKIA" + strings.Repeat("Q7", 8)},
		{"private-key", "-----BEGIN RSA PRIVATE KEY-----\nMIIB\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, rng := rangeRepo(t, tt.body+"\n")
			err := RangeSecrets(context.Background(), repo, rng, nil)
			if !errors.Is(err, ErrTokenShaped) || !strings.Contains(err.Error(), tt.name) {
				t.Fatalf("err = %v, want ErrTokenShaped naming %s", err, tt.name)
			}
		})
	}
}

func TestRangeSecretsPassesLookalikes(t *testing.T) {
	body := strings.Join([]string{
		"id = task-management-service-handler-name",
		"short = ghp_abc",
		"cut = AKIA1234",
		"-----BEGIN CERTIFICATE-----",
	}, "\n")
	repo, rng := rangeRepo(t, body+"\n")
	if err := RangeSecrets(context.Background(), repo, rng, nil); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestRangeSecretsChecksItsInputs(t *testing.T) {
	if err := RangeSecrets(context.Background(), "", "a..b", nil); !errors.Is(err, errRangeInput) {
		t.Fatal("an empty repository was accepted")
	}
	if err := RangeSecrets(context.Background(), t.TempDir(), "", nil); !errors.Is(err, errRangeInput) {
		t.Fatal("an empty range was accepted")
	}
}
