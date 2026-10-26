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
	PhaseAgentStart Phase = "agent-start"
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
	StopAgentContainer    StopReason = "agent-container"
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
	// StopRunBudget reports a stage whose own share of NFR-1's run-duration
	// cap was already spent, so it started no round.
	StopRunBudget StopReason = "run-budget"
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
	// StopPlanStageFailed reports a plan stage that stopped before writing its
	// artifact, so the trusted record job found nothing to record and ended the
	// run instead of leaving it holding its plan reservation.
	StopPlanStageFailed StopReason = "plan-stage-failed"
	// StopWaitingOnOwner reports a run parked on an owner decision the owner
	// did not answer within the configured window, so the dispatcher stopped it
	// rather than hold it forever.
	StopWaitingOnOwner StopReason = "waiting-on-owner"
	// StopAgentNoProgress reports a build phase that completed
	// DEFAULT_NO_PROGRESS_STEPS requests in a row with no edit or write, so a
	// loop over reading is stopped by what the agent is doing rather than by
	// the wall clock alone.
	StopAgentNoProgress StopReason = "agent-no-progress"
)

// WaitReason names why a run's PR waits for the owner rather than merging itself.
type WaitReason string

// The reasons a PR waits for the owner. The inbox adds WaitAutoMergeOff from
// the live switch and WaitChecksNotGreen from GitHub's check state; the merge
// refuses on WaitChecksNotGreen, WaitDraft and WaitUnconfirmedDecision.
const (
	WaitAutoMergeOff        WaitReason = "auto-merge-off"
	WaitChecksNotGreen      WaitReason = "checks-not-green"
	WaitDraft               WaitReason = "draft"
	WaitFrontEnd            WaitReason = "front-end"
	WaitSampled             WaitReason = "sampled"
	WaitSizeL               WaitReason = "size-l"
	WaitUnconfirmedDecision WaitReason = "unconfirmed-decision"
)

