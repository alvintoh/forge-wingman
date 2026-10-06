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
	"strconv"
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

// ValidModel reports whether model is a provider/model id short enough to record.
func ValidModel(model string) bool {
	return len(model) <= maxModelBytes && modelPattern.MatchString(model)
}

// Provider is model's prefix before its first "/" — "command-code" from
// "command-code/deepseek/deepseek-v4.1-flash" — matching modelPattern's own
// requirement that every model contain that separator.
func Provider(model string) string {
	if i := strings.IndexByte(model, '/'); i >= 0 {
		return model[:i]
	}
	return model
}

// Agent runs the build model in a directory, continuing session when it is
// non-empty, and streams its events to stdout. rules is the projection's rules
// head, which a harness places ahead of its per-run context; it is empty only on
// a round that starts fresh with no projection, such as the review pass.
type Agent interface {
	Run(ctx context.Context, dir, session, prompt, rules string, stdout, stderr io.Writer) error
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
	// ReviewAgent runs the pre-PR loop's review pass (FR-28), restricted the
	// same way PlanAgent is.
	ReviewAgent Agent
	// Checks runs the repository's own quality gates for the pre-PR loop —
	// RunChecks in production.
	Checks CheckRunner
	Report func(Summary) error
	Logger *slog.Logger
	Now    func() time.Time
}

// BuildConfig identifies one build.
type BuildConfig struct {
	AttemptID string
	Repo      string
	TempDir   string
	Pointer   string
	Model     string
	// PlanModels is the plan phase's ordered model list: the first entry is the
	// main model and later ones are backups the phase moves to when the one
	// before is unavailable or out of allowance. Each is any well-formed
	// provider/model; they must be distinct, and at least one is required.
	PlanModels []string
	// ReviewModels is FR-14's configuration for the pre-PR loop's review pass
	// (FR-28), ordered like PlanModels: the first entry is the main model and
	// later ones are backups the phase moves to when the one before is
	// unavailable or out of allowance. None may be Model, so the review is
	// never the builder checking its own work.
	ReviewModels []string
	AgentTimeout time.Duration
	// Secrets are checked against the branch before it is bundled, so an
	// agent's own API key never reaches the pushed branch.
	Secrets  []string
	Identity Identity
	Ticket   Ticket
}

