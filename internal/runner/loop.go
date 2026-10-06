package runner

import (
	"context"
	"fmt"
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
// session to continue and the round the build agent last ran under. Each
// round's valid commit-message block replaces msg. Every round carries the
// build projection's rules head, so the resumed session's system prompt stays
// the same across the loop.
func runCheckLoop(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, rules, prompt string, msg *CommitMessage) (ok bool, detail, session string, round int, err error) {
	round = 1
	call := agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Rules: rules, Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now())}
	var text string
	text, session, round, err = runAgentWithFallback(ctx, d, c, call, d.Agent, wt.Dir, withCommitInstruction(prompt), sum)
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
		call := agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Session: session, Rules: rules, Detail: gate,
			Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, now)}
		text, session, round, err = runAgentWithFallback(ctx, d, c, call, d.Agent, wt.Dir, withCommitInstruction(checkFeedbackPrompt(gate, output)), sum)
		if err != nil {
			return false, "", session, round, err
		}
		msg.adopt(text, c.Ticket.ID)
	}
	return false, checkGiveUpDetail(lastGate, lastOutput, round), session, round, nil
}

// reviewPass runs one review pass over the worktree's pending diff and
// returns its findings, empty when the diff satisfies the ticket. It starts a
// fresh session with no projection, so it carries no rules.
func reviewPass(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, round int) (string, error) {
	diff, err := wt.DiffPending(ctx)
	if err != nil {
		return "", stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	call := agentCall{Phase: PhaseReview, Round: round, Model: c.ReviewModels[0],
		Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now())}
	text, _, _, err := runAgentInOrder(ctx, d, c, call, c.ReviewModels, d.ReviewAgent, wt.Dir, ReviewPrompt(diff, c.Ticket), sum)
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

// runReview runs the pre-PR loop's review pass (FR-28) once the checks have
// passed: a model other than the builder's checks the diff against the
// ticket's acceptance criteria. Findings get exactly one fix round, fed back
// to the build agent's own session the same way a check failure is. The checks
// then run once, to catch a gate the fix round newly broke, and — when NFR-1's
// run-duration budget still allows it — the review runs once more on the fixed
// diff, so the detail lists only the findings still open. A clean re-review
// with the checks passing leaves the run ready (FR-59).
//
// It gives up — running neither the review nor the fix round — once the
// budget would not leave enough time for it, and skips the re-review alone
// once it would not leave enough time for that, labelling the findings as
// addressed by a fix round rather than re-reviewed. A valid commit-message
// block from the fix round replaces msg. Each review pass starts a fresh
// session with no projection, so it carries no rules; the fix round resumes
// the build agent's own session, so it carries the build projection's rules —
// dropping them there would change the resumed session's system prompt
// mid-conversation.
func runReview(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, rules, session string, buildRound int, msg *CommitMessage) (ready bool, detail string, err error) {
	now := d.Now()
	if !withinBudget(sum.StartedAt, now) {
		return false, "review: skipped — NFR-1's run-duration budget was spent by the check loop", nil
	}
	findings, err := reviewPass(ctx, d, c, wt, sum, 1)
	if err != nil {
		return false, "", err
	}
	if findings == "" {
		return true, "", nil
	}

	now = d.Now()
	if !withinBudget(sum.StartedAt, now) {
		return false, "review findings open: " + findings + " (fix round skipped: NFR-1's run-duration budget was spent)", nil
	}
	fixCall := agentCall{Phase: PhaseBuild, Round: buildRound + 1, Model: c.Model, Session: session, Rules: rules, Detail: findings,
		Timeout: roundTimeout(c.AgentTimeout, sum.StartedAt, now)}
	fixText, _, _, err := runAgentWithFallback(ctx, d, c, fixCall, d.Agent, wt.Dir, withCommitInstruction(fixPrompt(findings)), sum)
	if err != nil {
		return false, "", err
	}
	msg.adopt(fixText, c.Ticket.ID)

	gate, output, cerr := d.Checks(ctx, wt.Dir)
	if cerr != nil {
		return false, "", stopWith(OutcomeInfraFailure, StopChecksRun, cerr)
	}
	checksDetail := ""
	if gate != "" {
		checksDetail = "checks: " + gate + " failing after the fix round\n\n" + truncate(output, checkFeedbackLimit)
	}

	now = d.Now()
	if !withinBudget(sum.StartedAt, now) {
		return false, detailParts("review findings addressed by a fix round, not re-reviewed: "+findings, checksDetail), nil
	}
	remaining, err := reviewPass(ctx, d, c, wt, sum, 2)
	if err != nil {
		return false, "", err
	}
	if remaining == "" {
		return checksDetail == "", checksDetail, nil
	}
	return false, detailParts("review findings open: "+remaining, checksDetail), nil
}
