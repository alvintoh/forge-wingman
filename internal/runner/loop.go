package runner

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// checkLoopMaxRounds is FR-28's "at most 3 rounds": the build agent's first
// attempt plus at most two rebuilds fed the failing gate's output. Counts
// rebuild PASSES, not agentCall.Round — a same-pass model substitution
// advances Round without spending one of these three.
const checkLoopMaxRounds = 3

// checkFeedbackLimit bounds how much of a gate's output a rebuild round or
// the loop's give-up detail carries.
const checkFeedbackLimit = 8 << 10

const (
	// nfr1RunCap is NFR-1's whole-run duration ceiling — model.yml's build
	// job's own timeout-minutes.
	nfr1RunCap = 60 * time.Minute
	// nfr1CommitReserve is how much of nfr1RunCap the pre-PR loop leaves
	// unspent, for the commit, bundle and report steps that still run after it.
	nfr1CommitReserve = 5 * time.Minute
	// checkLoopBudget is how much of nfr1RunCap the loop may spend before
	// giving up, checks still failing or findings still open, rather than
	// risk the run past its cap.
	checkLoopBudget = nfr1RunCap - nfr1CommitReserve
)

// effectiveAgentTimeout is configured, or defaultAgentTimeout when it is unset.
func effectiveAgentTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return defaultAgentTimeout
	}
	return configured
}

// withinBudget reports whether another round may still be started without
// risking NFR-1's run-duration cap.
func withinBudget(started, now time.Time) bool {
	return now.Sub(started) < checkLoopBudget
}

// roundTimeout is a round's own deadline, bounded by whichever is smaller:
// the configured agent timeout, or what remains of checkLoopBudget — so a
// single round can never by itself carry the run past NFR-1's cap.
func roundTimeout(configured time.Duration, started, now time.Time) time.Duration {
	remaining := checkLoopBudget - now.Sub(started)
	upper := effectiveAgentTimeout(configured)
	if remaining < upper {
		return remaining
	}
	return upper
}

// checkFeedbackPrompt is what a rebuild round receives: the failing gate's
// name and output, asking for a fix and nothing else.
func checkFeedbackPrompt(gate, output string) string {
	return "The `" + gate + "` check failed. Fix it and make no other change.\n\n```\n" +
		truncate(output, checkFeedbackLimit) + "\n```"
}

// fixPrompt is what the pre-open loop's one fix round receives: its findings —
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

// buildOrder is the ordered models a build-phase call falls through: the
// ticket's free models on a free-tier run, so a model that is withdrawn or
// unavailable falls to the next in list order (AC1); else the run's model and
// its same-provider fallbacks, which an exhausted allowance also advances
// along rather than stopping the build.
func buildOrder(c BuildConfig) []string {
	if free := c.Ticket.FreeModels; len(free) > 0 {
		return free
	}
	return append([]string{c.Model}, fallbackModels(c.Model)...)
}

// lastBuildModel is the model the build phase last ran — the one that built the
// run, since a successful attempt returns at once, so the newest build step is
// the successful one.
func lastBuildModel(sum *Summary) string {
	for i := len(sum.Steps) - 1; i >= 0; i-- {
		if sum.Steps[i].Phase == PhaseBuild {
			return sum.Steps[i].Model
		}
	}
	return ""
}

// reviewOrder is the review phase's ordered models: the free models after the
// one that built the run on a free-tier run, so the review is never the builder
// checking its own work (AC2), empty once the build used every free model; else
// the configured review list.
func reviewOrder(c BuildConfig, sum *Summary) []string {
	if len(c.Ticket.FreeModels) == 0 {
		return c.ReviewModels
	}
	i := slices.Index(c.Ticket.FreeModels, lastBuildModel(sum))
	if i < 0 {
		return nil
	}
	return c.Ticket.FreeModels[i+1:]
}

// fixOrder is the ordered models the review's fix round resumes the build
// session on: the one that built the run, on a free-tier run, so the round
// cannot consume a free model the re-review needs (AC2); else the starting
// model and its same-provider fallbacks.
func fixOrder(c BuildConfig, sum *Summary) []string {
	if len(c.Ticket.FreeModels) > 0 {
		return []string{lastBuildModel(sum)}
	}
	return buildOrder(c)
}

