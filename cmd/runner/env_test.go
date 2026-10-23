package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestLoadEnv(t *testing.T) {
	full := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "p",
		"RUNNER_TEMP":          "/tmp/r",
		"GITHUB_RUN_ID":        "42",
		"GITHUB_RUN_ATTEMPT":   "2",
	}
	required, known := requiredEnv("build")
	if !known {
		t.Fatal("build is not a known subcommand")
	}
	e, err := loadEnv(func(k string) string { return full[k] }, required...)
	if err != nil {
		t.Fatal(err)
	}
	if e.attemptID != "42-2" || e.project != "p" || e.tempDir != "/tmp/r" {
		t.Fatalf("env = %+v", e)
	}

	delete(full, "GITHUB_RUN_ATTEMPT")
	delete(full, "GOOGLE_CLOUD_PROJECT")
	_, err = loadEnv(func(k string) string { return full[k] }, required...)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT GITHUB_RUN_ATTEMPT") {
		t.Fatalf("err = %v, want both missing names", err)
	}
}

// TestLoadEnvNeedsOnlyTheProjectForAnOwnerCommand is the regression for an owner
// command failing locally: it reads no workflow-run identity, so it must run
// with the project alone.
func TestLoadEnvNeedsOnlyTheProjectForAnOwnerCommand(t *testing.T) {
	required, known := requiredEnv("plan-define")
	if !known {
		t.Fatal("plan-define is not a known subcommand")
	}
	only := map[string]string{"GOOGLE_CLOUD_PROJECT": "p"}
	if _, err := loadEnv(func(k string) string { return only[k] }, required...); err != nil {
		t.Fatalf("an owner command needs the project alone: %v", err)
	}
	_, err := loadEnv(func(string) string { return "" }, required...)
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("err = %v, want only the project named", err)
	}
}

// TestRecordNeedsTheRunsIdentity asserts record, which checks the attempt id
// against its own run, still refuses to start without that run's identity.
func TestRecordNeedsTheRunsIdentity(t *testing.T) {
	required, _ := requiredEnv("record")
	only := map[string]string{"GOOGLE_CLOUD_PROJECT": "p"}
	_, err := loadEnv(func(k string) string { return only[k] }, required...)
	if err == nil || !strings.Contains(err.Error(), "GITHUB_RUN_ID GITHUB_RUN_ATTEMPT") {
		t.Fatalf("err = %v, want the run id and attempt named", err)
	}
}

// TestHarnessesWireTheKeyAndHome asserts the harness resolves the plan's key
// from the environment and keeps its HOME under the job's temp directory, so a
// later round finds the earlier session, and that each plan's key is listed.
func TestHarnessesWireTheKeyAndHome(t *testing.T) {
	env := map[string]string{"COMMANDCODE_API_KEY": "k", "GEMINI_API_KEY": "g", "RUNNER_TEMP": "/tmp/r"}
	var omp runner.OmpHarness
	for _, h := range runner.Harnesses(func(k string) string { return env[k] }) {
		if h, ok := h.(runner.OmpHarness); ok {
			omp = h
		}
	}
	if omp.Getenv == nil || omp.Getenv("COMMANDCODE_API_KEY") != "k" {
		t.Errorf("the harness does not resolve the plan's key from the environment")
	}
	if omp.Home != filepath.Join("/tmp/r", "omp-home") {
		t.Errorf("home = %q, want it under RUNNER_TEMP", omp.Home)
	}
	if got := runner.HarnessSecrets(func(k string) string { return env[k] }); !slices.Equal(got, []string{"k", "g"}) {
		t.Errorf("secrets = %q, want each configured plan's key", got)
	}
}

func TestOwnAttemptID(t *testing.T) {
	for _, tt := range []struct {
		id   string
		want bool
	}{
		{"42-1", true},
		{"42-3", true},
		{"42-4", false},
		{"42-0", false},
		{"42-01", false},
		{"42-+1", false},
		{"42-12", false},
		{"", false},
		{"42-", false},
		{"43-1", false},
		{"42-1/../x", false},
		{"421-1", false},
	} {
		if got := ownAttemptID(tt.id, "42", 3); got != tt.want {
			t.Errorf("ownAttemptID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestLoadEnvRejectsAMalformedAttempt(t *testing.T) {
	for _, attempt := range []string{"0", "01", "x", "-1"} {
		env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": "/tmp/r", "GITHUB_RUN_ID": "42",
			"GITHUB_RUN_ATTEMPT": attempt}
		if _, err := loadEnv(func(k string) string { return env[k] },
			"GOOGLE_CLOUD_PROJECT", "RUNNER_TEMP", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"); err == nil {
			t.Errorf("accepted attempt %q", attempt)
		}
	}
}

func TestBuildAttemptKeepsOnlyThisRunsAttempts(t *testing.T) {
	e := env{runID: "42", attempt: 2, attemptID: "42-2"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for id, want := range map[string]string{"": "42-2", "42-1": "42-1", "43-1": "42-2"} {
		if got := buildAttempt(logger, id, e); got != want {
			t.Errorf("buildAttempt(%q) = %q, want %q", id, got, want)
		}
	}
}
