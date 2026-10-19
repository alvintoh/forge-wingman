package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/money"
	prov "github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// dispatchCollection holds the bookkeeping beside the run records: one
	// rejected-<ticket id> doc per ticket the queue would not admit, which a
	// later refusal of the same ticket replaces, one webhook-<session id> doc
	// per Linear agent session the webhook has taken, one notice-<id> doc per
	// systemic-failure notice, the breaker document while a systemic stop
	// holds dispatch, and the single ledger document (adr/0003).
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
	// retriedAtField is when an operator returned a terminal run to the queue,
	// and attemptField how many times the ticket has been queued: the run's retry
	// history, kept beside the record a runner job rebuilds.
	retriedAtField = "retried_at"
	// abandonedReasonField and abandonedAtField record why and when the sweep
	// abandoned a claim; a requeue clears them. They sit apart from stop_reason
	// and outcome, which mark a run that reported back.
	abandonedReasonField = "abandoned_reason"
	abandonedAtField     = "abandoned_at"
	attemptField         = "attempt"
	// ticketStateField is the Linear state the ticket held before its claim: the
	// state a requeue or an abandon puts it back to. It sits outside the record,
	// which Finalize rebuilds whole.
	ticketStateField = "ticket_state"
	// queuedAtField is stamped at enqueue and requeue, the tie-break among runs
	// of one priority; an answered or released run keeps its position.
	queuedAtField = "queued_at"
	// startedAtField is Record's own start time, the oldest a legacy row's
	// queue position can be read from.
	startedAtField = "started_at"
	// waitingOnField names the condition that withheld a queued run at its
	// last claim attempt; a claim clears it.
	waitingOnField = "waiting_on"
	// claimConcurrencyField is how many runs were in flight, this one
	// included, when the run was claimed.
	claimConcurrencyField = "claim_concurrency"
	blockedByField        = "blocked_by"
	blocksField           = "blocks"
	// planFilesField is the write set the run's plan stage recorded, outside
	// Record, read here to build a Candidate without a second query.
	planFilesField = "plan_files"
	// planSettledAtField, planSpendMicrosField and planSpendMinutesField are
	// the plan stage's own settlement: when it settled and what its reserved
	// estimate cost it, summed by the budget windows beside a run's own and
	// kept outside the record Finalize rebuilds whole.
	planSettledAtField    = "plan_settled_at"
	planSpendMicrosField  = "plan_spend_micros"
	planSpendMinutesField = "plan_spend_minutes"

	// autonomyField is the owner's autonomy level the run's attempt was first
	// claimed at, absent until that claim.
	autonomyField = "autonomy"

	// decisionModeField is how the run's plan decisions are settled, recorded at
	// the attempt's first claim like autonomyField.
	decisionModeField = "decision_mode"

	// decisionsField is the list of decisions a run has parked on, written
	// outside runner.Record so Finalize's whole-field rebuild cannot clobber it.
	// stateWaitingField is the pause that accompanies it.
	decisionsField = "decisions"
	// runURLField is the Actions run that recorded the row's outcome: the plan
	// stage's own run while the row waits, later superseded by Finalize.
	runURLField = "run_url"
	// stopReasonField, stopDetailField and buildOutcomeField are Record's own
	// stop fields, written directly by StopWaiting since a parked run never
	// finalizes through a runner job.
	stopReasonField   = "stop_reason"
	stopDetailField   = "stop_detail"
	buildOutcomeField = "build_outcome"

	// transientField is Record's own transient-fault class, which the runner
	// records from the failing step's log and a requeue clears: a run between
	// attempts carries none.
	transientField = "transient"
	// transientRequeuesField is the automatic requeues a run has already had,
	// each recording the class it was made for and when. It sits outside the
	// record because Finalize rebuilds the record whole, and clearing it on a
	// requeue would lose the count the limit is read from.
	transientRequeuesField = "transient_requeues"

	// sizeField and privateField are Record's own fields (runner.Record),
	// read here to build a Candidate without a second query. modelLabelsField is
	// read for the build model a ticket named, which selects its per-model cap.
	sizeField        = "size"
	privateField     = "private"
	modelLabelsField = "model_labels"
	verdictField     = "provider_verdict"
	ticketIDField    = "ticket_id"
	outcomeField     = "outcome"
	// planField, lastResortField, lastResortModelsField, movedFromField and
	// planModelsField are Record's own, written with the claim.
	planField             = "plan"
	lastResortField       = "last_resort"
	lastResortModelsField = "last_resort_models"
	movedFromField        = "moved_from"
	planModelsField       = "plan_models"
	// settledAtField, settledProviderCostField and settledRunnerMinutesField
	// are Record's own settlement fields, written once by runner.Finalize and
	// summed here for FR-22's window checks.
	settledAtField            = "settled_at"
	settledProviderCostField  = "settled_provider_cost_micros"
	settledRunnerMinutesField = "settled_runner_minutes"
	// priorSettledCostField and priorSettledMinutesField are what the run's
	// earlier attempts settled, which the next record adds to its own.
	priorSettledCostField    = "prior_settled_provider_cost_micros"
	priorSettledMinutesField = "prior_settled_runner_minutes"
	// candidateScanLimit bounds how many queued runs one Candidates call
	// considers, so a long backlog of deferred tickets costs one bounded scan
	// rather than an unbounded one.
	candidateScanLimit = 25
)

// The states a run holds. A run leaves queued once, and comes back to it when
// the dispatch that claimed it never reached GitHub. A run whose plan stage
// has settled waits in planned for its build to claim; a build-stage claim
// that never reached GitHub returns it there, its plan still standing.
const (
	stateQueued  = "queued"
	statePlanned = "planned"
	stateClaimed = "claimed"
	// stateWaiting is a run parked on an owner decision: it holds no runner
	// and no reservation, and Candidates never lists it.
	stateWaiting = "waiting"
	// stateStopped is a wait that ended unanswered: the run is finished, and
	// nothing lists it as waiting, so a stop cannot hold a slot in the scan
	// that finds the runs still owed a question.
	stateStopped = "stopped"
	// stateAbandoned is a claim whose run never reported back within its
	// window: it holds no reservation, and only an operator's requeue returns it.
	stateAbandoned = "abandoned"
)

// candidateStates are the states a poll may claim from.
var candidateStates = []string{stateQueued, statePlanned}

// planKeySuffix keys a plan-stage reservation beside the run's own in the
// ledger: the two stages book separate reservations, settled at separate
// times, and the build-stage claim must find the run's own key free.
const planKeySuffix = "#plan"

// attemptKeyMarker joins a run id and its claim's attempt in a ledger key.
const attemptKeyMarker = "#a"

// reservationKey is the ledger key of runID's claim at attempt: every claim
// books under it, so one attempt's settle never reaches another's entry.
func reservationKey(runID string, attempt int, stage string) string {
	return stageKey(runID+attemptKeyMarker+strconv.Itoa(attempt), stage)
}

// reservationRunID is the run a ledger key belongs to, whether it is attempt-keyed or legacy.
func reservationRunID(key string) string {
	runID, _ := strings.CutSuffix(key, planKeySuffix)
	runID, _, _ = strings.Cut(runID, attemptKeyMarker)
	return runID
}

