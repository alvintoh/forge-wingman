package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
)

// Phase is how far a run got.
type Phase string

const (
	PhaseProjection Phase = "projection"
	PhaseWorktree   Phase = "worktree"
	PhasePlan       Phase = "plan"
	PhaseBuild      Phase = "build"
	// PhaseReview is FR-28's review pass, once the build's own checks pass:
	// a model other than the builder's checks the diff against the ticket's
	// acceptance criteria.
	PhaseReview Phase = "review"
	PhaseCommit Phase = "commit"
	PhasePR     Phase = "pr"
)

// Outcome is how a run ended.
type Outcome string

const (
	OutcomeBuilt        Outcome = "built"
	OutcomePROpened     Outcome = "pr-opened"
	OutcomeNoChanges    Outcome = "no-changes"
	OutcomeStopped      Outcome = "stopped"
	OutcomeAgentFailed  Outcome = "agent-failed"
	OutcomeInfraFailure Outcome = "infra-failure"
	// OutcomeBudgetStop is a run the provider itself stopped by exhausting an
	// allowance mid-build (FR-22). Kept distinct from OutcomeAgentFailed so it
	// never reads as a verification failure FR-13 would retry at a higher,
	// more expensive tier — the one thing an exhausted allowance must not do.
	OutcomeBudgetStop Outcome = "budget-stop"
)

// StopReason names the condition that ended a run short of a PR.
type StopReason string

const (
	StopProjectionMissing StopReason = "projection-missing"
	StopProjectionInvalid StopReason = "projection-invalid"
	StopProjectionRead    StopReason = "projection-read"
	StopWorktree          StopReason = "worktree"
	StopAgentExit         StopReason = "agent-exit"
	StopCompletions       StopReason = "completions-upload"
	StopCommit            StopReason = "commit"
	StopNoBuildRecord     StopReason = "no-build-record"
	StopModelInvalid      StopReason = "model-invalid"
	StopAgentTimeout      StopReason = "agent-timeout"
	StopGitTampered       StopReason = "git-tampered"
	StopHeadMoved         StopReason = "head-moved"
	StopSecretInBranch    StopReason = "secret-in-branch"
	StopPanic             StopReason = "panic"
	StopPRJob             StopReason = "pr-job"
	// StopRebaseConflict reports the run's commits conflicting with main at the
	// push: the build survives as its bundle artifact, but the branch cannot be
	// delivered as built.
	StopRebaseConflict    StopReason = "rebase-conflict"
	StopSummaryInvalid    StopReason = "summary-invalid"
	StopSummaryUnreadable StopReason = "summary-unreadable"
	StopSetup             StopReason = "setup"
	StopRecordMissing     StopReason = "record-missing"
	StopTicketMissing     StopReason = "ticket-missing"
	// StopTicketNotDelivered reports no ticket file where the build expected one —
	// a hand-off GitHub dropped or an artifact that never arrived — kept distinct
	// from StopTicketMissing, a record whose ticket cannot be built.
	StopTicketNotDelivered StopReason = "ticket-not-delivered"
	StopIdentityMismatch   StopReason = "identity-mismatch"
	// StopAllowanceExhausted is a provider allowance-exhaustion error surfaced
	// mid-build, recorded as a budget stop rather than an agent failure (FR-22).
	StopAllowanceExhausted StopReason = "allowance-exhausted"
	StopPlanInvalid        StopReason = "plan-invalid"
	// StopChecksRun reports the pre-PR loop's own checks (FR-28) failing to
	// run at all — never a gate that ran and failed, which does not stop the
	// build; see RunChecks.
	StopChecksRun StopReason = "checks-run"
	// StopReviewInvalid reports a review pass (FR-28) whose final message
	// carries no valid review-findings block.
	StopReviewInvalid StopReason = "review-invalid"
	// StopModelUnavailable reports every model in a starting model's
	// availability order failing as unavailable, so dispatch counts this as
	// an infra stop against the provider rather than an agent failure (AC3).
	StopModelUnavailable StopReason = "model-unavailable"
	// StopCredentialAbsent reports a model whose plan's harnesses were all
	// unready for want of their key.
	StopCredentialAbsent StopReason = "credential-absent"
	// StopWorkflowChange reports a plan or build touching a file under
	// .github/workflows/, which the run's GitHub App has no permission to push.
	StopWorkflowChange StopReason = "workflow-change"
)

