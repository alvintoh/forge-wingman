package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

func TestPlanStageProceedsPastItsImageCheckWithAPinnedImage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pinnedImagesOnPath(t)
	dir := t.TempDir()
	err := planStage(context.Background(), logger, env{attemptID: "42-1"},
		append(pinnedImageArgs()[:2], "-ticket-file", filepath.Join(dir, "absent.json"), "-out", filepath.Join(dir, "plan.json")))
	if !errors.Is(err, errTicketNotDelivered) {
		t.Fatalf("err = %v, want the stage to reach its ticket", err)
	}
}

func TestPlanStageNeedsAnOutPath(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := planStage(context.Background(), logger, env{}, nil); err == nil ||
		!strings.Contains(err.Error(), "-out") {
		t.Fatalf("err = %v, want a missing -out path refused before any model call", err)
	}
}

func jsonLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line %q is not JSON: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestFinishPlanStageLogsTheCauseOnce(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil)).With("ticket", "T-9")
	out := filepath.Join(t.TempDir(), "plan.json")
	cause := errors.New("agent exited 1")

	got := finishPlanStage(logger, runner.PlanStageResult{}, cause, out)

	if !errors.Is(got, errRunFailed) || !errors.Is(got, cause) {
		t.Fatalf("err = %v, want it to wrap errRunFailed and the cause", got)
	}
	lines := jsonLogLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly one: %s", len(lines), buf.String())
	}
	if lines[0]["msg"] != "planStageFailed" || lines[0]["level"] != "ERROR" ||
		lines[0]["err"] != "agent exited 1" || lines[0]["ticket"] != "T-9" {
		t.Fatalf("line = %v, want an ERROR planStageFailed carrying err and ticket", lines[0])
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stat %s = %v, want no artifact written for a failed stage", out, statErr)
	}
}

// The agent failure and the plan that does not validate are plain errors: the
// runner's stop errors for those are unexported.
func TestFinishPlanStageLogsEachFailureShapeUnmodified(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"the agent failing", errors.New("plan agent: exit status 1")},
		{"a projection miss", fmt.Errorf("reading the rule stack: %w", runner.ErrObjectNotFound)},
		{"a plan that does not validate", errors.New("plan agent's response carries no valid plan-files list")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))

			_ = finishPlanStage(logger, runner.PlanStageResult{}, tc.err, filepath.Join(t.TempDir(), "plan.json"))

			lines := jsonLogLines(t, &buf)
			if len(lines) != 1 || lines[0]["err"] != tc.err.Error() {
				t.Fatalf("lines = %v, want one line whose err is %q", lines, tc.err.Error())
			}
		})
	}
}

func TestFinishPlanStageLogsNothingOnSuccess(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	out := filepath.Join(t.TempDir(), "plan.json")
	res := runner.PlanStageResult{Files: []string{"internal/runner/plan.go"}, BaseSHA: strings.Repeat("a", 40)}

	if err := finishPlanStage(logger, res, nil, out); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	lines := jsonLogLines(t, &buf)
	if len(lines) != 1 || lines[0]["msg"] != "planStageDone" {
		t.Fatalf("lines = %v, want only planStageDone", lines)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the artifact: %v", err)
	}
	if _, err := runner.ParsePlanStage(raw); err != nil {
		t.Fatalf("artifact does not re-check: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat %s: %v", out, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode = %v, want 0600", info.Mode().Perm())
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

// TestRecordPlanStageRecordsTheModelThePlanRanOn drives the trusted half to the
// plan write: the artifact's model reaches the row as plan_model, and an
// artifact carrying none records the plan with that field empty. The call goes
// on to wake the dispatcher, which needs Cloud Run credentials a test has none
// of, so only the recorded row is asserted.
func TestRecordPlanStageRecordsTheModelThePlanRanOn(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "forge-wingman-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := env{identity: runner.Identity{Account: "octo", Owner: "octo"}, project: "forge-wingman-test"}
	for name, tc := range map[string]struct{ model, want string }{
		"a plan with a model":  {"command-code/deepseek/deepseek-v4.1-flash", "command-code/deepseek/deepseek-v4.1-flash"},
		"a plan with no model": {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			runID := "plan-model-" + strconv.FormatInt(time.Now().UnixNano(), 10)
			ref := client.Collection("runs").Doc(runID)
			t.Cleanup(func() { _, _ = ref.Delete(context.Background()) })
			raw, err := runner.EncodePlanStage(runner.PlanStageResult{
				Files: []string{"internal/runner/plan.go"}, BaseSHA: strings.Repeat("a", 40), Model: tc.model,
			})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			// The plan is recorded before the wake, which cannot reach a
			// dispatcher from here.
			_ = recordPlanStage(ctx, logger, e, []string{
				"-run-id", runID, "-plan-file", path,
				"-dispatcher-uri", "https://example.test", "-plan-stage-result", jobSuccess,
			})
			snap, err := ref.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := snap.Data()["plan_model"]; got != tc.want {
				t.Fatalf("plan_model = %v, want %q", got, tc.want)
			}
			if files, _ := snap.Data()["plan_files"].([]any); len(files) == 0 {
				t.Fatalf("plan_files = %v, want the plan recorded", snap.Data()["plan_files"])
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
