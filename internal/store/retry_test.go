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
	seedRow(t, client, id, retryRow(map[string]any{waitingOnField: dispatcher.ConditionRepoOverlap, transientField: "x"}))
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
		{"cancelled", retryRow(map[string]any{cancelledByOwnerAtField: queueAt, cancelledByField: "owner@example.test"}), nil, ErrRunCancelled},
		{"a stopped run cancelled", retryRow(map[string]any{stateField: stateStopped, outcomeField: string(runner.OutcomeAgentFailed), prURLField: firestore.Delete, cancelledByOwnerAtField: queueAt, cancelledByField: "owner@example.test"}), nil, ErrRunCancelled},
		{"merged by the owner after a cancel", retryRow(map[string]any{cancelledByOwnerAtField: queueAt, mergedByOwnerAtField: queueAt}), nil, ErrRunMerged},
		{"merged by a poll after a cancel", retryRow(map[string]any{cancelledByOwnerAtField: queueAt, rewriteMergedAtField: queueAt}), nil, ErrRunMerged},
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

func TestGetAttemptReadsTheAttemptARetryArchived(t *testing.T) {
	q, client := queue(t)
	id := fresh("retry-get-attempt")
	row := retryRow(map[string]any{
		outcomeField:    string(runner.OutcomeAgentFailed),
		stopReasonField: string(runner.StopAgentExit),
	})
	delete(row, prURLField)
	seedRow(t, client, id, row)
	if _, err := q.RetryWithNote(context.Background(), id, retryReq(func(r *RetryRequest) { r.SupersedePR = 0 }), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = archiveRef(client.Collection(runsCollection).Doc(id), 2).Delete(context.Background()) })
	records := NewRecords(client)
	got, err := records.GetAttempt(context.Background(), id, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != runner.OutcomeAgentFailed || got.StopReason != runner.StopAgentExit {
		t.Fatalf("attempt 2 = %s/%s, want the ended attempt's agent-failed/agent-exit", got.Outcome, got.StopReason)
	}
	if _, err := records.GetAttempt(context.Background(), id, 1); !errors.Is(err, runner.ErrRecordNotFound) {
		t.Fatalf("attempt 1 err = %v, want ErrRecordNotFound for an attempt never archived", err)
	}
}

// retried seeds retryRow and retries it, so id holds the owner's retry waiting
// to be claimed, its plan files kept.
func retried(t *testing.T, q *Queue, client *firestore.Client, id string) {
	t.Helper()
	seedRow(t, client, id, retryRow(nil))
	if _, err := q.RetryWithNote(context.Background(), id, retryReq(nil), queueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = archiveRef(client.Collection(runsCollection).Doc(id), 2).Delete(context.Background()) })
}

// candidate is id as the poll's Candidates lists it.
func candidate(t *testing.T, q *Queue, id string) dispatcher.Candidate {
	t.Helper()
	cands, err := q.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.RunID == id {
			return c
		}
	}
	t.Fatalf("%s is not a candidate", id)
	return dispatcher.Candidate{}
}

func TestRefreshRetriedModelsCarriesANewLabelIntoTheClaim(t *testing.T) {
	const mimo = "command-code/xiaomi/mimo-v2.6-pro"
	for name, state := range map[string]string{"queued with its plan files": stateQueued, "planned": statePlanned} {
		t.Run(name, func(t *testing.T) {
			q, client := queue(t)
			id := fresh("retry-models-" + state)
			retried(t, q, client, id)
			if _, err := client.Collection(runsCollection).Doc(id).Update(context.Background(),
				[]firestore.Update{{Path: stateField, Value: state}}); err != nil {
				t.Fatal(err)
			}
			awaiting, err := q.RetryAwaitingClaim(context.Background(), id)
			if err != nil || !awaiting {
				t.Fatalf("awaiting = %v (err %v), want the retry waiting to be claimed", awaiting, err)
			}
			changed, err := q.RefreshRetriedModels(context.Background(), id, runner.ModelLabels{Build: mimo})
			if err != nil || !changed {
				t.Fatalf("changed = %v (err %v), want the new label stored", changed, err)
			}
			if c := candidate(t, q, id); c.Model != mimo || !c.ModelsNamed {
				t.Fatalf("candidate = %+v, want it to claim on %s", c, mimo)
			}
			row := readRow(t, client, id)
			if row[operatorNoteField] != "Keep the old flag name." || row[attemptField] != int64(3) {
				t.Fatalf("row = %v, want the retry's note and attempt kept", row)
			}
			if again, _ := q.RefreshRetriedModels(context.Background(), id, runner.ModelLabels{Build: mimo}); again {
				t.Fatal("an unchanged label was written again")
			}
			for _, m := range []runner.ModelLabels{{Build: mimo, Review: "command-code/r"}, {Build: mimo, Review: "command-code/r", Plan: []string{"command-code/p"}}} {
				if changed, err := q.RefreshRetriedModels(context.Background(), id, m); err != nil || !changed {
					t.Fatalf("changed = %v (err %v) for %+v, want a new review or plan label stored", changed, err, m)
				}
			}
		})
	}
}

