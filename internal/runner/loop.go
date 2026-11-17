package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// checkLoopMaxRounds is FR-28's "at most 3 rounds": the build agent's first
// attempt plus at most two rebuilds fed the failing gate's output. Counts
// rebuild PASSES, not agentCall.Round — a same-pass model substitution
// advances Round without spending one of these three.
const checkLoopMaxRounds = 3

// checkFeedbackLimit bounds how much of a gate's output a rebuild or repair
// round, or the loop's give-up detail, carries.
const checkFeedbackLimit = 8 << 10

// maxFixRounds is FR-28's fix-round ceiling for the pre-open review: a fix
// round and, when its re-check still reports a finding, one more. A finding
// still open after the last stays open and holds the PR as a draft.
const maxFixRounds = 2

const (
	// nfr1RunCap is NFR-1's whole-run duration ceiling — the build job's own
	// timeout-minutes in .github/workflows/model.yml, which must agree with it.
	nfr1RunCap = nfr1BuildBudget + nfr1ReviewFloor + nfr1CommitReserve
	// nfr1BuildBudget is how much of nfr1RunCap the build stage may spend, from
	// the run's start: the plan phase, the build and check rounds, and the
	// guard scan the review pass follows.
	nfr1BuildBudget = 90 * time.Minute
	// nfr1ReviewFloor is the least the review stage may spend, from the
	// review's own start: the review pass, its fix rounds and the re-checks
	// that follow them. It is the share nfr1RunCap keeps for the review once
	// the build stage has spent its own and the commit reserve is set aside, so
	// a run that spent the whole build budget still gets this much.
	nfr1ReviewFloor = 22 * time.Minute
	// nfr1CommitReserve is the share of nfr1RunCap kept for the commit, bundle
	// and report steps that still run after the review. A re-check the review
	// stage had no room for spends part of it, down to reserveRecheckFloor.
	nfr1CommitReserve = 8 * time.Minute
)

// reviewStartRoom is the least the review stage must have left for another
// pass, fix round or re-check to start. A call started with less would run out
// on the stage's spent budget rather than on its own work, so the run would
// record an agent timeout for what is really the budget running out.
const reviewStartRoom = 3 * time.Minute

// reserveRecheckFloor is what a re-check run from the commit reserve leaves of
// the run cap for the commit, bundle and report steps that follow the review.
const reserveRecheckFloor = 2 * time.Minute

// repairRoundFloor is the least repairBudget leaves to start a repair round:
// the round and the checks that follow it. Under it no round starts and the
// PR's notes name the failing gate. Measured against the cap rather than the
// review stage's own budget, because the commit reserve is what pays for it.
const repairRoundFloor = 6 * time.Minute

// repairCheckRoom is the part of repairRoundFloor the checks after the round
// get: the round's own deadline stops this much short of repairBudget, so the
// gate re-check still has room to run and reach the PR's notes once the round
// returns.
const repairCheckRoom = 2 * time.Minute

// compileLevelGate reports whether gate's failure leaves code that may not
// build, which is the one failure a fix round can leave that a round on the
// gate's own output repairs: vet and web-build compile it, and a format gate
// still failing is one a formatter could not fix — for gofmt, a file it cannot
// parse. A test or lint failure is deliberately not one: the repair round does
// not address those.
func compileLevelGate(gate string) bool {
	return gate == checkVet || gate == checkWebBuild || isFormatGate(gate)
}

// The stage words a run in flight is shown with.
const (
	RunStagePlanning = "planning"
	RunStageBuilding = "building"
)

// RunLimitMinutes is the whole minutes a run in the given stage word may take:
// the build budget for planning, the whole-run cap for every other word.
func RunLimitMinutes(stage string) int {
	if stage == RunStagePlanning {
		return int(nfr1BuildBudget / time.Minute)
	}
	return int(nfr1RunCap / time.Minute)
}

// RunBudgetLeft is what NFR-1's whole-run cap leaves from started at now, never
// below zero: the shared budget a wait of the run's own comes out of, so no wait
// outlasts the run it belongs to.
func RunBudgetLeft(started, now time.Time) time.Duration {
	return max(0, nfr1RunCap-now.Sub(started))
}

// effectiveAgentTimeout is configured, or defaultAgentTimeout when it is unset.
func effectiveAgentTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return defaultAgentTimeout
	}
	return configured
}

// withinBudget reports whether another round may still be started within
// budget, measured from started, without risking NFR-1's run-duration cap.
func withinBudget(started, now time.Time, budget time.Duration) bool {
	return now.Sub(started) < budget
}

// roundTimeout is a round's own deadline within budget, measured from started —
// the smaller of the configured agent timeout and what the stage has left — and
// whether the stage has room to start a round at all: more than minStart of its
// budget left, zero for a stage that starts a round whatever remains. The
// deadline is clamped at zero so a spent stage is never a negative duration, and
// the caller must read the flag: runAgent turns a non-positive timeout into the
// full defaultAgentTimeout, so a spent stage that passed it on would buy 50
// minutes.
func roundTimeout(configured time.Duration, started, now time.Time, budget, minStart time.Duration) (time.Duration, bool) {
	remaining := budget - now.Sub(started)
	if remaining <= 0 || remaining < minStart {
		return 0, false
	}
	upper := effectiveAgentTimeout(configured)
	if remaining < upper {
		return remaining, true
	}
	return upper, true
}

