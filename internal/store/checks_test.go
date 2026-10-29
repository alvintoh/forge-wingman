package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const checksPRURL = "https://github.com/octo/scratch/pull/7"

// prOpenedRow is a run row whose attempt opened the pull request at url.
func prOpenedRow(url string, extra map[string]any) map[string]any {
	row := map[string]any{prURLField: url, outcomeField: string(runner.OutcomePROpened), startedAtField: queueAt}
	for k, v := range extra {
		row[k] = v
	}
	return row
}

func TestRecordChecksWritesBothFieldsOnceAndLeavesASettledRowAlone(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	id := fresh("checks-once")
	seedRow(t, client, id, prOpenedRow(checksPRURL, map[string]any{"linear_priority": int64(2)}))
	checks := NewChecks(client)
	if recorded, err := checks.RecordChecks(ctx, id, checksPRURL, "agent1", "green"); err != nil || !recorded {
		t.Fatalf("recorded = %v, %v, want the first result written", recorded, err)
	}
	row := readRow(t, client, id)
	if row[prHeadSHAField] != "agent1" || row[checksResultField] != "green" || row["linear_priority"] != int64(2) {
		t.Fatalf("row = %v, want the sha and result written beside the fields already there", row)
	}
	if recorded, err := checks.RecordChecks(ctx, id, checksPRURL, "agent2", "failing"); err != nil || recorded {
		t.Fatalf("recorded = %v, %v, want a settled row left alone", recorded, err)
	}
	row = readRow(t, client, id)
	if row[prHeadSHAField] != "agent1" || row[checksResultField] != "green" {
		t.Fatalf("row = %v, want the first result kept", row)
	}
}

func TestRecordChecksOfTwoWritersRecordsExactlyOne(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	id := fresh("checks-race")
	seedRow(t, client, id, prOpenedRow(checksPRURL, nil))
	checks := NewChecks(client)
	type outcome struct {
		sha, result string
		recorded    bool
		err         error
	}
	done := make(chan outcome, 2)
	for _, w := range [][2]string{{"agent-a", "green"}, {"agent-b", "failing"}} {
		go func() {
			recorded, err := checks.RecordChecks(ctx, id, checksPRURL, w[0], w[1])
			done <- outcome{w[0], w[1], recorded, err}
		}()
	}
	var winners []outcome
	for range 2 {
		o := <-done
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.recorded {
			winners = append(winners, o)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("%d writers recorded, want exactly one", len(winners))
	}
	row := readRow(t, client, id)
	if row[prHeadSHAField] != winners[0].sha || row[checksResultField] != winners[0].result {
		t.Fatalf("row = %v, want the winner's %s and %s", row, winners[0].sha, winners[0].result)
	}
}

func TestRecordChecksLeavesARowThatMovedOnSinceTheResultWasRead(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	checks := NewChecks(client)
	for name, tt := range map[string]struct {
		row map[string]any
	}{
		"the run opened another pull request": {prOpenedRow("https://github.com/octo/scratch/pull/8", nil)},
		"the run was requeued":                {map[string]any{prURLField: checksPRURL, startedAtField: queueAt}},
	} {
		for _, write := range []struct{ sha, result string }{{"agent1", "green"}, {"", "unresolved"}} {
			t.Run(name+" for a "+write.result+" write", func(t *testing.T) {
				id := fresh("checks-moved")
				seedRow(t, client, id, tt.row)
				if recorded, err := checks.RecordChecks(ctx, id, checksPRURL, write.sha, write.result); err != nil || recorded {
					t.Fatalf("recorded = %v, %v, want the stale result refused", recorded, err)
				}
				if row := readRow(t, client, id); row[checksResultField] != nil || row[prHeadSHAField] != nil {
					t.Fatalf("row = %v, want no result written", row)
				}
			})
		}
	}
}

func TestRecordChecksMarksARunUnresolvedOnceWithNoHeadSHA(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	id := fresh("checks-unresolved")
	seedRow(t, client, id, prOpenedRow(checksPRURL, nil))
	checks := NewChecks(client)
	if recorded, err := checks.RecordChecks(ctx, id, checksPRURL, "", "unresolved"); err != nil || !recorded {
		t.Fatalf("recorded = %v, %v, want the mark written", recorded, err)
	}
	row := readRow(t, client, id)
	if row[checksResultField] != "unresolved" {
		t.Fatalf("row = %v, want checks_result unresolved", row)
	}
	if _, ok := row[prHeadSHAField]; ok {
		t.Fatalf("row = %v, want no pr_head_sha for an unresolved run", row)
	}
	if recorded, err := checks.RecordChecks(ctx, id, checksPRURL, "agent1", "green"); err != nil || recorded {
		t.Fatalf("recorded = %v, %v, want the mark kept over a later result", recorded, err)
	}
}

func TestRecordChecksRefusesAnAbsentRun(t *testing.T) {
	_, client := queue(t)
	_, err := NewChecks(client).RecordChecks(context.Background(), fresh("checks-absent"), checksPRURL, "agent1", "green")
	if !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want ErrRecordNotFound", err)
	}
}

