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

func TestRangeSecretsCatchesATokenShape(t *testing.T) {
	repo, rng := rangeRepo(t, "k = AKIA"+strings.Repeat("Q7", 8)+"\n")
	err := RangeSecrets(context.Background(), repo, rng, nil)
	if !errors.Is(err, ErrTokenShaped) || !strings.Contains(err.Error(), "aws-access-key") {
		t.Fatalf("err = %v, want ErrTokenShaped naming aws-access-key", err)
	}
}

func TestTokenShapedNamesEachShape(t *testing.T) {
	tests := []struct{ text, want string }{
		{"t = ghp_" + strings.Repeat("a1", 12), "github-token"},
		{"t = github_pat_" + strings.Repeat("B2", 15), "github-pat"},
		{"k = sk-" + strings.Repeat("c3", 12), "api-key"},
		{"sk-" + strings.Repeat("c3", 12), "api-key"},
		{"k = AKIA" + strings.Repeat("Q7", 8), "aws-access-key"},
		{"AKIAIOSFODNN7EXAMPLE then AKIA" + strings.Repeat("Q7", 8), "aws-access-key"},
		{"k = ASIA" + strings.Repeat("Q7", 8), "aws-session-key"},
		{"k = AIza" + strings.Repeat("d4", 17) + "e", "google-api-key"},
		{"-----BEGIN RSA PRIVATE KEY-----", "private-key"},
		{".sk-loading-spinner-container {", ""},
		{"key = AKIAIOSFODNN7EXAMPLE", ""},
		{"id = task-management-service-handler-name", ""},
		{"short = ghp_abc, cut = AKIA1234, AIzaShort", ""},
		{"-----BEGIN CERTIFICATE-----", ""},
	}
	for _, tt := range tests {
		if got := tokenShaped([]byte(tt.text)); got != tt.want {
			t.Errorf("tokenShaped(%q) = %q, want %q", tt.text, got, tt.want)
		}
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
