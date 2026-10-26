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

// maxReviewFindings and maxFindingBytes bound what a summary carries of the
// pre-open review: room for every finding a review pass and a capped guard scan
// can make, and never enough for one long finding to crowd out the rest.
const (
	maxReviewFindings = 24
	maxFindingBytes   = 512
)

// ErrSummaryMissing reports a record job that received no build summary.
var ErrSummaryMissing = errors.New("no build summary")

// ErrSummaryUnreported marks a build whose summary did not reach the record job.
var ErrSummaryUnreported = errors.New("summary not reported")

// Summary is what a build reports for the record job to write, since the build's
// own identity cannot write the run store.
type Summary struct {
	Outcome      Outcome          `json:"outcome"`
	StopReason   StopReason       `json:"stop_reason,omitempty"`
	StopDetail   string           `json:"stop_detail,omitempty"`
	Phase        Phase            `json:"phase"`
	Steps        []Step           `json:"steps,omitempty"`
	EditedFiles  []string         `json:"edited_files,omitempty"`
	DiffLines    DiffLines        `json:"diff_lines"`
	DurationsMS  map[string]int64 `json:"durations_ms,omitempty"`
	RuleStackSHA string           `json:"rule_stack_sha,omitempty"`
	Branch       string           `json:"branch,omitempty"`
	UsageWarning string           `json:"usage_warning,omitempty"`
	// Cache is each phase's own prompt-cache figures, keyed by phase (AC1).
	Cache map[string]PhaseCache `json:"cache,omitempty"`
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
	// ReadFiles is every repository file the agent's read tool calls touched,
	// and PeakContextTokens the largest context a single request read.
	ReadFiles         []string `json:"read_files,omitempty"`
	PeakContextTokens int64    `json:"peak_context_tokens,omitempty"`
	// ReviewFindings is each finding the pre-open review made and how the run
	// left it, which the PR body publishes so a reader sees what the run fixed
	// before opening the pull request (AC5).
	ReviewFindings []ReviewFinding `json:"review_findings,omitempty"`
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
	// PeakContextTokens is the largest context one of this step's requests read.
	PeakContextTokens int64 `firestore:"peak_context_tokens" json:"peak_context_tokens,omitempty"`
	// At is when the step started. The step is priced per event time by
	// Rates.At, but nothing of that time survives the events themselves, so
	// the record job reads this back to tag the run's own time of use.
	At time.Time `firestore:"at" json:"at"`
}

// ReviewFinding is one finding the pre-open review made and how the run left
// it: a guard the guard scan broke, or a review pass's own finding, and whether
// the fix round resolved it. The PR body lists them, so a reader sees what the
// run fixed before it opened the pull request.
type ReviewFinding struct {
	Finding    string `json:"finding"`
	Resolution string `json:"resolution"`
}

// PhaseCache is one phase's prompt-cache figures: what its first request read
// fresh and from the provider's cache, and the cached share of every input token
// the phase read. Both harnesses report a request's input exclusive of what it
// read from cache, so a first request's fresh tokens are its own input and its
// cached tokens its cache read.
type PhaseCache struct {
	FirstFresh  int64 `firestore:"first_fresh" json:"first_fresh"`
	FirstCached int64 `firestore:"first_cached" json:"first_cached"`
	// CachedShare is the share of the phase's whole input — fresh and cached
	// together — that came from the provider's cache.
	CachedShare float64 `firestore:"cached_share" json:"cached_share"`
}

// CachedShare is the share of a fresh + cached input total that came from the
// provider's cache, zero when nothing was read at all.
func CachedShare(fresh, cached int64) float64 {
	if total := fresh + cached; total > 0 {
		return float64(cached) / float64(total)
	}
	return 0
}

const (
	// PhaseCacheNone is what a view prints for a phase that made no model
	// request.
	PhaseCacheNone = "none"
	// PhaseCacheNotRecorded is what a view prints when there is no share to
	// print: "not recorded" means no cache map, "none" means no request in that
	// phase, and a phase whose first request read no tokens reads as not recorded.
	PhaseCacheNotRecorded = "not recorded"
)

// PhaseCacheCell renders phase's prompt-cache figures from cache — one run's own,
// or a plan's trailing runs' — as a view prints them: the share of the phase's
// first request and of its whole input that came from the provider's cache. A
// phase the map holds no figures for reads PhaseCacheNone, and a map holding no
// phase at all reads PhaseCacheNotRecorded, so a view never prints a share that
// was never read.
func PhaseCacheCell(cache map[string]PhaseCache, phase Phase) string {
	first, whole, word := phaseCacheFigures(cache, phase)
	if word != "" {
		return word
	}
	return fmt.Sprintf("%s first, %s phase", percentShare(first), percentShare(whole))
}

// PhaseCacheShares returns the fractions behind PhaseCacheCell: the share of the
// phase's first request and of its whole input that came from the provider's
// cache, each between 0 and 1. Both are nil wherever the cell prints a word, so
// a share of zero is a cold phase and nil is no share to read.
func PhaseCacheShares(cache map[string]PhaseCache, phase Phase) (first, whole *float64) {
	firstShare, wholeShare, word := phaseCacheFigures(cache, phase)
	if word != "" {
		return nil, nil
	}
	return &firstShare, &wholeShare
}

