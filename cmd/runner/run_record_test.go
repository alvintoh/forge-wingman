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