func TestUnsettledRunsListsOnlyOpenedPullRequestsWithoutAResultOldestFirst(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	oldest, newer, settled, noPR, requeued, unresolved, emptyURL := fresh("checks-oldest"), fresh("checks-newer"), fresh("checks-settled"), fresh("checks-no-pr"), fresh("checks-requeued"), fresh("checks-unresolved"), fresh("checks-empty-url")
	seedRow(t, client, emptyURL, prOpenedRow("", nil))
	seedRow(t, client, newer, prOpenedRow("https://github.com/octo/scratch/pull/8", nil))
	seedRow(t, client, oldest, prOpenedRow(checksPRURL, map[string]any{startedAtField: queueAt.Add(-1000 * 24 * time.Hour)}))
	seedRow(t, client, settled, prOpenedRow("https://github.com/octo/scratch/pull/9", map[string]any{checksResultField: "green"}))
	seedRow(t, client, unresolved, prOpenedRow("https://github.com/octo/scratch/pull/11", map[string]any{checksResultField: "unresolved"}))
	seedRow(t, client, noPR, map[string]any{startedAtField: queueAt})
	seedRow(t, client, requeued, map[string]any{prURLField: "https://github.com/octo/scratch/pull/10", startedAtField: queueAt})
	checks := NewChecks(client)

	all, err := checks.UnsettledRuns(ctx, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	var mine []string
	for _, run := range all {
		switch run.ID {
		case oldest, newer, settled, noPR, requeued, unresolved, emptyURL:
			mine = append(mine, run.ID)
		}
		if run.ID == oldest && run.PRURL != checksPRURL {
			t.Errorf("run %s has PR URL %q", run.ID, run.PRURL)
		}
	}
	if want := []string{oldest, newer}; !slices.Equal(mine, want) {
		t.Fatalf("listed %v of the seeded runs, want %v", mine, want)
	}

	capped, err := checks.UnsettledRuns(ctx, 1)
	if err != nil || len(capped) != 1 || capped[0].ID != all[0].ID {
		t.Fatalf("capped to one = %+v, %v, want the first of %d", capped, err, len(all))
	}
}

func TestUnsettledRunsFallsBackToTheQueueStampForARunWithNoStart(t *testing.T) {
	_, client := queue(t)
	id := fresh("checks-queued-at")
	seedRow(t, client, id, map[string]any{prURLField: checksPRURL, outcomeField: string(runner.OutcomePROpened), queuedAtField: queueAt})
	runs, err := NewChecks(client).UnsettledRuns(context.Background(), 10_000)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.ID == id {
			if !run.StartedAt.Equal(queueAt) {
				t.Fatalf("start = %v, want the queue stamp %v", run.StartedAt, queueAt)
			}
			return
		}
	}
	t.Fatalf("run %s was not listed", id)
}

func TestRequeueClearsTheChecksResultAndDropsTheRunFromTheUnsettledList(t *testing.T) {
	for _, tt := range []struct {
		result string
		row    map[string]any
	}{
		{"failing", map[string]any{prHeadSHAField: "agent1", checksResultField: "failing"}},
		{"unresolved", map[string]any{checksResultField: "unresolved"}},
	} {
		t.Run(tt.result, func(t *testing.T) {
			q, client := queue(t)
			ctx := context.Background()
			run := queuedRun(fresh("queue-requeue-checks"), 1)
			forget(t, client, run.RunID)
			resetLedger(t, client, 1)
			if err := q.Enqueue(ctx, run); err != nil {
				t.Fatal(err)
			}
			updates := []firestore.Update{
				{Path: outcomeField, Value: string(runner.OutcomePROpened)},
				{Path: prURLField, Value: checksPRURL},
			}
			for field, value := range tt.row {
				updates = append(updates, firestore.Update{Path: field, Value: value})
			}
			if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, updates); err != nil {
				t.Fatal(err)
			}
			if _, err := q.Requeue(ctx, run.RunID, queueAt.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			data := readRow(t, client, run.RunID)
			for _, field := range []string{prHeadSHAField, checksResultField} {
				if _, ok := data[field]; ok {
					t.Errorf("field %s survived the requeue: %v", field, data[field])
				}
			}
			if data[prURLField] != checksPRURL {
				t.Fatalf("pr_url = %v, want the old PR still named, which is the case the list must skip", data[prURLField])
			}
			runs, err := NewChecks(client).UnsettledRuns(ctx, 10_000)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range runs {
				if r.ID == run.RunID {
					t.Fatalf("requeued run %s is listed as unsettled", run.RunID)
				}
			}
		})
	}
}
