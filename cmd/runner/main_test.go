package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
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

// TestHarnessesWireTheKeyAndHome asserts each harness resolves the plan's key
// from the environment and keeps its HOME under the job's temp directory, so a
// later round finds the earlier session, and that each plan's key is listed.
func TestHarnessesWireTheKeyAndHome(t *testing.T) {
	env := map[string]string{"COMMANDCODE_API_KEY": "k", "GEMINI_API_KEY": "g", "RUNNER_TEMP": "/tmp/r"}
	var oc runner.OpencodeHarness
	var omp runner.OmpHarness
	for _, h := range runner.Harnesses(func(k string) string { return env[k] }) {
		switch h := h.(type) {
		case runner.OpencodeHarness:
			oc = h
		case runner.OmpHarness:
			omp = h
		}
	}
	if oc.Getenv == nil || oc.Getenv("COMMANDCODE_API_KEY") != "k" || omp.Getenv == nil || omp.Getenv("COMMANDCODE_API_KEY") != "k" {
		t.Errorf("harnesses do not resolve the plan's key from the environment")
	}
	if oc.Home != filepath.Join("/tmp/r", "opencode-home") || omp.Home != filepath.Join("/tmp/r", "omp-home") {
		t.Errorf("homes = %q and %q, want each under RUNNER_TEMP", oc.Home, omp.Home)
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
		{"a stop a stage reports as a failed run", fmt.Errorf("%w: %w", errRunFailed, stopped), 1},
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
	got := modelOutputs(runner.ModelLabels{})
	for _, k := range []string{"override_model", "override_review_models", "override_plan_models"} {
		if v, ok := got[k]; !ok || v != "" {
			t.Errorf("%s = %q (present %v), want an empty output", k, v, ok)
		}
	}
	if _, ok := got["ticket"]; ok {
		t.Error("modelOutputs carries a ticket output")
	}
}

func TestModelOutputsCarryTheModelsTheTicketNamed(t *testing.T) {
	got := modelOutputs(runner.ModelLabels{Build: "p/a", Review: "p/b", Plan: []string{"p/c", "p/d"}})
	if got["override_model"] != "p/a" || got["override_review_models"] != "p/b" || got["override_plan_models"] != "p/c,p/d" {
		t.Fatalf("outputs = %v", got)
	}
}

// TestWriteTicketWritesTheFileAndNoTicketOutput is the regression for the
// masked-output drop: the ticket must reach the model job as a file, never a
// job output.
func TestWriteTicketWritesTheFileAndNoTicketOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ticket.json")
	output := filepath.Join(dir, "output")
	tk := runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}
	if err := writeTicket(path, output, tk, runner.ModelLabels{Build: "p/a"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := runner.ParseTicket(string(raw)); err != nil || got.ID != "ABC-1" {
		t.Fatalf("ticket file %q parsed to %+v, err %v", raw, got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, err %v, want 0600", info.Mode(), err)
	}
	out, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "ticket=") {
		t.Errorf("GITHUB_OUTPUT carries a ticket output: %q", out)
	}
	if !strings.Contains(string(out), "override_model=p/a") {
		t.Errorf("GITHUB_OUTPUT lacks the model labels: %q", out)
	}
	if err := writeTicket("", output, tk, runner.ModelLabels{}); err == nil {
		t.Error("writeTicket wrote with no -out path")
	}
}

func TestReadTicketDistinguishesAMissingHandOffFromAnUnbuildableTicket(t *testing.T) {
	dir := t.TempDir()
	if _, err := readTicket(filepath.Join(dir, "absent.json")); !errors.Is(err, errTicketNotDelivered) {
		t.Errorf("absent file: err = %v, want errTicketNotDelivered", err)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTicket(empty); !errors.Is(err, errTicketNotDelivered) {
		t.Errorf("empty file: err = %v, want errTicketNotDelivered", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTicket(bad); !errors.Is(err, runner.ErrTicketInvalid) {
		t.Errorf("unbuildable file: err = %v, want ErrTicketInvalid", err)
	}
	good := filepath.Join(dir, "good.json")
	v, err := (runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte(v), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readTicket(good); err != nil || got.ID != "ABC-1" {
		t.Fatalf("good file: ticket %+v, err %v", got, err)
	}
}

func TestBuildStopsAsTicketNotDeliveredWithoutATicketFile(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	output := filepath.Join(dir, "out")
	err := build(context.Background(), logger, env{output: output, attemptID: "42-1"},
		[]string{"-model", "command-code/p/m", "-ticket-file", filepath.Join(dir, "absent.json")})
	if exitCode(err) != 0 {
		t.Fatalf("exit %d, err %v; want a reported stop", exitCode(err), err)
	}
	out, rerr := os.ReadFile(output)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var raw string
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "summary="); ok {
			raw = v
		}
	}
	sum, perr := runner.ParseSummary(raw, "42-1", runner.Ticket{}, time.Now())
	if perr != nil || sum.StopReason != runner.StopTicketNotDelivered {
		t.Fatalf("summary %+v, err %v; want ticket-not-delivered", sum, perr)
	}
}

func TestWriteTicketNeedsAnOutPath(t *testing.T) {
	output := filepath.Join(t.TempDir(), "out")
	if err := writeTicket("", output, runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}, runner.ModelLabels{}); err == nil {
		t.Fatal("wrote a ticket with no -out path")
	}
}

func TestTicketNotDeliveredReportsAStoppedSummary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "out")
	err := ticketNotDelivered(logger, path, errors.New("no ticket file"))
	if exitCode(err) != 0 {
		t.Fatalf("exit %d for a reported stop", exitCode(err))
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
	if perr != nil || sum.Outcome != runner.OutcomeStopped || sum.StopReason != runner.StopTicketNotDelivered {
		t.Fatalf("summary %+v, err %v", sum, perr)
	}

	if exitCode(ticketNotDelivered(logger, "", errors.New("x"))) != 1 {
		t.Fatal("exit 0 with nowhere to report")
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
		"":              {runner.DefaultPlanModel()},
		"  ":            {runner.DefaultPlanModel()},
		"a/b,":          {"a/b", ""},
	} {
		if got := splitModels(in, []string{runner.DefaultPlanModel()}); !slices.Equal(got, want) {
			t.Errorf("splitModels(%q, default) = %q, want %q", in, got, want)
		}
	}
	if got := splitModels("", runner.DefaultReviewModels()); !slices.Equal(got, runner.DefaultReviewModels()) {
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

func TestCheckRunnerRefusesAnImageNotPinnedByDigest(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "golangci-lint"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	if _, err := checkRunner(ctx, "golang:1.27.1", t.TempDir(), "."); err == nil {
		t.Fatal("checkRunner accepted an image pinned by tag alone")
	}
	if _, err := checkRunner(ctx, "golang:1.27.1@sha256:"+strings.Repeat("a", 64), t.TempDir(), "."); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRunnerRunsOnTheHostWithNoImage(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	checks, err := checkRunner(context.Background(), "", t.TempDir(), t.TempDir())
	if err != nil || checks == nil {
		t.Fatalf("checkRunner with no image = %v, %v; want the host runner and no lookups", checks != nil, err)
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

func TestRunWorkflowPassesTheBuildSummaryToPRMeta(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := strings.Cut(string(yml), "\n  pr:\n")
	_, job, _ = strings.Cut(job, "\n  pr-meta:\n")
	for _, want := range []string{
		"ATTEMPT_ID: ${{ needs.model.outputs.attempt_id }}",
		"SUMMARY: ${{ needs.model.outputs.summary }}",
		`-attempt-id "$ATTEMPT_ID" -summary "$SUMMARY"`,
	} {
		if !strings.Contains(job, want) {
			t.Errorf("run.yml's pr-meta job lacks %q", want)
		}
	}
}

func TestRunWorkflowMarksAutoMergeEligibilityOnlyOnPRMetasDecision(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"AUTO_MERGE_SWITCH: ${{ vars.WINGMAN_AUTO_MERGE }}",
		`-auto-merge-switch "$AUTO_MERGE_SWITCH"`,
		"FRONT_END_PATHS: ${{ vars.WINGMAN_FRONT_END_PATHS }}",
		`-front-end-paths "$FRONT_END_PATHS"`,
		"if: needs.pr-meta.outputs.auto_merge == 'true'\n",
		`gh pr edit "$URL" --add-label ` + autoMergeEligibleLabel,
		"auto_merge: ${{ steps.eligible.outputs.labelled }}",
		"AUTO_MERGE: ${{ needs.pr.outputs.auto_merge == 'true' }}",
		`-auto-merge="$AUTO_MERGE"`,
	} {
		if !strings.Contains(string(yml), want) {
			t.Errorf("run.yml lacks %q", want)
		}
	}
	if strings.Contains(string(yml), "gh pr merge") {
		t.Error("run.yml requests auto-merge itself; only a clean review may")
	}
}

func TestWithdrawWorkflowDisablesAutoMergeOnAPushToARunsBranch(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "automerge-withdraw.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pull_request_target:\n    types: [synchronize]",
		"startsWith(github.event.pull_request.head.ref, 'wingman/')",
		`requested="$(gh pr view "$PR" --json autoMergeRequest`,
		`gh pr merge "$PR" --disable-auto`,
		`gh pr edit "$PR" --remove-label wingman:auto-merge-eligible`,
	} {
		if !strings.Contains(string(yml), want) {
			t.Errorf("automerge-withdraw.yml lacks %q", want)
		}
	}
	if strings.Index(string(yml), "--remove-label") > strings.Index(string(yml), "auto-merge is not requested") {
		t.Error("automerge-withdraw.yml can exit before it removes the eligibility label")
	}
	if strings.Contains(string(yml), "actions/checkout") {
		t.Error("automerge-withdraw.yml checks out the branch it runs for")
	}
}

func TestResetBreakerRefusesAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	err := run(context.Background(), logger, []string{"reset-breaker"}, func(k string) string { return env[k] })
	if !errors.Is(err, runner.ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
	}
}

type fakeNotices struct {
	raised []dispatcher.Notice
	err    error
}

func (n *fakeNotices) Raise(_ context.Context, notice dispatcher.Notice, _ time.Time) error {
	if n.err != nil {
		return n.err
	}
	n.raised = append(n.raised, notice)
	return nil
}

type fakeBreaker struct {
	tripped []string
	err     error
}

func (b *fakeBreaker) Trip(_ context.Context, runID, class string, _ time.Time) error {
	if b.err != nil {
		return b.err
	}
	b.tripped = append(b.tripped, runID+" "+class)
	return nil
}

func TestRaiseSystemicNoticesAndTripsByClass(t *testing.T) {
	const runURL = "https://github.com/o/r/actions/runs/7"
	for name, tt := range map[string]struct {
		reason        runner.StopReason
		raised, trips bool
	}{
		"an identity mismatch notifies and trips": {runner.StopIdentityMismatch, true, true},
		"a model outage notifies only":            {runner.StopModelUnavailable, true, false},
		"an agent failure does neither":           {runner.StopAgentExit, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			notices, breaker := &fakeNotices{}, &fakeBreaker{}
			rec := runner.Record{RunID: "ABC-1", StopReason: tt.reason, RunURL: runURL, TicketBody: "secret body"}
			raiseSystemic(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), notices, breaker, rec, "42-1", time.Now())
			if tt.raised != (len(notices.raised) == 1) || tt.trips != (len(breaker.tripped) == 1) {
				t.Fatalf("raised %v, tripped %v", notices.raised, breaker.tripped)
			}
			if tt.raised && notices.raised[0] != (dispatcher.Notice{ID: "ABC-1-42-1", Class: string(tt.reason), Link: runURL}) {
				t.Fatalf("notice = %+v", notices.raised[0])
			}
		})
	}
}

func TestRaiseSystemicTripsTheBreakerWhenTheNoticeFails(t *testing.T) {
	var log strings.Builder
	breaker := &fakeBreaker{}
	rec := runner.Record{RunID: "ABC-1", StopReason: runner.StopCredentialAbsent}
	raiseSystemic(context.Background(), slog.New(slog.NewTextHandler(&log, nil)), &fakeNotices{err: errors.New("firestore down")}, breaker, rec, "42-1", time.Now())
	if len(breaker.tripped) != 1 || !strings.Contains(log.String(), "noticeNotRaised") {
		t.Fatalf("tripped %v, log %q", breaker.tripped, log.String())
	}
}

func TestRaiseSystemicLogsABreakerItCouldNotTrip(t *testing.T) {
	var log strings.Builder
	rec := runner.Record{RunID: "ABC-1", StopReason: runner.StopAllowanceExhausted}
	raiseSystemic(context.Background(), slog.New(slog.NewTextHandler(&log, nil)), &fakeNotices{}, &fakeBreaker{err: errors.New("firestore down")}, rec, "42-1", time.Now())
	if !strings.Contains(log.String(), "breakerNotTripped") || strings.Contains(log.String(), "msg=breakerTripped") {
		t.Fatalf("log %q, want the failed trip reported and no trip claimed", log.String())
	}
}

func TestRaiseSystemicKeysANoticeByTheRunAloneForAnUnsafeAttemptID(t *testing.T) {
	notices := &fakeNotices{}
	rec := runner.Record{RunID: "ABC-1", StopReason: runner.StopIdentityMismatch}
	raiseSystemic(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), notices, &fakeBreaker{}, rec, "42/../1", time.Now())
	if len(notices.raised) != 1 || notices.raised[0].ID != "ABC-1" {
		t.Fatalf("raised %+v, want the run id alone", notices.raised)
	}
}

