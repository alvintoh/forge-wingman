package dispatcher

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// delegateLane matches the lane a delegatable title opens with: the ticket
// templates' [BE], [FE] and [SLICE], read in any case as the runner reads a
// ticket's own lane tag.
var delegateLane = regexp.MustCompile(`(?i)^\[(BE|FE|SLICE)\]`)

// unstartedStates are the Linear state types of a ticket nobody has begun.
var unstartedStates = []string{"backlog", "unstarted"}

const (
	// holdLabel takes a ticket out of the automatic queue: the dispatcher never
	// delegates one, so the owner can park work without undelegating it.
	holdLabel = "wingman:hold"
	// workflowPath is the repository path a ticket's body must not name, since
	// no run may edit the workflow it is dispatched by.
	workflowPath = ".github/workflows/"
	// delegateRule names what chose a ticket the dispatcher delegated: the
	// earliest milestone that still has open tickets, then Linear priority.
	delegateRule = "earliest-milestone-then-priority"
)

// DefaultDailyRuns is how many runs a poll takes on itself in a day when none
// is configured.
const DefaultDailyRuns = 5

// dailyRuns is the runs a poll delegates to itself in a day: Config.DailyRuns,
// or DefaultDailyRuns when none is configured.
func (c Config) dailyRuns() int { return cmp.Or(c.DailyRuns, DefaultDailyRuns) }

// autoDelegate delegates the tickets the product takes on itself, up to the
// configured daily run cap, while the review WIP limit leaves room.
//
// The cap is Config.DailyRuns, counted from the runs claimed today, so a poll
// whose day's runs are already claimed delegates nothing, and every delegation
// one poll makes counts against it, so no poll spends more than the day has
// left. No autonomy level is read: self-delegation runs at every level.
//
// The tickets are listed before the admission hold and the open-PR count, so a
// poll with nothing to delegate pays neither: those two are the poll's only
// reads of the ledger and of GitHub on this path. A listing that fails is
// reported as the poll's own error, ahead of any hold — an outage reads as an
// outage rather than as a quiet hold, and the hold is named on the next poll.
//
// A poll that delegates nothing logs ticketAutoDelegationSkipped once, naming
// the condition that stopped it: the day's cap is reached (cap-reached), the
// milestone had no eligible ticket (no-eligible-ticket), admission is held
// (breaker-tripped, provider-halted, peak-hold, budget-ceiling or
// budget-unknown), the review WIP limit has no room (wip-full), or the open-PR
// count could not be read (wip-unknown).
//
// It runs last in a poll, so a failure here cannot withhold the admission and
// dispatch that already succeeded. One is returned rather than swallowed, since
// nothing else would report it. The ticket it delegates is admitted by the next
// poll — the agent session that delegation opens wakes one within seconds.
func autoDelegate(ctx context.Context, d Deps, c Config, res *Result, history HistoryLoader) error {
	dailyRuns := c.dailyRuns()
	claimed, err := d.Queue.ClaimedSince(ctx, dayStart(d.Now()))
	if err != nil {
		return err
	}
	if claimed >= dailyRuns {
		d.Logger.Info("ticketAutoDelegationSkipped", "reason", "cap-reached",
			"claimed", claimed, "dailyRuns", dailyRuns)
		return nil
	}
	open, err := d.Delegatable.Open(ctx)
	if err != nil {
		return err
	}
	milestone := activeMilestone(open)
	delegable, err := delegatableTickets(ctx, d, c, open, milestone)
	if err != nil {
		return err
	}
	if len(delegable) == 0 {
		d.Logger.Info("ticketAutoDelegationSkipped", "reason", "no-eligible-ticket",
			"open", len(open), "milestone", milestone.ID)
		return nil
	}
	if reason, ceiling := admissionHold(ctx, d, c, history); reason != "" {
		d.Logger.Info("ticketAutoDelegationSkipped", "reason", reason, "ceiling", ceiling)
		return nil
	}
	limits := c.Limits.orDefault()
	prs := countOpenPRs(ctx, d, c.Repos)
	if !prs.known {
		d.Logger.Info("ticketAutoDelegationSkipped", "reason", "wip-unknown")
		return nil
	}
	if prs.count >= limits.ReviewWIP {
		d.Logger.Info("ticketAutoDelegationSkipped", "reason", "wip-full",
			"openPRs", prs.count, "reviewWIP", limits.ReviewWIP)
		return nil
	}
	room := min(dailyRuns-claimed, limits.ReviewWIP-prs.count)
	for _, issue := range delegable[:min(room, len(delegable))] {
		if err := d.Delegatable.DelegateIssue(ctx, issue.UUID); err != nil {
			d.Logger.Error("ticketDelegationFailed", "ticket", issue.ID, "err", err)
			return err
		}
		d.Logger.Info("ticketAutoDelegated", "ticket", issue.ID, "rule", delegateRule,
			"milestone", milestone.ID, "priority", issue.Priority)
		res.Delegated = append(res.Delegated, issue.ID)
	}
	return nil
}

