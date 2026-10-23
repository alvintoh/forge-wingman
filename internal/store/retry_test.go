package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const retryPR = "https://github.com/octo/scratch/pull/141"

// retryRow is a run that stopped after an attempt that opened a draft PR, on
// attempt 2, with a plan that stood and an attempt's settled spend.
func retryRow(extra map[string]any) map[string]any {
	row := map[string]any{
		stateField:                stateClaimed,
		claimedAtField:            queueAt,
		priorityField:             int64(1),
		repoField:                 "octo/scratch",
		sizeField:                 "M",
		attemptField:              int64(2),
		outcomeField:              string(runner.OutcomePROpened),
		prURLField:                retryPR,
		stopReasonField:           string(runner.StopRebaseConflict),
		settledAtField:            queueAt.Add(time.Minute),
		settledProviderCostField:  int64(250_000),
		settledRunnerMinutesField: int64(4),
		planFilesField:            []string{"a.go"},
		planSettledAtField:        queueAt,
	}
	for k, v := range extra {
		row[k] = v
	}
	return row
}

// readArchive reads the archive of run id's ended attempt n, and removes it when
// the test ends, since forget leaves a row's subcollection behind.
func readArchive(t *testing.T, client *firestore.Client, id string, n string) (map[string]any, bool) {
	t.Helper()
	ref := client.Collection(runsCollection).Doc(id).Collection(attemptsCollection).Doc(n)
	t.Cleanup(func() { _, _ = ref.Delete(context.Background()) })
	snap, err := ref.Get(context.Background())
	if err != nil {
		return nil, false
	}
	return snap.Data(), true
}

func retryReq(extra func(*RetryRequest)) RetryRequest {
	req := RetryRequest{Note: "Keep the old flag name.", Actor: "owner@example.test", ExpectedAttempt: 2, SupersedePR: 141}
	if extra != nil {
		extra(&req)
	}
	return req
}

func TestRetryWithNoteQueuesTheRunForANewAttemptWithTheNote(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("retry-queue")
	seedRow(t, client, id, retryRow(map[string]any{waitingOnField: dispatcher.ConditionRepoBusy, transientField: "x"}))
	at := queueAt.Add(time.Hour)
	got, err := q.RetryWithNote(ctx, id, retryReq(nil), at)
	if err != nil {
		t.Fatal(err)
	}
	if got != (RetryResult{Attempt: 3}) {
		t.Fatalf("result = %+v, want attempt 3, not already applied", got)
	}
	data := readRow(t, client, id)
	if data[stateField] != stateQueued || data[attemptField] != int64(3) {
		t.Fatalf("state %v, attempt %v, want queued as attempt 3: a note can widen the write set, so the plan is made again", data[stateField], data[attemptField])
	}
	if data[operatorNoteField] != "Keep the old flag name." || data[operatorAssistedField] != true || data[retriedByField] != "owner@example.test" {
		t.Fatalf("note %v, assisted %v, retried_by %v, want the note word for word and who sent it", data[operatorNoteField], data[operatorAssistedField], data[retriedByField])
	}
	if data[supersededAttemptField] != int64(2) {
		t.Fatalf("superseded_attempt = %v, want the ended attempt, 2", data[supersededAttemptField])
	}
	for _, field := range []string{outcomeField, stopReasonField, claimedAtField, waitingOnField, transientField} {
		if v, ok := data[field]; ok {
			t.Errorf("field %s survived the retry: %v", field, v)
		}
	}
	if !listed(t, q, ctx, id) {
		t.Fatalf("retried run %s is not a candidate", id)
	}
}

func TestRetryWithNoteArchivesTheEndedAttemptBeforeItIsOverwritten(t *testing.T) {
	q, client := queue(t)
	id := fresh("retry-archive")
	seedRow(t, client, id, retryRow(nil))
	at := queueAt.Add(time.Hour)
	if _, err := q.RetryWithNote(context.Background(), id, retryReq(nil), at); err != nil {
		t.Fatal(err)
	}
	archive, ok := readArchive(t, client, id, "2")
	if !ok {
		t.Fatal("the ended attempt was not archived at attempts/2")
	}
	if archive[stopReasonField] != string(runner.StopRebaseConflict) || archive[prURLField] != retryPR || archive[attemptField] != int64(2) {
		t.Fatalf("archive = %v, want the ended attempt's stop reason, PR and attempt", archive)
	}
	if archive[supersededByAttemptField] != int64(3) {
		t.Fatalf("superseded_by_attempt = %v, want 3", archive[supersededByAttemptField])
	}
	if archivedAt, _ := archive[archivedAtField].(time.Time); !archivedAt.Equal(at) {
		t.Fatalf("archived_at = %v, want the call's own time", archive[archivedAtField])
	}
	if _, kept := readRow(t, client, id)[stopReasonField]; kept {
		t.Fatal("the row still carries the ended attempt's stop reason")
	}
}

