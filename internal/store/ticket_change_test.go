package store

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// failingCommitQueue is a Queue whose commits touching failDoc fail, so a test
// can prove a stamp failure is reported rather than swallowed.
func failingCommitQueue(t *testing.T, failDoc string) *Queue {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	fail := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if commit, ok := req.(*firestorepb.CommitRequest); ok {
			for _, w := range commit.Writes {
				if strings.HasSuffix(w.Document.GetName(), "/"+failDoc) {
					return status.Error(codes.PermissionDenied, "commit refused")
				}
			}
		}
		return invoker(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer owner"), method, req, reply, cc, opts...)
	}
	conn, err := grpc.NewClient(os.Getenv("FIRESTORE_EMULATOR_HOST"),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithChainUnaryInterceptor(fail))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := firestore.NewClient(context.Background(), "forge-wingman-test", option.WithGRPCConn(conn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewQueue(client)
}

func TestRecordTicketChangesStampsOnlyAnInFlightRunWhoseTicketDiffers(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	prefix := fresh(fmt.Sprintf("ticket-change-%d", time.Now().UnixNano()))
	id := func(name string) string { return prefix + "-" + name }
	seed := func(name string, fields map[string]any) string {
		runID := id(name)
		fields[ticketIDField] = runID
		seedRow(t, client, runID, fields)
		return runID
	}
	claimed := seed("claimed", map[string]any{stateField: stateClaimed, ticketBodyField: "old"})
	planned := seed("planned", map[string]any{stateField: statePlanned, ticketBodyField: "old"})
	queued := seed("queued", map[string]any{stateField: stateQueued, ticketBodyField: "old"})
	prOpen := seed("pr-open", map[string]any{
		stateField: stateClaimed, ticketBodyField: "old",
		prURLField: "https://github.com/octo/scratch/pull/7",
	})
	ended := seed("ended", map[string]any{
		stateField: stateClaimed, ticketBodyField: "old",
		outcomeField: string(runner.OutcomeBuilt),
	})
	matches := seed("matches", map[string]any{stateField: stateClaimed, ticketBodyField: "same"})
	notDelegated := seed("not-delegated", map[string]any{stateField: stateClaimed, ticketBodyField: "old"})

	tickets := map[string]string{}
	for _, runID := range []string{claimed, planned, queued, prOpen, ended} {
		tickets[runID] = "new"
	}
	tickets[matches] = "same"

	changed, err := q.RecordTicketChanges(ctx, tickets, queueAt)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{claimed, planned}
	slices.Sort(changed)
	slices.Sort(want)
	if !slices.Equal(changed, want) {
		t.Fatalf("changed = %q, want the claimed and planned runs", changed)
	}
	for _, runID := range []string{claimed, planned} {
		row := readRow(t, client, runID)
		if at, _ := row[ticketChangedAtField].(time.Time); !at.Equal(queueAt) {
			t.Fatalf("run %s's ticket change time = %v, want %v", runID, at, queueAt)
		}
		if row[ticketChangedBodyField] != "new" {
			t.Fatalf("run %s's changed description = %v, want the new text", runID, row[ticketChangedBodyField])
		}
	}
	for _, runID := range []string{queued, prOpen, ended, matches, notDelegated} {
		if v, ok := readRow(t, client, runID)[ticketChangedAtField]; ok {
			t.Fatalf("run %s's ticket change time = %v, want none", runID, v)
		}
	}

	changed, err = q.RecordTicketChanges(ctx, tickets, queueAt.Add(time.Minute))
	if err != nil || len(changed) != 0 {
		t.Fatalf("second poll changed = %q, %v, want nothing written again", changed, err)
	}
	if at, _ := readRow(t, client, claimed)[ticketChangedAtField].(time.Time); !at.Equal(queueAt) {
		t.Fatalf("claimed run's ticket change time = %v, want the first sighting %v", at, queueAt)
	}
}

func TestRecordTicketChangesRefreshesTheStoredDescriptionOnALaterEdit(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh(fmt.Sprintf("ticket-change-later-%d", time.Now().UnixNano()))
	seedRow(t, client, id, map[string]any{
		stateField: stateClaimed, ticketIDField: id, ticketBodyField: "built",
	})
	changed, err := q.RecordTicketChanges(ctx, map[string]string{id: "first"}, queueAt)
	if err != nil || !slices.Equal(changed, []string{id}) {
		t.Fatalf("first poll changed = %q, %v, want the run stamped", changed, err)
	}
	changed, err = q.RecordTicketChanges(ctx, map[string]string{id: "second"}, queueAt.Add(time.Minute))
	if err != nil || len(changed) != 0 {
		t.Fatalf("second poll changed = %q, %v, want no second sighting", changed, err)
	}
	row := readRow(t, client, id)
	if at, _ := row[ticketChangedAtField].(time.Time); !at.Equal(queueAt) {
		t.Fatalf("ticket change time = %v, want the first sighting %v", at, queueAt)
	}
	if row[ticketChangedBodyField] != "second" {
		t.Fatalf("stored description = %v, want the later edit", row[ticketChangedBodyField])
	}
	change, err := q.TicketChange(ctx, id)
	if err != nil || !change.At.Equal(queueAt) || change.Body != "second" {
		t.Fatalf("TicketChange = %+v, %v, want the first time and the later edit", change, err)
	}
}

func TestRecordTicketChangesReadsNothingWithoutTickets(t *testing.T) {
	q, queries, _, _ := countingQueue(t, false)
	changed, err := q.RecordTicketChanges(context.Background(), nil, queueAt)
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed = %q, %v, want no read and no change", changed, err)
	}
	if got := queries.Load(); got != 0 {
		t.Fatalf("ran %d queries for no tickets, want none", got)
	}
}

func TestRecordTicketChangesIgnoresARunWithNoDelegatedTicket(t *testing.T) {
	q, _ := queue(t)
	changed, err := q.RecordTicketChanges(context.Background(),
		map[string]string{fresh("ticket-change-missing"): "new"}, queueAt)
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed = %q, %v, want no change", changed, err)
	}
}

