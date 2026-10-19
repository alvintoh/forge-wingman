package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

var queueAt = time.Date(2026, 9, 27, 9, 15, 0, 0, time.UTC)

var queueSeq atomic.Uint64

// fresh suffixes an id, because the emulator outlives one run of the suite: a
// document an interrupted run left behind must not read as this run's own.
func fresh(id string) string {
	return id + "-" + strconv.FormatUint(queueSeq.Add(1), 10)
}

// queue returns a Queue over the emulator, with a cleanup for the documents
// forget() is given: one test's rows are its own, since the ids name the run.
func queue(t *testing.T) (*Queue, *firestore.Client) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "forge-wingman-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewQueue(client), client
}

// forget removes the run and refusal documents one test wrote, and its
// ledger reservation.
func forget(t *testing.T, client *firestore.Client, ids ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range ids {
			_, _ = client.Collection(runsCollection).Doc(id).Delete(ctx)
			_, _ = client.Collection(dispatchCollection).Doc(rejectedPrefix + id).Delete(ctx)
			for _, key := range []string{reservationKey(id, 0, dispatcher.StageBuild), reservationKey(id, 0, dispatcher.StagePlan), legacyReservationKey(id, dispatcher.StageBuild)} {
				_, _ = client.Collection(dispatchCollection).Doc(ledgerDocID).Update(ctx,
					[]firestore.Update{{FieldPath: firestore.FieldPath{"reservations", key}, Value: firestore.Delete}})
			}
		}
	})
}

// queuedRun is a run the dispatcher would admit.
func queuedRun(id string, priority int) dispatcher.Queued {
	return dispatcher.Queued{
		RunID:    id,
		Ticket:   runner.Ticket{ID: id, Title: "feat(x): add a file", Size: "M", SizedBy: "linear-label", Body: "Add a file."},
		Repo:     "octo/scratch",
		Priority: priority,
		At:       queueAt,
	}
}

// generousBudget fits any candidate this suite claims, so a test not
// exercising the budget itself never has to think about it.
var generousBudget = dispatcher.BudgetConfig{
	ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 1000 * money.Dollar}},
	Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
	Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
}

// openFacts are the facts outside the ledger under which nothing but the
// ledger and the budget can withhold a run.
var openFacts = dispatcher.Facts{Limits: dispatcher.DefaultLimits, OpenPRsKnown: true}

// resetLedger replaces the shared ledger with an empty one whose N is n, so a
// test's admission starts from a known state, and drops it afterwards.
func resetLedger(t *testing.T, client *firestore.Client, n int) {
	t.Helper()
	ref := client.Collection(dispatchCollection).Doc(ledgerDocID)
	if _, err := ref.Set(context.Background(), ledgerDoc{Reservations: map[string]reservationEntry{}, N: n}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = ref.Delete(context.Background()) })
}

// attemptOf is the settling run's attempt as Settle takes it.
func attemptOf(n int) *int { return &n }

// readLedgerDoc reads the shared ledger as the store keeps it.
func readLedgerDoc(t *testing.T, client *firestore.Client) ledgerDoc {
	t.Helper()
	snap, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ledger ledgerDoc
	if err := snap.DataTo(&ledger); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestEnqueueStoresTheBlockingRelationsOnTheRow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-enq-relations"), 2)
	run.BlockedBy, run.Blocks = []string{"run-p"}, []string{"run-x"}
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := subject(snap.Data())
	if !slices.Equal(got.BlockedBy, []string{"run-p"}) || !slices.Equal(got.Blocks, []string{"run-x"}) {
		t.Fatalf("subject = %+v, want the relations read back as enqueued", got)
	}
	if _, has := snap.Data()[blocksField]; !has {
		t.Fatalf("queued row = %v, want a blocks column", snap.Data())
	}
}

func TestEnqueueWritesTheRunRecordQueued(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-enq"), 2)
	run.Private = true
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data[stateField] != stateQueued || data[priorityField] != int64(2) || data[repoField] != "octo/scratch" ||
		data[privateField] != true {
		t.Fatalf("queued row = %v", data)
	}
	for _, field := range []string{blockedByField, blocksField} {
		if _, has := data[field]; has {
			t.Fatalf("queued row = %v, want no %s column for a ticket with none", data, field)
		}
	}
	var rec runner.Record
	if err := snap.DataTo(&rec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec.Ticket(), run.Ticket) || !rec.StartedAt.Equal(queueAt) || !rec.UpdatedAt.Equal(queueAt) || !rec.Private {
		t.Fatalf("record = %+v", rec)
	}
	// A poll that finds the same ticket delegated again must not start a second run.
	if err := q.Enqueue(ctx, run); !errors.Is(err, dispatcher.ErrAlreadyQueued) {
		t.Fatalf("err = %v, want ErrAlreadyQueued", err)
	}
}

func TestEnqueueStoresTheModelsTheTicketNamedOnTheRunRecord(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	named, unnamed := queuedRun(fresh("queue-enq-models"), 2), queuedRun(fresh("queue-enq-nomodels"), 2)
	named.Models = runner.ModelLabels{Build: "p/a", Review: "p/b", Plan: []string{"p/c", "p/d"}}
	forget(t, client, named.RunID, unnamed.RunID)
	for _, run := range []dispatcher.Queued{named, unnamed} {
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NewRecords(client).GetRecord(ctx, named.RunID)
	if err != nil || got.ModelLabels.Build != "p/a" || got.ModelLabels.Review != "p/b" ||
		!slices.Equal(got.ModelLabels.Plan, []string{"p/c", "p/d"}) {
		t.Fatalf("record %+v, err %v, want the named models read back", got.ModelLabels, err)
	}
	got, err = NewRecords(client).GetRecord(ctx, unnamed.RunID)
	if err != nil || got.ModelLabels.Build != "" || got.ModelLabels.Review != "" || len(got.ModelLabels.Plan) != 0 {
		t.Fatalf("record %+v, err %v, want no named model", got.ModelLabels, err)
	}
}

func TestRejectRecordsTheRefusalAndEnqueueClearsIt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rej"), 1)
	forget(t, client, run.RunID)
	r := dispatcher.Rejection{Ticket: run.RunID, Reason: dispatcher.RefusalNoSize,
		Detail: "no size: label", At: queueAt}
	if err := q.Reject(ctx, r); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection("dispatch").Doc("rejected-" + run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()["reason"] != string(r.Reason) || snap.Data()["detail"] != r.Detail ||
		snap.Data()["at"] != queueAt {
		t.Fatalf("refusal = %v", snap.Data())
	}
	if _, err := client.Collection("dispatch").Doc("rejected-" + fresh("never-refused")).Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want no refusal to be found", err)
	}
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection("dispatch").Doc("rejected-" + run.RunID).Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("a refusal outlived the run that superseded it: %v", err)
	}
}

func TestCandidatesListsQueuedRunsInPriorityOrder(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	// The suffixes sort after the names, so the ids cannot stand in for the
	// order: only linear_priority can.
	var ids []string
	priorities := map[string]int{}
	for _, queued := range [][2]int{{1, 1}, {3, 2}, {5, 3}} {
		id := fresh(strconv.Itoa(queued[1]))
		ids = append(ids, id)
		priorities[id] = queued[0]
	}
	forget(t, client, ids...)
	for id, priority := range priorities {
		run := queuedRun(id, priority)
		run.Ticket.Size = "S"
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]dispatcher.Candidate{}
	var order []string
	for _, c := range cands {
		if _, ours := priorities[c.RunID]; !ours {
			continue // another test's row, from a shared collection
		}
		byID[c.RunID] = c
		order = append(order, c.RunID)
	}
	want := slices.Sorted(maps.Keys(priorities))
	if !slices.Equal(order, want) {
		t.Fatalf("candidate order = %v, want %v", order, want)
	}
	for id := range priorities {
		if byID[id].Repo != "octo/scratch" || byID[id].Size != "S" || byID[id].Priority == 0 {
			t.Fatalf("candidate %s = %+v", id, byID[id])
		}
	}
}

// ownOrder is the order Candidates lists the given runs in, leaving out the
// rows other tests wrote to the shared collection.
func ownOrder(t *testing.T, q *Queue, ids ...string) []string {
	t.Helper()
	cands, err := q.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range cands {
		if slices.Contains(ids, c.RunID) {
			order = append(order, c.RunID)
		}
	}
	return order
}

