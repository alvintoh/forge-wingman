package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
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
	// Provisional is set when auto-pilot took the recommended option rather than the owner choosing it.
	Provisional bool `firestore:"provisional"`
	// ConfirmedAt is when the owner confirmed the option auto-pilot took; zero until then.
	ConfirmedAt time.Time `firestore:"confirmed_at"`
}

// provisionalUnpostedField marks a run holding a provisional decision whose
// question has not been posted, so a poll finds it without scanning every run.
const provisionalUnpostedField = "provisional_unposted"

// provisionalRepliedField marks a run whose provisional decision has an owner
// reply not yet handled, so a poll finds it without scanning every run.
const provisionalRepliedField = "provisional_replied"

// The fields a supersede records on the run row, outside runner.Record so
// Finalize leaves them: the PR the superseded attempt opened, and the attempt.
const (
	supersededPRField      = "superseded_pr"
	supersededAttemptField = "superseded_attempt"
)

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
	err := q.settleDecision(ctx, runID, runURL, at, func(row *decisionsRow) []firestore.Update {
		row.Decisions = append(row.Decisions, decisionEntry{Decision: d, AskedAt: at})
		return []firestore.Update{{Path: stateField, Value: stateWaiting}}
	})
	if err != nil {
		return fmt.Errorf("suspending run %s: %w", runID, err)
	}
	return nil
}

// TakeRecommendation settles runID's decision d with its first option, recorded
// as provisional, and returns the row to queued, so the next poll claims the run
// again and its plan resumes carrying the answer. The plan's spend settles and
// its reservation drops as Suspend's do.
func (q *Queue) TakeRecommendation(ctx context.Context, runID string, d runner.Decision, runURL string, at time.Time) error {
	if len(d.Options) == 0 {
		return fmt.Errorf("taking the recommendation for run %s: the decision has no options", runID)
	}
	err := q.settleDecision(ctx, runID, runURL, at, func(row *decisionsRow) []firestore.Update {
		row.Decisions = append(row.Decisions, decisionEntry{Decision: d, AskedAt: at, Answer: d.Options[0].Label, Provisional: true})
		return []firestore.Update{
			{Path: stateField, Value: stateQueued},
			{Path: provisionalUnpostedField, Value: true},
		}
	})
	if err != nil {
		return fmt.Errorf("taking the recommendation for run %s: %w", runID, err)
	}
	return nil
}

// settleDecision is the transaction Suspend and TakeRecommendation share: apply
// records the decision and names the state the row moves to, and the row's claim,
// deferral and plan reservation are released with the plan's spend settled.
func (q *Queue) settleDecision(ctx context.Context, runID, runURL string, at time.Time, apply func(*decisionsRow) []firestore.Update) error {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	return q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(rowRef)
		if err != nil {
			return err
		}
		var row decisionsRow
		if err := snap.DataTo(&row); err != nil {
			return err
		}
		moved := apply(&row)
		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		updates := append(moved,
			firestore.Update{Path: decisionsField, Value: row.Decisions},
			firestore.Update{Path: runURLField, Value: runURL},
			firestore.Update{Path: updatedAtField, Value: at},
			firestore.Update{Path: claimedAtField, Value: firestore.Delete},
			firestore.Update{Path: waitingOnField, Value: firestore.Delete},
		)
		var ledgerUpdates []firestore.Update
		if key, entry, has := ledger.reservation(runID, rowAttempt(snap.Data())); has {
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
}

// Reply records the owner's reply text and reports whether it recorded one. On
// a waiting run it lands on the last unanswered decision; on any other run it
// lands on the last decision when auto-pilot took it, it was posted and the
// owner has not confirmed it, and the run is marked so a poll finds the reply.
// It records nothing when the decision's session no longer matches the session
// the reply arrived on — the staleness correlator, so a redelivery and a
// message on an unrelated session are both no-ops.
func (q *Queue) Reply(ctx context.Context, runID, sessionID, text string, at time.Time) (bool, error) {
	// An empty message carries no answer. Recorded, it would read as a reply
	// matching no option, and the run would ask the owner again for nothing.
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
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
		provisional := row.State != stateWaiting
		switch {
		case provisional:
			i = unconfirmedPosted(row.Decisions)
		case row.Outcome != "":
			i = -1
		}
		if i < 0 || row.Decisions[i].SessionID != sessionID {
			return nil
		}
		row.Decisions[i].Reply = text
		row.Decisions[i].RepliedAt = at
		updates := []firestore.Update{{Path: decisionsField, Value: row.Decisions}}
		if provisional {
			updates = append(updates, firestore.Update{Path: provisionalRepliedField, Value: true})
		}
		recorded = true
		return tx.Update(rowRef, updates)
	})
	if err != nil {
		return false, fmt.Errorf("recording a reply for run %s: %w", runID, err)
	}
	return recorded, nil
}

