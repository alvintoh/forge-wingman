package dispatcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
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
// asked to claim, and the reservation it was asked to claim it with.
type tryClaimCall struct {
	runID string
	cfg   BudgetConfig
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
	bindings    map[string]string
	unavailable map[string]bool
	tryClaimErr error

	claimed   []string
	tryClaims []tryClaimCall

	existsErr error
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

func (q *fakeQueue) TryClaim(_ context.Context, runID string, _ time.Time, cfg BudgetConfig, res Reservation) (bool, string, error) {
	if q.tryClaimErr != nil {
		return false, "", q.tryClaimErr
	}
	q.tryClaims = append(q.tryClaims, tryClaimCall{runID: runID, cfg: cfg, res: res})
	if binding, ok := q.bindings[runID]; ok {
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
// a zero estimate for a size it holds none for. errBySize fails one size
// specifically, so a test can prove a failed estimate withholds only that
// candidate; err fails every size.
type fakeEstimator struct {
	bySize    map[string]Estimate
	err       error
	errBySize map[string]error
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

func pollDeps(source Source, q *fakeQueue, w *fakeWorkflow) Deps {
	return Deps{
		Source: source, Queue: q, Workflow: w,
		Estimator:  fakeEstimator{},
		Visibility: fakeVisibility{},
		Providers:  fakeProviders{},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        func() time.Time { return pollAt },
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
	if res.Dispatched != "FRG-18" || !slices.Equal(w.claims, []Claim{{RunID: "FRG-18", Repo: "octo/scratch", Priority: 2}}) {
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
	if res.Dispatched != "FRG-9" {
		t.Fatalf("dispatched = %q, want the ready candidate dispatched despite the earlier failure", res.Dispatched)
	}
}

func TestPollWithNothingQueuedDispatchesNothing(t *testing.T) {
	w := &fakeWorkflow{}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, &fakeQueue{}, w), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dispatched != "" || len(w.claims) != 0 {
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
	if res.Dispatched != "" {
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
	if !slices.Equal(q.claimed, []string{"FRG-19"}) || res.Dispatched != "FRG-19" {
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
	if res.Dispatched != "" {
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
	if res.Dispatched != "FRG-19" {
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
	if len(q.tryClaims) != 1 {
		t.Fatalf("TryClaim called %d times, want 1 (it stopped at the first claim)", len(q.tryClaims))
	}
	got := q.tryClaims[0]
	if got.runID != "FRG-18" || got.res != (Reservation{ProviderCost: 5 * money.Dollar, RunnerMinutes: 40}) {
		t.Fatalf("reservation = %+v, want the private target's estimated minutes counted", got)
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
	if res.Dispatched != "FRG-9" {
		t.Fatalf("dispatched = %q, want the candidate whose estimate succeeded", res.Dispatched)
	}
	if len(q.tryClaims) != 1 || q.tryClaims[0].runID != "FRG-9" {
		t.Fatalf("tryClaims = %+v, want only the un-failed candidate claimed", q.tryClaims)
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

func TestPollDefersEveryCandidateWhileItsProviderIsHalted(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1},
		{RunID: "FRG-19", Repo: "octo/scratch", Priority: 2},
	}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Providers = fakeProviders{halted: map[string]bool{"opencode": true}}
	cfg := Config{Repos: buildConfig.Repos, Budget: buildConfig.Budget, Model: "opencode/big-pickle"}
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dispatched != "" || len(q.claimed) != 0 {
		t.Fatalf("dispatched %q, claimed %v, want nothing while the provider is halted (AC4)", res.Dispatched, q.claimed)
	}
	if got := deferredCeilings(res.Deferrals); !slices.Equal(got, []string{CeilingProviderHalted, CeilingProviderHalted}) {
		t.Fatalf("deferrals = %v, want every candidate deferred as provider-halted, reconsidered next poll", got)
	}
}

func TestPollClaimsNormallyWhenTheConfiguredProviderIsNotHalted(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	// A different provider is halted; the configured one is not, so nothing withholds this candidate.
	deps.Providers = fakeProviders{halted: map[string]bool{"openrouter": true}}
	cfg := Config{Repos: buildConfig.Repos, Budget: buildConfig.Budget, Model: "opencode/big-pickle"}
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dispatched != "FRG-18" || len(res.Deferrals) != 0 {
		t.Fatalf("dispatched %q, deferrals %v, want the candidate claimed (AC5)", res.Dispatched, res.Deferrals)
	}
}

func TestPollClaimsNormallyWhenTheProviderHaltCheckErrors(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	// A halt check that itself fails must not withhold every candidate — it fails open,
	// same as the estimate/visibility checks it sits alongside.
	deps.Providers = fakeProviders{err: errFirestore}
	cfg := Config{Repos: buildConfig.Repos, Budget: buildConfig.Budget, Model: "opencode/big-pickle"}
	res, err := Poll(context.Background(), deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dispatched != "FRG-18" || len(res.Deferrals) != 0 {
		t.Fatalf("dispatched %q, deferrals %v, want the candidate claimed despite the halt-check error (fail open)", res.Dispatched, res.Deferrals)
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
