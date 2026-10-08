package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// maxSummaryBytes keeps the summary under Linux's 128 KiB limit on one argument
	// or environment string, which is how the record job receives it.
	maxSummaryBytes  = 64 << 10
	maxEditedFiles   = 200
	maxPathBytes     = 512
	maxModelBytes    = 200
	maxPhaseDuration = 24 * time.Hour
	maxCount         = 1 << 40
	maxCost          = 1e6
	maxSummaryAge    = 7 * 24 * time.Hour
	maxClockSkew     = 5 * time.Minute
)

// ErrSummaryMissing reports a record job that received no build summary.
var ErrSummaryMissing = errors.New("no build summary")

// ErrSummaryUnreported marks a build whose summary did not reach the record job.
var ErrSummaryUnreported = errors.New("summary not reported")

// Summary is what a build reports for the record job to write, since the build's
// own identity cannot write the run store.
type Summary struct {
	Outcome        Outcome          `json:"outcome"`
	StopReason     StopReason       `json:"stop_reason,omitempty"`
	StopDetail     string           `json:"stop_detail,omitempty"`
	Phase          Phase            `json:"phase"`
	Steps          []Step           `json:"steps,omitempty"`
	EditedFiles    []string         `json:"edited_files,omitempty"`
	OutOfPlanFiles []string         `json:"out_of_plan_files,omitempty"`
	DiffLines      DiffLines        `json:"diff_lines"`
	DurationsMS    map[string]int64 `json:"durations_ms,omitempty"`
	RuleStackSHA   string           `json:"rule_stack_sha,omitempty"`
	Branch         string           `json:"branch,omitempty"`
	UsageWarning   string           `json:"usage_warning,omitempty"`
	// Ready is FR-5's draft-vs-ready decision, from the pre-PR loop (FR-28):
	// true only when the checks passed and the review found nothing open.
	Ready      bool   `json:"ready"`
	LoopDetail string `json:"loop_detail,omitempty"`
	// CommitSubject is the subject the build committed with, and the PR's title.
	CommitSubject string `json:"commit_subject,omitempty"`
	// CommitBody is the commit message after its subject.
	CommitBody string `json:"commit_body,omitempty"`
	// PRSummary is the PR's one-line summary.
	PRSummary string    `json:"pr_summary,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// Step is one agent invocation in a run's build, distinct from Usage.Steps,
// which counts that invocation's own internal step_finish events.
type Step struct {
	Phase Phase  `firestore:"phase" json:"phase"`
	Round int    `firestore:"round" json:"round"`
	Model string `firestore:"model" json:"model"`
	// Harness is the agent CLI this step ran through, from the plan its model
	// names; empty for a step no harness ran.
	Harness string `firestore:"harness" json:"harness,omitempty"`
	// Detail names what drove this round (FR-6): the failing gate for a
	// check-rebuild round, or the review's findings for the fix round; empty
	// for the initial build round and for the review round itself.
	Detail            string `firestore:"detail" json:"detail,omitempty"`
	Tokens            Usage  `firestore:"tokens" json:"tokens"`
	DurationMS        int64  `firestore:"duration_ms" json:"duration_ms"`
	CompletionsObject string `firestore:"completions_object" json:"completions_object"`
}

type ending struct {
	outcome Outcome
	reason  StopReason
	phase   Phase
}

// buildEndings are the outcome, stop reason and phase combinations a build reports.
var buildEndings = func() map[ending]bool {
	m := map[ending]bool{
		{OutcomeInfraFailure, StopSetup, ""}:                       true,
		{OutcomeStopped, StopIdentityMismatch, ""}:                 true,
		{OutcomeStopped, StopTicketMissing, ""}:                    true,
		{OutcomeStopped, StopTicketNotDelivered, ""}:               true,
		{OutcomeStopped, StopModelInvalid, PhaseProjection}:        true,
		{OutcomeStopped, StopCredentialAbsent, PhaseProjection}:    true,
		{OutcomeStopped, StopProjectionMissing, PhaseProjection}:   true,
		{OutcomeStopped, StopProjectionInvalid, PhaseProjection}:   true,
		{OutcomeInfraFailure, StopProjectionRead, PhaseProjection}: true,
		{OutcomeInfraFailure, StopWorktree, PhaseWorktree}:         true,
		{OutcomeStopped, StopProjectionMissing, PhasePlan}:         true,
		{OutcomeStopped, StopProjectionInvalid, PhasePlan}:         true,
		{OutcomeInfraFailure, StopProjectionRead, PhasePlan}:       true,
		{OutcomeInfraFailure, StopCompletions, PhasePlan}:          true,
		{OutcomeAgentFailed, StopAgentTimeout, PhasePlan}:          true,
		{OutcomeAgentFailed, StopAgentExit, PhasePlan}:             true,
		{OutcomeStopped, StopPlanInvalid, PhasePlan}:               true,
		{OutcomeBudgetStop, StopAllowanceExhausted, PhasePlan}:     true,
		{OutcomeInfraFailure, StopModelUnavailable, PhasePlan}:     true,
		{OutcomeInfraFailure, StopCompletions, PhaseBuild}:         true,
		{OutcomeAgentFailed, StopAgentTimeout, PhaseBuild}:         true,
		{OutcomeAgentFailed, StopAgentExit, PhaseBuild}:            true,
		{OutcomeBudgetStop, StopAllowanceExhausted, PhaseBuild}:    true,
		{OutcomeInfraFailure, StopModelUnavailable, PhaseBuild}:    true,
		{OutcomeInfraFailure, StopChecksRun, PhaseBuild}:           true,
		// A review-phase failure never ends a build at PhaseReview: it forces
		// a draft and the build proceeds to PhaseCommit instead (FR-5) — only
		// a panic mid-review, below, can end there.
		{OutcomeStopped, StopGitTampered, PhaseCommit}:    true,
		{OutcomeStopped, StopHeadMoved, PhaseCommit}:      true,
		{OutcomeInfraFailure, StopCommit, PhaseCommit}:    true,
		{OutcomeStopped, StopSecretInBranch, PhaseCommit}: true,
		{OutcomeNoChanges, "", PhaseCommit}:               true,
		{OutcomeBuilt, "", PhaseCommit}:                   true,
	}
	for _, p := range []Phase{PhaseProjection, PhaseWorktree, PhasePlan, PhaseBuild, PhaseReview, PhaseCommit} {
		m[ending{OutcomeInfraFailure, StopPanic, p}] = true
	}
	return m
}()

var buildPhases = map[Phase]bool{
	PhaseProjection: true, PhaseWorktree: true, PhasePlan: true, PhaseBuild: true, PhaseReview: true, PhaseCommit: true,
}

// SetupSummary is the summary of a build that failed before it could start.
func SetupSummary(err error, now time.Time) Summary {
	return Summary{
		Outcome:    OutcomeInfraFailure,
		StopReason: StopSetup,
		StopDetail: truncate(err.Error(), stopDetailLimit),
		StartedAt:  now,
	}
}

// StoppedSummary is the summary of a build that stopped before it could start
// for reason.
func StoppedSummary(reason StopReason, err error, now time.Time) Summary {
	return Summary{
		Outcome:    OutcomeStopped,
		StopReason: reason,
		StopDetail: truncate(err.Error(), stopDetailLimit),
		StartedAt:  now,
	}
}

// Encode renders the summary as one line of JSON within maxSummaryBytes: first
// dropping edited and out-of-plan file names from the end, then, once none are
// left, dropping the oldest steps until it fits.
func (s Summary) Encode() (string, error) {
	s.EditedFiles = capFiles(s.EditedFiles)
	s.OutOfPlanFiles = capFiles(s.OutOfPlanFiles)
	for {
		b, err := json.Marshal(s)
		if err != nil {
			return "", err
		}
		if len(b) <= maxSummaryBytes {
			return string(b), nil
		}
		switch {
		case len(s.EditedFiles) > 0:
			s.EditedFiles = s.EditedFiles[:len(s.EditedFiles)/2]
			s.OutOfPlanFiles = s.OutOfPlanFiles[:len(s.OutOfPlanFiles)/2]
		case len(s.Steps) > 0:
			s.Steps = s.Steps[1:]
		default:
			return "", fmt.Errorf("summary is %d bytes, over %d", len(b), maxSummaryBytes)
		}
	}
}

func capFiles(files []string) []string {
	if len(files) > maxEditedFiles {
		files = files[:maxEditedFiles]
	}
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = truncate(f, maxPathBytes)
	}
	return out
}

// ParseSummary decodes and validates a summary written by attempt attemptID's build
// of ticket t. The build shares a machine with the model, so every field is checked
// against what this run could have produced.
func ParseSummary(raw, attemptID string, t Ticket, now time.Time) (Summary, error) {
	if raw == "" {
		return Summary{}, ErrSummaryMissing
	}
	if len(raw) > maxSummaryBytes {
		return Summary{}, fmt.Errorf("summary is %d bytes, over %d", len(raw), maxSummaryBytes)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var s Summary
	if err := dec.Decode(&s); err != nil {
		return Summary{}, fmt.Errorf("decoding summary: %w", err)
	}
	if dec.More() {
		return Summary{}, errors.New("trailing data after summary")
	}
	if err := s.validate(attemptID, t, now); err != nil {
		return Summary{}, err
	}
	s.StopDetail = truncate(s.StopDetail, stopDetailLimit)
	s.UsageWarning = truncate(s.UsageWarning, stopDetailLimit)
	s.LoopDetail = truncate(s.LoopDetail, stopDetailLimit)
	s.CommitSubject = truncate(s.CommitSubject, stopDetailLimit)
	s.CommitBody = truncate(s.CommitBody, maxTicketBodyBytes)
	s.PRSummary = truncate(s.PRSummary, stopDetailLimit)
	s.EditedFiles = capFiles(s.EditedFiles)
	s.OutOfPlanFiles = capFiles(s.OutOfPlanFiles)
	return s, nil
}

func (s Summary) validate(attemptID string, t Ticket, now time.Time) error {
	if !buildEndings[ending{s.Outcome, s.StopReason, s.Phase}] {
		return fmt.Errorf("outcome %q, stop reason %q at phase %q is not a build ending",
			truncate(string(s.Outcome), logErrorLimit), truncate(string(s.StopReason), logErrorLimit),
			truncate(string(s.Phase), logErrorLimit))
	}
	// A build names its branch once the worktree exists and gains a step for
	// whichever phase's agent ran once that agent's events are uploaded, so an
	// ending past each point that lacks the field did not come from a build.
	if (s.Phase == PhasePlan || s.Phase == PhaseBuild || s.Phase == PhaseReview || s.Phase == PhaseCommit) && s.Branch == "" {
		return errors.New("an ending past the worktree names no branch")
	}
	agentStep := s.Phase
	if s.Phase == PhaseCommit {
		agentStep = PhaseBuild
	}
	if s.Phase == PhaseCommit || s.Outcome == OutcomeAgentFailed || s.Outcome == OutcomeBudgetStop {
		if !hasStep(s.Steps, agentStep) {
			return errors.New("an ending past the worktree names no build step")
		}
	}
	for _, step := range s.Steps {
		if err := step.validate(attemptID); err != nil {
			return err
		}
	}
	for kind, files := range map[string][]string{"edited": s.EditedFiles, "out-of-plan": s.OutOfPlanFiles} {
		if len(files) > maxEditedFiles {
			return fmt.Errorf("%d %s files, over %d", len(files), kind, maxEditedFiles)
		}
		for _, f := range files {
			if !repoRelative(f) {
				return fmt.Errorf("%s file %q is not a path inside the repository", kind, truncate(f, logErrorLimit))
			}
		}
	}
	for _, n := range []int64{s.DiffLines.Added, s.DiffLines.Removed} {
		if n < 0 || n > maxCount {
			return fmt.Errorf("diff line count %d is out of range", n)
		}
	}
	for p, ms := range s.DurationsMS {
		if !buildPhases[Phase(p)] {
			return fmt.Errorf("duration for %q, which is not a build phase", truncate(p, logErrorLimit))
		}
		if ms < 0 || ms > maxPhaseDuration.Milliseconds() {
			return fmt.Errorf("duration %d ms for %s is out of range", ms, p)
		}
	}
	if !printable(s.CommitSubject) || !printable(s.PRSummary) {
		return errors.New("commit subject or PR summary is not one printable line")
	}
	if s.RuleStackSHA != "" && !shaPattern.MatchString(s.RuleStackSHA) {
		return errors.New("rule-stack sha is not a commit sha")
	}
	if s.Branch != "" && s.Branch != BranchName(t.BranchSegment(), attemptID) {
		return errors.New("branch is not this run's")
	}
	if s.StartedAt.IsZero() || s.StartedAt.After(now.Add(maxClockSkew)) || s.StartedAt.Before(now.Add(-maxSummaryAge)) {
		return errors.New("start time is out of range")
	}
	return nil
}

func (u Usage) validate() error {
	for _, n := range []int64{u.Input, u.Output, u.Reasoning, u.CacheRead, u.CacheWrite, int64(u.Steps)} {
		if n < 0 || n > maxCount {
			return fmt.Errorf("token count %d is out of range", n)
		}
	}
	if !(u.Cost >= 0 && u.Cost <= maxCost) {
		return errors.New("cost is out of range")
	}
	return nil
}

func hasStep(steps []Step, phase Phase) bool {
	for _, st := range steps {
		if st.Phase == phase {
			return true
		}
	}
	return false
}

// validate checks a step's model, harness, tokens and completions object
// against attemptID's run.
func (st Step) validate(attemptID string) error {
	if !ValidModel(st.Model) {
		return errors.New("model is not provider/model")
	}
	if st.Harness != "" && !knownHarness(st.Harness) {
		return fmt.Errorf("harness %q is not one this run can route to", truncate(st.Harness, logErrorLimit))
	}
	if err := st.Tokens.validate(); err != nil {
		return err
	}
	if st.CompletionsObject != completionsObject(attemptID, st.Phase, st.Round) {
		return errors.New("completions object is not this run's")
	}
	return nil
}

// repoRelative reports whether f is a non-empty path that stays inside the repository.
func repoRelative(f string) bool {
	if f == "" || strings.ContainsRune(f, 0) || strings.HasPrefix(f, "/") {
		return false
	}
	return !slices.Contains(strings.Split(f, "/"), "..")
}

func completionsObject(attemptID string, phase Phase, round int) string {
	return "completions/" + attemptID + "-" + string(phase) + "-" + strconv.Itoa(round) + ".jsonl"
}
