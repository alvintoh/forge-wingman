package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// inboxBase is later than any row another test writes, so the inbox reads
// only this test's rows.
var inboxBase = time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

func TestInboxListsOpenPRsWaitingOnTheOwnerNewestFirst(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("inbox-%d", time.Now().UnixNano())
	row := func(age time.Duration, extra map[string]any) map[string]any {
		r := map[string]any{
			"ticket_id": "ABC-1", "repo": "octo/scratch", "size": "M", "outcome": string(runner.OutcomePROpened),
			"pr_url": "https://github.com/octo/scratch/pull/7", "auto_merge": false, "updated_at": inboxBase.Add(-age),
			"ticket_title": "A ticket a PR row does not name",
		}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	rows := map[string]map[string]any{
		"newest":            row(time.Hour, map[string]any{"wait_reason": "front-end"}),
		"older":             row(2*time.Hour, nil),
		"auto-merging":      row(3*time.Hour, map[string]any{"auto_merge": true}),
		"auto-merge held":   row(3*time.Hour+time.Minute, map[string]any{"auto_merge": true, "wait_reason": "draft"}),
		"merged by owner":   row(4*time.Hour, map[string]any{"merged_by_owner_at": inboxBase}),
		"owner not merged":  row(5*time.Hour, map[string]any{"merged_by_owner_at": time.Time{}, "wait_reason": "sampled"}),
		"merged by a poll":  row(6*time.Hour, map[string]any{"rewrite_merged_at": inboxBase, "wait_reason": "size-l"}),
		"no PR":             row(7*time.Hour, map[string]any{"pr_url": "", "wait_reason": "draft"}),
		"stopped":           row(8*time.Hour, map[string]any{"outcome": string(runner.OutcomeInfraFailure), "wait_reason": "draft"}),
		"cancelled":         row(9*time.Hour, map[string]any{"cancelled_by_owner_at": inboxBase, "cancelled_by": "owner@example.com", "wait_reason": "draft"}),
		"too old to review": row(400*24*time.Hour, map[string]any{"wait_reason": "size-l"}),
	}
	var ids []string
	for name, data := range rows {
		id := prefix + "-" + name
		ids = append(ids, id)
		if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, data); err != nil {
			t.Fatal(err)
		}
	}
	forget(t, client, ids...)

	got, err := NewRecords(client).Inbox(ctx, inboxBase.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Run[len(prefix)+1:])
	}
	if want := []string{"newest", "older", "auto-merge held", "owner not merged"}; !slices.Equal(names, want) {
		t.Fatalf("inbox = %v, want %v", names, want)
	}
	first := got[0]
	if first.Stopped || first.Title != "" {
		t.Fatalf("first = %+v, want a PR row not marked as a stopped run and carrying no ticket title", first)
	}
	if first.Ticket != "ABC-1" || first.Repo != "octo/scratch" || first.Size != "M" || first.WaitReason != runner.WaitFrontEnd ||
		first.PRURL != "https://github.com/octo/scratch/pull/7" || !first.UpdatedAt.Equal(inboxBase.Add(-time.Hour)) {
		t.Fatalf("first = %+v, want the row's ticket, repo, size, PR and reason", first)
	}

	capped, err := NewRecords(client).Inbox(ctx, inboxBase.Add(-24*time.Hour), 1)
	if err != nil || len(capped) != 1 || capped[0].Run != got[0].Run {
		t.Fatalf("capped = %v, %v, want the newest alone", capped, err)
	}
}

