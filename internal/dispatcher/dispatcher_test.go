package dispatcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

var pollAt = time.Date(2026, 9, 27, 9, 15, 0, 0, time.UTC)

var errFirestore = errors.New("firestore is unreachable")

// fakeSource hands over the issues a poll is to consider.
type fakeSource struct {
	issues []Issue
	err    error
}

func (s fakeSource) Delegated(context.Context) ([]Issue, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.issues, nil
}

// tryClaimCall is one TryClaim invocation a test can inspect: what run it was
// asked to claim, the facts admission was given, and the reservation it was
// asked to claim it with.
type tryClaimCall struct {
	runID string
	cfg   BudgetConfig
	facts Facts
	res   Reservation
}

// fakeQueue is the store a poll writes, with the outcome each call is to have.
type fakeQueue struct {
	queued     []Queued
	holds      map[string]bool
	rejected   []Rejection
	released   []string
	enqueueErr error
	rejectErr  error
	releaseErr error

	candidates    []Candidate
	candidatesErr error

	// bindings maps a candidate's run id to the ceiling TryClaim reports it
	// would breach; a run id absent from both bindings and unavailable is
	// claimed.
	bindings map[string]string
	// freeBindings is what TryClaim reports for a run asked on a free tier, in place of bindings.
	freeBindings map[string]string
	unavailable  map[string]bool
	tryClaimErr  error
	// tryClaimErrFor fails TryClaim for one run id only.
	tryClaimErrFor map[string]error

	claimed   []string
	tryClaims []tryClaimCall

	existsErr error

	// reserved and settled are the spend Spend reports at every moment; spends
	// records each moment it was asked about.
	reserved Totals
	settled  Settled
	spendErr error
	spends   []time.Time
}

func (q *fakeQueue) Spend(_ context.Context, _ BudgetConfig, _ string, at time.Time) (Totals, Settled, error) {
	q.spends = append(q.spends, at)
	return q.reserved, q.settled, q.spendErr
}

func (q *fakeQueue) Exists(_ context.Context, runID string) (bool, error) {
	if q.existsErr != nil {
		return false, q.existsErr
	}
	return q.holds[runID], nil
}

func (q *fakeQueue) Enqueue(_ context.Context, run Queued) error {
	if q.enqueueErr != nil {
		return q.enqueueErr
	}
	if q.holds[run.RunID] {
		return ErrAlreadyQueued
	}
	q.queued = append(q.queued, run)
	return nil
}

func (q *fakeQueue) Candidates(context.Context) ([]Candidate, error) {
	if q.candidatesErr != nil {
		return nil, q.candidatesErr
	}
	return q.candidates, nil
}

func (q *fakeQueue) TryClaim(_ context.Context, runID string, _ time.Time, cfg BudgetConfig, facts Facts, res Reservation) (bool, string, error) {
	if q.tryClaimErr != nil {
		return false, "", q.tryClaimErr
	}
	if err := q.tryClaimErrFor[runID]; err != nil {
		return false, "", err
	}
	q.tryClaims = append(q.tryClaims, tryClaimCall{runID: runID, cfg: cfg, facts: facts, res: res})
	bindings := q.bindings
	if cfg.ProviderFree {
		bindings = q.freeBindings
	}
	if binding, ok := bindings[runID]; ok {
		return false, binding, nil
	}
	if q.unavailable[runID] {
		return false, "", nil
	}
	q.claimed = append(q.claimed, runID)
	return true, "", nil
}

func (q *fakeQueue) Release(_ context.Context, runID string, _ time.Time) error {
	if q.releaseErr != nil {
		return q.releaseErr
	}
	q.released = append(q.released, runID)
	return nil
}

func (q *fakeQueue) Reject(_ context.Context, r Rejection) error {
	if q.rejectErr != nil {
		return q.rejectErr
	}
	q.rejected = append(q.rejected, r)
	return nil
}

// fakeWorkflow records the run it was asked to start.
type fakeWorkflow struct {
	claims []Claim
	err    error
}

func (w *fakeWorkflow) Dispatch(_ context.Context, c Claim) error {
	if w.err != nil {
		return w.err
	}
	w.claims = append(w.claims, c)
	return nil
}

// fakeEstimator reports the estimate configured for each size, defaulting to
// a zero estimate for a size it holds none for. planBySize is the same for the
// plan stage. errBySize fails one size specifically, so a test can prove a
// failed estimate withholds only that candidate; err fails every size.
type fakeEstimator struct {
	bySize     map[string]Estimate
	planBySize map[string]Estimate
	planErr    error
	err        error
	errBySize  map[string]error
}

func (e fakeEstimator) Estimate(_ context.Context, size string) (Estimate, error) {
	if err, ok := e.errBySize[size]; ok {
		return Estimate{}, err
	}
	if e.err != nil {
		return Estimate{}, e.err
	}
	return e.bySize[size], nil
}

func (e fakeEstimator) EstimatePlan(_ context.Context, size string) (Estimate, error) {
	if e.planErr != nil {
		return Estimate{}, e.planErr
	}
	if est, ok := e.planBySize[size]; ok {
		return est, nil
	}
	return e.Estimate(context.Background(), size)
}

// fakeVisibility reports the visibility configured for each repository,
// defaulting to public for one it holds none for. calls counts every
// invocation, through a pointer so it survives the struct being copied into
// Deps by value — a test asserting admission never reached this check reads
// it directly rather than inferring it from Poll's outcome.
type fakeVisibility struct {
	private map[string]bool
	err     error
	calls   *int
}