// unconfirmedPosted is the index of the last decision when auto-pilot took it,
// it has been posted and the owner has not confirmed it, or -1.
func unconfirmedPosted(entries []decisionEntry) int {
	i := len(entries) - 1
	if i < 0 || !entries[i].Provisional || entries[i].PostedAt.IsZero() || !entries[i].ConfirmedAt.IsZero() {
		return -1
	}
	return i
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
// stop naming the question and leaves the waiting state, so no poll claims it
// again and it cannot hold a slot in the scan that finds the runs still owed a
// question.
func (q *Queue) StopWaiting(ctx context.Context, runID string, d runner.Decision, at time.Time) error {
	_, err := q.client.Collection(runsCollection).Doc(runID).Update(ctx, []firestore.Update{
		{Path: stateField, Value: stateStopped},
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

// ProvisionalUnposted lists the runs holding a provisional decision whose
// question has not been posted, each with its first such decision. The scan is
// bounded like Waiting's.
func (q *Queue) ProvisionalUnposted(ctx context.Context) ([]dispatcher.ProvisionalRun, error) {
	iter := q.client.Collection(runsCollection).
		Where(provisionalUnpostedField, "==", true).
		Limit(waitingScanLimit).
		Documents(ctx)
	defer iter.Stop()
	var out []dispatcher.ProvisionalRun
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing provisional decisions: %w", err)
		}
		var row decisionsRow
		if err := snap.DataTo(&row); err != nil {
			return nil, fmt.Errorf("decoding run %s's decisions: %w", snap.Ref.ID, err)
		}
		i := firstUnpostedProvisional(row.Decisions)
		if i < 0 || row.Outcome != "" {
			continue
		}
		e := row.Decisions[i]
		out = append(out, dispatcher.ProvisionalRun{RunID: snap.Ref.ID, Decision: e.Decision, Taken: e.Answer, Index: i + 1})
	}
	return out, nil
}

// firstUnpostedProvisional is the index of the first provisional decision not
// yet posted, or -1 when there is none.
func firstUnpostedProvisional(entries []decisionEntry) int {
	return slices.IndexFunc(entries, func(e decisionEntry) bool { return e.Provisional && e.PostedAt.IsZero() })
}

// PostedProvisional stamps runID's first unposted provisional decision as asked
// in sessionID, and clears the run's mark once none remains. A run with none is
// a no-op.
func (q *Queue) PostedProvisional(ctx context.Context, runID, sessionID string, at time.Time) error {
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
		i := firstUnpostedProvisional(row.Decisions)
		if i < 0 {
			return nil
		}
		row.Decisions[i].SessionID = sessionID
		row.Decisions[i].PostedAt = at
		updates := []firestore.Update{{Path: decisionsField, Value: row.Decisions}}
		if firstUnpostedProvisional(row.Decisions) < 0 {
			updates = append(updates, firestore.Update{Path: provisionalUnpostedField, Value: firestore.Delete})
		}
		return tx.Update(rowRef, updates)
	})
	if err != nil {
		return fmt.Errorf("marking run %s's provisional decision posted: %w", runID, err)
	}
	return nil
}

// repliedProvisional is the index of the last provisional decision the owner
// replied to after it was posted and has not confirmed, or -1.
func repliedProvisional(entries []decisionEntry) int {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Provisional && e.ConfirmedAt.IsZero() && e.RepliedAt.After(e.PostedAt) {
			return i
		}
	}
	return -1
}

// ProvisionalReplied lists the runs whose provisional decision the owner has
// replied to and not yet confirmed, each with its reply. The scan is bounded
// like Waiting's; it reads and changes no run's state.
func (q *Queue) ProvisionalReplied(ctx context.Context) ([]dispatcher.ProvisionalRun, error) {
	iter := q.client.Collection(runsCollection).
		Where(provisionalRepliedField, "==", true).
		Limit(waitingScanLimit).
		Documents(ctx)
	defer iter.Stop()
	var out []dispatcher.ProvisionalRun
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing replied provisional decisions: %w", err)
		}
		var row decisionsRow
		if err := snap.DataTo(&row); err != nil {
			return nil, fmt.Errorf("decoding run %s's decisions: %w", snap.Ref.ID, err)
		}
		i := repliedProvisional(row.Decisions)
		if i < 0 {
			continue
		}
		e := row.Decisions[i]
		out = append(out, dispatcher.ProvisionalRun{
			RunID: snap.Ref.ID, Decision: e.Decision, Taken: e.Answer, Index: i + 1,
			Reply: e.Reply, RepliedAt: e.RepliedAt,
		})
	}
	return out, nil
}

// ConfirmProvisional stamps the index'th decision (1-based) on runID as
// confirmed by the owner at, and clears the run's replied mark once no reply
// remains to handle. A decision that is absent, not provisional or already
// confirmed is a no-op; the run's state is never touched.
func (q *Queue) ConfirmProvisional(ctx context.Context, runID string, index int, at time.Time) error {
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
		i := index - 1
		if i < 0 || i >= len(row.Decisions) || !row.Decisions[i].Provisional || !row.Decisions[i].ConfirmedAt.IsZero() {
			return nil
		}
		row.Decisions[i].ConfirmedAt = at
		updates := []firestore.Update{{Path: decisionsField, Value: row.Decisions}}
		if repliedProvisional(row.Decisions) < 0 {
			updates = append(updates, firestore.Update{Path: provisionalRepliedField, Value: firestore.Delete})
		}
		return tx.Update(rowRef, updates)
	})
	if err != nil {
		return fmt.Errorf("confirming run %s's provisional decision: %w", runID, err)
	}
	return nil
}

