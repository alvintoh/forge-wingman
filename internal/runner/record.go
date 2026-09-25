package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
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
)

// ErrRecordNotFound is what a RecordStore returns for an absent record.
var ErrRecordNotFound = errors.New("run record not found")

// Record is one run's entry in the run store.
type Record struct {
	RunID             string            `firestore:"run_id"`
	TicketID          string            `firestore:"ticket_id"`
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
	Added   int64 `firestore:"added"`
	Removed int64 `firestore:"removed"`
}

// RecordStore reads and writes run records by id.
type RecordStore interface {
	GetRecord(ctx context.Context, id string) (Record, error)
	PutRecord(ctx context.Context, id string, r Record) error
}

// ObjectCreator creates objects in the completions bucket, failing if one exists.
type ObjectCreator interface {
	CreateObject(ctx context.Context, name string, r io.Reader) error
}

func newRecord(id string, t Ticket, now time.Time) Record {
	return Record{
		RunID:       id,
		TicketID:    t.ID,
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
	RecordID   string
	Ticket     Ticket
	PRURL      string
	RunResult  string
	PRResult   string
	PRDuration time.Duration
}

// Finalize derives the final outcome from the build's own outcome and the PR
// job's result, or writes an infra-failure record when the build never wrote one.
// Running it again with a later result replaces the earlier derivation.
func Finalize(ctx context.Context, store RecordStore, in FinalizeInput, now time.Time) (Record, error) {
	rec, err := store.GetRecord(ctx, in.RecordID)
	switch {
	case errors.Is(err, ErrRecordNotFound):
		rec = newRecord(in.RecordID, in.Ticket, now)
		rec.BuildOutcome = OutcomeInfraFailure
		rec.Outcome = OutcomeInfraFailure
		rec.StopReason = StopNoBuildRecord
	case err != nil:
		return Record{}, fmt.Errorf("reading record %s: %w", in.RecordID, err)
	case rec.BuildOutcome == OutcomeBuilt:
		if in.PRResult == "success" && in.PRURL != "" {
			rec.Outcome, rec.StopReason = OutcomePROpened, ""
		} else {
			rec.Outcome, rec.StopReason = OutcomeInfraFailure, StopPRJob
		}
	}
	if rec.JobResults == nil {
		rec.JobResults = map[string]string{}
	}
	rec.JobResults["run"] = in.RunResult
	rec.JobResults["pr"] = in.PRResult
	if in.PRURL != "" {
		rec.PRURL = in.PRURL
		rec.Phase = PhasePR
	}
	if in.PRDuration > 0 {
		if rec.DurationsMS == nil {
			rec.DurationsMS = map[string]int64{}
		}
		rec.DurationsMS[string(PhasePR)] = in.PRDuration.Milliseconds()
	}
	rec.UpdatedAt = now
	if err := store.PutRecord(ctx, in.RecordID, rec); err != nil {
		return rec, fmt.Errorf("writing record %s: %w", in.RecordID, err)
	}
	return rec, nil
}