// gates are the gates run.yml's check job runs on the branch, in order, and
// what FailedGate admits. It runs the Go gates alone: the web job's steps
// RunChecks runs when a run's diff touches webDir are the pre-PR loop's, ahead
// of the CI that would report them.
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
	FrontEnd    bool   `firestore:"front_end"`
	// Private is whether the run's target repository is private, resolved
	// once at admission and never re-queried (FR-22, NFR-1): its runner
	// minutes count toward the cash ceiling; a public repository's count as
	// zero.
	Private bool   `firestore:"private"`
	Phase   Phase  `firestore:"phase"`
	Steps   []Step `firestore:"steps"`
	Tokens  Usage  `firestore:"tokens"`
	// Cache is each phase's prompt-cache figures, keyed by phase: what its first
	// request read fresh and from cache, and the cached share of the phase's
	// whole input (AC1).
	Cache map[string]PhaseCache `firestore:"cache"`
	// ProviderVerdict is the owner's verdict on the run's provider plan, never read from a summary.
	ProviderVerdict providers.Verdict `firestore:"provider_verdict"`
	// ModelLabels is what the ticket named at admission, never rewritten by the run.
	ModelLabels ModelLabels `firestore:"model_labels"`
	// Plan is the provider plan the run was claimed on.
	Plan string `firestore:"plan"`
	// TimeOfUse is the run's own time-of-use bucket — "peak" or "off-peak"
	// for a run its provider prices by the hour, empty otherwise — written
	// once, by Finalize, from its steps' own times, so a provider's
	// time-of-use spend can be summed.
	TimeOfUse string `firestore:"time_of_use"`
	// CostDrift names how far the run's settled cost sits above its
	// provider's own trailing average, empty when it is within the drift
	// threshold or there is too little history to compare. Self-describing,
	// like UsageWarning, rather than a bare flag.
	CostDrift string `firestore:"cost_drift"`
	// CostDriftUnread reports that the provider's own history could not be
	// read, so the drift flag was left empty rather than set from a comparison
	// that never happened. Without it a broken read would disable every future
	// flag with nothing in the record or the log to say so.
	CostDriftUnread bool `firestore:"cost_drift_unread"`
	// LastResort is set when the run was claimed on the plan's free tier,
	// whose models LastResortModels names in place of ModelLabels.
	LastResort       bool        `firestore:"last_resort"`
	LastResortModels ModelLabels `firestore:"last_resort_models"`
	// MovedFrom names the paid plan whose ceiling bound when the claim moved the
	// run to Plan; PlanModels are then the models the run starts on.
	MovedFrom  string      `firestore:"moved_from"`
	PlanModels ModelLabels `firestore:"plan_models"`
	// SettledAt, SettledProviderCostMicros and SettledRunnerMinutes are
	// written once, by Finalize: the run's actual cost, settled against the
	// dispatch/ledger reservation the claim booked (adr/0003). Zero until
	// then; runner minutes are zero for a public target.
	SettledAt                 time.Time        `firestore:"settled_at"`
	SettledProviderCostMicros money.Micros     `firestore:"settled_provider_cost_micros"`
	SettledRunnerMinutes      int64            `firestore:"settled_runner_minutes"`
	DurationsMS               map[string]int64 `firestore:"durations_ms"`
	EditedFiles               []string         `firestore:"edited_files"`
	DiffLines                 DiffLines        `firestore:"diff_lines"`
	Branch                    string           `firestore:"branch"`
	Branches                  []string         `firestore:"branches"`
	BuildOutcome              Outcome          `firestore:"build_outcome"`
	Outcome                   Outcome          `firestore:"outcome"`
	StopReason                StopReason       `firestore:"stop_reason"`
	StopDetail                string           `firestore:"stop_detail"`
	// Transient is the class of the transient infrastructure fault the run
	// stopped on, empty when it stopped on none — which is what the dispatcher
	// requeues a stopped run on, and what keeps every other stop waiting.
	Transient  TransientClass `firestore:"transient"`
	FailedGate string         `firestore:"failed_gate"`
	// PriorSettledProviderCostMicros and PriorSettledRunnerMinutes are what the
	// run's earlier attempts settled, already counted in the settled totals above.
	PriorSettledProviderCostMicros money.Micros `firestore:"prior_settled_provider_cost_micros"`
	PriorSettledRunnerMinutes      int64        `firestore:"prior_settled_runner_minutes"`
	// Ready is FR-5's draft-vs-ready decision: true only when the pre-PR
	// loop's checks passed and the review found nothing left open.
	Ready      bool   `firestore:"ready"`
	LoopDetail string `firestore:"loop_detail"`
	// Sampled is whether the run id falls in FR-17's review sample, whatever the run's size.
	Sampled bool `firestore:"sampled"`
	// AutoMerge is whether the pr job marked the PR eligible for auto-merge (FR-16).
	AutoMerge bool `firestore:"auto_merge"`
	// WaitReason is why the PR waits for the owner, empty when none is named.
	WaitReason WaitReason `firestore:"wait_reason"`
	// MergedByOwnerAt and MergedSHA are when, and as what, the owner merged it.
	MergedByOwnerAt time.Time `firestore:"merged_by_owner_at"`
	MergedSHA       string    `firestore:"merged_sha"`
	// CancelledByOwnerAt and CancelledBy are when, and by which signed-in
	// address, the owner cancelled the run from its inbox row. The operator's
	// requeue clears them, since the run is live again.
	CancelledByOwnerAt time.Time `firestore:"cancelled_by_owner_at"`
	CancelledBy        string    `firestore:"cancelled_by"`
	// DecisionMode is how the run's plan decisions were settled when it was claimed, empty for a run claimed before it was recorded.
	DecisionMode DecisionMode `firestore:"decision_mode"`
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
	// ReadFiles is every repository file the agent's read tool calls touched,
	// and PeakContextTokens the largest context a single request read. A run
	// that reads its way over a large context shows here, which the token
	// totals alone cannot tell from slow work.
	ReadFiles         []string `firestore:"read_files"`
	PeakContextTokens int64    `firestore:"peak_context_tokens"`
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