func TestInboxListsStoppedRunsUnlessThereIsNothingForTheOwnerToRetry(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("inbox-stopped-%d", time.Now().UnixNano())
	row := func(age time.Duration, extra map[string]any) map[string]any {
		r := map[string]any{
			"ticket_id": "ABC-1", "repo": "octo/scratch", "size": "M", "state": "claimed", "attempt": int64(2),
			"outcome": string(runner.OutcomeAgentFailed), "stop_reason": "agent-exit",
			"settled_at": inboxBase.Add(-age), "claimed_at": inboxBase.Add(-age - time.Hour), "updated_at": inboxBase.Add(-age),
			"ticket_title": "Add the widget", "ticket_body": "the whole ticket body",
		}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	requeued := []any{map[string]any{"class": "module-proxy", "at": inboxBase}}
	rows := map[string]map[string]any{
		"finished claim":      row(time.Hour, nil),
		"stopped state":       row(2*time.Hour, map[string]any{"state": "stopped", "outcome": "stopped", "stop_reason": "waiting-on-owner"}),
		"old draft kept":      row(2*time.Hour+time.Minute, map[string]any{"state": "stopped", "outcome": "stopped", "stop_reason": "plan-stage-failed", "pr_url": "https://github.com/octo/scratch/pull/9", "wait_reason": "draft"}),
		"retry exhausted":     row(3*time.Hour, map[string]any{"transient": string(runner.TransientModuleProxy), "transient_requeues": append(requeued, requeued...)}),
		"requeue due":         row(4*time.Hour, map[string]any{"transient": string(runner.TransientModuleProxy), "transient_requeues": requeued}),
		"made no changes":     row(5*time.Hour, map[string]any{"outcome": string(runner.OutcomeNoChanges), "stop_reason": ""}),
		"abandoned":           row(6*time.Hour, map[string]any{"state": "abandoned"}),
		"claimed again":       row(7*time.Hour, map[string]any{"claimed_at": inboxBase}),
		"queued":              row(8*time.Hour, map[string]any{"state": "queued"}),
		"merged by the owner": row(9*time.Hour, map[string]any{"outcome": string(runner.OutcomeBuilt), "merged_by_owner_at": inboxBase}),
		"cancelled":           row(11*time.Hour, map[string]any{"outcome": string(runner.OutcomePROpened), "pr_url": "https://github.com/octo/scratch/pull/8", "cancelled_by_owner_at": inboxBase, "cancelled_by": "owner@example.com"}),
		"cancelled stopped":   row(12*time.Hour, map[string]any{"cancelled_by_owner_at": inboxBase, "cancelled_by": "owner@example.com"}),
		"opened a PR":         row(10*time.Hour, map[string]any{"pr_url": "https://github.com/octo/scratch/pull/7"}),
	}
	var ids []string
	for name, data := range rows {
		id := prefix + "-" + name
		ids = append(ids, id)
		if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, data); err != nil {
			t.Fatal(err)
		}
	}
	forget(t, client, ids...)

	got, err := NewRecords(client).Inbox(ctx, inboxBase.Add(-24*time.Hour), 20)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Run[len(prefix)+1:])
	}
	if want := []string{"finished claim", "stopped state", "old draft kept", "retry exhausted"}; !slices.Equal(names, want) {
		t.Fatalf("inbox = %v, want %v", names, want)
	}
	first := got[0]
	if !first.Stopped || first.Attempt != 2 || first.Outcome != string(runner.OutcomeAgentFailed) || first.StopReason != "agent-exit" || first.PRURL != "" {
		t.Fatalf("first = %+v, want the attempt, outcome and stop reason, and no PR", first)
	}
	if first.Title != "Add the widget" {
		t.Fatalf("title = %q, want the ticket's title for a stopped run", first.Title)
	}
	if kept := got[2]; !kept.Stopped || kept.PRURL != "https://github.com/octo/scratch/pull/9" {
		t.Fatalf("kept = %+v, want a stopped run that still names its earlier attempt's draft", kept)
	}
}

