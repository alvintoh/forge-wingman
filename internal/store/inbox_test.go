package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

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
	if first.Ticket != "ABC-1" || first.Repo != "octo/scratch" || first.Size != "M" || first.WaitReason != runner.WaitFrontEnd ||
		first.PRURL != "https://github.com/octo/scratch/pull/7" || !first.UpdatedAt.Equal(inboxBase.Add(-time.Hour)) {
		t.Fatalf("first = %+v, want the row's ticket, repo, size, PR and reason", first)
	}

	capped, err := NewRecords(client).Inbox(ctx, inboxBase.Add(-24*time.Hour), 1)
	if err != nil || len(capped) != 1 || capped[0].Run != got[0].Run {
		t.Fatalf("capped = %v, %v, want the newest alone", capped, err)
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