// gates are the checks run.yml's check job — and RunChecks, in-job — run on
// the branch, in order.
var gates = []string{checkGofmt, checkVet, checkLint, checkTest}

// gateUnnamed is the failed gate of a check job that did not name one it runs.
const gateUnnamed = "check"

// ModelLabels are the models a ticket names for its phases in place of the
// run's defaults. An empty field names none; Step.Model records the model a
// phase actually used.
type ModelLabels struct {
	Build  string   `firestore:"build"`
	Review string   `firestore:"review"`
	Plan   []string `firestore:"plan"`
}

// Record is one run's entry in the run store.
type Record struct {
	RunID       string `firestore:"run_id"`
	TicketID    string `firestore:"ticket_id"`
	TicketTitle string `firestore:"ticket_title"`
	TicketBody  string `firestore:"ticket_body"`
	Size        string `firestore:"size"`
	SizedBy     string `firestore:"sized_by"`
	// Private is whether the run's target repository is private, resolved
	// once at admission and never re-queried (FR-22, NFR-1): its runner
	// minutes count toward the cash ceiling; a public repository's count as
	// zero.
	Private bool   `firestore:"private"`
	Phase   Phase  `firestore:"phase"`
	Steps   []Step `firestore:"steps"`
	Tokens  Usage  `firestore:"tokens"`
	// ProviderVerdict is the owner's verdict on the run's provider plan, never read from a summary.
	ProviderVerdict providers.Verdict `firestore:"provider_verdict"`
	// ModelLabels is what the ticket named at admission, never rewritten by the run.
	ModelLabels ModelLabels `firestore:"model_labels"`
	// Plan is the provider plan the run was claimed on.
	Plan string `firestore:"plan"`
	// LastResort is set when the run was claimed on the plan's free tier,
	// whose models LastResortModels names in place of ModelLabels.
	LastResort       bool        `firestore:"last_resort"`
	LastResortModels ModelLabels `firestore:"last_resort_models"`
	// SettledAt, SettledProviderCostMicros and SettledRunnerMinutes are
	// written once, by Finalize: the run's actual cost, settled against the
	// dispatch/ledger reservation the claim booked (adr/0003). Zero until
	// then; runner minutes are zero for a public target.
	SettledAt                 time.Time        `firestore:"settled_at"`
	SettledProviderCostMicros money.Micros     `firestore:"settled_provider_cost_micros"`
	SettledRunnerMinutes      int64            `firestore:"settled_runner_minutes"`
	DurationsMS               map[string]int64 `firestore:"durations_ms"`
	EditedFiles               []string         `firestore:"edited_files"`
	OutOfPlanFiles            []string         `firestore:"out_of_plan_files"`
	DiffLines                 DiffLines        `firestore:"diff_lines"`
	Branch                    string           `firestore:"branch"`
	BuildOutcome              Outcome          `firestore:"build_outcome"`
	Outcome                   Outcome          `firestore:"outcome"`
	StopReason                StopReason       `firestore:"stop_reason"`
	StopDetail                string           `firestore:"stop_detail"`
	FailedGate                string           `firestore:"failed_gate"`
	// Ready is FR-5's draft-vs-ready decision: true only when the pre-PR
	// loop's checks passed and the review found nothing left open.
	Ready      bool   `firestore:"ready"`
	LoopDetail string `firestore:"loop_detail"`
	// Sampled is whether the run id falls in FR-17's review sample, whatever the run's size.
	Sampled bool `firestore:"sampled"`
	// AutoMerge is whether the pr job marked the PR eligible for auto-merge (FR-16).
	AutoMerge bool `firestore:"auto_merge"`
	// CommitSubject is the subject the build committed with, and the PR's title.
	CommitSubject string `firestore:"commit_subject"`
	// CommitBody is the commit message after its subject.
	CommitBody string `firestore:"commit_body"`
	// PRSummary is the PR's one-line summary.
	PRSummary    string            `firestore:"pr_summary"`
	UsageWarning string            `firestore:"usage_warning"`
	RuleStackSHA string            `firestore:"rule_stack_sha"`
	PRURL        string            `firestore:"pr_url"`
	JobResults   map[string]string `firestore:"job_results"`
	StartedAt    time.Time         `firestore:"started_at"`
	UpdatedAt    time.Time         `firestore:"updated_at"`
	// RunURL is the GitHub Actions run that recorded the outcome.
	RunURL string `firestore:"run_url"`
}