func TestRecordMergeAndSetWaitReasonKeepTheRestOfTheRow(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	id := fmt.Sprintf("merge-%d", time.Now().UnixNano())
	forget(t, client, id)
	doc := client.Collection(runsCollection).Doc(id)
	if _, err := doc.Set(ctx, map[string]any{"ticket_id": "ABC-1", "pr_url": "https://github.com/octo/scratch/pull/7"}); err != nil {
		t.Fatal(err)
	}
	recs := NewRecords(client)

	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	if err := recs.SetWaitReason(ctx, id, runner.WaitSizeL); err != nil {
		t.Fatal(err)
	}
	if err := recs.RecordMerge(ctx, id, "def456", at); err != nil {
		t.Fatal(err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data["ticket_id"] != "ABC-1" || data["pr_url"] != "https://github.com/octo/scratch/pull/7" {
		t.Errorf("a field neither write names was lost: %v", data)
	}
	if data["wait_reason"] != "size-l" || data["merged_sha"] != "def456" || !data["merged_by_owner_at"].(time.Time).Equal(at) {
		t.Errorf("row = %v, want the reason and the merge recorded", data)
	}
}

func TestRecordMergeRefusesARunWithNoRow(t *testing.T) {
	_, client := queue(t)
	id := fmt.Sprintf("absent-%d", time.Now().UnixNano())
	forget(t, client, id)
	if err := NewRecords(client).RecordMerge(context.Background(), id, "def456", time.Now()); err == nil {
		t.Fatal("RecordMerge created a row for a run that has none")
	}
	if snap, _ := client.Collection(runsCollection).Doc(id).Get(context.Background()); snap != nil && snap.Exists() {
		t.Fatal("a row now exists")
	}
}

func TestRecordCancelKeepsTheRestOfTheRow(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	id := fmt.Sprintf("cancel-%d", time.Now().UnixNano())
	forget(t, client, id)
	doc := client.Collection(runsCollection).Doc(id)
	if _, err := doc.Set(ctx, map[string]any{"ticket_id": "ABC-1", "wait_reason": "size-l", outcomeField: string(runner.OutcomePROpened)}); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	if err := NewRecords(client).RecordCancel(ctx, id, "owner@example.com", at); err != nil {
		t.Fatal(err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data["ticket_id"] != "ABC-1" || data["wait_reason"] != "size-l" {
		t.Errorf("a field the cancel does not name was lost: %v", data)
	}
	if data["cancelled_by"] != "owner@example.com" || !data["cancelled_by_owner_at"].(time.Time).Equal(at) {
		t.Errorf("row = %v, want the cancel and its owner recorded", data)
	}
}

func TestRecordCancelRefusesARunWithNoRow(t *testing.T) {
	_, client := queue(t)
	id := fmt.Sprintf("absent-cancel-%d", time.Now().UnixNano())
	forget(t, client, id)
	err := NewRecords(client).RecordCancel(context.Background(), id, "owner@example.com", time.Now())
	if !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want ErrRecordNotFound", err)
	}
	if snap, _ := client.Collection(runsCollection).Doc(id).Get(context.Background()); snap != nil && snap.Exists() {
		t.Fatal("a row now exists")
	}
}

func TestRecordCancelRecordsARunWaitingOrStoppedAndRefusesTheRest(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	waiting := string(runner.OutcomePROpened)
	stopped := map[string]any{stateField: stateStopped, outcomeField: string(runner.OutcomeStopped), stopReasonField: string(runner.StopWaitingOnOwner)}
	finished := map[string]any{
		stateField: stateClaimed, outcomeField: string(runner.OutcomeAgentFailed), stopReasonField: string(runner.StopAgentExit),
		claimedAtField: at.Add(-2 * time.Hour), settledAtField: at.Add(-time.Hour),
	}
	with := func(base, extra map[string]any) map[string]any {
		row := maps.Clone(base)
		maps.Copy(row, extra)
		return row
	}
	proxyFault := string(runner.TransientModuleProxy)
	requeue := map[string]any{"class": proxyFault, "at": at}
	for _, tt := range []struct {
		name string
		row  map[string]any
		want error
	}{
		{"waiting", map[string]any{outcomeField: waiting}, nil},
		{"stopped by state", stopped, nil},
		{"a finished claim", finished, nil},
		{"a run whose automatic requeues ran out", with(finished, map[string]any{transientField: proxyFault, transientRequeuesField: slices.Repeat([]any{requeue}, dispatcher.DefaultTransientRequeues)}), nil},
		{"cancelled", map[string]any{outcomeField: waiting, cancelledByOwnerAtField: at, cancelledByField: "first@example.com"}, ErrRunCancelled},
		{"a stopped run already cancelled", with(stopped, map[string]any{cancelledByOwnerAtField: at, cancelledByField: "first@example.com"}), ErrRunCancelled},
		{"merged by the owner", map[string]any{outcomeField: waiting, mergedByOwnerAtField: at}, ErrRunMerged},
		{"merged by a poll", map[string]any{outcomeField: waiting, rewriteMergedAtField: at}, ErrRunMerged},
		{"merged after a cancel", map[string]any{outcomeField: waiting, cancelledByOwnerAtField: at, rewriteMergedAtField: at}, ErrRunMerged},
		{"a stopped run that was merged", with(stopped, map[string]any{mergedByOwnerAtField: at}), ErrRunMerged},
		{"a run that made no changes", with(finished, map[string]any{outcomeField: string(runner.OutcomeNoChanges)}), ErrRunNotWaiting},
		{"a run a poll will requeue", with(finished, map[string]any{transientField: proxyFault, transientRequeuesField: slices.Repeat([]any{requeue}, dispatcher.DefaultTransientRequeues-1)}), ErrRunNotWaiting},
		{"a run claimed again", with(finished, map[string]any{claimedAtField: at}), ErrRunNotWaiting},
		{"in flight", map[string]any{stateField: stateClaimed}, ErrRunNotWaiting},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := fmt.Sprintf("cancel-refuse-%d", time.Now().UnixNano())
			forget(t, client, id)
			doc := client.Collection(runsCollection).Doc(id)
			if _, err := doc.Set(ctx, tt.row); err != nil {
				t.Fatal(err)
			}
			err := NewRecords(client).RecordCancel(ctx, id, "second@example.com", at.Add(time.Hour))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			snap, getErr := doc.Get(ctx)
			if getErr != nil {
				t.Fatal(getErr)
			}
			data := snap.Data()
			if tt.want == nil {
				if data[cancelledByField] != "second@example.com" {
					t.Fatalf("row = %v, want the cancel recorded", data)
				}
				return
			}
			if data[cancelledByField] == "second@example.com" || len(data) != len(tt.row) {
				t.Fatalf("row = %v, want the refused cancel to write nothing", data)
			}
		})
	}
}

func TestRecordCancelTakesAStoppedRunOffTheInbox(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	records := NewRecords(client)
	stoppedAt := inboxBase.Add(-time.Hour)
	listed := func(id string) bool {
		got, err := records.Inbox(ctx, inboxBase.Add(-24*time.Hour), 50)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(got, func(r InboxRun) bool { return r.Run == id })
	}
	for _, tt := range []struct {
		name string
		row  map[string]any
	}{
		{"stopped by state", map[string]any{stateField: stateStopped, outcomeField: string(runner.OutcomeStopped), stopReasonField: string(runner.StopWaitingOnOwner)}},
		{"a finished claim", map[string]any{stateField: stateClaimed, outcomeField: string(runner.OutcomeAgentFailed), claimedAtField: stoppedAt.Add(-time.Hour), settledAtField: stoppedAt}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := fmt.Sprintf("cancel-inbox-%d", time.Now().UnixNano())
			forget(t, client, id)
			tt.row[updatedAtField] = stoppedAt
			if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, tt.row); err != nil {
				t.Fatal(err)
			}
			if !listed(id) {
				t.Fatal("the stopped run is not in the inbox before the cancel")
			}
			if err := records.RecordCancel(ctx, id, "owner@example.com", inboxBase); err != nil {
				t.Fatal(err)
			}
			if listed(id) {
				t.Fatal("the cancelled run is still in the inbox")
			}
		})
	}
}