// legacyReservationKey is the key a claim booked under before attempts were
// part of it. It is read only to settle those claims and can go once the runs
// in flight at that deploy have drained.
func legacyReservationKey(runID, stage string) string {
	return stageKey(runID, stage)
}

// stageKey places a plan-stage claim's key beside the build's.
func stageKey(base, stage string) string {
	if stage == dispatcher.StagePlan {
		return base + planKeySuffix
	}
	return base
}

// rowAttempt is the attempt a run row is on: how many times it has been requeued.
func rowAttempt(row map[string]any) int {
	n, _ := row[attemptField].(int64)
	return int(n)
}

// transientRequeues is the automatic requeues a run has already had, in order,
// each recording the class it was made for and when.
func transientRequeues(row map[string]any) []any {
	list, _ := row[transientRequeuesField].([]any)
	return list
}

// Queue admits runs to the store's queue and claims them off it.
type Queue struct {
	client *firestore.Client
	logger *slog.Logger
}

// NewQueue returns a Queue backed by client, logging to the default logger.
func NewQueue(client *firestore.Client) *Queue {
	return &Queue{client: client, logger: slog.Default()}
}

// WithLogger returns a Queue over the same client that logs to logger.
func (q *Queue) WithLogger(logger *slog.Logger) *Queue {
	return &Queue{client: q.client, logger: logger}
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
	rec.ModelLabels = run.Models
	rec.UpdatedAt = run.At
	data := fields(rec)
	delete(data, autonomyField)
	delete(data, decisionModeField)
	data[stateField] = stateQueued
	data[priorityField] = run.Priority
	data[queuedAtField] = run.At
	data[repoField] = run.Repo
	if len(run.BlockedBy) > 0 {
		data[blockedByField] = run.BlockedBy
	}
	if len(run.Blocks) > 0 {
		data[blocksField] = run.Blocks
	}
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

// RecordTicketState records the Linear state the run's ticket held before its
// claim, which a requeue or an abandon restores it to. It is written beside the
// run record, so the runner's own Finalize cannot drop it, and it is overwritten
// by the next claim that moves the ticket.
func (q *Queue) RecordTicketState(ctx context.Context, runID, stateID string) error {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	if _, err := rowRef.Update(ctx, []firestore.Update{{Path: ticketStateField, Value: stateID}}); err != nil {
		return fmt.Errorf("recording the state run %s claimed its ticket in: %w", runID, err)
	}
	return nil
}

// UpdateQueuedPriority sets the priority rank of a queued or planned run — the
// states Candidates orders by it — reporting whether the run holds that rank
// afterwards. A run already at that rank is not written. A run in any other
// state keeps the rank it was claimed under, and a missing run reports false.
func (q *Queue) UpdateQueuedPriority(ctx context.Context, runID string, rank int) (bool, error) {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	var applied, changed bool
	var from int64
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		applied, changed = false, false
		snap, err := tx.Get(rowRef)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if !slices.Contains(candidateStates, str(snap.Data(), stateField)) {
			return nil
		}
		applied = true
		stored, ok := snap.Data()[priorityField].(int64)
		if ok && stored == int64(rank) {
			return nil
		}
		changed, from = true, stored
		return tx.Update(rowRef, []firestore.Update{{Path: priorityField, Value: rank}})
	})
	if err != nil {
		return false, fmt.Errorf("updating the priority of run %s: %w", runID, err)
	}
	if changed {
		q.logger.InfoContext(ctx, "queuedPriorityChanged", "run", runID, "from", from, "to", rank)
	}
	return applied, nil
}

// BackfillResult is what BackfillQueuedAt did: the rows it stamped, or with
// dryRun would stamp, and the rows it left alone because they changed under it
// or carry no time to stamp from.
type BackfillResult struct {
	Stamped int
	Skipped int
}

// BackfillQueuedAt stamps queued_at on every queued or planned row that lacks
// it, from the row's started_at and else its updated_at, so rows written before
// the field existed keep their relative order. Each row is re-read in its own
// transaction and written only while it is still queued or planned and still
// unstamped, so it cannot overwrite a concurrent Enqueue or requeue. A second
// run stamps nothing; with dryRun it writes nothing.
func (q *Queue) BackfillQueuedAt(ctx context.Context, dryRun bool) (BackfillResult, error) {
	var res BackfillResult
	refs, err := q.unstampedRefs(ctx)
	if err != nil {
		return res, err
	}
	for _, ref := range refs {
		stamped, err := q.stampQueuedAt(ctx, ref, dryRun)
		if err != nil {
			return res, err
		}
		if stamped {
			res.Stamped++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}

// stampQueuedAt stamps one row, reporting whether it did: the row is re-read, so
// a row that was stamped, claimed or settled since it was listed is left alone.
func (q *Queue) stampQueuedAt(ctx context.Context, ref *firestore.DocumentRef, dryRun bool) (bool, error) {
	stamped := false
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		stamped = false
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		row := snap.Data()
		at, ok := queuedAtSource(row)
		if _, has := row[queuedAtField]; has || !ok || !slices.Contains(candidateStates, str(row, stateField)) {
			return nil
		}
		stamped = true
		if dryRun {
			return nil
		}
		return tx.Update(ref, []firestore.Update{{Path: queuedAtField, Value: at}})
	})
	if err != nil {
		return false, fmt.Errorf("stamping queued_at on run %s: %w", ref.ID, err)
	}
	return stamped, nil
}

// unstampedRefs lists the queued and planned rows with no queued_at.
func (q *Queue) unstampedRefs(ctx context.Context) ([]*firestore.DocumentRef, error) {
	iter := q.client.Collection(runsCollection).
		Where(stateField, "in", candidateStates).Documents(ctx)
	defer iter.Stop()
	var refs []*firestore.DocumentRef
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return refs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing queued runs: %w", err)
		}
		if _, has := snap.Data()[queuedAtField]; !has {
			refs = append(refs, snap.Ref)
		}
	}
}

// queuedAtSource is the time a row without queued_at is stamped with: when its
// run started, else when its row last changed.
func queuedAtSource(row map[string]any) (time.Time, bool) {
	for _, field := range []string{startedAtField, updatedAtField} {
		if at, ok := row[field].(time.Time); ok && !at.IsZero() {
			return at, true
		}
	}
	return time.Time{}, false
}

// Candidates lists runs a poll may claim in Linear priority order, oldest
// queued first within a priority, for admission to walk (adr/0003): the queued,
// and with them the planned — a run whose plan stage has settled, waiting for
// its build to be claimed. Bounded by candidateScanLimit, so a long backlog of
// deferred tickets costs one scan rather than an unbounded one.
//
// Firestore drops a document missing an ordered field from the query, so a row
// with no priority or no queued_at is never listed; warnUnlisted logs them.
func (q *Queue) Candidates(ctx context.Context) ([]dispatcher.Candidate, error) {
	q.warnUnlisted(ctx)
	iter := q.client.Collection(runsCollection).
		Where(stateField, "in", candidateStates).
		OrderBy(priorityField, firestore.Asc).
		OrderBy(queuedAtField, firestore.Asc).
		Limit(candidateScanLimit).Documents(ctx)
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
		var stage string
		if state, _ := data[stateField].(string); state == statePlanned {
			stage = dispatcher.StageBuild
		}
		named := labelsOf(data, modelLabelsField)
		cand := dispatcher.Candidate{
			RunID: snap.Ref.ID, Repo: repo, Size: size, Private: private, Priority: int(priority),
			Model:       buildModel(data),
			ModelsNamed: named.Build != "" || named.Review != "" || len(named.Plan) > 0,
			Stage:       stage,
			PlanFiles:   stringList(data, planFilesField),
			Plan:        str(data, planField),
			Attempt:     rowAttempt(data),
		}
		if n, _ := data[supersededPRField].(int64); n > 0 {
			cand.SupersededPR = int(n)
		}
		if last, _ := data[lastResortField].(bool); last {
			labels := labelsOf(data, lastResortModelsField)
			cand.LastResort = &labels
		}
		out = append(out, cand)
	}
	return out, nil
}