// noReviewDetail is the pre-PR loop's report when a free-tier run's build used
// the last of the plan's free models, so no model was left to review its diff
// (AC2): the PR opens as a draft.
const noReviewDetail = "review: the build used every free model, so none was left to review it"

// runCheckLoop runs the build agent, then the repository's own checks,
// feeding a failing gate's output back to the same session and rebuilding,
// up to checkLoopMaxRounds attempts in all (FR-28). It stops early, checks
// still failing, once NFR-1's run-duration budget would not leave enough
// time for another round — a round already started always finishes. A build
// that produced no change skips the checks entirely: nothing to review.
//
// It returns whether the checks passed, why not when they did not, the
// session to continue and the round the build agent last ran under. Each
// round's valid commit-message block replaces msg. Every round carries the
// build projection's rules head, so the resumed session's system prompt stays
// the same across the loop.
func runCheckLoop(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, rules, prompt string, msg *CommitMessage) (ok bool, detail, session string, round int, err error) {
	round = 1
	call := agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Rules: rules, Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now())}
	var text string
	text, session, round, err = runAgentInOrder(ctx, d, c, call, buildOrder(c), d.Agent, wt.Dir, withCommitInstruction(prompt), sum)
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
		gate, output, cerr := timedChecks(ctx, d, sum, wt, round)
		if cerr != nil {
			return false, "", session, round, stopWith(OutcomeInfraFailure, StopChecksRun, cerr)
		}
		if gate == "" {
			return true, "", session, round, nil
		}
		lastGate, lastOutput = gate, output
		now := d.Now()
		if rebuildRound >= checkLoopMaxRounds || !withinBudget(sum.StartedAt, now) {
			break
		}
		round++
		call := agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Session: session, Rules: rules, Detail: gate,
			Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, now)}
		text, session, round, err = runAgentInOrder(ctx, d, c, call, buildOrder(c), d.Agent, wt.Dir, withCommitInstruction(checkFeedbackPrompt(gate, output)), sum)
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

// reviewPass runs one review pass over the worktree's pending diff on models
// in order and returns its findings, empty when the diff satisfies the ticket.
// models is non-empty: the caller has already decided the run has a model to
// review it. It starts a fresh session, so rules is the whole of what that
// session carries — the build projection's rules, which is what the pass checks
// the diff against.
func reviewPass(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, models []string, round int, rules string) (string, error) {
	diff, err := wt.DiffPending(ctx)
	if err != nil {
		return "", stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	call := agentCall{Phase: PhaseReview, Round: round, Model: models[0], Rules: rules,
		Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now())}
	text, _, _, err := runAgentInOrder(ctx, d, c, call, models, d.ReviewAgent, wt.Dir, PreOpenReviewPrompt(diff, c.Ticket), sum)
	if err != nil {
		return "", err
	}
	findings, ferr := ParseReviewFindings(text)
	if ferr != nil {
		return "", stopWith(OutcomeStopped, StopReviewInvalid, ferr)
	}
	return findings, nil
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
	var out []pendingFinding
	for _, line := range strings.Split(findings, "\n") {
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
		rows = append(rows, ReviewFinding{Finding: f.text, Resolution: findingResolution(f.key, stillOpen, fixRound, rechecked)})
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
		return "still open"
	case rechecked:
		return fmt.Sprintf("fixed in round %d", fixRound)
	default:
		return fmt.Sprintf("addressed in round %d, not re-checked", fixRound)
	}
}

// reviewRows is what the PR body publishes (AC5): every finding the fix round
// was given and how the run left it, then any finding the re-check made that
// the fix round never saw — a guard the round itself added uncovered, say.
func reviewRows(pending, open []pendingFinding, fixRound int) []ReviewFinding {
	rows := resolveFindings(pending, findingKeys(open), fixRound, true)
	seen := findingKeys(pending)
	for _, f := range open {
		if !seen[f.key] {
			rows = append(rows, ReviewFinding{Finding: f.text, Resolution: "still open"})
		}
	}
	return capFindings(rows)
}