func TestRetryWithNoteArchivesARetrysOwnNote(t *testing.T) {
	q, client := queue(t)
	id := fresh("retry-archive-note")
	seedRow(t, client, id, retryRow(map[string]any{
		operatorNoteField: "The first note.", operatorAssistedField: true, retriedByField: "first@example.test",
	}))
	if _, err := q.RetryWithNote(context.Background(), id, retryReq(nil), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	archive, ok := readArchive(t, client, id, "2")
	if !ok || archive[operatorNoteField] != "The first note." || archive[retriedByField] != "first@example.test" {
		t.Fatalf("archive = %v (found %v), want the attempt's own note and sender", archive, ok)
	}
	if data := readRow(t, client, id); data[operatorNoteField] != "Keep the old flag name." || data[retriedByField] != "owner@example.test" {
		t.Fatalf("row note %v, sender %v, want the new retry's", data[operatorNoteField], data[retriedByField])
	}
}

func TestRetryWithNoteRefusesAndLeavesTheRowAndNoArchive(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		row  map[string]any
		req  func(*RetryRequest)
		want error
	}{
		{"a stale attempt", retryRow(nil), func(r *RetryRequest) { r.ExpectedAttempt = 1 }, ErrAttemptMoved},
		{"waiting", retryRow(map[string]any{stateField: stateWaiting}), nil, ErrRunWaiting},
		{"in flight", retryRow(map[string]any{outcomeField: firestore.Delete, stopReasonField: firestore.Delete}), nil, ErrRunInFlight},
		{"already dispatchable", retryRow(map[string]any{stateField: stateQueued, outcomeField: firestore.Delete, stopReasonField: firestore.Delete}), nil, ErrRunDispatchable},
		{"abandoned", retryRow(map[string]any{stateField: stateAbandoned}), nil, ErrRunAbandoned},
		{"merged by the owner", retryRow(map[string]any{mergedByOwnerAtField: queueAt}), nil, ErrRunMerged},
		{"merged by a poll", retryRow(map[string]any{rewriteMergedAtField: queueAt}), nil, ErrRunMerged},
		{"no changes", retryRow(map[string]any{outcomeField: string(runner.OutcomeNoChanges)}), nil, ErrRunNoChanges},
		{"a blank note", retryRow(nil), func(r *RetryRequest) { r.Note = " \n" }, ErrNoteInvalid},
		{"a note over the limit", retryRow(nil), func(r *RetryRequest) { r.Note = strings.Repeat("a", runner.MaxNoteBytes+1) }, ErrNoteInvalid},
		{"a note with a NUL", retryRow(nil), func(r *RetryRequest) { r.Note = "a\x00b" }, ErrNoteInvalid},
		{"no actor", retryRow(nil), func(r *RetryRequest) { r.Actor = " " }, ErrActorMissing},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := fresh("retry-refuse")
			fields := map[string]any{}
			for k, v := range tt.row {
				if v != firestore.Delete {
					fields[k] = v
				}
			}
			seedRow(t, client, id, fields)
			before := readRow(t, client, id)
			if _, err := q.RetryWithNote(ctx, id, retryReq(tt.req), queueAt.Add(time.Hour)); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if after := readRow(t, client, id); !reflect.DeepEqual(before, after) {
				t.Fatalf("row changed by a refused retry: %v, want %v", after, before)
			}
			if archive, ok := readArchive(t, client, id, "2"); ok {
				t.Fatalf("a refused retry archived the attempt: %v", archive)
			}
		})
	}
}

func TestRetryWithNoteRefusesARunWithNoRow(t *testing.T) {
	q, _ := queue(t)
	if _, err := q.RetryWithNote(context.Background(), fresh("retry-absent"), retryReq(nil), queueAt); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want the store's own not-found", err)
	}
}