func TestCandidatesCarryThePRARunSupersedes(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id, plain := fresh("superseding"), fresh("plain")
	forget(t, client, id, plain)
	for _, rid := range []string{id, plain} {
		if err := q.Enqueue(ctx, queuedRun(rid, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{{Path: supersededPRField, Value: int64(141)}}); err != nil {
		t.Fatal(err)
	}
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range cands {
		got[c.RunID] = c.SupersededPR
	}
	if got[id] != 141 || got[plain] != 0 {
		t.Fatalf("superseded PRs = %v, want 141 for the superseding run and none for the other", got)
	}
}

func TestCandidatesBreaksAPriorityTieByQueuedAt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	late, early, middle := fresh("tie-late"), fresh("tie-early"), fresh("tie-middle")
	forget(t, client, late, early, middle)
	for id, at := range map[string]time.Time{late: queueAt.Add(2 * time.Minute), early: queueAt, middle: queueAt.Add(time.Minute)} {
		run := queuedRun(id, 3)
		run.At = at
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := ownOrder(t, q, late, early, middle), []string{early, middle, late}; !slices.Equal(got, want) {
		t.Fatalf("candidate order = %v, want %v", got, want)
	}
}

func TestCandidatesPutsARequeuedRunBehindANewerOne(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	requeued, newer := fresh("tie-requeued"), fresh("tie-newer")
	seedRow(t, client, requeued, map[string]any{stateField: stateStopped, priorityField: int64(3), queuedAtField: queueAt})
	forget(t, client, newer)
	run := queuedRun(newer, 3)
	run.At = queueAt.Add(time.Minute)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Requeue(ctx, requeued, queueAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, want := ownOrder(t, q, requeued, newer), []string{newer, requeued}; !slices.Equal(got, want) {
		t.Fatalf("candidate order = %v, want %v", got, want)
	}
}

func TestCandidatesLeavesOutARowWithNoPriority(t *testing.T) {
	q, client := queue(t)
	noPriority := fresh("tie-no-priority")
	seedRow(t, client, noPriority, map[string]any{stateField: stateQueued})
	if got := ownOrder(t, q, noPriority); len(got) != 0 {
		t.Fatalf("listed a row with no priority: %v", got)
	}
}

func TestCandidatesStaysWithinTheScanLimit(t *testing.T) {
	q, client := queue(t)
	for range candidateScanLimit + 1 {
		seedRow(t, client, fresh("scan-limit"), map[string]any{stateField: stateQueued, priorityField: int64(1), queuedAtField: queueAt})
	}
	cands, err := q.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != candidateScanLimit {
		t.Fatalf("candidates = %d, want %d", len(cands), candidateScanLimit)
	}
}

func TestCandidatesLogsARowTheOrderedQueryCannotList(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"no queued_at": {stateField: stateQueued, priorityField: int64(3)},
		"no priority":  {stateField: stateQueued, queuedAtField: queueAt},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			var logged bytes.Buffer
			q = q.WithLogger(slog.New(slog.NewJSONHandler(&logged, nil)))
			ctx := context.Background()
			queuedBefore, listedBefore, err := q.countUnlisted(ctx)
			if err != nil {
				t.Fatal(err)
			}
			id := fresh("tie-unlisted")
			seedRow(t, client, id, fields)
			if got := ownOrder(t, q, id); len(got) != 0 {
				t.Fatalf("listed the row: %v", got)
			}
			queued, listed, err := q.countUnlisted(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if queued-queuedBefore != 1 || listed != listedBefore {
				t.Fatalf("counts moved by %d queued and %d listed, want 1 and 0", queued-queuedBefore, listed-listedBefore)
			}
			var rec struct {
				Msg    string `json:"msg"`
				Queued int64  `json:"queued"`
				Listed int64  `json:"listed"`
			}
			if err := json.NewDecoder(&logged).Decode(&rec); err != nil {
				t.Fatalf("no log record: %v", err)
			}
			if rec.Msg != "queuedRowsNotListed" || rec.Queued <= rec.Listed {
				t.Fatalf("log = %+v, want queuedRowsNotListed with more queued than listed", rec)
			}
		})
	}
}

// countingQueue returns a Queue whose gRPC calls are counted by method, and
// whose aggregation queries fail when failCounts is set. commits counts the
// document writes the queue commits.
func countingQueue(t *testing.T, failCounts bool) (q *Queue, queries, counts, commits *atomic.Int64) {
	t.Helper()
	queries, counts, commits = new(atomic.Int64), new(atomic.Int64), new(atomic.Int64)
	return NewQueue(countingClient(t, new(atomic.Int64), queries, counts, commits, failCounts)), queries, counts, commits
}

// countingClient returns a Firestore client over the emulator that counts the
// documents its reads return (reads), its queries (queries), its aggregation
// queries (counts) and the document writes it commits (commits); a set
// failCounts refuses every aggregation query, so a test can prove a caller runs
// without them.
func countingClient(t *testing.T, reads, queries, counts, commits *atomic.Int64, failCounts bool) *firestore.Client {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	countCommits := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if commit, ok := req.(*firestorepb.CommitRequest); ok {
			commits.Add(int64(len(commit.Writes)))
		}
		return invoker(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer owner"), method, req, reply, cc, opts...)
	}
	intercept := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		switch {
		case strings.HasSuffix(method, "/RunQuery"):
			queries.Add(1)
		case strings.HasSuffix(method, "/RunAggregationQuery"):
			counts.Add(1)
			if failCounts {
				return nil, status.Error(codes.PermissionDenied, "count refused")
			}
		}
		stream, err := streamer(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer owner"), desc, cc, method, opts...)
		if err != nil || !carriesDocuments(method) {
			return stream, err
		}
		return &documentStream{ClientStream: stream, reads: reads}, nil
	}
	conn, err := grpc.NewClient(os.Getenv("FIRESTORE_EMULATOR_HOST"),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithChainStreamInterceptor(intercept), grpc.WithChainUnaryInterceptor(countCommits))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := firestore.NewClient(context.Background(), "forge-wingman-test", option.WithGRPCConn(conn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// carriesDocuments reports whether method's responses each carry one document
// read: a query returns a row per document, and a batch read — which is what a
// point read and a transaction's read use — returns the document asked for.
func carriesDocuments(method string) bool {
	return strings.HasSuffix(method, "/RunQuery") || strings.HasSuffix(method, "/BatchGetDocuments")
}

// documentStream counts the documents the responses of one read carry.
type documentStream struct {
	grpc.ClientStream
	reads *atomic.Int64
}

func (s *documentStream) RecvMsg(m any) error {
	if err := s.ClientStream.RecvMsg(m); err != nil {
		return err
	}
	switch msg := m.(type) {
	case *firestorepb.RunQueryResponse:
		if msg.GetDocument() != nil {
			s.reads.Add(1)
		}
	case *firestorepb.BatchGetDocumentsResponse:
		if msg.GetFound() != nil {
			s.reads.Add(1)
		}
	}
	return nil
}

func TestCandidatesReadsOneOrderedQuery(t *testing.T) {
	q, queries, counts, _ := countingQueue(t, false)
	if _, err := q.Candidates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queries.Load() != 1 || counts.Load() != 2 {
		t.Fatalf("Candidates ran %d row queries and %d counts, want 1 and 2", queries.Load(), counts.Load())
	}
}

func TestCandidatesStillListsWhenTheCountsFail(t *testing.T) {
	q, queries, _, _ := countingQueue(t, true)
	var logged bytes.Buffer
	q = q.WithLogger(slog.New(slog.NewJSONHandler(&logged, nil)))
	if _, err := q.Candidates(context.Background()); err != nil {
		t.Fatalf("a failed count stopped the poll: %v", err)
	}
	if queries.Load() != 1 || !strings.Contains(logged.String(), `"msg":"queuedRowCountFailed"`) {
		t.Fatalf("row queries = %d, log = %q, want 1 and queuedRowCountFailed", queries.Load(), logged.String())
	}
}

func TestUpdateQueuedPriorityRewritesOnlyAQueuedRun(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	for _, tt := range []struct {
		name    string
		state   string
		rank    int
		applied bool
		want    int64
	}{
		{"queued at another rank", stateQueued, 1, true, 1},
		{"queued at the same rank", stateQueued, 3, true, 3},
		{"planned", statePlanned, 1, true, 1},
		{"claimed", stateClaimed, 1, false, 3},
		{"waiting", stateWaiting, 1, false, 3},
		{"stopped", stateStopped, 1, false, 3},
		{"abandoned", stateAbandoned, 1, false, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := fresh("queue-reprioritize")
			seedRow(t, client, id, map[string]any{stateField: tt.state, priorityField: int64(3)})
			applied, err := q.UpdateQueuedPriority(ctx, id, tt.rank)
			if err != nil || applied != tt.applied {
				t.Fatalf("applied = %v, err %v, want %v", applied, err, tt.applied)
			}
			if got := readRow(t, client, id)[priorityField]; got != tt.want {
				t.Fatalf("priority = %v, want %d", got, tt.want)
			}
		})
	}
	if applied, err := q.UpdateQueuedPriority(ctx, fresh("queue-reprioritize-missing"), 1); err != nil || applied {
		t.Fatalf("missing run: applied = %v, err %v, want a no-op", applied, err)
	}
}

func TestUpdateQueuedPriorityWritesNothingForAnUnchangedRank(t *testing.T) {
	q, _, _, commits := countingQueue(t, false)
	id := fresh("queue-reprioritize-same")
	seedRow(t, q.client, id, map[string]any{stateField: stateQueued, priorityField: int64(3)})
	seeded := commits.Load()
	applied, err := q.UpdateQueuedPriority(context.Background(), id, 3)
	if err != nil || !applied {
		t.Fatalf("applied = %v, err %v, want a queued run reported", applied, err)
	}
	if got := commits.Load() - seeded; got != 0 {
		t.Fatalf("committed %d writes for an unchanged rank, want none", got)
	}
}

func TestUpdateQueuedPriorityLogsOnlyAChangedRank(t *testing.T) {
	q, client := queue(t)
	var logged bytes.Buffer
	q = q.WithLogger(slog.New(slog.NewJSONHandler(&logged, nil)))
	id := fresh("queue-reprioritize-log")
	seedRow(t, client, id, map[string]any{stateField: stateQueued, priorityField: int64(3)})
	for _, rank := range []int{3, 1} {
		if _, err := q.UpdateQueuedPriority(context.Background(), id, rank); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(logged.String(), `"msg":"queuedPriorityChanged"`); got != 1 {
		t.Fatalf("logged %d changes, want one for the 3 to 1 edit only: %s", got, logged.String())
	}
	if !strings.Contains(logged.String(), `"from":3,"to":1`) {
		t.Fatalf("log = %s, want from 3 to 1", logged.String())
	}
}

// updateTime is when a row was last written, which a no-op leaves unchanged.
func updateTime(t *testing.T, client *firestore.Client, id string) time.Time {
	t.Helper()
	snap, err := client.Collection(runsCollection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap.UpdateTime
}

func TestBackfillQueuedAtStampsOnlyQueuedAndPlannedRowsWithoutIt(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	started, changed := queueAt, queueAt.Add(time.Hour)
	zeroStart := fresh("backfill-zero-start")
	seedRow(t, client, zeroStart, map[string]any{stateField: stateQueued, startedAtField: time.Time{}, updatedAtField: changed})
	stamp := queueAt.Add(-time.Hour)
	queuedRow, plannedRow, hasStamp := fresh("backfill-queued"), fresh("backfill-planned"), fresh("backfill-stamped")
	claimed, settled, noTime := fresh("backfill-claimed"), fresh("backfill-settled"), fresh("backfill-no-time")
	seedRow(t, client, queuedRow, map[string]any{stateField: stateQueued, startedAtField: started, updatedAtField: changed})
	seedRow(t, client, plannedRow, map[string]any{stateField: statePlanned, updatedAtField: changed})
	seedRow(t, client, hasStamp, map[string]any{stateField: stateQueued, startedAtField: started, queuedAtField: stamp})
	seedRow(t, client, claimed, map[string]any{stateField: stateClaimed, startedAtField: started})
	seedRow(t, client, settled, map[string]any{stateField: stateStopped, startedAtField: started})
	seedRow(t, client, noTime, map[string]any{stateField: stateQueued})
	untouched := map[string]time.Time{}
	for _, id := range []string{hasStamp, claimed, settled, noTime} {
		untouched[id] = updateTime(t, client, id)
	}

	res, err := q.BackfillQueuedAt(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stamped < 2 || res.Skipped < 1 {
		t.Fatalf("result = %+v, want at least 2 stamped and the row with no time skipped", res)
	}
	if got := readRow(t, client, queuedRow)[queuedAtField]; got != started {
		t.Fatalf("queued row queued_at = %v, want its started_at %v", got, started)
	}
	if got := readRow(t, client, zeroStart)[queuedAtField]; got != changed {
		t.Fatalf("zero started_at row queued_at = %v, want its updated_at %v", got, changed)
	}
	if got := readRow(t, client, plannedRow)[queuedAtField]; got != changed {
		t.Fatalf("planned row queued_at = %v, want its updated_at %v", got, changed)
	}
	for id, before := range untouched {
		if got := updateTime(t, client, id); !got.Equal(before) {
			t.Fatalf("row %s was written by the backfill", id)
		}
	}

	again := updateTime(t, client, queuedRow)
	if _, err := q.BackfillQueuedAt(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := updateTime(t, client, queuedRow); !got.Equal(again) {
		t.Fatal("a second run wrote an already stamped row")
	}
}

func TestBackfillQueuedAtDryRunWritesNothing(t *testing.T) {
	q, client := queue(t)
	id := fresh("backfill-dry")
	seedRow(t, client, id, map[string]any{stateField: stateQueued, startedAtField: queueAt})
	before := updateTime(t, client, id)
	res, err := q.BackfillQueuedAt(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stamped < 1 {
		t.Fatalf("result = %+v, want the unstamped row counted", res)
	}
	if _, has := readRow(t, client, id)[queuedAtField]; has || !updateTime(t, client, id).Equal(before) {
		t.Fatal("a dry run wrote the row")
	}
}

func TestStampQueuedAtLeavesAMissingRow(t *testing.T) {
	q, client := queue(t)
	stamped, err := q.stampQueuedAt(context.Background(), client.Collection(runsCollection).Doc(fresh("backfill-missing")), false)
	if err != nil || stamped {
		t.Fatalf("stamped = %v, err %v, want a clean no-op", stamped, err)
	}
}

func TestStampQueuedAtLeavesARowThatChangedSinceItWasListed(t *testing.T) {
	q, client := queue(t)
	for name, row := range map[string]map[string]any{
		"stamped": {stateField: stateQueued, startedAtField: queueAt, queuedAtField: queueAt.Add(-time.Hour)},
		"claimed": {stateField: stateClaimed, startedAtField: queueAt},
	} {
		id := fresh("backfill-" + name)
		seedRow(t, client, id, row)
		before := updateTime(t, client, id)
		stamped, err := q.stampQueuedAt(context.Background(), client.Collection(runsCollection).Doc(id), false)
		if err != nil || stamped || !updateTime(t, client, id).Equal(before) {
			t.Fatalf("%s: stamped = %v, err %v, want the row left alone", name, stamped, err)
		}
	}
}

// TestCandidatesDefaultsAMissingPrivateFieldToTrue is the regression test for
// a rolling-deploy hazard: a run record written before this field existed has
// no privateField at all. Defaulting that to false would silently zero a
// private target's runner minutes against NFR-1's ceiling during the
// transition; defaulting to true is the safe direction instead.
func TestCandidatesDefaultsAMissingPrivateFieldToTrue(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("pre-migration")
	forget(t, client, id)
	if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, map[string]any{
		stateField:    stateQueued,
		priorityField: int64(1),
		queuedAtField: queueAt,
		repoField:     "octo/scratch",
		sizeField:     "S",
		// privateField deliberately omitted.
	}); err != nil {
		t.Fatal(err)
	}
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range cands {
		if c.RunID != id {
			continue
		}
		found = true
		if !c.Private {
			t.Fatalf("candidate %+v, want Private true for a record missing the field", c)
		}
	}
	if !found {
		t.Fatalf("candidate %s not listed among %+v", id, cands)
	}
}

// TestCandidatesReadsTheBuildModelTheTicketNamed asserts a candidate carries the
// build model its ticket named, which admission uses to select the model's own
// cap.
func TestCandidatesReadsTheBuildModelTheTicketNamed(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	named, unnamed := queuedRun(fresh("queue-cand-model"), 1), queuedRun(fresh("queue-cand-nomodel"), 2)
	named.Models = runner.ModelLabels{Build: "command-code/deepseek/deepseek-v4.1-flash"}
	forget(t, client, named.RunID, unnamed.RunID)
	for _, run := range []dispatcher.Queued{named, unnamed} {
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{named.RunID: named.Models.Build, unnamed.RunID: ""}
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		wantModel, ours := want[c.RunID]
		if !ours {
			continue
		}
		if c.Model != wantModel {
			t.Fatalf("candidate %s model = %q, want %q", c.RunID, c.Model, wantModel)
		}
	}
}

func TestTryClaimAdmitsARunThatFitsAndBooksItsReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-claim"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{ProviderCost: 3 * money.Dollar, RunnerMinutes: 12}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res)
	if err != nil || !ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q, err = %v", ok, binding, err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != stateClaimed {
		t.Fatalf("%s is %v after being claimed", run.RunID, snap.Data()[stateField])
	}
	ledgerSnap, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ledger ledgerDoc
	if err := ledgerSnap.DataTo(&ledger); err != nil {
		t.Fatal(err)
	}
	entry, ok := ledger.Reservations[reservationKey(run.RunID, 0, dispatcher.StageBuild)]
	if !ok || entry.ProviderCostMicros != res.ProviderCost || entry.RunnerMinutes != res.RunnerMinutes {
		t.Fatalf("reservation = %+v, ok %v", entry, ok)
	}
	if entry.TicketID != run.Ticket.ID || entry.Repo != run.Repo || entry.Size != run.Ticket.Size || !entry.ClaimedAt.Equal(queueAt) {
		t.Fatalf("reservation = %+v, want the ticket, repository, size and claim time booked", entry)
	}
	if snap.Data()[claimConcurrencyField] != int64(1) {
		t.Fatalf("claim concurrency = %v, want 1 for a run claimed alone", snap.Data()[claimConcurrencyField])
	}
}

// TestTryClaimSettlesARowThatEndedWithoutAClaim asserts a row a finished
// manual run left in a dispatchable state is settled, not rebuilt: the run
// reported its own outcome, so no poll claims it and no reservation is booked.
func TestTryClaimSettlesARowThatEndedWithoutAClaim(t *testing.T) {
	for _, state := range []string{stateQueued, statePlanned} {
		t.Run(state, func(t *testing.T) {
			q, client := queue(t)
			var logged bytes.Buffer
			q = q.WithLogger(slog.New(slog.NewJSONHandler(&logged, nil)))
			ctx := context.Background()
			run := queuedRun(fresh("queue-claim-settled"), 1)
			forget(t, client, run.RunID)
			resetLedger(t, client, 1)
			if err := q.Enqueue(ctx, run); err != nil {
				t.Fatal(err)
			}
			// The manual run recorded its own outcome and touched no state, so
			// the row sits in a dispatchable state carrying a PR.
			if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{
				{Path: stateField, Value: state},
				{Path: outcomeField, Value: string(runner.OutcomePROpened)},
				{Path: prURLField, Value: "https://github.com/octo/scratch/pull/159"},
				{Path: waitingOnField, Value: "5h"},
			}); err != nil {
				t.Fatal(err)
			}
			ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt.Add(time.Minute), generousBudget, openFacts,
				dispatcher.Reservation{ProviderCost: money.Dollar})
			if err != nil {
				t.Fatal(err)
			}
			if ok || binding != "" {
				t.Fatalf("ok = %v, binding = %q, want the ended row settled rather than claimed", ok, binding)
			}
			data := readRow(t, client, run.RunID)
			if data[stateField] != stateClaimed {
				t.Fatalf("state = %v, want the row settled to %s", data[stateField], stateClaimed)
			}
			if data[outcomeField] != string(runner.OutcomePROpened) {
				t.Fatalf("outcome = %v, want the recorded one left intact", data[outcomeField])
			}
			if held, present := data[waitingOnField]; present {
				t.Fatalf("waiting_on = %v survived the settle", held)
			}
			if at, _ := data[updatedAtField].(time.Time); !at.Equal(queueAt.Add(time.Minute)) {
				t.Fatalf("updated_at = %v, want the settle's own time", at)
			}
			if res := readLedgerDoc(t, client).Reservations; len(res) != 0 {
				t.Fatalf("reservations = %v, want none booked for a settled row", res)
			}
			if _, claimed := data[claimedAtField]; claimed {
				t.Fatalf("claimed_at = %v, want a settled row to carry no claim of its own", data[claimedAtField])
			}
			var rec struct {
				Level   string `json:"level"`
				Msg     string `json:"msg"`
				Run     string `json:"run"`
				Outcome string `json:"outcome"`
			}
			if err := json.NewDecoder(&logged).Decode(&rec); err != nil {
				t.Fatalf("no log record: %v", err)
			}
			if rec.Msg != "queuedRowSettled" || rec.Run != run.RunID || rec.Outcome != string(runner.OutcomePROpened) {
				t.Fatalf("log = %+v, want queuedRowSettled naming the run and its outcome", rec)
			}
			if rec.Level != "WARN" {
				t.Fatalf("log level = %q, want WARN for a row settled without a claim", rec.Level)
			}
		})
	}
}

// TestTryClaimSettlesARowThatOnlyRecordsAStopReason asserts the guard reads the
// same ended test a requeue does: a stop reason alone, with no outcome, is
// enough to settle a row rather than rebuild it.
func TestTryClaimSettlesARowThatOnlyRecordsAStopReason(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-claim-stop-only"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{
		{Path: stopReasonField, Value: string(runner.StopRebaseConflict)},
	}); err != nil {
		t.Fatal(err)
	}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt.Add(time.Minute), generousBudget, openFacts,
		dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q, want the ended row settled rather than claimed", ok, binding)
	}
	if data := readRow(t, client, run.RunID); data[stateField] != stateClaimed {
		t.Fatalf("state = %v, want the row settled to %s", data[stateField], stateClaimed)
	}
	if res := readLedgerDoc(t, client).Reservations; len(res) != 0 {
		t.Fatalf("reservations = %v, want none booked for a settled row", res)
	}
}

