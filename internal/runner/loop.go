package runner

import (
	"context"
	"errors"
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

// maxFixRounds is FR-28's fix-round ceiling for the pre-open review: a fix
// round and, when its re-check still reports a finding, one more. A finding
// still open after the last stays open and holds the PR as a draft.
const maxFixRounds = 2

const (
	// nfr1RunCap is NFR-1's whole-run duration ceiling — the build job's own
	// timeout-minutes in .github/workflows/model.yml, which must agree with it.
	nfr1RunCap = 90 * time.Minute
	// nfr1BuildBudget is how much of nfr1RunCap the build stage may spend, from
	// the run's start: the plan phase, the build and check rounds, and the
	// guard scan the review pass follows.
	nfr1BuildBudget = 60 * time.Minute
	// nfr1ReviewBudget is how much of nfr1RunCap the review stage may spend,
	// from the review's own start: the review pass, its fix rounds and the
	// re-checks that follow them.
	nfr1ReviewBudget = 25 * time.Minute
	// nfr1CommitReserve is what nfr1RunCap leaves once both stages have spent
	// their own — nfr1RunCap - nfr1BuildBudget - nfr1ReviewBudget — for the
	// commit, bundle and report steps that still run after the review.
	nfr1CommitReserve = 5 * time.Minute
)

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
// whether the stage had any budget left for a round at all. The deadline is
// clamped at zero so a spent stage is never a negative duration, and the caller
// must read the flag: runAgent turns a non-positive timeout into the full
// defaultAgentTimeout, so a spent stage that passed it on would buy 50 minutes.
func roundTimeout(configured time.Duration, started, now time.Time, budget time.Duration) (time.Duration, bool) {
	remaining := budget - now.Sub(started)
	if remaining <= 0 {
		return 0, false
	}
	upper := effectiveAgentTimeout(configured)
	if remaining < upper {
		return remaining, true
	}
	return upper, true
}

// stageBudgetSpent names the stage whose own budget ran out, so a review cut
// short at 25 minutes reads differently from a build cut at 60.
func stageBudgetSpent(stage string, budget time.Duration) string {
	return fmt.Sprintf("the %s stage's %d-minute budget was spent", stage, int(budget.Minutes()))
}

// checkFeedbackPrompt is what a rebuild round receives: the failing gate's
// name and output, asking for a fix and nothing else.
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

// lastFixRoundCost is the spend of the fix round that has just returned, as the
// PR notes price it: runAgent records the round's step before it returns, so the
// summary's newest step is that round's, and it carries the round's own number
// even when a model substitution advanced it. The caller reads it only after a
// round that returned without an error, so there is always a step to read.
func lastFixRoundCost(sum *Summary) *FixRoundCost {
	st := sum.Steps[len(sum.Steps)-1]
	return &FixRoundCost{Round: st.Round, Tokens: st.Tokens, DurationMS: st.DurationMS}
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
	timeout, inBudget := roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now(), nfr1BuildBudget)
	if !inBudget {
		return false, "", "", noFixRound, stopWith(OutcomeStopped, StopRunBudget,
			errors.New(stageBudgetSpent("build", nfr1BuildBudget)))
	}
	var text string
	text, session, round, err = runAgentInOrder(ctx, d, c, agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Rules: rules, Timeout: timeout}, buildOrder(c), d.Agent, wt.Dir, withCommitInstruction(prompt), sum, sum.StartedAt, nfr1BuildBudget)
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
		timeout, inBudget := roundTimeout(c.AgentTimeout, sum.StartedAt, d.Now(), nfr1BuildBudget)
		if rebuildRound >= checkLoopMaxRounds || !inBudget {
			break
		}
		round++
		call := agentCall{Phase: PhaseBuild, Round: round, Model: c.Model, Session: session, Rules: rules, Detail: gate,
			Timeout: timeout}
		text, session, round, err = runAgentInOrder(ctx, d, c, call, buildOrder(c), d.Agent, wt.Dir, withCommitInstruction(checkFeedbackPrompt(gate, output)), sum, sum.StartedAt, nfr1BuildBudget)
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

