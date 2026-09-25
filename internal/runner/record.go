package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"time"
)

// Phase is how far a run got.
type Phase string

const (
	PhaseProjection Phase = "projection"
	PhaseWorktree   Phase = "worktree"
	PhaseBuild      Phase = "build"
	PhaseCommit     Phase = "commit"
	PhasePR         Phase = "pr"
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
	StopSummaryInvalid    StopReason = "summary-invalid"
	StopSummaryUnreadable StopReason = "summary-unreadable"
	StopSetup             StopReason = "setup"
	StopRecordMissing     StopReason = "record-missing"
	StopTicketMissing     StopReason = "ticket-missing"
	StopIdentityMismatch  StopReason = "identity-mismatch"
)

// gates are the checks run.yml's check job runs on the branch, in order.
var gates = []string{"gofmt", "vet", "golangci-lint", "test"}

// gateUnnamed is the failed gate of a check job that did not name one it runs.
const gateUnnamed = "check"

// Record is one run's entry in the run store.
type Record struct {
	RunID             string            `firestore:"run_id"`
	TicketID          string            `firestore:"ticket_id"`
	TicketTitle       string            `firestore:"ticket_title"`
	TicketBody        string            `firestore:"ticket_body"`
	Size              string            `firestore:"size"`
	SizedBy           string            `firestore:"sized_by"`
	Phase             Phase             `firestore:"phase"`
	Models            map[string]string `firestore:"models"`
	Tokens            Usage             `firestore:"tokens"`
	DurationsMS       map[string]int64  `firestore:"durations_ms"`
	EditedFiles       []string          `firestore:"edited_files"`
	DiffLines         DiffLines         `firestore:"diff_lines"`
	Branch            string            `firestore:"branch"`
	BuildOutcome      Outcome           `firestore:"build_outcome"`
	Outcome           Outcome           `firestore:"outcome"`
	StopReason        StopReason        `firestore:"stop_reason"`
	StopDetail        string            `firestore:"stop_detail"`
	FailedGate        string            `firestore:"failed_gate"`
	UsageWarning      string            `firestore:"usage_warning"`
	RuleStackSHA      string            `firestore:"rule_stack_sha"`
	CompletionsObject string            `firestore:"completions_object"`
	PRURL             string            `firestore:"pr_url"`
	JobResults        map[string]string `firestore:"job_results"`
	StartedAt         time.Time         `firestore:"started_at"`
	UpdatedAt         time.Time         `firestore:"updated_at"`
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

// RecordCreator writes a run record, failing if one exists.
type RecordCreator interface {
	CreateRecord(ctx context.Context, id string, r Record) error
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

// Seed writes a new run record for ticket t.
//
// TODO(FRG-18): the dispatcher writes run records; retire Seed with it.
func Seed(ctx context.Context, c RecordCreator, runID string, t Ticket, now time.Time) error {
	if !ValidRunID(runID) {
		return fmt.Errorf("run id %q cannot name a record", truncate(runID, logErrorLimit))
	}
	if err := t.Validate(); err != nil {
		return err
	}
	rec := newRecord(runID, t, now)
	rec.UpdatedAt = now
	if err := c.CreateRecord(ctx, runID, rec); err != nil {
		return fmt.Errorf("creating record %s: %w", runID, err)
	}
	return nil
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

func newRecord(id string, t Ticket, now time.Time) Record {
	return Record{
		RunID:       id,
		TicketID:    t.ID,
		TicketTitle: t.Title,
		TicketBody:  t.Body,
		Size:        t.Size,
		SizedBy:     t.SizedBy,
		Models:      map[string]string{},
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
	RunResult  string
	PRResult   string
	PRDuration time.Duration
	// CheckReport is the check job's failed_gate output, or empty when it did not run.
	CheckReport string
}

// Finalize writes the run's outcome into its record from the record's ticket, the
// build's summary and the PR job's result, keeping the record's start time. An
// identity mismatch is recorded as the stop ahead of anything else, then a missing
// record or ticket; a missing, invalid or unreadable summary as an infra failure.
// Running it again with a later result replaces the earlier derivation.
func Finalize(ctx context.Context, store RecordStore, in FinalizeInput, now time.Time) (Record, error) {
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
	rec := newRecord(in.RunID, t, started)
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
				rec.Outcome, rec.StopReason = OutcomeInfraFailure, StopPRJob
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
	if in.PRDuration > 0 {
		rec.DurationsMS[string(PhasePR)] = in.PRDuration.Milliseconds()
	}
	rec.UpdatedAt = now
	if err := store.PutRecord(ctx, in.RunID, rec); err != nil {
		return rec, fmt.Errorf("writing record %s: %w", in.RunID, err)
	}
	return rec, nil
}

// Succeeded reports whether a run ended where it should: a PR opened, or nothing to change.
func (r Record) Succeeded() bool {
	return r.Outcome == OutcomePROpened || r.Outcome == OutcomeNoChanges
}

func (s Summary) apply(rec *Record) {
	rec.BuildOutcome, rec.Outcome = s.Outcome, s.Outcome
	rec.StopReason, rec.StopDetail = s.StopReason, s.StopDetail
	rec.Phase = s.Phase
	if s.Model != "" {
		rec.Models[string(PhaseBuild)] = s.Model
	}
	rec.Tokens = s.Tokens
	rec.EditedFiles = s.EditedFiles
	rec.DiffLines = s.DiffLines
	rec.CompletionsObject = s.CompletionsObject
	for p, ms := range s.DurationsMS {
		rec.DurationsMS[p] = ms
	}
	rec.RuleStackSHA = s.RuleStackSHA
	rec.Branch = s.Branch
	rec.UsageWarning = s.UsageWarning
}