// warnUnlisted logs once when more queued or planned rows exist than the
// ordered query can list. A failed count is logged and left at that: a
// diagnostic must not stop the poll.
func (q *Queue) warnUnlisted(ctx context.Context) {
	queued, listed, err := q.countUnlisted(ctx)
	if err != nil {
		q.logger.WarnContext(ctx, "queuedRowCountFailed", "err", err)
		return
	}
	if queued != listed {
		q.logger.WarnContext(ctx, "queuedRowsNotListed", "queued", queued, "listed", listed)
	}
}

// countUnlisted counts the queued and planned rows, and those among them the
// ordered query lists.
func (q *Queue) countUnlisted(ctx context.Context) (queued, listed int64, err error) {
	rows := q.client.Collection(runsCollection).Where(stateField, "in", candidateStates)
	if queued, err = countRows(ctx, rows); err != nil {
		return 0, 0, err
	}
	listed, err = countRows(ctx, rows.OrderBy(priorityField, firestore.Asc).OrderBy(queuedAtField, firestore.Asc))
	return queued, listed, err
}

// countRows counts the rows query matches without reading them.
func countRows(ctx context.Context, query firestore.Query) (int64, error) {
	res, err := query.NewAggregationQuery().WithCount("n").Get(ctx)
	if err != nil {
		return 0, err
	}
	n, ok := res["n"].(*firestorepb.Value)
	if !ok {
		return 0, fmt.Errorf("count result is %T, not a value", res["n"])
	}
	return n.GetIntegerValue(), nil
}

// ClaimedSince counts the runs claimed at or after since: the runs a level's
// daily bar of delegations is spent from. A run whose claim was
// released or requeued carries no claim stamp and is not counted, since it never
// reached a runner.
func (q *Queue) ClaimedSince(ctx context.Context, since time.Time) (int, error) {
	n, err := countRows(ctx, q.client.Collection(runsCollection).Where(claimedAtField, ">=", since))
	if err != nil {
		return 0, fmt.Errorf("counting the runs claimed since %s: %w", since.UTC().Format(time.RFC3339), err)
	}
	return int(n), nil
}

// Planned lists every run whose plan stage has recorded a write set and whose
// own run has not been recorded yet: the rows in state planned or claimed that
// carry plan_files and no settled_at. A two-stage run waits in planned between
// its plan stage and its build — a window no ledger reservation covers, since
// settling the plan drops it — so a pull request asking what it could collide
// with reads the rows rather than the ledger.
//
// This scans every run row in either state, the way Candidates scans the queued
// ones, and filters in Go: accepted at this project's personal-scale volume,
// same as the settled-cost scans.
func (q *Queue) Planned(ctx context.Context) ([]dispatcher.InFlight, error) {
	iter := q.client.Collection(runsCollection).
		Where(stateField, "in", []string{statePlanned, stateClaimed}).
		Documents(ctx)
	defer iter.Stop()
	var out []dispatcher.InFlight
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing planned runs: %w", err)
		}
		data := snap.Data()
		files := stringList(data, planFilesField)
		if len(files) == 0 || settled(data) {
			continue
		}
		out = append(out, dispatcher.InFlight{
			Ticket: str(data, ticketIDField), Repo: str(data, repoField), Size: str(data, sizeField),
			Files: files,
		})
	}
	return out, nil
}

// settled reports whether row's run has been recorded, which only the record job
// writes. The field is written either way, so a zero time is what an unrecorded
// run holds.
func settled(row map[string]any) bool {
	at, ok := row[settledAtField].(time.Time)
	return ok && !at.IsZero()
}

