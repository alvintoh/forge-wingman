package runner

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultReviewFixRounds is how many fix rounds the runner makes from a pull
// request review's findings before the pull request waits for the owner. Three is
// the owner's starting number rather than a measured one: the rounds' own records
// show when rounds two and three stop resolving findings, and the cap comes down
// from that evidence.
const DefaultReviewFixRounds = 3

// FixRoundTrailer prefixes the trailer a fix round's commit carries, holding the
// round's agent-call number (Step.Round, which skips one when a backup model
// answers): the rewrite measurement reads it to count the lines the runner fixed
// after review, which stay the agent's lines in the rate.
const FixRoundTrailer = "Fix-Round: "

// fixRoundsBudget is how long the whole fix-round loop may take: NFR-1's
// whole-run duration ceiling, measured from the loop's own start, so a round, its
// commit, its push and the re-review after it come out of the same ceiling a
// build does. The job that runs the loop must set its timeout-minutes to agree
// with it, as model.yml's does with nfr1RunCap.
const fixRoundsBudget = nfr1RunCap

// ErrFixRoundsBudget reports that no fix round fits what the loop's budget has
// left: no round ran, and the findings are the owner's.
var ErrFixRoundsBudget = errors.New("no fix round fits the loop's own budget")

// ErrBranchMismatch reports a worktree that is not on the branch a round was
// given: a round commits to the pull request's branch, never to another.
var ErrBranchMismatch = errors.New("the worktree is not on the pull request's branch")

// FixStop is why a fix-round loop ended with findings left for the owner.
type FixStop string

const (
	// FixStopNoChange is a round that changed no file, so the findings it was
	// given are the findings still open.
	FixStopNoChange FixStop = "no-change"
	// FixStopUnchanged is a re-review that reported the round's findings again:
	// the round made no progress.
	FixStopUnchanged FixStop = "unchanged"
	// FixStopNew is a re-review that reported a finding the round was not given:
	// the round added one.
	FixStopNew FixStop = "new-findings"
	// FixStopDeclined is a round that declined the only findings left, so they
	// wait for the owner rather than for another round.
	FixStopDeclined FixStop = "declined"
	// FixStopBudget is a loop that had no fixRoundsBudget left to start a round.
	FixStopBudget FixStop = "budget"
	// FixStopCap is a loop that ran its rounds with findings still open.
	FixStopCap FixStop = "round-cap"
)

// noDeclineReason is recorded for a decline whose line named no reason, rather
// than dropping the decline from the record.
const noDeclineReason = "no reason given"

// DeclinedFinding is one finding a fix round did not act on, and why: the owner
// reads it where the round is recorded, so a decline is never a silent drop.
type DeclinedFinding struct {
	Finding string `firestore:"finding" json:"finding"`
	Reason  string `firestore:"reason" json:"reason"`
}

// ReviewFixRound is one fix round the runner made on a pull request's branch,
// recorded under FR-6: the step that ran it, the findings that drove it, the ones
// it declined, what the re-review after it showed, the session it ran in and the
// commit it made.
type ReviewFixRound struct {
	// Step is the round's own agent call, as every other phase's is recorded.
	Step Step `firestore:"step" json:"step"`
	// Findings are the findings the round was given, one per line.
	Findings []string `firestore:"findings" json:"findings"`
	// Declined are the findings the round left for the owner, with its reason.
	Declined []DeclinedFinding `firestore:"declined" json:"declined,omitempty"`
	// Resolved is how many of Findings the re-review no longer reported, and New
	// how many it reported that the round was not given. Both are meaningful only
	// when Reviewed: they stay zero for a round that changed no file, or whose push
	// or re-review did not complete.
	Resolved int `firestore:"resolved" json:"resolved"`
	New      int `firestore:"new" json:"new"`
	// Reviewed is whether the re-review of the round's commit completed, so
	// Resolved and New are its counts.
	Reviewed bool `firestore:"reviewed" json:"reviewed"`
	// Session is the session the round ran in, which the round after it resumes.
	Session string `firestore:"session" json:"session,omitempty"`
	// Commit is the round's own commit on the branch, empty when it made none.
	Commit string `firestore:"commit" json:"commit,omitempty"`
}

