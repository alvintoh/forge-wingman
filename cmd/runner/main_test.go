package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestLoadEnv(t *testing.T) {
	full := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "p",
		"RUNNER_TEMP":          "/tmp/r",
		"GITHUB_RUN_ID":        "42",
		"GITHUB_RUN_ATTEMPT":   "2",
	}
	e, err := loadEnv(func(k string) string { return full[k] })
	if err != nil {
		t.Fatal(err)
	}
	if e.attemptID != "42-2" || e.project != "p" || e.tempDir != "/tmp/r" {
		t.Fatalf("env = %+v", e)
	}

	delete(full, "GITHUB_RUN_ATTEMPT")
	delete(full, "GOOGLE_CLOUD_PROJECT")
	_, err = loadEnv(func(k string) string { return full[k] })
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT GITHUB_RUN_ATTEMPT") {
		t.Fatalf("err = %v, want both missing names", err)
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
		if _, err := loadEnv(func(k string) string { return env[k] }); err == nil {
			t.Errorf("accepted attempt %q", attempt)
		}
	}
}

func TestExitCode(t *testing.T) {
	stopped := &runner.StopError{Outcome: runner.OutcomeStopped, Reason: runner.StopProjectionMissing, Err: errors.New("x")}
	for _, tt := range []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"a reported stop", stopped, 0},
		{"a stop whose summary was not reported", errors.Join(stopped, runner.ErrSummaryUnreported), 1},
		{"an error that is not a stop", errors.New("boom"), 1},
		{"a recorded run that failed", errRunFailed, 1},
	} {
		if got := exitCode(tt.err); got != tt.want {
			t.Errorf("%s: exit %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestSetupFailedReportsAValidSummary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "out")
	err := setupFailed(logger, path, errors.New("flag provided but not defined: -x"))
	if exitCode(err) != 0 {
		t.Fatalf("exit %d for a reported setup failure", exitCode(err))
	}
	out, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	raw, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "summary=")
	if !ok {
		t.Fatalf("GITHUB_OUTPUT = %q", out)
	}
	sum, perr := runner.ParseSummary(raw, "42-1", runner.Ticket{}, time.Now())
	if perr != nil || sum.StopReason != runner.StopSetup || sum.StopDetail == "" {
		t.Fatalf("summary %+v, err %v", sum, perr)
	}

	if exitCode(setupFailed(logger, "", errors.New("x"))) != 1 {
		t.Fatal("exit 0 with nowhere to report")
	}
}

func TestWriteOutputsRefusesAMultilineValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := writeOutputs(path, map[string]string{"summary": "a\nb=c"}); err == nil {
		t.Fatal("wrote a value spanning lines")
	}
	if err := writeOutputs(path, map[string]string{"summary": `{"a":"b\nc"}`}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `summary={"a":"b\nc"}`+"\n" {
		t.Fatalf("GITHUB_OUTPUT = %q", got)
	}
}

func TestWriteSummaryWithNoOutputIsUnreported(t *testing.T) {
	if err := writeSummary("", runner.SetupSummary(errors.New("x"), time.Now())); !errors.Is(err, errNoOutput) {
		t.Fatalf("err = %v, want errNoOutput", err)
	}
}

func TestSubcommandsRefuseARunIDBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": t.TempDir(), "GITHUB_RUN_ID": "42",
		"GITHUB_RUN_ATTEMPT": "1", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("Add a file."), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"ticket", "-run-id", "runs/x"},
		{"record", "-run-id", ""},
		{"pr-meta", "-run-id", "../x", "-template", "../../.github/pull_request_template.md"},
		{"seed", "-run-id", "a b", "-id", "ABC-12", "-title", "t", "-size", "S", "-body-file", body},
	} {
		err := run(context.Background(), logger, args, func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), "cannot name a record") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestTicketAndPRMetaRefuseAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": t.TempDir(), "GITHUB_RUN_ID": "42",
		"GITHUB_RUN_ATTEMPT": "1", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	for _, sub := range []string{"ticket", "pr-meta"} {
		err := run(context.Background(), logger, []string{sub, "-run-id", "run-1"}, func(k string) string { return env[k] })
		if !errors.Is(err, runner.ErrIdentityMismatch) {
			t.Errorf("%s: err = %v, want ErrIdentityMismatch", sub, err)
		}
	}
}

type recordReader map[string]runner.Record

func (r recordReader) GetRecord(_ context.Context, id string) (runner.Record, error) {
	rec, ok := r[id]
	if !ok {
		return runner.Record{}, runner.ErrRecordNotFound
	}
	return rec, nil
}

func TestTicketFromRecordFailsTheRunAndLogsWhy(t *testing.T) {
	records := recordReader{"ticketless": {RunID: "ticketless"}}
	for runID, reason := range map[string]runner.StopReason{
		"absent":     runner.StopRecordMissing,
		"ticketless": runner.StopTicketMissing,
	} {
		var log strings.Builder
		logger := slog.New(slog.NewTextHandler(&log, nil))
		_, err := ticketFromRecord(context.Background(), logger, records, runID)
		if !errors.Is(err, errRunFailed) || exitCode(err) != 1 {
			t.Errorf("%s: err = %v, exit %d; want errRunFailed, exit 1", runID, err, exitCode(err))
		}
		if !strings.Contains(log.String(), "ticketUnavailable") || !strings.Contains(log.String(), string(reason)) {
			t.Errorf("%s: log = %q, want ticketUnavailable naming %s", runID, log.String(), reason)
		}
	}
}

func TestRecordsClientNeedsAProject(t *testing.T) {
	if _, err := recordsClient(context.Background(), "", "run-1"); err == nil ||
		!strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("err = %v, want GOOGLE_CLOUD_PROJECT is not set", err)
	}
}

func TestWriteMultilineOutputUsesADelimiter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := writeMultilineOutput(path, "body", "a\nb"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	delim, ok := strings.CutPrefix(lines[0], "body<<")
	if !ok || len(lines) != 4 || lines[1] != "a" || lines[2] != "b" || lines[3] != delim {
		t.Fatalf("GITHUB_OUTPUT = %q", got)
	}
}