func TestRecordCancelOfTwoRequestsRecordsOneAndRefusesTheOther(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	id := fmt.Sprintf("cancel-race-%d", time.Now().UnixNano())
	forget(t, client, id)
	doc := client.Collection(runsCollection).Doc(id)
	if _, err := doc.Set(ctx, map[string]any{outcomeField: string(runner.OutcomePROpened)}); err != nil {
		t.Fatal(err)
	}
	records := NewRecords(client)
	errs := make(chan error, 2)
	for _, by := range []string{"a@example.com", "b@example.com"} {
		go func() { errs <- records.RecordCancel(ctx, id, by, time.Now()) }()
	}
	var recorded, refused int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			recorded++
		case errors.Is(err, ErrRunCancelled):
			refused++
		default:
			t.Fatalf("err = %v", err)
		}
	}
	if recorded != 1 || refused != 1 {
		t.Fatalf("recorded %d, refused %d, want one of each", recorded, refused)
	}
}

func TestRunRepositoryReadsWhatTheQueueWroteAndRefusesAnAbsentRun(t *testing.T) {
	_, client := queue(t)
	ctx := context.Background()
	id := fmt.Sprintf("repo-%d", time.Now().UnixNano())
	absent := id + "-absent"
	forget(t, client, id)
	forget(t, client, absent)
	if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, map[string]any{"repo": "octo/scratch", "pr_url": "https://github.com/octo/other/pull/7"}); err != nil {
		t.Fatal(err)
	}
	recs := NewRecords(client)
	if got, err := recs.RunRepository(ctx, id); err != nil || got != "octo/scratch" {
		t.Fatalf("RunRepository = %q, %v, want the queued repository and not the PR's", got, err)
	}
	if _, err := recs.RunRepository(ctx, absent); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want ErrRecordNotFound", err)
	}
}