// phaseCacheFigures reads phase's first-request and whole-phase shares from
// cache, or the word that stands in for them. Both renderers read it, so the
// cell only formats the shares and cannot disagree with them. A phase whose
// first request read no tokens has no first share to print, and the cell prints
// no whole-phase share beside a missing one, so it is a word too.
func phaseCacheFigures(cache map[string]PhaseCache, phase Phase) (first, whole float64, word string) {
	c, ok := cache[string(phase)]
	if !ok {
		if len(cache) == 0 {
			return 0, 0, PhaseCacheNotRecorded
		}
		return 0, 0, PhaseCacheNone
	}
	if c.FirstFresh+c.FirstCached == 0 {
		return 0, 0, PhaseCacheNotRecorded
	}
	return CachedShare(c.FirstFresh, c.FirstCached), c.CachedShare, ""
}

// percentShare renders a 0-1 share as a whole percentage.
func percentShare(share float64) string {
	return fmt.Sprintf("%.0f%%", share*100)
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
		{OutcomeInfraFailure, StopAgentContainer, PhaseAgentStart}: true,
		{OutcomeStopped, StopProjectionMissing, PhasePlan}:         true,
		{OutcomeStopped, StopProjectionInvalid, PhasePlan}:         true,
		{OutcomeInfraFailure, StopProjectionRead, PhasePlan}:       true,
		{OutcomeInfraFailure, StopCompletions, PhasePlan}:          true,
		{OutcomeInfraFailure, StopAgentContainer, PhasePlan}:       true,
		{OutcomeAgentFailed, StopAgentTimeout, PhasePlan}:          true,
		{OutcomeAgentFailed, StopAgentExit, PhasePlan}:             true,
		{OutcomeStopped, StopPlanInvalid, PhasePlan}:               true,
		{OutcomeStopped, StopWorkflowChange, PhasePlan}:            true,
		{OutcomeStopped, StopRunBudget, PhasePlan}:                 true,
		{OutcomeBudgetStop, StopAllowanceExhausted, PhasePlan}:     true,
		{OutcomeInfraFailure, StopModelUnavailable, PhasePlan}:     true,
		{OutcomeInfraFailure, StopCompletions, PhaseBuild}:         true,
		{OutcomeInfraFailure, StopAgentContainer, PhaseBuild}:      true,
		{OutcomeAgentFailed, StopAgentTimeout, PhaseBuild}:         true,
		{OutcomeAgentFailed, StopAgentExit, PhaseBuild}:            true,
		{OutcomeAgentFailed, StopAgentNoProgress, PhaseBuild}:      true,
		{OutcomeBudgetStop, StopAllowanceExhausted, PhaseBuild}:    true,
		{OutcomeInfraFailure, StopModelUnavailable, PhaseBuild}:    true,
		{OutcomeInfraFailure, StopChecksRun, PhaseBuild}:           true,
		{OutcomeStopped, StopRunBudget, PhaseBuild}:                true,
		// A review-phase failure never ends a build at PhaseReview: it forces
		// a draft and the build proceeds to PhaseCommit instead (FR-5) — only
		// a panic mid-review, below, can end there.
		{OutcomeStopped, StopGitTampered, PhaseCommit}:    true,
		{OutcomeStopped, StopHeadMoved, PhaseCommit}:      true,
		{OutcomeInfraFailure, StopCommit, PhaseCommit}:    true,
		{OutcomeStopped, StopSecretInBranch, PhaseCommit}: true,
		{OutcomeStopped, StopWorkflowChange, PhaseCommit}: true,
		{OutcomeNoChanges, "", PhaseCommit}:               true,
		{OutcomeBuilt, "", PhaseCommit}:                   true,
	}
	for _, p := range []Phase{PhaseProjection, PhaseWorktree, PhaseAgentStart, PhasePlan, PhaseBuild, PhaseReview, PhaseCommit} {
		m[ending{OutcomeInfraFailure, StopPanic, p}] = true
	}
	return m
}()

// checksDuration is the DurationsMS key for the run's total check time. That
// time is already inside the build and review phases' own durations, so
// BillableMinutes leaves it out.
const checksDuration = "checks"

