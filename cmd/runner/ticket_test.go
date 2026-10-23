package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

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

func TestWriteTicketNeedsAnOutPath(t *testing.T) {
	output := filepath.Join(t.TempDir(), "out")
	if err := writeTicket("", output, runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}, runner.ModelLabels{}); err == nil {
		t.Fatal("wrote a ticket with no -out path")
	}
}

// retriedRun seeds a run record on the emulator with the fields a retry leaves on
// its row beside it, and returns the run id and a way to read its job outputs.
func retriedRun(t *testing.T, fields map[string]any) (ctx context.Context, runID string, jobs func(args ...string) string) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx = context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID = "retried-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("runs").Doc(runID).Delete(context.Background())
		_ = client.Close()
	})
	if err := store.NewRecords(client).PutRecord(ctx, runID, runner.NewRecord(runID, prRecord.Ticket(), time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection("runs").Doc(runID).Set(ctx, fields, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "RUNNER_TEMP": dir, "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo", "GITHUB_OUTPUT": output}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return ctx, runID, func(args ...string) string {
		t.Helper()
		if err := run(ctx, logger, args, func(k string) string { return env[k] }); err != nil {
			t.Fatal(err)
		}
		out, _ := os.ReadFile(output)
		return string(out)
	}
}

func TestTicketJobCarriesTheOwnersNoteOfARetryAndNoneOtherwise(t *testing.T) {
	for name, tt := range map[string]struct {
		fields map[string]any
		want   string
	}{
		"a retry":  {map[string]any{"operator_note": "Keep the old flag name.", "operator_assisted": true}, "Keep the old flag name."},
		"no retry": {map[string]any{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, runID, jobs := retriedRun(t, tt.fields)
			path := filepath.Join(t.TempDir(), "ticket.json")
			jobs("ticket", "-run-id", runID, "-out", path)
			got, err := readTicket(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Note != tt.want {
				t.Fatalf("ticket.json note = %q, want %q", got.Note, tt.want)
			}
		})
	}
}