// DiffLines is the size of the branch's diff against its base.
type DiffLines struct {
	Added   int64 `firestore:"added" json:"added"`
	Removed int64 `firestore:"removed" json:"removed"`
}

// ErrRecordNotFound is what a RecordReader returns for an absent record.
var ErrRecordNotFound = errors.New("run record not found")

// RecordReader reads run records by id.
type RecordReader interface {
	GetRecord(ctx context.Context, id string) (Record, error)
}

// RecordStore reads run records and writes a Record's fields into them by id.
type RecordStore interface {
	RecordReader
	PutRecord(ctx context.Context, id string, r Record) error
}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ValidRunID reports whether id can name a run record.
func ValidRunID(id string) bool {
	return runIDPattern.MatchString(id)
}

// Ticket is the ticket the record names.
func (r Record) Ticket() Ticket {
	return Ticket{ID: r.TicketID, Title: r.TicketTitle, Size: r.Size, SizedBy: r.SizedBy, Body: r.TicketBody}
}

// ReadRun returns run runID's record. A missing record or an unbuildable ticket
// is a StopError; the record is returned with it as read.
func ReadRun(ctx context.Context, r RecordReader, runID string) (Record, error) {
	rec, err := r.GetRecord(ctx, runID)
	if errors.Is(err, ErrRecordNotFound) {
		return Record{}, stopWith(OutcomeStopped, StopRecordMissing, err)
	}
	if err != nil {
		return Record{}, fmt.Errorf("reading record %s: %w", runID, err)
	}
	if err := rec.Ticket().Validate(); err != nil {
		return rec, stopWith(OutcomeStopped, StopTicketMissing, err)
	}
	return rec, nil
}

// FailedGate names the gate a check job reported failing: "" for a check that
// was not run or reported "none", gateUnnamed for any report that is not a gate.
func FailedGate(reported string) string {
	if reported == "" || reported == "none" {
		return ""
	}
	if slices.Contains(gates, reported) {
		return reported
	}
	return gateUnnamed
}

// ObjectCreator creates objects in the completions bucket, failing if one exists.
type ObjectCreator interface {
	CreateObject(ctx context.Context, name string, r io.Reader) error
}

// Ledger settles a run's dispatch/ledger reservation once its actual cost is
// known, dropping its entry from the in-flight total FR-22 admission reads
// (adr/0003). Settle is idempotent: a run holding no reservation settles as a
// no-op, so a record job that runs more than once for the same run settles it
// exactly once.
type Ledger interface {
	Settle(ctx context.Context, runID string) error
}

// msPerMinute is how many milliseconds GitHub Actions bills as one minute.
const msPerMinute = 60_000

// BillableMinutes approximates a run's own GitHub Actions minutes from its
// timed phases (projection, worktree, build, commit, pr) — not the checks,
// whose time those phases already hold — rounding up since GitHub bills whole
// minutes. The true billed figure also includes checkout and job setup this
// run does not time, and is only knowable from GitHub's own API once the whole
// workflow has finished — which is after the record job itself runs — so this
// is a deliberate estimate of it (FR-22), not the billed truth.
func BillableMinutes(durationsMS map[string]int64) int64 {
	var totalMS int64
	for p, ms := range durationsMS {
		if p != checksDuration {
			totalMS += ms
		}
	}
	if totalMS <= 0 {
		return 0
	}
	return (totalMS + msPerMinute - 1) / msPerMinute
}

// NewRecord is the record the dispatcher starts a run with: the ticket as read
// from Linear and nothing derived from a build yet.
func NewRecord(id string, t Ticket, now time.Time) Record {
	return Record{
		RunID:       id,
		TicketID:    t.ID,
		TicketTitle: t.Title,
		TicketBody:  t.Body,
		Size:        t.Size,
		SizedBy:     t.SizedBy,
		DurationsMS: map[string]int64{},
		JobResults:  map[string]string{},
		StartedAt:   now,
	}
}

