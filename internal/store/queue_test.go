package store

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
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

// forget removes the run and refusal documents one test wrote.
func forget(t *testing.T, client *firestore.Client, ids ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, id := range ids {
			_, _ = client.Collection(runsCollection).Doc(id).Delete(ctx)
			_, _ = client.Collection(dispatchCollection).Doc(rejectedPrefix + id).Delete(ctx)
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

func TestEnqueueWritesTheRunRecordQueued(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-enq"), 2)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	snap, err := client.Collection(runsCollection).Doc(run.RunID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := snap.Data()
	if data[stateField] != stateQueued || data[priorityField] != int64(2) || data[repoField] != "octo/scratch" {
		t.Fatalf("queued row = %v", data)
	}
	var rec runner.Record
	if err := snap.DataTo(&rec); err != nil {
		t.Fatal(err)
	}
	if rec.Ticket() != run.Ticket || !rec.StartedAt.Equal(queueAt) || !rec.UpdatedAt.Equal(queueAt) {
		t.Fatalf("record = %+v", rec)
	}
	// A poll that finds the same ticket delegated again must not start a second run.
	if err := q.Enqueue(ctx, run); !errors.Is(err, dispatcher.ErrAlreadyQueued) {
		t.Fatalf("err = %v, want ErrAlreadyQueued", err)
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

func TestClaimTakesTheHighestPriorityRunFirst(t *testing.T) {
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
		if err := q.Enqueue(ctx, queuedRun(id, priority)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range slices.Sorted(maps.Keys(priorities)) {
		claim, ok, err := q.Claim(ctx, queueAt)
		if err != nil || !ok || claim.RunID != want {
			t.Fatalf("claim = %+v, %v, %v, want %s", claim, ok, err, want)
		}
		if claim.Priority == 0 || claim.Repo != "octo/scratch" {
			t.Fatalf("claim = %+v", claim)
		}
		snap, err := client.Collection(runsCollection).Doc(want).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Data()[stateField] != stateClaimed {
			t.Fatalf("%s is %v after being claimed", want, snap.Data()[stateField])
		}
	}
	claim, ok, err := q.Claim(ctx, queueAt)
	if err != nil || ok || claim.RunID != "" {
		t.Fatalf("claim of an empty queue = %+v, %v, %v", claim, ok, err)
	}
}

func TestTwoPollsClaimOneRunBetweenThem(t *testing.T) {
	q, client := queue(t)
	run := queuedRun(fresh("queue-race"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		won   []string
		fails []error
	)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, ok, err := q.Claim(context.Background(), queueAt)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				fails = append(fails, err)
			case ok:
				won = append(won, claim.RunID)
			}
		}()
	}
	wg.Wait()
	if len(won) != 1 || won[0] != run.RunID {
		t.Fatalf("%d polls claimed a run, want exactly one: %v", len(won), won)
	}
	if len(fails) > 0 {
		t.Fatalf("a poll failed rather than reporting the run was taken: %v", fails)
	}
}

func TestReleaseReturnsAClaimedRunToTheQueue(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	run := queuedRun(fresh("queue-rel"), 1)
	forget(t, client, run.RunID)
	if err := q.Enqueue(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.Claim(ctx, queueAt); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
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
	claim, ok, err := q.Claim(ctx, queueAt)
	if err != nil || !ok || claim.RunID != run.RunID {
		t.Fatalf("the released run was not claimed again: %+v, %v, %v", claim, ok, err)
	}
}