// Ticket is the ticket the record names, carrying the plan's free models when
// the run was claimed on the free tier, which its ticket file hands the build
// (AC1, AC2).
func (r Record) Ticket() Ticket {
	t := Ticket{ID: r.TicketID, Title: r.TicketTitle, Size: r.Size, SizedBy: r.SizedBy, Body: r.TicketBody,
		FrontEnd: r.FrontEnd}
	if r.LastResort {
		t.FreeModels = slices.Clone(r.LastResortModels.Plan)
	}
	return t
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
// exactly once. A nil attempt is a run dispatched before the workflow carried
// one.
type Ledger interface {
	Settle(ctx context.Context, runID string, attempt *int) error
}

// ProviderCostReader reads a provider's own settled cost history, which
// Finalize compares a run's cost against. The build shares a machine with the
// model and cannot read the run store at all, so the comparison is the record
// job's to make.
type ProviderCostReader interface {
	// ProviderTrailingCost is the mean settled provider cost of plan's own
	// runs settled since since, and how many runs that mean covers.
	// excludeRunID is left out of both, so a run is never compared against
	// itself when its record job is rerun.
	ProviderTrailingCost(ctx context.Context, plan string, since time.Time, excludeRunID string) (money.Micros, int, error)
}

// TrailingWindow is how far back a provider's own history is measured: the
// drift baseline a run is flagged against, and the window the surface's own
// comparison reads over the same records.
const TrailingWindow = 30 * 24 * time.Hour

// TimeOfUsePeak and TimeOfUseOffPeak are the buckets a run its plan prices by
// the hour is tagged with, so its time-of-use spend can be summed.
const (
	TimeOfUsePeak    = "peak"
	TimeOfUseOffPeak = "off-peak"
)

const (
	// driftPercent is how far above its provider's trailing mean a run's own
	// settled cost must sit before it is flagged.
	driftPercent = 25
	// minDriftSample is how many of a provider's own settled runs the trailing
	// mean must cover before it can flag anything: a mean over one or two runs
	// says more about those runs than about the provider.
	minDriftSample = 5
)

// msPerMinute is how many milliseconds GitHub Actions bills as one minute.
const msPerMinute = 60_000

// BillableMinutes approximates a run's own GitHub Actions minutes from its
// timed phases (projection, worktree, agent-start, build, commit, pr) — not the
// checks, whose time those phases already hold — rounding up since GitHub bills
// whole minutes. The true billed figure also includes checkout and job setup this
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
		FrontEnd:    t.FrontEnd,
		DurationsMS: map[string]int64{},
		Cache:       map[string]PhaseCache{},
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
	// FailedJobLog is the failing jobs' own log tails, read by the record job:
	// a run's stop detail and loop detail are summaries, and the words that name
	// the transient fault are in the log the job itself printed. Empty when the
	// workflow handed the record job no token to read them with.
	FailedJobLog string
	RunURL       string
	// Attempt is the attempt the dispatcher claimed this run at, nil when the dispatch carried none.
	Attempt *int
}

