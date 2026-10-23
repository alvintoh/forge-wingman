package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// env is the workflow context every subcommand reads.
type env struct {
	project   string
	runID     string
	attempt   uint64
	attemptID string
	tempDir   string
	output    string
	secrets   []string
	harnesses []runner.Harness
	identity  runner.Identity
}

// requiredEnv names the variables a subcommand cannot run without. Only the two
// that act on a workflow run itself need its identity and temp directory; the
// owner commands need the project alone, so they run from a terminal with no
// Actions environment.
func requiredEnv(command string) ([]string, bool) {
	switch command {
	case "ticket", "pr-meta", "enable-provider", "reset-breaker", "peak-mode", "auto-merge", "requeue", "backfill-queued-at",
		"plan-define", "plan-optin", "plan-verdict", "plan-reply", "plan-list", "plans-refresh", "project-add", "project-remove":
		return []string{"GOOGLE_CLOUD_PROJECT"}, true
	case "record", "record-plan-stage":
		return []string{"GOOGLE_CLOUD_PROJECT", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}, true
	case "build", "plan-stage":
		return []string{"GOOGLE_CLOUD_PROJECT", "RUNNER_TEMP", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}, true
	}
	return nil, false
}

func loadEnv(getenv func(string) string, required ...string) (env, error) {
	e := env{
		project:  getenv("GOOGLE_CLOUD_PROJECT"),
		tempDir:  getenv("RUNNER_TEMP"),
		output:   getenv("GITHUB_OUTPUT"),
		identity: runner.IdentityFromEnv(getenv),
	}
	e.secrets = runner.HarnessSecrets(getenv)
	e.harnesses = runner.Harnesses(getenv)
	var missing []string
	for _, name := range required {
		if getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return env{}, fmt.Errorf("missing environment: %v", missing)
	}
	attempt := getenv("GITHUB_RUN_ATTEMPT")
	if attempt != "" {
		n, err := parseAttempt(attempt)
		if err != nil {
			return env{}, fmt.Errorf("GITHUB_RUN_ATTEMPT: %w", err)
		}
		e.attempt = n
	}
	e.runID = getenv("GITHUB_RUN_ID")
	e.attemptID = e.runID + "-" + attempt
	return e, nil
}

// buildAttempt is id when it names an attempt of this run, and the current
// attempt otherwise.
func buildAttempt(logger *slog.Logger, id string, e env) string {
	if id == "" {
		return e.attemptID
	}
	if !ownAttemptID(id, e.runID, e.attempt) {
		logger.Warn("attemptIDRejected", "length", len(id))
		return e.attemptID
	}
	return id
}

// ownAttemptID reports whether id names an attempt of this workflow run up to the current one.
func ownAttemptID(id, runID string, current uint64) bool {
	attempt, ok := strings.CutPrefix(id, runID+"-")
	if !ok {
		return false
	}
	n, err := parseAttempt(attempt)
	return err == nil && n <= current
}

// parseAttempt reads a run attempt: a positive decimal with no sign or leading zero.
func parseAttempt(s string) (uint64, error) {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return 0, fmt.Errorf("attempt %q is not a positive number", s)
	}
	return strconv.ParseUint(s, 10, 32)
}