// failingRecords holds records it reads but cannot write.
type failingRecords struct{ recordReader }

func (failingRecords) PutRecord(context.Context, string, runner.Record) error {
	return errors.New("firestore refused the write")
}

type noLedger struct{}

func (noLedger) Settle(context.Context, string) error { return nil }

func TestFinalizeRaisesASystemicStopWhoseRecordCannotBeWritten(t *testing.T) {
	notices, breaker := &fakeNotices{}, &fakeBreaker{}
	env := map[string]string{"WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	in := runner.FinalizeInput{RunID: "ABC-1", AttemptID: "42-1", Identity: runner.IdentityFromEnv(func(k string) string { return env[k] })}
	_, err := finalize(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		failingRecords{recordReader{"ABC-1": {RunID: "ABC-1"}}}, noLedger{}, nil, notices, breaker, in, time.Now())
	if err == nil {
		t.Fatal("the failed write was dropped")
	}
	if len(notices.raised) != 1 || len(breaker.tripped) != 1 {
		t.Fatalf("raised %v, tripped %v, want the identity stop raised and tripped", notices.raised, breaker.tripped)
	}
}

func TestPlanStageNeedsAnOutPath(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := planStage(context.Background(), logger, env{}, nil); err == nil ||
		!strings.Contains(err.Error(), "-out") {
		t.Fatalf("err = %v, want a missing -out path refused before any model call", err)
	}
}

func TestValidRunURL(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		wantErr   bool
	}{
		{"the run's own URL", "https://github.com/octo/repo/actions/runs/7", false},
		{"empty", "", true},
		{"not https", "http://github.com/octo/repo/actions/runs/7", true},
		{"relative", "/octo/repo/actions/runs/7", true},
		{"no host", "https:///actions/runs/7", true},
		{"https but not an actions run", "https://example.test/x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validRunURL(tc.url); (err != nil) != tc.wantErr {
				t.Fatalf("validRunURL(%q) = %v, want an error = %v", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestRecordPlanStageNeedsADispatcherURI(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{identity: runner.Identity{Account: "octo", Owner: "octo"}}
	err := recordPlanStage(context.Background(), logger, e,
		[]string{"-run-id", "run-1", "-plan-file", filepath.Join(t.TempDir(), "plan.json"), "-plan-stage-result", jobSuccess})
	if err == nil || !strings.Contains(err.Error(), "-dispatcher-uri") {
		t.Fatalf("err = %v, want a missing dispatcher URI refused", err)
	}
}

func TestRecordPlanStageNeedsTheStageResult(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{identity: runner.Identity{Account: "octo", Owner: "octo"}}
	err := recordPlanStage(context.Background(), logger, e, []string{"-run-id", "run-1"})
	if err == nil || !strings.Contains(err.Error(), "-plan-stage-result") {
		t.Fatalf("err = %v, want a missing plan-stage result refused", err)
	}
}

// TestRecordPlanStageStopsARunWhenTheStageFailed pins the branch a stopped plan
// stage needs: the run is ended rather than left holding its plan reservation.
// No run row exists here, so the stop's own error naming the run is what shows
// the failed result reached it, rather than the path that reads a missing plan.
func TestRecordPlanStageStopsARunWhenTheStageFailed(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{identity: runner.Identity{Account: "octo", Owner: "octo"}, project: "forge-wingman-test"}
	err := recordPlanStage(context.Background(), logger, e, []string{
		"-run-id", "10000000001", "-plan-file", filepath.Join(t.TempDir(), "plan.json"),
		"-dispatcher-uri", "https://example.test", "-plan-stage-result", "failure",
	})
	if err == nil || !strings.Contains(err.Error(), "stopping run 10000000001") {
		t.Fatalf("err = %v, want the failed stage to reach the stop", err)
	}
}

// TestRecordPlanStageStopsARunWithNoRecordablePlan pins the other half of the
// wedge: the stage reported success, so this job is what records its artifact,
// and one it cannot read or re-check must not leave the run queued either.
func TestRecordPlanStageStopsARunWithNoRecordablePlan(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{identity: runner.Identity{Account: "octo", Owner: "octo"}, project: "forge-wingman-test"}
	for _, tc := range []struct{ name, runID, content string }{
		{"an artifact that was never written", "10000000002", ""},
		{"an artifact that does not re-check", "10000000003", "not a plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := recordPlanStage(context.Background(), logger, e, []string{
				"-run-id", tc.runID, "-plan-file", path,
				"-dispatcher-uri", "https://example.test", "-plan-stage-result", jobSuccess,
			})
			if err == nil || !strings.Contains(err.Error(), "stopping run "+tc.runID) {
				t.Fatalf("err = %v, want the run stopped rather than left queued", err)
			}
		})
	}
}

func TestRecordPlanStageRefusesAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{identity: runner.Identity{Account: "work-account", Owner: "octo"}}
	err := recordPlanStage(context.Background(), logger, e,
		[]string{"-run-id", "run-1", "-plan-file", "x", "-dispatcher-uri", "https://example.test"})
	if !errors.Is(err, runner.ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
	}
}