func (v fakeVisibility) Private(_ context.Context, repo string) (bool, error) {
	if v.calls != nil {
		*v.calls++
	}
	if v.err != nil {
		return false, v.err
	}
	return v.private[repo], nil
}

// fakeProviders reports the halt state configured for each provider.
type fakeProviders struct {
	halted map[string]bool
	err    error
}

func (p fakeProviders) Halted(_ context.Context, provider string) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.halted[provider], nil
}

// fakeOpenPRs reports the open agent PR count configured for each repository,
// and counts the reads made of each. calls is a pointer for the same reason
// fakeVisibility's is.
type fakeOpenPRs struct {
	open     map[string]int
	err      error
	failOnly string
	calls    map[string]int
}

func (p fakeOpenPRs) OpenAgentPRs(_ context.Context, repo string) (int, error) {
	if p.calls != nil {
		p.calls[repo]++
	}
	if p.err != nil && (p.failOnly == "" || p.failOnly == repo) {
		return 0, p.err
	}
	return p.open[repo], nil
}

func pollDeps(source Source, q *fakeQueue, w *fakeWorkflow) Deps {
	return Deps{
		Source: source, Queue: q, Workflow: w,
		Estimator:  fakeEstimator{},
		Visibility: fakeVisibility{},
		Providers:  fakeProviders{},
		Breaker:    fakeBreaker{},
		OpenPRs:    fakeOpenPRs{},
		Overrides:  LabelOverrides{},
		ModelPlans: &fakeModelPlans{},
		Notices:    &fakeNotices{},
		OpenPoster: func(context.Context) (Poster, error) { return &fakePoster{}, nil },
		Elicit:     &fakeElicitor{},
		Decisions:  &fakeDecisions{},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        func() time.Time { return pollAt },
	}
}

// fakeElicitor records the decisions a poll asked and the session it asked in.
type fakeElicitor struct {
	mu    sync.Mutex
	err   error
	calls []elicitCall
}

type elicitCall struct {
	sessionID string
	decision  runner.Decision
}

func (e *fakeElicitor) Elicit(_ context.Context, sessionID string, d runner.Decision) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, elicitCall{sessionID: sessionID, decision: d})
	return e.err
}

// fakeDecisions is the store's decision surface as a poll uses it.
type fakeDecisions struct {
	waiting    []WaitingRun
	waitingErr error
	answerErr  error
	postedErr  error
	stopErr    error
	sessions   map[string]string
	sessionErr error

	answered []answerCall
	posted   []postedCall
	stopped  []stopCall
}

type answerCall struct {
	runID, answer string
	at            time.Time
}
type postedCall struct {
	runID, sessionID string
	at               time.Time
}
type stopCall struct {
	runID    string
	question string
	at       time.Time
}

func (d *fakeDecisions) Waiting(context.Context) ([]WaitingRun, error) {
	return d.waiting, d.waitingErr
}

func (d *fakeDecisions) Answer(_ context.Context, runID, answer string, at time.Time) error {
	if d.answerErr != nil {
		return d.answerErr
	}
	d.answered = append(d.answered, answerCall{runID: runID, answer: answer, at: at})
	d.drop(runID)
	return nil
}

func (d *fakeDecisions) Posted(_ context.Context, runID, sessionID string, at time.Time) error {
	if d.postedErr != nil {
		return d.postedErr
	}
	d.posted = append(d.posted, postedCall{runID: runID, sessionID: sessionID, at: at})
	return nil
}

func (d *fakeDecisions) StopWaiting(_ context.Context, runID string, dec runner.Decision, at time.Time) error {
	if d.stopErr != nil {
		return d.stopErr
	}
	d.stopped = append(d.stopped, stopCall{runID: runID, question: dec.Question, at: at})
	d.drop(runID)
	return nil
}

// drop removes a run from the waiting set, modelling the store moving a run
// out of the waiting state once it is answered or stopped.
func (d *fakeDecisions) drop(runID string) {
	var kept []WaitingRun
	for _, w := range d.waiting {
		if w.RunID != runID {
			kept = append(kept, w)
		}
	}
	d.waiting = kept
}

func (d *fakeDecisions) Session(_ context.Context, ticketID string) (string, error) {
	if d.sessionErr != nil {
		return "", d.sessionErr
	}
	return d.sessions[ticketID], nil
}

func TestPollNamesTheDependencyItWasNotGiven(t *testing.T) {
	unset := map[string]func(*Deps){
		"Source":     func(d *Deps) { d.Source = nil },
		"Queue":      func(d *Deps) { d.Queue = nil },
		"Estimator":  func(d *Deps) { d.Estimator = nil },
		"Visibility": func(d *Deps) { d.Visibility = nil },
		"Providers":  func(d *Deps) { d.Providers = nil },
		"Breaker":    func(d *Deps) { d.Breaker = nil },
		"Notices":    func(d *Deps) { d.Notices = nil },
		"OpenPoster": func(d *Deps) { d.OpenPoster = nil },
		"OpenPRs":    func(d *Deps) { d.OpenPRs = nil },
		"Workflow":   func(d *Deps) { d.Workflow = nil },
		"Overrides":  func(d *Deps) { d.Overrides = nil },
		"ModelPlans": func(d *Deps) { d.ModelPlans = nil },
		"Elicit":     func(d *Deps) { d.Elicit = nil },
		"Decisions":  func(d *Deps) { d.Decisions = nil },
		"Logger":     func(d *Deps) { d.Logger = nil },
		"Now":        func(d *Deps) { d.Now = nil },
	}
	for name, clear := range unset {
		t.Run(name, func(t *testing.T) {
			d := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
			clear(&d)
			_, err := Poll(context.Background(), d, buildConfig)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, want it to name %s", err, name)
			}
		})
	}
}

