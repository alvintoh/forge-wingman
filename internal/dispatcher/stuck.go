package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// DefaultStuckRunAfter is how long a run may sit unclaimed or stopped before
// it raises a notice, and DefaultStuckLookback how far back a stopped run is
// still read.
const (
	DefaultStuckRunAfter = 4 * time.Hour
	DefaultStuckLookback = 7 * 24 * time.Hour

	stuckClass     = "stuck:"
	stuckNoticeKey = "stuck-"
)

// The states a stuck run is held in.
const (
	StuckQueued    = "queued"
	StuckPlanned   = "planned"
	StuckStopped   = "stopped"
	StuckAbandoned = "abandoned"
)

// Stuck is a run that has sat in State, for Reason.
type Stuck struct {
	RunID  string
	Ticket string
	State  string
	// Reason is a Condition, a stop reason, an outcome or an abandoned reason,
	// empty when the row records none.
	Reason string
}

// StuckReader lists the runs that have been stuck since before before: those
// still waiting to be claimed, and those that stopped or were abandoned no
// earlier than since. A run since dispatched, merged or requeued is not listed.
type StuckReader interface {
	StuckRuns(ctx context.Context, before, since time.Time) ([]Stuck, error)
}

// noticeStuck raises one notice per stuck run, naming why it is held and what
// clears it. The notice id carries the run, state and reason, so a changed
// state or reason is a new notice and a repeat poll is not.
func noticeStuck(ctx context.Context, d Deps, issues []Issue) error {
	at := d.Now()
	runs, err := d.Queue.StuckRuns(ctx, at.Add(-DefaultStuckRunAfter), at.Add(-DefaultStuckLookback))
	if err != nil {
		return fmt.Errorf("listing stuck runs: %w", err)
	}
	byTicket := make(map[string]Issue, len(issues))
	for _, issue := range issues {
		byTicket[issue.ID] = issue
	}
	var errs []error
	for _, run := range runs {
		issue := byTicket[run.Ticket]
		n := Notice{
			ID:     stuckNoticeKey + url.PathEscape(run.RunID+"-"+run.State+"-"+run.Reason),
			Class:  stuckClass + run.RunID,
			Detail: stuckDetail(run, issue),
			Link:   issue.URL,
		}
		if err := d.Notices.Raise(ctx, n, at); err != nil {
			d.Logger.Warn("stuckNoticeNotRaised", "run", run.RunID, "state", run.State, "err", err.Error())
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// stuckDetail says why run is held and what clears it, built only from the run
// id, closed state and reason words, the issue's open blockers and fixed text.
func stuckDetail(run Stuck, issue Issue) string {
	switch run.State {
	case StuckQueued, StuckPlanned:
		if len(issue.BlockedByOpen) > 0 {
			return run.State + ", blocked by " + blockerList(issue) + "; clears when they finish"
		}
		if run.Reason == "" {
			return run.State + ", not yet claimed"
		}
		return run.State + ", held by " + run.Reason + "; clears when that condition lifts"
	}
	detail := run.State
	if run.Reason != "" {
		detail += " (" + run.Reason + ")"
	}
	return detail + "; requeue with: runner requeue -run-id " + run.RunID
}

// blockerList names each open blocker, in identifier order, with its state type
// when Linear carried one: "ABC-9 (started)".
func blockerList(issue Issue) string {
	ids := slices.Clone(issue.BlockedByOpen)
	slices.Sort(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id
		if state := issue.BlockerStates[id]; state != "" {
			parts[i] += " (" + state + ")"
		}
	}
	return strings.Join(parts, ", ")
}
