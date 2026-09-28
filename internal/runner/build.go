package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	uploadTimeout       = 30 * time.Second
	stopDetailLimit     = 2048
	logErrorLimit       = 200
	defaultAgentTimeout = 50 * time.Minute
	// bundleName is also named by model.yml's bundle verify and upload and run.yml's fetch.
	bundleName = "wingman.bundle"
)

var modelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(/[A-Za-z0-9][A-Za-z0-9._:-]*)+$`)

// Agent runs the build model in a directory, streaming its events to stdout.
type Agent interface {
	Run(ctx context.Context, dir, prompt string, stdout, stderr io.Writer) error
}

// BuildDeps are the stores and the agents a build talks to, and where it reports.
type BuildDeps struct {
	Projections ObjectReader
	Completions ObjectCreator
	// Agent runs the build phase, with the repository open for edits.
	Agent Agent
	// PlanAgent runs the plan phase for an M or L ticket, restricted so it
	// cannot edit the worktree or run shell commands.
	PlanAgent Agent
	Report    func(Summary) error
	Logger    *slog.Logger
	Now       func() time.Time
}

// BuildConfig identifies one build.
type BuildConfig struct {
	AttemptID    string
	Repo         string
	TempDir      string
	Pointer      string
	Model        string
	AgentTimeout time.Duration
	Secret       string
	Identity     Identity
	Ticket       Ticket
}

// BuildResult is what the workflow needs from a build to open the PR.
type BuildResult struct {
	Branch     string
	BundlePath string
	Changed    bool
}

// StopError ends a build with an outcome the summary reports. Build has already
// logged it by the time it is returned.
type StopError struct {
	Outcome Outcome
	Reason  StopReason
	Err     error
}

func (s *StopError) Error() string { return fmt.Sprintf("%s (%s): %v", s.Outcome, s.Reason, s.Err) }
func (s *StopError) Unwrap() error { return s.Err }

func stopWith(o Outcome, r StopReason, err error) *StopError {
	return &StopError{Outcome: o, Reason: r, Err: err}
}

// BranchName is the branch attempt attemptID builds a ticket on, from the
// ticket's BranchSegment.
func BranchName(segment, attemptID string) string {
	return "wingman/" + segment + "-" + attemptID
}

// Build checks the identity and the ticket, fetches the projection, plans an M
// or L ticket with edit and bash denied, runs the build agent in a fresh
// worktree, and commits and bundles what it changed.
//
// The summary is reported on every return path, a panic included. The agent
// never runs unless the identity matches, the ticket is buildable, the model is
// well formed and the projection was found and valid. A build that edits a
// file outside its plan is stopped rather than committed.
func Build(ctx context.Context, d BuildDeps, c BuildConfig) (res BuildResult, err error) {
	sum := Summary{DurationsMS: map[string]int64{}, StartedAt: d.Now()}
	defer func() {
		p := recover()
		var s *StopError
		switch {
		case p != nil:
			sum.Outcome, sum.StopReason = OutcomeInfraFailure, StopPanic
			sum.StopDetail = truncate(fmt.Sprint(p), stopDetailLimit)
		case errors.As(err, &s):
			sum.Outcome, sum.StopReason = s.Outcome, s.Reason
			sum.StopDetail = truncate(detail(s.Err), stopDetailLimit)
			d.Logger.Error("runStopped", "reason", string(s.Reason), "phase", string(sum.Phase),
				"err", truncate(s.Err.Error(), logErrorLimit))
		case err != nil:
			sum.Outcome = OutcomeInfraFailure
		}
		if rerr := d.Report(sum); rerr != nil {
			d.Logger.Error("summaryReportFailed", "err", truncate(rerr.Error(), logErrorLimit))
			err = errors.Join(err, fmt.Errorf("%w: %w", ErrSummaryUnreported, rerr))
		} else {
			d.Logger.Info("summaryReported", "outcome", string(sum.Outcome))
		}
		if p != nil {
			panic(p)
		}
	}()

	timed := func(p Phase, f func() error) error {
		sum.Phase = p
		d.Logger.Info("phaseStarted", "phase", string(p))
		start := d.Now()
		ferr := f()
		sum.DurationsMS[string(p)] = d.Now().Sub(start).Milliseconds()
		return ferr
	}

	if err := c.Identity.CheckModel(); err != nil {
		return BuildResult{}, stopWith(OutcomeStopped, StopIdentityMismatch, err)
	}
	if err := c.Ticket.Validate(); err != nil {
		return BuildResult{}, stopWith(OutcomeStopped, StopTicketMissing, err)
	}

	var prompt string
	if err := timed(PhaseProjection, func() error {
		if !modelPattern.MatchString(c.Model) {
			return stopWith(OutcomeStopped, StopModelInvalid, errors.New("model is not provider/model"))
		}
		p, err := FetchBuildProjection(ctx, d.Projections, c.Pointer)
		var missing *MissingError
		switch {
		case errors.As(err, &missing):
			return stopWith(OutcomeStopped, StopProjectionMissing, err)
		case errors.Is(err, ErrPointerInvalid):
			return stopWith(OutcomeStopped, StopProjectionInvalid, err)
		case err != nil:
			return stopWith(OutcomeInfraFailure, StopProjectionRead, err)
		}
		sum.RuleStackSHA = p.SHA
		d.Logger.Info("projectionFetched", "sha", p.SHA)
		prompt, err = RenderPrompt(p.Text, c.Ticket)
		if err != nil {
			return stopWith(OutcomeStopped, StopProjectionInvalid, err)
		}
		return nil
	}); err != nil {
		return BuildResult{}, err
	}

	var wt Worktree
	if err := timed(PhaseWorktree, func() error {
		var err error
		wt, err = AddWorktree(ctx, c.Repo, filepath.Join(c.TempDir, "wt-"+c.AttemptID), BranchName(c.Ticket.BranchSegment(), c.AttemptID))
		if err != nil {
			return stopWith(OutcomeInfraFailure, StopWorktree, err)
		}
		sum.Branch = wt.Branch
		d.Logger.Info("worktreeCreated", "path", wt.Dir, "branch", wt.Branch)
		return nil
	}); err != nil {
		return BuildResult{}, err
	}

	var planFiles []string
	if c.Ticket.Size != "S" {
		if err := timed(PhasePlan, func() error {
			p, err := FetchPlanProjection(ctx, d.Projections, c.Pointer)
			var missing *MissingError
			switch {
			case errors.As(err, &missing):
				return stopWith(OutcomeStopped, StopProjectionMissing, err)
			case errors.Is(err, ErrPointerInvalid):
				return stopWith(OutcomeStopped, StopProjectionInvalid, err)
			case err != nil:
				return stopWith(OutcomeInfraFailure, StopProjectionRead, err)
			}
			planPrompt, err := PlanPrompt(p.Text, c.Ticket)
			if err != nil {
				return stopWith(OutcomeStopped, StopProjectionInvalid, err)
			}
			text, err := runAgent(ctx, d, c, PhasePlan, d.PlanAgent, wt.Dir, planPrompt, &sum)
			if err != nil {
				return err
			}
			if planFiles, err = parsePlanFiles(text); err != nil {
				return stopWith(OutcomeStopped, StopPlanInvalid, err)
			}
			d.Logger.Info("planFilesParsed", "files", len(planFiles))
			return nil
		}); err != nil {
			return BuildResult{}, err
		}
	}

	if err := timed(PhaseBuild, func() error {
		_, err := runAgent(ctx, d, c, PhaseBuild, d.Agent, wt.Dir, prompt, &sum)
		return err
	}); err != nil {
		return BuildResult{}, err
	}

	res = BuildResult{Branch: wt.Branch}
	if err := timed(PhaseCommit, func() error {
		if err := wt.Verify(ctx); err != nil {
			switch {
			case errors.Is(err, ErrGitTampered):
				return stopWith(OutcomeStopped, StopGitTampered, err)
			case errors.Is(err, ErrHeadMoved):
				return stopWith(OutcomeStopped, StopHeadMoved, err)
			}
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		files, err := wt.Commit(ctx, c.Ticket.Subject()+"\n\n"+c.Ticket.Body)
		if err != nil {
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		sum.EditedFiles = files
		if len(files) > 0 {
			if sum.DiffLines, err = wt.DiffLines(ctx); err != nil {
				return stopWith(OutcomeInfraFailure, StopCommit, err)
			}
		}
		d.Logger.Info("changesCommitted", "files", len(files))
		if len(files) == 0 {
			sum.Outcome = OutcomeNoChanges
			return nil
		}
		if c.Ticket.Size != "S" {
			if extra := outOfPlanFiles(files, planFiles); len(extra) > 0 {
				return stopWith(OutcomeStopped, StopOutOfPlan, fmt.Errorf("edited outside the plan: %s", strings.Join(extra, ", ")))
			}
		}
		if err := wt.CheckSecret(ctx, c.Secret); err != nil {
			if errors.Is(err, ErrSecretInBranch) {
				return stopWith(OutcomeStopped, StopSecretInBranch, err)
			}
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		res.BundlePath = filepath.Join(c.TempDir, bundleName)
		if err := wt.Bundle(ctx, res.BundlePath); err != nil {
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		res.Changed = true
		sum.Outcome = OutcomeBuilt
		return nil
	}); err != nil {
		return BuildResult{}, err
	}
	return res, nil
}

// runAgent runs agent under its own deadline with its events captured in a
// file, uploads them to the completions bucket, appends the round's usage to
// the summary as a Step for phase, and returns the agent's final text.
func runAgent(ctx context.Context, d BuildDeps, c BuildConfig, phase Phase, agent Agent, dir, prompt string, sum *Summary) (string, error) {
	const round = 1
	start := d.Now()
	completions := completionsObject(c.AttemptID, phase, round)
	stderrObject := strings.TrimSuffix(completions, ".jsonl") + ".stderr.log"
	events := filepath.Join(c.TempDir, "completions-"+c.AttemptID+"-"+string(phase)+".jsonl")
	stderrPath := filepath.Join(c.TempDir, "opencode-"+c.AttemptID+"-"+string(phase)+".stderr")
	out, err := os.Create(events)
	if err != nil {
		return "", stopWith(OutcomeInfraFailure, StopCompletions, err)
	}
	defer func() { _ = out.Close() }()
	errOut, err := os.Create(stderrPath)
	if err != nil {
		return "", stopWith(OutcomeInfraFailure, StopCompletions, err)
	}
	defer func() { _ = errOut.Close() }()

	timeout := c.AgentTimeout
	if timeout <= 0 {
		timeout = defaultAgentTimeout
	}
	agentCtx, cancel := context.WithTimeout(ctx, timeout)
	runErr := agent.Run(agentCtx, dir, prompt, out, errOut)
	timedOut := errors.Is(agentCtx.Err(), context.DeadlineExceeded)
	cancel()

	for _, u := range []struct {
		name string
		f    *os.File
	}{{completions, out}, {stderrObject, errOut}} {
		if _, err := u.f.Seek(0, io.SeekStart); err != nil {
			return "", stopWith(OutcomeInfraFailure, StopCompletions, err)
		}
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadTimeout)
		err := d.Completions.CreateObject(uctx, u.name, u.f)
		cancel()
		if err != nil {
			return "", stopWith(OutcomeInfraFailure, StopCompletions, fmt.Errorf("uploading %s: %w", u.name, err))
		}
	}

	step := Step{Phase: phase, Round: round, Model: c.Model, CompletionsObject: completions}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		sum.UsageWarning = err.Error()
	} else if usage, err := SumUsage(out); err != nil {
		sum.UsageWarning = err.Error()
	} else {
		step.Tokens = usage
	}
	var text string
	if _, err := out.Seek(0, io.SeekStart); err == nil {
		text, _ = FinalText(out)
	}
	step.DurationMS = d.Now().Sub(start).Milliseconds()
	sum.Steps = append(sum.Steps, step)
	d.Logger.Info("agentFinished", "phase", string(phase), "steps", step.Tokens.Steps, "input", step.Tokens.Input,
		"output", step.Tokens.Output, "cacheRead", step.Tokens.CacheRead, "cost", step.Tokens.Cost,
		"usageWarning", sum.UsageWarning != "")

	switch {
	case timedOut:
		return text, stopWith(OutcomeAgentFailed, StopAgentTimeout, fmt.Errorf("agent exceeded %s", timeout))
	case runErr != nil:
		outcome, reason := classifyAgentFailure(stderrPath)
		return text, stopWith(outcome, reason, runErr)
	}
	return text, nil
}

// allowanceMarkers are phrases assumed to appear in the agent's stderr when
// the provider's own allowance is exhausted mid-build, distinguishing a
// budget stop (FR-22, never escalated) from an ordinary agent failure (FR-13,
// which may retry at a higher tier). UNVERIFIED against a live exhaustion: no
// probe has confirmed OpenCode Go's actual wording, so this is a documented
// assumption pending that verification, not an observed fact — see the PR's
// Known Limitations.
var allowanceMarkers = []string{
	"allowance exhausted",
	"insufficient credit",
	"insufficient balance",
	"quota exceeded",
	"payment required",
}

// classifyStderrTail is how much of the agent's stderr classifyAgentFailure
// reads, from the END of the file — an exhaustion message is the process's
// last output before it exits, and bounding the read keeps a runaway stream
// from being loaded into memory on the one path meant to handle a bad run
// gracefully.
const classifyStderrTail = 64 << 10

// classifyAgentFailure reads a bounded tail of the agent's stderr file to
// tell a provider allowance exhaustion apart from any other agent failure.
// A read it cannot perform (or a stderr silent on every marker) falls back to
// the ordinary agent-failure classification, never to a false budget stop.
func classifyAgentFailure(stderrPath string) (Outcome, StopReason) {
	raw, err := readTail(stderrPath, classifyStderrTail)
	if err != nil {
		return OutcomeAgentFailed, StopAgentExit
	}
	lower := strings.ToLower(string(raw))
	for _, marker := range allowanceMarkers {
		if strings.Contains(lower, marker) {
			return OutcomeBudgetStop, StopAllowanceExhausted
		}
	}
	return OutcomeAgentFailed, StopAgentExit
}

// readTail reads at most limit bytes from the end of the file at path.
func readTail(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var start int64
	if info.Size() > limit {
		start = info.Size() - limit
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func detail(err error) string {
	var g *GitError
	if errors.As(err, &g) {
		return err.Error() + ": " + g.Stderr
	}
	return err.Error()
}

// truncate caps s at n bytes on a character boundary, replacing invalid UTF-8,
// since Firestore rejects a record holding a string that is not valid UTF-8.
func truncate(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
