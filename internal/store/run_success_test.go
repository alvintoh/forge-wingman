package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// runSuccessBase is later than any row another test writes, so a window counts
// only the rows these tests write.
var runSuccessBase = time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)

// runSuccessWindowAt is the base of one test's two windows, each test's 100 days
// apart so no two overlap, and clearRunSuccessWindow empties it before a test
// writes: the emulator outlives one run of the suite, and this read counts every
// row in its window.
func runSuccessWindowAt() time.Time {
	return runSuccessBase.Add(time.Duration(queueSeq.Add(1)) * 100 * 24 * time.Hour)
}

// clearRunSuccessWindow deletes the runs started in the two windows ending at
// base, so a document an interrupted run left behind is never counted with this
// test's own.
func clearRunSuccessWindow(t *testing.T, client *firestore.Client, base time.Time) {
	t.Helper()
	ctx := context.Background()
	iter := client.Collection(runsCollection).
		Where(startedAtField, ">=", base.Add(-2*runSuccessWindowSpan)).
		Documents(ctx)
	defer iter.Stop()
	var refs []*firestore.DocumentRef
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, snap.Ref)
	}
	for _, ref := range refs {
		if _, err := ref.Delete(ctx); err != nil {
			t.Fatal(err)
		}
	}
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
	day := 24 * time.Hour
	at := func(age time.Duration) time.Time { return runSuccessBase.Add(-age) }
	opened := func(age time.Duration, checks string) runResult {
		return runResult{StartedAt: at(age), Outcome: runner.OutcomePROpened, ChecksResult: checks}
	}
	// rows is n runs that opened a pull request with the given checks result,
	// a minute apart so no two share a start.
	rows := func(n int, checks string) []runResult {
		out := make([]runResult, n)
		for i := range out {
			out[i] = opened(day+time.Duration(i)*time.Minute, checks)
		}
		return out
	}
	for _, tt := range []struct {
		name string
		rows []runResult
		want string
	}{
		{"a run whose checks passed", []runResult{opened(day, dispatcher.CheckGreen)},
			"recent 1/1 null, previous 0/0 null, failure none"},
		{"a run whose checks failed", []runResult{opened(day, dispatcher.CheckFailing)},
			"recent 0/1 null, previous 0/0 null, failure checks-failing=1"},
		{"a run that made no changes", []runResult{{StartedAt: at(day), Outcome: runner.OutcomeNoChanges}},
			"recent 0/1 null, previous 0/0 null, failure no-changes=1"},
		{"a run whose checks are still pending", []runResult{opened(day, "")},
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"a run whose checks can never be read", []runResult{opened(day, dispatcher.CheckUnresolved)},
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"a run the owner cancelled", []runResult{
			{StartedAt: at(day), Outcome: runner.OutcomePROpened, ChecksResult: dispatcher.CheckGreen, Cancelled: true},
		}, "recent 0/0 null, previous 0/0 null, failure none"},
		{"a run still in flight", []runResult{{StartedAt: at(day)}},
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"a run stopped for a named reason", []runResult{
			{StartedAt: at(day), Outcome: runner.OutcomeStopped, StopReason: runner.StopRebaseConflict},
		}, "recent 0/1 null, previous 0/0 null, failure rebase-conflict=1"},
		{"a run stopped without one", []runResult{{StartedAt: at(day), Outcome: runner.OutcomeAgentFailed}},
			"recent 0/1 null, previous 0/0 null, failure agent-failed=1"},
		{"a run started before the earlier window", []runResult{opened(60*day, dispatcher.CheckGreen)},
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"a run started in the four weeks before", []runResult{opened(30*day, dispatcher.CheckGreen)},
			"recent 0/0 null, previous 1/1 null, failure none"},
		{"a run started after now", []runResult{opened(-time.Hour, dispatcher.CheckGreen)},
			"recent 0/0 null, previous 0/0 null, failure none"},
		{"nineteen runs state no rate", rows(19, dispatcher.CheckGreen),
			"recent 19/19 null, previous 0/0 null, failure none"},
		{"twenty runs state the rate", rows(20, dispatcher.CheckGreen),
			"recent 20/20 1, previous 0/0 null, failure none"},
		{"the rate is the share that passed", append(rows(15, dispatcher.CheckGreen), rows(5, dispatcher.CheckFailing)...),
			"recent 15/20 0.75, previous 0/0 null, failure checks-failing=5"},
		{"the commonest failure is named", []runResult{
			{StartedAt: at(day), Outcome: runner.OutcomeNoChanges},
			{StartedAt: at(day + time.Hour), Outcome: runner.OutcomeNoChanges},
			{StartedAt: at(day + 2*time.Hour), Outcome: runner.OutcomeAgentFailed},
		}, "recent 0/3 null, previous 0/0 null, failure no-changes=2"},
		{"a tie goes to the first class by name", []runResult{
			{StartedAt: at(day), Outcome: runner.OutcomeStopped, StopReason: runner.StopAgentExit},
			{StartedAt: at(day + time.Hour), Outcome: runner.OutcomeNoChanges},
		}, "recent 0/2 null, previous 0/0 null, failure agent-exit=1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := runSuccessText(countRunSuccess(tt.rows, runSuccessBase)); got != tt.want {
				t.Errorf("run success = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRunSuccessReadsOneCappedQuery asserts the rate is one query of the runs
// started since the earlier window opened, newest start first and capped at 200
// (AC4): 201 runs in the trailing four weeks read as the newest 200, and the
// oldest is the one left out.
func TestRunSuccessReadsOneCappedQuery(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	base := runSuccessWindowAt()
	clearRunSuccessWindow(t, client, base)
	var ids []string
	write := func(started time.Time, outcome, checks string) {
		id := fresh("run-success")
		ids = append(ids, id)
		_, err := client.Collection(runsCollection).Doc(id).Set(ctx, map[string]any{
			startedAtField: started, outcomeField: outcome, checksResultField: checks,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 200 {
		write(base.Add(-28*24*time.Hour+time.Duration(i)*time.Minute), string(runner.OutcomePROpened), dispatcher.CheckGreen)
	}
	write(base, string(runner.OutcomePROpened), dispatcher.CheckFailing)
	forget(t, client, ids...)

	got, err := NewRecords(client).RunSuccess(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	// The 200 newest keep the failing run and drop the oldest green one.
	if want := "recent 199/200 0.995, previous 0/0 null, failure checks-failing=1"; runSuccessText(got) != want {
		t.Fatalf("run success = %q, want %q", runSuccessText(got), want)
	}
}

// TestRunSuccessCountsTheFourWeeksBefore asserts the earlier window is counted
// off the same read, and a run started before it is not counted at all (AC1).
func TestRunSuccessCountsTheFourWeeksBefore(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	base := runSuccessWindowAt()
	clearRunSuccessWindow(t, client, base)
	seed := func(age time.Duration) {
		seedRow(t, client, fresh("run-success"), map[string]any{
			startedAtField:    base.Add(-age),
			outcomeField:      string(runner.OutcomePROpened),
			checksResultField: dispatcher.CheckGreen,
		})
	}
	seed(10 * 24 * time.Hour)
	seed(30 * 24 * time.Hour)
	seed(60 * 24 * time.Hour)

	got, err := NewRecords(client).RunSuccess(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if want := "recent 1/1 null, previous 1/1 null, failure none"; runSuccessText(got) != want {
		t.Fatalf("run success = %q, want %q", runSuccessText(got), want)
	}
}