func TestPollAdmitsWhatItCanAndRefusesWhatItCannot(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Size: "M", Priority: 2}}}
	w := &fakeWorkflow{}
	res, err := Poll(context.Background(), pollDeps(fakeSource{issues: []Issue{
		admitted("size:M", "repo:octo/scratch"),
		admitted("repo:octo/scratch"),
		admitted("size:S", "repo:someone/else"),
	}}, q, w), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if res.Seen != 3 || !slices.Equal(res.Enqueued, []string{"FRG-18"}) {
		t.Fatalf("result = %+v", res)
	}
	if got := reasons(res.Rejections); !slices.Equal(got, []Refusal{RefusalNoSize, RefusalNotAllowlist}) {
		t.Fatalf("refused %v", got)
	}
	if len(q.queued) != 1 || q.queued[0].Repo != "octo/scratch" || !q.queued[0].At.Equal(pollAt) {
		t.Fatalf("queued = %+v", q.queued)
	}
	if !slices.Equal(res.Dispatched, []string{"FRG-18"}) || !slices.Equal(w.claims, []Claim{{RunID: "FRG-18", Repo: "octo/scratch", Priority: 2}}) {
		t.Fatalf("dispatched %q, %+v", res.Dispatched, w.claims)
	}
}

func TestPollLeavesATicketTheQueueAlreadyHolds(t *testing.T) {
	q := &fakeQueue{holds: map[string]bool{"FRG-18": true}}
	deps := pollDeps(fakeSource{issues: []Issue{
		admitted("size:M", "repo:octo/scratch"),
	}}, q, &fakeWorkflow{})
	calls := 0
	deps.Visibility = fakeVisibility{calls: &calls}
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Enqueued) != 0 || len(q.queued) != 0 || len(q.rejected) != 0 {
		t.Fatalf("result %+v, queue %+v", res, q)
	}
	// The regression this guards: Exists must short-circuit BEFORE the
	// visibility check, not merely tolerate the check's own failure — those
	// are two different fixes and this one isolates the first.
	if calls != 0 {
		t.Fatalf("visibility checked %d times, want the already-queued ticket to skip it entirely", calls)
	}
}

func TestPollFailsWhenExistsCannotBeRead(t *testing.T) {
	q := &fakeQueue{existsErr: errFirestore}
	deps := pollDeps(fakeSource{issues: []Issue{admitted("size:M", "repo:octo/scratch")}}, q, &fakeWorkflow{})
	if _, err := Poll(context.Background(), deps, buildConfig); !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the existence check's failure", err)
	}
}

// TestPollSkipsATicketWhoseVisibilityCannotBeRead is the regression test for
// the bug a mutation test caught: a transient failure checking one NEW
// ticket's repository visibility must not withhold every other delegated
// ticket's admission, or the dispatch that follows it — the ticket is simply
// not enqueued yet, and Delegated returns it again next poll.
func TestPollSkipsATicketWhoseVisibilityCannotBeRead(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-9", Repo: "octo/ready"}}}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{issues: []Issue{admitted("size:M", "repo:octo/scratch")}}, q, w)
	deps.Visibility = fakeVisibility{err: errFirestore}
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatalf("err = %v, want the poll to continue past the failed check", err)
	}
	if len(res.Enqueued) != 0 || len(q.queued) != 0 {
		t.Fatalf("queued = %+v, want the ticket left unqueued for a later poll", q.queued)
	}
	if !slices.Equal(res.Dispatched, []string{"FRG-9"}) {
		t.Fatalf("dispatched = %q, want the ready candidate dispatched despite the earlier failure", res.Dispatched)
	}
}

func TestPollWithNothingQueuedDispatchesNothing(t *testing.T) {
	w := &fakeWorkflow{}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, &fakeQueue{}, w), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Dispatched) != 0 || len(w.claims) != 0 {
		t.Fatalf("dispatched %q, %+v", res.Dispatched, w.claims)
	}
}

func TestPollReturnsAClaimedRunWhoseDispatchFailed(t *testing.T) {
	dispatchErr := errors.New("github is unreachable")
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1}}}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q,
		&fakeWorkflow{err: dispatchErr}), buildConfig)
	if !errors.Is(err, dispatchErr) {
		t.Fatalf("err = %v, want the dispatch error", err)
	}
	if len(res.Dispatched) != 0 {
		t.Fatalf("dispatched %q", res.Dispatched)
	}
	if !slices.Equal(q.released, []string{"FRG-18"}) {
		t.Fatalf("released %v", q.released)
	}
}

func TestPollReportsAFailedReleaseBesideTheDispatchThatCausedIt(t *testing.T) {
	dispatchErr, releaseErr := errors.New("github is unreachable"), errors.New("firestore is unreachable")
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18"}}, releaseErr: releaseErr}
	_, err := Poll(context.Background(), pollDeps(fakeSource{}, q,
		&fakeWorkflow{err: dispatchErr}), buildConfig)
	if !errors.Is(err, dispatchErr) || !errors.Is(err, releaseErr) {
		t.Fatalf("err = %v, want both the dispatch and the release failure", err)
	}
}

