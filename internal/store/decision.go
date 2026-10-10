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

// sessionBindingPrefix keys the issue → agent-session binding beside the
// webhook markers: dispatch/session-<ticket id>.
const sessionBindingPrefix = "session-"

// waitingScanLimit bounds how many waiting runs one poll considers, the way
// candidateScanLimit bounds the queue scan.
const waitingScanLimit = 25

// decisionEntry is one decision on a run row: the question the plan raised and
// the dispatcher's own record of asking it and being answered. It is kept
// outside runner.Record, so Finalize's whole-field rebuild leaves it intact.
type decisionEntry struct {
	Decision  runner.Decision `firestore:"decision"`
	SessionID string          `firestore:"session_id"`
	AskedAt   time.Time       `firestore:"asked_at"`
	PostedAt  time.Time       `firestore:"posted_at"`
	Answer    string          `firestore:"answer"`
	Reply     string          `firestore:"reply"`
	RepliedAt time.Time       `firestore:"replied_at"`
}

// decisionsRow is the row fields the decision surface reads: the decisions
// themselves, the state and outcome that decide whether a run still waits, the
// plan spend a suspend accumulates, and the link a notice carries.
type decisionsRow struct {
	Decisions        []decisionEntry `firestore:"decisions"`
	State            string          `firestore:"state"`
	Outcome          string          `firestore:"outcome"`
	RunURL           string          `firestore:"run_url"`
	PlanSpendMicros  int64           `firestore:"plan_spend_micros"`
	PlanSpendMinutes int64           `firestore:"plan_spend_minutes"`
}

// lastUnanswered is the index of the decision a run still waits on — the last
// with no answer — or -1 when the run waits on none.
func lastUnanswered(entries []decisionEntry) int {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Answer == "" {
			return i
		}
	}
	return -1
}

// Suspend parks runID on d: it appends the decision, moves the row to waiting,
// clears the claim and the deferral, records the plan's own Actions run, and
// drops the ledger reservation the run holds — the plan stage's own key among
// them — so a waiting run holds neither a runner nor a reservation.
func (q *Queue) Suspend(ctx context.Context, runID string, d runner.Decision, runURL string, at time.Time) error {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(rowRef)
		if err != nil {
			return err
		}
		var row decisionsRow
		if err := snap.DataTo(&row); err != nil {
			return err
		}
		row.Decisions = append(row.Decisions, decisionEntry{Decision: d, AskedAt: at})
		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		updates := []firestore.Update{
			{Path: stateField, Value: stateWaiting},
			{Path: decisionsField, Value: row.Decisions},
			{Path: runURLField, Value: runURL},
			{Path: updatedAtField, Value: at},
			{Path: claimedAtField, Value: firestore.Delete},
			{Path: waitingOnField, Value: firestore.Delete},
		}
		var ledgerUpdates []firestore.Update
		if key, entry, has := ledger.reservation(runID); has {
			spend, ledgerU := planSettle(key, entry, planRow{
				PlanSpendMicros:  row.PlanSpendMicros,
				PlanSpendMinutes: row.PlanSpendMinutes,
			})
			updates = append(updates, spend...)
			// The window the plan's cost falls in is the one its settle stamp
			// lies in; a suspended run never writes the plan via WritePlan, so
			// the stamp is written here.
			updates = append(updates, firestore.Update{Path: planSettledAtField, Value: at})
			ledgerUpdates = ledgerU
		}
		if err := tx.Update(rowRef, updates); err != nil {
			return err
		}
		if len(ledgerUpdates) == 0 {
			return nil
		}
		return tx.Update(ledgerRef, ledgerUpdates)
	})
	if err != nil {
		return fmt.Errorf("suspending run %s: %w", runID, err)
	}
	return nil
}

// Reply records the owner's reply text on runID's last unanswered decision and
// reports whether it recorded one. It records nothing when the run waits on no
// decision, or when the decision's session no longer matches the session the
// reply arrived on — the staleness correlator, so a redelivery and a message on
// an unrelated session are both no-ops.
func (q *Queue) Reply(ctx context.Context, runID, sessionID, text string, at time.Time) (bool, error) {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	var recorded bool
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		recorded = false
		snap, err := tx.Get(rowRef)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		var row decisionsRow
		if err := snap.DataTo(&row); err != nil {
			return err
		}
		i := lastUnanswered(row.Decisions)
		if row.State != stateWaiting || row.Outcome != "" || i < 0 {
			return nil
		}
		if row.Decisions[i].SessionID != sessionID {
			return nil
		}
		row.Decisions[i].Reply = text
		row.Decisions[i].RepliedAt = at
		recorded = true
		return tx.Update(rowRef, []firestore.Update{{Path: decisionsField, Value: row.Decisions}})
	})
	if err != nil {
		return false, fmt.Errorf("recording a reply for run %s: %w", runID, err)
	}
	return recorded, nil
}