// stringList is row's field as a string list, empty when it holds none.
func stringList(row map[string]any, field string) []string {
	var out []string
	list, _ := row[field].([]any)
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// str is row's field as a string, empty when it holds none.
func str(row map[string]any, field string) string {
	v, _ := row[field].(string)
	return v
}

// buildModel is the build model a queued run's ticket named, empty when it named
// none and the run's own default applies.
func buildModel(row map[string]any) string {
	labels, _ := row[modelLabelsField].(map[string]any)
	model, _ := labels["build"].(string)
	return model
}

// labelsOf is the model labels row holds in field, zero when it holds none.
func labelsOf(row map[string]any, field string) runner.ModelLabels {
	labels, _ := row[field].(map[string]any)
	return runner.ModelLabels{Build: str(labels, "build"), Review: str(labels, "review"), Plan: stringList(labels, "plan")}
}

// ledgerDoc is the dispatch/ledger document's own shape (adr/0003): the
// reservation every claimed-but-not-yet-settled run holds, keyed by run id and
// attempt, and the discovered concurrency limit N beside them. A ledger written
// before N existed reads as 1.
type ledgerDoc struct {
	Reservations map[string]reservationEntry `firestore:"reservations"`
	N            int                         `firestore:"n"`
	StableRuns   int                         `firestore:"stable_runs"`
	// PlatformCap is the configured cap N may not pass, as the last claim saw it.
	PlatformCap int `firestore:"platform_cap"`
	// ChangedAfter is the latest claim time among the runs in flight when N last
	// moved; a run claimed no later than this ran under the old N.
	ChangedAfter time.Time `firestore:"changed_after"`
	// TuneStableRuns, TuneRiseWithin and TuneHalveBeyond are the rule's numbers as
	// the last claim saw them, for the runner's Settle to apply.
	TuneStableRuns  int     `firestore:"tune_stable_runs"`
	TuneRiseWithin  float64 `firestore:"tune_rise_within"`
	TuneHalveBeyond float64 `firestore:"tune_halve_beyond"`
}

type reservationEntry struct {
	ProviderCostMicros money.Micros `firestore:"provider_cost_micros"`
	RunnerMinutes      int64        `firestore:"runner_minutes"`
	TicketID           string       `firestore:"ticket_id"`
	Repo               string       `firestore:"repo"`
	Size               string       `firestore:"size"`
	ClaimedAt          time.Time    `firestore:"claimed_at"`
	Attempt            int          `firestore:"attempt"`
	// Stage is the stage this reserve claimed — plan or build; empty for a
	// single-stage claim, which plans and builds in one job.
	Stage string `firestore:"stage"`
	// Files is the write set this reserve may touch — a build-stage claim's
	// plan — and empty for a plan-stage or single-stage one.
	Files []string `firestore:"files"`
}

// inFlight lists the runs the ledger holds a reservation for, with the stage
// and write set each reservation carries for the one-run-per-repository
// condition's overlap check.
func inFlight(doc ledgerDoc) []dispatcher.InFlight {
	out := make([]dispatcher.InFlight, 0, len(doc.Reservations))
	for _, r := range doc.Reservations {
		out = append(out, dispatcher.InFlight{
			Ticket: r.TicketID, Repo: r.Repo, Size: r.Size, Stage: r.Stage, Files: r.Files,
		})
	}
	return out
}

// tuning is the rule's numbers as the ledger holds them, defaulted where unset.
func (d ledgerDoc) tuning() dispatcher.Tuning {
	return dispatcher.Tuning{StableRuns: d.TuneStableRuns, RiseWithin: d.TuneRiseWithin, HalveBeyond: d.TuneHalveBeyond}.OrDefault()
}

// concurrency is the ledger's N and stable count as the rule reads them.
func (d ledgerDoc) concurrency() dispatcher.Concurrency {
	return dispatcher.Concurrency{N: max(d.N, 1), Stable: d.StableRuns}
}

// subject is the candidate a run row describes.
func subject(row map[string]any) dispatcher.Subject {
	return dispatcher.Subject{
		Ticket: str(row, ticketIDField), Repo: str(row, repoField), Size: str(row, sizeField),
		BlockedBy: stringList(row, blockedByField), Blocks: stringList(row, blocksField),
		Files: stringList(row, planFilesField),
	}
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
// row (must still be queued, or planned — waiting for its build) and the
// ledger, sums the ledger's in-flight reservations and the settled cost of
// runs that ended inside each of cfg's windows, and commits — claiming the
// row and adding res to the ledger — only if dispatcher.Admit lets it start
// (adr/0003). The row and the ledger are read here, so two polls cannot both
// claim one run (AC2); the settled cost is summed from facts.History, the
// poll's own snapshot, so a poll reads that history once however many
// candidates it walks (AC1), and a candidate a condition checked ahead of the
// ceilings holds is withheld without reading it at all (AC5). It reports
// (false, "", nil) when another poll already claimed the row, and
// (false, <binding>, nil) when a condition withholds it, which it also records
// on the row. A row still in a dispatchable state that already records an
// outcome or a stop reason has ended without a claim: it is settled to claimed
// and logged instead of claimed for a build, and books no reservation.
//
// The reservation is booked under the key of the row's attempt, a plan-stage
// one beside the build's, so the build stage can claim the run while its plan
// is settled, and the tier a plan-stage claim wrote onto the row stands for the
// whole run: a build-stage claim leaves those fields as its plan stage left them.
func (q *Queue) TryClaim(ctx context.Context, runID string, at time.Time, cfg dispatcher.BudgetConfig, facts dispatcher.Facts, res dispatcher.Reservation) (bool, string, error) {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	var binding string
	var claimed bool
	var overlaps []string
	var settledRow bool
	var endedOutcome string
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		binding, claimed, overlaps, settledRow, endedOutcome = "", false, nil, false, ""
		row, err := tx.Get(rowRef)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		data := row.Data()
		state, _ := data[stateField].(string)
		if state != stateQueued && state != statePlanned {
			return nil
		}
		// A row in a dispatchable state that has already ended was left there
		// by a run that recorded its own result without a claim — a manual
		// dispatch — so claiming it would rebuild finished work. Settle it to
		// the state a finished run sits in, clear its wait, and leave the ledger
		// alone.
		if hasEnded(data) {
			settledRow, endedOutcome = true, str(data, outcomeField)
			return tx.Update(rowRef, []firestore.Update{
				{Path: stateField, Value: stateClaimed},
				{Path: updatedAtField, Value: at},
				{Path: waitingOnField, Value: firestore.Delete},
			})
		}

		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}

		running := inFlight(ledger)
		cand := subject(data)
		// The claim's own stage, which the row does not record: admission lets a
		// plan-stage claim share its repository, since it writes nothing.
		cand.Stage = res.Stage
		if facts.Relations.Known {
			cand.BlockedBy, cand.Blocks = facts.Relations.BlockedBy, facts.Relations.Blocks
			cand.BlockedByOpen = facts.Relations.BlockedByOpen
		}
		input := dispatcher.AdmitInput{
			Facts:       facts,
			N:           ledger.concurrency().N,
			InFlight:    running,
			Candidate:   cand,
			Budget:      cfg,
			Reserved:    reservedTotals(ledger),
			Reservation: res,
		}
		// A condition checked ahead of the ceilings withholds the candidate
		// without reading the settled history its ceilings would be summed from
		// (AC5): a poll the review WIP or the concurrency limit already holds
		// reads no history at all.
		if why := dispatcher.HeldBeforeBudget(input); why != "" {
			binding = why
			return tx.Update(rowRef, []firestore.Update{{Path: waitingOnField, Value: why}})
		}
		settled, err := q.settledFor(tx, cfg, facts.Model, at, facts.History)
		if err != nil {
			return err
		}
		input.Settled = settled
		ok, why := dispatcher.Admit(input)
		if !ok {
			binding = why
			return tx.Update(rowRef, []firestore.Update{{Path: waitingOnField, Value: why}})
		}

		if ledger.Reservations == nil {
			ledger.Reservations = map[string]reservationEntry{}
		}
		attempt := rowAttempt(data)
		ledger.Reservations[reservationKey(runID, attempt, res.Stage)] = reservationEntry{
			ProviderCostMicros: res.ProviderCost,
			RunnerMinutes:      res.RunnerMinutes,
			TicketID:           cand.Ticket,
			Repo:               cand.Repo,
			Size:               cand.Size,
			ClaimedAt:          at,
			Attempt:            attempt,
			Stage:              res.Stage,
			Files:              res.Files,
		}
		ledger.N = ledger.concurrency().N
		ledger.PlatformCap = facts.Limits.PlatformCap
		ledger.TuneStableRuns, ledger.TuneRiseWithin, ledger.TuneHalveBeyond = facts.Tuning.StableRuns, facts.Tuning.RiseWithin, facts.Tuning.HalveBeyond
		if err := tx.Set(ledgerRef, ledger); err != nil {
			return err
		}
		updates := []firestore.Update{
			{Path: stateField, Value: stateClaimed},
			{Path: claimedAtField, Value: at},
			{Path: updatedAtField, Value: at},
			{Path: claimConcurrencyField, Value: len(running) + 1},
			{Path: waitingOnField, Value: firestore.Delete},
		}
		// The level is recorded once per attempt, so a build stage, an answered
		// decision or a released dispatch keeps the level first claimed at.
		if _, recorded := data[autonomyField]; !recorded {
			updates = append(updates, firestore.Update{Path: autonomyField, Value: int64(facts.Autonomy)})
		}
		if _, recorded := data[decisionModeField]; !recorded {
			updates = append(updates, firestore.Update{Path: decisionModeField, Value: string(facts.DecisionMode)})
		}
		if res.Stage != dispatcher.StageBuild {
			var lastResortModels, movedFrom, planModels any = firestore.Delete, firestore.Delete, firestore.Delete
			if facts.LastResort != nil {
				lastResortModels = *facts.LastResort
			}
			if facts.Moved != nil {
				movedFrom, planModels = facts.Moved.From, facts.Moved.Models
			}
			updates = append(updates,
				firestore.Update{Path: planField, Value: runner.Provider(facts.Model)},
				firestore.Update{Path: lastResortField, Value: facts.LastResort != nil},
				firestore.Update{Path: lastResortModelsField, Value: lastResortModels},
				firestore.Update{Path: movedFromField, Value: movedFrom},
				firestore.Update{Path: planModelsField, Value: planModels},
			)
		}
		if err := tx.Update(rowRef, updates); err != nil {
			return err
		}
		claimed = true
		overlaps = dispatcher.OverlappingRuns(input)
		return nil
	})
	if err != nil {
		return false, "", fmt.Errorf("claiming run %s: %w", runID, err)
	}
	if settledRow {
		q.logger.Warn("queuedRowSettled", "run", runID, "outcome", endedOutcome)
	}
	if claimed && len(overlaps) > 0 {
		q.logger.Info("admittedOverlapping", "run", runID, "overlaps", overlaps)
	}
	return claimed, binding, nil
}

