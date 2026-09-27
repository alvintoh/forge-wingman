// Package dispatcher admits the tickets Linear delegates to the run queue and
// dispatches the highest-priority run each poll finds there.
package dispatcher

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Issue is one Linear issue delegated to this agent.
type Issue struct {
	ID       string
	Title    string
	Body     string
	Priority int
	Labels   []string
}

// Refusal is why a delegated ticket was not admitted to the queue.
type Refusal string

const (
	RefusalNoSize        Refusal = "no-size"
	RefusalSizeUnknown   Refusal = "size-unknown"
	RefusalAboveCeiling  Refusal = "size-above-ceiling"
	RefusalNoRepository  Refusal = "no-repository"
	RefusalNotAllowlist  Refusal = "repository-not-allowlisted"
	RefusalTicketInvalid Refusal = "ticket-invalid"
)

// Rejection is the refusal kept against one ticket, with what made it.
type Rejection struct {
	Ticket string
	Reason Refusal
	Detail string
	At     time.Time
}

// Queued is one admitted run waiting to be dispatched.
type Queued struct {
	RunID    string
	Ticket   runner.Ticket
	Repo     string
	Priority int
	At       time.Time
}

// Claim is the run a poll took off the queue.
type Claim struct {
	RunID    string
	Repo     string
	Priority int
}

// Config is the boundary a poll admits inside: the repositories it may
// dispatch into. The agent it acts for is the Source's own, which proves the
// token reads as that agent before any ticket is looked at.
type Config struct {
	Repos []string
}

// Deps are the tickets a poll reads, the queue it writes and the workflow it
// dispatches, and where it reports.
type Deps struct {
	Source   Source
	Queue    Queue
	Workflow Workflow
	Logger   *slog.Logger
	Now      func() time.Time
}

// Source yields the issues delegated to the configured agent, and proves the
// token reads as that agent.
type Source interface {
	Delegated(ctx context.Context) ([]Issue, error)
}

// Enqueuer writes a run record and queues it, failing with ErrAlreadyQueued
// when the ticket already has one.
type Enqueuer interface {
	Enqueue(ctx context.Context, q Queued) error
}

// Claimer takes the highest-priority queued run, or reports that there is none
// left; Release returns a claimed run whose dispatch never reached GitHub.
type Claimer interface {
	Claim(ctx context.Context, at time.Time) (Claim, bool, error)
	Release(ctx context.Context, runID string, at time.Time) error
}

// Rejector records why a delegated ticket was not admitted.
type Rejector interface {
	Reject(ctx context.Context, r Rejection) error
}

// Queue is the run store as a poll uses it.
type Queue interface {
	Enqueuer
	Claimer
	Rejector
}

// Workflow starts one run in its target repository.
type Workflow interface {
	Dispatch(ctx context.Context, c Claim) error
}

// ErrAlreadyQueued reports a ticket the queue already holds, which every poll
// after the first that enqueued it is expected to hit.
var ErrAlreadyQueued = errors.New("run is already queued")

// ErrDelegateMismatch reports a poll whose token reads as a Linear agent other
// than the configured delegate, so nothing it read may be acted on.
var ErrDelegateMismatch = errors.New("Linear delegate mismatch")

// Result is what one poll found delegated, what it admitted, and what it dispatched.
type Result struct {
	Seen       int
	Enqueued   []string
	Rejections []Rejection
	// Dispatched is the claimed run's id, empty when the queue yielded nothing.
	Dispatched string
}

// Poll admits every issue still delegated, then dispatches the
// highest-priority run the queue yields. A ticket the queue already holds is
// left alone, and a run whose dispatch fails is released rather than left
// claimed, so the next poll considers it again.
func Poll(ctx context.Context, d Deps, c Config) (Result, error) {
	issues, err := d.Source.Delegated(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Seen: len(issues)}
	for _, issue := range issues {
		admitErr := admit(ctx, d, c, issue, &res)
		if admitErr != nil {
			return res, admitErr
		}
	}
	claim, ok, err := d.Queue.Claim(ctx, d.Now())
	if err != nil {
		return res, err
	}
	if ok {
		if err := dispatch(ctx, d, claim); err != nil {
			return res, err
		}
		res.Dispatched = claim.RunID
	}
	d.Logger.Info("pollComplete", "seen", res.Seen, "enqueued", len(res.Enqueued),
		"rejected", len(res.Rejections), "dispatched", res.Dispatched)
	return res, nil
}

// admit queues one issue or records the refusal against it, adding what it did
// to res. A queue error is returned as the queue wrote it: the store already
// names the run, the ticket and the reason, and saying it twice reads as a
// different failure.
func admit(ctx context.Context, d Deps, c Config, issue Issue, res *Result) error {
	q, rejection := build(issue, c, d.Now())
	if rejection.Reason != "" {
		if err := d.Queue.Reject(ctx, rejection); err != nil {
			return err
		}
		d.Logger.Warn("ticketRefused", "ticket", rejection.Ticket, "reason", string(rejection.Reason),
			"detail", rejection.Detail)
		res.Rejections = append(res.Rejections, rejection)
		return nil
	}
	switch err := d.Queue.Enqueue(ctx, q); {
	case errors.Is(err, ErrAlreadyQueued):
		d.Logger.Info("ticketAlreadyQueued", "run", q.RunID, "repo", q.Repo, "priority", q.Priority)
	case err != nil:
		return err
	default:
		d.Logger.Info("ticketEnqueued", "run", q.RunID, "repo", q.Repo, "size", q.Ticket.Size, "priority", q.Priority)
		res.Enqueued = append(res.Enqueued, q.RunID)
	}
	return nil
}

// dispatch starts the claimed run, returning it to the queue if GitHub refused
// it. A release that fails too is joined to the dispatch error rather than
// replacing it, so the run is not silently left claimed.
func dispatch(ctx context.Context, d Deps, claim Claim) error {
	err := d.Workflow.Dispatch(ctx, claim)
	if err == nil {
		d.Logger.Info("runDispatched", "run", claim.RunID, "repo", claim.Repo, "priority", claim.Priority)
		return nil
	}
	d.Logger.Error("runDispatchFailed", "run", claim.RunID, "repo", claim.Repo, "err", err)
	if rerr := d.Queue.Release(ctx, claim.RunID, d.Now()); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}
