package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

func TestRecordTicketsClosedMarksOnlyStoppedRunsOfClosedTicketsOnce(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	prefix := fresh(fmt.Sprintf("ticket-closed-%d", time.Now().UnixNano()))
	stoppedAt := inboxBase.Add(-time.Hour)
	earlier := inboxBase.Add(-30 * time.Minute)
	stopped := func(extra map[string]any) map[string]any {
		row := map[string]any{
			stateField: stateStopped, outcomeField: string(runner.OutcomeStopped),
			stopReasonField: string(runner.StopWaitingOnOwner), updatedAtField: stoppedAt,
		}
		for k, v := range extra {
			row[k] = v
		}
		return row
	}
	rows := map[string]map[string]any{
		"closed":    stopped(nil),
		"open":      stopped(nil),
		"marked":    stopped(map[string]any{ticketClosedAtField: earlier}),
		"cancelled": stopped(map[string]any{cancelledByOwnerAtField: earlier, cancelledByField: "owner@example.com"}),
		"pr open": {
			stateField: stateClaimed, outcomeField: string(runner.OutcomePROpened),
			prURLField: "https://github.com/octo/scratch/pull/7", updatedAtField: stoppedAt,
		},
	}
	id := func(name string) string { return prefix + "-" + name }
	for name, row := range rows {
		row[ticketIDField] = id(name)
		seedRow(t, client, id(name), row)
	}
	closed := []string{id("closed"), id("marked"), id("cancelled"), id("pr open")}

	marked, err := q.RecordTicketsClosed(ctx, closed, inboxBase)
	if err != nil || marked != 1 {
		t.Fatalf("marked = %d, %v, want only the stopped run of a closed ticket", marked, err)
	}
	if at, _ := readRow(t, client, id("closed"))[ticketClosedAtField].(time.Time); !at.Equal(inboxBase) {
		t.Fatalf("closed run's ticket-closed time = %v, want %v", at, inboxBase)
	}
	if row := readRow(t, client, id("closed")); row[cancelledByOwnerAtField] != nil || row[cancelledByField] != nil {
		t.Fatalf("closed run = %v, want no cancel written", row)
	}
	if at, _ := readRow(t, client, id("marked"))[ticketClosedAtField].(time.Time); !at.Equal(earlier) {
		t.Fatalf("marked run's ticket-closed time = %v, want the earlier poll's %v", at, earlier)
	}
	for _, name := range []string{"open", "cancelled", "pr open"} {
		if v, ok := readRow(t, client, id(name))[ticketClosedAtField]; ok {
			t.Fatalf("%s run's ticket-closed time = %v, want none", name, v)
		}
	}
	if marked, err := q.RecordTicketsClosed(ctx, closed, inboxBase.Add(time.Minute)); err != nil || marked != 0 {
		t.Fatalf("second poll marked = %d, %v, want nothing written again", marked, err)
	}

	inbox, err := NewRecords(client).Inbox(ctx, inboxBase.Add(-24*time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, r := range inbox {
		if r.Stopped {
			listed = append(listed, r.Run)
		}
	}
	if want := []string{id("open")}; !slices.Equal(listed, want) {
		t.Fatalf("stopped runs in the inbox = %v, want only the open ticket's", listed)
	}
}

func TestRequeueClearsTheTicketClosedTime(t *testing.T) {
	q, client := queue(t)
	id := fresh(fmt.Sprintf("ticket-closed-requeue-%d", time.Now().UnixNano()))
	seedRow(t, client, id, map[string]any{
		stateField: stateStopped, outcomeField: string(runner.OutcomeStopped),
		stopReasonField: string(runner.StopWaitingOnOwner), ticketClosedAtField: queueAt,
	})
	if _, err := q.Requeue(context.Background(), id, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if v, ok := readRow(t, client, id)[ticketClosedAtField]; ok {
		t.Fatalf("ticket-closed time = %v after a requeue, want it cleared", v)
	}
}
