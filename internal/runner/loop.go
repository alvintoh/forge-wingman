package runner

import (
	"context"
	"fmt"
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

// fixPrompt is what the review's one fix round receives: its findings,
// asking for a fix and nothing else.
func fixPrompt(findings string) string {
	return "A review against the ticket's acceptance criteria found the following. Address it and make no " +
		"unrelated change.\n\n" + findings
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

// runCheckLoop runs the build agent, then the repository's own checks,
// feeding a failing gate's output back to the same session and rebuilding,
// up to checkLoopMaxRounds attempts in all (FR-28). It stops early, checks
// still failing, once NFR-1's run-duration budget would not leave enough
// time for another round — a round already started always finishes. A build
// that produced no change skips the checks entirely: nothing to review.
//
// It returns whether the checks passed, why not when they did not, the
// session to continue and the round the build agent last ran under.
func runCheckLoop(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, prompt string) (ok bool, detail, session string, round int, err error) {
	round = 1
	call := agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now())}
	_, session, round, err = runAgentWithFallback(ctx, d, c, call, d.Agent, wt.Dir, prompt, sum)
	if err != nil {
		return false, "", "", round, err
	}

	dirty, err := wt.Dirty(ctx)
	if err != nil {
		return false, "", session, round, stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	if !dirty {
		return false, "", session, round, nil
	}

	var lastGate, lastOutput string
	for rebuildRound := 1; ; rebuildRound++ {
		gate, output, cerr := d.Checks(ctx, wt.Dir)
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
		call := agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Session: session, Detail: gate,
			Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, now)}
		_, session, round, err = runAgentWithFallback(ctx, d, c, call, d.Agent, wt.Dir, checkFeedbackPrompt(gate, output), sum)
		if err != nil {
			return false, "", session, round, err
		}
	}
	return false, checkGiveUpDetail(lastGate, lastOutput, round), session, round, nil
}

// runReview runs the pre-PR loop's review pass (FR-28) once the checks have
// passed: a model other than the builder's checks the diff against the
// ticket's acceptance criteria. Findings get exactly one fix round, fed back
// to the build agent's own session the same way a check failure is. Findings
// always leave the run not ready — nothing re-verifies the fix round beyond
// re-running the checks once, to catch a gate it newly broke.
//
// It gives up — running neither the review nor the fix round — once NFR-1's
// run-duration budget would not leave enough time for it.
func runReview(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, session string, buildRound int) (ready bool, detail string, err error) {
	now := d.Now()
	if !withinBudget(sum.StartedAt, now) {
		return false, "review: skipped — NFR-1's run-duration budget was spent by the check loop", nil
	}
	diff, err := wt.DiffPending(ctx)
	if err != nil {
		return false, "", stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	call := agentCall{Phase: PhaseReview, Round: 1, Model: c.ReviewModel, Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, now)}
	text, _, _, err := runAgentWithFallback(ctx, d, c, call, d.ReviewAgent, wt.Dir, ReviewPrompt(diff, c.Ticket), sum)
	if err != nil {
		return false, "", err
	}
	findings, ferr := ParseReviewFindings(text)
	if ferr != nil {
		return false, "", stopWith(OutcomeStopped, StopReviewInvalid, ferr)
	}
	if findings == "" {
		return true, "", nil
	}
	detail = "review findings open: " + findings

	now = d.Now()
	if !withinBudget(sum.StartedAt, now) {
		return false, detail + " (fix round skipped: NFR-1's run-duration budget was spent)", nil
	}
	fixCall := agentCall{Phase: PhaseBuild, Round: buildRound + 1, Model: c.Model, Session: session, Detail: findings,
		Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, now)}
	if _, _, _, err := runAgentWithFallback(ctx, d, c, fixCall, d.Agent, wt.Dir, fixPrompt(findings), sum); err != nil {
		return false, "", err
	}

	gate, output, cerr := d.Checks(ctx, wt.Dir)
	if cerr != nil {
		return false, "", stopWith(OutcomeInfraFailure, StopChecksRun, cerr)
	}
	if gate != "" {
		detail += "; checks: " + gate + " failing after the fix round\n\n" + truncate(output, checkFeedbackLimit)
	}
	return false, detail, nil
}