// stageBudgetSpent names the stage whose own budget ran out and how much it
// had, so a review cut short reads differently from a build cut at 90 minutes.
func stageBudgetSpent(stage string, budget time.Duration) string {
	return fmt.Sprintf("the %s stage's %d-minute budget was spent", stage, int(budget.Minutes()))
}

// reviewBudget is how much of nfr1RunCap the review stage may spend from now:
// what the cap leaves once the time the run has already spent and the commit
// reserve are set aside, so a build that finished early leaves the review the
// time it did not use. It is never below nfr1ReviewFloor, so the review keeps
// the share the cap always gave it however late it begins.
func reviewBudget(runStarted, now time.Time) time.Duration {
	// A build that ran past its 90 minutes still gets the floor, so that run
	// can total more than nfr1RunCap: AC1 puts the floor ahead of the cap.
	return max(nfr1ReviewFloor, nfr1RunCap-now.Sub(runStarted)-nfr1CommitReserve)
}

// reviewBudgetSpent is the stop a review call, or the checks after a fix
// round, ends with when the review stage's budget ran out under it.
func reviewBudgetSpent(budget time.Duration) error {
	return stopWith(OutcomeStopped, StopRunBudget, errors.New(stageBudgetSpent("review", budget)))
}

// reviewCallError relabels a review call that timed out once the review
// stage's budget, measured from started, was spent at now: the deadline it hit
// was the stage's, not the agent's allowance, so the run records the budget
// running out rather than an agent timeout. It judges by the stage, not by the
// first attempt's deadline, because a backup model's deadline is re-clamped to
// what the stage has left when that attempt starts.
func reviewCallError(err error, now, started time.Time, budget time.Duration) error {
	var s *StopError
	if !errors.As(err, &s) || s.Reason != StopAgentTimeout || withinBudget(started, now, budget) {
		return err
	}
	return reviewBudgetSpent(budget)
}

// checkFeedbackPrompt is what a rebuild or repair round receives: the failing
// gate's name and output, asking for a fix and nothing else.
func checkFeedbackPrompt(gate, output string) string {
	return "The `" + gate + "` check failed. Fix it and make no other change.\n\n```\n" +
		truncate(output, checkFeedbackLimit) + "\n```"
}

// fixPrompt is what a fix round of the pre-open loop receives: its findings —
// the review pass's and the guard probe's together — asking for a fix and
// nothing else.
func fixPrompt(findings string) string {
	return "A review of this change found the following. Address it and make no unrelated change.\n\n" + findings
}

// checkGiveUpDetail is the pre-PR loop's report when the checks never
// passed: the last failing gate and its output, so the PR names it (FR-5).
func checkGiveUpDetail(gate, output string, rounds int) string {
	return fmt.Sprintf("checks: %s still failing after %d round(s)\n\n%s", gate, rounds, truncate(output, checkFeedbackLimit))
}

// reviewFailureDetail is the pre-PR loop's report when the review pass
// itself never reached a verdict — an unparseable response, an agent
// timeout, an upload failure — distinct from findings it did produce. The
// build still proceeds to commit as a draft naming this (FR-5) rather than
// discarding otherwise-good build work over a review-phase failure.
func reviewFailureDetail(err error) string {
	return "review: " + truncate(detail(err), checkFeedbackLimit)
}

// buildOrder is the ordered models a build-phase call falls through: the run's
// model and its same-provider fallbacks, which an exhausted allowance also
// advances along rather than stopping the build.
func buildOrder(c BuildConfig) []string {
	return append([]string{c.Model}, fallbackModels(c.Model)...)
}

// lastFixRoundCost is the spend of the fix round that has just returned, as the
// PR notes price it: runAgent records the round's step before it returns, so the
// summary's newest step is that round's, and it carries the round's own number
// even when a model substitution advanced it. The caller reads it only after a
// round that returned without an error, so there is always a step to read.
func lastFixRoundCost(sum *Summary) *FixRoundCost {
	st := sum.Steps[len(sum.Steps)-1]
	return &FixRoundCost{Round: st.Round, Tokens: st.Tokens, DurationMS: st.DurationMS}
}