// BuildResult is what the workflow needs from a build to open the PR.
type BuildResult struct {
	Branch     string
	BundlePath string
	Changed    bool
	// Ready is FR-5's draft-vs-ready decision: true only when the pre-PR
	// loop's checks passed and no review finding is still open. LoopDetail
	// names why not, so the PR can say so.
	Ready      bool
	LoopDetail string
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

// BranchPrefix starts the name of every branch a run builds on.
const BranchPrefix = "wingman/"

// BranchName is the branch attempt attemptID builds a ticket on, from the
// ticket's BranchSegment.
func BranchName(segment, attemptID string) string {
	return BranchPrefix + segment + "-" + attemptID
}

// localBranch is the worktree's own branch name. It is fixed, not per-run, so
// the branch the agent's context block reports is the same on every run; the
// bundle carries BranchName instead, which is what run.yml pushes.
const localBranch = BranchPrefix + "wt"

// Build checks the identity and the ticket, fetches the projection, plans an M
// or L ticket with edit and bash denied, runs the build agent in a fresh
// worktree, and commits and bundles what it changed.
//
// The summary is reported on every return path, a panic included. The agent
// never runs unless the identity matches, the ticket is buildable, the model is
// well formed and the projection was found and valid. A build that edits a
// file outside its plan is still committed, but never ready, and names those
// files in its summary.
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

	var rules, prompt string
	if err := timed(PhaseProjection, func() error {
		if !ValidModel(c.Model) {
			return stopWith(OutcomeStopped, StopModelInvalid, errors.New("model is not provider/model"))
		}
		if c.Ticket.Size != "S" {
			if err := ValidatePlanModels(c.PlanModels); err != nil {
				return stopWith(OutcomeStopped, StopModelInvalid, err)
			}
		}
		if err := ValidateReviewModels(c.ReviewModels, c.Model); err != nil {
			return stopWith(OutcomeStopped, StopModelInvalid, err)
		}
		// A harness the run is not configured for stops here, before any
		// agent runs, with the same model-invalid stop (AC7).
		if g, ok := d.Agent.(modelGate); ok {
			models := append([]string{c.Model}, c.ReviewModels...)
			if c.Ticket.Size != "S" {
				models = append(models, c.PlanModels...)
			}
			for _, m := range models {
				if err := g.Gate(m); err != nil {
					return stopWith(OutcomeStopped, StopModelInvalid, err)
				}
			}
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
		rules, prompt, err = RenderPromptParts(p.Text, c.Ticket)
		if err != nil {
			return stopWith(OutcomeStopped, StopProjectionInvalid, err)
		}
		return nil
	}); err != nil {
		return BuildResult{}, err
	}

	// The pushed branch must stay BranchName, which run.yml and the summary
	// validation check; the worktree instead sits on the fixed localBranch.
	pushBranch := BranchName(c.Ticket.BranchSegment(), c.AttemptID)
	var wt Worktree
	if err := timed(PhaseWorktree, func() error {
		var err error
		wt, err = AddWorktree(ctx, c.Repo, filepath.Join(c.TempDir, "wt"), localBranch)
		if err != nil {
			return stopWith(OutcomeInfraFailure, StopWorktree, err)
		}
		sum.Branch = pushBranch
		d.Logger.Info("worktreeCreated", "path", wt.Dir, "branch", wt.Branch, "push", pushBranch)
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
			planRules, planPrompt, err := PlanPrompt(p.Text, c.Ticket)
			if err != nil {
				return stopWith(OutcomeStopped, StopProjectionInvalid, err)
			}
			call := agentCall{Phase: PhasePlan, Round: 1, Model: c.PlanModels[0], Rules: planRules, Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now())}
			text, _, _, err := runAgentInOrder(ctx, d, c, call, c.PlanModels, d.PlanAgent, wt.Dir, planPrompt, &sum)
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

	var checksOK bool
	var loopDetail, session string
	var lastRound int
	var msg CommitMessage
	if err := timed(PhaseBuild, func() error {
		var err error
		checksOK, loopDetail, session, lastRound, err = runCheckLoop(ctx, d, c, wt, &sum, rules, prompt, &msg)
		return err
	}); err != nil {
		return BuildResult{}, err
	}

	ready := checksOK
	if checksOK {
		// Unlike the check loop above, a review-phase failure never stops the
		// build: it forces a draft naming the failure instead (FR-5).
		_ = timed(PhaseReview, func() error {
			var rerr error
			ready, loopDetail, rerr = runReview(ctx, d, c, wt, &sum, rules, session, lastRound, &msg)
			if rerr != nil {
				ready = false
				loopDetail = reviewFailureDetail(rerr)
				var s *StopError
				reason := StopReason("")
				if errors.As(rerr, &s) {
					reason = s.Reason
				}
				d.Logger.Error("reviewFailed", "reason", string(reason), "err", truncate(detail(rerr), logErrorLimit))
			}
			return nil
		})
	}

	sum.Ready = ready
	sum.LoopDetail = truncate(loopDetail, stopDetailLimit)
	res = BuildResult{Branch: pushBranch, Ready: ready, LoopDetail: loopDetail}
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
		if msg.Subject == "" {
			pending, err := wt.PendingFiles(ctx)
			if err != nil {
				return stopWith(OutcomeInfraFailure, StopCommit, err)
			}
			msg = FallbackCommitMessage(c.Ticket, pending)
			d.Logger.Info("commitMessageFallback")
		}
		files, err := wt.Commit(ctx, msg.Text())
		if err != nil {
			return stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		sum.CommitSubject, sum.CommitBody, sum.PRSummary = msg.Subject, msg.Body, msg.Summary
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
				sum.OutOfPlanFiles = extra
				sum.Ready, res.Ready = false, false
				d.Logger.Info("outOfPlanEdits", "files", len(extra))
			}
		}
		for _, secret := range c.Secrets {
			if err := wt.CheckSecret(ctx, secret); err != nil {
				if errors.Is(err, ErrSecretInBranch) {
					return stopWith(OutcomeStopped, StopSecretInBranch, err)
				}
				return stopWith(OutcomeInfraFailure, StopCommit, err)
			}
		}
		res.BundlePath = filepath.Join(c.TempDir, bundleName)
		if err := wt.Bundle(ctx, res.BundlePath, pushBranch); err != nil {
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

// agentCall is one agent invocation's identity within a build: which
// phase and round it belongs to, which model runs it, the session it
// continues (empty for a fresh one), the projection's rules head it carries
// (empty for the review pass, which starts fresh), its own deadline, and — for
// a round the check or review loop drove — what drove it (FR-6).
type agentCall struct {
	Phase   Phase
	Round   int
	Model   string
	Session string
	Rules   string
	Detail  string
	Timeout time.Duration
}

// runAgent runs agent under call's own deadline with its events captured in a
// file, uploads them to the completions bucket, appends the round's usage to
// the summary as a Step, and returns the agent's final text and the session
// id it reports, so a caller can feed it back on a later round.
func runAgent(ctx context.Context, d BuildDeps, c BuildConfig, call agentCall, agent Agent, dir, prompt string, sum *Summary) (string, string, error) {
	start := d.Now()
	completions := completionsObject(c.AttemptID, call.Phase, call.Round)
	stderrObject := strings.TrimSuffix(completions, ".jsonl") + ".stderr.log"
	roundSuffix := string(call.Phase) + "-" + strconv.Itoa(call.Round)
	events := filepath.Join(c.TempDir, "completions-"+c.AttemptID+"-"+roundSuffix+".jsonl")
	stderrPath := filepath.Join(c.TempDir, "agent-"+c.AttemptID+"-"+roundSuffix+".stderr")
	out, err := os.Create(events)
	if err != nil {
		return "", "", stopWith(OutcomeInfraFailure, StopCompletions, err)
	}
	defer func() { _ = out.Close() }()
	errOut, err := os.Create(stderrPath)
	if err != nil {
		return "", "", stopWith(OutcomeInfraFailure, StopCompletions, err)
	}
	defer func() { _ = errOut.Close() }()

	timeout := call.Timeout
	if timeout <= 0 {
		timeout = defaultAgentTimeout
	}
	agentCtx, cancel := context.WithTimeout(ctx, timeout)
	runErr := agent.Run(agentCtx, dir, call.Session, prompt, call.Rules, out, errOut)
	timedOut := errors.Is(agentCtx.Err(), context.DeadlineExceeded)
	cancel()

	for _, u := range []struct {
		name string
		f    *os.File
	}{{completions, out}, {stderrObject, errOut}} {
		if _, err := u.f.Seek(0, io.SeekStart); err != nil {
			return "", "", stopWith(OutcomeInfraFailure, StopCompletions, err)
		}
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadTimeout)
		err := d.Completions.CreateObject(uctx, u.name, u.f)
		cancel()
		if err != nil {
			return "", "", stopWith(OutcomeInfraFailure, StopCompletions, fmt.Errorf("uploading %s: %w", u.name, err))
		}
	}

	step := Step{Phase: call.Phase, Round: call.Round, Model: call.Model, CompletionsObject: completions,
		Detail: truncate(call.Detail, stopDetailLimit)}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		addUsageWarning(sum, err.Error())
	} else if usage, err := SumUsage(out); err != nil {
		addUsageWarning(sum, err.Error())
	} else {
		step.Tokens = usage
	}
	if _, err := out.Seek(0, io.SeekStart); err == nil {
		warnings, _ := UsageWarnings(out)
		for _, w := range warnings {
			addUsageWarning(sum, w)
		}
	}
	if _, err := out.Seek(0, io.SeekStart); err == nil {
		requests, _ := RequestUsage(out)
		for i, r := range requests {
			d.Logger.Info("modelRequest", "phase", string(call.Phase), "round", call.Round, "request", i+1,
				"input", r.Input, "cacheRead", r.CacheRead, "output", r.Output)
		}
	}
	var text, sessionID string
	if _, err := out.Seek(0, io.SeekStart); err == nil {
		text, _ = FinalText(out)
	}
	if _, err := out.Seek(0, io.SeekStart); err == nil {
		sessionID, _ = SessionID(out)
	}
	// A round past the first continues an earlier session (call.Session is
	// non-empty); an empty sessionID here means the agent reported no session
	// id at all, so the NEXT round would silently start a fresh session with
	// none of this run's history. Round 1 legitimately starting fresh never
	// warns.
	if call.Round > 1 && sessionID == "" {
		addUsageWarning(sum, fmt.Sprintf("round %d reported no session id: continuity with earlier rounds may be lost", call.Round))
	}
	step.DurationMS = d.Now().Sub(start).Milliseconds()
	sum.Steps = append(sum.Steps, step)
	d.Logger.Info("agentFinished", "phase", string(call.Phase), "round", call.Round, "steps", step.Tokens.Steps,
		"input", step.Tokens.Input, "output", step.Tokens.Output, "cacheRead", step.Tokens.CacheRead,
		"cost", step.Tokens.Cost, "usageWarning", sum.UsageWarning != "")

	switch {
	case timedOut:
		return text, sessionID, stopWith(OutcomeAgentFailed, StopAgentTimeout, fmt.Errorf("agent exceeded %s", timeout))
	case runErr != nil:
		outcome, reason := classifyAgentFailure(agent, stderrPath, runErr)
		return text, sessionID, stopWith(outcome, reason, runErr)
	}
	return text, sessionID, nil
}

// addUsageWarning appends warning to the summary's usage warning.
func addUsageWarning(sum *Summary, warning string) {
	if sum.UsageWarning == "" {
		sum.UsageWarning = warning
	} else {
		sum.UsageWarning += "; " + warning
	}
}

// DefaultPlanModel is the plan phase's model when none is configured.
const DefaultPlanModel = "command-code/deepseek/deepseek-v4.1-flash"

// DefaultReviewModel is the review model run.yml's review_models input defaults
// to, used when a ticket names none.
const DefaultReviewModel = "command-code/meta/muse-spark-1.3-contributor"

// ValidatePlanModels reports whether models is a usable plan list: at least
// one entry, each well formed and none repeated.
func ValidatePlanModels(models []string) error {
	return validateModelList("plan", models)
}

// ValidateReviewModels reports whether models is a usable review list: at
// least one entry, each well formed and none repeated, and none the build
// model or one of its fallbacks, so the review never runs on a model the
// builder itself may run.
func ValidateReviewModels(models []string, build string) error {
	if err := validateModelList("review", models); err != nil {
		return err
	}
	// The build phase falls back along fallbackModels when its model is
	// unavailable, so a review on one of them would still be the builder
	// checking its own work.
	builder := map[string]bool{build: true}
	for _, m := range fallbackModels(build) {
		builder[m] = true
	}
	for _, m := range models {
		if builder[m] {
			return fmt.Errorf("review model %q is the build model or one of its fallbacks", m)
		}
	}
	return nil
}

// validateModelList reports whether models is a usable ordered list: at least
// one entry, each well formed and none repeated. label names the list in the
// errors.
func validateModelList(label string, models []string) error {
	if len(models) == 0 {
		return fmt.Errorf("%s models: at least one model is required", label)
	}
	seen := map[string]bool{}
	for _, m := range models {
		switch {
		case !ValidModel(m):
			return fmt.Errorf("%s model %q is not provider/model", label, m)
		case seen[m]:
			return fmt.Errorf("%s model %q is listed twice", label, m)
		}
		seen[m] = true
	}
	return nil
}

// runAgentWithFallback runs call on its model, moving to the same-provider
// fallbacks of fallbackModels(call.Model) when one is unavailable or out of
// allowance.
func runAgentWithFallback(ctx context.Context, d BuildDeps, c BuildConfig, call agentCall, agent Agent, dir, prompt string, sum *Summary) (text, session string, round int, err error) {
	// An exhausted allowance advances along this phase's own ordered fallbacks
	// rather than stopping the build.
	return runAgentInOrder(ctx, d, c, call, append([]string{call.Model}, fallbackModels(call.Model)...), agent, dir, prompt, sum)
}

// runAgentInOrder runs call via runAgent on each of models in turn, advancing
// on an availability-classified failure (StopModelUnavailable) and on an
// exhausted allowance too. Each attempt is
// its own Step under an incrementing Round so every model tried is recorded
// (AC1). It reports the round its last attempt used, so a caller numbering
// further rounds for this phase continues from there rather than reusing one.
// Once the order is exhausted it stops the build with the last failure's own
// classification: OutcomeInfraFailure/StopModelUnavailable, or
// OutcomeBudgetStop/StopAllowanceExhausted. That is deliberately not a path
// FR-13's (unimplemented) escalation could hook into. A later attempt's
// deadline is recomputed from the time then left, so earlier attempts cannot
// stretch the phase past the run cap.
func runAgentInOrder(ctx context.Context, d BuildDeps, c BuildConfig, call agentCall, models []string, agent Agent, dir, prompt string, sum *Summary) (text, session string, round int, err error) {
	round = call.Round
	var lastErr *StopError
	for i, model := range models {
		attempt := call
		attempt.Model, attempt.Round = model, round
		if i > 0 {
			attempt.Timeout = roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now())
		}
		text, session, err = runAgent(ctx, d, c, attempt, bindModel(agent, model), dir, prompt, sum)
		if err == nil {
			return text, session, round, nil
		}
		var s *StopError
		if !errors.As(err, &s) || !movesToNextModel(s.Reason) {
			return text, session, round, err
		}
		lastErr = s
		if i+1 >= len(models) {
			break
		}
		round++
		d.Logger.Warn("modelSubstituted", "phase", string(call.Phase), "reason", string(s.Reason), "from", model, "to", models[i+1])
	}
	if lastErr.Reason == StopAllowanceExhausted {
		return "", "", round, stopWith(OutcomeBudgetStop, StopAllowanceExhausted,
			fmt.Errorf("allowance order for %s exhausted: %w", models[0], lastErr))
	}
	return "", "", round, stopWith(OutcomeInfraFailure, StopModelUnavailable,
		fmt.Errorf("availability order for %s exhausted: %w", models[0], lastErr))
}