// TestTryClaimClaimsARowAnOperatorRequeued asserts the settle leaves the row
// itself alone: once a requeue has cleared the outcome, the row is claimed as
// any other queued one is.
func TestTryClaimClaimsARowAnOperatorRequeued(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-claim-requeued"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{
		{Path: outcomeField, Value: string(runner.OutcomeAgentFailed)},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := q.Requeue(ctx, run.RunID, queueAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != stateQueued {
		t.Fatalf("requeue state = %q, want %s", got.State, stateQueued)
	}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt.Add(2*time.Minute), generousBudget, openFacts,
		dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q, want the requeued run claimed", ok, binding)
	}
	if data := readRow(t, client, run.RunID); data[stateField] != stateClaimed {
		t.Fatalf("state = %v, want %s", data[stateField], stateClaimed)
	}
}

// TestRecordTicketStateTravelsOutWithTheRequeueAndIsCleared: the state a claim
// recorded on the row is what its requeue reports, so the caller can put the
// ticket back, and a run the queue holds again carries none.
func TestRecordTicketStateTravelsOutWithTheRequeueAndIsCleared(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("queue-ticket-state")
	forget(t, client, id)
	if err := q.Enqueue(ctx, queuedRun(id, 1)); err != nil {
		t.Fatal(err)
	}
	if err := q.RecordTicketState(ctx, id, "state-todo"); err != nil {
		t.Fatal(err)
	}
	if got := readRow(t, client, id)[ticketStateField]; got != "state-todo" {
		t.Fatalf("ticket_state = %v, want the state the claim recorded", got)
	}
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{
		{Path: outcomeField, Value: string(runner.OutcomeAgentFailed)},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := q.Requeue(ctx, id, queueAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.TicketState != "state-todo" {
		t.Fatalf("requeue reports %q, want the state the claim recorded", got.TicketState)
	}
	if held := readRow(t, client, id)[ticketStateField]; held != nil {
		t.Fatalf("the ticket state survived the requeue: %v", held)
	}
}

func TestTryClaimDefersARunTheProviderWindowWouldBreach(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-defer"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 2 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, tight, openFacts, dispatcher.Reservation{ProviderCost: 3 * money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "5h" {
		t.Fatalf("ok = %v, binding = %q, want deferred on 5h", ok, binding)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != stateQueued {
		t.Fatalf("a deferred run's state = %v, want it to stay queued", snap.Data()[stateField])
	}
	if snap.Data()[waitingOnField] != "5h" {
		t.Fatalf("waiting on %v, want the binding condition recorded on the row", snap.Data()[waitingOnField])
	}
}

// TestTryClaimDefersARunTheModelsOwnCapWouldBreach asserts the plan's per-model
// cap binds beside its windows: the plan's month window has room for
// the estimate, but the run's model has spent its own cap.
func TestTryClaimDefersARunTheModelsOwnCapWouldBreach(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-defer-cap"), 1)
	run.Models = runner.ModelLabels{Build: "command-code/deepseek/deepseek-v4.1-flash"}
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "month", Period: 720 * time.Hour, Limit: 70 * money.Dollar}},
		ModelCaps: map[string]dispatcher.Window{
			"command-code/deepseek/deepseek-v4.1-flash": {Name: "flash", Period: 720 * time.Hour, Limit: money.Dollar},
		},
		Cash:   dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner: dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	facts := dispatcher.Facts{Limits: dispatcher.DefaultLimits, OpenPRsKnown: true,
		Model: "command-code/deepseek/deepseek-v4.1-flash"}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, tight, facts, dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "flash" {
		t.Fatalf("ok = %v, binding = %q, want deferred on the model cap", ok, binding)
	}
}

func TestTryClaimCountsAnotherRunsInFlightReservationTowardTheCeiling(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	first := queuedRun(fresh("queue-inflight-a"), 1)
	second := queuedRun(fresh("queue-inflight-b"), 2)
	first.Repo, second.Repo = "octo/first", "octo/second"
	forget(t, client, first.RunID, second.RunID)
	resetLedger(t, client, 2)
	for _, r := range []dispatcher.Queued{first, second} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 5 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	// Claiming the first run reserves $4 of the $5 window.
	ok, _, err := q.TryClaim(ctx, first.RunID, queueAt, tight, openFacts, dispatcher.Reservation{ProviderCost: 4 * money.Dollar})
	if err != nil || !ok {
		t.Fatalf("first claim: ok = %v, err = %v", ok, err)
	}
	// Nothing has settled yet, but the first run's reservation alone leaves no
	// room for a second $4 estimate.
	ok, binding, err := q.TryClaim(ctx, second.RunID, queueAt, tight, openFacts, dispatcher.Reservation{ProviderCost: 4 * money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "5h" {
		t.Fatalf("ok = %v, binding = %q, want the in-flight reservation to defer this claim", ok, binding)
	}
}

func TestTwoTryClaimsClaimOneRunBetweenThem(t *testing.T) {
	q, client := queue(t)
	run := queuedRun(fresh("queue-race"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		won   int
		fails []error
	)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := q.TryClaim(context.Background(), run.RunID, queueAt, generousBudget, openFacts,
				dispatcher.Reservation{ProviderCost: money.Dollar})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				fails = append(fails, err)
			case ok:
				won++
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d claims won, want exactly one", won)
	}
	if len(fails) > 0 {
		t.Fatalf("a claim failed rather than reporting the run was taken: %v", fails)
	}
}

func TestReleaseReturnsAClaimedRunToTheQueueAndDropsItsReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rel"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{ProviderCost: money.Dollar}); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if err := q.Release(ctx, run.RunID, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != stateQueued {
		t.Fatalf("released run is %v", snap.Data()[stateField])
	}
	if _, ok := snap.Data()[claimedAtField]; ok {
		t.Fatalf("released run still holds %s", claimedAtField)
	}
	ledgerSnap, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ledger ledgerDoc
	if err := ledgerSnap.DataTo(&ledger); err != nil {
		t.Fatal(err)
	}
	if _, ok := ledger.Reservations[reservationKey(run.RunID, 0, dispatcher.StageBuild)]; ok {
		t.Fatalf("a released run's reservation was not dropped: %+v", ledger.Reservations[reservationKey(run.RunID, 0, dispatcher.StageBuild)])
	}
	// The released run is claimable again, with the ledger no longer double-
	// counting its reservation.
	ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil || !ok {
		t.Fatalf("the released run was not claimed again: %v, %v", ok, err)
	}
}

func TestSettleDropsTheReservationAndIsIdempotent(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-settle"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{ProviderCost: 2 * money.Dollar}); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if err := q.Settle(ctx, run.RunID, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	ledgerSnap, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ledger ledgerDoc
	if err := ledgerSnap.DataTo(&ledger); err != nil {
		t.Fatal(err)
	}
	if _, ok := ledger.Reservations[reservationKey(run.RunID, 0, dispatcher.StageBuild)]; ok {
		t.Fatalf("a settled run's reservation was not dropped: %+v", ledger.Reservations[reservationKey(run.RunID, 0, dispatcher.StageBuild)])
	}
	// Settling a second time (a rerun record job) must not error.
	if err := q.Settle(ctx, run.RunID, attemptOf(0)); err != nil {
		t.Fatalf("settling an already-settled run: %v", err)
	}
	if got := readLedgerDoc(t, client).N; got != 1 {
		t.Fatalf("N = %d, want an unsettled run to leave it alone", got)
	}
}

func TestTryClaimCountsSettledCostWithinTheWindow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	settled := queuedRun(fresh("queue-settled"), 1)
	candidate := queuedRun(fresh("queue-after-settled"), 2)
	forget(t, client, settled.RunID, candidate.RunID)
	resetLedger(t, client, 1)
	for _, r := range []dispatcher.Queued{settled, candidate} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Settle a run's cost directly, as runner.Finalize would.
	if _, err := client.Collection(runsCollection).Doc(settled.RunID).Set(ctx, map[string]any{
		settledAtField:           queueAt,
		settledProviderCostField: int64(4 * money.Dollar),
	}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}

	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 5 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	ok, binding, err := q.TryClaim(ctx, candidate.RunID, queueAt.Add(time.Minute), tight, openFacts,
		dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != "5h" {
		t.Fatalf("ok = %v, binding = %q, want the $4 already settled in this window to defer a further $2", ok, binding)
	}

	// The same settled cost falls outside a window that starts after it.
	ok, binding, err = q.TryClaim(ctx, candidate.RunID, queueAt.Add(6*time.Hour), tight, openFacts,
		dispatcher.Reservation{ProviderCost: 2 * money.Dollar})
	if err != nil || !ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q, want it to fit once the settled run has rolled out of the window", ok, binding)
	}
}

