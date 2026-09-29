// Package dispatcher admits the tickets Linear delegates to the run queue and
// dispatches every run each poll's admission lets start.
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
	// BlockedBy and Blocks are the ids of the issues Linear relates to this
	// one as blocking it and as blocked by it.
	BlockedBy []string
	Blocks    []string
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
	Private   bool
	Priority  int
	BlockedBy []string
	Blocks    []string
	At        time.Time
}

// Claim is the run a poll took off the queue.
type Claim struct {
	RunID    string
	Repo     string
	Priority int
}

// Config is the boundary a poll admits inside: the repositories it may
// dispatch into, the ceilings FR-22 checks before claiming one, and the
// model every dispatched run starts with (run.yml's own workflow_dispatch
// default) — read here only to name its provider for AC4's halt check, not a
// new per-ticket configuration surface. The agent it acts for is the
// Source's own, which proves the token reads as that agent before any
// ticket is looked at.
type Config struct {
	Repos  []string
	Budget BudgetConfig
	Model  string
	// Limits are the concurrency ceilings; a zero field takes its default.
	Limits Limits
	// Tuning is the concurrency rule's numbers; a zero field takes its default.
	Tuning Tuning
}

// ProviderHalts reports whether repeated infra stops have halted dispatch to
// a provider (AC4), until an operator's runner enable-provider admits it
// again (AC5).
type ProviderHalts interface {
	Halted(ctx context.Context, provider string) (bool, error)
}

// Deps are the tickets a poll reads, the queue it writes, the estimator and
// repository visibility it checks a claim's budget against, the provider
// halt state it checks before claiming (AC4), the workflow it dispatches,
// and where it reports.
type Deps struct {
	Source     Source
	Queue      Queue
	Estimator  Estimator
	Visibility RepoVisibility
	Providers  ProviderHalts
	OpenPRs    OpenPRCounter
	Workflow   Workflow
	Logger     *slog.Logger
	Now        func() time.Time
}

// missing names the first dependency left unset, or "" when all are wired.
func (d Deps) missing() string {
	for _, dep := range []struct {
		name string
		set  bool
	}{
		{"Source", d.Source != nil},
		{"Queue", d.Queue != nil},
		{"Estimator", d.Estimator != nil},
		{"Visibility", d.Visibility != nil},
		{"Providers", d.Providers != nil},
		{"OpenPRs", d.OpenPRs != nil},
		{"Workflow", d.Workflow != nil},
		{"Logger", d.Logger != nil},
		{"Now", d.Now != nil},
	} {
		if !dep.set {
			return dep.name
		}
	}
	return ""
}