// reviewPass runs one review pass over the worktree's pending diff on models
// in order and returns its findings, empty when the diff satisfies the ticket.
// models is non-empty: the caller has already decided the run has a model to
// review it. It starts a fresh session, so rules is the whole of what that
// session carries — the build projection's rules, which is what the pass checks
// the diff against. timeout is what the review's own budget leaves the pass,
// and started and budget are the review stage's, so a pass that steps to its
// backup model is bounded by the review's budget rather than the build's.
func reviewPass(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, models []string, round int, rules string, timeout time.Duration, started time.Time, budget time.Duration) (string, error) {
	diff, err := wt.DiffPending(ctx)
	if err != nil {
		return "", stopWith(OutcomeInfraFailure, StopChecksRun, err)
	}
	call := agentCall{Phase: PhaseReview, Round: round, Model: models[0], Rules: rules, Timeout: timeout}
	text, _, _, err := runAgentInOrder(ctx, d, c, call, models, d.ReviewAgent, wt.Dir, PreOpenReviewPrompt(diff, c.Ticket), sum, started, budget)
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
	// stable is set when the key survives a rewording — a guard's file and
	// header — and unset for a model's finding, whose text a re-review may word
	// differently.
	stable bool
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
		return stillOpenText
	case rechecked:
		return fixedIn(fixRound)
	default:
		return fmt.Sprintf("addressed in round %d, not re-checked", fixRound)
	}
}

// stillOpenText is a row's resolution when nothing closed its finding: no fix
// round ran, or the last re-check still reports it.
const stillOpenText = "still open"