// runCheckLoop runs the build agent, then the repository's own checks,
// feeding a failing gate's output back to the same session and rebuilding,
// up to checkLoopMaxRounds attempts in all (FR-28). It starts no round, the
// first included, that the build stage's budget cannot finish — a budget
// already spent before the build's first round stops the run — and gives up,
// checks still failing, once that budget would not leave time for another
// rebuild; a round already started always finishes. A build that produced no
// change skips the checks entirely: nothing to review.
//
// It returns whether the checks passed, why not when they did not, the
// session to continue and the round the build agent last ran under. Each
// round's valid commit-message block replaces msg. Every round carries the
// build projection's rules head, so the resumed session's system prompt stays
// the same across the loop.
func runCheckLoop(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, rules, prompt string, msg *CommitMessage) (ok bool, detail, session string, round int, err error) {
	round = 1
	timeout, inBudget := roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now(), nfr1BuildBudget, 0)
	if !inBudget {
		return false, "", "", noFixRound, stopWith(OutcomeStopped, StopRunBudget,
			errors.New(stageBudgetSpent("build", nfr1BuildBudget)))
	}
	var text string
	text, session, round, err = runAgentInOrder(ctx, d, c, agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Rules: rules, Timeout: timeout}, buildOrder(c), d.Agent, wt.Dir, withCommitInstruction(prompt), sum, sum.StartedAt, nfr1BuildBudget, 0)
	if err != nil {
		return false, "", "", round, err
	}
	msg.adopt(text, c.Ticket.ID)

	dirty, err := wt.Dirty(ctx)
	if err != nil {
		return false, "", session, round, stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	if !dirty {
		return false, "", session, round, nil
	}

	var lastGate, lastOutput string
	for rebuildRound := 1; ; rebuildRound++ {
		gate, output, cerr := checksFormatted(ctx, d, sum, wt, round)
		if cerr != nil {
			return false, "", session, round, cerr
		}
		if gate == "" {
			return true, "", session, round, nil
		}
		lastGate, lastOutput = gate, output
		timeout, inBudget := roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now(), nfr1BuildBudget, 0)
		if rebuildRound >= checkLoopMaxRounds || !inBudget {
			break
		}
		round++
		call := agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Session: session, Rules: rules, Detail: gate,
			Timeout: timeout}
		text, session, round, err = runAgentInOrder(ctx, d, c, call, buildOrder(c), d.Agent, wt.Dir, withCommitInstruction(checkFeedbackPrompt(gate, output)), sum, sum.StartedAt, nfr1BuildBudget, 0)
		if err != nil {
			return false, "", session, round, err
		}
		msg.adopt(text, c.Ticket.ID)
	}
	return false, checkGiveUpDetail(lastGate, lastOutput, round), session, round, nil
}

// timedChecks runs d.Checks once against the build round's work, adding its
// time to the run's checks duration and logging the round. The worktree's
// changed paths travel with it, so a check that only applies to part of the
// repository — the web job's steps — knows whether it applies to this run.
func timedChecks(ctx context.Context, d BuildDeps, sum *Summary, wt Worktree, round int) (gate, output string, err error) {
	changed, err := wt.PendingFiles(ctx)
	if err != nil {
		return "", "", err
	}
	start := d.Now()
	gate, output, err = d.Checks(ctx, wt.Dir, changed)
	elapsed := d.Now().Sub(start).Milliseconds()
	sum.DurationsMS[checksDuration] += elapsed
	d.Logger.Info("checkRound", "round", round, "failedGate", gate, "durationMS", elapsed, "ran", err == nil)
	return gate, output, err
}

// checksFormatted is timedChecks with a formatter pass: a failing gofmt or
// web-fmt-check gate is fixed in place and the checks run again, spending no
// rebuild round. A gate still failing after its pass is returned for the build
// agent. Every error it returns is a *StopError.
func checksFormatted(ctx context.Context, d BuildDeps, sum *Summary, wt Worktree, round int) (gate, output string, err error) {
	formatted := map[string]bool{}
	for {
		gate, output, err = timedChecks(ctx, d, sum, wt, round)
		if err != nil {
			return "", "", stopWith(OutcomeInfraFailure, StopChecksRun, err)
		}
		if d.Format == nil || !isFormatGate(gate) || formatted[gate] {
			return gate, output, nil
		}
		formatted[gate] = true
		if err := formatInPlace(ctx, d, sum, wt, gate, round); err != nil {
			return "", "", err
		}
	}
}