func TestRecordTicketChangesReturnsAnIteratorFailure(t *testing.T) {
	q, _ := queue(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.RecordTicketChanges(ctx, map[string]string{"any": "new"}, queueAt); err == nil {
		t.Fatal("err = nil, want the canceled read's failure")
	}
}

func TestRecordTicketChangesStopsOnAStampFailure(t *testing.T) {
	_, client := queue(t)
	id := fresh("ticket-change-stamp-failure")
	seedRow(t, client, id, map[string]any{
		stateField: stateClaimed, ticketIDField: id, ticketBodyField: "old",
	})
	failing := failingCommitQueue(t, id)
	if _, err := failing.RecordTicketChanges(context.Background(), map[string]string{id: "new"}, queueAt); err == nil {
		t.Fatal("err = nil, want the refused stamp's failure")
	}
}

func TestRecordTicketChangeRefusesWhatItMustNotStamp(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	for _, tt := range []struct {
		name     string
		row      map[string]any
		body     string
		wantAt   time.Time
		wantBody string
	}{
		{"a queued run", map[string]any{stateField: stateQueued, ticketBodyField: "old"}, "new", time.Time{}, ""},
		{"an ended run", map[string]any{stateField: stateClaimed, ticketBodyField: "old", outcomeField: string(runner.OutcomeBuilt)}, "new", time.Time{}, ""},
		{"a run whose PR opened", map[string]any{stateField: stateClaimed, ticketBodyField: "old", prURLField: "https://github.com/octo/scratch/pull/7"}, "new", time.Time{}, ""},
		{"a matching ticket", map[string]any{stateField: stateClaimed, ticketBodyField: "same"}, "same", time.Time{}, ""},
		{"a stamped run with the same description", map[string]any{stateField: stateClaimed, ticketBodyField: "old", ticketChangedAtField: queueAt, ticketChangedBodyField: "new"}, "new", queueAt, "new"},
		{"a stamped run with a later description", map[string]any{stateField: stateClaimed, ticketBodyField: "old", ticketChangedAtField: queueAt, ticketChangedBodyField: "first"}, "second", queueAt, "second"},
		{"a stamped run whose ticket reverted", map[string]any{stateField: stateClaimed, ticketBodyField: "old", ticketChangedAtField: queueAt, ticketChangedBodyField: "changed"}, "old", queueAt, "old"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := fresh("ticket-change-refuse")
			row := map[string]any{ticketIDField: id}
			for k, v := range tt.row {
				row[k] = v
			}
			seedRow(t, client, id, row)
			ref := client.Collection(runsCollection).Doc(id)
			stamped, err := q.recordTicketChange(ctx, ref, tt.body, queueAt.Add(time.Minute))
			if err != nil || stamped {
				t.Fatalf("stamped = %v, %v, want no first sighting", stamped, err)
			}
			got := readRow(t, client, id)
			if at, _ := got[ticketChangedAtField].(time.Time); !at.Equal(tt.wantAt) {
				t.Fatalf("ticket change time = %v, want %v", at, tt.wantAt)
			}
			if body := str(got, ticketChangedBodyField); body != tt.wantBody {
				t.Fatalf("stored description = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestRecordTicketChangeReadsNoMissingRun(t *testing.T) {
	q, client := queue(t)
	id := fresh("ticket-change-missing-run")
	ref := client.Collection(runsCollection).Doc(id)
	stamped, err := q.recordTicketChange(context.Background(), ref, "new", queueAt)
	if err != nil || stamped {
		t.Fatalf("stamped = %v, %v, want no change for a missing run", stamped, err)
	}
}

func TestRecordTicketChangeReturnsATransactionFailure(t *testing.T) {
	q, client := queue(t)
	id := fresh("ticket-change-tx-failure")
	seedRow(t, client, id, map[string]any{stateField: stateClaimed, ticketBodyField: "old"})
	ref := client.Collection(runsCollection).Doc(id)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.recordTicketChange(ctx, ref, "new", queueAt); err == nil {
		t.Fatal("err = nil, want the canceled transaction's failure")
	}
}

func TestTicketChangeReadsTheRecordedChange(t *testing.T) {
	q, client := queue(t)
	id := fresh(fmt.Sprintf("ticket-change-read-%d", time.Now().UnixNano()))
	seedRow(t, client, id, map[string]any{
		stateField: stateClaimed, ticketIDField: id, ticketBodyField: "old",
		ticketChangedAtField: queueAt, ticketChangedBodyField: "new",
	})
	got, err := q.TicketChange(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(queueAt) || got.Body != "new" {
		t.Fatalf("TicketChange = %+v, want %v and the new text", got, queueAt)
	}
}

func TestTicketChangeReadsNoChangeForAMissingRun(t *testing.T) {
	q, _ := queue(t)
	got, err := q.TicketChange(context.Background(), fresh("ticket-change-no-run"))
	if err != nil || !got.At.IsZero() || got.Body != "" {
		t.Fatalf("TicketChange = %+v, %v, want the zero value", got, err)
	}
}

func TestTicketChangeReturnsAReadFailure(t *testing.T) {
	q, client := queue(t)
	id := fresh("ticket-change-read-failure")
	seedRow(t, client, id, map[string]any{stateField: stateClaimed})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.TicketChange(ctx, id); err == nil {
		t.Fatal("err = nil, want the canceled read's failure")
	}
}

func TestRefreshQueuedTicketClearsAStaleChange(t *testing.T) {
	q, client := queue(t)
	id := fresh(fmt.Sprintf("ticket-change-refresh-%d", time.Now().UnixNano()))
	run := queuedRun(id, 1)
	row := ticketRow(run, stateQueued)
	row[ticketChangedAtField] = queueAt
	row[ticketChangedBodyField] = "new"
	seedRow(t, client, id, row)
	if _, err := q.RefreshQueuedTicket(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if v, ok := readRow(t, client, id)[ticketChangedAtField]; ok {
		t.Fatalf("ticket change time = %v after a queued refresh, want it cleared", v)
	}
}

func TestRequeueClearsAStaleChange(t *testing.T) {
	q, client := queue(t)
	id := fresh("ticket-change-requeue")
	seedRow(t, client, id, map[string]any{
		stateField: stateStopped, outcomeField: string(runner.OutcomeStopped),
		stopReasonField: string(runner.StopWaitingOnOwner),
		ticketChangedAtField: queueAt, ticketChangedBodyField: "new",
	})
	if _, err := q.Requeue(context.Background(), id, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if v, ok := readRow(t, client, id)[ticketChangedAtField]; ok {
		t.Fatalf("ticket change time = %v after a requeue, want it cleared", v)
	}
}

func TestRequeueToPlannedKeepsTheRecordedChange(t *testing.T) {
	q, client := queue(t)
	id := fresh("ticket-change-requeue-planned")
	seedRow(t, client, id, map[string]any{
		stateField: stateStopped, outcomeField: string(runner.OutcomeStopped),
		stopReasonField: string(runner.StopWaitingOnOwner),
		planFilesField: []string{"a.go"}, planSettledAtField: queueAt,
		ticketChangedAtField: queueAt, ticketChangedBodyField: "new",
	})
	result, err := q.Requeue(context.Background(), id, queueAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.State != statePlanned {
		t.Fatalf("requeue state = %q, want planned", result.State)
	}
	if v, ok := readRow(t, client, id)[ticketChangedAtField]; !ok || v == nil {
		t.Fatalf("ticket change time = %v after a planned requeue, want it kept", v)
	}
}

func TestReleaseClearsAStaleChange(t *testing.T) {
	q, client := queue(t)
	id := fresh("ticket-change-release")
	seedRow(t, client, id, map[string]any{
		stateField: stateClaimed, claimedAtField: queueAt,
		ticketChangedAtField: queueAt, ticketChangedBodyField: "new",
	})
	if err := q.Release(context.Background(), id, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if v, ok := readRow(t, client, id)[ticketChangedAtField]; ok {
		t.Fatalf("ticket change time = %v after a release, want it cleared", v)
	}
}

func TestReleaseToPlannedKeepsTheRecordedChange(t *testing.T) {
	q, client := queue(t)
	id := fresh("ticket-change-release-planned")
	seedRow(t, client, id, map[string]any{
		stateField: stateClaimed, claimedAtField: queueAt,
		ticketChangedAtField: queueAt, ticketChangedBodyField: "new",
	})
	resetLedger(t, client, 1)
	ledger := readLedgerDoc(t, client)
	if ledger.Reservations == nil {
		ledger.Reservations = map[string]reservationEntry{}
	}
	ledger.Reservations[reservationKey(id, 0, dispatcher.StageBuild)] = reservationEntry{Stage: dispatcher.StageBuild}
	if _, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Set(context.Background(), ledger); err != nil {
		t.Fatal(err)
	}
	if err := q.Release(context.Background(), id, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if v, ok := readRow(t, client, id)[ticketChangedAtField]; !ok || v == nil {
		t.Fatalf("ticket change time = %v after a planned release, want it kept", v)
	}
}