// ReviewFixResult is what a fix-round loop left: each round it ran, whether the
// pull request's review came back clean, and why not when it did not.
type ReviewFixResult struct {
	// Rounds is each round the loop ran, in the order it ran them.
	Rounds []ReviewFixRound
	// Ready is true only when the last re-review reported no finding, so the
	// pull request is clean and the owner's to merge.
	Ready bool
	// Stop is why the loop ended with findings open, one of the FixStop reasons;
	// empty when Ready.
	Stop FixStop
	// Detail names the findings left open and why the loop stopped, empty when
	// Ready.
	Detail string
	// Session is the session the last round ran in.
	Session string
	// Steps is every agent call the rounds made, one per round, for the caller to
	// append to its summary so the rounds' tokens and time settle with the run.
	Steps []Step
}

// FixRounds is one pull request's fix-round loop: the review's findings, the
// session the rounds resume, and the two steps of the pull request a round hands
// off — publishing its commit, and re-reviewing the head it published.
type FixRounds struct {
	// Branch is the pull request's own branch. The worktree must be on it: a
	// round commits to the pull request, never to another branch and never as a
	// pull request of its own.
	Branch string
	// Head is the commit the pull request's branch is at, which the worktree must
	// be checked out on: a worktree reset to another commit is not the pull
	// request's branch, and committing there would lose the pull request's own
	// commits when they are published.
	Head string
	// Session is the session the first round resumes, empty when the caller has
	// none to resume, so the loop starts one and continues it on the rounds after.
	Session string
	// Rules is the projection's rules head, carried by every round so the
	// resumed session's system prompt stays the same across them.
	Rules string
	// Findings is the review's findings on the pull request, one finding per
	// line; empty means there is nothing to fix.
	Findings string
	// Rounds lowers the cap below DefaultReviewFixRounds; zero, a negative number
	// or one above it uses DefaultReviewFixRounds.
	Rounds int
	// Push publishes a round's commit to branch. Required.
	Push func(ctx context.Context, branch, commit string) error
	// Review reports the findings a review of branch's new head made, in the
	// same one-per-line form as Findings, empty when it found none. Required.
	Review func(ctx context.Context, branch, commit string) (string, error)
}

// FixRound is one fix round on its own: the pull request it commits to, the
// session it resumes, the findings it is given and where in the loop it sits, so
// a job that runs a single round per process carries all of it forward.
type FixRound struct {
	// Branch is the pull request's own branch, which the worktree must be on.
	Branch string
	// Head is the commit the branch is at: the round's own commit is made on it,
	// so a worktree at any other commit is refused before the agent runs.
	Head string
	// Session is the session to resume, empty when the caller has none to resume
	// and the round starts one.
	Session string
	// Rules is the projection's rules head, carried so the resumed session's
	// system prompt stays the same across rounds.
	Rules string
	// Findings is what the round is given, one finding per line, already minus
	// the ones an earlier round declined.
	Findings []string
	// Calls is how many agent calls the loop has already numbered, which this
	// round's call, its transcript and its commit trailer continue from. A backup
	// model answering advances that number past the round's own.
	Calls int
	// StartedAt is when the loop started, which this round's share of
	// fixRoundsBudget is measured from; the zero time starts the clock now.
	StartedAt time.Time
}

// FixRoundDecision is what one finished fix round leaves the loop: another
// round, a clean pull request, or findings for the owner with the reason.
type FixRoundDecision struct {
	// Continue is set when the loop makes another round.
	Continue bool
	// Ready is set when the re-review reported no finding, so the pull request
	// is clean and the owner's to merge.
	Ready bool
	// Stop is why the loop ended with findings open, one of the FixStop reasons;
	// empty when Continue or Ready.
	Stop FixStop
	// Detail names the decline or the round's own failure, empty when Ready.
	Detail string
}

// fixDeclinesBlock is the block fixDeclinesInstruction asks a round for.
var fixDeclinesBlock = regexp.MustCompile("(?s)```declined-findings\\s*\\n(.*?)```")

// fixDeclinesInstruction ends every fix-round prompt: the format a finding the
// round will not act on is recorded in, since nothing else declares one.
const fixDeclinesInstruction = "If a finding should not be acted on, leave that part of the code as it is and say why. " +
	"End your response with a fenced code block named declined-findings holding one line per finding you did not act on, " +
	"each as `<the finding> — <why>`; leave the block empty when you acted on every finding:\n\n" +
	"```declined-findings\n```"

