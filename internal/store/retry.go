package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// The fields a retry records on the run row, outside runner.Record so Finalize
// leaves them: the owner's note for the attempt, that the attempt carries one,
// and who retried it. A requeue that is not the retry or an automatic one drops
// all three.
const (
	operatorNoteField     = "operator_note"
	operatorAssistedField = "operator_assisted"
	retriedByField        = "retried_by"
	// archivedAtField and supersededByAttemptField mark an archived attempt: when it
	// was copied and the attempt that replaced it.
	archivedAtField          = "archived_at"
	supersededByAttemptField = "superseded_by_attempt"
	// attemptsCollection holds a run's ended attempts, one document per attempt
	// numbered as the row's attempt field is, from 0.
	attemptsCollection = "attempts"
)

// The reasons RetryWithNote refuses, beside the ones Requeue has.
var (
	// ErrAttemptMoved reports a run that is no longer on the attempt the owner saw.
	ErrAttemptMoved = errors.New("run is no longer on the attempt it was retried from")
	// ErrRunMerged reports a run whose PR is merged, which a retry cannot replace.
	ErrRunMerged = errors.New("run's PR is already merged")
	// ErrRunNoChanges reports a run that found nothing to change, which a note would not change.
	ErrRunNoChanges = errors.New("run made no changes")
	// ErrNoteInvalid reports a note that is blank, too long, not UTF-8 or holding a NUL.
	ErrNoteInvalid = errors.New("the note is blank or cannot be carried in a ticket")
	// ErrActorMissing reports a retry made with no signed-in owner to record.
	ErrActorMissing = errors.New("a retry records who made it")

	errRetryApplied = errors.New("the retry was already applied")
)

// clearNote deletes the note fields, for every requeue that is not a retry or an
// automatic one.
func clearNote() []firestore.Update {
	return []firestore.Update{
		{Path: operatorNoteField, Value: firestore.Delete},
		{Path: operatorAssistedField, Value: firestore.Delete},
		{Path: retriedByField, Value: firestore.Delete},
	}
}

// archiveRef is the document the ended attempt n of the run at rowRef is archived to.
func archiveRef(rowRef *firestore.DocumentRef, n int) *firestore.DocumentRef {
	return rowRef.Collection(attemptsCollection).Doc(fmt.Sprint(n))
}

// archivedAttempt is the row as the ended attempt left it, marked with the
// attempt that replaces it and when it was archived.
func archivedAttempt(row map[string]any, next int, at time.Time) map[string]any {
	archive := maps.Clone(row)
	archive[supersededByAttemptField] = next
	archive[archivedAtField] = at
	return archive
}

// RetryRequest is an owner's retry of a stopped run. ExpectedAttempt is the
// attempt the owner saw, so a retry of a run that has moved on is refused, and
// SupersedePR the number of the open draft PR the new attempt replaces, zero
// when the ended attempt opened none.
type RetryRequest struct {
	Note            string
	Actor           string
	ExpectedAttempt int
	SupersedePR     int
}

// RetryResult is the attempt a retry put the run back as, and whether the
// request had already been applied.
type RetryResult struct {
	Attempt int
	Already bool
}

// RetryWithNote returns a run that ended, or whose PR is a draft, to the queue as
// its next attempt for the owner's note. The attempt that ended is archived
// first, in the same transaction, and the new one is queued rather than planned,
// so its plan is made again with the note in view.
//
// It refuses what Requeue refuses, and an abandoned run, a merged one, one that
// made no changes and one that has moved off req.ExpectedAttempt. Repeating a
// retry already applied (same attempt, note and actor) reports it as such, so a
// request the owner's client sent twice moves the run once.
func (q *Queue) RetryWithNote(ctx context.Context, runID string, req RetryRequest, at time.Time) (RetryResult, error) {
	if strings.TrimSpace(req.Note) == "" || !runner.ValidNote(req.Note) {
		return RetryResult{}, ErrNoteInvalid
	}
	if strings.TrimSpace(req.Actor) == "" {
		return RetryResult{}, ErrActorMissing
	}
	res, err := q.requeue(ctx, runID, at, requeueOptions{keepNote: true, archive: true},
		func(row map[string]any, attempt int) (string, []firestore.Update, error) {
			if rowAttempt(row) == req.ExpectedAttempt+1 && str(row, operatorNoteField) == req.Note && str(row, retriedByField) == req.Actor {
				return "", nil, errRetryApplied
			}
			switch {
			case rowAttempt(row) != req.ExpectedAttempt:
				return "", nil, ErrAttemptMoved
			case mergedRow(row):
				return "", nil, ErrRunMerged
			case str(row, outcomeField) == string(runner.OutcomeNoChanges):
				return "", nil, ErrRunNoChanges
			case str(row, stateField) == stateAbandoned:
				return "", nil, ErrRunAbandoned
			}
			if _, err := requeueState(row); err != nil {
				return "", nil, err
			}
			var pr any = firestore.Delete
			if req.SupersedePR > 0 {
				pr = req.SupersedePR
			}
			cost, _ := row[settledProviderCostField].(int64)
			minutes, _ := row[settledRunnerMinutesField].(int64)
			return stateQueued, []firestore.Update{
				{Path: operatorNoteField, Value: req.Note},
				{Path: operatorAssistedField, Value: true},
				{Path: retriedByField, Value: req.Actor},
				{Path: supersededAttemptField, Value: attempt - 1},
				{Path: supersededPRField, Value: pr},
				{Path: priorSettledCostField, Value: cost},
				{Path: priorSettledMinutesField, Value: minutes},
			}, nil
		})
	if errors.Is(err, errRetryApplied) {
		return RetryResult{Attempt: req.ExpectedAttempt + 1, Already: true}, nil
	}
	if err != nil {
		return RetryResult{}, fmt.Errorf("retrying run %s with a note: %w", runID, err)
	}
	return RetryResult{Attempt: res.Attempt}, nil
}

// OperatorNote reads the owner's note for runID's current attempt, empty when it
// carries none.
func (q *Queue) OperatorNote(ctx context.Context, runID string) (string, error) {
	row, err := q.row(ctx, runID)
	if err != nil {
		return "", err
	}
	return str(row, operatorNoteField), nil
}

// OperatorAssisted reports whether runID's current attempt is an owner's retry,
// which words the PR it opens.
func (q *Queue) OperatorAssisted(ctx context.Context, runID string) (bool, error) {
	row, err := q.row(ctx, runID)
	if err != nil {
		return false, err
	}
	assisted, _ := row[operatorAssistedField].(bool)
	return assisted, nil
}

// row reads runID's whole row.
func (q *Queue) row(ctx context.Context, runID string) (map[string]any, error) {
	snap, err := q.client.Collection(runsCollection).Doc(runID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, fmt.Errorf("%s: %w", runID, runner.ErrRecordNotFound)
	}
	if err != nil {
		return nil, err
	}
	return snap.Data(), nil
}