// Spend is what the ledger holds reserved now, and the settled cost inside each
// of cfg's windows ending at at: the ledger is read in one read-only
// transaction, and the settled half is summed from history — the poll's own
// snapshot, so a poll reads the settled run history once however many deferrals
// raise a notice (AC1) — or, for a caller holding none, in that transaction
// itself.
func (q *Queue) Spend(ctx context.Context, cfg dispatcher.BudgetConfig, model string, at time.Time, history dispatcher.HistoryLoader) (dispatcher.Totals, dispatcher.Settled, error) {
	var reserved dispatcher.Totals
	var settled dispatcher.Settled
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		ledger, err := q.readLedger(tx, q.ledgerRef())
		if err != nil {
			return err
		}
		reserved = reservedTotals(ledger)
		settled, err = q.settledFor(tx, cfg, model, at, history)
		return err
	}, firestore.ReadOnly)
	if err != nil {
		return dispatcher.Totals{}, dispatcher.Settled{}, fmt.Errorf("reading the spend at %s: %w", at, err)
	}
	return reserved, settled, nil
}

// PlanWindows is what each of plan's own allowance windows holds spent at at,
// one figure per window in the order given, read in one read-only transaction.
func (q *Queue) PlanWindows(ctx context.Context, plan string, windows []dispatcher.Window, at time.Time) ([]money.Micros, error) {
	spend := make([]money.Micros, len(windows))
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		for i, window := range windows {
			spent, err := q.windowSpend(tx, window, plan, at)
			if err != nil {
				return err
			}
			spend[i] = spent
		}
		return nil
	}, firestore.ReadOnly)
	if err != nil {
		return nil, fmt.Errorf("reading plan %s's window spend at %s: %w", plan, at, err)
	}
	return spend, nil
}

// settledFor is the settled spend a caller's ceilings are checked against: the
// poll's own snapshot of the settled history where the caller carries one — read
// once for the whole poll, however many candidates it walks and however many
// deferrals raise a notice (AC1) — and this transaction's own scan where it
// carries none.
func (q *Queue) settledFor(tx *firestore.Transaction, cfg dispatcher.BudgetConfig, model string, at time.Time, history dispatcher.HistoryLoader) (dispatcher.Settled, error) {
	if history == nil {
		return q.settled(tx, cfg, model, at)
	}
	rows, err := history()
	if err != nil {
		return dispatcher.Settled{}, err
	}
	return rows.Sums(cfg, model, at), nil
}

// settled sums, within tx, the settled cost inside each of cfg's windows
// ending at at, the run model's own cap among them. A window and a model cap
// count only the runs of the model's own plan; the cash ceiling counts every
// plan's. Each window holds both halves of a two-stage run's spend: what its own
// settlement cost, from settledSince, and what its plan stage's reserved estimate
// cost it, from planSince — so a plan is never left outside the ceiling it was
// checked against, and never counted twice either.
func (q *Queue) settled(tx *firestore.Transaction, cfg dispatcher.BudgetConfig, model string, at time.Time) (dispatcher.Settled, error) {
	settled := dispatcher.Settled{Windows: make([]money.Micros, len(cfg.ProviderWindows))}
	plan := runner.Provider(model)
	for i, w := range cfg.ProviderWindows {
		spent, err := q.windowSpend(tx, w, plan, at)
		if err != nil {
			return dispatcher.Settled{}, err
		}
		settled.Windows[i] = spent
	}
	if modelCap, ok := cfg.ModelCaps[model]; ok {
		spent, err := q.windowSpend(tx, modelCap, plan, at)
		if err != nil {
			return dispatcher.Settled{}, err
		}
		settled.ModelCap = spent
	}
	var err error
	settled.CashCost, settled.CashMinutes, err = q.settledSince(tx, cfg.Cash.Since(at), "")
	if err != nil {
		return dispatcher.Settled{}, err
	}
	planCost, planMinutes, err := q.planSince(tx, cfg.Cash.Since(at), "")
	if err != nil {
		return dispatcher.Settled{}, err
	}
	settled.CashCost += planCost
	settled.CashMinutes += planMinutes
	return settled, nil
}

// windowSpend is what window holds at at for plan's own runs, within tx: what
// they settled for plus the reserved estimate of their plan stages, so a plan
// is counted against the ceiling it was checked against and never twice.
func (q *Queue) windowSpend(tx *firestore.Transaction, window dispatcher.Window, plan string, at time.Time) (money.Micros, error) {
	sum, _, err := q.settledSince(tx, window.Since(at), plan)
	if err != nil {
		return 0, err
	}
	planCost, _, err := q.planSince(tx, window.Since(at), plan)
	if err != nil {
		return 0, err
	}
	return sum + planCost, nil
}

// planSince sums the plan stage's reserved estimate over every run whose plan
// settled at or after since, within tx — the cost scan's plan-stage half, read
// by the plan's own settlement stamp rather than the run's, so a run that
// plans and never builds is still counted. It re-scans the window the way
// settledSince does, for the reason recorded there. A plan other than "" limits
// the sum to that plan's runs.
func (q *Queue) planSince(tx *firestore.Transaction, since time.Time, plan string) (cost money.Micros, minutes int64, err error) {
	iter := tx.Documents(q.client.Collection(runsCollection).Where(planSettledAtField, ">=", since))
	defer iter.Stop()
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("summing plan stages since %s: %w", since, err)
		}
		data := snap.Data()
		if !onPlan(data, plan) {
			continue
		}
		c, _ := data[planSpendMicrosField].(int64)
		m, _ := data[planSpendMinutesField].(int64)
		cost += money.Micros(c)
		minutes += m
	}
	return cost, minutes, nil
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

// SettledHistory reads the settled run history at or after since, once for a
// poll: every run whose settlement, or whose plan stage's settlement, falls in
// that range, projected to the fields the budget ceilings sum. One poll sums
// every candidate's ceilings from the result in memory, so the history costs a
// poll one read however many candidates it walks (AC1).
//
// Two scans, because the two stamps are written independently — a run that
// planned and never built carries plan_settled_at alone — so neither range
// contains the other. A run inside both is read once, under whichever scan
// reached it first, since the rows are keyed by their path.
func (q *Queue) SettledHistory(ctx context.Context, since time.Time) (dispatcher.SettledHistory, error) {
	rows := map[string]dispatcher.Settlement{}
	for _, field := range []string{settledAtField, planSettledAtField} {
		if err := q.settledRows(ctx, field, since, rows); err != nil {
			return nil, err
		}
	}
	history := make(dispatcher.SettledHistory, 0, len(rows))
	for _, s := range rows {
		history = append(history, s)
	}
	return history, nil
}