// Finalize writes the run's outcome into its record from the record's ticket, the
// build's summary and the PR job's result, keeping the record's start time. An
// identity mismatch is recorded as the stop ahead of anything else, then a missing
// record or ticket; a missing, invalid or unreadable summary as an infra failure.
// Running it again with a later result replaces the earlier derivation.
//
// A run that reached an agent is also tagged with the time of use its own steps
// fell in, and flagged when its settled cost sits well above what reader reports
// its provider's runs usually cost — the provider's average lives in the store,
// which this record job holds and the build does not.
//
// Every path through Finalize writes a terminal outcome, so every call
// settles the run's ledger reservation to its actual cost (adr/0003) — even a
// repeated call for the same run, since Settle is idempotent.
func Finalize(ctx context.Context, store RecordStore, ledger Ledger, reader ProviderCostReader, in FinalizeInput, now time.Time) (Record, error) {
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
	rec.PriorSettledProviderCostMicros, rec.PriorSettledRunnerMinutes = existing.PriorSettledProviderCostMicros, existing.PriorSettledRunnerMinutes
	rec.MovedFrom, rec.PlanModels = existing.MovedFrom, existing.PlanModels
	rec.DecisionMode = existing.DecisionMode
	rec.WaitReason, rec.MergedByOwnerAt, rec.MergedSHA = existing.WaitReason, existing.MergedByOwnerAt, existing.MergedSHA
	rec.CancelledByOwnerAt, rec.CancelledBy = existing.CancelledByOwnerAt, existing.CancelledBy
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
	// Every attempt's branch is kept, this attempt's among them: the row's own
	// branch names only the latest attempt, and a merged PR is matched to its run
	// by the branch of whichever attempt opened it.
	rec.Branches = branchesWith(existing.Branches, rec.Branch)
	// The failing step's own words name a known transient fault when the run
	// stopped on one, which is what the dispatcher requeues on. The stop detail
	// and loop detail are summaries; the failing job's own log is read as well
	// only for a stop that means a job failed outside the code, so a log that
	// merely quotes a marker never classifies an agent or gate failure. Only a
	// stopped run is classified: a run that reached its PR carries the fault
	// that did not stop it, and holding it against the run would requeue a
	// delivered PR.
	if !rec.Succeeded() {
		words := rec.StopDetail + "\n" + rec.LoopDetail
		if ReadsJobLog(rec.StopReason) {
			words += "\n" + in.FailedJobLog
		}
		rec.Transient = ClassifyTransient(words)
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
		rec.TimeOfUse = timeOfUse(rec.Steps)
		rec.CostDrift, rec.CostDriftUnread = costDrift(ctx, reader, rec, now)
	}
	if rec.Private {
		rec.SettledRunnerMinutes = BillableMinutes(rec.DurationsMS)
	}
	rec.SettledProviderCostMicros += rec.PriorSettledProviderCostMicros
	rec.SettledRunnerMinutes += rec.PriorSettledRunnerMinutes
	if rec.SettledAt.IsZero() && (rec.PriorSettledProviderCostMicros > 0 || rec.PriorSettledRunnerMinutes > 0) {
		// A retry that never reached an agent still holds what the run's earlier
		// attempts settled, which the windows sum by this stamp.
		rec.SettledAt = now
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
	if err := ledger.Settle(ctx, in.RunID, in.Attempt); err != nil {
		return rec, fmt.Errorf("settling run %s (rerun this job to retry — PutRecord and Settle are both idempotent): %w", in.RunID, err)
	}
	return rec, nil
}

// branchesWith returns branches with branch added, in the order the attempts
// ran. A branch the list already holds is not added again, and an empty one is
// nothing to add: a run that ended before its build pushed none.
func branchesWith(branches []string, branch string) []string {
	if branch == "" || slices.Contains(branches, branch) {
		return branches
	}
	return append(branches, branch)
}

// timeOfUse is the run's own time-of-use bucket: "peak" when any of its steps
// ran inside a window its own plan prices at peak, "off-peak" when none did, and
// empty for a run no step of which its plan prices by the hour at all. A step
// counts by its whole span rather than its start instant, because its cost is
// metered per request at each event's own time — a step that begins before a
// peak window and runs into it was charged at peak for that part. A step whose
// time is unknown counts as peak, the direction Rates.At prices it.
func timeOfUse(steps []Step) string {
	hourly, peak := false, false
	for _, st := range steps {
		rates, ok := providers.RatesFor(st.Model)
		if !ok || rates.Peak == nil {
			continue
		}
		hourly = true
		if rates.Peak.Spans(st.At, time.Duration(st.DurationMS)*time.Millisecond) {
			peak = true
		}
	}
	switch {
	case !hourly:
		return ""
	case peak:
		return TimeOfUsePeak
	default:
		return TimeOfUseOffPeak
	}
}

// costDrift names how far the run's settled cost sits above its provider's own
// trailing average, empty when it is within the drift threshold, when too
// little of that provider's history has settled to average it, or when there is
// no reader to ask. A read that fails leaves the flag empty rather than failing
// Finalize: the flag annotates a run whose cost is already settled, and the
// record is the only place its outcome survives — an advisory comparison must
// not be able to cost it.
func costDrift(ctx context.Context, reader ProviderCostReader, rec Record, now time.Time) (string, bool) {
	if reader == nil || rec.Plan == "" {
		return "", false
	}
	mean, sample, err := reader.ProviderTrailingCost(ctx, rec.Plan, now.Add(-TrailingWindow), rec.RunID)
	if err != nil {
		return "", true
	}
	if sample < minDriftSample || mean <= 0 {
		// A mean of zero has no percentage to be above: the provider's own
		// history holds no cost to compare a run against.
		return "", false
	}
	cost := rec.SettledProviderCostMicros
	if cost*100 <= mean*(100+driftPercent) {
		return "", false
	}
	return fmt.Sprintf("cost $%.4f is %d%% above the $%.4f average of %d of this provider's runs",
		cost.USD(), (cost-mean)*100/mean, mean.USD(), sample), false
}

// RunModels are the models the run's phases start on: the free tier's when it
// was claimed on one, the plan's it moved to when it moved, else what the
// ticket named.
func (r Record) RunModels() ModelLabels {
	if r.LastResort {
		return r.LastResortModels
	}
	if r.MovedFrom != "" {
		return r.PlanModels
	}
	return r.ModelLabels
}

// PhaseModel is the one model the steps of phase ran on: empty when no step of
// the phase names a model, and when they name more than one, as after a fallback
// took over part way.
func PhaseModel(steps []Step, phase Phase) string {
	model := ""
	for _, step := range steps {
		if step.Phase != phase || step.Model == "" {
			continue
		}
		if model != "" && step.Model != model {
			return ""
		}
		model = step.Model
	}
	return model
}

// Succeeded reports whether a run ended where it should: a PR opened, or nothing to change.
func (r Record) Succeeded() bool {
	return r.Outcome == OutcomePROpened || r.Outcome == OutcomeNoChanges
}

// CachedShare is the share of the run's own input tokens that came from the
// provider's cache: zero for a run that read no input at all.
func (r Record) CachedShare() float64 { return CachedShare(r.Tokens.Input, r.Tokens.CacheRead) }

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
	rec.ReadFiles = s.ReadFiles
	rec.PeakContextTokens = s.PeakContextTokens
	rec.DiffLines = s.DiffLines
	for p, ms := range s.DurationsMS {
		rec.DurationsMS[p] = ms
	}
	for p, c := range s.Cache {
		rec.Cache[p] = c
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