// TestRunWorkflowGatesTheStageOnTheDispatcher is the regression for the
// two-stage dispatch's dormancy: the stage gate is inert until the dispatcher
// stage=plan, and then the build-side jobs are skipped rather than run against
// a plan-stage branch.
func TestRunWorkflowGatesTheStageOnTheDispatcher(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(yml)
	if want := "      stage:\n        description: the stage to run — plan or build\n        type: string\n        default: build\n        required: true\n"; !strings.Contains(s, want) {
		t.Errorf("run.yml lacks the stage input:\n%q", want)
	}
	for _, want := range []string{
		"stage: ${{ inputs.stage }}",
		"if: inputs.stage != 'plan' && needs.model.outputs.changed == 'true'\n",
		"if: always() && inputs.stage != 'plan' && github.ref == 'refs/heads/main'\n",
		"  record-plan-stage:\n",
		"if: always() && github.ref == 'refs/heads/main' && inputs.stage == 'plan'",
		"record-plan-stage\n          -run-id \"$RUN_ID\" -plan-file \"$RUNNER_TEMP/plan.json\" -dispatcher-uri \"$DISPATCHER_RUN_URI\"",
		"-run-url \"$RUN_URL\" -plan-stage-result \"${{ needs.model.result }}\"",
		"if: needs.model.result == 'success'\n        with:\n          name: plan\n",
		"      - name: Record the plan stage\n        if: always()\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("run.yml lacks %q", want)
		}
	}
	if n := strings.Count(s, "inputs.stage != 'plan'"); n != 4 {
		t.Errorf("run.yml gates %d jobs on the stage, want 4 (check, pr-meta, pr, record)", n)
	}
}