func TestPollDefersACandidateTheBudgetWouldBreachAndClaimsTheNext(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{
			{RunID: "FRG-18", Repo: "octo/scratch", Size: "L", Priority: 1},
			{RunID: "FRG-19", Repo: "octo/scratch", Size: "S", Priority: 2},
		},
		bindings: map[string]string{"FRG-18": CeilingCash},
	}
	w := &fakeWorkflow{}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, w), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.claimed, []string{"FRG-19"}) || !slices.Equal(res.Dispatched, []string{"FRG-19"}) {
		t.Fatalf("claimed %v, dispatched %q", q.claimed, res.Dispatched)
	}
	if len(res.Deferrals) != 1 || res.Deferrals[0].RunID != "FRG-18" || res.Deferrals[0].Ceiling != CeilingCash {
		t.Fatalf("deferrals = %+v", res.Deferrals)
	}
}

func TestPollDispatchesNothingWhenEveryCandidateBreachesItsCeiling(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "FRG-18", Priority: 1}, {RunID: "FRG-19", Priority: 2}},
		bindings:   map[string]string{"FRG-18": CeilingCash, "FRG-19": "5h"},
	}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Dispatched) != 0 {
		t.Fatalf("dispatched %q, want nothing", res.Dispatched)
	}
	if got := deferredCeilings(res.Deferrals); !slices.Equal(got, []string{CeilingCash, "5h"}) {
		t.Fatalf("deferrals named %v", got)
	}
}

func TestPollSkipsACandidateAnotherPollAlreadyClaimedWithoutDeferringIt(t *testing.T) {
	q := &fakeQueue{
		candidates:  []Candidate{{RunID: "FRG-18", Priority: 1}, {RunID: "FRG-19", Priority: 2}},
		unavailable: map[string]bool{"FRG-18": true},
	}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deferrals) != 0 {
		t.Fatalf("deferrals = %+v, want none for a row another poll already took", res.Deferrals)
	}
	if !slices.Equal(res.Dispatched, []string{"FRG-19"}) {
		t.Fatalf("dispatched %q, want FRG-19", res.Dispatched)
	}
}

func TestPollBuildsTheReservationFromTheEstimateAndVisibility(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "FRG-18", Repo: "octo/private", Size: "L", Private: true},
		{RunID: "FRG-19", Repo: "octo/public", Size: "S", Private: false},
	}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{
		"L": {ProviderCost: 5 * money.Dollar, Minutes: 40},
		"S": {ProviderCost: money.Dollar, Minutes: 10},
	}}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 2 {
		t.Fatalf("TryClaim called %d times, want 2 (the walk continues past a claim)", len(q.tryClaims))
	}
	got := q.tryClaims[0]
	if got.runID != "FRG-18" || got.res.ProviderCost != 5*money.Dollar || got.res.RunnerMinutes != 40 {
		t.Fatalf("reservation = %+v, want the private target's estimated minutes counted", got.res)
	}
}

func TestPollZeroesRunnerMinutesForAPublicTarget(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/public", Size: "S", Private: false}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"S": {ProviderCost: money.Dollar, Minutes: 40}}}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if got := q.tryClaims[0].res; got.RunnerMinutes != 0 {
		t.Fatalf("reservation = %+v, want a public target's runner minutes zeroed", got)
	}
}

func TestPollFailsOnTheStore(t *testing.T) {
	for name, tt := range map[string]struct {
		queue  *fakeQueue
		source fakeSource
		issues []Issue
	}{
		"the source cannot be read":     {queue: &fakeQueue{}, source: fakeSource{err: errFirestore}},
		"the candidates cannot be read": {queue: &fakeQueue{candidatesErr: errFirestore}},
		"a candidate cannot be claimed": {queue: &fakeQueue{candidates: []Candidate{{RunID: "FRG-18"}}, tryClaimErr: errFirestore}},
		"a run cannot be queued": {queue: &fakeQueue{enqueueErr: errFirestore},
			issues: []Issue{admitted("size:M", "repo:octo/scratch")}},
		"a refusal cannot be recorded": {queue: &fakeQueue{rejectErr: errFirestore},
			issues: []Issue{admitted("repo:octo/scratch")}},
	} {
		t.Run(name, func(t *testing.T) {
			source := tt.source
			source.issues = tt.issues
			if _, err := Poll(context.Background(), pollDeps(source, tt.queue, &fakeWorkflow{}),
				buildConfig); !errors.Is(err, errFirestore) {
				t.Fatalf("err = %v, want the store failure", err)
			}
		})
	}
}

// TestPollSkipsACandidateWhoseEstimateFails is the regression test for the
// same class of bug as TestPollSkipsATicketWhoseVisibilityCannotBeRead: a
// failed estimate must withhold only the one candidate it failed for, never
// abort the whole claim walk — a lower-priority candidate whose own estimate
// succeeds must still be considered and dispatched this poll.
func TestPollSkipsACandidateWhoseEstimateFails(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "FRG-18", Size: "L", Priority: 1},
		{RunID: "FRG-9", Size: "S", Priority: 2},
	}}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{}, q, w)
	deps.Estimator = fakeEstimator{errBySize: map[string]error{"L": errFirestore}}
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatalf("err = %v, want the poll to continue past the failed estimate", err)
	}
	if !slices.Equal(res.Dispatched, []string{"FRG-9"}) {
		t.Fatalf("dispatched = %q, want the candidate whose estimate succeeded", res.Dispatched)
	}
	if len(q.tryClaims) != 1 || q.tryClaims[0].runID != "FRG-9" {
		t.Fatalf("tryClaims = %+v, want only the un-failed candidate claimed", q.tryClaims)
	}
	if len(res.Deferrals) != 1 || res.Deferrals[0].RunID != "FRG-18" || res.Deferrals[0].Ceiling != ConditionEstimateFailed {
		t.Fatalf("deferrals = %+v, want the failed estimate recorded", res.Deferrals)
	}
}

