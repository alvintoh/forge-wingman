package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func planEnv(account string) func(string) string {
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": "/tmp", "GITHUB_RUN_ID": "42",
		"GITHUB_RUN_ATTEMPT": "1", "WINGMAN_ACCOUNT": account, "GITHUB_REPOSITORY_OWNER": "octo"}
	return func(k string) string { return env[k] }
}

func TestPlanCommandsRefuseInvalidInputBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, tt := range map[string]struct {
		args []string
		want string
	}{
		"a reply with no verdict": {[]string{"plan-reply", "-provider", "opencode", "-reply", "fine"}, "is not allowed, restricted or unconfirmed"},
		"a verdict with an impossible date": {[]string{"plan-verdict", "-provider", "opencode", "-verdict", "allowed",
			"-wording", "w", "-source", "https://v.example/terms", "-read-on", "2026-02-30"}, "yyyy-mm-dd"},
		"a definition with a malformed provider": {[]string{"plan-define", "-provider", "Open Code", "-name", "n", "-price-usd", "15"}, "is not a provider name"},
		"a definition with no price":             {[]string{"plan-define", "-provider", "opencode", "-name", "n"}, "not a positive amount"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), logger, tt.args, planEnv("octo"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestPlanCommandsRefuseAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, args := range [][]string{{"plan-list"}, {"plan-verdict", "-provider", "opencode"}} {
		err := run(context.Background(), logger, args, planEnv("work-account"))
		if !errors.Is(err, runner.ErrIdentityMismatch) {
			t.Errorf("%v: err = %v, want ErrIdentityMismatch", args, err)
		}
	}
}

func TestOpenPlansNeedsNoProviderForAListing(t *testing.T) {
	e := env{identity: runner.IdentityFromEnv(planEnv("octo"))}
	_, _, err := openPlans(context.Background(), e, "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("err = %v, want it to get past the provider check and stop at the missing project", err)
	}
}
