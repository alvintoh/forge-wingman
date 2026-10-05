package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

// TestHarnessesWireTheKeyAndHome asserts the Command Code harness takes the
// secret's key and keeps its HOME under the job's temp directory, so a later
// round finds the earlier session.
func TestHarnessesWireTheKeyAndHome(t *testing.T) {
	env := map[string]string{"COMMANDCODE_API_KEY": "k", "RUNNER_TEMP": "/tmp/r"}
	var cc runner.CommandCodeHarness
	for _, h := range harnesses(func(k string) string { return env[k] }) {
		if c, ok := h.(runner.CommandCodeHarness); ok {
			cc = c
		}
	}
	if cc.Key != "k" {
		t.Errorf("key = %q, want the secret's", cc.Key)
	}
	if cc.Home != filepath.Join("/tmp/r", "commandcode-home") {
		t.Errorf("home = %q, want it under RUNNER_TEMP", cc.Home)
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
	for _, args := range [][]string{
		{"ticket", "-run-id", "runs/x"},
		{"record", "-run-id", ""},
		{"pr-meta", "-run-id", "../x", "-template", "../../.github/pull_request_template.md"},
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

func TestEnableProviderRefusesAMalformedProviderBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": t.TempDir(), "GITHUB_RUN_ID": "42",
		"GITHUB_RUN_ATTEMPT": "1", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	for _, provider := range []string{"", "CLIAgent", "command code", "command-code/x"} {
		err := run(context.Background(), logger, []string{"enable-provider", "-provider", provider},
			func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), "is not a provider name") {
			t.Errorf("provider %q: err = %v, want refused before opening Firestore", provider, err)
		}
	}
}

func TestEnableProviderRefusesAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": t.TempDir(), "GITHUB_RUN_ID": "42",
		"GITHUB_RUN_ATTEMPT": "1", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	err := run(context.Background(), logger, []string{"enable-provider", "-provider", "command-code"},
		func(k string) string { return env[k] })
	if !errors.Is(err, runner.ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
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

func TestRunRecordFailsTheRunAndLogsWhy(t *testing.T) {
	records := recordReader{"ticketless": {RunID: "ticketless"}}
	for runID, reason := range map[string]runner.StopReason{
		"absent":     runner.StopRecordMissing,
		"ticketless": runner.StopTicketMissing,
	} {
		var log strings.Builder
		logger := slog.New(slog.NewTextHandler(&log, nil))
		_, err := runRecord(context.Background(), logger, records, runID)
		if !errors.Is(err, errRunFailed) || exitCode(err) != 1 {
			t.Errorf("%s: err = %v, exit %d; want errRunFailed, exit 1", runID, err, exitCode(err))
		}
		if !strings.Contains(log.String(), "ticketUnavailable") || !strings.Contains(log.String(), string(reason)) {
			t.Errorf("%s: log = %q, want ticketUnavailable naming %s", runID, log.String(), reason)
		}
	}
}

func TestModelOutputsAreEmptyForATicketThatNamedNoModel(t *testing.T) {
	got := modelOutputs(runner.ModelLabels{}, "t")
	for _, k := range []string{"override_model", "override_review_models", "override_plan_models"} {
		if v, ok := got[k]; !ok || v != "" {
			t.Errorf("%s = %q (present %v), want an empty output", k, v, ok)
		}
	}
	if got["ticket"] != "t" {
		t.Errorf("ticket = %q", got["ticket"])
	}
}

func TestModelOutputsCarryTheModelsTheTicketNamed(t *testing.T) {
	got := modelOutputs(runner.ModelLabels{Build: "p/a", Review: "p/b", Plan: []string{"p/c", "p/d"}}, "t")
	if got["override_model"] != "p/a" || got["override_review_models"] != "p/b" || got["override_plan_models"] != "p/c,p/d" {
		t.Fatalf("outputs = %v", got)
	}
}

func TestRunRecordReadsTheModelsTheTicketNamed(t *testing.T) {
	rec := runner.Record{RunID: "named", TicketID: "T-1", TicketTitle: "t", TicketBody: "b", Size: "S", SizedBy: "linear-label",
		ModelLabels: runner.ModelLabels{Build: "p/a"}}
	got, err := runRecord(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), recordReader{"named": rec}, "named")
	if err != nil || got.ModelLabels.Build != "p/a" {
		t.Fatalf("record %+v, err %v, want the record with its model labels", got, err)
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

func TestSplitModels(t *testing.T) {
	for in, want := range map[string][]string{
		"a/b":           {"a/b"},
		"a/b, c/d ,e/f": {"a/b", "c/d", "e/f"},
		"":              {runner.DefaultPlanModel},
		"  ":            {runner.DefaultPlanModel},
		"a/b,":          {"a/b", ""},
	} {
		if got := splitModels(in, runner.DefaultPlanModel); !slices.Equal(got, want) {
			t.Errorf("splitModels(%q, default) = %q, want %q", in, got, want)
		}
	}
	if got := splitModels("", runner.DefaultReviewModel); !slices.Equal(got, []string{runner.DefaultReviewModel}) {
		t.Errorf("splitModels(\"\", review default) = %q, want the review default", got)
	}
}

func TestSmokeModelsAreTheFirstAndLast(t *testing.T) {
	for _, tt := range []struct{ in, want []string }{
		{[]string{"a/1"}, []string{"a/1"}},
		{[]string{"a/1", "b/2"}, []string{"a/1", "b/2"}},
		{[]string{"a/1", "b/2", "c/3"}, []string{"a/1", "c/3"}},
	} {
		if got := smokeModels(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("smokeModels(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPlanSmokeRefusesAnInvalidModelList(t *testing.T) {
	err := planSmoke(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(string) string { return "" }, []string{"-plan-models", "not a model"})
	if err == nil {
		t.Fatal("plan-smoke accepted a malformed model")
	}
}

func TestAppendFileAppendsAndCreatesOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	for _, line := range []string{"a\n", "b\n"} {
		if err := appendFile(path, line); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "a\nb\n" {
		t.Fatalf("file = %q, err %v, want both appends", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, err %v, want 0600", info.Mode(), err)
	}
	if err := appendFile(t.TempDir(), "x"); err == nil {
		t.Fatal("appendFile wrote to a directory")
	}
}

func TestPRTitlePrefersTheCommittedSubject(t *testing.T) {
	rec := runner.Record{TicketID: "ABC-1", TicketTitle: "[BE] Add the widget"}
	if got := prTitle(rec); got != "ABC-1 [BE] Add the widget" {
		t.Fatalf("title without a committed subject = %q, want the ticket's subject", got)
	}
	rec.CommitSubject = "feat(runner): ABC-1 add the widget"
	if got := prTitle(rec); got != "feat(runner): ABC-1 add the widget" {
		t.Fatalf("title = %q, want the committed subject", got)
	}
}
