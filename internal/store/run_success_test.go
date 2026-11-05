package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// runSuccessClient is a client on a project of its own, emptied of runs: the
// read takes the newest runs in the whole collection, so a test's rows must be
// the only ones there, and the emulator outlives one run of the suite.
func runSuccessClient(t *testing.T) *firestore.Client {
	t.Helper()
	queue(t) // skips without the emulator
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, fresh("forge-wingman-run-success"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	refs, err := client.Collection(runsCollection).DocumentRefs(ctx).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if _, err := ref.Delete(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return client
}

// runSuccessBase is where the seeded runs start counting back from.
var runSuccessBase = time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)

// seedOpened seeds a run started at that moment that opened a pull request
// whose checks settled at checks.
func seedOpened(t *testing.T, client *firestore.Client, started time.Time, checks string) {
	t.Helper()
	seedRow(t, client, fresh("run-success"), map[string]any{
		startedAtField: started, outcomeField: string(runner.OutcomePROpened), checksResultField: checks,
	})
}

// runSuccessText prints a rate as one line, so a table compares every field the
// endpoint serves at once.
func runSuccessText(s RunSuccess) string {
	rate := func(p *float64) string {
		if p == nil {
			return "null"
		}
		return fmt.Sprint(*p)
	}
	failure := "none"
	if s.Failure != nil {
		failure = fmt.Sprintf("%s=%d", s.Failure.Class, s.Failure.Count)
	}
	return fmt.Sprintf("recent %d/%d %s, previous %d/%d %s, failure %s",
		s.Recent.Passed, s.Recent.Runs, rate(s.Recent.Rate),
		s.Previous.Passed, s.Previous.Runs, rate(s.Previous.Rate), failure)
}

func TestCountRunSuccessCountsTheRunsThatFinished(t *testing.T) {
	opened := func(checks string) runResult {
		return runResult{Outcome: runner.OutcomePROpened, ChecksResult: checks}
	}
	// rows is n runs that opened a pull request with the given checks result.
	rows := func(n int, checks string) []runResult {
		out := make([]runResult, n)
		for i := range out {
			out[i] = opened(checks)
		}
		return out
	}
	// join lays groups of rows end to end, newest first.
	join := func(groups ...[]runResult) []runResult {
		var out []runResult
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	cancelled := runResult{Outcome: runner.OutcomePROpened, ChecksResult: dispatcher.CheckGreen, Cancelled: true}
	for _, tt := range []struct {
		name string
		rows []runResult
		want string
	}{
		{"a run whose checks passed", rows(1, dispatcher.CheckGreen),
			"recent 1/1 null, previous 0/0 null, failure none"},
		{"a run whose checks failed", rows(1, dispatcher.CheckFailing),
			"recent 0/1 null, previous 0/0 null, failure checks-failing=1"},
		{"a run that made no changes", []runResult{{Outcome: runner.OutcomeNoChanges}},
			"recent 0/1 null, previous 0/0 null, failure no-changes=1"},
		{"a run whose checks are still pending", rows(1, ""),
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"a run whose checks can never be read", rows(1, dispatcher.CheckUnresolved),
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"a run the owner cancelled", []runResult{cancelled},
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"a run stopped for a named reason", []runResult{{Outcome: runner.OutcomeStopped, StopReason: runner.StopRebaseConflict}},
			"recent 0/1 null, previous 0/0 null, failure rebase-conflict=1"},
		{"a run stopped without one", []runResult{{Outcome: runner.OutcomeAgentFailed}},
			"recent 0/1 null, previous 0/0 null, failure agent-failed=1"},
		{"the newest 30 counted runs are recent and the next 30 previous", join(rows(30, dispatcher.CheckGreen), rows(30, dispatcher.CheckFailing)),
			"recent 30/30 1, previous 0/30 0, failure none"},
		{"a run past the two windows counts in neither", join(rows(60, dispatcher.CheckGreen), rows(1, dispatcher.CheckFailing)),
			"recent 30/30 1, previous 30/30 1, failure none"},
		{"cancelled and pending runs take no place in a window", join([]runResult{cancelled}, rows(1, ""), rows(30, dispatcher.CheckGreen), rows(1, dispatcher.CheckFailing)),
			"recent 30/30 1, previous 0/1 null, failure none"},
		{"nineteen runs state no rate", rows(19, dispatcher.CheckGreen),
			"recent 19/19 null, previous 0/0 null, failure none"},
		{"twenty runs state the rate", rows(20, dispatcher.CheckGreen),
			"recent 20/20 1, previous 0/0 null, failure none"},
		{"the rate is the share that passed", join(rows(15, dispatcher.CheckGreen), rows(5, dispatcher.CheckFailing)),
			"recent 15/20 0.75, previous 0/0 null, failure checks-failing=5"},
		{"the previous window states its own rate", join(rows(24, dispatcher.CheckGreen), rows(6, dispatcher.CheckFailing), rows(27, dispatcher.CheckGreen), rows(3, dispatcher.CheckFailing)),
			"recent 24/30 0.8, previous 27/30 0.9, failure checks-failing=6"},
		{"the commonest failure is named", []runResult{
			{Outcome: runner.OutcomeNoChanges}, {Outcome: runner.OutcomeNoChanges}, {Outcome: runner.OutcomeAgentFailed},
		}, "recent 0/3 null, previous 0/0 null, failure no-changes=2"},
		{"a failure in the previous window is not the top stop", join(rows(30, dispatcher.CheckGreen), []runResult{{Outcome: runner.OutcomeNoChanges}}),
			"recent 30/30 1, previous 0/1 null, failure none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := runSuccessText(countRunSuccess(tt.rows, 30)); got != tt.want {
				t.Errorf("run success = %q, want %q", got, tt.want)
			}
		})
	}
}