// fixedIn is how a row reads when fixRound resolved its finding.
func fixedIn(fixRound int) string {
	return fmt.Sprintf("fixed in round %d", fixRound)
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
		rows[i] = ReviewFinding{Finding: f.text}
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
			case !f.stable && reworded && res == fixedIn(r.round):
				res = fmt.Sprintf("addressed in round %d; the re-review still reported findings", r.round)
			case res == stillOpenText && r.rechecked && last && len(rounds) > 1:
				res = fmt.Sprintf("still open after round %d", r.round)
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
			rows = append(rows, ReviewFinding{Finding: f.text, Resolution: stillOpenText})
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
// opens as a draft instead. Findings from either get a fix round, fed back to
// the build agent's own session the same way a check failure is, and the
// summary carries each one with how the run left it (AC5). The checks then run
// once, to catch a gate the fix round newly broke, and — when the review
// stage's own budget still allows it — the guards are probed again and the
// review re-run on the fixed diff.
//
// A re-check that still reports a finding, the round's own or one it newly
// made, starts one more fix round on the same session, up to maxFixRounds in
// all: a finding the last round still leaves open stays open and holds the PR
// as a draft. So the detail lists only what the last re-check reports. A clean
// re-check with the checks passing leaves the run ready (FR-5), and a run whose
// review found nothing is untouched by any of this (AC6).
//
// The review's budget runs from when the review begins, after the guard scan,
// so a build stage that spent its own share of the cap leaves the review stage
// the whole of its own. It gives up — running no fix round — once that budget
// would not leave enough time for one, and skips a re-check alone once it would
// not leave enough time for that, labelling the findings as addressed by a fix
// round rather than re-checked. A valid commit-message block from a fix round
// replaces msg. Each review pass starts a fresh session, so it carries no
// history of its own; rules is the build projection's rules, which the review
// needs to check the diff against them, and every fix round resumes the build
// agent's own session with the same rules it was built under — dropping them
// there would change the resumed session's system prompt mid-conversation.
func runReview(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, rules, session string, buildRound int, models []string, msg *CommitMessage) (ready bool, detail string, err error) {
	if len(models) == 0 {
		return false, noReviewDetail, nil
	}
	// The guard scan is charged to the build stage: it probes the diff the
	// review reads, so it stops once the build's budget is spent and its time
	// stays outside the review's own budget, which begins below.
	guards, err := scanGuards(ctx, d, wt, sum.StartedAt, nfr1BuildBudget)
	if err != nil {
		return false, "", err
	}
	reviewStarted := d.Now()
	// The review's budget begins here, so its first pass takes the stage's own
	// clamp with nothing to gate — the budget is whole at the instant the stage
	// starts. Only the fix rounds and the re-checks below run late enough in the
	// stage to need one.
	passTimeout := min(effectiveAgentTimeout(c.AgentTimeout), nfr1ReviewBudget)
	found, err := reviewPass(ctx, d, c, wt, sum, models, 1, rules, passTimeout, reviewStarted, nfr1ReviewBudget)
	if err != nil {
		return false, "", err
	}
	pending := append(guards, reviewPending(found)...)
	if len(pending) == 0 {
		return true, "", nil
	}

	rounds := make([]fixRoundResult, 0, maxFixRounds)
	findings, checksDetail := pending, ""
	for n := 1; n <= maxFixRounds; n++ {
		round := buildRound + n
		timeout, inBudget := roundTimeout(c.AgentTimeout, reviewStarted, d.Now(), nfr1ReviewBudget)
		if !inBudget {
			// A stage with no budget left starts no round: the findings stay open
			// and the PR says so, exactly as it did before there was a second one.
			break
		}
		text := findingsText(findings)
		fixes := fixOrder(c, sum)
		call := agentCall{Phase: PhaseBuild, Round: round, Model: fixes[0], Session: session, Rules: rules, Detail: text,
			Timeout: timeout}
		fixText, resumed, _, err := runAgentInOrder(ctx, d, c, call, fixes, d.Agent, wt.Dir, withCommitInstruction(fixPrompt(text)), sum, reviewStarted, nfr1ReviewBudget)
		if err != nil {
			return false, "", err
		}
		session = resumed
		msg.adopt(fixText, c.Ticket.ID)
		if n > 1 {
			// The one extra round this run took is priced in the PR notes, so the
			// owner sees what it cost before deciding to keep it.
			sum.ReviewSecondRound = lastFixRoundCost(sum)
		}

		gate, output, cerr := checksFormatted(ctx, d, sum, wt, round)
		if cerr != nil {
			return false, "", cerr
		}
		checksDetail = ""
		if gate != "" {
			checksDetail = "checks: " + gate + " failing after the fix round\n\n" + truncate(output, checkFeedbackLimit)
		}

		// The post-fix scan is charged to the review stage, and each probe is
		// itself bounded by what the review's budget leaves, so the guards cannot
		// spend the time the re-review needs. Both re-checks then run under the
		// one gate below: a finding is open, or fixed, only once its own re-check
		// has said so.
		surviving, err := scanGuards(ctx, d, wt, reviewStarted, nfr1ReviewBudget)
		if err != nil {
			return false, "", err
		}
		recheckTimeout, inBudget := roundTimeout(c.AgentTimeout, reviewStarted, d.Now(), nfr1ReviewBudget)
		if !inBudget {
			rounds = append(rounds, fixRoundResult{round: round, issued: findings})
			sum.ReviewFindings = reviewRows(rounds)
			return false, detailParts("findings addressed by a fix round, not re-reviewed: "+
				text+" ("+stageBudgetSpent("review", nfr1ReviewBudget)+")", checksDetail), nil
		}
		remaining, err := reviewPass(ctx, d, c, wt, sum, models, round, rules, recheckTimeout, reviewStarted, nfr1ReviewBudget)
		if err != nil {
			return false, "", err
		}
		open := append(surviving, reviewPending(remaining)...)
		rounds = append(rounds, fixRoundResult{round: round, issued: findings, open: open, rechecked: true})
		if len(open) == 0 {
			sum.ReviewFindings = reviewRows(rounds)
			return checksDetail == "", checksDetail, nil
		}
		findings = open
	}
	if len(rounds) == 0 {
		// The budget left no round at all: nothing was done about the findings.
		sum.ReviewFindings = capFindings(resolveFindings(pending, nil, noFixRound, false))
		return false, "findings open: " + findingsText(pending) + " (fix round skipped: " + stageBudgetSpent("review", nfr1ReviewBudget) + ")", nil
	}
	sum.ReviewFindings = reviewRows(rounds)
	return false, detailParts("findings open: "+findingsText(findings), checksDetail), nil
}