var buildPhases = map[Phase]bool{
	PhaseProjection: true, PhaseWorktree: true, PhaseAgentStart: true, PhasePlan: true, PhaseBuild: true, PhaseReview: true, PhaseCommit: true,
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
// dropping edited file names from the end, then, once none are
// left, dropping the oldest steps until it fits.
func (s Summary) Encode() (string, error) {
	s.EditedFiles = capFiles(s.EditedFiles)
	s.ReadFiles = capFiles(s.ReadFiles)
	for {
		b, err := json.Marshal(s)
		if err != nil {
			return "", err
		}
		if len(b) <= maxSummaryBytes {
			return string(b), nil
		}
		switch {
		case len(s.ReadFiles) > 0:
			s.ReadFiles = s.ReadFiles[:len(s.ReadFiles)/2]
		case len(s.EditedFiles) > 0:
			s.EditedFiles = s.EditedFiles[:len(s.EditedFiles)/2]
		case len(s.Steps) > 0:
			s.Steps = s.Steps[1:]
		case len(s.ReviewFindings) > 0:
			s.ReviewFindings = s.ReviewFindings[:len(s.ReviewFindings)/2]
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

// capFindings bounds how many findings a summary carries and how long each is.
// The run that reports them and the job that reads them both apply it, so what
// one writes is what the other accepts.
func capFindings(findings []ReviewFinding) []ReviewFinding {
	if len(findings) > maxReviewFindings {
		findings = findings[:maxReviewFindings]
	}
	out := make([]ReviewFinding, len(findings))
	for i, f := range findings {
		out[i] = ReviewFinding{Finding: truncate(f.Finding, maxFindingBytes), Resolution: truncate(f.Resolution, maxFindingBytes)}
	}
	return out
}

// mergePaths appends add's repository paths to paths in first-seen order and
// without repeats, so two phases or rounds touching one file name it once. A
// path that stays outside the repository is left out: the summary carries only
// what the run could have touched inside it.
func mergePaths(paths, add []string) []string {
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		seen[p] = true
	}
	for _, p := range add {
		p = truncate(p, maxPathBytes)
		if !repoRelative(p) || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
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
	s.ReadFiles = capFiles(s.ReadFiles)
	s.ReviewFindings = capFindings(s.ReviewFindings)
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
	// The run's own window is checked first, so a step's time below is only
	// ever measured against one that this build could have reported.
	if s.StartedAt.IsZero() || s.StartedAt.After(now.Add(maxClockSkew)) || s.StartedAt.Before(now.Add(-maxSummaryAge)) {
		return errors.New("start time is out of range")
	}
	for _, step := range s.Steps {
		if err := step.validate(attemptID); err != nil {
			return err
		}
		// A step starts inside the run that reported it, so its own time
		// sits between the run's start and now; the clock skew covers the
		// build's and the record job's own clocks.
		if step.At.IsZero() || step.At.After(now.Add(maxClockSkew)) || step.At.Before(s.StartedAt.Add(-maxClockSkew)) {
			return errors.New("a step's start time is outside the run")
		}
	}
	if len(s.EditedFiles) > maxEditedFiles {
		return fmt.Errorf("%d edited files, over %d", len(s.EditedFiles), maxEditedFiles)
	}
	for _, f := range s.EditedFiles {
		if !repoRelative(f) {
			return fmt.Errorf("edited file %q is not a path inside the repository", truncate(f, logErrorLimit))
		}
	}
	if len(s.ReadFiles) > maxEditedFiles {
		return fmt.Errorf("%d read files, over %d", len(s.ReadFiles), maxEditedFiles)
	}
	for _, f := range s.ReadFiles {
		if !repoRelative(f) {
			return fmt.Errorf("read file %q is not a path inside the repository", truncate(f, logErrorLimit))
		}
	}
	if len(s.ReviewFindings) > maxReviewFindings {
		return fmt.Errorf("%d review findings, over %d", len(s.ReviewFindings), maxReviewFindings)
	}
	for _, f := range s.ReviewFindings {
		if !printable(f.Finding) || !printable(f.Resolution) {
			return errors.New("a review finding or its resolution is not one printable line")
		}
	}
	if s.PeakContextTokens < 0 || s.PeakContextTokens > maxCount {
		return fmt.Errorf("peak context tokens %d is out of range", s.PeakContextTokens)
	}
	for _, n := range []int64{s.DiffLines.Added, s.DiffLines.Removed} {
		if n < 0 || n > maxCount {
			return fmt.Errorf("diff line count %d is out of range", n)
		}
	}
	for p, ms := range s.DurationsMS {
		if !buildPhases[Phase(p)] && p != checksDuration {
			return fmt.Errorf("duration for %q, which is not a build phase or the checks", truncate(p, logErrorLimit))
		}
		if ms < 0 || ms > maxPhaseDuration.Milliseconds() {
			return fmt.Errorf("duration %d ms for %s is out of range", ms, p)
		}
	}
	for p, c := range s.Cache {
		if !buildPhases[Phase(p)] {
			return fmt.Errorf("cache figures for %q, which is not a build phase", truncate(p, logErrorLimit))
		}
		if c.FirstFresh < 0 || c.FirstFresh > maxCount || c.FirstCached < 0 || c.FirstCached > maxCount {
			return errors.New("a phase's first-request token count is out of range")
		}
		if !(c.CachedShare >= 0 && c.CachedShare <= 1) {
			return errors.New("a phase's cached share is outside 0-1")
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
	if st.PeakContextTokens < 0 || st.PeakContextTokens > maxCount {
		return fmt.Errorf("step peak context tokens %d is out of range", st.PeakContextTokens)
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
