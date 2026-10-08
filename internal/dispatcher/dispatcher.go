// Package dispatcher admits the tickets Linear delegates to the run queue and
// dispatches every run each poll's admission lets start.
package dispatcher

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// Issue is one Linear issue delegated to this agent.
type Issue struct {
	ID       string
	URL      string
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
	// The model refusals are a ticket naming a model a run may not use.
	RefusalModelMalformed    Refusal = "model-malformed"
	RefusalReviewIsBuild     Refusal = "review-model-is-build-model"
	RefusalModelUnconfigured Refusal = "model-provider-unconfigured"
	RefusalModelNotOptedIn   Refusal = "model-per-token-not-opted-in"
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
	// Models are the models the ticket named, carried onto the run record.
	Models runner.ModelLabels
	At     time.Time
}

// Claim is the run a poll took off the queue.
type Claim struct {
	RunID    string
	Repo     string
	Priority int
	// Verdict is the provider plan's recorded verdict at claim time; empty when none is wired.
	Verdict providers.Verdict
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
	// LastResort is the dispatched plan's free models in fall-through order,
	// empty for a plan with none.
	LastResort []string
}

// ProviderHalts reports whether repeated infra stops have halted dispatch to
// a provider (AC4), until an operator's runner enable-provider admits it
// again (AC5).
type ProviderHalts interface {
	Halted(ctx context.Context, provider string) (bool, error)
}

// ProviderVerdicts reports the owner's recorded verdict on a provider's plan.
type ProviderVerdicts interface {
	Verdict(ctx context.Context, provider string) (providers.Verdict, error)
}

// VerdictRecorder writes a claimed run's provider verdict onto its record.
type VerdictRecorder interface {
	RecordVerdict(ctx context.Context, runID string, verdict providers.Verdict) error
}

// ModelOverrides yields the models a ticket names in place of a run's defaults.
type ModelOverrides interface {
	Overrides(issue Issue) runner.ModelLabels
}

