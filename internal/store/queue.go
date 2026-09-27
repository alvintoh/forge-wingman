package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// dispatchCollection holds the bookkeeping the dispatcher owns: one
	// rejected-<ticket id> doc per ticket the queue would not admit, which a
	// later refusal of the same ticket replaces.
	dispatchCollection = "dispatch"
	rejectedPrefix     = "rejected-"

	// The queue's own fields, kept beside the run record rather than inside it:
	// the runner rewrites the fields a Record names and leaves these alone, so a
	// claim cannot be dropped by the run that finalises the record.
	stateField     = "state"
	priorityField  = "linear_priority"
	repoField      = "repo"
	claimedAtField = "claimed_at"
	updatedAtField = "updated_at"
)

// The states a queued run holds. A run leaves queued once, and comes back to it
// when the dispatch that claimed it never reached GitHub.
const (
	stateQueued  = "queued"
	stateClaimed = "claimed"
)

// Queue admits runs to the store's queue and claims them off it.
type Queue struct {
	client *firestore.Client
}

// NewQueue returns a Queue backed by client.
func NewQueue(client *firestore.Client) *Queue {
	return &Queue{client: client}
}

// Enqueue writes the run record for a delegated ticket, queued, and clears any
// refusal an earlier poll recorded against the same ticket. It fails with
// dispatcher.ErrAlreadyQueued when the record exists, so neither a repeated
// poll nor the webhook's own row can start a second run of one ticket.
func (q *Queue) Enqueue(ctx context.Context, run dispatcher.Queued) error {
	rec := runner.NewRecord(run.RunID, run.Ticket, run.At)
	rec.UpdatedAt = run.At
	data := fields(rec)
	data[stateField] = stateQueued
	data[priorityField] = run.Priority
	data[repoField] = run.Repo
	rejected := q.rejected(run.RunID)
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		// Read before writing: a transaction may not read after it writes, and
		// a ticket refused by no earlier poll is not an error to report.
		if _, err := tx.Get(rejected); err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if err := tx.Create(q.client.Collection(runsCollection).Doc(run.RunID), data); err != nil {
			return err
		}
		if err := tx.Delete(rejected); err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		return nil
	})
	if status.Code(err) == codes.AlreadyExists {
		return fmt.Errorf("queuing run %s: %w", run.RunID, dispatcher.ErrAlreadyQueued)
	}
	if err != nil {
		return fmt.Errorf("queuing run %s: %w", run.RunID, err)
	}
	return nil
}

// Claim takes the highest-priority queued run, in one transaction over the row
// the ordered query found, so that of two polls that read the same run only one
// is given it. It reports false when the queue is empty, and when another poll
// won the row this one read.
func (q *Queue) Claim(ctx context.Context, at time.Time) (dispatcher.Claim, bool, error) {
	iter := q.client.Collection(runsCollection).
		Where(stateField, "==", stateQueued).
		OrderBy(priorityField, firestore.Asc).
		Limit(1).
		Documents(ctx)
	defer iter.Stop()
	snap, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return dispatcher.Claim{}, false, nil
	}
	if err != nil {
		return dispatcher.Claim{}, false, fmt.Errorf("reading the queue: %w", err)
	}
	var claim dispatcher.Claim
	err = q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		claim = dispatcher.Claim{}
		current, err := tx.Get(snap.Ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if state, _ := current.Data()[stateField].(string); state != stateQueued {
			return nil
		}
		repo, _ := current.Data()[repoField].(string)
		priority, _ := current.Data()[priorityField].(int64)
		claim = dispatcher.Claim{RunID: snap.Ref.ID, Repo: repo, Priority: int(priority)}
		return tx.Update(snap.Ref, []firestore.Update{
			{Path: stateField, Value: stateClaimed},
			{Path: claimedAtField, Value: at},
			{Path: updatedAtField, Value: at},
		})
	})
	if err != nil {
		return dispatcher.Claim{}, false, fmt.Errorf("claiming run %s: %w", snap.Ref.ID, err)
	}
	return claim, claim.RunID != "", nil
}

// Release returns a claimed run to the queue, for the dispatch that never
// reached GitHub: the next poll then considers it again, at the priority it was
// queued with.
func (q *Queue) Release(ctx context.Context, runID string, at time.Time) error {
	_, err := q.client.Collection(runsCollection).Doc(runID).Update(ctx, []firestore.Update{
		{Path: stateField, Value: stateQueued},
		{Path: updatedAtField, Value: at},
		{Path: claimedAtField, Value: firestore.Delete},
	})
	if err != nil {
		return fmt.Errorf("releasing run %s: %w", runID, err)
	}
	return nil
}

// Reject records why the queue would not admit a ticket, replacing any earlier
// refusal of the same ticket.
func (q *Queue) Reject(ctx context.Context, r dispatcher.Rejection) error {
	doc := q.client.Collection(dispatchCollection).Doc(rejectedPrefix + r.Ticket)
	if _, err := doc.Set(ctx, map[string]any{
		"reason": string(r.Reason),
		"detail": r.Detail,
		"at":     r.At,
	}); err != nil {
		return fmt.Errorf("recording %s for %s: %w", r.Reason, r.Ticket, err)
	}
	return nil
}

// rejected is the document one ticket's refusal is kept in.
func (q *Queue) rejected(ticketID string) *firestore.DocumentRef {
	return q.client.Collection(dispatchCollection).Doc(rejectedPrefix + ticketID)
}