func TestTryClaimAdmitsOneRunPerRepositoryAndRecordsWhyTheOtherWaits(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	first := queuedRun(fresh("queue-repo-a"), 1)
	second := queuedRun(fresh("queue-repo-b"), 2)
	forget(t, client, first.RunID, second.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{first, second} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	if ok, _, err := q.TryClaim(ctx, first.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("first claim: ok %v, err %v", ok, err)
	}
	ok, binding, err := q.TryClaim(ctx, second.RunID, queueAt, generousBudget, openFacts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionRepoBusy {
		t.Fatalf("ok = %v, binding = %q, want the repository held by the first run", ok, binding)
	}
	snap, err := client.Collection(runsCollection).Doc(second.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[waitingOnField] != dispatcher.ConditionRepoBusy {
		t.Fatalf("waiting on %v, want the binding condition on the row", snap.Data()[waitingOnField])
	}
	if err := q.Settle(ctx, first.RunID, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, second.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("second claim after the first settled: ok %v, err %v", ok, err)
	}
	snap, err = client.Collection(runsCollection).Doc(second.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, waiting := snap.Data()[waitingOnField]; waiting {
		t.Fatalf("a claimed run still records %v as what it waits on", snap.Data()[waitingOnField])
	}
}

// buildInFlight enqueues running and waiting with the ledger reset, and claims
// running for its build stage on the write set files: the state a poll leaves a
// repository in while an M or L run builds.
func buildInFlight(t *testing.T, q *Queue, client *firestore.Client, running, waiting dispatcher.Queued, files []string) {
	t.Helper()
	ctx := context.Background()
	forget(t, client, running.RunID, waiting.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{running, waiting} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _, err := q.TryClaim(ctx, running.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{
		ProviderCost: money.Dollar, Stage: dispatcher.StageBuild, Files: files,
	}); err != nil || !ok {
		t.Fatalf("claiming the build in flight: ok %v, err %v", ok, err)
	}
}

// TestTryClaimAdmitsAPlanStageBesideABuildInItsRepository asserts a claim's own
// stage reaches admission: a plan stage writes nothing, so a build in flight in
// the same repository does not hold it. Without the stage on the candidate its
// empty write set reads as unknown and the build holds it.
func TestTryClaimAdmitsAPlanStageBesideABuildInItsRepository(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	running := queuedRun(fresh("queue-plan-stage-running"), 1)
	plan := queuedRun(fresh("queue-plan-stage"), 2)
	buildInFlight(t, q, client, running, plan, []string{"a.go"})
	// The claim the dispatcher makes for an unplanned M or L ticket: its plan
	// stage, carrying no write set of its own.
	ok, binding, err := q.TryClaim(ctx, plan.RunID, queueAt, generousBudget, openFacts,
		dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StagePlan})
	if err != nil || !ok || binding != "" {
		t.Fatalf("ok = %v, binding = %q, err = %v, want the plan stage admitted beside a build", ok, binding, err)
	}
}

// TestTryClaimHoldsASingleStageClaimBesideABuildInItsRepository asserts an S
// run — never plan-staged, and recording no write set of its own — is still
// held while a build runs in its repository.
func TestTryClaimHoldsASingleStageClaimBesideABuildInItsRepository(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	running := queuedRun(fresh("queue-s-running"), 1)
	sized := queuedRun(fresh("queue-s-held"), 2)
	sized.Ticket.Size = "S"
	buildInFlight(t, q, client, running, sized, []string{"a.go"})
	ok, binding, err := q.TryClaim(ctx, sized.RunID, queueAt, generousBudget, openFacts,
		dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionRepoBusy {
		t.Fatalf("ok = %v, binding = %q, want the repository held by the build", ok, binding)
	}
	if held := readRow(t, client, sized.RunID)[waitingOnField]; held != dispatcher.ConditionRepoBusy {
		t.Fatalf("waiting on %v, want the binding condition recorded on the row", held)
	}
}

// TestTryClaimHoldsABuildStageClaimBesideABuildInItsRepository asserts the plan
// stage's exemption is the stage's alone: a build stage whose plan recorded no
// write set is still held by a build in its repository, where a claim exempted
// on its file list alone would be admitted.
func TestTryClaimHoldsABuildStageClaimBesideABuildInItsRepository(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	running := queuedRun(fresh("queue-build-stage-running"), 1)
	planned := queuedRun(fresh("queue-build-stage"), 2)
	buildInFlight(t, q, client, running, planned, []string{"a.go"})
	// The run waits for its build with no write set recorded: its plan stage
	// settled, listing no files.
	if _, err := client.Collection(runsCollection).Doc(planned.RunID).Update(ctx, []firestore.Update{
		{Path: stateField, Value: statePlanned},
		{Path: planSettledAtField, Value: queueAt},
	}); err != nil {
		t.Fatal(err)
	}
	ok, binding, err := q.TryClaim(ctx, planned.RunID, queueAt, generousBudget, openFacts,
		dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StageBuild})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionRepoBusy {
		t.Fatalf("ok = %v, binding = %q, want the build stage held beside a build", ok, binding)
	}
}

func TestTryClaimHoldsARunBlockedByOneInFlight(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	running := queuedRun(fresh("queue-blocker"), 1)
	blocked := queuedRun(fresh("queue-blocked"), 2)
	running.Repo, blocked.Repo = "octo/running", "octo/blocked"
	blocked.BlockedBy = []string{running.Ticket.ID}
	forget(t, client, running.RunID, blocked.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{running, blocked} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	if ok, _, err := q.TryClaim(ctx, running.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	ok, binding, err := q.TryClaim(ctx, blocked.RunID, queueAt, generousBudget, openFacts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionBlocked {
		t.Fatalf("ok = %v, binding = %q, want the blocking relation to hold the run", ok, binding)
	}
}

func TestTryClaimCapsTheRunsOfTheLargeSizeInFlight(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	first, second := queuedRun(fresh("queue-large-a"), 1), queuedRun(fresh("queue-large-b"), 2)
	first.Repo, second.Repo = "octo/large-a", "octo/large-b"
	first.Ticket.Size, second.Ticket.Size = "L", "L"
	forget(t, client, first.RunID, second.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{first, second} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	if ok, _, err := q.TryClaim(ctx, first.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("first claim: ok %v, err %v", ok, err)
	}
	ok, binding, err := q.TryClaim(ctx, second.RunID, queueAt, generousBudget, openFacts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionLargeCap {
		t.Fatalf("ok = %v, binding = %q, want the large run held by the one already in flight", ok, binding)
	}
}

func TestRacingClaimsForOneRepositoryAdmitOne(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	runs := []dispatcher.Queued{queuedRun(fresh("queue-same-repo-a"), 1), queuedRun(fresh("queue-same-repo-b"), 2)}
	forget(t, client, runs[0].RunID, runs[1].RunID)
	resetLedger(t, client, 5)
	for _, r := range runs {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	won, fails := raceClaims(q, runs, generousBudget)
	if won != 1 || len(fails) > 0 {
		t.Fatalf("%d claims won, failures %v, want exactly one claim for the repository", won, fails)
	}
}

func TestRacingClaimsNeverPushTheReservedTotalPastTheCeiling(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	var runs []dispatcher.Queued
	var ids []string
	for i := range 4 {
		r := queuedRun(fresh("queue-ceiling"), i+1)
		r.Repo = "octo/repo-" + strconv.Itoa(i)
		runs = append(runs, r)
		ids = append(ids, r.RunID)
	}
	forget(t, client, ids...)
	resetLedger(t, client, 10)
	for _, r := range runs {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	tight := dispatcher.BudgetConfig{
		ProviderWindows: []dispatcher.Window{{Name: "5h", Period: 5 * time.Hour, Limit: 2 * money.Dollar}},
		Cash:            dispatcher.Window{Name: dispatcher.CeilingCash, Calendar: true, Limit: 1000 * money.Dollar},
		Runner:          dispatcher.RunnerMinutes{FreeMinutes: 1_000_000},
	}
	won, fails := raceClaims(q, runs, tight)
	if won != 2 || len(fails) > 0 {
		t.Fatalf("%d claims won, failures %v, want the $2 window to fit exactly two $1 runs", won, fails)
	}
	if got := reservedTotals(readLedgerDoc(t, client)).ProviderCost; got != 2*money.Dollar {
		t.Fatalf("reserved %v, want the ceiling respected", got)
	}
}

// raceClaims claims every run at once, each with a one-dollar reservation, and
// reports how many won and which failed outright.
func raceClaims(q *Queue, runs []dispatcher.Queued, cfg dispatcher.BudgetConfig) (won int, fails []error) {
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, r := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := q.TryClaim(context.Background(), r.RunID, queueAt, cfg, openFacts,
				dispatcher.Reservation{ProviderCost: money.Dollar})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				fails = append(fails, err)
			case ok:
				won++
			}
		}()
	}
	wg.Wait()
	return won, fails
}

// seedSettled writes id as a settled run of size that took the given time and
// ended with outcome, claimed at claimedAt while claimConcurrency runs were in
// flight.
func seedSettled(t *testing.T, client *firestore.Client, id, size string, outcome runner.Outcome, claimConcurrency int, took time.Duration, claimedAt time.Time) {
	t.Helper()
	settledRecord(t, client, id, size, money.Dollar, took.Milliseconds(), claimedAt.Add(took))
	if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), map[string]any{
		claimConcurrencyField: int64(claimConcurrency),
		claimedAtField:        claimedAt,
		outcomeField:          string(outcome),
	}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
}

// seedRuns settles count runs of size that reached a PR, claimed at
// concurrency and each taking took, and returns their ids. The latest ended
// agoBase before queueAt, and each earlier one an hour before the last.
func seedRuns(t *testing.T, client *firestore.Client, size, prefix string, count, concurrency int, took, agoBase time.Duration) []string {
	t.Helper()
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fresh(prefix)
		seedSettled(t, client, ids[i], size, runner.OutcomePROpened, concurrency, took, queueAt.Add(-agoBase-time.Duration(i)*time.Hour))
	}
	return ids
}

// holdReservation makes the ledger hold a reservation for each run, claimed at
// the given time, beside the ledger's own N, stable count and change marker.
func holdReservation(t *testing.T, client *firestore.Client, ledger ledgerDoc, claimedAt map[string]time.Time) {
	t.Helper()
	ledger.Reservations = map[string]reservationEntry{}
	for id, at := range claimedAt {
		ledger.Reservations[reservationKey(id, 0, dispatcher.StageBuild)] = reservationEntry{ClaimedAt: at}
	}
	if _, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Set(context.Background(), ledger); err != nil {
		t.Fatal(err)
	}
}

func TestSettleMovesNByWhatTheSettledRunShowed(t *testing.T) {
	const solo, slow = 10 * time.Minute, 20 * time.Minute
	for name, tt := range map[string]struct {
		before  ledgerDoc
		claimed int
		took    time.Duration
		outcome runner.Outcome
		// alone and loaded are earlier runs of the size that reached a PR, at
		// concurrency 1 and at the settled run's own; older are slow loaded runs
		// settled before them all.
		alone, loaded, older int
		// olderAlone are slow runs that ran alone, settled before all the others.
		olderAlone int
		want       ledgerDoc
	}{
		"the fifth stable run raises N": {
			before: ledgerDoc{N: 1, StableRuns: 4}, claimed: 1, took: solo, outcome: runner.OutcomePROpened, alone: 4,
			want: ledgerDoc{N: 2},
		},
		"a slow median of five at N halves it": {
			before: ledgerDoc{N: 2, StableRuns: 3}, claimed: 2, took: slow, outcome: runner.OutcomePROpened, alone: 5, loaded: 4,
			want: ledgerDoc{N: 1},
		},
		"a run claimed below N holds it": {
			before: ledgerDoc{N: 3, StableRuns: 2}, claimed: 1, took: solo, outcome: runner.OutcomePROpened, alone: 5,
			want: ledgerDoc{N: 3, StableRuns: 2},
		},
		"fewer than five runs at N is no evidence": {
			before: ledgerDoc{N: 1, StableRuns: 4}, claimed: 1, took: solo, outcome: runner.OutcomePROpened, alone: 3,
			want: ledgerDoc{N: 1, StableRuns: 4},
		},
		"a quick failure does not count as a stable run": {
			before: ledgerDoc{N: 1, StableRuns: 4}, claimed: 1, took: time.Second, outcome: runner.OutcomeAgentFailed, alone: 5,
			want: ledgerDoc{N: 1, StableRuns: 4},
		},
		"a ledger tuned to two stable runs raises N on the second": {
			before: ledgerDoc{N: 1, StableRuns: 1, TuneStableRuns: 2}, claimed: 1, took: solo, outcome: runner.OutcomePROpened, alone: 2,
			want: ledgerDoc{N: 2},
		},
		"a ledger tuned to a tighter rise band holds a median the default would count": {
			before: ledgerDoc{N: 2, StableRuns: 3, TuneRiseWithin: 1.05}, claimed: 2, took: 11 * time.Minute, outcome: runner.OutcomePROpened, alone: 5, loaded: 4,
			want: ledgerDoc{N: 2},
		},
		"a ledger tuned to an earlier fall band halves at a median the default would hold": {
			before: ledgerDoc{N: 2, StableRuns: 3, TuneRiseWithin: 1.1, TuneHalveBeyond: 1.2}, claimed: 2, took: 13 * time.Minute, outcome: runner.OutcomePROpened, alone: 5, loaded: 4,
			want: ledgerDoc{N: 1},
		},
		"a tuned count reads only that many of the latest runs": {
			before: ledgerDoc{N: 2, StableRuns: 1, TuneStableRuns: 2}, claimed: 2, took: solo, outcome: runner.OutcomePROpened, alone: 5, loaded: 1, older: 3,
			want: ledgerDoc{N: 3},
		},
		"a tuned count reads only that many of the latest runs alone": {
			before: ledgerDoc{N: 2, StableRuns: 1, TuneStableRuns: 2}, claimed: 2, took: 16 * time.Minute, outcome: runner.OutcomePROpened, alone: 2, loaded: 1, olderAlone: 3,
			want: ledgerDoc{N: 1},
		},
		"runs at N are no evidence while too few ran alone": {
			before: ledgerDoc{N: 2, StableRuns: 3}, claimed: 2, took: solo, outcome: runner.OutcomePROpened, alone: 3, loaded: 4,
			want: ledgerDoc{N: 2, StableRuns: 3},
		},
		"only the latest five runs at N make the median": {
			before: ledgerDoc{N: 2, StableRuns: 4}, claimed: 2, took: solo, outcome: runner.OutcomePROpened, alone: 5, loaded: 4, older: 6,
			want: ledgerDoc{N: 3},
		},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			size := "S-" + fresh("settle-n")
			id := fresh("queue-settle-n")
			ids := []string{id}
			ids = append(ids, seedRuns(t, client, size, "queue-settle-alone", tt.alone, 1, solo, 2*time.Hour)...)
			ids = append(ids, seedRuns(t, client, size, "queue-settle-loaded", tt.loaded, tt.claimed, tt.took, 2*time.Hour)...)
			ids = append(ids, seedRuns(t, client, size, "queue-settle-older", tt.older, tt.claimed, 6*slow, 101*time.Hour)...)
			ids = append(ids, seedRuns(t, client, size, "queue-settle-older-alone", tt.olderAlone, 1, 6*slow, 102*time.Hour)...)
			forget(t, client, ids...)
			resetLedger(t, client, tt.before.N)
			seedSettled(t, client, id, size, tt.outcome, tt.claimed, tt.took, queueAt)
			holdReservation(t, client, tt.before, map[string]time.Time{id: queueAt})
			if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
				t.Fatal(err)
			}
			got := readLedgerDoc(t, client)
			if got.N != tt.want.N || got.StableRuns != tt.want.StableRuns {
				t.Fatalf("N %d stable %d, want N %d stable %d", got.N, got.StableRuns, tt.want.N, tt.want.StableRuns)
			}
			if _, held := got.Reservations[reservationKey(id, 0, dispatcher.StageBuild)]; held {
				t.Fatal("the settled run's reservation was not dropped")
			}
		})
	}
}

func TestSettleHalvesNOncePerIncident(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	ids := []string{fresh("queue-limited-a"), fresh("queue-limited-b"), fresh("queue-limited-c")}
	forget(t, client, ids...)
	resetLedger(t, client, 4)
	claimedAt := map[string]time.Time{}
	for i, id := range ids {
		at := queueAt.Add(time.Duration(i) * time.Second)
		claimedAt[id] = at
		rec := runner.NewRecord(id, runner.Ticket{ID: id, Title: "feat(x): add a file", Size: "S", Body: "Add a file."}, at)
		rec.Outcome, rec.StopReason = runner.OutcomeInfraFailure, runner.StopModelUnavailable
		data := fields(rec)
		data[claimConcurrencyField], data[claimedAtField] = int64(4), at
		if _, err := client.Collection(runsCollection).Doc(id).Set(ctx, data); err != nil {
			t.Fatal(err)
		}
	}
	holdReservation(t, client, ledgerDoc{N: 4, StableRuns: 3}, claimedAt)
	if err := q.Settle(ctx, ids[0], attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 2 || got.StableRuns != 0 {
		t.Fatalf("N %d stable %d, want N halved to 2 with the count restarted", got.N, got.StableRuns)
	}
	for _, id := range ids[1:] {
		if err := q.Settle(ctx, id, attemptOf(0)); err != nil {
			t.Fatal(err)
		}
	}
	if got := readLedgerDoc(t, client).N; got != 2 {
		t.Fatalf("N = %d, want the other failures of the same incident to leave it at 2", got)
	}
}

func TestSettleIgnoresARunClaimedAboveAnNAlreadyHalved(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("queue-overloaded")
	forget(t, client, id)
	resetLedger(t, client, 4)
	seedSettled(t, client, id, "S-"+fresh("overloaded"), runner.OutcomePROpened, 8, time.Hour, queueAt)
	holdReservation(t, client, ledgerDoc{N: 4, StableRuns: 2, ChangedAfter: queueAt.Add(time.Minute)}, map[string]time.Time{id: queueAt})
	if err := q.Settle(ctx, id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 4 || got.StableRuns != 2 {
		t.Fatalf("N %d stable %d, want a run claimed before N moved to leave both alone", got.N, got.StableRuns)
	}
}

func TestSettleCapsNAtTheConfiguredPlatformCap(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("cap")
	id := fresh("queue-cap")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-cap-alone", 5, 1, 10*time.Minute, time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-cap-loaded", 4, 2, 10*time.Minute, time.Hour)...)
	forget(t, client, ids...)
	resetLedger(t, client, 2)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 2, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 4, PlatformCap: 2}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client).N; got != 2 {
		t.Fatalf("N = %d, want it held at the configured cap", got)
	}
}

func TestTryClaimUsesTheRelationsThePollReadNow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	running := queuedRun(fresh("queue-fresh-running"), 1)
	waiting := queuedRun(fresh("queue-fresh-waiting"), 2)
	running.Repo, waiting.Repo = "octo/running", "octo/waiting"
	forget(t, client, running.RunID, waiting.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{running, waiting} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	if ok, _, err := q.TryClaim(ctx, running.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	facts := openFacts
	facts.Relations = dispatcher.Relations{Known: true, BlockedBy: []string{running.Ticket.ID}}
	ok, binding, err := q.TryClaim(ctx, waiting.RunID, queueAt, generousBudget, facts, res)
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionBlocked {
		t.Fatalf("ok = %v, binding = %q, want a relation added after enqueue to hold the run", ok, binding)
	}
}

// TestTryClaimHoldsARunWhoseBlockerIsStillOpen is AC1 end to end: the relations
// this poll read name a blocker Linear has not completed or canceled, so the run
// waits with nothing in flight and no run bearing the blocker's id.
func TestTryClaimHoldsARunWhoseBlockerIsStillOpen(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-open-blocker"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 5)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.Relations = dispatcher.Relations{Known: true, BlockedBy: []string{"FRG-9"}, BlockedByOpen: []string{"FRG-9"}}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil {
		t.Fatal(err)
	}
	if ok || binding != dispatcher.ConditionBlocked {
		t.Fatalf("ok = %v, binding = %q, want an open blocker to hold the run", ok, binding)
	}
}

// TestTryClaimAdmitsARunWhoseBlockersAreDone is AC2: the same run is claimed
// once the poll reads every blocker as completed or canceled.
func TestTryClaimAdmitsARunWhoseBlockersAreDone(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-done-blocker"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 5)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.Relations = dispatcher.Relations{Known: true, BlockedBy: []string{"FRG-9"}}
	ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{ProviderCost: money.Dollar})
	if err != nil || !ok {
		t.Fatalf("ok = %v, binding = %q, err = %v, want a finished blocker not to hold the run", ok, binding, err)
	}
}