func TestModelWorkflowRunsThePlanStageForThePlanStream(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "model.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(yml)
	for _, want := range []string{
		"      stage:\n        type: string\n        required: true\n",
		"if: inputs.stage != 'plan'\n",
		`"$RUNNER_TEMP/runner" plan-stage -plan-models "$PLAN_MODELS" -ticket-file "$RUNNER_TEMP/ticket.json" -out "$RUNNER_TEMP/plan.json"`,
		"name: plan\n          path: ${{ runner.temp }}/plan.json\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("model.yml lacks %q", want)
		}
	}
}

func TestRequeueRefusesAMalformedRunIDBeforeOpeningFirestore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	for _, id := range []string{"", "runs/x", "../x"} {
		err := run(context.Background(), logger, []string{"requeue", "-run-id", id}, func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), "cannot name a record") {
			t.Errorf("id %q: err = %v, want refused before opening Firestore", id, err)
		}
	}
}

func TestRequeueRefusesAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	err := run(context.Background(), logger, []string{"requeue", "-run-id", "run-1"}, func(k string) string { return env[k] })
	if !errors.Is(err, runner.ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
	}
}

func TestBackfillQueuedAtRefusesAMismatchedIdentity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	err := run(context.Background(), logger, []string{"backfill-queued-at", "-dry-run"}, func(k string) string { return env[k] })
	if !errors.Is(err, runner.ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
	}
}

