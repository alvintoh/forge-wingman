package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	Outcome           Outcome          `json:"outcome"`
	StopReason        StopReason       `json:"stop_reason,omitempty"`
	StopDetail        string           `json:"stop_detail,omitempty"`
	Phase             Phase            `json:"phase"`
	Model             string           `json:"model,omitempty"`
	Tokens            Usage            `json:"tokens"`
	EditedFiles       []string         `json:"edited_files,omitempty"`
	DiffLines         DiffLines        `json:"diff_lines"`
	CompletionsObject string           `json:"completions_object,omitempty"`
	DurationsMS       map[string]int64 `json:"durations_ms,omitempty"`
	RuleStackSHA      string           `json:"rule_stack_sha,omitempty"`
	Branch            string           `json:"branch,omitempty"`
	UsageWarning      string           `json:"usage_warning,omitempty"`
	StartedAt         time.Time        `json:"started_at"`
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
		{OutcomeStopped, StopModelInvalid, PhaseProjection}:        true,
		{OutcomeStopped, StopProjectionMissing, PhaseProjection}:   true,
		{OutcomeStopped, StopProjectionInvalid, PhaseProjection}:   true,
		{OutcomeInfraFailure, StopProjectionRead, PhaseProjection}: true,
		{OutcomeInfraFailure, StopWorktree, PhaseWorktree}:         true,
		{OutcomeInfraFailure, StopCompletions, PhaseBuild}:         true,
		{OutcomeAgentFailed, StopAgentTimeout, PhaseBuild}:         true,
		{OutcomeAgentFailed, StopAgentExit, PhaseBuild}:            true,
		{OutcomeStopped, StopGitTampered, PhaseCommit}:             true,
		{OutcomeStopped, StopHeadMoved, PhaseCommit}:               true,
		{OutcomeInfraFailure, StopCommit, PhaseCommit}:             true,
		{OutcomeStopped, StopSecretInBranch, PhaseCommit}:          true,
		{OutcomeNoChanges, "", PhaseCommit}:                        true,
		{OutcomeBuilt, "", PhaseCommit}:                            true,
	}
	for _, p := range []Phase{PhaseProjection, PhaseWorktree, PhaseBuild, PhaseCommit} {
		m[ending{OutcomeInfraFailure, StopPanic, p}] = true
	}
	return m
}()

var buildPhases = map[Phase]bool{
	PhaseProjection: true, PhaseWorktree: true, PhaseBuild: true, PhaseCommit: true,
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

// Encode renders the summary as one line of JSON within maxSummaryBytes,
// dropping edited file names from the end until it fits.
func (s Summary) Encode() (string, error) {
	s.EditedFiles = capFiles(s.EditedFiles)
	for {
		b, err := json.Marshal(s)
		if err != nil {
			return "", err
		}
		if len(b) <= maxSummaryBytes {
			return string(b), nil
		}
		if len(s.EditedFiles) == 0 {
			return "", fmt.Errorf("summary is %d bytes, over %d", len(b), maxSummaryBytes)
		}
		s.EditedFiles = s.EditedFiles[:len(s.EditedFiles)/2]
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

// ParseSummary decodes and validates a summary written by run recordID's build of
// ticket t. The build shares a machine with the model, so every field is checked
// against what this run could have produced.
func ParseSummary(raw, recordID string, t Ticket, now time.Time) (Summary, error) {
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
	if err := s.validate(recordID, t, now); err != nil {
		return Summary{}, err
	}
	s.StopDetail = truncate(s.StopDetail, stopDetailLimit)
	s.UsageWarning = truncate(s.UsageWarning, stopDetailLimit)
	s.EditedFiles = capFiles(s.EditedFiles)
	return s, nil
}

func (s Summary) validate(recordID string, t Ticket, now time.Time) error {
	if !buildEndings[ending{s.Outcome, s.StopReason, s.Phase}] {
		return fmt.Errorf("outcome %q, stop reason %q at phase %q is not a build ending",
			truncate(string(s.Outcome), logErrorLimit), truncate(string(s.StopReason), logErrorLimit),
			truncate(string(s.Phase), logErrorLimit))
	}
	if s.Model != "" && (len(s.Model) > maxModelBytes || !modelPattern.MatchString(s.Model)) {
		return errors.New("model is not provider/model")
	}
	if err := s.Tokens.validate(); err != nil {
		return err
	}
	if len(s.EditedFiles) > maxEditedFiles {
		return fmt.Errorf("%d edited files, over %d", len(s.EditedFiles), maxEditedFiles)
	}
	for _, f := range s.EditedFiles {
		if !repoRelative(f) {
			return fmt.Errorf("edited file %q is not a path inside the repository", truncate(f, logErrorLimit))
		}
	}
	for _, n := range []int64{s.DiffLines.Added, s.DiffLines.Removed} {
		if n < 0 || n > maxCount {
			return fmt.Errorf("diff line count %d is out of range", n)
		}
	}
	if s.CompletionsObject != "" && s.CompletionsObject != completionsObject(recordID) {
		return errors.New("completions object is not this run's")
	}
	for p, ms := range s.DurationsMS {
		if !buildPhases[Phase(p)] {
			return fmt.Errorf("duration for %q, which is not a build phase", truncate(p, logErrorLimit))
		}
		if ms < 0 || ms > maxPhaseDuration.Milliseconds() {
			return fmt.Errorf("duration %d ms for %s is out of range", ms, p)
		}
	}
	if s.RuleStackSHA != "" && !shaPattern.MatchString(s.RuleStackSHA) {
		return errors.New("rule-stack sha is not a commit sha")
	}
	if s.Branch != "" && s.Branch != BranchName(t.ID, recordID) {
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

// repoRelative reports whether f is a non-empty path that stays inside the repository.
func repoRelative(f string) bool {
	if f == "" || strings.ContainsRune(f, 0) || strings.HasPrefix(f, "/") {
		return false
	}
	return !slices.Contains(strings.Split(f, "/"), "..")
}

func completionsObject(recordID string) string {
	return "completions/" + recordID + ".jsonl"
}
