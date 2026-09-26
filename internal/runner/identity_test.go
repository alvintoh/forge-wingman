package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestIdentityFromEnv(t *testing.T) {
	env := map[string]string{"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "Octo", "GH_TOKEN": "x"}
	got := IdentityFromEnv(func(k string) string { return env[k] })
	if got != (Identity{Account: "octo", Owner: "Octo", GitHubToken: true}) {
		t.Fatalf("identity = %+v", got)
	}
}

func TestIdentityFromEnvSeesGitHubToken(t *testing.T) {
	got := IdentityFromEnv(func(k string) string { return map[string]string{"GITHUB_TOKEN": "x"}[k] })
	if !got.GitHubToken {
		t.Fatal("GitHubToken = false with GITHUB_TOKEN set")
	}
}

func TestCheckAccountNamesTheMissingVariable(t *testing.T) {
	for want, id := range map[string]Identity{
		"WINGMAN_ACCOUNT is not set":         {Owner: "octo"},
		"GITHUB_REPOSITORY_OWNER is not set": {Account: "octo"},
	} {
		if err := id.CheckAccount(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckAccount(%+v) = %v, want it to say %q", id, err, want)
		}
	}
}

func TestIdentityCheck(t *testing.T) {
	for _, tt := range []struct {
		name         string
		id           Identity
		wantAccount  bool
		wantModelJob bool
	}{
		{"the owner", Identity{Account: "octo", Owner: "octo"}, true, true},
		{"the owner in another case", Identity{Account: "Octo", Owner: "octo"}, true, true},
		{"another account", Identity{Account: "work", Owner: "octo"}, false, false},
		{"no account configured", Identity{Owner: "octo"}, false, false},
		{"no owner", Identity{Account: "octo"}, false, false},
		{"neither set", Identity{}, false, false},
		{"the owner holding a GitHub token", Identity{Account: "octo", Owner: "octo", GitHubToken: true}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.id.CheckAccount(); (err == nil) != tt.wantAccount || (err != nil && !errors.Is(err, ErrIdentityMismatch)) {
				t.Errorf("CheckAccount = %v", err)
			}
			if err := tt.id.CheckModel(); (err == nil) != tt.wantModelJob || (err != nil && !errors.Is(err, ErrIdentityMismatch)) {
				t.Errorf("CheckModel = %v", err)
			}
		})
	}
}