// settledRows adds every run whose field falls at or after since to rows, keyed
// by its document path, reading only the fields the budget ceilings sum.
func (q *Queue) settledRows(ctx context.Context, field string, since time.Time, rows map[string]dispatcher.Settlement) error {
	iter := q.client.Collection(runsCollection).
		Where(field, ">=", since).
		Select(settledAtField, planSettledAtField, planField,
			settledProviderCostField, settledRunnerMinutesField, planSpendMicrosField, planSpendMinutesField).
		Documents(ctx)
	defer iter.Stop()
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the runs settled since %s: %w", since, err)
		}
		row := snap.Data()
		settledAt, _ := row[settledAtField].(time.Time)
		planSettledAt, _ := row[planSettledAtField].(time.Time)
		cost, _ := row[settledProviderCostField].(int64)
		minutes, _ := row[settledRunnerMinutesField].(int64)
		planCost, _ := row[planSpendMicrosField].(int64)
		planMinutes, _ := row[planSpendMinutesField].(int64)
		rows[snap.Ref.Path] = dispatcher.Settlement{
			SettledAt: settledAt, PlanSettledAt: planSettledAt, Plan: str(row, planField),
			Cost: money.Micros(cost), Minutes: minutes,
			PlanCost: money.Micros(planCost), PlanMinutes: planMinutes,
		}
	}
}

// settledSince sums settled_provider_cost_micros and settled_runner_minutes
// over every run whose settlement fell at or after since, within tx — one
// scan shared by every window's admission check, so the provider-window and
// cash queries cannot silently diverge from each other.
//
// A poll's claims no longer call it: they sum a window from the poll's own
// snapshot of the history in memory (SettledHistory), one read for the whole
// poll. This in-transaction scan serves a caller holding no snapshot.
//
// A plan other than "" limits the sum to that plan's runs, filtered here rather
// than in the query so a run settled before its plan was written still counts.
func (q *Queue) settledSince(tx *firestore.Transaction, since time.Time, plan string) (cost money.Micros, minutes int64, err error) {
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
		if !onPlan(data, plan) {
			continue
		}
		c, _ := data[settledProviderCostField].(int64)
		m, _ := data[settledRunnerMinutesField].(int64)
		costSum += c
		minutesSum += m
	}
	return money.Micros(costSum), minutesSum, nil
}

// onPlan reports whether row's run is on plan, every run being on plan "". A run
// settled before plans were recorded is on the default model's plan.
func onPlan(row map[string]any, plan string) bool {
	if plan == "" {
		return true
	}
	if rowPlan := str(row, planField); rowPlan != "" {
		return rowPlan == plan
	}
	return plan == runner.Provider(runner.DefaultModel())
}

// reservedKeys lists the ledger keys holding runID's reservation at attempt,
// build before plan. The pre-attempt keys count only when none of the attempt's
// own is held, so a legacy entry is never matched once the attempt has one.
func (d ledgerDoc) reservedKeys(runID string, attempt int) []string {
	held := func(keys ...string) []string {
		return slices.DeleteFunc(keys, func(k string) bool { _, has := d.Reservations[k]; return !has })
	}
	if keys := held(reservationKey(runID, attempt, dispatcher.StageBuild), reservationKey(runID, attempt, dispatcher.StagePlan)); len(keys) > 0 {
		return keys
	}
	return held(legacyReservationKey(runID, dispatcher.StageBuild), legacyReservationKey(runID, dispatcher.StagePlan))
}

// reservation is the entry runID holds at attempt and the ledger key holding it.
func (d ledgerDoc) reservation(runID string, attempt int) (key string, entry reservationEntry, ok bool) {
	keys := d.reservedKeys(runID, attempt)
	if len(keys) == 0 {
		return "", reservationEntry{}, false
	}
	return keys[0], d.Reservations[keys[0]], true
}

// Release returns a claimed run to the queue and drops its ledger
// reservation, for the dispatch that never reached GitHub: the next poll then
// considers it again, at the priority it was queued with, with no stale
// reservation counted against it.
//
// A build-stage claim returns to planned rather than queued — the plan its
// plan stage recorded still stands, so the next poll claims it for its build
// without planning again.
func (q *Queue) Release(ctx context.Context, runID string, at time.Time) error {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		snap, err := tx.Get(rowRef)
		if err != nil {
			return err
		}
		key, entry, has := ledger.reservation(runID, rowAttempt(snap.Data()))
		state := stateQueued
		if has && entry.Stage == dispatcher.StageBuild {
			state = statePlanned
		}
		if err := tx.Update(rowRef, []firestore.Update{
			{Path: stateField, Value: state},
			{Path: updatedAtField, Value: at},
			{Path: claimedAtField, Value: firestore.Delete},
		}); err != nil {
			return err
		}
		if !has {
			return nil
		}
		return tx.Update(ledgerRef, []firestore.Update{
			{FieldPath: firestore.FieldPath{"reservations", key}, Value: firestore.Delete},
		})
	})
	if err != nil {
		return fmt.Errorf("releasing run %s: %w", runID, err)
	}
	return nil
}

// Stop ends a run that will never reach GitHub: its row leaves the queue with
// the reason recorded, and its plan reservation is released, so nothing counts
// a run that will not proceed. It is what a plan stage that stopped before
// writing its artifact needs — otherwise the reservation is held for a run no
// job will ever record. Unlike Release the run does not return to the
// queue: the reason reports a condition no retry changes.
//
// The plan really did spend, so its reserved estimate books onto the row the way
// a settled plan's does, and the settle stamp is written here because a stopped
// plan never reaches WritePlan — the window the cost falls in is the one that
// stamp lies in.
func (q *Queue) Stop(ctx context.Context, runID string, reason runner.StopReason, detail string, at time.Time) error {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		updates := []firestore.Update{
			{Path: stateField, Value: stateStopped},
			{Path: outcomeField, Value: runner.OutcomeStopped},
			{Path: buildOutcomeField, Value: runner.OutcomeStopped},
			{Path: stopReasonField, Value: reason},
			{Path: stopDetailField, Value: detail},
			{Path: updatedAtField, Value: at},
		}
		snap, err := tx.Get(rowRef)
		if err != nil {
			return err
		}
		key, entry, has := ledger.reservation(runID, rowAttempt(snap.Data()))
		if !has {
			return tx.Update(rowRef, updates)
		}
		var row planRow
		if err := snap.DataTo(&row); err != nil {
			return err
		}
		spend, ledgerUpdates := planSettle(key, entry, row)
		updates = append(updates, spend...)
		updates = append(updates, firestore.Update{Path: planSettledAtField, Value: at})
		if err := tx.Update(rowRef, updates); err != nil {
			return err
		}
		return tx.Update(ledgerRef, ledgerUpdates)
	})
	if err != nil {
		return fmt.Errorf("stopping run %s: %w", runID, err)
	}
	return nil
}

// RequeueResult is what a requeue did: the state the run returned to, the
// attempt it was put back as, and the Linear state its ticket held before the
// claim, empty when that claim moved none, which the ticket is put back to.
type RequeueResult struct {
	State       string
	Attempt     int
	TicketState string
}

