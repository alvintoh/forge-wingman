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

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
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

// TestHarnessesWireTheKeyAndHome asserts each harness takes the plan's key and
// keeps its HOME under the job's temp directory, so a later round finds the
// earlier session, and that the key they share is checked once.
func TestHarnessesWireTheKeyAndHome(t *testing.T) {
	env := map[string]string{"COMMANDCODE_API_KEY": "k", "RUNNER_TEMP": "/tmp/r"}
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
	if oc.Key != "k" || omp.Key != "k" {
		t.Errorf("keys = %q and %q, want the secret's", oc.Key, omp.Key)
	}
	if oc.Home != filepath.Join("/tmp/r", "opencode-home") || omp.Home != filepath.Join("/tmp/r", "omp-home") {
		t.Errorf("homes = %q and %q, want each under RUNNER_TEMP", oc.Home, omp.Home)
	}
	if got := runner.HarnessSecrets(func(k string) string { return env[k] }); !slices.Equal(got, []string{"k"}) {
		t.Errorf("secrets = %q, want the shared key once", got)
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

func TestPRTitlePrefersTheCommittedSubject(t *testing.T) {
	tk := runner.Ticket{ID: "ABC-1", Title: "[BE] Add the widget"}
	if got := prTitle(runner.Summary{}, tk); got != "ABC-1 [BE] Add the widget" {
		t.Fatalf("title without a committed subject = %q, want the ticket's subject", got)
	}
	sum := runner.Summary{CommitSubject: "feat(runner): ABC-1 add the widget"}
	if got := prTitle(sum, tk); got != "feat(runner): ABC-1 add the widget" {
		t.Fatalf("title = %q, want the committed subject", got)
	}
}

// prRecord is a run record as pr-meta reads it: the ticket, and none of the
// build's fields, which only the record job writes after the PR opens.
var prRecord = runner.Record{TicketID: "ABC-1", TicketTitle: "[BE] Add the widget", Size: "M", SizedBy: "test", TicketBody: "Add it."}

func prTemplate(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join("..", "..", runner.DefaultPRTemplate))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func encodedSummary(t *testing.T, attemptID string, now time.Time, edits ...func(*runner.Summary)) string {
	tk := prRecord.Ticket()
	sum := runner.Summary{
		Outcome: runner.OutcomeBuilt,
		Phase:   runner.PhaseCommit,
		Steps: []runner.Step{{Phase: runner.PhaseBuild, Round: 1, Model: "command-code/x",
			CompletionsObject: "completions/" + attemptID + "-build-1.jsonl", At: now.Add(-30 * time.Minute)}},
		EditedFiles:    []string{"widget.go", "extra.go"},
		OutOfPlanFiles: []string{"extra.go"},
		Branch:         runner.BranchName(tk.BranchSegment(), attemptID),
		CommitSubject:  "feat(runner): ABC-1 add the widget",
		PRSummary:      "Adds the widget the runner needs.",
		StartedAt:      now.Add(-time.Hour),
	}
	for _, edit := range edits {
		edit(&sum)
	}
	raw, err := sum.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRenderPRTakesTheTitleSummaryAndOutOfPlanFilesFromTheBuildSummary(t *testing.T) {
	now := time.Now()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum := buildSummary(logger, encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	title, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "")
	if err != nil {
		t.Fatal(err)
	}
	if title != "feat(runner): ABC-1 add the widget" {
		t.Fatalf("title = %q, want the build's committed subject", title)
	}
	if !strings.Contains(body, "\nAdds the widget the runner needs\\.\n") ||
		!strings.Contains(body, "**Edited outside the plan:** `extra.go`") {
		t.Fatalf("body lacks the build's summary or out-of-plan files:\n%s", body)
	}
}

func TestRenderPRFallsBackToTheTicketWithoutAUsableSummary(t *testing.T) {
	now := time.Now()
	for name, tt := range map[string]struct {
		raw     string
		wantLog bool
	}{
		"empty":           {"", false},
		"another attempt": {encodedSummary(t, "42-2", now), true},
		"malformed":       {"{", true},
	} {
		var log strings.Builder
		logger := slog.New(slog.NewTextHandler(&log, nil))
		sum := buildSummary(logger, tt.raw, "42-1", prRecord.Ticket(), now)
		title, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if title != "ABC-1 [BE] Add the widget" || !strings.Contains(body, "`ABC-1`: [BE] Add the widget") ||
			strings.Contains(body, "outside the plan") {
			t.Errorf("%s: title %q, want the ticket's subject and summary and no out-of-plan line:\n%s", name, title, body)
		}
		if got := strings.Contains(log.String(), "summaryRejected"); got != tt.wantLog {
			t.Errorf("%s: logged summaryRejected = %v, want %v: %q", name, got, tt.wantLog, log.String())
		}
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

func TestRefuseWorkflowPush(t *testing.T) {
	now := time.Now()
	stopped := func(s *runner.Summary) {
		s.Outcome, s.StopReason = runner.OutcomeStopped, runner.StopWorkflowChange
		s.StopDetail = "touches files under .github/workflows/, which the run cannot push: .github/workflows/ci.yml"
	}
	for name, tt := range map[string]struct {
		raw     string
		refused bool
	}{
		"a workflow change is refused": {encodedSummary(t, "42-1", now, stopped), true},
		"a built summary is not":       {encodedSummary(t, "42-1", now), false},
	} {
		t.Run(name, func(t *testing.T) {
			var log strings.Builder
			logger := slog.New(slog.NewTextHandler(&log, nil))
			err := refuseWorkflowPush(logger, buildSummary(logger, tt.raw, "42-1", prRecord.Ticket(), now))
			if tt.refused != errors.Is(err, errRunFailed) {
				t.Fatalf("err = %v, want refused %v", err, tt.refused)
			}
			if tt.refused != strings.Contains(log.String(), "msg=pushRefused") ||
				tt.refused != strings.Contains(log.String(), ".github/workflows/ci.yml") {
				t.Fatalf("log = %q", log.String())
			}
		})
	}
}

func TestAutoMerge(t *testing.T) {
	// run-0 falls in the review sample and run-abc-12 does not.
	const unsampled, sampled = "run-abc-12", "run-0"
	for name, tt := range map[string]struct {
		switchValue, runID, size, failedGate string
		edit                                 func(*runner.Summary)
		want                                 bool
	}{
		"a ready M run requests it":       {"on", unsampled, "M", "", nil, true},
		"a ready S run requests it":       {"on", unsampled, "S", "", nil, true},
		"an L run does not":               {"on", unsampled, "L", "", nil, false},
		"a sampled run does not":          {"on", sampled, "M", "", nil, false},
		"a draft does not":                {"on", unsampled, "M", "", func(s *runner.Summary) { s.Ready = false }, false},
		"an out-of-plan edit does not":    {"on", unsampled, "M", "", func(s *runner.Summary) { s.OutOfPlanFiles = []string{"x.go"} }, false},
		"a workflow change does not":      {"on", unsampled, "M", "", func(s *runner.Summary) { s.StopReason = runner.StopWorkflowChange }, false},
		"a failed check gate does not":    {"on", unsampled, "M", "test", nil, false},
		"the switch unset does not":       {"", unsampled, "M", "", nil, false},
		"any value but on does not":       {"true", unsampled, "M", "", nil, false},
		"the switch in capitals does not": {"ON", unsampled, "M", "", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
			if tt.edit != nil {
				tt.edit(&sum)
			}
			tk := prRecord.Ticket()
			tk.Size = tt.size
			if got := autoMerge(tt.switchValue, tt.runID, tk, sum, tt.failedGate); got != tt.want {
				t.Fatalf("autoMerge = %v, want %v", got, tt.want)
			}
		})
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

func TestRecordPlanStageNeedsADispatcherURI(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{identity: runner.Identity{Account: "octo", Owner: "octo"}}
	err := recordPlanStage(context.Background(), logger, e,
		[]string{"-run-id", "run-1", "-plan-file", filepath.Join(t.TempDir(), "plan.json")})
	if err == nil || !strings.Contains(err.Error(), "-dispatcher-uri") {
		t.Fatalf("err = %v, want a missing dispatcher URI refused", err)
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
		"if: github.ref == 'refs/heads/main' && inputs.stage == 'plan'",
		"record-plan-stage\n          -run-id \"$RUN_ID\" -plan-file \"$RUNNER_TEMP/plan.json\" -dispatcher-uri \"$DISPATCHER_RUN_URI\"",
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