// formatInPlace runs d.Format for gate and stops the run when it left a path
// pending that was not pending before.
func formatInPlace(ctx context.Context, d BuildDeps, sum *Summary, wt Worktree, gate string, round int) error {
	before, err := wt.PendingFiles(ctx)
	if err != nil {
		return stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	start := d.Now()
	if err := d.Format(ctx, wt.Dir, gate); err != nil {
		return stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	elapsed := d.Now().Sub(start).Milliseconds()
	sum.DurationsMS[checksDuration] += elapsed
	after, err := wt.PendingFiles(ctx)
	if err != nil {
		return stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	if outside := slices.DeleteFunc(slices.Clone(after), func(p string) bool { return slices.Contains(before, p) }); len(outside) > 0 {
		return stopWith(OutcomeStopped, StopFormatterOutsideDiff,
			fmt.Errorf("the %s formatter changed files outside the run's diff: %s", gate, strings.Join(outside, ", ")))
	}
	d.Logger.Info("formatterRan", "round", round, "gate", gate, "durationMS", elapsed)
	return nil
}

// checksWithin runs the checks on what round changed under deadline — what the
// budget that paid for round leaves from now — so a round started near its
// budget's end cannot run them past it. A budget that ran out under the checks
// is spent, never an infrastructure failure.
func checksWithin(ctx context.Context, d BuildDeps, wt Worktree, sum *Summary, deadline time.Time, round int, spent error) (gate, output string, err error) {
	cctx, cancel := context.WithTimeout(ctx, deadline.Sub(d.Now()))
	defer cancel()
	gate, output, err = checksFormatted(cctx, d, sum, wt, round)
	if ctx.Err() == nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return "", "", spent
	}
	return gate, output, err
}

// checkDetail is how the PR's notes carry a gate still failing after a fix
// round, empty when the checks passed.
func checkDetail(gate, output string) string {
	if gate == "" {
		return ""
	}
	return "checks: " + gate + " failing after the fix round\n\n" + truncate(output, checkFeedbackLimit)
}

// repairBudget is the run cap less reserveRecheckFloor: what a round paid from
// the commit reserve may spend, from the run's start — the review-only re-check
// the last fix round gets, and a repair round with the checks after it.
func repairBudget() time.Duration {
	return nfr1RunCap - reserveRecheckFloor
}

// repairCapSpent is the stop a repair round's checks end with when repairBudget
// ran out under them: the reserve the round was paid from is gone, so the run
// stops on its cap rather than spending what the commit steps keep.
func repairCapSpent() error {
	return stopWith(OutcomeStopped, StopRunBudget,
		fmt.Errorf("the run spent its %d-minute cap down to the %d minutes its commit steps keep",
			int(nfr1RunCap/time.Minute), int(reserveRecheckFloor/time.Minute)))
}

// repairRound gives a fix round's failing compile-level gate one repair round
// on that gate's own output: the build agent's session continued, the gate as
// its step's detail so its time and tokens reach the run record. It starts only
// while repairBudget leaves repairRoundFloor, and its own deadline stops
// repairCheckRoom short of that budget so the checks after it still have room.
// ran is false when it started none. round and session advance to the round it
// ran as and the session it reported, and a valid commit-message block from it
// replaces msg.
func repairRound(ctx context.Context, d BuildDeps, c BuildConfig, dir string, sum *Summary, rules, session string, round int,
	msg *CommitMessage, gate, output string) (newSession string, newRound int, ran bool, err error) {
	// The round starts only while repairBudget leaves repairRoundFloor, and it
	// stops repairCheckRoom short of that budget so the checks after it have
	// room: the gate and the deadline are both expressed against the budget less
	// that room, which leaves the rule itself unchanged.
	budget, minStart := repairBudget()-repairCheckRoom, repairRoundFloor-repairCheckRoom
	timeout, inBudget := roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now(), budget, minStart)
	if !inBudget {
		d.Logger.Info("repairRoundSkipped", "gate", gate, "reason", "budget")
		return session, round, false, nil
	}
	round++
	call := agentCall{Phase: PhaseBuild, Round: round, Model: buildOrder(c)[0], Session: session, Rules: rules, Detail: gate, Timeout: timeout}
	text, resumed, round, err := runAgentInOrder(ctx, d, c, call, buildOrder(c), d.Agent, dir,
		withCommitInstruction(checkFeedbackPrompt(gate, output)), sum, sum.StartedAt, budget, minStart)
	if err != nil {
		return session, round, true, err
	}
	msg.adopt(text, c.Ticket.ID)
	d.Logger.Info("repairRoundRan", "gate", gate, "round", round)
	return resumed, round, true, nil
}

// reviewPass runs one review pass over the worktree's pending diff on the
// review models in order and returns its findings, each sourced to the model
// that answered, empty when the diff satisfies the ticket. It starts a fresh
// session, so rules is the whole of what that session carries — the build
// projection's rules, which is what the pass checks the diff against. timeout
// is what the review's own budget leaves the pass, and started and budget are
// the review stage's, so a pass that steps to its backup model is bounded by
// the review's budget rather than the build's, and starts no backup attempt
// with under reviewStartRoom of it left.
func reviewPass(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, round int, rules string, timeout time.Duration, started time.Time, budget time.Duration) ([]pendingFinding, error) {
	diff, err := wt.DiffPending(ctx)
	if err != nil {
		return nil, stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	call := agentCall{Phase: PhaseReview, Round: round, Model: c.ReviewModels[0], Rules: rules, Timeout: timeout}
	text, _, _, err := runAgentInOrder(ctx, d, c, call, c.ReviewModels, d.ReviewAgent, wt.Dir, PreOpenReviewPrompt(diff, c.Ticket), sum, started, budget, reviewStartRoom)
	if err != nil {
		return nil, err
	}
	findings, ferr := ParseReviewFindings(text)
	if ferr != nil {
		return nil, stopWith(OutcomeStopped, StopReviewInvalid, ferr)
	}
	// runAgent records each attempt's step before it returns, so the newest step
	// is the model that answered.
	return sourced(reviewPending(findings), sum.Steps[len(sum.Steps)-1].Model), nil
}

// fixReviewHeadroom is what a fix-review pass leaves of the review stage's
// budget: the start room of the fix round its findings may need and of the
// review pass's re-check after it, so the findings-only pass never takes the
// time the pass that decides the verdict needs.
const fixReviewHeadroom = 2 * reviewStartRoom

// fixReviewPass runs the first fix-review model once over the worktree's
// pending diff and returns its findings that known does not already hold, at
// most maxReviewFindings, each sourced to that model. It never decides the
// verdict: a pass with under reviewStartRoom of the budget left once
// fixReviewHeadroom is set aside, or one that fails, is logged with its reason
// and returns nothing, so the loop goes on as the review pass decided. Its time
// is recorded under its own phase.
func fixReviewPass(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, rules string, started time.Time, budget time.Duration, known map[string]bool) []pendingFinding {
	if len(c.FixReviewModels) == 0 {
		return nil
	}
	model := c.FixReviewModels[0]
	timeout, inBudget := roundTimeout(c.AgentTimeout, started, d.Now(), budget-fixReviewHeadroom, reviewStartRoom)
	if !inBudget {
		d.Logger.Info("fixReviewSkipped", "model", model, "reason", "budget")
		return nil
	}
	start := d.Now()
	defer func() { sum.DurationsMS[string(PhaseFixReview)] += d.Now().Sub(start).Milliseconds() }()
	diff, err := wt.DiffPending(ctx)
	if err != nil {
		d.Logger.Warn("fixReviewFailed", "model", model, "reason", "diff-unread", "err", truncate(err.Error(), logErrorLimit))
		return nil
	}
	call := agentCall{Phase: PhaseFixReview, Round: 1, Model: model, Rules: rules, Timeout: timeout}
	text, _, err := runAgent(ctx, d, c, call, bindModel(d.ReviewAgent, model), wt.Dir, PreOpenReviewPrompt(diff, c.Ticket), sum)
	if err != nil {
		reason := "agent-error"
		var s *StopError
		if errors.As(err, &s) {
			reason = string(s.Reason)
		}
		d.Logger.Warn("fixReviewFailed", "model", model, "reason", reason, "err", truncate(detail(err), logErrorLimit))
		return nil
	}
	found, err := ParseReviewFindings(text)
	if err != nil {
		d.Logger.Warn("fixReviewFailed", "model", model, "reason", string(StopReviewInvalid), "err", err.Error())
		return nil
	}
	var kept []pendingFinding
	duplicates := 0
	seen := maps.Clone(known)
	for _, f := range sourced(reviewPending(found), model) {
		if seen[f.key] {
			duplicates++
			continue
		}
		seen[f.key] = true
		f.unrechecked = true
		kept = append(kept, f)
	}
	if len(kept) > maxReviewFindings {
		d.Logger.Info("fixReviewFindingsCut", "model", model, "found", len(kept), "kept", maxReviewFindings)
		kept = kept[:maxReviewFindings]
	}
	d.Logger.Info("fixReviewRan", "model", model, "findings", len(kept), "duplicates", duplicates)
	return kept
}

// sourced is findings with each one's source set to model.
func sourced(findings []pendingFinding, model string) []pendingFinding {
	for i := range findings {
		findings[i].source = model
	}
	return findings
}

// detailParts joins a loop detail's parts with the separator the pre-PR loop's
// messages use, dropping empty parts.
func detailParts(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "; ")
}

// pendingFinding is one finding the pre-open review made before the fix round:
// the line the fix round receives, and the key a re-check matches it by — a
// guard's file and header, or a review finding's own text.
type pendingFinding struct {
	key  string
	text string
	// stable is set when the key survives a rewording — a guard's file and
	// header — and unset for a model's finding, whose text a re-review may word
	// differently.
	stable bool
	// source is the model that reported the finding, empty for a guard.
	source string
	// unrechecked is set for a fix-review model's finding: no later pass runs
	// that model, so a clean re-review does not show it fixed.
	unrechecked bool
}

// findingsText is findings as the fix round receives them: one per line, empty
// when there are none.
func findingsText(findings []pendingFinding) string {
	lines := make([]string, len(findings))
	for i, f := range findings {
		lines[i] = f.text
	}
	return strings.Join(lines, "\n")
}

// reviewPending is the review agent's findings block as findings, one per line,
// the way the block lists them. Each line keys itself, so a finding a re-review
// repeats is recognizable as the same one.
func reviewPending(findings string) []pendingFinding {
	return lineFindings(strings.Split(findings, "\n"))
}

// lineFindings is findings given as lines — a review's block, or a state record's
// open list — each keying itself, with blank lines dropped.
func lineFindings(lines []string) []pendingFinding {
	var out []pendingFinding
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, pendingFinding{key: line, text: line})
		}
	}
	return out
}