// The reasons Requeue refuses a run, each reporting a different operator action:
// one already dispatchable needs no help, one waiting on a decision is resumed
// by answering it, and one in flight would run its ticket twice.
var (
	ErrRunDispatchable = errors.New("run is already dispatchable")
	ErrRunWaiting      = errors.New("run is waiting on a decision")
	ErrRunInFlight     = errors.New("run is still in flight")
	ErrRunAbandoned    = errors.New("run was abandoned and needs an operator's requeue")
)

// Requeue returns a run that has ended — stopped, abandoned, or carrying a
// recorded outcome, whether a claim or a manual run left it — to the queue, for
// an operator asking that its ticket be attempted again: the row returns to
// planned when its plan still stands and queued otherwise, with its claim, wait
// and outcome fields cleared and its ledger reservations dropped, and records
// the attempt it was put back as.
//
// It refuses a run that is still to be dispatched, parked on a decision, or
// still running, so a retry is never anything but the operator's own action.
func (q *Queue) Requeue(ctx context.Context, runID string, at time.Time) (RequeueResult, error) {
	return q.requeue(ctx, runID, at, func(row map[string]any, _ int) (string, []firestore.Update, error) {
		state, err := requeueState(row)
		return state, nil, err
	})
}

// ErrRetriesExhausted reports that a run has already been put back as many
// times as the automatic retry allows.
var ErrRetriesExhausted = errors.New("run has no automatic retries left")

// RequeueAfterConflict returns a run whose PR rebase conflicted to the queue —
// never to planned, so its plan stage runs again against the new base — as the
// next attempt, and carries what the failed attempt settled into the row's
// prior-settled fields for the next record to add to its own. It refuses the
// runs Requeue refuses, an abandoned run, which only an operator's Requeue
// returns, and a run whose next attempt would pass maxAttempts.
func (q *Queue) RequeueAfterConflict(ctx context.Context, runID string, at time.Time, maxAttempts int) (RequeueResult, error) {
	return q.requeue(ctx, runID, at, func(row map[string]any, attempt int) (string, []firestore.Update, error) {
		if str(row, stateField) == stateAbandoned {
			return "", nil, ErrRunAbandoned
		}
		if _, err := requeueState(row); err != nil {
			return "", nil, err
		}
		if attempt > maxAttempts {
			return "", nil, ErrRetriesExhausted
		}
		cost, _ := row[settledProviderCostField].(int64)
		minutes, _ := row[settledRunnerMinutesField].(int64)
		return stateQueued, []firestore.Update{
			{Path: priorSettledCostField, Value: cost},
			{Path: priorSettledMinutesField, Value: minutes},
		}, nil
	})
}

// TransientStops lists every run that ended carrying a transient fault class,
// which is what a poll requeues on. It reads the classes one at a time: they
// are a closed list, so the queries are too, and a row carrying anything else
// is never listed.
func (q *Queue) TransientStops(ctx context.Context) ([]dispatcher.TransientStop, error) {
	var out []dispatcher.TransientStop
	for _, class := range runner.TransientClasses() {
		iter := q.client.Collection(runsCollection).Where(transientField, "==", string(class)).Documents(ctx)
		for {
			snap, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				iter.Stop()
				return nil, fmt.Errorf("listing the runs stopped on %s: %w", class, err)
			}
			row := snap.Data()
			out = append(out, dispatcher.TransientStop{
				RunID:    snap.Ref.ID,
				Ticket:   str(row, ticketIDField),
				Repo:     str(row, repoField),
				Class:    class,
				Attempts: len(transientRequeues(row)),
			})
		}
		iter.Stop()
	}
	return out, nil
}

// RequeueTransient returns a run that stopped on a transient fault to the queue
// as its next attempt, recording the class the requeue was made for and
// reporting the attempt it is now on beside the state its ticket held before
// the claim, which the ticket is put back to. It refuses the
// runs Requeue refuses — a run still in flight among them — and one that has
// already used max automatic requeues, with ErrRetriesExhausted.
//
// It returns the run to the queue and nothing more: the claim that follows it
// is admitted like any other, so a budget ceiling, the review WIP limit or a
// tripped breaker holds a retry exactly as it holds a first attempt.
func (q *Queue) RequeueTransient(ctx context.Context, runID string, at time.Time, class runner.TransientClass, max int) (dispatcher.TransientRequeue, error) {
	res, err := q.requeue(ctx, runID, at, func(row map[string]any, _ int) (string, []firestore.Update, error) {
		state, err := requeueState(row)
		if err != nil {
			return "", nil, err
		}
		history := transientRequeues(row)
		if len(history) >= max {
			return "", nil, ErrRetriesExhausted
		}
		next := append(slices.Clone(history), map[string]any{"class": string(class), "at": at})
		return state, []firestore.Update{{Path: transientRequeuesField, Value: next}}, nil
	})
	if err != nil {
		return dispatcher.TransientRequeue{}, fmt.Errorf("requeueing run %s after a transient fault: %w", runID, err)
	}
	return dispatcher.TransientRequeue{Attempt: res.Attempt, TicketState: res.TicketState}, nil
}

// requeue is the transaction Requeue and RequeueAfterConflict share: decide
// names the state the run returns to, given the row and the attempt it would be
// put back as, with any fields to write beside the common ones, or the refusal.
func (q *Queue) requeue(ctx context.Context, runID string, at time.Time,
	decide func(row map[string]any, attempt int) (string, []firestore.Update, error)) (RequeueResult, error) {
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	ledgerRef := q.ledgerRef()
	var result RequeueResult
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(rowRef)
		if status.Code(err) == codes.NotFound {
			return runner.ErrRecordNotFound
		}
		if err != nil {
			return err
		}
		row := snap.Data()
		attempt := rowAttempt(row) + 1
		state, extra, err := decide(row, attempt)
		if err != nil {
			return err
		}
		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		if err := tx.Update(rowRef, append([]firestore.Update{
			{Path: stateField, Value: state},
			{Path: updatedAtField, Value: at},
			{Path: claimedAtField, Value: firestore.Delete},
			{Path: waitingOnField, Value: firestore.Delete},
			{Path: outcomeField, Value: firestore.Delete},
			{Path: buildOutcomeField, Value: firestore.Delete},
			{Path: stopReasonField, Value: firestore.Delete},
			{Path: stopDetailField, Value: firestore.Delete},
			{Path: transientField, Value: firestore.Delete},
			{Path: retriedAtField, Value: at},
			{Path: abandonedReasonField, Value: firestore.Delete},
			{Path: abandonedAtField, Value: firestore.Delete},
			{Path: ticketStateField, Value: firestore.Delete},
			{Path: queuedAtField, Value: at},
			{Path: attemptField, Value: attempt},
			{Path: autonomyField, Value: firestore.Delete},
			{Path: decisionModeField, Value: firestore.Delete},
		}, extra...)); err != nil {
			return err
		}
		result = RequeueResult{State: state, Attempt: attempt, TicketState: str(row, ticketStateField)}
		return dropReservations(tx, ledgerRef, ledger, runID, attempt-1)
	})
	if err != nil {
		return RequeueResult{}, fmt.Errorf("requeueing run %s: %w", runID, err)
	}
	return result, nil
}

// hasEnded reports whether row carries an outcome or a stop reason — what a
// finished run records — which is how a row left in a dispatchable state is
// told from one that has not run yet. It reads the run's own result rather than
// settled's record stamp, so a claim and a requeue agree on which rows ended.
func hasEnded(row map[string]any) bool {
	return str(row, outcomeField) != "" || str(row, stopReasonField) != ""
}