func TestAdmitCarriesTheRepositorysVisibilityOntoTheQueuedRun(t *testing.T) {
	q := &fakeQueue{}
	deps := pollDeps(fakeSource{issues: []Issue{admitted("size:M", "repo:octo/scratch")}}, q, &fakeWorkflow{})
	deps.Visibility = fakeVisibility{private: map[string]bool{"octo/scratch": true}}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.queued) != 1 || !q.queued[0].Private {
		t.Fatalf("queued = %+v, want Private carried from the visibility check", q.queued)
	}
}

func TestPollTellsAdmissionWhetherTheConfiguredProviderIsHalted(t *testing.T) {
	for name, tt := range map[string]struct {
		halted map[string]bool
		err    error
		want   bool
	}{
		"the configured provider is halted":      {halted: map[string]bool{"command-code": true}, want: true},
		"only another provider is halted":        {halted: map[string]bool{"openrouter": true}},
		"the halt check fails, so it fails open": {err: errFirestore},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Priority: 1}}}
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.Providers = fakeProviders{halted: tt.halted, err: tt.err}
			cfg := Config{Repos: buildConfig.Repos, Budget: buildConfig.Budget, Model: "command-code/x"}
			if _, err := Poll(context.Background(), deps, cfg); err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 || q.tryClaims[0].facts.ProviderHalted != tt.want {
				t.Fatalf("tryClaims = %+v, want ProviderHalted %v", q.tryClaims, tt.want)
			}
		})
	}
}

func TestPollAdmitsEveryCandidateTheQueueAllows(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "run-a", Repo: "octo/a", Size: "S", Priority: 1},
		{RunID: "run-b", Repo: "octo/b", Size: "S", Priority: 2},
		{RunID: "run-c", Repo: "octo/c", Size: "S", Priority: 3},
	}}
	w := &fakeWorkflow{}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, w), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"run-a", "run-b", "run-c"}; !slices.Equal(res.Dispatched, want) || len(w.claims) != len(want) {
		t.Fatalf("dispatched %v, workflow started %+v", res.Dispatched, w.claims)
	}
}

func TestPollNamesTheConditionThatWithheldEachWaitingCandidate(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{
			{RunID: "run-a", Repo: "octo/a", Priority: 1},
			{RunID: "run-b", Repo: "octo/a", Priority: 2},
			{RunID: "run-c", Repo: "octo/b", Priority: 3},
		},
		bindings: map[string]string{"run-b": ConditionRepoBusy, "run-c": ConditionConcurrency},
	}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) {
		t.Fatalf("dispatched %v", res.Dispatched)
	}
	if got := deferredCeilings(res.Deferrals); !slices.Equal(got, []string{ConditionRepoBusy, ConditionConcurrency}) {
		t.Fatalf("deferrals named %v", got)
	}
}

func TestPollLeavesAContendedRunQueuedWithNoOutcome(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{
			{RunID: "run-a", Repo: "octo/a", Priority: 1},
			{RunID: "run-b", Repo: "octo/a", Priority: 2},
		},
		bindings: map[string]string{"run-b": ConditionRepoBusy},
	}
	var logs strings.Builder
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deferrals) != 1 {
		t.Fatalf("deferrals %+v", res.Deferrals)
	}
	if len(res.Rejections) != 0 || len(q.rejected) != 0 {
		t.Fatalf("contention refused a ticket: %+v", q.rejected)
	}
	if len(q.released) != 0 {
		t.Fatalf("contention released %v", q.released)
	}
	if strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("contention logged above info:\n%s", logs.String())
	}
}

func TestPollCountsOpenPRsAcrossTheAllowlistOnce(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "run-a", Repo: "octo/a", Priority: 1},
		{RunID: "run-b", Repo: "octo/a", Priority: 2},
		{RunID: "run-c", Repo: "octo/b", Priority: 3},
	}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	calls := map[string]int{}
	deps.OpenPRs = fakeOpenPRs{open: map[string]int{"octo/a": 2, "octo/b": 1, "octo/idle": 4}, calls: calls}
	cfg := Config{Repos: []string{"octo/a", "octo/b", "octo/idle"}}
	if _, err := Poll(context.Background(), deps, cfg); err != nil {
		t.Fatal(err)
	}
	if calls["octo/a"] != 1 || calls["octo/b"] != 1 || calls["octo/idle"] != 1 {
		t.Fatalf("reads = %v, want one per allowlisted repository", calls)
	}
	for i, claim := range q.tryClaims {
		if got := claim.facts; got.OpenPRs != 7 || !got.OpenPRsKnown {
			t.Fatalf("claim %d facts = %+v, want every allowlisted repository's PRs counted", i, got)
		}
	}
}

func TestPollMarksTheOpenPRCountUnknownWhenOneRepositoryFailsToRead(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.OpenPRs = fakeOpenPRs{open: map[string]int{"octo/a": 1, "octo/b": 1}, err: errFirestore, failOnly: "octo/b"}
	if _, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a", "octo/b"}}); err != nil {
		t.Fatal(err)
	}
	if got := q.tryClaims[0].facts; got.OpenPRsKnown || got.OpenPRs != 0 {
		t.Fatalf("facts = %+v, want a partial count discarded as unknown", got)
	}
}