// ModelPlans reads the owner's plan record for a provider; ok is false when
// none is defined.
type ModelPlans interface {
	Plan(ctx context.Context, provider string) (plan providers.Plan, ok bool, err error)
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
	Breaker    Breaker
	OpenPRs    OpenPRCounter
	Workflow   Workflow
	Logger     *slog.Logger
	Now        func() time.Time
	// Overrides yields the models a ticket names and ModelPlans is what they are
	// checked against; both are required, as an unchecked model could spend per token.
	Overrides  ModelOverrides
	ModelPlans ModelPlans
	Notices    Notices
	// OpenPoster is called only once a notice is claimed, so a missing destination never blocks dispatch.
	OpenPoster func(ctx context.Context) (Poster, error)
	// Plans is optional: left unset, no verdict is looked up.
	Plans ProviderVerdicts
	// Verdicts is optional: left unset, no verdict is written to the run.
	Verdicts VerdictRecorder
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
		{"Breaker", d.Breaker != nil},
		{"OpenPRs", d.OpenPRs != nil},
		{"Workflow", d.Workflow != nil},
		{"Logger", d.Logger != nil},
		{"Now", d.Now != nil},
		{"Overrides", d.Overrides != nil},
		{"ModelPlans", d.ModelPlans != nil},
		{"Notices", d.Notices != nil},
		{"OpenPoster", d.OpenPoster != nil},
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
	Spender
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
// order claiming every run Admit lets start, and posts the notices waiting,
// whatever the rest of the poll did.
// A ticket the queue already holds is left alone; a candidate a condition
// withholds is deferred, not rejected, and reconsidered next poll; and a run
// whose dispatch fails is released rather than left claimed, so the next
// poll considers it again.
func Poll(ctx context.Context, d Deps, c Config) (Result, error) {
	if name := d.missing(); name != "" {
		return Result{}, fmt.Errorf("dispatcher dependency %s is not set", name)
	}
	res, err := poll(ctx, d, c)
	start := d.Now()
	noticeErr := postNotices(ctx, d)
	LogPhase(d.Logger, "notices", start, d.Now())
	return res, errors.Join(err, noticeErr)
}

// LogPhase logs how long one phase of a poll took, so a slow poll can be traced
// to the call it waited on.
func LogPhase(logger *slog.Logger, phase string, start, end time.Time) {
	logger.Info("pollPhase", "phase", phase, "ms", end.Sub(start).Milliseconds())
}

func poll(ctx context.Context, d Deps, c Config) (Result, error) {
	start := d.Now()
	issues, err := d.Source.Delegated(ctx)
	if err != nil {
		return Result{}, err
	}
	LogPhase(d.Logger, "delegated", start, d.Now())
	res := Result{Seen: len(issues)}
	start = d.Now()
	for _, issue := range issues {
		admitErr := admit(ctx, d, c, issue, &res)
		if admitErr != nil {
			return res, admitErr
		}
	}
	LogPhase(d.Logger, "admit", start, d.Now())
	relations := make(map[string]Relations, len(issues))
	links := make(map[string]string, len(issues))
	for _, issue := range issues {
		relations[issue.ID] = Relations{Known: true, BlockedBy: issue.BlockedBy, Blocks: issue.Blocks}
		links[issue.ID] = issue.URL
	}
	start = d.Now()
	claims, claimErr := admitClaims(ctx, d, c, relations, links, &res)
	LogPhase(d.Logger, "claim", start, d.Now())
	dispatchErrs := []error{claimErr}
	start = d.Now()
	for _, claim := range claims {
		if err := dispatch(ctx, d, claim); err != nil {
			dispatchErrs = append(dispatchErrs, err)
			continue
		}
		res.Dispatched = append(res.Dispatched, claim.RunID)
	}
	LogPhase(d.Logger, "dispatch", start, d.Now())
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
// every poll the ticket remains undispatched. The models a ticket names are
// checked at the same point, for the same reason.
//
// A visibility-check or model-lookup failure aborts only this ticket's
// admission, never the poll: an issue not yet enqueued is seen again next poll
// (Delegated still returns it), so nothing is lost, and one flaky check must not
// withhold every other delegated ticket's admission or the ready dispatch that
// follows it.
func admit(ctx context.Context, d Deps, c Config, issue Issue, res *Result) error {
	q, rej := build(issue, c, d.Now())
	if rej.Reason != "" {
		return refuse(ctx, d, rej, res)
	}
	exists, err := d.Queue.Exists(ctx, q.RunID)
	if err != nil {
		return err
	}
	if exists {
		d.Logger.Info("ticketAlreadyQueued", "run", q.RunID, "repo", q.Repo, "priority", q.Priority)
		return nil
	}
	q.Models = d.Overrides.Overrides(issue)
	reason, detail, err := checkModels(ctx, d.ModelPlans, q.Models, c.Model)
	if err != nil {
		// A failed read is neither a refusal nor an admission: the ticket is
		// seen again next poll, and admitting it unchecked would let a per-token
		// model through.
		d.Logger.Info("modelConfigLookupFailed", "run", q.RunID, "err", err.Error())
		return nil
	}
	if reason != "" {
		return refuse(ctx, d, rejection(issue, reason, detail, d.Now()), res)
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

// refuse records the refusal against its ticket.
func refuse(ctx context.Context, d Deps, r Rejection, res *Result) error {
	if err := d.Queue.Reject(ctx, r); err != nil {
		return err
	}
	d.Logger.Warn("ticketRefused", "ticket", r.Ticket, "reason", string(r.Reason), "detail", r.Detail)
	res.Rejections = append(res.Rejections, r)
	return nil
}

// admitClaims walks the queue in priority order, claiming every candidate
// Admit lets start and recording each one it withholds as a deferral naming
// the condition that bound it.
//
// On an error it returns the claims already booked beside it, so the caller can
// dispatch or release them. The breaker and the provider halt are read once
// ahead of the walk, since every candidate starts on the same model, and the
// open-PR count across the allowlist once, when anything is queued. A breaker
// that cannot be read holds the walk as a tripped one would, and a budget
// ceiling raises at most one notice. A candidate the plan's own window or cap
// withholds is retried on the plan's free tier, a private one only with the
// owner's private opt-in.
func admitClaims(ctx context.Context, d Deps, c Config, relations map[string]Relations, links map[string]string, res *Result) ([]Claim, error) {
	candidates, err := d.Queue.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	tripped, err := d.Breaker.Tripped(ctx)
	if err != nil {
		d.Logger.Warn("breakerCheckFailed", "err", err.Error())
		tripped = true
	}
	provider := runner.Provider(c.Model)
	halted, err := d.Providers.Halted(ctx, provider)
	if err != nil {
		d.Logger.Info("providerHaltCheckFailed", "provider", provider, "err", err.Error())
		halted = false
	}
	verdict := lookupVerdict(ctx, d, provider)
	limits := c.Limits.orDefault()
	var prs openPRCount
	var privateOptIn bool
	if len(candidates) > 0 {
		prs = countOpenPRs(ctx, d, c.Repos)
		privateOptIn = lookupPrivateOptIn(ctx, d, c.LastResort)
	}
	var claims []Claim
	noticed := map[string]bool{}
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
		facts := Facts{Limits: limits, Tuning: c.Tuning.OrDefault(), ProviderHalted: halted, BreakerTripped: tripped,
			OpenPRs: prs.count, OpenPRsKnown: prs.known, Relations: relations[cand.RunID], Model: cmp.Or(cand.Model, c.Model)}
		budget := c.Budget
		ok, binding, err := d.Queue.TryClaim(ctx, cand.RunID, d.Now(), budget, facts, reservation)
		if err != nil {
			return claims, err
		}
		if !ok && budget.isProviderCeiling(binding) && len(c.LastResort) > 1 {
			if cand.Private && !privateOptIn {
				d.Logger.Info("lastResortNeedsPrivateOptIn", "run", cand.RunID, "ceiling", binding)
			} else {
				paid := binding
				budget, facts, reservation = budget.lastResort(), onLastResort(facts, c.LastResort), Reservation{RunnerMinutes: reservation.RunnerMinutes}
				ok, binding, err = d.Queue.TryClaim(ctx, cand.RunID, d.Now(), budget, facts, reservation)
				if err != nil {
					return claims, err
				}
				if ok {
					d.Logger.Info("runOnLastResort", "run", cand.RunID, "model", facts.Model, "paidCeiling", paid)
				}
			}
		}
		if ok {
			claims = append(claims, Claim{RunID: cand.RunID, Repo: cand.Repo, Priority: cand.Priority, Verdict: verdict})
			continue
		}
		if binding == "" {
			// Another poll already claimed this row: not an admission matter.
			continue
		}
		d.Logger.Info("runDeferred", "run", cand.RunID, "ceiling", binding)
		res.Deferrals = append(res.Deferrals, Deferral{RunID: cand.RunID, Ceiling: binding, At: d.Now()})
		if !noticed[binding] {
			noticed[binding] = noticeDeferral(ctx, d, budget, facts.Model, reservation, binding, links[cand.RunID])
		}
	}
	if len(candidates) > 0 && len(res.Deferrals) == len(candidates) {
		d.Logger.Info("everyCandidateDeferred", "candidates", len(candidates))
	}
	return claims, nil
}

// onLastResort is facts for a claim on the free tier of models: the first
// model builds and plans and the next reviews, so the build model never
// reviews its own work.
func onLastResort(facts Facts, models []string) Facts {
	labels := runner.ModelLabels{Build: models[0], Review: models[1], Plan: slices.Clone(models)}
	facts.Model, facts.LastResort = labels.Build, &labels
	return facts
}

// lookupPrivateOptIn reads whether the owner has opted private repositories in
// to the free tier models belong to. A failed read is logged and reads as no
// opt-in, so a private candidate defers rather than runs unconsented.
func lookupPrivateOptIn(ctx context.Context, d Deps, models []string) bool {
	if len(models) == 0 {
		return false
	}
	plan := runner.Provider(models[0])
	p, _, err := d.ModelPlans.Plan(ctx, plan)
	if err != nil {
		d.Logger.Info("privateOptInLookupFailed", "provider", plan, "err", err.Error())
		return false
	}
	return p.PrivateOptIn
}

// lookupVerdict reads provider's recorded verdict. A failed read is logged and
// reported as unknown rather than blocking the run: the verdict informs the
// owner and never gates dispatch.
func lookupVerdict(ctx context.Context, d Deps, provider string) providers.Verdict {
	if d.Plans == nil {
		return ""
	}
	verdict, err := d.Plans.Verdict(ctx, provider)
	if err != nil {
		d.Logger.Info("providerVerdictLookupFailed", "provider", provider, "err", err.Error())
		return providers.VerdictUnknown
	}
	return verdict
}

// recordVerdict writes the claim's verdict onto its run record. A failed write
// is logged and never blocks the dispatch.
func recordVerdict(ctx context.Context, d Deps, claim Claim) {
	if d.Verdicts == nil || claim.Verdict == "" {
		return
	}
	if err := d.Verdicts.RecordVerdict(ctx, claim.RunID, claim.Verdict); err != nil {
		d.Logger.Warn("verdictRecordFailed", "run", claim.RunID, "err", err.Error())
	}
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
	recordVerdict(ctx, d, claim)
	err := d.Workflow.Dispatch(ctx, claim)
	if err == nil {
		d.Logger.Info("runDispatched", "run", claim.RunID, "repo", claim.Repo, "priority", claim.Priority, "verdict", string(claim.Verdict))
		return nil
	}
	d.Logger.Error("runDispatchFailed", "run", claim.RunID, "repo", claim.Repo, "err", err)
	if rerr := d.Queue.Release(ctx, claim.RunID, d.Now()); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}
