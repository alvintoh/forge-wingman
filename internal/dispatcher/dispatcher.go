// Package dispatcher admits the tickets Linear delegates to the run queue and
// dispatches the highest-priority run each poll finds there.
package dispatcher

import (
	"context"
	"errors"
	"fmt"
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
	RunID  string
	Ticket runner.Ticket
	Repo   string
	// Private is whether Repo is a private repository, resolved once at
	// admission and carried on the run record from then on (FR-22, NFR-1):
	// its runner minutes count toward the cash ceiling; a public repo's
	// count as zero.
	Private  bool
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
// dispatch into, and the ceilings FR-22 checks before claiming one. The
// agent it acts for is the Source's own, which proves the token reads as
// that agent before any ticket is looked at.
type Config struct {
	Repos  []string
	Budget BudgetConfig
}

// Deps are the tickets a poll reads, the queue it writes, the estimator and
// repository visibility it checks a claim's budget against, the workflow it
// dispatches, and where it reports.
type Deps struct {
	Source     Source
	Queue      Queue
	Estimator  Estimator
	Visibility RepoVisibility
	Workflow   Workflow
	Logger     *slog.Logger
	Now        func() time.Time
}

// Source yields the issues delegated to the configured agent, and proves the
// token reads as that agent.
type Source interface {
	Delegated(ctx context.Context) ([]Issue, error)
}

// Enqueuer writes a run record and queues it, failing with ErrAlreadyQueued
// when the ticket already has one. Exists reports whether a run record
// already exists for runID, so a caller can skip resolving anything the
// record would only need once, at creation.
type Enqueuer interface {
	Enqueue(ctx context.Context, q Queued) error
	Exists(ctx context.Context, runID string) (bool, error)
}

// Candidater lists queued runs a poll may claim, in Linear priority order
// (adr/0003).
type Candidater interface {
	Candidates(ctx context.Context) ([]Candidate, error)
}

// Claimer attempts to reserve a candidate's budget and claim it, atomically
// with every other poll that might race it (adr/0003); Release returns a
// claimed run whose dispatch never reached GitHub.
//
// TryClaim reports (true, "", nil) once runID is claimed and its reservation
// booked; (false, "", nil) when another poll already claimed the row first —
// not a budget matter, so nothing is deferred; and (false, <ceiling>, nil)
// when cfg and res together would breach a ceiling, naming which one.
type Claimer interface {
	TryClaim(ctx context.Context, runID string, at time.Time, cfg BudgetConfig, res Reservation) (ok bool, binding string, err error)
	Release(ctx context.Context, runID string, at time.Time) error
}

// Rejector records why a delegated ticket was not admitted.
type Rejector interface {
	Reject(ctx context.Context, r Rejection) error
}

// Queue is the run store as a poll uses it.
type Queue interface {
	Enqueuer
	Candidater
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

// Result is what one poll found delegated, what it admitted, and what it
// dispatched.
type Result struct {
	Seen       int
	Enqueued   []string
	Rejections []Rejection
	// Deferrals is every candidate a ceiling withheld this poll, in the order
	// admission walked them (FR-22). Reconsidered next poll, not permanent.
	Deferrals []Deferral
	// Dispatched is the claimed run's id, empty when nothing was claimed.
	Dispatched string
}

// Poll admits every issue still delegated, then walks the queue in priority
// order claiming the first run whose estimate fits every budget ceiling.
// A ticket the queue already holds is left alone; a candidate a ceiling would
// breach is deferred, not rejected, and reconsidered next poll; and a run
// whose dispatch fails is released rather than left claimed, so the next
// poll considers it again.
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
	claim, ok, err := admitClaim(ctx, d, c, &res)
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
		"rejected", len(res.Rejections), "deferred", len(res.Deferrals), "dispatched", res.Dispatched)
	return res, nil
}

// admit queues one issue or records the refusal against it, adding what it did
// to res. A queue error is returned as the queue wrote it: the store already
// names the run, the ticket and the reason, and saying it twice reads as a
// different failure. The repository's visibility is resolved once, at
// creation, and carried on the run record from then on (FR-22) — Exists skips
// that resolution for a ticket already queued, rather than re-checking it on
// every poll the ticket remains undispatched.
//
// A visibility-check failure aborts only this ticket's admission, never the
// poll: an issue not yet enqueued is seen again next poll (Delegated still
// returns it), so nothing is lost, and one flaky check must not withhold every
// other delegated ticket's admission or the ready dispatch that follows it.
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
	exists, err := d.Queue.Exists(ctx, q.RunID)
	if err != nil {
		return err
	}
	if exists {
		d.Logger.Info("ticketAlreadyQueued", "run", q.RunID, "repo", q.Repo, "priority", q.Priority)
		return nil
	}
	private, err := d.Visibility.Private(ctx, q.Repo)
	if err != nil {
		d.Logger.Warn("visibilityCheckFailed", "run", q.RunID, "repo", q.Repo, "err", err.Error())
		return nil
	}
	q.Private = private
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

// admitClaim walks the queue in priority order, claiming the first candidate
// whose estimated cost fits every ceiling FR-22 checks. Each one it skips
// over is recorded as a deferral naming the ceiling that bound it.
func admitClaim(ctx context.Context, d Deps, c Config, res *Result) (Claim, bool, error) {
	candidates, err := d.Queue.Candidates(ctx)
	if err != nil {
		return Claim{}, false, err
	}
	for _, cand := range candidates {
		est, err := d.Estimator.Estimate(ctx, cand.Size)
		if err != nil {
			return Claim{}, false, fmt.Errorf("estimating a size-%s run: %w", cand.Size, err)
		}
		reservation := Reservation{ProviderCost: est.ProviderCost}
		if cand.Private {
			reservation.RunnerMinutes = est.Minutes
		}
		ok, binding, err := d.Queue.TryClaim(ctx, cand.RunID, d.Now(), c.Budget, reservation)
		if err != nil {
			return Claim{}, false, err
		}
		if ok {
			return Claim{RunID: cand.RunID, Repo: cand.Repo, Priority: cand.Priority}, true, nil
		}
		if binding == "" {
			// Another poll already claimed this row: not a budget matter.
			continue
		}
		d.Logger.Warn("runDeferred", "run", cand.RunID, "ceiling", binding)
		res.Deferrals = append(res.Deferrals, Deferral{RunID: cand.RunID, Ceiling: binding, At: d.Now()})
	}
	return Claim{}, false, nil
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
