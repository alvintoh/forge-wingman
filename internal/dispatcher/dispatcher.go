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
	// UUID is Linear's own id for the issue, which the mutation that delegates
	// one names.
	UUID string
	// DelegatedTo is the id of the agent the issue is delegated to, empty when
	// none: Open reads it, and a ticket that names one is never delegated again.
	DelegatedTo string
	// StateType is the Linear state type of the issue's workflow state, empty
	// when the reply did not carry it.
	StateType string
	// Milestone is the project milestone the issue belongs to, zero when it has
	// none.
	Milestone Milestone
	// BlockedBy and Blocks are the ids of the issues Linear relates to this
	// one as blocking it and as blocked by it.
	BlockedBy []string
	Blocks    []string
	// BlockedByOpen names the blocked-by issues Linear still holds open — any
	// state type but completed or canceled, or one the reply did not carry,
	// which counts as open.
	BlockedByOpen []string
	// BlockerStates maps each BlockedByOpen id to its Linear state type, empty
	// when the reply did not carry one.
	BlockerStates map[string]string
}

// Milestone is the Linear project milestone an issue belongs to.
type Milestone struct {
	ID string
	// SortOrder is Linear's own milestone order: a smaller value is earlier.
	SortOrder float64
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
	// Stage is the run's stage — plan or build; empty for a single-stage
	// claim, which plans inside its own job. Dispatch sends it as the
	// workflow's stage input only for the plan stage.
	Stage string
	// Attempt is the run's attempt the claim booked its reservation under, which the run settles it by.
	Attempt int
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
	// NextPlans are the paid plans a run moves to, in order, once the ceilings
	// of the plan Model names bind; empty when Model's plan is the only one.
	NextPlans []PaidPlan
	// Limits are the concurrency ceilings; a zero field takes its default.
	Limits Limits
	// Tuning is the concurrency rule's numbers; a zero field takes its default.
	Tuning Tuning
	// LastResortPlans are the plans' free models, each entry one plan's list, in
	// the order the free-tier fallback walks them; empty for a configuration
	// with no free tier to fall to.
	LastResortPlans [][]string
	// TwoStage is whether an M or L run is planned before it is admitted to
	// build. Off by default; a run plans inside its own build job, and the
	// stage input the workflows carry goes unsent.
	TwoStage bool
	// DecisionTimeout is how long a run waits on an owner decision before it is
	// stopped as waiting-on-owner. Zero takes the default.
	DecisionTimeout time.Duration
	// DecisionMode is how a plan's decisions are settled. Empty takes guided.
	DecisionMode runner.DecisionMode
}