func TestPollReadsNoOpenPRCountWhenNothingIsQueued(t *testing.T) {
	deps := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	calls := map[string]int{}
	deps.OpenPRs = fakeOpenPRs{calls: calls}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("reads = %v, want none when there is nothing to admit", calls)
	}
}

func TestPollAdmitsWithAnUnknownOpenPRCountWhenTheReadFails(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.OpenPRs = fakeOpenPRs{err: errFirestore}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatalf("err = %v, want the poll to continue past the failed count", err)
	}
	if got := q.tryClaims[0].facts; got.OpenPRsKnown || got.OpenPRs != 0 {
		t.Fatalf("facts = %+v, want the count marked unknown", got)
	}
}

func TestPollPassesTheLimitsAdmissionHoldsToTheQueue(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	custom := Limits{PlatformCap: 4, LargeCap: 2, ReviewWIP: 6}
	partial := Limits{ReviewWIP: 6}
	for name, tt := range map[string]struct {
		cfg  Limits
		want Limits
	}{
		"the zero value means the defaults": {want: DefaultLimits},
		"a configured value is kept":        {cfg: custom, want: custom},
		"a partial value defaults only the fields it leaves out": {cfg: partial,
			want: Limits{PlatformCap: DefaultLimits.PlatformCap, LargeCap: DefaultLimits.LargeCap, ReviewWIP: 6}},
	} {
		t.Run(name, func(t *testing.T) {
			q.tryClaims = nil
			cfg := buildConfig
			cfg.Limits = tt.cfg
			if _, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), cfg); err != nil {
				t.Fatal(err)
			}
			if got := q.tryClaims[0].facts.Limits; got != tt.want {
				t.Fatalf("limits = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPollPassesTheTuningTheQueueStores(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	custom := Tuning{StableRuns: 3, RiseWithin: 1.1, HalveBeyond: 1.8}
	for name, tt := range map[string]struct {
		cfg  Tuning
		want Tuning
	}{
		"the zero value means the defaults": {want: DefaultTuning},
		"a configured value is kept":        {cfg: custom, want: custom},
		"a partial value defaults only the fields it leaves out": {cfg: Tuning{StableRuns: 3},
			want: Tuning{StableRuns: 3, RiseWithin: DefaultTuning.RiseWithin, HalveBeyond: DefaultTuning.HalveBeyond}},
	} {
		t.Run(name, func(t *testing.T) {
			q.tryClaims = nil
			cfg := buildConfig
			cfg.Tuning = tt.cfg
			if _, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), cfg); err != nil {
				t.Fatal(err)
			}
			if got := q.tryClaims[0].facts.Tuning; got != tt.want {
				t.Fatalf("tuning = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPollStartsTheOtherClaimsWhenOneDispatchFails(t *testing.T) {
	dispatchErr := errors.New("github is unreachable")
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "run-a", Repo: "octo/a", Priority: 1},
		{RunID: "run-b", Repo: "octo/b", Priority: 2},
	}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Workflow = &failingWorkflow{failing: "run-a", err: dispatchErr}
	res, err := Poll(context.Background(), deps, buildConfig)
	if !errors.Is(err, dispatchErr) {
		t.Fatalf("err = %v, want the dispatch failure", err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-b"}) || !slices.Equal(q.released, []string{"run-a"}) {
		t.Fatalf("dispatched %v, released %v", res.Dispatched, q.released)
	}
}

// failingWorkflow refuses one run and starts every other.
type failingWorkflow struct {
	failing string
	err     error
}

func (w *failingWorkflow) Dispatch(_ context.Context, c Claim) error {
	if c.RunID == w.failing {
		return w.err
	}
	return nil
}

func TestPollStartsTheClaimsAlreadyBookedWhenALaterClaimFails(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{
			{RunID: "run-a", Repo: "octo/a", Priority: 1},
			{RunID: "run-b", Repo: "octo/b", Priority: 2},
			{RunID: "run-c", Repo: "octo/c", Priority: 3},
		},
		tryClaimErrFor: map[string]error{"run-c": errFirestore},
	}
	w := &fakeWorkflow{}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, w), buildConfig)
	if !errors.Is(err, errFirestore) {
		t.Fatalf("err = %v, want the claim failure", err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a", "run-b"}) {
		t.Fatalf("dispatched %v, want the two claims already booked started", res.Dispatched)
	}
}

func TestPollAdmitsWithTheRelationsLinearReportsNow(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	issue := admitted("size:M", "repo:octo/scratch")
	issue.ID, issue.BlockedBy, issue.Blocks = "run-a", []string{"run-p"}, []string{"run-x"}
	if _, err := Poll(context.Background(), pollDeps(fakeSource{issues: []Issue{issue}}, q, &fakeWorkflow{}), buildConfig); err != nil {
		t.Fatal(err)
	}
	want := Relations{Known: true, BlockedBy: []string{"run-p"}, Blocks: []string{"run-x"}}
	if got := q.tryClaims[0].facts.Relations; !got.Known || !slices.Equal(got.BlockedBy, want.BlockedBy) || !slices.Equal(got.Blocks, want.Blocks) {
		t.Fatalf("relations = %+v, want %+v", got, want)
	}
}

func TestPollLeavesRelationsUnknownForATicketItDidNotRead(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	if _, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), buildConfig); err != nil {
		t.Fatal(err)
	}
	if q.tryClaims[0].facts.Relations.Known {
		t.Fatal("relations marked known for a ticket this poll did not read")
	}
}

func reasons(rejections []Rejection) []Refusal {
	var out []Refusal
	for _, r := range rejections {
		out = append(out, r.Reason)
	}
	return out
}

func deferredCeilings(deferrals []Deferral) []string {
	var out []string
	for _, d := range deferrals {
		out = append(out, d.Ceiling)
	}
	return out
}

func TestPollLogsWhenEveryCandidateIsDeferred(t *testing.T) {
	two := []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}, {RunID: "run-b", Repo: "octo/b", Priority: 2}}
	for name, tt := range map[string]struct {
		candidates  []Candidate
		bindings    map[string]string
		unavailable map[string]bool
		want        bool
	}{
		"nothing is queued": {},
		"every candidate is withheld": {candidates: two,
			bindings: map[string]string{"run-a": ConditionRepoBusy, "run-b": ConditionConcurrency}, want: true},
		"one candidate is claimed": {candidates: two, bindings: map[string]string{"run-b": ConditionConcurrency}},
		"one candidate was taken by another poll": {candidates: two,
			bindings: map[string]string{"run-b": ConditionConcurrency}, unavailable: map[string]bool{"run-a": true}},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: tt.candidates, bindings: tt.bindings, unavailable: tt.unavailable}
			var logs strings.Builder
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logs.String(), "everyCandidateDeferred"); got != tt.want {
				t.Fatalf("logged = %v, want %v:\n%s", got, tt.want, logs.String())
			}
		})
	}
}