// findingKeys is the keys of findings, as the set a re-check reports.
func findingKeys(findings []pendingFinding) map[string]bool {
	keys := make(map[string]bool, len(findings))
	for _, f := range findings {
		keys[f.key] = true
	}
	return keys
}

// resolveFindings is every finding the fix round was given, each with how the
// run left it: still open when the re-check reported its key again, fixed when
// the re-check ran and did not, and addressed but not re-checked when that
// re-check never ran. fixRound is the round that addressed them, or noFixRound
// when no round ran at all — the findings then stay open.
func resolveFindings(pending []pendingFinding, stillOpen map[string]bool, fixRound int, rechecked bool) []ReviewFinding {
	rows := make([]ReviewFinding, 0, len(pending))
	for _, f := range pending {
		res := findingResolution(f.key, stillOpen, fixRound, rechecked)
		if f.unrechecked && fixRound == noFixRound {
			res = notActedOnText
		}
		rows = append(rows, ReviewFinding{Finding: f.text, Resolution: res, Source: f.source})
	}
	return rows
}

// noFixRound is the round resolveFindings takes when a run's fix round never
// ran at all: nothing was done about the findings, so every one of them stays
// open, whatever the round it would have been given.
const noFixRound = 0

// findingResolution is how the run left one finding.
func findingResolution(key string, stillOpen map[string]bool, fixRound int, rechecked bool) string {
	switch {
	case stillOpen[key], fixRound == noFixRound:
		return stillOpenText
	case rechecked:
		return fixedIn(fixRound)
	default:
		return notRecheckedIn(fixRound)
	}
}