func TestTryClaimStoresNAndThePlatformCapOnTheLedger(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-ledger-n"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 0)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.Limits.PlatformCap = 7
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{}); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if got := readLedgerDoc(t, client); got.N != 1 || got.PlatformCap != 7 {
		t.Fatalf("N %d, platform cap %d, want 1 and 7 stored", got.N, got.PlatformCap)
	}
}

func TestTryClaimStoresTheTuningOnTheLedger(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-ledger-tuning"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 0)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	facts := openFacts
	facts.Tuning = dispatcher.Tuning{StableRuns: 3, RiseWithin: 1.1, HalveBeyond: 1.8}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{}); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	got := readLedgerDoc(t, client)
	if got.TuneStableRuns != 3 || got.TuneRiseWithin != 1.1 || got.TuneHalveBeyond != 1.8 {
		t.Fatalf("stored tuning = %d %v %v, want 3 1.1 1.8", got.TuneStableRuns, got.TuneRiseWithin, got.TuneHalveBeyond)
	}
}

func TestSettleIgnoresAnInfraFailureThatIsNotAProviderStop(t *testing.T) {
	for name, tt := range map[string]struct {
		outcome runner.Outcome
		reason  runner.StopReason
	}{
		"an infrastructure failure for another reason":        {runner.OutcomeInfraFailure, runner.StopAgentExit},
		"a model-unavailable stop that is not infrastructure": {runner.OutcomeAgentFailed, runner.StopModelUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			id := fresh("queue-not-limited")
			forget(t, client, id)
			resetLedger(t, client, 4)
			rec := runner.NewRecord(id, runner.Ticket{ID: id, Title: "feat(x): add a file", Size: "S", Body: "Add a file."}, queueAt)
			rec.Outcome, rec.StopReason = tt.outcome, tt.reason
			data := fields(rec)
			data[claimConcurrencyField], data[claimedAtField] = int64(4), queueAt
			if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), data); err != nil {
				t.Fatal(err)
			}
			holdReservation(t, client, ledgerDoc{N: 4, StableRuns: 3}, map[string]time.Time{id: queueAt})
			if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
				t.Fatal(err)
			}
			if got := readLedgerDoc(t, client); got.N != 4 || got.StableRuns != 3 {
				t.Fatalf("N %d stable %d, want N left at 4", got.N, got.StableRuns)
			}
		})
	}
}

func TestSettleNeedsFiveRunsAtNNotOnlyFiveAlone(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("few-loaded")
	id := fresh("queue-few-loaded")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-few-alone", 5, 1, 10*time.Minute, 10*time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-few-loaded-slow", 2, 2, 40*time.Minute, time.Hour)...)
	forget(t, client, ids...)
	resetLedger(t, client, 2)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 2, 40*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 3}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 2 || got.StableRuns != 3 {
		t.Fatalf("N %d stable %d, want three loaded runs to be no evidence", got.N, got.StableRuns)
	}
}

func TestSettleCountsRunsThatFoundNothingToChange(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("no-changes")
	id := fresh("queue-no-changes")
	ids := []string{id}
	for i := range 4 {
		alone := fresh("queue-no-changes-alone")
		ids = append(ids, alone)
		seedSettled(t, client, alone, size, runner.OutcomeNoChanges, 1, 10*time.Minute, queueAt.Add(-time.Duration(i+1)*time.Hour))
	}
	forget(t, client, ids...)
	resetLedger(t, client, 1)
	seedSettled(t, client, id, size, runner.OutcomeNoChanges, 1, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 1, StableRuns: 4}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client).N; got != 2 {
		t.Fatalf("N = %d, want five no-change runs to count as stable and raise it to 2", got)
	}
}

func TestSettleReadsOnlyRunsOfTheSettledRunsSize(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("own-size")
	other := "L-" + fresh("other-size")
	id := fresh("queue-own-size")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-own-alone", 5, 1, 10*time.Minute, 20*time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-own-loaded", 4, 2, 10*time.Minute, time.Hour)...)
	ids = append(ids, seedRuns(t, client, other, "queue-other-loaded", 5, 2, 60*time.Minute, 0)...)
	forget(t, client, ids...)
	resetLedger(t, client, 2)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 2, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 4}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client).N; got != 3 {
		t.Fatalf("N = %d, want runs of another size left out of the medians", got)
	}
}

func TestSettleSkipsARunWithNoSettlementTime(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("unsettled")
	id := fresh("queue-unsettled")
	unsettled := fresh("queue-unsettled-zero")
	ids := append([]string{id, unsettled}, seedRuns(t, client, size, "queue-unsettled-alone", 3, 1, 10*time.Minute, time.Hour)...)
	if _, err := client.Collection(runsCollection).Doc(unsettled).Set(context.Background(), map[string]any{
		sizeField: size, outcomeField: string(runner.OutcomePROpened), claimConcurrencyField: int64(1), settledAtField: time.Time{},
	}); err != nil {
		t.Fatal(err)
	}
	forget(t, client, ids...)
	resetLedger(t, client, 1)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 1, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 1, StableRuns: 4}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 1 || got.StableRuns != 4 {
		t.Fatalf("N %d stable %d, want four settled runs to be no evidence", got.N, got.StableRuns)
	}
}

func TestSettleTakesTheMedianOfOnlyTheLatestFiveLoadedRuns(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("latest-five")
	id := fresh("queue-latest-five")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-five-alone", 5, 1, 10*time.Minute, 50*time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-five-recent", 1, 2, 10*time.Minute, time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-five-slow", 3, 2, 40*time.Minute, 2*time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-five-old", 1, 2, 10*time.Minute, 10*time.Hour)...)
	forget(t, client, ids...)
	resetLedger(t, client, 2)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 2, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 3}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client).N; got != 1 {
		t.Fatalf("N = %d, want the slow median of the latest five to halve it", got)
	}
}