func TestPollLogsHowLongEachPhaseTook(t *testing.T) {
	var logs strings.Builder
	deps := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	clock := pollAt
	deps.Now = func() time.Time { clock = clock.Add(time.Second); return clock }
	if _, err := Poll(context.Background(), deps, Config{}); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"delegated", "admit", "claim", "dispatch", "notices"} {
		if !strings.Contains(logs.String(), "msg=pollPhase phase="+phase+" ms=") {
			t.Errorf("no pollPhase line for %s in:\n%s", phase, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "msg=pollPhase phase=delegated ms=1000") {
		t.Errorf("delegated phase not timed from its own start:\n%s", logs.String())
	}
}

// paidPlan is a dispatch config whose plan has a window, a model cap and two free models.
var paidPlan = Config{
	Repos: buildConfig.Repos,
	Model: "p/paid",
	Budget: BudgetConfig{
		ProviderWindows: []Window{{Name: "p-5h", Period: 5 * time.Hour, Limit: 14 * money.Dollar}},
		ModelCaps:       map[string]Window{"p/paid": {Name: "p/paid", Limit: 60 * money.Dollar}},
		Cash:            Window{Name: CeilingCash, Calendar: true, Limit: 30 * money.Dollar},
		Runner:          RunnerMinutes{FreeMinutes: 2000},
	},
	LastResort: []string{"p/free-a", "p/free-b"},
}

func TestPollRunsACandidateOnTheFreeTierOnceAPaidCeilingBinds(t *testing.T) {
	for name, ceiling := range map[string]string{"a plan window": "p-5h", "a model cap": "p/paid"} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{
				candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}},
				bindings:   map[string]string{"run-a": ceiling},
			}
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}}}
			res, err := Poll(context.Background(), deps, paidPlan)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.Dispatched, []string{"run-a"}) || len(res.Deferrals) != 0 {
				t.Fatalf("dispatched %q, deferred %+v, want the run dispatched on the free tier", res.Dispatched, res.Deferrals)
			}
			free := q.tryClaims[len(q.tryClaims)-1]
			if len(q.tryClaims) != 2 || !free.cfg.ProviderFree || len(free.cfg.ProviderWindows) != 0 || len(free.cfg.ModelCaps) != 0 ||
				free.cfg.Cash != paidPlan.Budget.Cash || free.cfg.Runner != paidPlan.Budget.Runner {
				t.Fatalf("claims %+v, want a second claim against cash and runner minutes alone", q.tryClaims)
			}
			if free.res.ProviderCost != 0 {
				t.Fatalf("reservation = %+v, want no provider cost on the free tier", free.res)
			}
			want := runner.ModelLabels{Build: "p/free-a", Review: "p/free-b", Plan: []string{"p/free-a", "p/free-b"}}
			if free.facts.Model != "p/free-a" || free.facts.LastResort == nil || !reflect.DeepEqual(*free.facts.LastResort, want) {
				t.Fatalf("facts = %+v, want the first free model building and the next reviewing", free.facts)
			}
		})
	}
}

func TestPollLeavesACandidateACeilingOtherThanThePlansOwnWithholds(t *testing.T) {
	for name, ceiling := range map[string]string{"cash": CeilingCash, "the breaker": ConditionCircuitBreaker, "the provider halt": CeilingProviderHalted} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a"}}, bindings: map[string]string{"run-a": ceiling}}
			res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), paidPlan)
			if err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != ceiling {
				t.Fatalf("claims %d, deferrals %+v, want one claim deferred on %s", len(q.tryClaims), res.Deferrals, ceiling)
			}
		})
	}
}

func TestPollKeepsAPaidCeilingWhenTheFreeTierCannotStaffAReview(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a"}}, bindings: map[string]string{"run-a": "p-5h"}}
	plan := paidPlan
	plan.LastResort = []string{"p/free-a"}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "p-5h" {
		t.Fatalf("claims %d, deferrals %+v, want one claim deferred on the plan window", len(q.tryClaims), res.Deferrals)
	}
}

func TestPollDefersAFreeTierClaimOnTheCeilingThatStillBindsIt(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a"}},
		bindings:     map[string]string{"run-a": "p-5h"},
		freeBindings: map[string]string{"run-a": CeilingCash},
	}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), paidPlan)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Dispatched) != 0 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != CeilingCash {
		t.Fatalf("dispatched %q, deferrals %+v, want the run deferred on cash", res.Dispatched, res.Deferrals)
	}
}

