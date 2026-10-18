package dispatcher

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// DefaultTransientRequeues is how many times a poll requeues a run that stopped
// on a known transient infrastructure fault before leaving it to the owner.
const DefaultTransientRequeues = 2

const (
	exhaustedClass     = "transient-exhausted:"
	exhaustedNoticeKey = "transient-exhausted-"
)

// TransientStop is a run that stopped on a known transient infrastructure
// fault: the class the runner recorded on its row and how many automatic
// requeues it has already had.
type TransientStop struct {
	RunID    string
	Ticket   string
	Repo     string
	Class    runner.TransientClass
	Attempts int
}

// TransientRequeuer is the run store as a poll's transient requeue reads it:
// the stopped runs carrying a class, and the requeue that returns one to the
// queue.
type TransientRequeuer interface {
	// TransientStops lists every ended run carrying a transient class.
	TransientStops(ctx context.Context) ([]TransientStop, error)
	// RequeueTransient returns runID to the queue as the next attempt for
	// class, recording the attempt, and reports how many automatic requeues it
	// has now had. It refuses a run that has not ended, and one that has used
	// every one of max attempts.
	RequeueTransient(ctx context.Context, runID string, at time.Time, class runner.TransientClass, max int) (int, error)
}

// requeueStopped returns every run that stopped on a known transient fault to
// the queue, up to DefaultTransientRequeues times each, and raises a notice for
// one that has used them all, so it waits for the owner. A requeue only returns
// the run to the queue: the claim that follows it is admitted like any other,
// so the budget ceilings, the review WIP limit and the breaker hold it exactly
// as they hold a run's first attempt.
func requeueStopped(ctx context.Context, d Deps) error {
	stops, err := d.Queue.TransientStops(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range stops {
		// A class the list does not carry is never requeued: the listing is a
		// query on the class, and this keeps a row written by something else
		// from being acted on.
		if !s.Class.Listed() {
			continue
		}
		if s.Attempts >= DefaultTransientRequeues {
			if err := d.Notices.Raise(ctx, s.ExhaustedNotice(), d.Now()); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		attempt, err := d.Queue.RequeueTransient(ctx, s.RunID, d.Now(), s.Class, DefaultTransientRequeues)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		d.Logger.Warn("runRequeued", "run", s.RunID, "ticket", s.Ticket, "class", string(s.Class), "requeues", attempt)
	}
	return errors.Join(errs...)
}

// ExhaustedNotice is the notice a poll raises for a run whose transient faults
// have used every automatic requeue: the run, its class and the attempts made,
// then the command that takes it further by hand. All of it is ids and this
// package's own class names, never text a ticket wrote.
func (s TransientStop) ExhaustedNotice() Notice {
	class := exhaustedClass + s.RunID
	if s.Ticket != s.RunID {
		class += " ticket " + s.Ticket
	}
	class += " " + string(s.Class) + " after " + strconv.Itoa(s.Attempts) +
		" automatic requeues; requeue with: runner requeue -run-id " + s.RunID
	return Notice{ID: exhaustedNoticeKey + s.RunID + "-" + strconv.Itoa(s.Attempts), Class: class, Link: s.link()}
}

// link is the repository's Actions page, where the owner reads what the run's
// attempts did.
func (s TransientStop) link() string {
	if s.Repo == "" {
		return ""
	}
	return "https://github.com/" + s.Repo + "/actions"
}