// runReview runs the pre-PR loop (FR-28) once the checks have passed: the
// guards the diff adds are broken in turn, mechanically, to find the ones no
// test covers (AC1), and a model other than the builder's checks the diff and
// the code around it against the ticket's acceptance criteria (AC2, AC3, AC4).
// models is the review's ordered list, which a free-tier run derives from its
// free list; an empty one is a run with no model left to review it (AC2), which
// opens as a draft instead. Findings from either get exactly one fix round, fed
// back to the build agent's own session the same way a check failure is, and
// the summary carries each one with how the run left it (AC5). The checks then
// run once, to catch a gate the fix round newly broke, and — when NFR-1's
// run-duration budget still allows it — the guards are probed again and the
// review re-run on the fixed diff, so the detail lists only the findings the
// re-checks still report. A clean re-check with the checks passing leaves the
// run ready (FR-5), and a run whose review found nothing is untouched by any of
// this (AC6).
//
// It gives up — running neither the review nor the fix round — once the budget
// would not leave enough time for it, and skips the re-checks alone once it
// would not leave enough time for those, labelling the findings as addressed by
// a fix round rather than re-checked. A valid commit-message block from the fix
// round replaces msg. Each review pass starts a fresh session, so it carries no
// history of its own; rules is the build projection's rules, which the review
// needs to check the diff against them, and the fix round resumes the build
// agent's own session with the same rules it was built under — dropping them
// there would change the resumed session's system prompt mid-conversation.
func runReview(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, rules, session string, buildRound int, models []string, msg *CommitMessage) (ready bool, detail string, err error) {
	if len(models) == 0 {
		return false, noReviewDetail, nil
	}
	now := d.Now()
	if !withinBudget(sum.StartedAt, now) {
		return false, "review: skipped — NFR-1's run-duration budget was spent by the check loop", nil
	}
	guards, err := scanGuards(ctx, d, wt, sum)
	if err != nil {
		return false, "", err
	}
	found, err := reviewPass(ctx, d, c, wt, sum, models, 1, rules)
	if err != nil {
		return false, "", err
	}
	pending := append(guards, reviewPending(found)...)
	if len(pending) == 0 {
		return true, "", nil
	}
	findings := findingsText(pending)

	now = d.Now()
	if !withinBudget(sum.StartedAt, now) {
		sum.ReviewFindings = capFindings(resolveFindings(pending, nil, noFixRound, false))
		return false, "findings open: " + findings + " (fix round skipped: NFR-1's run-duration budget was spent)", nil
	}
	fixRound := buildRound + 1
	fixes := fixOrder(c, sum)
	fixCall := agentCall{Phase: PhaseBuild, Round: fixRound, Model: fixes[0], Session: session, Rules: rules, Detail: findings,
		Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, now)}
	fixText, _, _, err := runAgentInOrder(ctx, d, c, fixCall, fixes, d.Agent, wt.Dir, withCommitInstruction(fixPrompt(findings)), sum)
	if err != nil {
		return false, "", err
	}
	msg.adopt(fixText, c.Ticket.ID)

	gate, output, cerr := timedChecks(ctx, d, sum, wt, fixRound)
	if cerr != nil {
		return false, "", stopWith(OutcomeInfraFailure, StopChecksRun, cerr)
	}
	checksDetail := ""
	if gate != "" {
		checksDetail = "checks: " + gate + " failing after the fix round\n\n" + truncate(output, checkFeedbackLimit)
	}

	// Both re-checks run under one budget gate: they need the same time, and a
	// finding is open, or fixed, only once its own re-check has said so. Each
	// probe is itself bounded by what the budget leaves, so the guards cannot
	// spend the time the review re-run needs.
	now = d.Now()
	if !withinBudget(sum.StartedAt, now) {
		sum.ReviewFindings = capFindings(resolveFindings(pending, nil, fixRound, false))
		return false, detailParts("findings addressed by a fix round, not re-reviewed: "+findings, checksDetail), nil
	}
	surviving, err := scanGuards(ctx, d, wt, sum)
	if err != nil {
		return false, "", err
	}
	remaining, err := reviewPass(ctx, d, c, wt, sum, models, 2, rules)
	if err != nil {
		return false, "", err
	}
	open := append(surviving, reviewPending(remaining)...)
	sum.ReviewFindings = reviewRows(pending, open, fixRound)
	if len(open) == 0 {
		return checksDetail == "", checksDetail, nil
	}
	return false, detailParts("findings open: "+findingsText(open), checksDetail), nil
}