// notRecheckedIn is how a row reads when fixRound addressed its finding and no
// pass that could report it again ran after.
func notRecheckedIn(fixRound int) string {
	return fmt.Sprintf("addressed in round %d, not re-checked", fixRound)
}

// notActedOnText is a fix-review finding's resolution when the review stage's
// budget left no fix round: it stays open without holding the run.
const notActedOnText = "not acted on: no fix round had room"

// stillOpenText is a row's resolution when nothing closed its finding: no fix
// round ran, or the last re-check still reports it.
const stillOpenText = "still open"

// stillOpenAfter is how a row reads when the re-check after fixRound reports
// its finding and more than one round ran.
func stillOpenAfter(fixRound int) string {
	return fmt.Sprintf("still open after round %d", fixRound)
}

// fixedResolutionPrefix begins the one resolution that closes a finding.
const fixedResolutionPrefix = "fixed in round "

// fixedIn is how a row reads when fixRound resolved its finding.
func fixedIn(fixRound int) string {
	return fixedResolutionPrefix + strconv.Itoa(fixRound)
}

// openFindings is how many of rows the run left unfixed: every resolution but
// fixedIn's, whether the finding is still reported or was never re-checked.
func openFindings(rows []ReviewFinding) int {
	open := 0
	for _, r := range rows {
		if !strings.HasPrefix(r.Resolution, fixedResolutionPrefix) {
			open++
		}
	}
	return open
}

// fixRoundResult is one fix round of the pre-open loop and the re-check that
// followed it: the findings the round was given, the ones that re-check still
// reported, the round its own step ran as, and whether the review stage's
// budget left room to re-check it at all.
type fixRoundResult struct {
	round     int
	issued    []pendingFinding
	open      []pendingFinding
	rechecked bool
}

// reviewRows is what the PR body publishes (AC5, AC2): every finding a fix round
// was given and how the run left it, then any finding the last re-check made that
// no fix round saw — a guard the round itself added uncovered, say.
//
// Each round's own re-check settles the findings it was given, and a later round
// settles them again, so a finding the first round left open and the second fixed
// reads as fixed in the second, and one the last re-check still reports reads as
// open after that round. Round numbers are the round each step ran as.
func reviewRows(rounds []fixRoundResult) []ReviewFinding {
	var issued []pendingFinding
	at := map[string]int{}
	for _, r := range rounds {
		for _, f := range r.issued {
			if _, seen := at[f.key]; !seen {
				at[f.key] = len(issued)
				issued = append(issued, f)
			}
		}
	}
	rows := make([]ReviewFinding, len(issued))
	for i, f := range issued {
		rows[i] = ReviewFinding{Finding: f.text, Source: f.source}
	}
	for i, r := range rounds {
		open := findingKeys(r.open)
		// A model's finding is matched only by its text, so while this round's
		// re-check still reports any finding of its own, an earlier one it did
		// not repeat word for word may be that same finding reworded: it is not
		// shown as fixed.
		reworded := slices.ContainsFunc(r.open, func(f pendingFinding) bool { return !f.stable })
		last := i == len(rounds)-1
		for _, f := range r.issued {
			res := findingResolution(f.key, open, r.round, r.rechecked)
			switch {
			case f.unrechecked && res == fixedIn(r.round):
				res = notRecheckedIn(r.round)
			case !f.stable && reworded && res == fixedIn(r.round):
				res = fmt.Sprintf("addressed in round %d; the re-review still reported findings", r.round)
			case res == stillOpenText && r.rechecked && last && len(rounds) > 1:
				res = stillOpenAfter(r.round)
			}
			rows[at[f.key]].Resolution = res
		}
	}
	for _, round := range rounds {
		for _, f := range round.open {
			// Anything a re-check reports that a later round was given is in at
			// already, so only the last round's own new findings are listed here.
			if _, seen := at[f.key]; seen {
				continue
			}
			at[f.key] = len(rows)
			res := stillOpenText
			if len(rounds) > 1 {
				res = stillOpenAfter(round.round)
			}
			rows = append(rows, ReviewFinding{Finding: f.text, Resolution: res, Source: f.source})
		}
	}
	return rows
}

// openUnprobed is the guards among issued that a re-scan reached no verdict for:
// a guard no probe has shown covered stays open, never fixed.
func openUnprobed(issued, unprobed []pendingFinding) []pendingFinding {
	var open []pendingFinding
	for _, u := range unprobed {
		if slices.ContainsFunc(issued, func(f pendingFinding) bool { return f.key == u.key }) {
			open = append(open, u)
		}
	}
	return open
}