// PaidPlan is a paid plan a run may move to: the ceilings it is admitted
// against and the models the run starts on there.
type PaidPlan struct {
	Plan   string
	Budget BudgetConfig
	Models runner.ModelLabels
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

// Deps are the tickets a poll reads and delegates, the queue it writes, the
// estimator and repository visibility it checks a claim's budget against, the
// provider halt state it checks before claiming (AC4), the workflow it
// dispatches, and where it reports.
type Deps struct {
	Source Source
	// Delegatable lists the tickets the product may delegate to itself.
	Delegatable Delegatable
	Queue       Queue
	Estimator   Estimator
	Visibility  RepoVisibility
	Providers   ProviderHalts
	Breaker     Breaker
	PeakModes   PeakModes
	Autonomies  Autonomies
	OpenPRs     OpenPRCounter
	// PullRequests reads the merged run PRs the rewrite rate is measured from,
	// and Rewrites records each measurement on its run row and reads the samples
	// the level's conditions are checked against.
	PullRequests PullRequests
	Rewrites     Rewrites
	// Earned stores the level the measured PRs have earned, Shadow counts the
	// merges since the shadow began, CDs reads the
	// recorded CD flags it is computed with, and History records each change of
	// the level.
	Earned   EarnedSnapshots
	Shadow   ShadowCounter
	CDs      CDFlags
	History  AutonomyHistory
	Workflow Workflow
	Logger   *slog.Logger
	Now      func() time.Time
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
	// Elicit asks the owner a decision in a run's Linear agent session, and
	// Decisions is the store's decision surface a poll reads and writes.
	Elicit    Elicitor
	Decisions Decisions
}

// missing names the first dependency left unset, or "" when all are wired.
func (d Deps) missing() string {
	for _, dep := range []struct {
		name string
		set  bool
	}{
		{"Source", d.Source != nil},
		{"Delegatable", d.Delegatable != nil},
		{"Queue", d.Queue != nil},
		{"Estimator", d.Estimator != nil},
		{"Visibility", d.Visibility != nil},
		{"Providers", d.Providers != nil},
		{"Breaker", d.Breaker != nil},
		{"PeakModes", d.PeakModes != nil},
		{"Autonomies", d.Autonomies != nil},
		{"OpenPRs", d.OpenPRs != nil},
		{"PullRequests", d.PullRequests != nil},
		{"Rewrites", d.Rewrites != nil},
		{"Earned", d.Earned != nil},
		{"Shadow", d.Shadow != nil},
		{"CDs", d.CDs != nil},
		{"History", d.History != nil},
		{"Workflow", d.Workflow != nil},
		{"Logger", d.Logger != nil},
		{"Now", d.Now != nil},
		{"Overrides", d.Overrides != nil},
		{"ModelPlans", d.ModelPlans != nil},
		{"Notices", d.Notices != nil},
		{"OpenPoster", d.OpenPoster != nil},
		{"Elicit", d.Elicit != nil},
		{"Decisions", d.Decisions != nil},
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

// Delegatable reads the open tickets the product may take on itself and
// delegates one to the configured agent.
type Delegatable interface {
	// Open returns every open issue, with the delegate and milestone a poll
	// needs to choose the next ticket to delegate itself.
	Open(ctx context.Context) ([]Issue, error)
	// DelegateIssue sets the issue's delegate to that agent, and reports whether
	// Linear took it.
	DelegateIssue(ctx context.Context, issueID string) error
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

// Reprioritizer sets the priority rank of a queued or planned run, reporting
// whether the run is in such a state; a run in any other state keeps its rank.
// A rank the run already holds is not written.
type Reprioritizer interface {
	UpdateQueuedPriority(ctx context.Context, runID string, rank int) (bool, error)
}

// Claimer attempts to admit a candidate and claim it, atomically with every
// other poll that might race it (adr/0003); Release returns a claimed run
// whose dispatch never reached GitHub.
//
// TryClaim reports (true, "", nil) once runID is claimed and its reservation
// booked; (false, "", nil) when another poll already claimed the row first, or
// when the row was found to have ended without a claim and is settled to
// claimed instead — neither is an admission matter, so nothing is deferred; and
// (false, <binding>, nil) when Admit withholds it, naming the first condition
// that does.
type Claimer interface {
	TryClaim(ctx context.Context, runID string, at time.Time, cfg BudgetConfig, facts Facts, res Reservation) (ok bool, binding string, err error)
	Release(ctx context.Context, runID string, at time.Time) error
}

// Rejector records why a delegated ticket was not admitted.
type Rejector interface {
	Reject(ctx context.Context, r Rejection) error
}

// ClaimedRuns counts the runs claimed since a moment: the runs a level's daily
// bar is spent from.
type ClaimedRuns interface {
	ClaimedSince(ctx context.Context, since time.Time) (int, error)
}

// EndedRuns reports whether a ticket's run has already ended, so that only the
// owner's own `runner requeue` takes that ticket further.
type EndedRuns interface {
	NeedsRequeue(ctx context.Context, ticketID string) (bool, error)
}

// Queue is the run store as a poll uses it.
type Queue interface {
	Enqueuer
	Candidater
	Reprioritizer
	Claimer
	Rejector
	Spender
	Sweeper
	StuckReader
	ClaimedRuns
	EndedRuns
	TransientRequeuer
	// SettledHistory reads the settled run history at or after since: the one
	// read a poll's budget ceilings are summed from, whatever it walks (AC1).
	SettledHistory(ctx context.Context, since time.Time) (SettledHistory, error)
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
	// Delegated is the id of every ticket this poll delegated to the agent
	// itself, in the order it delegated them.
	Delegated []string

	// issues is what the poll read from Linear, kept for the stuck check.
	issues []Issue
}

// Poll admits every issue still delegated, then returns the runs that stopped
// on a known transient fault to the queue and walks it in priority order
// claiming every run Admit lets start, abandons the claims that outlived
// their window, measures the merges that landed since the last poll, raises a
// notice for each run stuck past its limit, and posts the notices waiting,
// whatever the rest of the poll did.
// A ticket the queue already holds is left alone; a candidate a condition
// withholds is deferred, not rejected, and reconsidered next poll; and a run
// whose dispatch fails is released rather than left claimed, so the next
// poll considers it again. The tickets the product delegates to itself are
// taken on at the end of the poll.
func Poll(ctx context.Context, d Deps, c Config) (Result, error) {
	if name := d.missing(); name != "" {
		return Result{}, fmt.Errorf("dispatcher dependency %s is not set", name)
	}
	res, err := poll(ctx, d, c)
	start := d.Now()
	sweepErr := sweepAbandoned(ctx, d)
	LogPhase(d.Logger, "sweep", start, d.Now())
	start = d.Now()
	rewriteErr := measureRewrites(ctx, d, c)
	LogPhase(d.Logger, "rewrite", start, d.Now())
	start = d.Now()
	decideErr := postDecisions(ctx, d, c)
	LogPhase(d.Logger, "decisions", start, d.Now())
	start = d.Now()
	stuckErr := noticeStuck(ctx, d, res.issues)
	LogPhase(d.Logger, "stuck", start, d.Now())
	start = d.Now()
	noticeErr := postNotices(ctx, d)
	LogPhase(d.Logger, "notices", start, d.Now())
	return res, errors.Join(err, sweepErr, rewriteErr, decideErr, stuckErr, noticeErr)
}

// LogPhase logs how long one phase of a poll took, so a slow poll can be traced
// to the call it waited on.
func LogPhase(logger *slog.Logger, phase string, start, end time.Time) {
	logger.Info("pollPhase", "phase", phase, "ms", end.Sub(start).Milliseconds())
}

func poll(ctx context.Context, d Deps, c Config) (Result, error) {
	// The level is read once, ahead of the delegation that spends its daily bar
	// and the claims that record it, so one poll acts on one level throughout.
	level := readAutonomy(ctx, d)
	// One loader for the whole poll: the claim walk and the delegation share it,
	// so the settled run history is read once whichever of them reaches for it
	// first, and neither reads it at all unless something needs it (AC1, AC5).
	history := newHistoryLoader(ctx, d.Queue, c.historySince(d.Now()))
	start := d.Now()
	issues, err := d.Source.Delegated(ctx)
	if err != nil {
		return Result{}, err
	}
	LogPhase(d.Logger, "delegated", start, d.Now())
	res := Result{Seen: len(issues), issues: issues}
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
		relations[issue.ID] = Relations{
			Known: true, BlockedBy: issue.BlockedBy, Blocks: issue.Blocks, BlockedByOpen: issue.BlockedByOpen,
		}
		links[issue.ID] = issue.URL
	}
	// A run that stopped on a known transient fault returns to the queue ahead
	// of the claim walk, so the retry it earns is claimed by this same poll
	// rather than waiting for the next one.
	start = d.Now()
	requeueErr := requeueStopped(ctx, d, level)
	LogPhase(d.Logger, "requeue", start, d.Now())
	start = d.Now()
	claims, claimErr := admitClaims(ctx, d, c, level, relations, links, &res, history)
	LogPhase(d.Logger, "claim", start, d.Now())
	phaseErrs := []error{requeueErr, claimErr}
	start = d.Now()
	for _, claim := range claims {
		if err := dispatch(ctx, d, claim); err != nil {
			phaseErrs = append(phaseErrs, err)
			continue
		}
		res.Dispatched = append(res.Dispatched, claim.RunID)
	}
	LogPhase(d.Logger, "dispatch", start, d.Now())
	// The delegation runs last, so a failure on it cannot withhold the admission
	// and dispatch that already succeeded: the agent session it opens wakes the
	// poll that admits the ticket within seconds.
	start = d.Now()
	phaseErrs = append(phaseErrs, autoDelegate(ctx, d, c, level, &res, history))
	LogPhase(d.Logger, "delegate", start, d.Now())
	if err := errors.Join(phaseErrs...); err != nil {
		return res, err
	}
	d.Logger.Info("pollComplete", "seen", res.Seen, "enqueued", len(res.Enqueued),
		"rejected", len(res.Rejections), "deferred", len(res.Deferrals), "dispatched", len(res.Dispatched),
		"delegated", len(res.Delegated))
	return res, nil
}

// admit queues one issue or records the refusal against it, adding what it did
// to res. A queue error is returned as the queue wrote it: the store already
// names the run, the ticket and the reason, and saying it twice reads as a
// different failure. The repository's visibility is resolved once, at
// creation, and carried on the run record from then on (FR-22) — Exists skips
// that resolution for a ticket already queued, rather than re-checking it on
// every poll the ticket remains undispatched. A ticket already queued has its
// rank refreshed from the issue instead, and a failed refresh is logged and
// leaves the poll running. The models a ticket names are
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
		if _, err := d.Queue.UpdateQueuedPriority(ctx, q.RunID, q.Priority); err != nil {
			d.Logger.Warn("priorityRefreshFailed", "run", q.RunID, "priority", q.Priority, "err", err.Error())
		}
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

// claimStage is the stage a claim for cand dispatches with. A run whose plan
// stage has recorded a plan claims its build; an unplanned M or L claims its
// plan stage when two-stage dispatch is on; and an S run — which is never
// plan-staged — claims single-stage.
func claimStage(cand Candidate, twoStage bool) string {
	if !twoStage {
		return ""
	}
	if cand.Stage == StageBuild {
		return StageBuild
	}
	if cand.Size == "S" {
		return ""
	}
	return StagePlan
}

// admitClaims walks the queue in priority order, claiming every candidate
// Admit lets start and recording each one it withholds as a deferral naming
// the condition that bound it.
//
// On an error it returns the claims already booked beside it, so the caller can
// dispatch or release them. The level is the poll's own, read once before this
// walk as the breaker is; the provider halt and the verdict are keyed off each
// candidate's own plan, and the open-PR count across the allowlist once, when
// anything is queued. A breaker
// that cannot be read holds the walk as a tripped one would, and a budget
// ceiling raises at most one notice. A candidate the plan's own window or cap
// withholds is retried on the next paid plans in order, and once every one binds
// on the configured free-tier plans, a private one only with the owner's private
// opt-in. A candidate held for peak hours is no ceiling's, so it is neither
// moved nor noticed.
//
// The settled run history is read at most once for the whole walk, the first
// time a candidate reaches its budget ceilings, and an estimate once per size
// rather than once per candidate: a backlog costs the poll one read of the
// history and one estimate of each size it holds, however many candidates are
// queued (AC1), and a walk whose candidates a non-budget limit withholds reads
// no history at all (AC5).
func admitClaims(ctx context.Context, d Deps, c Config, level Autonomy, relations map[string]Relations, links map[string]string, res *Result, history HistoryLoader) ([]Claim, error) {
	candidates, err := d.Queue.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	tripped := breakerTripped(ctx, d)
	limits := c.Limits.orDefault()
	var prs openPRCount
	var mode PeakMode
	if len(candidates) > 0 {
		prs = countOpenPRs(ctx, d, c.Repos)
		mode = readPeakMode(ctx, d)
	}
	// A size's estimate is the poll's, not the candidate's: one estimator read
	// per size, however many candidates of that size the walk considers.
	type sizeEstimate struct {
		est Estimate
		err error
	}
	estimated := map[string]sizeEstimate{}
	estimateOf := func(size string) (Estimate, error) {
		if v, ok := estimated[size]; ok {
			return v.est, v.err
		}
		est, err := d.Estimator.Estimate(ctx, size)
		estimated[size] = sizeEstimate{est: est, err: err}
		return est, err
	}
	planned := map[string]sizeEstimate{}
	planEstimateOf := func(size string) (Estimate, error) {
		if v, ok := planned[size]; ok {
			return v.est, v.err
		}
		est, err := d.Estimator.EstimatePlan(ctx, size)
		planned[size] = sizeEstimate{est: est, err: err}
		return est, err
	}
	// The halt, the verdict and the private opt-in are read lazily and once per
	// plan, since a run that moves to another free tier is judged by that plan.
	halts := map[string]bool{}
	halted := func(plan string) bool {
		if v, ok := halts[plan]; ok {
			return v
		}
		v := providerHalted(ctx, d, plan)
		halts[plan] = v
		return v
	}
	verdicts := map[string]providers.Verdict{}
	verdictOf := func(plan string) providers.Verdict {
		if v, ok := verdicts[plan]; ok {
			return v
		}
		v := lookupVerdict(ctx, d, plan)
		verdicts[plan] = v
		return v
	}
	optIns := map[string]bool{}
	privateOptIn := func(plan string) bool {
		if v, ok := optIns[plan]; ok {
			return v
		}
		v := lookupPrivateOptIn(ctx, d, plan)
		optIns[plan] = v
		return v
	}
	// A plan a run moves to needs the plan record a ticket's own models need at
	// enqueue, read lazily and once per plan.
	usables := map[string]bool{}
	usable := func(plan string) bool {
		if v, ok := usables[plan]; ok {
			return v
		}
		reason, detail, err := planRefusal(ctx, d.ModelPlans, plan)
		switch {
		case err != nil:
			d.Logger.Info("paidPlanLookupFailed", "provider", plan, "err", err.Error())
		case reason != "":
			d.Logger.Info("paidPlanRefused", "provider", plan, "reason", string(reason), "detail", detail)
		}
		usables[plan] = err == nil && reason == ""
		return usables[plan]
	}
	var claims []Claim
	noticed := map[string]bool{}
	for _, cand := range candidates {
		stage := claimStage(cand, c.TwoStage)
		est, err := estimateOf(cand.Size)
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
		if stage == StagePlan {
			// A plan stage's reservation is estimated from settled plan
			// phases rather than from whole runs.
			est, err = planEstimateOf(cand.Size)
			if err != nil {
				d.Logger.Info("planEstimateFailed", "run", cand.RunID, "size", cand.Size, "err", err.Error())
				res.Deferrals = append(res.Deferrals, Deferral{RunID: cand.RunID, Ceiling: ConditionEstimateFailed, At: d.Now()})
				continue
			}
		}
		reservation := Reservation{ProviderCost: est.ProviderCost, Stage: stage, Files: cand.PlanFiles}
		if cand.Private {
			reservation.RunnerMinutes = est.Minutes
		}
		budget, candModel, lastResort := c.Budget, cmp.Or(cand.Model, c.Model), (*runner.ModelLabels)(nil)
		if stage == StageBuild {
			budget, candModel, lastResort = c.buildTier(cand)
		}
		if lastResort != nil {
			reservation.ProviderCost = 0
		}
		candPlan := runner.Provider(candModel)
		facts := Facts{Limits: limits, Tuning: c.Tuning.OrDefault(), History: history, ProviderHalted: halted(candPlan), BreakerTripped: tripped,
			OpenPRs: prs.count, OpenPRsKnown: prs.known, Relations: relations[cand.RunID], Model: candModel, LastResort: lastResort,
			PeakHeld: PeakHeld(mode, candModel, d.Now(), est.Minutes), Autonomy: level, DecisionMode: c.DecisionMode}
		claimVerdict := verdictOf(candPlan)
		ok, binding, err := d.Queue.TryClaim(ctx, cand.RunID, d.Now(), budget, facts, reservation)
		if err != nil {
			return claims, err
		}
		// A build-stage claim keeps the tier its plan stage claimed — one tier
		// per run — so it never moves plans or falls back to the free tier,
		// which would mix tiers within the run; a ceiling that now binds
		// defers it instead. A ticket that named a model keeps its plan.
		if !ok && stage != StageBuild && !cand.ModelsNamed {
			from := candPlan
			for _, next := range c.NextPlans {
				if !budget.isProviderCeiling(binding) {
					break
				}
				if halted(next.Plan) || !usable(next.Plan) {
					continue
				}
				moved := onPlan(facts, next, from)
				moved.PeakHeld = PeakHeld(mode, moved.Model, d.Now(), est.Minutes)
				ok, binding, err = d.Queue.TryClaim(ctx, cand.RunID, d.Now(), next.Budget, moved, reservation)
				if err != nil {
					return claims, err
				}
				budget, facts = next.Budget, moved
				if ok {
					claimVerdict = verdictOf(next.Plan)
					d.Logger.Info("runMovedPlan", "run", cand.RunID, "plan", next.Plan, "from", from)
				}
				from = next.Plan
			}
		}
		if !ok && stage != StageBuild && budget.isProviderCeiling(binding) {
			paid := binding
			for _, rung := range c.LastResortPlans {
				// One free model cannot staff both the build and its review.
				if len(rung) < 2 {
					continue
				}
				rungPlan := runner.Provider(rung[0])
				if cand.Private && !privateOptIn(rungPlan) {
					d.Logger.Info("lastResortNeedsPrivateOptIn", "run", cand.RunID, "plan", rungPlan, "ceiling", paid)
					continue
				}
				freeFacts := onLastResort(facts, rung)
				freeFacts.ProviderHalted = halted(rungPlan)
				freeRes := Reservation{RunnerMinutes: reservation.RunnerMinutes}
				ok, binding, err = d.Queue.TryClaim(ctx, cand.RunID, d.Now(), c.Budget.lastResort(), freeFacts, freeRes)
				if err != nil {
					return claims, err
				}
				budget, facts, reservation = c.Budget.lastResort(), freeFacts, freeRes
				if ok {
					claimVerdict = verdictOf(rungPlan)
					d.Logger.Info("runOnLastResort", "run", cand.RunID, "model", facts.Model, "plan", rungPlan, "paidCeiling", paid)
				}
				if binding == "" {
					// Admitted, or another poll already claimed this row: no ceiling
					// binds, so the walk is over.
					break
				}
			}
		}
		if ok {
			claims = append(claims, Claim{RunID: cand.RunID, Repo: cand.Repo, Priority: cand.Priority, Verdict: claimVerdict, Stage: stage, Attempt: cand.Attempt})
			continue
		}
		if binding == "" {
			// Another poll already claimed this row: not an admission matter.
			continue
		}
		d.Logger.Info("runDeferred", "run", cand.RunID, "ceiling", binding)
		res.Deferrals = append(res.Deferrals, Deferral{RunID: cand.RunID, Ceiling: binding, At: d.Now()})
		if !noticed[binding] {
			noticed[binding] = noticeDeferral(ctx, d, budget, facts.Model, reservation, binding, links[cand.RunID], history)
		}
	}
	if len(candidates) > 0 && len(res.Deferrals) == len(candidates) {
		d.Logger.Info("everyCandidateDeferred", "candidates", len(candidates))
	}
	return claims, nil
}

// breakerTripped reads the breaker the way admission does: one that cannot be
// read holds as a tripped one would.
func breakerTripped(ctx context.Context, d Deps) bool {
	tripped, err := d.Breaker.Tripped(ctx)
	if err != nil {
		d.Logger.Warn("breakerCheckFailed", "err", err.Error())
		return true
	}
	return tripped
}

// providerHalted reads plan's halt the way admission does: a read that fails
// is logged and reads as not halted.
func providerHalted(ctx context.Context, d Deps, plan string) bool {
	v, err := d.Providers.Halted(ctx, plan)
	if err != nil {
		d.Logger.Info("providerHaltCheckFailed", "provider", plan, "err", err.Error())
		return false
	}
	return v
}

// buildTier is the budget, build model and free-tier models a build-stage claim
// is judged on: those of the tier its plan stage claimed, so the run is bound by
// one plan's ceilings throughout. A run whose plan is unknown to this
// configuration, or recorded none, keeps the default plan's.
func (c Config) buildTier(cand Candidate) (BudgetConfig, string, *runner.ModelLabels) {
	if cand.LastResort != nil {
		return c.Budget.lastResort(), cand.LastResort.Build, cand.LastResort
	}
	for _, p := range c.NextPlans {
		if p.Plan == cand.Plan {
			return p.Budget, cmp.Or(cand.Model, p.Models.Build), nil
		}
	}
	return c.Budget, cmp.Or(cand.Model, c.Model), nil
}

// onPlan is facts for a claim that moves to plan after from's ceiling bound: the
// plan's build model leads, and the move is recorded with the plan's models.
func onPlan(facts Facts, plan PaidPlan, from string) Facts {
	facts.Model, facts.Moved = plan.Models.Build, &Move{From: from, Models: plan.Models}
	return facts
}

// onLastResort is facts for a claim on the free tier of models: the first
// model builds and plans and the next reviews, so the build model never
// reviews its own work.
func onLastResort(facts Facts, models []string) Facts {
	labels := runner.ModelLabels{Build: models[0], Review: models[1], Plan: slices.Clone(models)}
	facts.Model, facts.LastResort, facts.Moved = labels.Build, &labels, nil
	return facts
}

// lookupPrivateOptIn reads whether the owner has opted private repositories in
// to plan's free tier. A failed read is logged and reads as no opt-in, so a
// private candidate defers rather than runs unconsented.
func lookupPrivateOptIn(ctx context.Context, d Deps, plan string) bool {
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
