package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// defaultDecisionTimeout is how long a run waits on an owner decision before
// the dispatcher stops it as waiting-on-owner, used when Config names none.
const defaultDecisionTimeout = 24 * time.Hour

// WaitingRun is one run parked on a decision, as a poll reads it: the last
// decision it has not answered, its session, and what has been said so far.
type WaitingRun struct {
	RunID    string
	Decision runner.Decision
	// SessionID is the session the decision was asked in, empty until posted.
	SessionID string
	AskedAt   time.Time
	PostedAt  time.Time
	// Reply is the owner's latest reply text, empty when none has arrived.
	Reply     string
	RepliedAt time.Time
	RunURL    string
	// Index is the decision's position on the row, 1-based, so a second
	// question on one run names a different notice.
	Index int
}

// Decisions is the store's decision surface as a poll reads and writes it.
type Decisions interface {
	// Waiting lists the runs parked on a decision.
	Waiting(ctx context.Context) ([]WaitingRun, error)
	// Answer writes the chosen option and returns the run to the queue.
	Answer(ctx context.Context, runID, answer string, at time.Time) error
	// Posted stamps the decision as asked in a session.
	Posted(ctx context.Context, runID, sessionID string, at time.Time) error
	// StopWaiting ends a wait unanswered, recording the waiting-on-owner stop.
	StopWaiting(ctx context.Context, runID string, d runner.Decision, at time.Time) error
	// Session reads the Linear agent session a ticket's delegation marked.
	Session(ctx context.Context, ticketID string) (string, error)
}

// Elicitor asks the owner a decision in a run's Linear agent session.
type Elicitor interface {
	Elicit(ctx context.Context, sessionID string, d runner.Decision) error
}

// decisionTimeout is c's configured wait, or the default when unset.
func (c Config) decisionTimeout() time.Duration {
	if c.DecisionTimeout <= 0 {
		return defaultDecisionTimeout
	}
	return c.DecisionTimeout
}

// postDecisions settles every run parked on a decision: the post that asks it,
// the answer that resumes the run, the re-ask of a reply matching no option,
// and the deadline that stops it. It runs at the poll's end, before the
// notices, so a stop it raises this poll is notified in the same poll.
func postDecisions(ctx context.Context, d Deps, c Config) error {
	waiting, err := d.Decisions.Waiting(ctx)
	if err != nil {
		return err
	}
	if len(waiting) == 0 {
		return nil
	}
	at := d.Now()
	var errs []error
	for _, w := range waiting {
		if err := decide(ctx, d, c, w, at); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// decide settles one waiting run, in this order: a reply newer than the post is
// matched (a match resumes, a miss re-asks the same options and never guesses);
// no reply past the deadline stops the run; otherwise an unposted decision is
// posted.
func decide(ctx context.Context, d Deps, c Config, w WaitingRun, at time.Time) error {
	switch {
	case w.RepliedAt.After(w.PostedAt):
		opt, ok := w.Decision.Match(w.Reply)
		if !ok {
			d.Logger.Info("decisionReplyUnmatched", "run", w.RunID)
			return postDecision(ctx, d, w, at)
		}
		if err := d.Decisions.Answer(ctx, w.RunID, opt.Label, at); err != nil {
			return err
		}
		d.Logger.Info("decisionAnswered", "run", w.RunID, "choice", opt.Value)
		return nil
	case at.After(deadline(w, c.decisionTimeout())):
		return stopWaiting(ctx, d, w, at)
	case w.PostedAt.IsZero():
		return postDecision(ctx, d, w, at)
	}
	return nil
}

// deadline is when an unanswered run stops: the last thing said — the question
// asked, or the owner's latest reply — plus the configured wait. Measuring from
// the last word keeps an owner mid-conversation from being cut off by a window
// that began before they spoke.
func deadline(w WaitingRun, timeout time.Duration) time.Time {
	from := w.AskedAt
	if w.RepliedAt.After(from) {
		from = w.RepliedAt
	}
	return from.Add(timeout)
}

// postDecision asks the run's decision in its agent session, once. A missing
// session or a refused post leaves it unposted for the next poll; the deadline
// still stops the run, so nothing hangs silently.
func postDecision(ctx context.Context, d Deps, w WaitingRun, at time.Time) error {
	session, err := d.Decisions.Session(ctx, w.RunID)
	if err != nil {
		return err
	}
	if session == "" {
		d.Logger.Warn("decisionSessionMissing", "run", w.RunID)
		return nil
	}
	if err := d.Elicit.Elicit(ctx, session, w.Decision); err != nil {
		d.Logger.Warn("decisionElicitFailed", "run", w.RunID, "err", err.Error())
		return nil
	}
	if err := d.Decisions.Posted(ctx, w.RunID, session, at); err != nil {
		return err
	}
	d.Logger.Info("decisionPosted", "run", w.RunID, "options", len(w.Decision.Options))
	return nil
}

// stopWaiting ends an unanswered run: it raises one Create-only notice carrying
// the decision, then records the waiting-on-owner stop. The notice's id carries
// the decision, so a second question on one run raises its own. A raise that
// failed leaves the run waiting, so the next poll raises it again rather than
// stopping the run with no notice at all.
func stopWaiting(ctx context.Context, d Deps, w WaitingRun, at time.Time) error {
	n := Notice{ID: waitingNoticeID(w.RunID, w.Index), Class: string(runner.StopWaitingOnOwner), Link: w.RunURL}
	if err := d.Notices.Raise(ctx, n, at); err != nil {
		return fmt.Errorf("raising the waiting-on-owner notice for run %s: %w", w.RunID, err)
	}
	if err := d.Decisions.StopWaiting(ctx, w.RunID, w.Decision, at); err != nil {
		return err
	}
	d.Logger.Info("decisionTimedOut", "run", w.RunID, "notice", n.ID)
	return nil
}

// waitingNoticeID names the one notice a run's nth decision raises when it goes
// unanswered.
func waitingNoticeID(runID string, index int) string {
	return "waiting-" + runID + "#" + strconv.Itoa(index)
}