func TestPollRunsAPrivateCandidateOnTheFreeTierOnlyWithTheOwnersOptIn(t *testing.T) {
	for name, tt := range map[string]struct {
		optedIn      bool
		readErr      error
		dispatched   int
		deferredOn   string
		freeAttempts int
	}{
		"without the opt-in":   {optedIn: false, deferredOn: "p-5h"},
		"with the opt-in":      {optedIn: true, dispatched: 1, freeAttempts: 1},
		"a failed opt-in read": {optedIn: true, readErr: errors.New("unavailable"), deferredOn: "p-5h"},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Private: true}}, bindings: map[string]string{"run-a": "p-5h"}}
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.ModelPlans = &fakeModelPlans{plans: map[string]providers.Plan{"p": {PrivateOptIn: tt.optedIn}}, err: tt.readErr}
			res, err := Poll(context.Background(), deps, paidPlan)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Dispatched) != tt.dispatched || len(q.tryClaims) != 1+tt.freeAttempts {
				t.Fatalf("dispatched %q after %d claims", res.Dispatched, len(q.tryClaims))
			}
			if tt.deferredOn != "" && (len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != tt.deferredOn) {
				t.Fatalf("deferrals %+v, want the paid ceiling named", res.Deferrals)
			}
		})
	}
}

func TestClaimStageDispatchesTheStageTheRunIsReadyFor(t *testing.T) {
	for name, tt := range map[string]struct {
		cand     Candidate
		twoStage bool
		want     string
	}{
		"a planned run claims its build": {Candidate{Size: "M", Stage: StageBuild}, true, StageBuild},
		"an unplanned M claims its plan": {Candidate{Size: "M"}, true, StagePlan},
		"an unplanned L claims its plan": {Candidate{Size: "L"}, true, StagePlan},
		"an S run is never plan-staged":  {Candidate{Size: "S"}, true, ""},
		"the switch off stages nothing":  {Candidate{Size: "M"}, false, ""},
		"the switch off ignores a plan":  {Candidate{Size: "M", Stage: StageBuild}, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := claimStage(tt.cand, tt.twoStage); got != tt.want {
				t.Fatalf("claimStage = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPollClaimsAPlanStageOnThePlanEstimateAndDispatchesItWithTheStage(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M", Private: true}}}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{}, q, w)
	deps.Estimator = fakeEstimator{
		bySize:     map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}},
		planBySize: map[string]Estimate{"M": {ProviderCost: money.Dollar, Minutes: 5}},
	}
	cfg := buildConfig
	cfg.TwoStage = true
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || len(res.Dispatched) != 1 {
		t.Fatalf("claims %d, dispatched %q, want one plan claim dispatched", len(q.tryClaims), res.Dispatched)
	}
	if got := q.tryClaims[0].res; got.Stage != StagePlan || got.ProviderCost != money.Dollar || got.RunnerMinutes != 5 {
		t.Fatalf("reservation = %+v, want the plan stage's own estimate", got)
	}
	if len(w.claims) != 1 || w.claims[0].Stage != StagePlan {
		t.Fatalf("dispatches = %+v, want the plan stage dispatched with stage=plan", w.claims)
	}
}

func TestPollClaimsAPlannedRunForItsBuildWithItsWriteSet(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{
		RunID: "run-a", Repo: "octo/scratch", Size: "M", Private: true,
		Stage: StageBuild, PlanFiles: []string{"a.go", "b.go"},
	}}}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{}, q, w)
	deps.Estimator = fakeEstimator{bySize: map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}}}
	cfg := buildConfig
	cfg.TwoStage = true
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Dispatched) != 1 {
		t.Fatalf("dispatched %q, want one", res.Dispatched)
	}
	got := q.tryClaims[0].res
	if got.Stage != StageBuild || got.ProviderCost != 2*money.Dollar || !slices.Equal(got.Files, []string{"a.go", "b.go"}) {
		t.Fatalf("reservation = %+v, want the build estimate on the planned write set", got)
	}
	if w.claims[0].Stage != StageBuild {
		t.Fatalf("claim stage = %q, want %s: the build it claims for", w.claims[0].Stage, StageBuild)
	}
}

func TestPollDefersAPlannedRunRatherThanMixTiersWithinIt(t *testing.T) {
	q := &fakeQueue{
		candidates:   []Candidate{{RunID: "run-a", Stage: StageBuild, PlanFiles: []string{"a.go"}}},
		bindings:     map[string]string{"run-a": "p-5h"},
		freeBindings: map[string]string{"run-a": CeilingCash},
	}
	plan := paidPlan
	plan.TwoStage = true
	plan.LastResort = []string{"p/free-a"}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 {
		t.Fatalf("TryClaim called %d times, want one: a build-stage claim never falls back to the free tier", len(q.tryClaims))
	}
	if len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "p-5h" {
		t.Fatalf("deferrals = %+v, want the run deferred on its plan window", res.Deferrals)
	}
}

func TestPollDefersAPlanStageWhosePlanEstimateCannotBeRead(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Size: "M"}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{
		bySize:  map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}},
		planErr: errors.New("unavailable"),
	}
	cfg := buildConfig
	cfg.TwoStage = true
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 0 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != ConditionEstimateFailed {
		t.Fatalf("claims %d, deferrals %+v, want the plan claim deferred on the estimate", len(q.tryClaims), res.Deferrals)
	}
}
