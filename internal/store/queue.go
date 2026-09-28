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
	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// dispatchCollection holds the bookkeeping the dispatcher owns: one
	// rejected-<ticket id> doc per ticket the queue would not admit, which a
	// later refusal of the same ticket replaces, and the single ledger
	// document (adr/0003).
	dispatchCollection = "dispatch"
	rejectedPrefix     = "rejected-"
	// ledgerDocID is the dispatch/ledger document adr/0003 describes: the
	// in-flight reservation of every claimed-but-not-yet-settled run.
	ledgerDocID = "ledger"

	// The queue's own fields, kept beside the run record rather than inside it:
	// the runner rewrites the fields a Record names and leaves these alone, so a
	// claim cannot be dropped by the run that finalises the record.
	stateField     = "state"
	priorityField  = "linear_priority"
	repoField      = "repo"
	claimedAtField = "claimed_at"
	updatedAtField = "updated_at"

	// sizeField and privateField are Record's own fields (runner.Record),
	// read here to build a Candidate without a second query.
	sizeField    = "size"
	privateField = "private"
	// settledAtField, settledProviderCostField and settledRunnerMinutesField
	// are Record's own settlement fields, written once by runner.Finalize and
	// summed here for FR-22's window checks.
	settledAtField            = "settled_at"
	settledProviderCostField  = "settled_provider_cost_micros"
	settledRunnerMinutesField = "settled_runner_minutes"
	// candidateScanLimit bounds how many queued runs one Candidates call
	// considers, so a long backlog of deferred tickets costs one bounded scan
	// rather than an unbounded one.
	candidateScanLimit = 25
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

// Exists reports whether a run record already exists for runID.
func (q *Queue) Exists(ctx context.Context, runID string) (bool, error) {
	_, err := q.client.Collection(runsCollection).Doc(runID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking whether run %s exists: %w", runID, err)
	}
	return true, nil
}

// Enqueue writes the run record for a delegated ticket, queued, and clears any
// refusal an earlier poll recorded against the same ticket. It fails with
// dispatcher.ErrAlreadyQueued when the record exists, so neither a repeated
// poll nor the webhook's own row can start a second run of one ticket.
func (q *Queue) Enqueue(ctx context.Context, run dispatcher.Queued) error {
	rec := runner.NewRecord(run.RunID, run.Ticket, run.At)
	rec.Private = run.Private
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

// Candidates lists queued runs in Linear priority order, for admission to
// walk (adr/0003). Bounded by candidateScanLimit, so a long backlog of
// deferred tickets costs one scan rather than an unbounded one.
func (q *Queue) Candidates(ctx context.Context) ([]dispatcher.Candidate, error) {
	iter := q.client.Collection(runsCollection).
		Where(stateField, "==", stateQueued).
		OrderBy(priorityField, firestore.Asc).
		Limit(candidateScanLimit).
		Documents(ctx)
	defer iter.Stop()
	var out []dispatcher.Candidate
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing queued runs: %w", err)
		}
		data := snap.Data()
		repo, _ := data[repoField].(string)
		size, _ := data[sizeField].(string)
		// A record from before this field existed has no privateField at
		// all — default that to true (private), the safe direction: it
		// counts the run's runner minutes toward NFR-1's ceiling rather than
		// silently zeroing them for a target that may in fact be private.
		private := true
		if v, ok := data[privateField]; ok {
			private, _ = v.(bool)
		}
		priority, _ := data[priorityField].(int64)
		out = append(out, dispatcher.Candidate{
			RunID: snap.Ref.ID, Repo: repo, Size: size, Private: private, Priority: int(priority),
		})
	}
	return out, nil
}

// ledgerDoc is the dispatch/ledger document's own shape (adr/0003): the
// reservation every claimed-but-not-yet-settled run holds, keyed by run id. A
// future concurrency limit (FR-27's discovered N) has room to live here
// beside Reservations without changing this shape.
type ledgerDoc struct {
	Reservations map[string]reservationEntry `firestore:"reservations"`
}

type reservationEntry struct {
	ProviderCostMicros money.Micros `firestore:"provider_cost_micros"`
	RunnerMinutes      int64        `firestore:"runner_minutes"`
}

func (q *Queue) ledgerRef() *firestore.DocumentRef {
	return q.client.Collection(dispatchCollection).Doc(ledgerDocID)
}

// reservedTotals sums every in-flight reservation the ledger doc holds.
func reservedTotals(doc ledgerDoc) dispatcher.Totals {
	var t dispatcher.Totals
	for _, r := range doc.Reservations {
		t.ProviderCost += r.ProviderCostMicros
		t.RunnerMinutes += r.RunnerMinutes
	}
	return t
}