// requeueState is the state a requeue of row returns the run to, or the refusal
// that reports why it will not: a settled plan (a recorded write set and the
// plan stage's own stamp) sends the run to planned, the way a released
// build-stage claim returns there, and every other run returns to queued.
//
// A queued or planned row still to be dispatched is refused; one that has ended
// without a claim is requeued instead, so a manual run's result never strands
// its row.
func requeueState(row map[string]any) (string, error) {
	state := str(row, stateField)
	switch state {
	case stateQueued, statePlanned:
		if !hasEnded(row) {
			return "", ErrRunDispatchable
		}
	case stateWaiting:
		return "", ErrRunWaiting
	case stateClaimed:
		if !hasEnded(row) {
			return "", ErrRunInFlight
		}
	case stateStopped, stateAbandoned:
	default:
		return "", fmt.Errorf("run is in state %q, which cannot be requeued", state)
	}
	if len(stringList(row, planFilesField)) > 0 && planStamped(row) {
		return statePlanned, nil
	}
	return stateQueued, nil
}

// planStamped reports whether row's plan stage settled: the stamp is written
// when the plan settles, so a zero time is a plan still to come.
func planStamped(row map[string]any) bool {
	at, ok := row[planSettledAtField].(time.Time)
	return ok && !at.IsZero()
}

// NeedsRequeue reports whether ticketID has a run that has ended — stopped,
// abandoned, or carrying a recorded outcome — so that only an operator's
// own Requeue takes the ticket further, never another delegation. A
// ticket with no run row at all has not ended.
func (q *Queue) NeedsRequeue(ctx context.Context, ticketID string) (bool, error) {
	snap, err := q.client.Collection(runsCollection).Doc(ticketID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading run %s: %w", ticketID, err)
	}
	_, err = requeueState(snap.Data())
	return err == nil, nil
}

// dropReservations removes the ledger reservations runID holds at attempt, in
// one write; a run that holds none is left alone.
func dropReservations(tx *firestore.Transaction, ledgerRef *firestore.DocumentRef, ledger ledgerDoc, runID string, attempt int) error {
	var updates []firestore.Update
	for _, key := range ledger.reservedKeys(runID, attempt) {
		updates = append(updates, firestore.Update{
			FieldPath: firestore.FieldPath{"reservations", key},
			Value:     firestore.Delete,
		})
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.Update(ledgerRef, updates)
}

// RecordVerdict writes the provider plan verdict onto run runID, touching no
// other field of its record.
func (q *Queue) RecordVerdict(ctx context.Context, runID string, verdict prov.Verdict) error {
	_, err := q.client.Collection(runsCollection).Doc(runID).Update(ctx, []firestore.Update{{Path: verdictField, Value: verdict}})
	if err != nil {
		return fmt.Errorf("recording the provider verdict on run %s: %w", runID, err)
	}
	return nil
}

// Settle removes the reservation runID holds at attempt from the
// ledger, once its record carries its actual cost (runner.Finalize), and in
// the same transaction moves N by what the settled run showed. It is a no-op
// when that attempt holds no reservation, so a record job that runs more than
// once settles exactly once, and a late job never reaches a later attempt's
// entry (adr/0003).
//
// A nil attempt is a run claimed before attempts were stamped: it settles the
// pre-attempt key.
//
// A plan-stage reservation, settled by SettlePlan, differs: the plan stage
// ran no build, so it says nothing about running at N and is not observed. Its
// reserved estimate accumulates onto the plan spend already on the run row — a
// run that plans again after being suspended counts every plan — since the plan
// job reports no summary; that figure is summed by the budget windows beside a
// run's own settlement, and the row moves to planned for its build to claim.
func (q *Queue) Settle(ctx context.Context, runID string, attempt *int) error {
	return q.settle(ctx, runID, attempt, dispatcher.StageBuild)
}

// SettlePlan settles the plan-stage reservation runID holds at attempt, the way
// Settle does a build's.
func (q *Queue) SettlePlan(ctx context.Context, runID string, attempt *int) error {
	return q.settle(ctx, runID, attempt, dispatcher.StagePlan)
}

func (q *Queue) settle(ctx context.Context, runID string, attempt *int, stage string) error {
	ledgerRef := q.ledgerRef()
	rowRef := q.client.Collection(runsCollection).Doc(runID)
	key := legacyReservationKey(runID, stage)
	if attempt != nil {
		key = reservationKey(runID, *attempt, stage)
	}
	err := q.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		ledger, err := q.readLedger(tx, ledgerRef)
		if err != nil {
			return err
		}
		entry, has := ledger.Reservations[key]
		if !has {
			return nil
		}
		if entry.Stage == dispatcher.StagePlan {
			var row planRow
			snap, err := tx.Get(rowRef)
			if err != nil {
				return err
			}
			if err := snap.DataTo(&row); err != nil {
				return err
			}
			updates, ledgerUpdates := planSettle(key, entry, row)
			updates = append(updates, firestore.Update{Path: stateField, Value: statePlanned})
			if err := tx.Update(rowRef, updates); err != nil {
				return err
			}
			return tx.Update(ledgerRef, ledgerUpdates)
		}
		cur := ledger.concurrency()
		obs, err := q.observe(tx, rowRef, ledger)
		if err != nil {
			return err
		}
		platformCap := cmp.Or(ledger.PlatformCap, dispatcher.DefaultLimits.PlatformCap)
		next := dispatcher.NextConcurrency(cur, obs, ledger.tuning(), platformCap)
		changedAfter := ledger.ChangedAfter
		if next.N != cur.N {
			for _, r := range ledger.Reservations {
				if r.ClaimedAt.After(changedAfter) {
					changedAfter = r.ClaimedAt
				}
			}
		}
		return tx.Update(ledgerRef, []firestore.Update{
			{FieldPath: firestore.FieldPath{"reservations", key}, Value: firestore.Delete},
			{Path: "n", Value: next.N},
			{Path: "stable_runs", Value: next.Stable},
			{Path: "changed_after", Value: changedAfter},
		})
	})
	if err != nil {
		return fmt.Errorf("settling run %s: %w", runID, err)
	}
	return nil
}

// planRow is the row state a plan-stage reservation settles onto: the plan
// spend it accumulates, which a run that plans more than once (suspended, then
// resumed) sums rather than overwrites.
type planRow struct {
	PlanSpendMicros  int64 `firestore:"plan_spend_micros"`
	PlanSpendMinutes int64 `firestore:"plan_spend_minutes"`
}

// planSettle books a plan-stage reservation as settled: its reserved estimate
// accumulates onto the row's plan spend, and the ledger key is dropped. Settle
// and Suspend share it so the two cannot drift on what a settled plan costs.
func planSettle(key string, entry reservationEntry, row planRow) ([]firestore.Update, []firestore.Update) {
	rowUpdates := []firestore.Update{
		{Path: planSpendMicrosField, Value: row.PlanSpendMicros + int64(entry.ProviderCostMicros)},
		{Path: planSpendMinutesField, Value: row.PlanSpendMinutes + entry.RunnerMinutes},
	}
	ledgerUpdates := []firestore.Update{
		{FieldPath: firestore.FieldPath{"reservations", key}, Value: firestore.Delete},
	}
	return rowUpdates, ledgerUpdates
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