// movesToNextModel reports whether a stop of reason sends a phase on to its next
// model: unavailability, or an exhausted allowance, which advances along the
// phase's own ordered fallbacks.
func movesToNextModel(reason StopReason) bool {
	return reason == StopModelUnavailable || reason == StopAllowanceExhausted
}

// modelBinder is implemented by an agent whose model can be swapped per attempt.
type modelBinder interface {
	WithModel(model string) Agent
}

// bindModel returns agent running model, or agent unchanged when it cannot be rebound.
func bindModel(agent Agent, model string) Agent {
	if b, ok := agent.(modelBinder); ok {
		return b.WithModel(model)
	}
	return agent
}

// classifyStderrTail is how much of the agent's stderr classifyAgentFailure
// reads, from the END of the file — an exhaustion message is the process's
// last output before it exits, and bounding the read keeps a runaway stream
// from being loaded into memory on the one path meant to handle a bad run
// gracefully.
const classifyStderrTail = 64 << 10

// classifyAgentFailure reads a bounded tail of the agent's stderr file and
// classifies the failure through the agent's own harness when it has one, else
// by the assumed provider markers. A read it cannot perform falls back to the
// ordinary agent-failure classification, never to a false budget stop.
func classifyAgentFailure(agent Agent, stderrPath string, exitErr error) (Outcome, StopReason) {
	raw, err := readTail(stderrPath, classifyStderrTail)
	if err != nil {
		return OutcomeAgentFailed, StopAgentExit
	}
	if c, ok := agent.(failureClassifier); ok {
		return c.Classify(string(raw), exitErr)
	}
	return classifyMarkers(string(raw))
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