func TestRefreshRetriedModelsLeavesAnUnlabelledRetryOnTheDefault(t *testing.T) {
	q, client := queue(t)
	id := fresh("retry-models-none")
	retried(t, q, client, id)
	if changed, err := q.RefreshRetriedModels(context.Background(), id, runner.ModelLabels{}); err != nil || changed {
		t.Fatalf("changed = %v (err %v), want nothing to change", changed, err)
	}
	if c := candidate(t, q, id); c.Model != "" || c.ModelsNamed {
		t.Fatalf("candidate = %+v, want no named model, so the run's default applies", c)
	}
}

func TestStopRetriedEndsTheRetriedAttemptKeepingItsNote(t *testing.T) {
	q, client := queue(t)
	id := fresh("retry-models-stop")
	retried(t, q, client, id)
	stopped, err := q.StopRetried(context.Background(), id, runner.StopModelRefused, "model-not-opted-in: tokens", queueAt.Add(2*time.Hour))
	if err != nil || !stopped {
		t.Fatalf("stopped = %v (err %v), want the attempt ended", stopped, err)
	}
	row := readRow(t, client, id)
	if row[stateField] != stateStopped || row[stopReasonField] != string(runner.StopModelRefused) ||
		row[stopDetailField] != "model-not-opted-in: tokens" || row[operatorNoteField] != "Keep the old flag name." {
		t.Fatalf("row = %v, want it stopped on model-refused with the note kept", row)
	}
	if awaiting, _ := q.RetryAwaitingClaim(context.Background(), id); awaiting {
		t.Fatal("a stopped retry still reads as waiting to be claimed")
	}
}

func TestRetriedWritesLeaveARunThatIsNotARetryAlone(t *testing.T) {
	q, client := queue(t)
	id := fresh("retry-models-other")
	row := retryRow(nil)
	row[stateField] = stateQueued
	delete(row, outcomeField)
	delete(row, stopReasonField)
	seedRow(t, client, id, row)
	ctx := context.Background()
	if awaiting, err := q.RetryAwaitingClaim(ctx, id); err != nil || awaiting {
		t.Fatalf("awaiting = %v (err %v), want a queued run no one retried left out", awaiting, err)
	}
	if changed, err := q.RefreshRetriedModels(ctx, id, runner.ModelLabels{Build: "command-code/x"}); err != nil || changed {
		t.Fatalf("changed = %v (err %v), want its models left alone", changed, err)
	}
	if stopped, err := q.StopRetried(ctx, id, runner.StopModelRefused, "x", queueAt); err != nil || stopped {
		t.Fatalf("stopped = %v (err %v), want it left queued", stopped, err)
	}
	if got := readRow(t, client, id); got[stateField] != stateQueued || got[modelLabelsField] != nil {
		t.Fatalf("row = %v, want it untouched", got)
	}
	if awaiting, err := q.RetryAwaitingClaim(ctx, fresh("retry-models-absent")); err != nil || awaiting {
		t.Fatalf("awaiting = %v (err %v), want an absent run left out", awaiting, err)
	}
}