// Answer writes the owner's chosen option onto runID's last unanswered decision
// and returns the row to queued, so the next poll claims the run again and its
// plan resumes carrying the answer. It is a no-op when the run waits on no
// decision.
func (q *Queue) Answer(ctx context.Context, runID, answer string, at time.Time) error {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(rowRef)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		var row decisionsRow
		if err := snap.DataTo(&row); err != nil {
			return err
		}
		i := lastUnanswered(row.Decisions)
		if row.State != stateWaiting || row.Outcome != "" || i < 0 {
			return nil
		}
		row.Decisions[i].Answer = answer
		return tx.Update(rowRef, []firestore.Update{
			{Path: decisionsField, Value: row.Decisions},
			{Path: stateField, Value: stateQueued},
			{Path: updatedAtField, Value: at},
			{Path: claimedAtField, Value: firestore.Delete},
			{Path: waitingOnField, Value: firestore.Delete},
		})
	})
	if err != nil {
		return fmt.Errorf("answering run %s: %w", runID, err)
	}
	return nil
}

// Posted stamps the decision as asked: it records the session it was asked in
// and when, which is what makes the post once-only and gives the reply a
// session to correlate against. A run waiting on no decision is a no-op.
func (q *Queue) Posted(ctx context.Context, runID, sessionID string, at time.Time) error {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(rowRef)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		var row decisionsRow
		if err := snap.DataTo(&row); err != nil {
			return err
		}
		i := lastUnanswered(row.Decisions)
		if i < 0 {
			return nil
		}
		row.Decisions[i].SessionID = sessionID
		row.Decisions[i].PostedAt = at
		return tx.Update(rowRef, []firestore.Update{{Path: decisionsField, Value: row.Decisions}})
	})
	if err != nil {
		return fmt.Errorf("marking run %s's decision posted: %w", runID, err)
	}
	return nil
}

// StopWaiting ends runID's wait unanswered: the row records a waiting-on-owner
// stop naming the question and stays waiting, so no poll claims it again. The
// recorded outcome is what keeps it from being reconsidered.
func (q *Queue) StopWaiting(ctx context.Context, runID string, d runner.Decision, at time.Time) error {
	_, err := q.client.Collection(runsCollection).Doc(runID).Update(ctx, []firestore.Update{
		{Path: outcomeField, Value: runner.OutcomeStopped},
		{Path: buildOutcomeField, Value: runner.OutcomeStopped},
		{Path: stopReasonField, Value: runner.StopWaitingOnOwner},
		{Path: stopDetailField, Value: d.Question},
		{Path: updatedAtField, Value: at},
	})
	if err != nil {
		return fmt.Errorf("stopping run %s as waiting-on-owner: %w", runID, err)
	}
	return nil
}

// Waiting lists the runs parked on a decision, as a poll reads them: rows in
// the waiting state with no recorded outcome, each with its last unanswered
// decision. The scan is bounded, and a waiting run whose decision was answered
// or whose run has stopped is skipped.
func (q *Queue) Waiting(ctx context.Context) ([]dispatcher.WaitingRun, error) {
	iter := q.client.Collection(runsCollection).
		Where(stateField, "==", stateWaiting).
		Limit(waitingScanLimit).
		Documents(ctx)
	defer iter.Stop()
	var out []dispatcher.WaitingRun
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing waiting runs: %w", err)
		}
		var row decisionsRow
		if err := snap.DataTo(&row); err != nil {
			return nil, fmt.Errorf("decoding run %s's decisions: %w", snap.Ref.ID, err)
		}
		if row.Outcome != "" {
			continue
		}
		i := lastUnanswered(row.Decisions)
		if i < 0 {
			continue
		}
		e := row.Decisions[i]
		out = append(out, dispatcher.WaitingRun{
			RunID:     snap.Ref.ID,
			Decision:  e.Decision,
			SessionID: e.SessionID,
			AskedAt:   e.AskedAt,
			PostedAt:  e.PostedAt,
			Reply:     e.Reply,
			RepliedAt: e.RepliedAt,
			RunURL:    row.RunURL,
			Index:     i + 1,
		})
	}
	return out, nil
}

// Decisions reads runID's answered decisions, in the order the plan asked them,
// which the ticket job carries into the resumed plan's prompt.
func (q *Queue) Decisions(ctx context.Context, runID string) ([]runner.Answer, error) {
	snap, err := q.client.Collection(runsCollection).Doc(runID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, fmt.Errorf("%s: %w", runID, runner.ErrRecordNotFound)
	}
	if err != nil {
		return nil, err
	}
	var row decisionsRow
	if err := snap.DataTo(&row); err != nil {
		return nil, fmt.Errorf("decoding run %s's decisions: %w", runID, err)
	}
	var out []runner.Answer
	for _, e := range row.Decisions {
		if e.Answer == "" {
			continue
		}
		out = append(out, runner.Answer{Question: e.Decision.Question, Choice: e.Answer})
	}
	return out, nil
}

// Session reads the Linear agent session a ticket's delegation marked, empty
// when the webhook recorded none. It is the binding the dispatcher resolves
// before it asks a decision in a run's session.
func (q *Queue) Session(ctx context.Context, ticketID string) (string, error) {
	snap, err := q.client.Collection(dispatchCollection).Doc(sessionBindingPrefix + ticketID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the session for %s: %w", ticketID, err)
	}
	session, _ := snap.Data()[sessionIDField].(string)
	return session, nil
}