// cappedLines returns the runSuccessCapped lines logged to buf.
func cappedLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(line, "runSuccessCapped") {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		out = append(out, entry)
	}
	return out
}

// TestRunSuccessWindowsAreTheNewest30RunsAcrossAnIdleStretch asserts the windows
// are the newest 30 counted runs and the 30 before them however long ago they
// started: half the recent window ran before 50 idle days, and the run past the
// 60th is in neither.
func TestRunSuccessWindowsAreTheNewest30RunsAcrossAnIdleStretch(t *testing.T) {
	client := runSuccessClient(t)
	day := 24 * time.Hour
	at := func(age time.Duration, i int) time.Time { return runSuccessBase.Add(-age - time.Duration(i)*time.Hour) }
	for i := range 15 {
		seedOpened(t, client, at(0, i), dispatcher.CheckGreen)
	}
	for i := range 15 {
		checks := dispatcher.CheckGreen
		if i < 6 {
			checks = dispatcher.CheckFailing
		}
		seedOpened(t, client, at(50*day, i), checks)
	}
	for i := range 30 {
		checks := dispatcher.CheckGreen
		if i < 3 {
			checks = dispatcher.CheckFailing
		}
		seedOpened(t, client, at(100*day, i), checks)
	}
	seedOpened(t, client, at(400*day, 0), dispatcher.CheckFailing)

	var logged bytes.Buffer
	got, err := NewRecords(client).WithLogger(slog.New(slog.NewJSONHandler(&logged, nil))).RunSuccess(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := "recent 24/30 0.8, previous 27/30 0.9, failure checks-failing=6"; runSuccessText(got) != want {
		t.Fatalf("run success = %q, want %q", runSuccessText(got), want)
	}
	if lines := cappedLines(t, &logged); len(lines) != 0 {
		t.Fatalf("runSuccessCapped lines = %v, want none once both windows are full", lines)
	}
}

// TestRunSuccessReadsPastRunsThatTakeNoPlace asserts the read stops at the 60th
// counted run, not the 60th row: a cancelled run and one whose checks are pending,
// both newer than the 60 counted, leave both windows full.
func TestRunSuccessReadsPastRunsThatTakeNoPlace(t *testing.T) {
	client := runSuccessClient(t)
	seedRow(t, client, fresh("run-success"), map[string]any{
		startedAtField: runSuccessBase, outcomeField: string(runner.OutcomePROpened),
		checksResultField: dispatcher.CheckGreen, cancelledByOwnerAtField: runSuccessBase.Add(time.Hour),
	})
	seedOpened(t, client, runSuccessBase.Add(-time.Minute), "")
	for i := range 60 {
		seedOpened(t, client, runSuccessBase.Add(-time.Duration(i+2)*time.Minute), dispatcher.CheckGreen)
	}

	got, err := NewRecords(client).RunSuccess(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := "recent 30/30 1, previous 30/30 1, failure none"; runSuccessText(got) != want {
		t.Fatalf("run success = %q, want %q", runSuccessText(got), want)
	}
}

// TestRunSuccessCapsTheReadAtTheNewestRows asserts a read that reaches 200 rows
// without filling both windows counts what it found, leaves out the rows past
// the cap, and logs the cap once with the limit, the runs counted and the
// oldest start read.
func TestRunSuccessCapsTheReadAtTheNewestRows(t *testing.T) {
	client := runSuccessClient(t)
	for i := range 199 {
		seedRow(t, client, fresh("run-success"), map[string]any{
			startedAtField: runSuccessBase.Add(-time.Duration(i) * time.Minute), stateField: stateClaimed,
		})
	}
	oldest := runSuccessBase.Add(-199 * time.Minute)
	seedOpened(t, client, oldest, dispatcher.CheckGreen)
	seedOpened(t, client, oldest.Add(-time.Minute), dispatcher.CheckFailing)

	var logged bytes.Buffer
	got, err := NewRecords(client).WithLogger(slog.New(slog.NewJSONHandler(&logged, nil))).RunSuccess(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := "recent 1/1 null, previous 0/0 null, failure none"; runSuccessText(got) != want {
		t.Fatalf("run success = %q, want %q", runSuccessText(got), want)
	}
	lines := cappedLines(t, &logged)
	if len(lines) != 1 || lines[0]["limit"] != 200.0 || lines[0]["counted"] != 1.0 {
		t.Fatalf("runSuccessCapped lines = %v, want one carrying limit 200 and counted 1", lines)
	}
	read, err := time.Parse(time.RFC3339Nano, fmt.Sprint(lines[0]["oldest"]))
	if err != nil || !read.Equal(oldest) {
		t.Errorf("oldest = %v (%v), want %v", read, err, oldest)
	}
}

// TestRunSuccessLogsNothingUnderTheCap asserts a read that runs out of rows
// before 200 logs no cap line, however few runs it counted.
func TestRunSuccessLogsNothingUnderTheCap(t *testing.T) {
	client := runSuccessClient(t)
	for i := range 199 {
		seedOpened(t, client, runSuccessBase.Add(-time.Duration(i)*time.Minute), "")
	}

	var logged bytes.Buffer
	got, err := NewRecords(client).WithLogger(slog.New(slog.NewJSONHandler(&logged, nil))).RunSuccess(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := "recent 0/0 null, previous 0/0 null, failure none"; runSuccessText(got) != want {
		t.Fatalf("run success = %q, want %q", runSuccessText(got), want)
	}
	if lines := cappedLines(t, &logged); len(lines) != 0 {
		t.Fatalf("runSuccessCapped lines = %v, want none", lines)
	}
}

// TestRunSuccessLogsNothingWhenTheLastRowReadFillsTheWindows asserts a read
// whose 200th row is the 60th counted run is not reported as capped.
func TestRunSuccessLogsNothingWhenTheLastRowReadFillsTheWindows(t *testing.T) {
	client := runSuccessClient(t)
	for i := range 200 {
		row := map[string]any{startedAtField: runSuccessBase.Add(-time.Duration(i) * time.Minute), stateField: stateClaimed}
		if i >= 140 {
			row = map[string]any{
				startedAtField: row[startedAtField], outcomeField: string(runner.OutcomePROpened), checksResultField: dispatcher.CheckGreen,
			}
		}
		seedRow(t, client, fresh("run-success"), row)
	}

	var logged bytes.Buffer
	got, err := NewRecords(client).WithLogger(slog.New(slog.NewJSONHandler(&logged, nil))).RunSuccess(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := "recent 30/30 1, previous 30/30 1, failure none"; runSuccessText(got) != want {
		t.Fatalf("run success = %q, want %q", runSuccessText(got), want)
	}
	if lines := cappedLines(t, &logged); len(lines) != 0 {
		t.Fatalf("runSuccessCapped lines = %v, want none once both windows are full", lines)
	}
}

// TestRunSuccessCountsFinishedRunsTheOwnerDidNotCancel asserts the stored rows
// alone tell a cancelled run and a run still in flight from a counted one: a
// finalized row carries the zero time until a cancel sets it.
func TestRunSuccessCountsFinishedRunsTheOwnerDidNotCancel(t *testing.T) {
	client := runSuccessClient(t)
	started := runSuccessBase.Add(-24 * time.Hour)
	opened := func(cancelledAt time.Time) map[string]any {
		return map[string]any{
			startedAtField:          started,
			outcomeField:            string(runner.OutcomePROpened),
			checksResultField:       dispatcher.CheckGreen,
			cancelledByOwnerAtField: cancelledAt,
		}
	}
	seedRow(t, client, fresh("run-success"), opened(started.Add(time.Hour)))
	seedRow(t, client, fresh("run-success"), opened(time.Time{}))
	seedRow(t, client, fresh("run-success"), map[string]any{startedAtField: started, cancelledByOwnerAtField: time.Time{}})

	got, err := NewRecords(client).RunSuccess(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := "recent 1/1 null, previous 0/0 null, failure none"; runSuccessText(got) != want {
		t.Fatalf("run success = %q, want %q", runSuccessText(got), want)
	}
}

// TestRunSuccessCountsAnAbandonedRunAsFailed asserts the stored state alone makes
// a run swept with no outcome a failure, unless the owner cancelled it, while a
// run still in flight stays out of the count.
func TestRunSuccessCountsAnAbandonedRunAsFailed(t *testing.T) {
	for _, tt := range []struct {
		name string
		row  map[string]any
		want string
	}{
		{"an abandoned run", map[string]any{stateField: stateAbandoned},
			"recent 0/1 null, previous 0/0 null, failure abandoned=1"},
		{"an abandoned run the owner cancelled", map[string]any{stateField: stateAbandoned, cancelledByOwnerAtField: time.Now()},
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"a run still in flight", map[string]any{stateField: stateClaimed},
			"recent 0/0 null, previous 0/0 null, failure none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := runSuccessClient(t)
			tt.row[startedAtField] = runSuccessBase.Add(-24 * time.Hour)
			seedRow(t, client, fresh("run-success"), tt.row)

			got, err := NewRecords(client).RunSuccess(context.Background(), 30)
			if err != nil {
				t.Fatal(err)
			}
			if runSuccessText(got) != tt.want {
				t.Fatalf("run success = %q, want %q", runSuccessText(got), tt.want)
			}
		})
	}
}

// TestMostCommonTieGoesToTheFirstClassByName calls mostCommon many times because
// a map ranges in a random order, so one call can pass by luck.
func TestMostCommonTieGoesToTheFirstClassByName(t *testing.T) {
	failures := map[string]int{"stopped": 2, "agent-exit": 2, "no-changes": 2, "rebase-conflict": 2, "agent-failed": 1}
	for range 64 {
		got := mostCommon(failures)
		if got == nil || got.Class != "agent-exit" || got.Count != 2 {
			t.Fatalf("mostCommon = %+v, want agent-exit=2", got)
		}
	}
}