// ClearProvisionalReply drops runID's replied mark when the reply the poll read
// at repliedAt is still the latest, so an unmatched reply is logged once and a
// newer reply that landed since keeps its mark. The decision keeps its reply.
func (q *Queue) ClearProvisionalReply(ctx context.Context, runID string, repliedAt time.Time) error {
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
		i := repliedProvisional(row.Decisions)
		if i < 0 || !row.Decisions[i].RepliedAt.Equal(repliedAt) {
			return nil
		}
		return tx.Update(rowRef, []firestore.Update{{Path: provisionalRepliedField, Value: firestore.Delete}})
	})
	if err != nil {
		return fmt.Errorf("clearing run %s's provisional reply: %w", runID, err)
	}
	return nil
}

// Supersede answers the index'th decision (1-based) on runID with the owner's
// chosen label and queues the run as its next attempt, so a fresh plan carries
// the answer. The decision stops being provisional, the row records the PR and
// attempt it replaces and carries that attempt's settled spend forward as
// RequeueAfterConflict does, and the run's reservations drop as Requeue's do.
//
// It refuses what Requeue refuses; a run still to run or in flight returns an
// error wrapping dispatcher.ErrRunNotEnded, left for a later poll.
func (q *Queue) Supersede(ctx context.Context, runID string, index int, answer string, at time.Time) error {
	_, err := q.requeue(ctx, runID, at, func(row map[string]any, attempt int) (string, []firestore.Update, error) {
		if _, err := requeueState(row); err != nil {
			if errors.Is(err, ErrRunInFlight) || errors.Is(err, ErrRunDispatchable) {
				return "", nil, fmt.Errorf("%w: %w", dispatcher.ErrRunNotEnded, err)
			}
			return "", nil, err
		}
		list, _ := row[decisionsField].([]any)
		i := index - 1
		if i < 0 || i >= len(list) {
			return "", nil, fmt.Errorf("run has no decision %d", index)
		}
		entry, _ := list[i].(map[string]any)
		if provisional, _ := entry["provisional"].(bool); !provisional {
			return "", nil, fmt.Errorf("decision %d is not provisional", index)
		}
		decisions := slices.Clone(list)
		settled := maps.Clone(entry)
		settled["answer"] = answer
		settled["provisional"] = false
		decisions[i] = settled
		var pr any = firestore.Delete
		if n := pullNumber(str(row, prURLField)); n > 0 {
			pr = n
		}
		cost, _ := row[settledProviderCostField].(int64)
		minutes, _ := row[settledRunnerMinutesField].(int64)
		// The plan stood on the option the owner rejected, so it is made again.
		return stateQueued, []firestore.Update{
			{Path: decisionsField, Value: decisions},
			{Path: provisionalRepliedField, Value: firestore.Delete},
			{Path: supersededAttemptField, Value: attempt - 1},
			{Path: supersededPRField, Value: pr},
			{Path: priorSettledCostField, Value: cost},
			{Path: priorSettledMinutesField, Value: minutes},
		}, nil
	})
	if err != nil {
		return fmt.Errorf("superseding run %s: %w", runID, err)
	}
	return nil
}

// pullNumber is the number in a pull request URL, or zero when url names none.
func pullNumber(url string) int {
	_, num, found := strings.Cut(url, "/pull/")
	if !found {
		return 0
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// SupersededPR reads the number of the PR a supersede of runID replaces, zero
// when the run records none.
func (q *Queue) SupersededPR(ctx context.Context, runID string) (int, error) {
	snap, err := q.client.Collection(runsCollection).Doc(runID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return 0, fmt.Errorf("%s: %w", runID, runner.ErrRecordNotFound)
	}
	if err != nil {
		return 0, err
	}
	n, _ := snap.Data()[supersededPRField].(int64)
	return int(n), nil
}

// ProvisionalDecisions reads the decisions auto-pilot settled on runID, in the
// order the plan asked them, with the option taken and whether the owner has
// confirmed it.
func (q *Queue) ProvisionalDecisions(ctx context.Context, runID string) ([]runner.Provisional, error) {
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
	var out []runner.Provisional
	for _, e := range row.Decisions {
		if !e.Provisional {
			continue
		}
		p := runner.Provisional{Question: e.Decision.Question, Choice: e.Answer, Confirmed: !e.ConfirmedAt.IsZero()}
		if i := slices.IndexFunc(e.Decision.Options, func(o runner.DecisionOption) bool { return o.Label == e.Answer }); i >= 0 {
			p.Cost = e.Decision.Options[i].Cost
		}
		out = append(out, p)
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