// OpenPRCounter counts the open agent PRs a repository holds, the review WIP
// admission limits.
type OpenPRCounter interface {
	OpenAgentPRs(ctx context.Context, repo string) (int, error)
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

// Claimer attempts to admit a candidate and claim it, atomically with every
// other poll that might race it (adr/0003); Release returns a claimed run
// whose dispatch never reached GitHub.
//
// TryClaim reports (true, "", nil) once runID is claimed and its reservation
// booked; (false, "", nil) when another poll already claimed the row first —
// not an admission matter, so nothing is deferred; and (false, <binding>, nil)
// when Admit withholds it, naming the first condition that does.
type Claimer interface {
	TryClaim(ctx context.Context, runID string, at time.Time, cfg BudgetConfig, facts Facts, res Reservation) (ok bool, binding string, err error)
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
	// Deferrals is every candidate a ceiling or condition withheld this poll,
	// in the order admission walked them (FR-22). Reconsidered next poll.
	Deferrals []Deferral
	// Dispatched is the id of every run this poll claimed and started.
	Dispatched []string
}

// Poll admits every issue still delegated, then walks the queue in priority
// order claiming every run Admit lets start.
// A ticket the queue already holds is left alone; a candidate a condition
// withholds is deferred, not rejected, and reconsidered next poll; and a run
// whose dispatch fails is released rather than left claimed, so the next
// poll considers it again.
func Poll(ctx context.Context, d Deps, c Config) (Result, error) {
	if name := d.missing(); name != "" {
		return Result{}, fmt.Errorf("dispatcher dependency %s is not set", name)
	}
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
	relations := make(map[string]Relations, len(issues))
	for _, issue := range issues {
		relations[issue.ID] = Relations{Known: true, BlockedBy: issue.BlockedBy, Blocks: issue.Blocks}
	}
	claims, claimErr := admitClaims(ctx, d, c, relations, &res)
	dispatchErrs := []error{claimErr}
	for _, claim := range claims {
		if err := dispatch(ctx, d, claim); err != nil {
			dispatchErrs = append(dispatchErrs, err)
			continue
		}
		res.Dispatched = append(res.Dispatched, claim.RunID)
	}
	if err := errors.Join(dispatchErrs...); err != nil {
		return res, err
	}
	d.Logger.Info("pollComplete", "seen", res.Seen, "enqueued", len(res.Enqueued),
		"rejected", len(res.Rejections), "deferred", len(res.Deferrals), "dispatched", len(res.Dispatched))
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
		d.Logger.Info("visibilityCheckFailed", "run", q.RunID, "repo", q.Repo, "err", err.Error())
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

// admitClaims walks the queue in priority order, claiming every candidate
// Admit lets start and recording each one it withholds as a deferral naming
// the condition that bound it.
//
// On an error it returns the claims already booked beside it, so the caller can
// dispatch or release them. The provider halt is read once ahead of the walk,
// since every candidate starts on the same model, and the open-PR count across
// the allowlist once, when anything is queued.
func admitClaims(ctx context.Context, d Deps, c Config, relations map[string]Relations, res *Result) ([]Claim, error) {
	candidates, err := d.Queue.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	provider := runner.Provider(c.Model)
	halted, err := d.Providers.Halted(ctx, provider)
	if err != nil {
		d.Logger.Info("providerHaltCheckFailed", "provider", provider, "err", err.Error())
		halted = false
	}
	limits := c.Limits.orDefault()
	var prs openPRCount
	if len(candidates) > 0 {
		prs = countOpenPRs(ctx, d, c.Repos)
	}
	var claims []Claim
	for _, cand := range candidates {
		est, err := d.Estimator.Estimate(ctx, cand.Size)
		if err != nil {
			// A failed estimate withholds only this candidate, not the whole
			// walk: the next poll re-reads the same candidates in the same
			// order, so nothing is lost, and one flaky estimate must not
			// block a lower-priority candidate whose own estimate would
			// have succeeded.
			d.Logger.Info("estimateFailed", "run", cand.RunID, "size", cand.Size, "err", err.Error())
			res.Deferrals = append(res.Deferrals, Deferral{RunID: cand.RunID, Ceiling: ConditionEstimateFailed, At: d.Now()})
			continue
		}
		reservation := Reservation{ProviderCost: est.ProviderCost}
		if cand.Private {
			reservation.RunnerMinutes = est.Minutes
		}
		facts := Facts{Limits: limits, Tuning: c.Tuning.OrDefault(), ProviderHalted: halted, OpenPRs: prs.count, OpenPRsKnown: prs.known,
			Relations: relations[cand.RunID]}
		ok, binding, err := d.Queue.TryClaim(ctx, cand.RunID, d.Now(), c.Budget, facts, reservation)
		if err != nil {
			return claims, err
		}
		if ok {
			claims = append(claims, Claim{RunID: cand.RunID, Repo: cand.Repo, Priority: cand.Priority})
			continue
		}
		if binding == "" {
			// Another poll already claimed this row: not an admission matter.
			continue
		}
		d.Logger.Info("runDeferred", "run", cand.RunID, "ceiling", binding)
		res.Deferrals = append(res.Deferrals, Deferral{RunID: cand.RunID, Ceiling: binding, At: d.Now()})
	}
	if len(candidates) > 0 && len(claims) == 0 && len(res.Deferrals) == len(candidates) {
		d.Logger.Info("everyCandidateDeferred", "candidates", len(candidates))
	}
	return claims, nil
}

// openPRCount is the open agent PR count across the allowlist, and whether
// every read behind it succeeded.
type openPRCount struct {
	count int
	known bool
}

func countOpenPRs(ctx context.Context, d Deps, repos []string) openPRCount {
	total := 0
	for _, repo := range repos {
		n, err := d.OpenPRs.OpenAgentPRs(ctx, repo)
		if err != nil {
			d.Logger.Info("openPRCountFailed", "repo", repo, "err", err.Error())
			return openPRCount{}
		}
		total += n
	}
	return openPRCount{count: total, known: true}
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