// admissionHold names what would stop the poll's admission claiming the default
// plan's next run, read as admission reads it: the breaker, the plan's halt, the
// peak-hour hold and the budget ceiling. A ticket delegated into a held
// admission only queues, so the delegation phase waits too. The ceiling names
// the budget window when that is the reason, and an unreadable spend holds.
func admissionHold(ctx context.Context, d Deps, c Config, history HistoryLoader) (reason, ceiling string) {
	if breakerTripped(ctx, d) {
		return "breaker-tripped", ""
	}
	if providerHalted(ctx, d, runner.Provider(c.Model)) {
		return "provider-halted", ""
	}
	if PeakHeld(readPeakMode(ctx, d), c.Model, d.Now(), 0) {
		return "peak-hold", ""
	}
	reserved, settled, err := d.Queue.Spend(ctx, c.Budget, c.Model, d.Now(), history)
	if err != nil {
		d.Logger.Warn("delegationSpendUnread", "err", err.Error())
		return "budget-unknown", ""
	}
	if fits, binding := Decide(c.Budget, c.Model, reserved, settled, Reservation{}); !fits {
		return "budget-ceiling", binding
	}
	return "", ""
}

// activeMilestone is the milestone a poll works through: the earliest one that
// still has an open ticket, by Linear's own milestone order. No open ticket in a
// milestone at all leaves it zero, and the poll then works through the tickets
// in none.
//
// A ticket that is open counts, delegated or not: a milestone whose tickets are
// all in flight is still the one being worked through, so the poll does not
// start the next one behind it.
func activeMilestone(open []Issue) Milestone {
	var earliest Milestone
	found := false
	for _, issue := range open {
		if issue.Milestone.ID == "" || (found && issue.Milestone.SortOrder >= earliest.SortOrder) {
			continue
		}
		earliest, found = issue.Milestone, true
	}
	return earliest
}

// delegatableTickets is the open tickets a poll may delegate, in the order it
// delegates them: those in the milestone it is working through, so the earliest
// milestone is finished before the next begins.
func delegatableTickets(ctx context.Context, d Deps, c Config, open []Issue, milestone Milestone) ([]Issue, error) {
	// The rank is kept beside the ticket, so the sort reads a priority the
	// eligibility check has already accepted.
	type ranked struct {
		issue Issue
		rank  int
	}
	var candidates []ranked
	for _, issue := range open {
		if !inMilestone(issue, milestone) {
			continue
		}
		rank, ok := delegatableTicket(issue, c)
		if !ok {
			continue
		}
		// A run that has already ended leaves its ticket to the owner's own
		// `runner requeue`: delegating it again would change nothing.
		awaitingRequeue, err := d.Queue.NeedsRequeue(ctx, issue.ID)
		if err != nil {
			return nil, err
		}
		if awaitingRequeue {
			continue
		}
		candidates = append(candidates, ranked{issue: issue, rank: rank})
	}
	slices.SortFunc(candidates, func(a, b ranked) int {
		// Priority first, then the identifier, so a tie picks the same ticket
		// on every poll the ticket stays eligible.
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.issue.ID, b.issue.ID))
	})
	out := make([]Issue, 0, len(candidates))
	for _, cand := range candidates {
		out = append(out, cand.issue)
	}
	return out, nil
}

// delegatableTicket reports whether a poll may delegate issue, and the rank its
// Linear priority gives it in the delegation order. A ticket is delegated only
// with the runner's own allowlist and a size beside its lane, and never with a
// workflow path in its body, the owner's hold, or a delegate already set.
func delegatableTicket(issue Issue, c Config) (rank int, ok bool) {
	rank, err := PriorityRank(issue.Priority)
	switch {
	case err != nil,
		// An unknown state reads as started, so only a ticket Linear reports as
		// not begun is taken.
		!slices.Contains(unstartedStates, issue.StateType),
		!delegateLane.MatchString(issue.Title),
		strings.Contains(issue.Body, workflowPath),
		issue.DelegatedTo != "",
		slices.ContainsFunc(issue.Labels, func(l string) bool { return strings.EqualFold(l, holdLabel) }):
		return 0, false
	}
	if _, reason, _ := repository(issue.Labels, c.Repos); reason != "" {
		return 0, false
	}
	if _, reason, _ := sizeLabel(issue.Labels); reason != "" {
		return 0, false
	}
	return rank, true
}

// inMilestone reports whether a poll may delegate issue, which it may only in
// the milestone it is working through.
func inMilestone(issue Issue, milestone Milestone) bool { return issue.Milestone.ID == milestone.ID }

// dayStart is the start of the UTC day now falls in: the boundary the daily run
// cap is counted from, the UTC calendar the budget's own month window shares.
func dayStart(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}