// FinalizeInput is what the workflow knows once every job has finished.
type FinalizeInput struct {
	RunID string
	// Identity is the record job's own, checked whatever the other jobs did.
	Identity Identity
	// AttemptID is the "<run>-<attempt>" the build named its branch and completions by.
	AttemptID string
	Summary   string
	// SummaryUnreadable is set when the summary could not be passed to the record job.
	SummaryUnreadable bool
	PRURL             string
	// RunResult is the model job's result; success means it finished and reported a
	// summary, whatever the build's outcome.
	RunResult string
	PRResult  string
	// PRStopReason is the pr job's own report of why the push could not be made,
	// empty when it made it.
	PRStopReason string
	PRDuration   time.Duration
	// AutoMerge is whether the pr job marked the PR eligible for auto-merge.
	AutoMerge bool
	// CheckReport is the check job's failed_gate output, or empty when it did not run.
	CheckReport string
	RunURL      string
}

// Finalize writes the run's outcome into its record from the record's ticket, the
// build's summary and the PR job's result, keeping the record's start time. An
// identity mismatch is recorded as the stop ahead of anything else, then a missing
// record or ticket; a missing, invalid or unreadable summary as an infra failure.
// Running it again with a later result replaces the earlier derivation.
//
// Every path through Finalize writes a terminal outcome, so every call
// settles the run's ledger reservation to its actual cost (adr/0003) — even a
// repeated call for the same run, since Settle is idempotent.
func Finalize(ctx context.Context, store RecordStore, ledger Ledger, in FinalizeInput, now time.Time) (Record, error) {
	existing, err := ReadRun(ctx, store, in.RunID)
	var stopped *StopError
	if err != nil && !errors.As(err, &stopped) {
		return Record{}, err
	}
	t := existing.Ticket()
	started := existing.StartedAt
	if started.IsZero() {
		started = now
	}
	rec := NewRecord(in.RunID, t, started)
	rec.Private = existing.Private
	rec.ProviderVerdict = existing.ProviderVerdict
	rec.ModelLabels = existing.ModelLabels
	rec.Plan, rec.LastResort, rec.LastResortModels = existing.Plan, existing.LastResort, existing.LastResortModels
	identityErr := in.Identity.CheckAccount()
	sum, err := ParseSummary(in.Summary, in.AttemptID, t, now)
	switch {
	case identityErr != nil:
		rec.BuildOutcome, rec.Outcome, rec.StopReason = OutcomeStopped, OutcomeStopped, StopIdentityMismatch
		rec.StopDetail = truncate(identityErr.Error(), stopDetailLimit)
	case stopped != nil:
		rec.BuildOutcome, rec.Outcome, rec.StopReason = stopped.Outcome, stopped.Outcome, stopped.Reason
		rec.StopDetail = truncate(stopped.Err.Error(), stopDetailLimit)
	case in.SummaryUnreadable:
		rec.BuildOutcome, rec.Outcome, rec.StopReason = OutcomeInfraFailure, OutcomeInfraFailure, StopSummaryUnreadable
	case errors.Is(err, ErrSummaryMissing):
		rec.BuildOutcome, rec.Outcome, rec.StopReason = OutcomeInfraFailure, OutcomeInfraFailure, StopNoBuildRecord
	case err != nil:
		rec.BuildOutcome, rec.Outcome, rec.StopReason = OutcomeInfraFailure, OutcomeInfraFailure, StopSummaryInvalid
		rec.StopDetail = truncate(err.Error(), stopDetailLimit)
	default:
		sum.apply(&rec)
		if rec.BuildOutcome == OutcomeBuilt {
			if in.PRResult == "success" && in.PRURL != "" {
				rec.Outcome = OutcomePROpened
			} else {
				reason := StopPRJob
				if in.PRStopReason == string(StopRebaseConflict) {
					reason = StopRebaseConflict
				}
				rec.Outcome, rec.StopReason = OutcomeInfraFailure, reason
			}
		}
	}
	rec.JobResults["run"] = in.RunResult
	rec.JobResults["pr"] = in.PRResult
	if in.PRURL != "" {
		rec.PRURL = in.PRURL
		rec.Phase = PhasePR
	}
	rec.FailedGate = FailedGate(in.CheckReport)
	rec.Sampled, rec.AutoMerge = Sampled(in.RunID), in.AutoMerge
	rec.RunURL = in.RunURL
	if in.PRDuration > 0 {
		rec.DurationsMS[string(PhasePR)] = in.PRDuration.Milliseconds()
	}
	rec.UpdatedAt = now
	// Settle only a run that reached an agent — one with no Steps never had
	// the chance to incur cost (identity/ticket/projection stops all happen
	// before any agent runs), and marking it settled anyway would enter the
	// estimator's mean as a genuine zero-cost sample, silently pulling every
	// future estimate of that size down (FR-22).
	if len(rec.Steps) > 0 {
		rec.SettledAt = now
		rec.SettledProviderCostMicros = money.FromUSD(rec.Tokens.Cost)
	}
	if rec.Private {
		rec.SettledRunnerMinutes = BillableMinutes(rec.DurationsMS)
	}
	if err := store.PutRecord(ctx, in.RunID, rec); err != nil {
		return rec, fmt.Errorf("writing record %s: %w", in.RunID, err)
	}
	// The record write and the ledger settle are two separate Firestore
	// writes, not one transaction: if this fails after the record above
	// already landed, the run stays double-counted (both reserved and
	// settled) against every budget ceiling until it settles. Both writes
	// are idempotent, so rerunning this job retries the settle safely —
	// state that explicitly, since this failure otherwise reads as an
	// ordinary infra error with no obvious fix.
	if err := ledger.Settle(ctx, in.RunID); err != nil {
		return rec, fmt.Errorf("settling run %s (rerun this job to retry — PutRecord and Settle are both idempotent): %w", in.RunID, err)
	}
	return rec, nil
}