func TestMedianDurationIsTheLowerMiddleOfTheSortedValues(t *testing.T) {
	for name, tt := range map[string]struct {
		in   []time.Duration
		want time.Duration
	}{
		"none":           {nil, 0},
		"an odd count":   {[]time.Duration{3, 1, 2}, 2},
		"an even count":  {[]time.Duration{4, 1, 3, 2}, 2},
		"a single value": {[]time.Duration{7}, 7},
	} {
		t.Run(name, func(t *testing.T) {
			if got := medianDuration(tt.in); got != tt.want {
				t.Fatalf("median = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReleaseDropsOnlyTheReleasedRunsReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	kept := queuedRun(fresh("queue-rel-kept"), 1)
	released := queuedRun(fresh("queue-rel-gone"), 2)
	kept.Repo = "octo/kept"
	forget(t, client, kept.RunID, released.RunID)
	resetLedger(t, client, 5)
	res := dispatcher.Reservation{ProviderCost: money.Dollar}
	for _, r := range []dispatcher.Queued{kept, released} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
		if ok, _, err := q.TryClaim(ctx, r.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
			t.Fatalf("claim %s: ok %v, err %v", r.RunID, ok, err)
		}
	}
	if err := q.Release(ctx, released.RunID, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	ledger := readLedgerDoc(t, client)
	if _, ok := ledger.Reservations[reservationKey(kept.RunID, 0, dispatcher.StageBuild)]; !ok || len(ledger.Reservations) != 1 {
		t.Fatalf("reservations = %v, want only the run still in flight", slices.Sorted(maps.Keys(ledger.Reservations)))
	}
}

func TestReleaseReturnsARunThatHoldsNoReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rel-none"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(dispatchCollection).Doc(ledgerDocID).Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.Release(ctx, run.RunID, queueAt.Add(time.Minute)); err != nil {
		t.Fatalf("release with no ledger to drop a reservation from: %v", err)
	}
}

func TestSettleIgnoresARunClaimedBelowNEvenWhereNHasEvidence(t *testing.T) {
	q, client := queue(t)
	size := "S-" + fresh("below-n")
	id := fresh("queue-below-n")
	ids := append([]string{id}, seedRuns(t, client, size, "queue-below-alone", 5, 1, 10*time.Minute, time.Hour)...)
	ids = append(ids, seedRuns(t, client, size, "queue-below-loaded", 5, 3, 40*time.Minute, time.Hour)...)
	forget(t, client, ids...)
	resetLedger(t, client, 3)
	seedSettled(t, client, id, size, runner.OutcomePROpened, 1, 10*time.Minute, queueAt)
	holdReservation(t, client, ledgerDoc{N: 3, StableRuns: 2}, map[string]time.Time{id: queueAt})
	if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	if got := readLedgerDoc(t, client); got.N != 3 || got.StableRuns != 2 {
		t.Fatalf("N %d stable %d, want a run claimed below N to leave both alone", got.N, got.StableRuns)
	}
}

func TestSettleDropsTheReservationOfARunWithNoRow(t *testing.T) {
	q, client := queue(t)
	id := fresh("queue-no-row")
	forget(t, client, id)
	resetLedger(t, client, 2)
	holdReservation(t, client, ledgerDoc{N: 2, StableRuns: 3}, map[string]time.Time{id: queueAt, "other-" + id: queueAt.Add(time.Minute)})
	if err := q.Settle(context.Background(), id, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	got := readLedgerDoc(t, client)
	if _, held := got.Reservations[reservationKey(id, 0, dispatcher.StageBuild)]; held || got.N != 2 || got.StableRuns != 3 {
		t.Fatalf("reservations %v, N %d stable %d, want the reservation dropped and N left alone", got.Reservations, got.N, got.StableRuns)
	}
	if !got.ChangedAfter.IsZero() {
		t.Fatalf("changed_after = %v, want it left alone while N did not move", got.ChangedAfter)
	}
}

func TestRecordVerdictWritesOnlyTheVerdictField(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-verdict"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := q.RecordVerdict(ctx, run.RunID, "restricted"); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[verdictField] != "restricted" || snap.Data()[stateField] != stateQueued || snap.Data()[repoField] != run.Repo {
		t.Fatalf("run = %v, want the verdict set and state and repo untouched", snap.Data())
	}
}

func TestRecordVerdictRefusesARunThatDoesNotExist(t *testing.T) {
	q, _ := queue(t)
	if err := q.RecordVerdict(context.Background(), fresh("queue-verdict-missing"), "allowed"); err == nil {
		t.Fatal("a verdict was written onto a run that does not exist")
	}
}

func TestBuildModelReadsTheTicketsBuildLabel(t *testing.T) {
	for name, tc := range map[string]struct {
		row  map[string]any
		want string
	}{
		"a named build model":    {map[string]any{modelLabelsField: map[string]any{"build": "p/model"}}, "p/model"},
		"labels without build":   {map[string]any{modelLabelsField: map[string]any{"review": "p/other"}}, ""},
		"no labels at all":       {map[string]any{}, ""},
		"labels of another type": {map[string]any{modelLabelsField: "p/model"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := buildModel(tc.row); got != tc.want {
				t.Fatalf("buildModel = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTryClaimOnAFreeTierRecordsThePlanAndItsModels(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-claim-free"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	free := runner.ModelLabels{Build: "command-code/free-a", Review: "command-code/free-b", Plan: []string{"command-code/free-a", "command-code/free-b"}}
	facts := openFacts
	facts.Model, facts.LastResort = free.Build, &free
	if ok, binding, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{RunnerMinutes: 12}); err != nil || !ok {
		t.Fatalf("ok = %v, binding = %q, err = %v", ok, binding, err)
	}
	rec, err := NewRecords(client).GetRecord(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Plan != "command-code" || !rec.LastResort || !reflect.DeepEqual(rec.RunModels(), free) {
		t.Fatalf("plan %q, last resort %v, models %+v, want the free tier's plan and models", rec.Plan, rec.LastResort, rec.RunModels())
	}
	if entry := readLedgerDoc(t, client).Reservations[reservationKey(run.RunID, 0, dispatcher.StageBuild)]; entry.ProviderCostMicros != 0 || entry.RunnerMinutes != 12 {
		t.Fatalf("reservation = %+v, want runner minutes and no provider cost", entry)
	}

	if err := q.Release(ctx, run.RunID, queueAt); err != nil {
		t.Fatal(err)
	}
	facts.Model, facts.LastResort = "command-code/paid", nil
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, facts, dispatcher.Reservation{}); err != nil || !ok {
		t.Fatalf("ok = %v, err = %v, want the run reclaimed on the paid plan", ok, err)
	}
	rec, err = NewRecords(client).GetRecord(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.LastResort || !reflect.DeepEqual(rec.LastResortModels, runner.ModelLabels{}) {
		t.Fatalf("last resort %v, models %+v, want both cleared by a paid claim", rec.LastResort, rec.LastResortModels)
	}
}

func TestTryClaimBooksTheStageAndWriteSetOnTheReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-stage"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{
		ProviderCost: money.Dollar, Stage: dispatcher.StageBuild, Files: []string{"a.go", "b.go"},
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	entry, ok := readLedgerDoc(t, client).Reservations[reservationKey(run.RunID, 0, dispatcher.StageBuild)]
	if !ok || entry.Stage != dispatcher.StageBuild || !slices.Equal(entry.Files, res.Files) {
		t.Fatalf("reservation = %+v, want the stage and write set booked for the overlap check", entry)
	}
}

func TestStopEndsARunAndReleasesItsPlanReservation(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-stop-plan"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{
		ProviderCost: money.FromUSD(0.25), RunnerMinutes: 4, Stage: dispatcher.StagePlan,
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("plan claim: ok %v, err %v", ok, err)
	}
	// The plan stage stopped before writing an artifact, so record-plan-stage
	// ends its run rather than leaving the reservation held forever.
	stoppedAt := queueAt.Add(time.Minute)
	if err := q.Stop(ctx, run.RunID, runner.StopPlanStageFailed, "the plan stage failure", stoppedAt); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data[stateField] != stateStopped {
		t.Fatalf("state = %v after the stop, want %s", data[stateField], stateStopped)
	}
	if data[stopReasonField] != string(runner.StopPlanStageFailed) || data[stopDetailField] != "the plan stage failure" {
		t.Fatalf("stop = %v/%v, want the reason and its detail recorded", data[stopReasonField], data[stopDetailField])
	}
	if data[planSpendMicrosField] != int64(res.ProviderCost) || data[planSpendMinutesField] != res.RunnerMinutes {
		t.Fatalf("plan spend = %v/%v, want the reserved estimate booked", data[planSpendMicrosField], data[planSpendMinutesField])
	}
	stamped, ok := data[planSettledAtField].(time.Time)
	if !ok || !stamped.Equal(stoppedAt) {
		t.Fatalf("plan settled at = %v, want the stop's own time: a stopped plan never reaches WritePlan", data[planSettledAtField])
	}
	if _, ok := readLedgerDoc(t, client).Reservations[reservationKey(run.RunID, 0, dispatcher.StagePlan)]; ok {
		t.Fatal("the plan reservation was not dropped")
	}
	// The stop is not a release: the run does not come back as a candidate.
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cands {
		if cands[i].RunID == run.RunID {
			t.Fatalf("stopped run %s came back as a candidate", run.RunID)
		}
	}
	// A second stop, with no reservation left, is a no-op rather than an error.
	if err := q.Stop(ctx, run.RunID, runner.StopPlanStageFailed, "again", stoppedAt); err != nil {
		t.Fatalf("stopping an already-stopped run: %v", err)
	}
}

func TestSettlePlansARunForItsBuild(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-plan-settle"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{
		ProviderCost: money.FromUSD(0.25), RunnerMinutes: 4, Stage: dispatcher.StagePlan,
	}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("plan claim: ok %v, err %v", ok, err)
	}
	// The plan artifact reaches the row the way record-plan-stage writes it.
	if err := NewRecords(client).WritePlan(ctx, run.RunID, runner.PlanRecord{
		Files: []string{"a.go", "b.go"}, BaseSHA: "abcdef0123456789abcdef0123456789abcdef01",
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.SettlePlan(ctx, run.RunID, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != statePlanned {
		t.Fatalf("state = %v after the plan settled, want %s", snap.Data()[stateField], statePlanned)
	}
	if snap.Data()[planSpendMicrosField] != int64(res.ProviderCost) || snap.Data()[planSpendMinutesField] != res.RunnerMinutes {
		t.Fatalf("plan spend = %v/%v, want the reserved estimate booked", snap.Data()[planSpendMicrosField], snap.Data()[planSpendMinutesField])
	}
	if _, ok := readLedgerDoc(t, client).Reservations[reservationKey(run.RunID, 0, dispatcher.StagePlan)]; ok {
		t.Fatal("the plan reservation was not dropped")
	}
	if got := readLedgerDoc(t, client).N; got != 1 {
		t.Fatalf("N = %d, want a plan settlement to leave it alone", got)
	}
	// A rerun record-plan-stage job settles again without error.
	if err := q.SettlePlan(ctx, run.RunID, attemptOf(0)); err != nil {
		t.Fatalf("settling an already-settled plan: %v", err)
	}
	// The run is now a candidate for its build, carrying its write set.
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *dispatcher.Candidate
	for i := range cands {
		if cands[i].RunID == run.RunID {
			found = &cands[i]
		}
	}
	if found == nil || found.Stage != dispatcher.StageBuild || !slices.Equal(found.PlanFiles, []string{"a.go", "b.go"}) {
		t.Fatalf("candidates = %+v, want the planned run waiting for its build with its write set", cands)
	}
}

func TestReleaseReturnsABuildStageClaimToPlanned(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rel-build"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StageBuild, Files: []string{"a.go"}}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim: ok %v, err %v", ok, err)
	}
	if err := q.Release(ctx, run.RunID, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != statePlanned {
		t.Fatalf("state = %v, want %s: the run's plan still stands", snap.Data()[stateField], statePlanned)
	}
}

// TestPlannedListsTheRunsAWriteSetIsKnownFor is what the S-overlap draft reads:
// the runs whose plans are still in flight. The one waiting between its stages
// is the case a ledger read would miss, since settling its plan dropped the only
// reservation it ever held.
func TestPlannedListsTheRunsAWriteSetIsKnownFor(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	waiting := queuedRun(fresh("queue-planned-waiting"), 1)
	building := queuedRun(fresh("queue-planned-building"), 1)
	done := queuedRun(fresh("queue-planned-done"), 1)
	single := queuedRun(fresh("queue-planned-single"), 1)
	forget(t, client, waiting.RunID, building.RunID, done.RunID, single.RunID)
	resetLedger(t, client, 5)
	for _, r := range []dispatcher.Queued{waiting, building, done, single} {
		if err := q.Enqueue(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Its plan stage settled, so it waits for its build with its plan standing
	// and no reservation at all.
	for _, r := range []dispatcher.Queued{waiting, building, done} {
		if _, _, err := q.TryClaim(ctx, r.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{
			ProviderCost: money.FromUSD(0.05), Stage: dispatcher.StagePlan,
		}); err != nil {
			t.Fatal(err)
		}
		if err := NewRecords(client).WritePlan(ctx, r.RunID, runner.PlanRecord{
			Files: []string{r.RunID + ".go"}, BaseSHA: "abcdef0123456789abcdef0123456789abcdef01",
		}); err != nil {
			t.Fatal(err)
		}
		if err := q.SettlePlan(ctx, r.RunID, attemptOf(0)); err != nil {
			t.Fatal(err)
		}
	}
	// Its build is claimed, and its record then written: a settled run's plan is
	// history, not a write set in flight.
	for _, r := range []dispatcher.Queued{building, done} {
		if _, _, err := q.TryClaim(ctx, r.RunID, queueAt, generousBudget, openFacts, dispatcher.Reservation{
			ProviderCost: money.Dollar, Stage: dispatcher.StageBuild, Files: []string{r.RunID + ".go"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewRecords(client).PutRecord(ctx, done.RunID, runner.Record{
		RunID: done.RunID, SettledAt: queueAt, SettledProviderCostMicros: money.Dollar,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Settle(ctx, done.RunID, attemptOf(0)); err != nil {
		t.Fatal(err)
	}
	// A single-stage run holds no plan to write, so its write set is unknown.
	if _, _, err := q.TryClaim(ctx, single.RunID, queueAt, generousBudget, openFacts,
		dispatcher.Reservation{ProviderCost: money.Dollar}); err != nil {
		t.Fatal(err)
	}

	flight, err := q.Planned(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]dispatcher.InFlight{}
	for _, f := range flight {
		got[f.Ticket] = f
	}
	for _, r := range []dispatcher.Queued{waiting, building} {
		f, ok := got[r.RunID]
		if !ok {
			t.Fatalf("%s is not listed among %v", r.RunID, flight)
		}
		if !slices.Equal(f.Files, []string{r.RunID + ".go"}) || f.Repo != r.Repo || f.Size != "M" {
			t.Fatalf("%s = %+v, want its own write set", r.RunID, f)
		}
	}
	// A recorded run's plan is finished, and a run with no plan writes an
	// unknown set: neither is what a pull request opening now can collide with.
	if _, listed := got[done.RunID]; listed {
		t.Errorf("%s is listed though its run is recorded", done.RunID)
	}
	if _, listed := got[single.RunID]; listed {
		t.Errorf("%s is listed though it holds no plan", single.RunID)
	}
}

// claimBoth enqueues two runs in one repository, records second's plan, then
// claims first's build writing firstFiles and second's writing secondFiles, and
// returns what second's claim logged.
func claimBoth(t *testing.T, prefix string, firstFiles, secondFiles []string) (first, second dispatcher.Queued, logged string) {
	t.Helper()
	q, client := queue(t)
	ctx := context.Background()
	first, second = queuedRun(fresh(prefix+"-a"), 1), queuedRun(fresh(prefix+"-b"), 2)
	forget(t, client, first.RunID, second.RunID)
	resetLedger(t, client, 2)
	for _, run := range []dispatcher.Queued{first, second} {
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	// The plan stage records the write set on the row before its build is
	// claimed, the way record-plan-stage does.
	if err := NewRecords(client).WritePlan(ctx, second.RunID, runner.PlanRecord{Files: secondFiles}); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := q.TryClaim(ctx, first.RunID, queueAt, generousBudget, openFacts,
		dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StageBuild, Files: firstFiles}); err != nil || !ok {
		t.Fatalf("first claim: ok %v, err %v", ok, err)
	}
	var buf bytes.Buffer
	ok, binding, err := q.WithLogger(slog.New(slog.NewJSONHandler(&buf, nil))).TryClaim(ctx, second.RunID, queueAt, generousBudget, openFacts,
		dispatcher.Reservation{ProviderCost: money.Dollar, Stage: dispatcher.StageBuild, Files: secondFiles})
	if err != nil || !ok {
		t.Fatalf("second claim: ok %v, binding %q, err %v, want both runs admitted together", ok, binding, err)
	}
	return first, second, buf.String()
}

func TestTryClaimAdmitsTwoRunsInOneRepositoryWithDisjointWriteSets(t *testing.T) {
	_, _, logged := claimBoth(t, "queue-disjoint", []string{"a.go"}, []string{"b.go"})
	if strings.Contains(logged, "admittedOverlapping") {
		t.Fatalf("log = %q, want no overlap logged for disjoint write sets", logged)
	}
}

func TestTryClaimAdmitsAndLogsTwoRunsWhoseWriteSetsOverlap(t *testing.T) {
	first, second, logged := claimBoth(t, "queue-overlap", []string{"a.go"}, []string{"a.go", "b.go"})
	var line struct {
		Level    string   `json:"level"`
		Msg      string   `json:"msg"`
		Run      string   `json:"run"`
		Overlaps []string `json:"overlaps"`
	}
	if err := json.Unmarshal([]byte(logged), &line); err != nil {
		t.Fatalf("log = %q: %v", logged, err)
	}
	if line.Level != "INFO" || line.Msg != "admittedOverlapping" || line.Run != second.RunID || !slices.Equal(line.Overlaps, []string{first.RunID}) {
		t.Fatalf("logged %+v, want INFO admittedOverlapping run %s overlaps [%s]", line, second.RunID, first.RunID)
	}
}

func TestReleaseReturnsAPlanStageClaimToTheQueue(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rel-plan"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{ProviderCost: money.Dollar, RunnerMinutes: 3, Stage: dispatcher.StagePlan}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("plan claim: ok %v, err %v", ok, err)
	}
	if _, ok := readLedgerDoc(t, client).Reservations[reservationKey(run.RunID, 0, dispatcher.StagePlan)]; !ok {
		t.Fatal("the plan-stage reservation was not booked under the plan key")
	}
	if err := q.Release(ctx, run.RunID, queueAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()[stateField] != stateQueued {
		t.Fatalf("state = %v, want %s: a run whose plan never ran is unplanned again", snap.Data()[stateField], stateQueued)
	}
	if _, ok := readLedgerDoc(t, client).Reservations[reservationKey(run.RunID, 0, dispatcher.StagePlan)]; ok {
		t.Fatal("the plan-stage reservation was not dropped")
	}
}

// seedRow writes a run row directly, for the terminal shapes Requeue reads that
// no single store call produces on its own.
func seedRow(t *testing.T, client *firestore.Client, id string, fields map[string]any) {
	t.Helper()
	forget(t, client, id)
	if _, err := client.Collection(runsCollection).Doc(id).Set(context.Background(), fields); err != nil {
		t.Fatal(err)
	}
}

// readRow reads a run row as a test sees it.
func readRow(t *testing.T, client *firestore.Client, id string) map[string]any {
	t.Helper()
	snap, err := client.Collection(runsCollection).Doc(id).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap.Data()
}

func TestRequeueStateDecidesFromTheRowAlone(t *testing.T) {
	for _, tt := range []struct {
		name string
		row  map[string]any
		want string
		err  error
	}{
		{"queued is dispatchable", map[string]any{stateField: stateQueued}, "", ErrRunDispatchable},
		{"planned is dispatchable", map[string]any{stateField: statePlanned}, "", ErrRunDispatchable},
		{"a queued row carrying an outcome has ended",
			map[string]any{stateField: stateQueued, outcomeField: string(runner.OutcomePROpened)}, stateQueued, nil},
		{"a planned row carrying an outcome has ended",
			map[string]any{stateField: statePlanned, outcomeField: string(runner.OutcomeBuilt)}, stateQueued, nil},
		{"a queued row carrying a stop reason has ended",
			map[string]any{stateField: stateQueued, stopReasonField: string(runner.StopRebaseConflict)}, stateQueued, nil},
		{"a planned row carrying a stop reason has ended",
			map[string]any{stateField: statePlanned, stopReasonField: string(runner.StopRebaseConflict)}, stateQueued, nil},
		{"a settled plan alone leaves a queued row dispatchable",
			map[string]any{stateField: stateQueued, planFilesField: []any{"a.go"}, planSettledAtField: queueAt},
			"", ErrRunDispatchable},
		{"waiting is parked", map[string]any{stateField: stateWaiting}, "", ErrRunWaiting},
		{"claimed with no outcome is in flight",
			map[string]any{stateField: stateClaimed, claimedAtField: queueAt}, "", ErrRunInFlight},
		{"stopped returns to queued", map[string]any{stateField: stateStopped}, stateQueued, nil},
		{"stopped with a settled plan returns to planned",
			map[string]any{stateField: stateStopped, planFilesField: []any{"a.go"}, planSettledAtField: queueAt},
			statePlanned, nil},
		{"plan files with no stamp stays queued",
			map[string]any{stateField: stateStopped, planFilesField: []any{"a.go"}}, stateQueued, nil},
		{"a zero stamp is not a settled plan",
			map[string]any{stateField: stateStopped, planFilesField: []any{"a.go"}, planSettledAtField: time.Time{}},
			stateQueued, nil},
		{"abandoned returns to queued", map[string]any{stateField: stateAbandoned}, stateQueued, nil},
		{"abandoned with a settled plan returns to planned",
			map[string]any{stateField: stateAbandoned, planFilesField: []any{"a.go"}, planSettledAtField: queueAt},
			statePlanned, nil},
		{"claimed with a recorded outcome returns to queued",
			map[string]any{stateField: stateClaimed, outcomeField: string(runner.OutcomeStopped)}, stateQueued, nil},
		{"claimed with a stop reason returns to queued",
			map[string]any{stateField: stateClaimed, stopReasonField: string(runner.StopRebaseConflict)}, stateQueued, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := requeueState(tt.row)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if got != tt.want {
				t.Fatalf("state = %q, want %q", got, tt.want)
			}
		})
	}
}

// A state outside the switch is a bug in whatever wrote the row, and never a
// reason to hand the run back to a poll — nor one of the reasons an operator
// reads, which each name a different action.
func TestRequeueRefusesAStateItDoesNotKnow(t *testing.T) {
	got, err := requeueState(map[string]any{stateField: "exploded"})
	if err == nil {
		t.Fatalf("state = %q from an unknown state, want a refusal", got)
	}
	for _, sentinel := range []error{ErrRunDispatchable, ErrRunWaiting, ErrRunInFlight} {
		if errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want a refusal that is not an operator-facing reason", err)
		}
	}
}

func TestRequeueReturnsAStoppedRunToTheQueue(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-requeue-stop"), 1)
	forget(t, client, run.RunID)
	resetLedger(t, client, 1)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	res := dispatcher.Reservation{ProviderCost: money.FromUSD(0.25), RunnerMinutes: 4, Stage: dispatcher.StagePlan}
	if ok, _, err := q.TryClaim(ctx, run.RunID, queueAt, generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("plan claim: ok %v, err %v", ok, err)
	}
	stoppedAt := queueAt.Add(time.Minute)
	if err := q.Stop(ctx, run.RunID, runner.StopPlanStageFailed, "the plan stage failure", stoppedAt); err != nil {
		t.Fatal(err)
	}
	// A stopped row a poll has not requeued stays invisible to Candidates.
	if listed(t, q, ctx, run.RunID) {
		t.Fatalf("stopped run %s came back as a candidate", run.RunID)
	}
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{
		{Path: waitingOnField, Value: dispatcher.ConditionRepoBusy},
	}); err != nil {
		t.Fatal(err)
	}
	retriedAt := stoppedAt.Add(time.Minute)
	got, err := q.Requeue(ctx, run.RunID, retriedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != stateQueued || got.Attempt != 1 {
		t.Fatalf("result = %+v, want the run back in %s as attempt 1", got, stateQueued)
	}
	data := readRow(t, client, run.RunID)
	if data[stateField] != stateQueued {
		t.Fatalf("state = %v, want %s", data[stateField], stateQueued)
	}
	for _, field := range []string{outcomeField, buildOutcomeField, stopReasonField, stopDetailField, claimedAtField, waitingOnField} {
		if _, ok := data[field]; ok {
			t.Errorf("field %s survived the requeue: %v", field, data[field])
		}
	}
	if stamped, ok := data[retriedAtField].(time.Time); !ok || !stamped.Equal(retriedAt) {
		t.Fatalf("retried_at = %v, want the call's own time", data[retriedAtField])
	}
	if data[attemptField] != int64(1) {
		t.Fatalf("attempt = %v, want 1", data[attemptField])
	}
	if stamped, ok := data[queuedAtField].(time.Time); !ok || !stamped.Equal(retriedAt) {
		t.Fatalf("queued_at = %v, want the call's own time", data[queuedAtField])
	}
	if !listed(t, q, ctx, run.RunID) {
		t.Fatalf("requeued run %s is not a candidate", run.RunID)
	}
	if ok, why, err := q.TryClaim(ctx, run.RunID, retriedAt.Add(time.Minute), generousBudget, openFacts, res); err != nil || !ok {
		t.Fatalf("claim after the requeue: ok %v, why %q, err %v", ok, why, err)
	}
}

func TestRequeueReturnsAStoppedRunWithASettledPlanToPlanned(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("queue-requeue-plan")
	seedRow(t, client, id, map[string]any{
		stateField:         stateStopped,
		outcomeField:       string(runner.OutcomeStopped),
		stopReasonField:    string(runner.StopPlanStageFailed),
		planFilesField:     []string{"a.go"},
		planSettledAtField: queueAt,
	})
	got, err := q.Requeue(ctx, id, queueAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != statePlanned {
		t.Fatalf("state = %q, want %s: the run's plan still stands", got.State, statePlanned)
	}
	data := readRow(t, client, id)
	if st := data[stateField]; st != statePlanned {
		t.Fatalf("stored state = %v, want the returned %s", st, statePlanned)
	}
	if !slices.Equal(stringList(data, planFilesField), []string{"a.go"}) {
		t.Fatalf("plan_files = %v, want the settled write set kept", data[planFilesField])
	}
	if _, ok := data[stopReasonField]; ok {
		t.Fatal("the stop reason survived the requeue")
	}
}

func TestRequeueCountsTheAttempt(t *testing.T) {
	q, client := queue(t)
	id := fresh("queue-requeue-attempt")
	seedRow(t, client, id, map[string]any{stateField: stateStopped, attemptField: int64(1)})
	got, err := q.Requeue(context.Background(), id, queueAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempt != 2 || readRow(t, client, id)[attemptField] != int64(2) {
		t.Fatalf("attempt = %d/%v, want 2", got.Attempt, readRow(t, client, id)[attemptField])
	}
}

func TestRequeueReturnsAClaimedRunWithARecordedOutcome(t *testing.T) {
	q, client := queue(t)
	id := fresh("queue-requeue-claimed")
	seedRow(t, client, id, map[string]any{
		stateField:     stateClaimed,
		claimedAtField: queueAt,
		outcomeField:   string(runner.OutcomeAgentFailed),
	})
	got, err := q.Requeue(context.Background(), id, queueAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != stateQueued || got.Attempt != 1 {
		t.Fatalf("result = %+v, want %s as attempt 1", got, stateQueued)
	}
	data := readRow(t, client, id)
	if _, ok := data[outcomeField]; ok {
		t.Fatal("the recorded outcome survived the requeue")
	}
	if st := data[stateField]; st != stateQueued {
		t.Fatalf("stored state = %v, want the returned %s", st, stateQueued)
	}
}

func TestRequeueRefusesARunWithItsOwnReasonAndLeavesItUnchanged(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	for _, tt := range []struct {
		name   string
		fields map[string]any
		want   error
	}{
		{"queued", map[string]any{stateField: stateQueued}, ErrRunDispatchable},
		{"planned", map[string]any{stateField: statePlanned, planFilesField: []string{"a.go"}, planSettledAtField: queueAt}, ErrRunDispatchable},
		{"waiting", map[string]any{stateField: stateWaiting}, ErrRunWaiting},
		{"claimed with no outcome", map[string]any{stateField: stateClaimed, claimedAtField: queueAt}, ErrRunInFlight},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := fresh("queue-requeue-refuse")
			seedRow(t, client, id, tt.fields)
			before := readRow(t, client, id)
			if _, err := q.Requeue(ctx, id, queueAt.Add(time.Minute)); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if after := readRow(t, client, id); !reflect.DeepEqual(before, after) {
				t.Fatalf("row changed by a refused requeue: %v, want %v", after, before)
			}
		})
	}
}

func TestRequeueRefusesARunWithNoRow(t *testing.T) {
	q, _ := queue(t)
	if _, err := q.Requeue(context.Background(), fresh("queue-requeue-absent"), queueAt); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want the store's own not-found", err)
	}
}

func TestRequeueDropsBothOfTheRunsLedgerReservations(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("queue-requeue-res")
	forget(t, client, id)
	ledgerRef := client.Collection(dispatchCollection).Doc(ledgerDocID)
	if _, err := ledgerRef.Set(ctx, ledgerDoc{Reservations: map[string]reservationEntry{
		reservationKey(id, 0, dispatcher.StageBuild): {TicketID: id, Stage: dispatcher.StageBuild},
		reservationKey(id, 0, dispatcher.StagePlan):  {TicketID: id, Stage: dispatcher.StagePlan},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = ledgerRef.Delete(ctx) })
	seedRow(t, client, id, map[string]any{stateField: stateStopped})
	if _, err := q.Requeue(ctx, id, queueAt); err != nil {
		t.Fatal(err)
	}
	if res := readLedgerDoc(t, client).Reservations; len(res) != 0 {
		t.Fatalf("reservations = %v, want both of the run's dropped", res)
	}
}

func TestRequeueAfterConflictReturnsTheRunToQueuedWhateverItsPlan(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("queue-conflict-requeue")
	seedRow(t, client, id, map[string]any{
		stateField:                stateClaimed,
		claimedAtField:            queueAt,
		priorityField:             int64(1),
		outcomeField:              string(runner.OutcomeInfraFailure),
		buildOutcomeField:         string(runner.OutcomeBuilt),
		stopReasonField:           string(runner.StopRebaseConflict),
		planFilesField:            []string{"a.go"},
		planSettledAtField:        queueAt,
		settledProviderCostField:  int64(250_000),
		settledRunnerMinutesField: int64(4),
	})
	retriedAt := queueAt.Add(time.Minute)
	got, err := q.RequeueAfterConflict(ctx, id, retriedAt, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != stateQueued || got.Attempt != 1 {
		t.Fatalf("result = %+v, want the run back in %s as attempt 1", got, stateQueued)
	}
	data := readRow(t, client, id)
	if data[stateField] != stateQueued || data[attemptField] != int64(1) {
		t.Fatalf("stored state/attempt = %v/%v, want %s/1", data[stateField], data[attemptField], stateQueued)
	}
	for _, field := range []string{outcomeField, buildOutcomeField, stopReasonField, claimedAtField} {
		if _, ok := data[field]; ok {
			t.Errorf("field %s survived the requeue: %v", field, data[field])
		}
	}
	if data[priorSettledCostField] != int64(250_000) || data[priorSettledMinutesField] != int64(4) {
		t.Fatalf("prior settled = %v micros, %v minutes, want 250000 and 4", data[priorSettledCostField], data[priorSettledMinutesField])
	}
	if !listed(t, q, ctx, id) {
		t.Fatalf("requeued run %s is not a candidate", id)
	}
}

// TestRequeueAfterConflictReturnsAQueuedRowThatEnded asserts a row a manual run
// left queued with its outcome can be put back for its build, rather than being
// refused as one still to be dispatched.
func TestRequeueAfterConflictReturnsAQueuedRowThatEnded(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-conflict-queued-ended"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(run.RunID).Update(ctx, []firestore.Update{
		{Path: outcomeField, Value: string(runner.OutcomeInfraFailure)},
		{Path: stopReasonField, Value: string(runner.StopRebaseConflict)},
		{Path: settledProviderCostField, Value: int64(250_000)},
		{Path: settledRunnerMinutesField, Value: int64(4)},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := q.RequeueAfterConflict(ctx, run.RunID, queueAt.Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != stateQueued || got.Attempt != 1 {
		t.Fatalf("result = %+v, want the run back in %s as attempt 1", got, stateQueued)
	}
	data := readRow(t, client, run.RunID)
	if data[stateField] != stateQueued {
		t.Fatalf("stored state = %v, want %s", data[stateField], stateQueued)
	}
	for _, field := range []string{outcomeField, stopReasonField} {
		if v, ok := data[field]; ok {
			t.Errorf("field %s survived the requeue: %v", field, v)
		}
	}
	if data[priorSettledCostField] != int64(250_000) || data[priorSettledMinutesField] != int64(4) {
		t.Fatalf("prior settled = %v micros, %v minutes, want the ended attempt's own spend carried",
			data[priorSettledCostField], data[priorSettledMinutesField])
	}
	if !listed(t, q, ctx, run.RunID) {
		t.Fatalf("requeued run %s is not a candidate", run.RunID)
	}
}

func TestRequeueAfterConflictCarriesTheRowsWholeSettledTotal(t *testing.T) {
	q, client := queue(t)
	id := fresh("queue-conflict-total")
	seedRow(t, client, id, map[string]any{
		stateField:                stateClaimed,
		outcomeField:              string(runner.OutcomeInfraFailure),
		attemptField:              int64(1),
		priorSettledCostField:     int64(100_000),
		priorSettledMinutesField:  int64(1),
		settledProviderCostField:  int64(300_000),
		settledRunnerMinutesField: int64(3),
	})
	got, err := q.RequeueAfterConflict(context.Background(), id, queueAt, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", got.Attempt)
	}
	data := readRow(t, client, id)
	if data[priorSettledCostField] != int64(300_000) || data[priorSettledMinutesField] != int64(3) {
		t.Fatalf("prior settled = %v micros, %v minutes, want the row's settled 300000 and 3 not added to the old prior",
			data[priorSettledCostField], data[priorSettledMinutesField])
	}
}

func TestRequeueAfterConflictRefusesAtTheCapAndLeavesTheRowUnchanged(t *testing.T) {
	q, client := queue(t)
	id := fresh("queue-conflict-cap")
	seedRow(t, client, id, map[string]any{
		stateField:               stateClaimed,
		outcomeField:             string(runner.OutcomeInfraFailure),
		stopReasonField:          string(runner.StopRebaseConflict),
		attemptField:             int64(1),
		settledProviderCostField: int64(250_000),
	})
	before := readRow(t, client, id)
	if _, err := q.RequeueAfterConflict(context.Background(), id, queueAt, 1); !errors.Is(err, ErrRetriesExhausted) {
		t.Fatalf("err = %v, want ErrRetriesExhausted", err)
	}
	if after := readRow(t, client, id); !reflect.DeepEqual(before, after) {
		t.Fatalf("row changed by a refused requeue: %v, want %v", after, before)
	}
}

func TestRequeueAfterConflictRefusesWhatRequeueRefuses(t *testing.T) {
	q, client := queue(t)
	for _, tt := range []struct {
		name   string
		fields map[string]any
		want   error
	}{
		{"queued", map[string]any{stateField: stateQueued}, ErrRunDispatchable},
		{"planned", map[string]any{stateField: statePlanned}, ErrRunDispatchable},
		{"waiting", map[string]any{stateField: stateWaiting}, ErrRunWaiting},
		{"claimed with no outcome", map[string]any{stateField: stateClaimed, claimedAtField: queueAt}, ErrRunInFlight},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := fresh("queue-conflict-refuse")
			seedRow(t, client, id, tt.fields)
			before := readRow(t, client, id)
			if _, err := q.RequeueAfterConflict(context.Background(), id, queueAt, 1); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if after := readRow(t, client, id); !reflect.DeepEqual(before, after) {
				t.Fatalf("row changed by a refused requeue: %v, want %v", after, before)
			}
		})
	}
}

func TestRequeueAfterConflictRefusesARunWithNoRow(t *testing.T) {
	q, _ := queue(t)
	if _, err := q.RequeueAfterConflict(context.Background(), fresh("queue-conflict-absent"), queueAt, 1); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want the store's own not-found", err)
	}
}

func TestRequeueAfterConflictDropsBothOfTheRunsLedgerReservations(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("queue-conflict-res")
	forget(t, client, id)
	ledgerRef := client.Collection(dispatchCollection).Doc(ledgerDocID)
	if _, err := ledgerRef.Set(ctx, ledgerDoc{Reservations: map[string]reservationEntry{
		reservationKey(id, 0, dispatcher.StageBuild): {TicketID: id, Stage: dispatcher.StageBuild},
		reservationKey(id, 0, dispatcher.StagePlan):  {TicketID: id, Stage: dispatcher.StagePlan},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = ledgerRef.Delete(ctx) })
	seedRow(t, client, id, map[string]any{stateField: stateClaimed, outcomeField: string(runner.OutcomeInfraFailure)})
	if _, err := q.RequeueAfterConflict(ctx, id, queueAt, 1); err != nil {
		t.Fatal(err)
	}
	if res := readLedgerDoc(t, client).Reservations; len(res) != 0 {
		t.Fatalf("reservations = %v, want both of the run's dropped", res)
	}
}

// listed reports whether Candidates holds runID.
func listed(t *testing.T, q *Queue, ctx context.Context, runID string) bool {
	t.Helper()
	cands, err := q.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cands {
		if cands[i].RunID == runID {
			return true
		}
	}
	return false
}

// TestClaimedSinceCountsTheRunsClaimedInsideTheWindow is the daily run cap: the
// runs a poll's delegations are counted from, by the claim stamp each one carries.
func TestClaimedSinceCountsTheRunsClaimedInsideTheWindow(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	since := queueAt
	before, err := q.ClaimedSince(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(id string, at time.Time) {
		t.Helper()
		forget(t, client, id)
		resetLedger(t, client, 1)
		if err := q.Enqueue(ctx, queuedRun(id, 1)); err != nil {
			t.Fatal(err)
		}
		if ok, binding, err := q.TryClaim(ctx, id, at, generousBudget, openFacts, dispatcher.Reservation{}); err != nil || !ok {
			t.Fatalf("claiming %s: ok = %v, binding = %q, err = %v", id, ok, binding, err)
		}
	}
	claim(fresh("queue-claimed-before"), since.Add(-time.Minute))
	n, err := q.ClaimedSince(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	if n != before {
		t.Fatalf("claimed since the window opened = %d, want the %d already counted: a claim before it is not spent from it", n, before)
	}
	claim(fresh("queue-claimed-inside"), since.Add(time.Minute))
	if n, err = q.ClaimedSince(ctx, since); err != nil {
		t.Fatal(err)
	}
	if n != before+1 {
		t.Fatalf("claimed since the window opened = %d, want %d", n, before+1)
	}
	claim(fresh("queue-claimed-at-since"), since)
	if n, err = q.ClaimedSince(ctx, since); err != nil {
		t.Fatal(err)
	}
	if n != before+2 {
		t.Fatalf("claimed since the window opened = %d, want %d: a claim stamped exactly at it counts", n, before+2)
	}
}

// TestNeedsRequeueReportsTheRowsOnlyAnOperatorReturns is the settled-row
// rule: the ticket of a run that has ended waits for the owner's own `runner
// requeue`, and one still queued or in flight does not.
func TestNeedsRequeueReportsTheRowsOnlyAnOperatorReturns(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	spare, queuedRunID := fresh("queue-needs-spare"), fresh("queue-needs-queued")
	stopped, inFlight, ended := fresh("queue-needs-stopped"), fresh("queue-needs-inflight"), fresh("queue-needs-ended")
	leftQueued, leftPlanned := fresh("queue-needs-left-queued"), fresh("queue-needs-left-planned")
	forget(t, client, spare, queuedRunID, stopped, inFlight, ended, leftQueued, leftPlanned)
	resetLedger(t, client, 5)
	// Each run needs its own repository: admission holds a second run back while
	// one is in flight in the same repository with an unknown write set.
	for _, id := range []string{queuedRunID, stopped, inFlight, ended, leftQueued, leftPlanned} {
		run := queuedRun(id, 1)
		run.Repo = "octo/" + id
		if err := q.Enqueue(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{stopped, inFlight, ended} {
		if ok, binding, err := q.TryClaim(ctx, id, queueAt, generousBudget, openFacts, dispatcher.Reservation{}); err != nil || !ok {
			t.Fatalf("claiming %s: ok = %v, binding = %q, err = %v", id, ok, binding, err)
		}
	}
	if err := q.Stop(ctx, stopped, runner.StopProjectionMissing, "no projection", queueAt); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(ended).Update(ctx,
		[]firestore.Update{{Path: outcomeField, Value: runner.OutcomeBuilt}}); err != nil {
		t.Fatal(err)
	}
	// A manual run's own result, recorded on a row the dispatcher had only
	// enqueued: the row stays where it was and carries the outcome.
	if _, err := client.Collection(runsCollection).Doc(leftQueued).Update(ctx,
		[]firestore.Update{{Path: outcomeField, Value: runner.OutcomePROpened}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(leftPlanned).Update(ctx, []firestore.Update{
		{Path: stateField, Value: statePlanned},
		{Path: outcomeField, Value: runner.OutcomePROpened},
	}); err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		id   string
		want bool
	}{
		"a ticket with no run row":        {id: spare},
		"a run still queued":              {id: queuedRunID},
		"a run still in flight":           {id: inFlight},
		"a run that stopped":              {id: stopped, want: true},
		"a claim with a recorded outcome": {id: ended, want: true},
		"a queued row with an outcome":    {id: leftQueued, want: true},
		"a planned row with an outcome":   {id: leftPlanned, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := q.NeedsRequeue(ctx, tt.id)
			if err != nil || got != tt.want {
				t.Fatalf("NeedsRequeue(%s) = %v, %v, want %v", tt.id, got, err, tt.want)
			}
		})
	}
}