// recheckFromReserve is the review-only pass the last fix round gets when the
// review stage's budget left no room to re-check it: one pass, started only
// while repairBudget leaves reviewStartRoom, so the commit reserve pays for it
// and the steps after the review keep what they need. open is the findings it
// leaves: surviving, the guards the round's own scan found still uncovered, and
// what the pass reports. ok is false when the pass could not run or failed,
// which leaves the round as not re-checked — the run goes on either way, so its
// error is logged and not returned.
func recheckFromReserve(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, round int, rules string, surviving []pendingFinding) (open []pendingFinding, ok bool) {
	budget := repairBudget()
	timeout, inBudget := roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now(), budget, reviewStartRoom)
	if !inBudget {
		return nil, false
	}
	remaining, err := reviewPass(ctx, d, c, wt, sum, round, rules, timeout, sum.StartedAt, budget)
	if err != nil {
		d.Logger.Warn("reserveRecheckFailed", "round", round, "err", err.Error())
		return nil, false
	}
	d.Logger.Info("reserveRecheckRan", "round", round)
	return append(surviving, remaining...), true
}

// runReview runs the pre-PR loop (FR-28) once the checks have passed: the
// guards the diff adds are broken in turn, mechanically, to find the ones no
// test covers (AC1), and a model other than the builder's checks the diff and
// the code around it against the ticket's acceptance criteria (AC2, AC3, AC4).
// Findings from either get a fix round, fed back to the build agent's own
// session the same way a check failure is, and the summary carries each one
// with how the run left it (AC5). The checks then run once, to catch a gate the
// fix round newly broke, and — when the review stage's own budget still allows
// it — the guards are probed again and the review re-run on the fixed diff.
//
// A gate the round left failing that is compile-level — vet, web-build, or a
// format gate its own in-place pass did not fix — gets one repair round on that
// gate's output before the guards are probed and the diff re-reviewed, so the
// re-review never reads code that does not build (the agent's sandbox has no
// toolchain to see it). One round per run, started only while repairBudget
// leaves repairRoundFloor and stopping repairCheckRoom short of it so its own
// checks have room; a test or lint failure gets none, and a gate still failing
// after the repair is recorded in the PR's notes.
//
// A re-check that still reports a finding, the round's own or one it newly
// made, starts one more fix round on the same session, up to maxFixRounds in
// all: a finding the last round still leaves open stays open and holds the PR
// as a draft. So the detail lists only what the last re-check reports. A clean
// re-check with the checks passing leaves the run ready (FR-5), and a run whose
// review found nothing is untouched by any of this (AC6).
//
// The review's budget runs from when the review begins, after the guard scan,
// and is what the run cap still leaves once the time the run has spent and the
// commit reserve are set aside — never below nfr1ReviewFloor — so a build that
// finished early leaves the review the time it did not use. No pass, fix round
// or re-check starts with under reviewStartRoom of that budget left: the run
// gives up — running no fix round — once the budget would not leave enough time
// for one, and skips a re-check alone once it would not leave enough time for
// that, which first gets recheckFromReserve's one pass from the commit reserve
// and otherwise labels the findings as addressed by a fix round rather than
// re-checked. A valid commit-message block from a fix round replaces msg. Each
// review pass starts a fresh session, so it carries no history of its own;
// rules is the build projection's rules, which the review needs to check the
// diff against them, and every fix round resumes the build agent's own session
// with the same rules it was built under — dropping them there would change the
// resumed session's system prompt mid-conversation.
func runReview(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, rules, session string, buildRound int, msg *CommitMessage) (ready bool, detail string, err error) {
	// The guard scan is charged to the build stage: it probes the diff the
	// review reads, so it stops once the build's budget is spent and its time
	// stays outside the review's own budget, which begins below.
	settled := make(map[string]bool)
	guards, unprobed, err := scanGuards(ctx, d, wt, sum.StartedAt, nfr1BuildBudget, settled)
	if err != nil {
		return false, "", err
	}
	if len(unprobed) > 0 {
		d.Logger.Info("guardsUntested", "count", len(unprobed))
	}
	sum.GuardsUntested = len(unprobed)
	reviewStarted := d.Now()
	budget := reviewBudget(sum.StartedAt, reviewStarted)
	// The budget is whole at the instant the stage starts and never below
	// nfr1ReviewFloor, which is more than reviewStartRoom, so the first pass
	// always has room to start; only the fix rounds and the re-checks below run
	// late enough in the stage to need the start floor.
	passTimeout := min(effectiveAgentTimeout(c.AgentTimeout), budget)
	found, err := reviewPass(ctx, d, c, wt, sum, 1, rules, passTimeout, reviewStarted, budget)
	if err != nil {
		return false, "", reviewCallError(err, d.Now(), reviewStarted, budget)
	}
	pending := append(guards, found...)
	pending = append(pending, fixReviewPass(ctx, d, c, wt, sum, rules, reviewStarted, budget, findingKeys(pending))...)
	if len(pending) == 0 {
		return true, "", nil
	}

	rounds := make([]fixRoundResult, 0, maxFixRounds)
	findings, checksDetail := pending, ""
	round := buildRound
	skipped := ""
	// repaired is the run's one repair round: whichever fix round leaves a
	// compile-level gate failing spends it, and a later round gets none.
	repaired := false
	for n := 1; n <= maxFixRounds; n++ {
		round++
		timeout, inBudget := roundTimeout(c.AgentTimeout, reviewStarted, d.Now(), budget, reviewStartRoom)
		if !inBudget {
			// A stage with no budget left, or too little to finish a round,
			// starts no round: the findings stay open and the PR says so, exactly
			// as it did before there was a second one.
			skipped = "fix round skipped: " + stageBudgetSpent("review", budget)
			break
		}
		text := findingsText(findings)
		fixes := buildOrder(c)
		call := agentCall{Phase: PhaseBuild, Round: round, Model: fixes[0], Session: session, Rules: rules, Detail: text,
			Timeout: timeout}
		fixText, resumed, ran, err := runAgentInOrder(ctx, d, c, call, fixes, d.Agent, wt.Dir, withCommitInstruction(fixPrompt(text)), sum, reviewStarted, budget, reviewStartRoom)
		if err != nil {
			return false, "", reviewCallError(err, d.Now(), reviewStarted, budget)
		}
		// A substitution inside the round advanced it, so the checks, the
		// re-review and the next round continue from the round that answered.
		round, session = ran, resumed
		msg.adopt(fixText, c.Ticket.ID)
		if n > 1 {
			// The one extra round this run took is priced in the PR notes, so the
			// owner sees what it cost before deciding to keep it.
			sum.ReviewSecondRound = lastFixRoundCost(sum)
		}

		// The checks get only what the review stage has left, so a fix round
		// started near the stage's end cannot run them into the commit reserve.
		gate, output, cerr := checksWithin(ctx, d, wt, sum, reviewStarted.Add(budget), round, reviewBudgetSpent(budget))
		if cerr != nil {
			return false, "", cerr
		}
		checksDetail = checkDetail(gate, output)

		// A compile-level gate the round left failing is the failure its
		// sandbox cannot see, so the gate's own output buys one repair round
		// before anything else reads the diff. The checks then run again, out of
		// the same reserve the round came from, and a gate still failing is
		// recorded in the PR's notes; no second round follows. The round is
		// priced by its own step, so ReviewSecondRound stays the second fix
		// round's cost.
		if gate != "" && !repaired && compileLevelGate(gate) {
			repaired = true
			var ranRepair bool
			session, round, ranRepair, err = repairRound(ctx, d, c, wt.Dir, sum, rules, session, round, msg, gate, output)
			if err != nil {
				return false, "", reviewCallError(err, d.Now(), reviewStarted, budget)
			}
			if ranRepair {
				deadline := sum.StartedAt.Add(repairBudget())
				if gate, output, cerr = checksWithin(ctx, d, wt, sum, deadline, round, repairCapSpent()); cerr != nil {
					return false, "", cerr
				}
				checksDetail = checkDetail(gate, output)
			}
		}

		// The post-fix scan is charged to the review stage and probes only the
		// guards still open as findings and those a fix round added: a guard an
		// earlier scan settled is not probed again, so the re-review's time goes
		// to what the fix round changed. Both re-checks then run under the one
		// gate below: a finding is open, or fixed, only once its own re-check
		// has said so.
		surviving, unprobed, err := scanGuards(ctx, d, wt, reviewStarted, budget, settled)
		if err != nil {
			return false, "", err
		}
		sum.GuardsUntested = len(unprobed)
		surviving = append(surviving, openUnprobed(findings, unprobed)...)
		recheckTimeout, inBudget := roundTimeout(c.AgentTimeout, reviewStarted, d.Now(), budget, reviewStartRoom)
		if !inBudget {
			rounds = append(rounds, fixRoundResult{round: round, issued: findings})
			if open, ok := recheckFromReserve(ctx, d, c, wt, sum, round, rules, surviving); ok {
				rounds[len(rounds)-1].open, rounds[len(rounds)-1].rechecked = open, true
				sum.setReviewFindings(reviewRows(rounds))
				if len(open) == 0 {
					return checksDetail == "", checksDetail, nil
				}
				return false, detailParts("findings open: "+findingsText(open), checksDetail), nil
			}
			sum.setReviewFindings(reviewRows(rounds))
			return false, detailParts("findings addressed by a fix round, not re-reviewed: "+
				text+" ("+stageBudgetSpent("review", budget)+")", checksDetail), nil
		}
		remaining, err := reviewPass(ctx, d, c, wt, sum, round, rules, recheckTimeout, reviewStarted, budget)
		if err != nil {
			return false, "", reviewCallError(err, d.Now(), reviewStarted, budget)
		}
		open := append(surviving, remaining...)
		rounds = append(rounds, fixRoundResult{round: round, issued: findings, open: open, rechecked: true})
		if len(open) == 0 {
			sum.setReviewFindings(reviewRows(rounds))
			return checksDetail == "", checksDetail, nil
		}
		findings = open
	}
	if len(rounds) == 0 {
		// The budget left no round at all: nothing was done about the findings.
		sum.setReviewFindings(resolveFindings(pending, nil, noFixRound, false))
		if !slices.ContainsFunc(pending, func(f pendingFinding) bool { return !f.unrechecked }) {
			// The review pass was clean, and a fix-review model never decides the
			// verdict, so its findings alone do not hold the run.
			d.Logger.Info("fixReviewFindingsNotActedOn", "count", len(pending), "reason", "budget")
			return true, "", nil
		}
		return false, "findings open: " + findingsText(pending) + " (" + skipped + ")", nil
	}
	sum.setReviewFindings(reviewRows(rounds))
	return false, detailParts("findings open: "+findingsText(findings), skipped, checksDetail), nil
}