// fixRoundPrompt is what a fix round receives: the review's findings on the pull
// request, asking for a fix and nothing else, then the block a decline is
// recorded in.
func fixRoundPrompt(findings string) string {
	return "A review of this pull request found the following. Fix it and make no unrelated change: this round " +
		"commits to the pull request's existing branch, and opens no pull request of its own.\n\n" + findings + "\n\n" +
		fixDeclinesInstruction
}

// parseFixDeclines reads an agent's final message for the last declined-findings
// block: one finding and its reason per line. A line naming no reason is
// recorded as such rather than dropped, so the decline is still the owner's to
// judge.
func parseFixDeclines(text string) []DeclinedFinding {
	matches := fixDeclinesBlock.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []DeclinedFinding
	for _, line := range strings.Split(matches[len(matches)-1][1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		finding, reason, ok := cutDecline(line)
		if !ok {
			reason = noDeclineReason
		}
		out = append(out, DeclinedFinding{Finding: truncate(finding, maxFindingBytes), Reason: truncate(reason, maxFindingBytes)})
	}
	return out
}

// cutDecline splits a declined-findings line into the finding it names and the
// reason, on the first em dash — or a plain hyphen, which a model sometimes
// writes instead.
func cutDecline(line string) (finding, reason string, ok bool) {
	for _, sep := range []string{" — ", " - "} {
		if finding, reason, ok = strings.Cut(line, sep); ok {
			return strings.TrimSpace(finding), strings.TrimSpace(reason), true
		}
	}
	return line, "", false
}

// declinedText is declines as the loop's detail names them, one per line with
// its reason, so the owner reads why the runner left it.
func declinedText(declined []DeclinedFinding) string {
	lines := make([]string, len(declined))
	for i, d := range declined {
		lines[i] = d.Finding + " — " + d.Reason
	}
	return strings.Join(lines, "; ")
}

// fixFindingLines is findings as the record carries them: one line each, cut to
// maxFindingBytes, which is the bound a summary's own findings take.
func fixFindingLines(findings []pendingFinding) []string {
	out := make([]string, len(findings))
	for i, f := range findings {
		out[i] = truncate(f.text, maxFindingBytes)
	}
	return out
}

// findingTexts is findings as their lines alone, untruncated: what the next round
// is given, and what a re-review's own lines are matched against.
func findingTexts(findings []pendingFinding) []string {
	out := make([]string, len(findings))
	for i, f := range findings {
		out[i] = f.text
	}
	return out
}

// fixCapDetail is why a loop ended at its cap, naming how many rounds it allowed.
func fixCapDetail(capped int) string {
	return fmt.Sprintf("the cap of %d fix round(s) was reached", capped)
}

// findingDelta is how many of issued a re-review no longer reports, and how many
// it reports that issued did not hold and no round declined: the per-round
// numbers the record keeps so the cap can be lowered from evidence.
func findingDelta(issued, after []pendingFinding, declined []DeclinedFinding) (resolved, added int) {
	before, now := findingKeys(issued), findingKeys(after)
	for _, f := range issued {
		if !now[f.key] {
			resolved++
		}
	}
	for _, f := range after {
		if !before[f.key] && !isDeclined(f, declined) {
			added++
		}
	}
	return resolved, added
}

// withoutDeclined is findings minus the ones the round declined: a declined
// finding is the owner's, so a later round is not given it again.
func withoutDeclined(findings []pendingFinding, declined []DeclinedFinding) []pendingFinding {
	var kept []pendingFinding
	for _, f := range findings {
		if !isDeclined(f, declined) {
			kept = append(kept, f)
		}
	}
	return kept
}

// stillReported is the declines whose finding the latest re-review reports, so a
// finding that has since gone is not named to the owner as one left open.
func stillReported(declined []DeclinedFinding, after []pendingFinding) []DeclinedFinding {
	return slices.DeleteFunc(slices.Clone(declined), func(d DeclinedFinding) bool {
		return !slices.ContainsFunc(after, func(f pendingFinding) bool { return namesFinding(d.Finding, f.text) })
	})
}

// isDeclined reports whether a declined-findings entry names f.
func isDeclined(f pendingFinding, declined []DeclinedFinding) bool {
	return slices.ContainsFunc(declined, func(d DeclinedFinding) bool { return namesFinding(d.Finding, f.text) })
}

// minQuotedFindingChars is how long the shorter of an entry and a finding must be
// for one containing the other to name it: a shorter entry names a finding only
// by equalling it, so a word cannot suppress an unrelated finding.
const minQuotedFindingChars = 24

// namesFinding reports whether a declined-findings entry names a finding: the
// entry equals it, or one quotes the other at length. A model rewords one side,
// and a mismatch only ever costs a round — a finding no round resolves stays open
// and stops the loop anyway, so nothing is lost; a false match would skip one.
func namesFinding(entry, text string) bool {
	e, t := strings.ToLower(strings.TrimSpace(entry)), strings.ToLower(strings.TrimSpace(text))
	if e == "" || t == "" {
		return false
	}
	if e == t {
		return true
	}
	short, long := e, t
	if len(short) > len(long) {
		short, long = long, short
	}
	return utf8.RuneCountInString(short) >= minQuotedFindingChars && strings.Contains(long, short)
}

// fixRoundCommit commits a round's edits to the pull request's branch as the
// runner, returning the commit it made and whether it made one. The commit
// carries FixRoundTrailer and the round's agent-call number, which is how the
// rewrite measurement tells what the runner fixed after review. It verifies the
// worktree first, so a changed git configuration or a moved HEAD stops the loop
// instead of committing somewhere else, and it runs the secret and workflow
// checks the build's own commit runs, since the commit goes to a live branch.
// expected is the commit the branch must be at when the round's edits are
// committed: a branch the agent moved elsewhere would carry them onto the wrong
// base.
func fixRoundCommit(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, round int, expected, text string) (string, bool, error) {
	if err := wt.Verify(ctx); err != nil {
		switch {
		case errors.Is(err, ErrGitTampered):
			return "", false, stopWith(OutcomeStopped, StopGitTampered, err)
		case errors.Is(err, ErrHeadMoved):
			return "", false, stopWith(OutcomeStopped, StopHeadMoved, err)
		}
		return "", false, stopWith(OutcomeInfraFailure, StopCommit, err)
	}
	before, err := wt.HeadCommit(ctx)
	if err != nil {
		return "", false, stopWith(OutcomeInfraFailure, StopCommit, err)
	}
	if before != expected {
		return "", false, stopWith(OutcomeStopped, StopHeadMoved, fmt.Errorf("%w: the branch is at %s, not %s", ErrHeadMoved, before, expected))
	}
	var msg CommitMessage
	msg.adopt(text, c.Ticket.ID)
	if msg.Subject == "" {
		pending, err := wt.PendingFiles(ctx)
		if err != nil {
			return "", false, stopWith(OutcomeInfraFailure, StopCommit, err)
		}
		msg = FallbackCommitMessage(c.Ticket, pending)
		d.Logger.Info("commitMessageFallback", "phase", string(PhaseFix))
	}
	if _, err := wt.Commit(ctx, msg.Text()+"\n\n"+FixRoundTrailer+strconv.Itoa(round)); err != nil {
		return "", false, stopWith(OutcomeInfraFailure, StopCommit, err)
	}
	after, err := wt.HeadCommit(ctx)
	if err != nil {
		return "", false, stopWith(OutcomeInfraFailure, StopCommit, err)
	}
	if after == before {
		return "", false, nil
	}
	for _, secret := range c.Secrets {
		if err := wt.CheckSecret(ctx, secret); err != nil {
			if errors.Is(err, ErrSecretInBranch) {
				return "", false, stopWith(OutcomeStopped, StopSecretInBranch, err)
			}
			return "", false, stopWith(OutcomeInfraFailure, StopCommit, err)
		}
	}
	touched, err := wt.TouchedPaths(ctx)
	if err != nil {
		return "", false, stopWith(OutcomeInfraFailure, StopCommit, err)
	}
	if wf := workflowFiles(touched); len(wf) > 0 {
		return "", false, stopWith(OutcomeStopped, StopWorkflowChange, workflowChange(wf))
	}
	return after, true, nil
}

// RunFixRound makes one fix round on wt: the agent's edits, then the local
// commit. It publishes nothing and reads no review — the caller publishes the
// commit and decides the next step from the findings it reads itself — and
// returns the round's record and the commit it made, empty when the round
// changed no file. sum collects the round's agent calls, so a loop's rounds
// settle together.
//
// An agent, commit or guard failure is returned as a *StopError, as in a build,
// with no round record: the caller is left nothing it may publish. A round the
// loop's budget no longer leaves room for is ErrFixRoundsBudget.
func RunFixRound(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, sum *Summary, r FixRound) (ReviewFixRound, string, error) {
	if err := ctx.Err(); err != nil {
		return ReviewFixRound{}, "", err
	}
	if r.Branch == "" || wt.Branch != r.Branch {
		return ReviewFixRound{}, "", fmt.Errorf("%w: it is on %q, not %q", ErrBranchMismatch, wt.Branch, r.Branch)
	}
	issued := lineFindings(r.Findings)
	if len(issued) == 0 {
		return ReviewFixRound{}, "", errors.New("a fix round needs the findings it is given")
	}
	head, err := wt.HeadCommit(ctx)
	if err != nil {
		return ReviewFixRound{}, "", fmt.Errorf("reading the worktree's head: %w", err)
	}
	if r.Head == "" || head != r.Head {
		return ReviewFixRound{}, "", fmt.Errorf("%w: the worktree is at %s, not the head %q the round is given", ErrHeadMoved, head, r.Head)
	}
	started := r.StartedAt
	if started.IsZero() {
		started = d.Now()
	}
	timeout, inBudget := roundTimeout(c.AgentTimeout, started, d.Now(), fixRoundsBudget, reviewStartRoom)
	if !inBudget {
		return ReviewFixRound{}, "", fmt.Errorf("%w: %s", ErrFixRoundsBudget, stageBudgetSpent("fix-round", fixRoundsBudget))
	}
	text := findingsText(issued)
	order := buildOrder(c)
	call := agentCall{Phase: PhaseFix, Round: r.Calls + 1, Model: order[0], Session: r.Session, Rules: r.Rules,
		Detail: truncate(text, stopDetailLimit), Timeout: timeout}
	fixText, resumed, num, err := runAgentInOrder(ctx, d, c, call, order, d.Agent, wt.Dir,
		withCommitInstruction(fixRoundPrompt(text)), sum, started, fixRoundsBudget, reviewStartRoom)
	if err != nil {
		return ReviewFixRound{}, "", err
	}
	round := ReviewFixRound{Step: sum.Steps[len(sum.Steps)-1], Session: resumed, Findings: fixFindingLines(issued),
		Declined: parseFixDeclines(fixText)}
	// fixRoundCommit reports an empty commit for a round that changed no file,
	// so the sha alone says whether the round made one.
	sha, _, err := fixRoundCommit(ctx, d, c, wt, num, r.Head, fixText)
	if err != nil {
		return ReviewFixRound{}, "", err
	}
	round.Commit = sha
	return round, sha, nil
}

// DecideFixRound decides what one finished fix round leaves, from the findings it
// was given, the findings the re-review of its commit reported, the declines so
// far, its own round number and the loop's cap. The loop continues only while a
// round strictly reduces the findings with no new one; a round that leaves them
// unchanged, adds one, or declines the only ones left ends it, as does the cap.
// A finding the re-review words differently counts as a new one, so the loop
// stops rather than spend a round on it.
//
// round is the loop's round number, not the agent-call number: a backup model
// answering advances the call number past its round's, and counting those would
// end the loop early. capped is the loop's own cap, so a caller that lowered it
// is judged by the cap it set.
//
// It reads nothing and writes nothing, so a caller running one round per process
// decides the next step from findings it read itself, with no loop in between.
func DecideFixRound(issued, after []string, declined []DeclinedFinding, round, capped int) FixRoundDecision {
	open := lineFindings(after)
	switch {
	case len(open) == 0:
		return FixRoundDecision{Ready: true}
	case len(withoutDeclined(open, declined)) == 0:
		return FixRoundDecision{Stop: FixStopDeclined, Detail: "declined: " + declinedText(stillReported(declined, open))}
	}
	resolved, added := findingDelta(lineFindings(issued), open, declined)
	switch {
	case added > 0:
		return FixRoundDecision{Stop: FixStopNew, Detail: fmt.Sprintf("round %d added findings", round)}
	case resolved == 0:
		return FixRoundDecision{Stop: FixStopUnchanged, Detail: fmt.Sprintf("round %d left the findings unchanged", round)}
	case round >= capped:
		return FixRoundDecision{Stop: FixStopCap, Detail: fixCapDetail(capped)}
	}
	return FixRoundDecision{Continue: true}
}

// RunFixRounds makes up to the loop's cap fix rounds from a pull request review's
// findings: each round is RunFixRound's, commits once to the pull request's own
// branch, is published by the push seam, and is followed by the review seam's
// findings at the head it published, which DecideFixRound judges. The loop
// continues only while a round strictly reduces the findings with no new one; a
// round that changes no file, leaves them unchanged, adds one, or declines the
// only ones left ends it and hands the pull request to the owner. No round starts
// with under reviewStartRoom of fixRoundsBudget left.
//
// A seam's own failure is returned as an error: it belongs to the step that
// publishes or reviews, whose own run records the failure, and the round's
// commit is on the branch either way. An agent, commit or guard failure is a
// *StopError, as in a build. Every error after the loop starts comes with the
// result so far: the rounds already made, their steps and the session.
func RunFixRounds(ctx context.Context, d BuildDeps, c BuildConfig, wt Worktree, r FixRounds) (ReviewFixResult, error) {
	if r.Branch == "" || wt.Branch != r.Branch {
		return ReviewFixResult{}, fmt.Errorf("%w: it is on %q, not %q", ErrBranchMismatch, wt.Branch, r.Branch)
	}
	if r.Push == nil || r.Review == nil {
		return ReviewFixResult{}, errors.New("a fix-round loop needs both a push and a review step")
	}
	capped := r.Rounds
	if capped <= 0 || capped > DefaultReviewFixRounds {
		capped = DefaultReviewFixRounds
	}
	open := reviewPending(r.Findings)
	res := ReviewFixResult{Ready: len(open) == 0, Session: r.Session}
	if len(open) == 0 {
		return res, nil
	}
	// Refused before the loop starts, so a worktree at another commit leaves no
	// envelope behind for the caller to mistake for work already done.
	head, err := wt.HeadCommit(ctx)
	if err != nil {
		return ReviewFixResult{}, fmt.Errorf("reading the worktree's head: %w", err)
	}
	if r.Head == "" || head != r.Head {
		return ReviewFixResult{}, fmt.Errorf("the worktree is at %s, not the pull request's head %q", head, r.Head)
	}
	sum := Summary{DurationsMS: map[string]int64{}, StartedAt: d.Now()}
	session, calls := r.Session, 0
	var stop FixStop
	var detail string
	var declined []DeclinedFinding
	expected := r.Head
	// fail returns the rounds already made with err: their commits are on the branch.
	fail := func(err error) (ReviewFixResult, error) {
		res.Session, res.Steps = session, sum.Steps
		return res, err
	}
	for n := 1; n <= capped; n++ {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		issued := withoutDeclined(open, declined)
		round, sha, err := RunFixRound(ctx, d, c, wt, &sum, FixRound{
			Branch: r.Branch, Head: expected, Session: session, Rules: r.Rules,
			Findings: findingTexts(issued), Calls: calls, StartedAt: sum.StartedAt,
		})
		if errors.Is(err, ErrFixRoundsBudget) {
			stop, detail = FixStopBudget, stageBudgetSpent("fix-round", fixRoundsBudget)
			break
		}
		if err != nil {
			return fail(err)
		}
		calls, session = round.Step.Round, round.Session
		res.Rounds = append(res.Rounds, round)
		cur := &res.Rounds[len(res.Rounds)-1]
		if sha == "" {
			stop, detail = FixStopNoChange, fmt.Sprintf("round %d changed no file", n)
			break
		}
		expected = sha
		if err := r.Push(ctx, r.Branch, sha); err != nil {
			return fail(fmt.Errorf("pushing round %d's commit to %s: %w", n, r.Branch, err))
		}
		found, err := r.Review(ctx, r.Branch, sha)
		if err != nil {
			return fail(fmt.Errorf("re-reviewing %s after round %d: %w", r.Branch, n, err))
		}
		after := reviewPending(found)
		declined = append(declined, cur.Declined...)
		cur.Resolved, cur.New = findingDelta(issued, after, declined)
		cur.Reviewed = true
		open = after
		dec := DecideFixRound(findingTexts(issued), findingTexts(after), declined, n, capped)
		switch {
		case dec.Ready:
			res.Ready = true
		case dec.Continue:
			continue
		default:
			stop, detail = dec.Stop, dec.Detail
		}
		break
	}
	if !res.Ready {
		res.Stop, res.Detail = stop, detailParts("findings open: "+findingsText(open), detail)
	}
	res.Session, res.Steps = session, sum.Steps
	return res, nil
}