func TestRetryWithNoteRepeatedMovesTheRunOnce(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("retry-repeat")
	seedRow(t, client, id, retryRow(nil))
	if _, err := q.RetryWithNote(ctx, id, retryReq(nil), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := readRow(t, client, id)
	got, err := q.RetryWithNote(ctx, id, retryReq(nil), queueAt.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got != (RetryResult{Attempt: 3, Already: true}) {
		t.Fatalf("repeat = %+v, want attempt 3 reported as already applied", got)
	}
	if after := readRow(t, client, id); !reflect.DeepEqual(before, after) {
		t.Fatalf("the repeat changed the row: %v, want %v", after, before)
	}
	// The same attempt retried with another note, or by another owner, is a new request.
	for name, edit := range map[string]func(*RetryRequest){
		"another note":  func(r *RetryRequest) { r.Note = "A different note." },
		"another owner": func(r *RetryRequest) { r.Actor = "other@example.test" },
	} {
		if _, err := q.RetryWithNote(ctx, id, retryReq(edit), queueAt.Add(3*time.Hour)); !errors.Is(err, ErrAttemptMoved) {
			t.Errorf("%s: err = %v, want ErrAttemptMoved: the run has moved on", name, err)
		}
	}
}

func TestRetryWithNoteRecordsTheSupersededPRAndCarriesSpendForward(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	draft := fresh("retry-draft")
	seedRow(t, client, draft, retryRow(nil))
	if _, err := q.RetryWithNote(ctx, draft, retryReq(nil), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	data := readRow(t, client, draft)
	if data[supersededPRField] != int64(141) {
		t.Fatalf("superseded_pr = %v, want 141", data[supersededPRField])
	}
	if data[priorSettledCostField] != int64(250_000) || data[priorSettledMinutesField] != int64(4) {
		t.Fatalf("prior settled = %v micros, %v minutes, want the ended attempt's 250000 and 4", data[priorSettledCostField], data[priorSettledMinutesField])
	}
	if n, err := q.SupersededPR(ctx, draft); err != nil || n != 141 {
		t.Fatalf("SupersededPR = %d, %v, want the number pr-meta reads", n, err)
	}

	// A run that opened no PR has none to replace, and a number an earlier retry
	// left on the row does not carry over.
	none := fresh("retry-none")
	seedRow(t, client, none, retryRow(map[string]any{prURLField: "", supersededPRField: int64(7)}))
	if _, err := q.RetryWithNote(ctx, none, retryReq(func(r *RetryRequest) { r.SupersedePR = 0 }), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if v, ok := readRow(t, client, none)[supersededPRField]; ok {
		t.Fatalf("superseded_pr = %v, want none recorded for a run with no PR", v)
	}
}

func TestOperatorNoteReadsWhatTheRetryWrote(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("retry-read")
	seedRow(t, client, id, retryRow(nil))
	if note, err := q.OperatorNote(ctx, id); err != nil || note != "" {
		t.Fatalf("note before a retry = %q, %v, want none", note, err)
	}
	if _, err := q.RetryWithNote(ctx, id, retryReq(nil), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if note, err := q.OperatorNote(ctx, id); err != nil || note != "Keep the old flag name." {
		t.Fatalf("note = %q, %v, want the owner's", note, err)
	}
	if _, err := q.OperatorNote(ctx, fresh("retry-read-absent")); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want ErrRecordNotFound", err)
	}
}

// The PR a retry replaced keeps its wording for as long as the row names it: the
// note is dropped by the next requeue, the number and the reason it was replaced are not.
func TestSupersededByRetryLivesAsLongAsTheSupersededPR(t *testing.T) {
	q, client := queue(t)
	ctx := context.Background()
	id := fresh("retry-wording")
	seedRow(t, client, id, retryRow(nil))
	if retried, err := q.SupersededByRetry(ctx, id); err != nil || retried {
		t.Fatalf("before a retry = %v, %v, want false", retried, err)
	}
	if _, err := q.RetryWithNote(ctx, id, retryReq(nil), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collection(runsCollection).Doc(id).Update(ctx, []firestore.Update{
		{Path: outcomeField, Value: string(runner.OutcomeStopped)}, {Path: stopReasonField, Value: string(runner.StopRebaseConflict)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Requeue(ctx, id, queueAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if note, _ := q.OperatorNote(ctx, id); note != "" {
		t.Fatalf("note = %q after the owner's requeue, want it dropped", note)
	}
	pr, _ := q.SupersededPR(ctx, id)
	if retried, err := q.SupersededByRetry(ctx, id); err != nil || pr != 141 || !retried {
		t.Fatalf("superseded PR %d, by retry %v, %v after the owner's requeue, want 141 still worded as the retry's", pr, retried, err)
	}

	// A retry that replaces no PR leaves neither the number nor its reason behind.
	none := fresh("retry-wording-none")
	seedRow(t, client, none, retryRow(map[string]any{prURLField: "", supersededPRField: int64(7), supersededByRetryField: true}))
	if _, err := q.RetryWithNote(ctx, none, retryReq(func(r *RetryRequest) { r.SupersedePR = 0 }), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if retried, err := q.SupersededByRetry(ctx, none); err != nil || retried {
		t.Fatalf("by retry = %v, %v, want false once no PR is replaced", retried, err)
	}
	if _, err := q.SupersededByRetry(ctx, fresh("retry-wording-absent")); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("err = %v, want ErrRecordNotFound", err)
	}
}