// TryClaim attempts to admit run runID: in one transaction, it re-reads the
// row (must still be queued) and the ledger, sums the ledger's in-flight
// reservations and the settled cost of runs that ended inside each of cfg's
// windows, and commits — claiming the row and adding res to the ledger —
// only if the result fits every ceiling (adr/0003, dispatcher.Decide). It
// reports (false, "", nil) when another poll already claimed the row, and
// (false, <ceiling>, nil) when the budget itself is why it was not claimed.
func (q *Queue) TryClaim(ctx context.Context, runID string, at time.Time, cfg dispatcher.BudgetConfig, res dispatcher.Reservation) (bool, string, error) {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	var binding string
	var claimed bool
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		binding, claimed = "", false
		row, err := tx.Get(rowRef)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if state, _ := row.Data()[stateField].(string); state != stateQueued {
			return nil
		}

		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		reserved := reservedTotals(ledger)

		windowSettled := make([]money.Micros, len(cfg.ProviderWindows))
		for i, w := range cfg.ProviderWindows {
			sum, _, err := q.settledSince(tx, w.Since(at))
			if err != nil {
				return err
			}
			windowSettled[i] = sum
		}
		cashSettledCost, cashSettledMinutes, err := q.settledSince(tx, cfg.Cash.Since(at))
		if err != nil {
			return err
		}

		fits, why := dispatcher.Decide(cfg, reserved, windowSettled, cashSettledCost, cashSettledMinutes, res)
		if !fits {
			binding = why
			return nil
		}

		if ledger.Reservations == nil {
			ledger.Reservations = map[string]reservationEntry{}
		}
		ledger.Reservations[runID] = reservationEntry{
			ProviderCostMicros: res.ProviderCost,
			RunnerMinutes:      res.RunnerMinutes,
		}
		if err := tx.Set(ledgerRef, ledger); err != nil {
			return err
		}
		if err := tx.Update(rowRef, []firestore.Update{
			{Path: stateField, Value: stateClaimed},
			{Path: claimedAtField, Value: at},
			{Path: updatedAtField, Value: at},
		}); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil {
		return false, "", fmt.Errorf("claiming run %s: %w", runID, err)
	}
	return claimed, binding, nil
}

// readLedger reads the ledger document within tx, reporting an empty ledger
// when none has been written yet.
func (q *Queue) readLedger(tx *firestore.Transaction, ledgerRef *firestore.DocumentRef) (ledgerDoc, error) {
	snap, err := tx.Get(ledgerRef)
	if status.Code(err) == codes.NotFound {
		return ledgerDoc{Reservations: map[string]reservationEntry{}}, nil
	}
	if err != nil {
		return ledgerDoc{}, err
	}
	var ledger ledgerDoc
	if err := snap.DataTo(&ledger); err != nil {
		return ledgerDoc{}, fmt.Errorf("decoding the ledger: %w", err)
	}
	if ledger.Reservations == nil {
		ledger.Reservations = map[string]reservationEntry{}
	}
	return ledger, nil
}

// settledSince sums settled_provider_cost_micros and settled_runner_minutes
// over every run whose settlement fell at or after since, within tx — one
// scan shared by every window's admission check, so the provider-window and
// cash queries cannot silently diverge from each other.
//
// This re-scans the full window on every call, and TryClaim calls it once per
// candidate walked in one poll (up to candidateScanLimit). Accepted at this
// project's personal-scale volume, same as Estimates' query (internal/store/
// estimate.go) — revisit both together if either the run history or the
// candidate backlog grows enough to matter.
func (q *Queue) settledSince(tx *firestore.Transaction, since time.Time) (cost money.Micros, minutes int64, err error) {
	iter := tx.Documents(q.client.Collection(runsCollection).Where(settledAtField, ">=", since))
	defer iter.Stop()
	var costSum, minutesSum int64
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("summing settled totals since %s: %w", since, err)
		}
		data := snap.Data()
		c, _ := data[settledProviderCostField].(int64)
		m, _ := data[settledRunnerMinutesField].(int64)
		costSum += c
		minutesSum += m
	}
	return money.Micros(costSum), minutesSum, nil
}

// hasReservation reports whether the ledger currently holds a reservation for
// runID, read within tx so the check is consistent with whatever else the
// transaction reads. Firestore requires every read in a transaction to
// happen before any write, so callers must read this before writing anything.
func hasReservation(tx *firestore.Transaction, ledgerRef *firestore.DocumentRef, runID string) (bool, error) {
	snap, err := tx.Get(ledgerRef)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var ledger ledgerDoc
	if err := snap.DataTo(&ledger); err != nil {
		return false, fmt.Errorf("decoding the ledger: %w", err)
	}
	_, ok := ledger.Reservations[runID]
	return ok, nil
}

// clearReservation drops runID's reservation from the ledger, within a
// transaction that has already confirmed (via hasReservation) that it holds one.
func clearReservation(tx *firestore.Transaction, ledgerRef *firestore.DocumentRef, runID string) error {
	return tx.Update(ledgerRef, []firestore.Update{
		{FieldPath: firestore.FieldPath{"reservations", runID}, Value: firestore.Delete},
	})
}

// Release returns a claimed run to the queue and drops its ledger
// reservation, for the dispatch that never reached GitHub: the next poll then
// considers it again, at the priority it was queued with, with no stale
// reservation counted against it.
func (q *Queue) Release(ctx context.Context, runID string, at time.Time) error {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		has, err := hasReservation(tx, ledgerRef, runID)
		if err != nil {
			return err
		}
		if err := tx.Update(rowRef, []firestore.Update{
			{Path: stateField, Value: stateQueued},
			{Path: updatedAtField, Value: at},
			{Path: claimedAtField, Value: firestore.Delete},
		}); err != nil {
			return err
		}
		if has {
			return clearReservation(tx, ledgerRef, runID)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("releasing run %s: %w", runID, err)
	}
	return nil
}

// Settle removes runID's reservation from the ledger, once its record carries
// its actual cost (runner.Finalize). It is a no-op when the run holds no
// reservation, so a run whose record job runs more than once settles exactly
// once (adr/0003).
func (q *Queue) Settle(ctx context.Context, runID string) error {
	ledgerRef := q.ledgerRef()
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		has, err := hasReservation(tx, ledgerRef, runID)
		if err != nil {
			return err
		}
		if !has {
			return nil
		}
		return clearReservation(tx, ledgerRef, runID)
	})
	if err != nil {
		return fmt.Errorf("settling run %s: %w", runID, err)
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