func TestBackfillQueuedAtStampsOnlyWithoutDryRun(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "backfill-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ref := client.Collection("runs").Doc(runID)
	t.Cleanup(func() {
		_, _ = ref.Delete(context.Background())
		_ = client.Close()
	})
	if _, err := ref.Set(ctx, map[string]any{"state": "queued", "started_at": time.Now()}); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		args    []string
		stamped bool
	}{{[]string{"backfill-queued-at", "-dry-run"}, false}, {[]string{"backfill-queued-at"}, true}} {
		if err := run(ctx, logger, tc.args, func(k string) string { return env[k] }); err != nil {
			t.Fatal(err)
		}
		snap, err := ref.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, has := snap.Data()["queued_at"]; has != tc.stamped {
			t.Fatalf("%v: queued_at present = %v, want %v", tc.args, has, tc.stamped)
		}
	}
}

// recordPRFailure seeds a claimed run whose build finished and whose PR job
// stopped for prStopReason, runs record against it, and returns the record
// job's error beside the run's stored row.
func recordPRFailure(t *testing.T, prior map[string]any, prStopReason string) (map[string]any, error) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "record-conflict-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("runs").Doc(runID).Delete(context.Background())
		_ = client.Close()
	})
	ticket := runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}
	if err := store.NewRecords(client).PutRecord(ctx, runID, runner.NewRecord(runID, ticket, time.Now())); err != nil {
		t.Fatal(err)
	}
	row := map[string]any{"state": "claimed", "claimed_at": time.Now()}
	maps.Copy(row, prior)
	if _, err := client.Collection("runs").Doc(runID).Set(ctx, row, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	summary, err := runner.Summary{
		Outcome: runner.OutcomeBuilt, Phase: runner.PhaseCommit, StartedAt: time.Now().Add(-time.Hour),
		Branch: runner.BranchName(ticket.BranchSegment(), "42-1"),
		Steps: []runner.Step{{Phase: runner.PhaseBuild, Round: 1, Model: "p/m", Tokens: runner.Usage{Cost: 0.5, Steps: 1},
			CompletionsObject: "completions/42-1-build-1.jsonl", At: time.Now().Add(-30 * time.Minute)}},
	}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	getenv := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recordErr := run(ctx, logger, []string{"record", "-run-id", runID, "-summary", summary,
		"-run-result", jobSuccess, "-pr-result", "failure", "-pr-stop-reason", prStopReason},
		func(k string) string { return getenv[k] })
	snap, err := client.Collection("runs").Doc(runID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return snap.Data(), recordErr
}

func TestRecordRequeuesARunWhoseRebaseConflicted(t *testing.T) {
	row, err := recordPRFailure(t, nil, string(runner.StopRebaseConflict))
	if err != nil {
		t.Fatalf("err = %v, want a requeued run not reported as a failed one", err)
	}
	if row["state"] != "queued" || row["attempt"] != int64(1) {
		t.Fatalf("state/attempt = %v/%v, want queued/1", row["state"], row["attempt"])
	}
	if row["prior_settled_provider_cost_micros"] != int64(500_000) {
		t.Fatalf("prior settled cost = %v, want the failed attempt's 500000 carried", row["prior_settled_provider_cost_micros"])
	}
	if _, ok := row["outcome"]; ok {
		t.Fatalf("outcome = %v survived the requeue", row["outcome"])
	}
}

func TestRecordFailsARunWhoseRebaseConflictedAfterItsRetry(t *testing.T) {
	row, err := recordPRFailure(t, map[string]any{"attempt": int64(1)}, string(runner.StopRebaseConflict))
	if !errors.Is(err, errRunFailed) {
		t.Fatalf("err = %v, want errRunFailed", err)
	}
	if row["state"] != "claimed" || row["outcome"] != string(runner.OutcomeInfraFailure) || row["stop_reason"] != string(runner.StopRebaseConflict) {
		t.Fatalf("state/outcome/stop_reason = %v/%v/%v, want claimed/infra-failure/rebase-conflict", row["state"], row["outcome"], row["stop_reason"])
	}
}

func TestRecordLeavesAFailedPRJobThatIsNotAConflictToFail(t *testing.T) {
	row, err := recordPRFailure(t, nil, "")
	if !errors.Is(err, errRunFailed) {
		t.Fatalf("err = %v, want errRunFailed", err)
	}
	if row["state"] != "claimed" || row["stop_reason"] != string(runner.StopPRJob) {
		t.Fatalf("state/stop_reason = %v/%v, want claimed/%s", row["state"], row["stop_reason"], runner.StopPRJob)
	}
}

func TestAgentRunnerRefusesAnImageNotPinnedByDigest(t *testing.T) {
	if _, _, err := agentRunner(context.Background(), "golang:1.27.1", ".", nil); err == nil {
		t.Fatal("agentRunner accepted an image pinned by tag alone")
	}
}

func TestAgentRunnerRunsAgentsOnTheHostWithNoImage(t *testing.T) {
	harnesses := []runner.Harness{runner.OmpHarness{Bin: "omp"}}
	got, probe, err := agentRunner(context.Background(), "", t.TempDir(), harnesses)
	if err != nil || probe != nil || len(got) != 1 || got[0].(runner.OmpHarness).Container.Image != "" {
		t.Fatalf("agentRunner with no image = %v, probe %v, %v; want the harnesses unchanged and no probe", got, probe != nil, err)
	}
}

func TestAgentRunnerContainsHarnessesAndHandsBackTheProbe(t *testing.T) {
	image := "golang:1.27.1@sha256:" + strings.Repeat("a", 64)
	got, probe, err := agentRunner(context.Background(), image, ".", []runner.Harness{runner.OmpHarness{Bin: "omp"}})
	if err != nil || probe == nil || got[0].(runner.OmpHarness).Container.Image != image {
		t.Fatalf("agentRunner = %v, probe %v, %v; want the harness in the image's container and a probe", got, probe != nil, err)
	}
}

func TestAgentProbeNeedsAnImageAndACommand(t *testing.T) {
	for _, args := range [][]string{{"--", "sh"}, {"-agent-image", "x@sha256:" + strings.Repeat("a", 64)}} {
		if err := agentProbe(context.Background(), io.Discard, io.Discard, args); err == nil {
			t.Errorf("agentProbe %v succeeded, want a usage error", args)
		}
	}
}