// RunModels are the models the run's phases start on: the free tier's when it
// was claimed on one, else what the ticket named.
func (r Record) RunModels() ModelLabels {
	if r.LastResort {
		return r.LastResortModels
	}
	return r.ModelLabels
}

// Succeeded reports whether a run ended where it should: a PR opened, or nothing to change.
func (r Record) Succeeded() bool {
	return r.Outcome == OutcomePROpened || r.Outcome == OutcomeNoChanges
}

// breakerStops are the systemic stops that halt every dispatch until an
// operator resets the breaker. An exhausted allowance is one only once the
// plan's free models have refused too.
var breakerStops = []StopReason{StopCredentialAbsent, StopIdentityMismatch, StopAllowanceExhausted}

// Systemic reports whether the run's stop is one the owner is told of, and
// whether it trips the dispatch breaker. A model outage notifies without
// tripping it, counting toward its provider's halt instead.
func (r Record) Systemic() (notify, trips bool) {
	trips = slices.Contains(breakerStops, r.StopReason)
	return trips || r.StopReason == StopModelUnavailable, trips
}

func (s Summary) apply(rec *Record) {
	rec.BuildOutcome, rec.Outcome = s.Outcome, s.Outcome
	rec.StopReason, rec.StopDetail = s.StopReason, s.StopDetail
	rec.Phase = s.Phase
	rec.Steps = s.Steps
	rec.Tokens = sumSteps(s.Steps)
	rec.EditedFiles = s.EditedFiles
	rec.OutOfPlanFiles = s.OutOfPlanFiles
	rec.DiffLines = s.DiffLines
	for p, ms := range s.DurationsMS {
		rec.DurationsMS[p] = ms
	}
	rec.RuleStackSHA = s.RuleStackSHA
	rec.Branch = s.Branch
	rec.UsageWarning = s.UsageWarning
	rec.Ready = s.Ready
	rec.LoopDetail = s.LoopDetail
	rec.CommitSubject, rec.CommitBody, rec.PRSummary = s.CommitSubject, s.CommitBody, s.PRSummary
}

// sumSteps totals every step's tokens into one run-wide Usage.
func sumSteps(steps []Step) Usage {
	var u Usage
	for _, st := range steps {
		u.Input += st.Tokens.Input
		u.Output += st.Tokens.Output
		u.Reasoning += st.Tokens.Reasoning
		u.CacheRead += st.Tokens.CacheRead
		u.CacheWrite += st.Tokens.CacheWrite
		u.Cost += st.Tokens.Cost
		u.Steps += st.Tokens.Steps
	}
	return u
}
