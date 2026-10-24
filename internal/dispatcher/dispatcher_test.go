package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// fakeTicketStates is Linear as a claim uses it: prior is the state each ticket
// held before the claim moved it, empty for one already where the claim would
// put it, and the moves each way are recorded.
type fakeTicketStates struct {
	prior map[string]string
	// started and restored name the tickets moved each way, in order.
	started  []string
	restored []restoreCall
	// startErr and restoreErr fail the write they belong to.
	startErr   error
	restoreErr error
}

func (s *fakeTicketStates) StartTicket(_ context.Context, ticketID string) (string, error) {
	if s.startErr != nil {
		return "", s.startErr
	}
	s.started = append(s.started, ticketID)
	return s.prior[ticketID], nil
}

func (s *fakeTicketStates) RestoreTicket(_ context.Context, ticketID, stateID string) error {
	if s.restoreErr != nil {
		return s.restoreErr
	}
	s.restored = append(s.restored, restoreCall{ticket: ticketID, state: stateID})
	return nil
}

// restoreCall is one ticket a poll put back, and the state it put it in.
type restoreCall struct {
	ticket string
	state  string
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
	// freeModelBindings is what TryClaim reports for a free-tier run of the
	// named build model, so a case can fail one plan's free rung and admit the
	// next.
	freeModelBindings map[string]string
	// planBindings is what TryClaim reports for a paid claim whose build model is
	// on the named plan, in place of bindings.
	planBindings map[string]string
	unavailable  map[string]bool
	tryClaimErr  error
	// tryClaimErrFor fails TryClaim for one run id only.
	tryClaimErrFor map[string]error

	// history is the snapshot a poll's loader reads, and histories counts the
	// reads made of it: one poll reads the settled history once however many
	// candidates it walks (AC1).
	history    SettledHistory
	historyErr error
	histories  int

	claimed   []string
	tryClaims []tryClaimCall

	existsErr error

	// transientStops is what the transient listing reports and transientErr its
	// failure; requeued records each automatic requeue, and requeueTransientErr
	// fails one.
	transientStops      []TransientStop
	transientErr        error
	requeued            []TransientStop
	requeueTransientErr error

	// ticketStates is the Linear state each claimed run's ticket held before its
	// claim, as RecordTicketState wrote it; ticketStateErr fails that write.
	ticketStates   map[string]string
	ticketStateErr error

	// reprioritized records each rank the poll refreshed, by run id.
	reprioritized map[string]int
	priorityErr   error

	// reserved and settled are the spend Spend reports at every moment; spends
	// records each moment it was asked about.
	reserved Totals
	settled  Settled
	spendErr error
	spends   []time.Time

	// abandoned is what the next SweepAbandoned reports, once: a flipped run is
	// not flipped again. sweeps records the moment and windows each call used.
	abandoned []Abandoned
	sweepErr  error
	sweeps    []sweepCall

	// stuck is what StuckRuns reports, and stuckWindow the bounds it was asked.
	stuck       []Stuck
	stuckErr    error
	stuckWindow [2]time.Time
}

func (q *fakeQueue) StuckRuns(_ context.Context, before, since time.Time) ([]Stuck, error) {
	q.stuckWindow = [2]time.Time{before, since}
	return q.stuck, q.stuckErr
}

type sweepCall struct {
	at      time.Time
	windows AbandonWindows
}

func (q *fakeQueue) SweepAbandoned(_ context.Context, now time.Time, w AbandonWindows) ([]Abandoned, error) {
	q.sweeps = append(q.sweeps, sweepCall{at: now, windows: w})
	out := q.abandoned
	q.abandoned = nil
	return out, q.sweepErr
}

func (q *fakeQueue) Spend(_ context.Context, _ BudgetConfig, _ string, at time.Time, _ HistoryLoader) (Totals, Settled, error) {
	q.spends = append(q.spends, at)
	return q.reserved, q.settled, q.spendErr
}

// SettledHistory is the snapshot a poll's loader reads, counted so a test can
// assert a poll read it once whatever it walked (AC1).
func (q *fakeQueue) SettledHistory(context.Context, time.Time) (SettledHistory, error) {
	q.histories++
	return q.history, q.historyErr
}

func (q *fakeQueue) Exists(_ context.Context, runID string) (bool, error) {
	if q.existsErr != nil {
		return false, q.existsErr
	}
	return q.holds[runID], nil
}

func (q *fakeQueue) TransientStops(context.Context) ([]TransientStop, error) {
	return q.transientStops, q.transientErr
}

// RequeueTransient records the requeue and reports the attempt the run is on,
// with the state its ticket held before the claim, which the store's own
// requeue clears from the row and reports with it.
func (q *fakeQueue) RequeueTransient(_ context.Context, runID string, _ time.Time, class runner.TransientClass, _ int) (TransientRequeue, error) {
	if q.requeueTransientErr != nil {
		return TransientRequeue{}, q.requeueTransientErr
	}
	q.requeued = append(q.requeued, TransientStop{RunID: runID, Class: class})
	requeues := 0
	for _, r := range q.requeued {
		if r.RunID == runID {
			requeues++
		}
	}
	state := q.ticketStates[runID]
	delete(q.ticketStates, runID)
	return TransientRequeue{Attempt: requeues, TicketState: state}, nil
}

// RecordTicketState records the state the run's ticket held before its claim,
// which a requeue or an abandon reports back.
func (q *fakeQueue) RecordTicketState(_ context.Context, runID, stateID string) error {
	if q.ticketStateErr != nil {
		return q.ticketStateErr
	}
	if q.ticketStates == nil {
		q.ticketStates = map[string]string{}
	}
	q.ticketStates[runID] = stateID
	return nil
}

func (q *fakeQueue) UpdateQueuedPriority(_ context.Context, runID string, rank int) (bool, error) {
	if q.priorityErr != nil {
		return false, q.priorityErr
	}
	if q.reprioritized == nil {
		q.reprioritized = map[string]int{}
	}
	q.reprioritized[runID] = rank
	return true, nil
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
	// The store sums a claim's ceilings from the poll's snapshot, so the fake
	// loads it the way a claim does: one poll hands the same loader to every
	// candidate, which is what its read count asserts (AC1).
	if facts.History != nil {
		if _, err := facts.History(); err != nil {
			return false, "", err
		}
	}
	if facts.PeakHeld {
		return false, ConditionPeakHours, nil
	}
	bindings := q.bindings
	if cfg.ProviderFree {
		if binding, ok := q.freeModelBindings[facts.Model]; ok {
			return false, binding, nil
		}
		bindings = q.freeBindings
	} else if binding, ok := q.planBindings[runner.Provider(facts.Model)]; ok {
		return false, binding, nil
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
	// pullOpen is the state PullOpen reports per PR number, and pullErr is what it fails with.
	pullOpen map[int]bool
	pullErr  error
	// pullCalls collects the PR numbers PullOpen was asked about.
	pullCalls *[]int
}

func (p fakeOpenPRs) PullOpen(_ context.Context, _ string, number int) (bool, error) {
	if p.pullCalls != nil {
		*p.pullCalls = append(*p.pullCalls, number)
	}
	return p.pullOpen[number], p.pullErr
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
		TicketStates: &fakeTicketStates{},
		Estimator:    fakeEstimator{},
		Visibility:   fakeVisibility{},
		Providers:    fakeProviders{},
		Breaker:      fakeBreaker{},
		Phases:       fakePhases{},
		PeakModes:    fakePeakModes{mode: PeakOffPeakOnly},
		OpenPRs:      fakeOpenPRs{},
		Overrides:    LabelOverrides{},
		ModelPlans:   &fakeModelPlans{},
		Notices:      &fakeNotices{},
		OpenPoster:   func(context.Context) (Poster, error) { return &fakePoster{}, nil },
		Elicit:       &fakeElicitor{},
		Decisions:    &fakeDecisions{},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:          func() time.Time { return pollAt },
		// The rewrite measurement reads GitHub and the run rows; a poll with no
		// merged PR to see does nothing with either.
		PullRequests: &fakePullRequests{},
		Rewrites:     &fakeRewrites{},
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
	taken     string
}

func (e *fakeElicitor) ElicitProvisional(_ context.Context, sessionID string, d runner.Decision, taken string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, elicitCall{sessionID: sessionID, decision: d, taken: taken})
	return e.err
}

func (e *fakeElicitor) Elicit(_ context.Context, sessionID string, d runner.Decision) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, elicitCall{sessionID: sessionID, decision: d})
	return e.err
}

// fakeDecisions is the store's decision surface as a poll uses it.
type fakeDecisions struct {
	waiting              []WaitingRun
	provisional          []ProvisionalRun
	waitingErr           error
	provisionalErr       error
	provisionalPostedErr error
	replied              []ProvisionalRun
	repliedErr           error
	confirmErr           error
	clearErr             error
	supersedeErr         error
	answerErr            error
	postedErr            error
	stopErr              error
	sessions             map[string]string
	sessionErr           error

	answered   []answerCall
	posted     []postedCall
	stopped    []stopCall
	confirmed  []string
	cleared    []string
	superseded []answerCall
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

func (d *fakeDecisions) ProvisionalUnposted(context.Context) ([]ProvisionalRun, error) {
	return d.provisional, d.provisionalErr
}

func (d *fakeDecisions) PostedProvisional(_ context.Context, runID, sessionID string, at time.Time) error {
	if d.provisionalPostedErr != nil {
		return d.provisionalPostedErr
	}
	d.posted = append(d.posted, postedCall{runID: runID, sessionID: sessionID, at: at})
	d.provisional = nil
	return nil
}

func (d *fakeDecisions) ProvisionalReplied(context.Context) ([]ProvisionalRun, error) {
	return d.replied, d.repliedErr
}

func (d *fakeDecisions) ConfirmProvisional(_ context.Context, runID string, _ int, _, _ time.Time) error {
	if d.confirmErr != nil {
		return d.confirmErr
	}
	d.confirmed = append(d.confirmed, runID)
	return nil
}

func (d *fakeDecisions) ClearProvisionalReply(_ context.Context, runID string, _ time.Time) error {
	if d.clearErr != nil {
		return d.clearErr
	}
	d.cleared = append(d.cleared, runID)
	return nil
}

func (d *fakeDecisions) Supersede(_ context.Context, runID string, _ int, _ time.Time, answer string, at time.Time) error {
	if d.supersedeErr != nil {
		return d.supersedeErr
	}
	d.superseded = append(d.superseded, answerCall{runID: runID, answer: answer, at: at})
	return nil
}

func (d *fakeDecisions) Session(_ context.Context, ticketID string) (string, error) {
	if d.sessionErr != nil {
		return "", d.sessionErr
	}
	return d.sessions[ticketID], nil
}

// fakePhases reports the repositories the owner has paused, or fails to read them.
type fakePhases struct {
	paused map[string]bool
	err    error
}

func (p fakePhases) Paused(context.Context) (map[string]bool, error) { return p.paused, p.err }

func TestPollHoldsANewClaimOfAPausedProjectOnly(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "plan-m", Repo: "octo/scratch", Size: "M", Priority: 1},
		{RunID: "single-s", Repo: "Octo/Scratch", Size: "S", Priority: 2},
		{RunID: "planned", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Priority: 3},
		{RunID: "other", Repo: "octo/other", Size: "M", Priority: 4},
	}}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Phases = fakePhases{paused: map[string]bool{"octo/scratch": true}}
	cfg := buildConfig
	cfg.TwoStage = true
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"plan-m": ConditionOwnerPaused, "single-s": ConditionOwnerPaused, "planned": "", "other": ""}
	if len(q.tryClaims) != len(want) {
		t.Fatalf("tryClaims = %+v, want one per candidate", q.tryClaims)
	}
	for _, call := range q.tryClaims {
		if got := HeldBeforeBudget(AdmitInput{Facts: call.facts}); got != want[call.runID] {
			t.Errorf("%s held by %q, want %q", call.runID, got, want[call.runID])
		}
	}
}

func TestPollHoldsEveryNewClaimWhenThePausedSetCannotBeRead(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "plan-m", Repo: "octo/scratch", Size: "M", Priority: 1},
		{RunID: "planned", Repo: "octo/scratch", Size: "M", Stage: StageBuild, Priority: 2},
	}}
	d := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	d.Phases = fakePhases{err: errFirestore}
	cfg := buildConfig
	cfg.TwoStage = true
	if _, err := Poll(context.Background(), d, cfg); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, call := range q.tryClaims {
		got[call.runID] = call.facts.OwnerPaused
	}
	if len(got) != 2 || !got["plan-m"] || got["planned"] {
		t.Fatalf("OwnerPaused by run = %v, want the new claim held and the planned build not", got)
	}
}

func TestPollNamesTheDependencyItWasNotGiven(t *testing.T) {
	unset := map[string]func(*Deps){
		"Source":     func(d *Deps) { d.Source = nil },
		"Queue":      func(d *Deps) { d.Queue = nil },
		"Estimator":  func(d *Deps) { d.Estimator = nil },
		"Visibility": func(d *Deps) { d.Visibility = nil },
		"Providers":  func(d *Deps) { d.Providers = nil },
		"Breaker":    func(d *Deps) { d.Breaker = nil },
		"Phases":     func(d *Deps) { d.Phases = nil },
		"PeakModes":  func(d *Deps) { d.PeakModes = nil },
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
		// The rewrite measurement's own reads.
		"PullRequests": func(d *Deps) { d.PullRequests = nil },
		"Rewrites":     func(d *Deps) { d.Rewrites = nil },
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
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Size: "M", Priority: 2, Attempt: 3}}}
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
	if !slices.Equal(res.Dispatched, []string{"FRG-18"}) || !slices.Equal(w.claims, []Claim{{RunID: "FRG-18", Repo: "octo/scratch", Priority: 2, Attempt: 3}}) {
		t.Fatalf("dispatched %q, %+v", res.Dispatched, w.claims)
	}
}

func TestPollRefusesWhatTheListedRepositoriesDoNotName(t *testing.T) {
	q := &fakeQueue{}
	res, err := Poll(context.Background(), pollDeps(fakeSource{issues: []Issue{
		admitted("size:S", "repo:someone/else"),
		admitted("size:S", "repo:octo/scratch"),
	}}, q, &fakeWorkflow{}), Config{Repos: []string{"octo/scratch"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := reasons(res.Rejections); !slices.Equal(got, []Refusal{RefusalNotAllowlist}) {
		t.Fatalf("refused %v", got)
	}
	if len(q.queued) != 1 || q.queued[0].Repo != "octo/scratch" {
		t.Fatalf("queued = %+v", q.queued)
	}
}

func TestPollAdmitsARepositoryListedSinceThePollBefore(t *testing.T) {
	issues := []Issue{admitted("size:S", "repo:octo/new")}
	before := &fakeQueue{}
	if _, err := Poll(context.Background(), pollDeps(fakeSource{issues: issues}, before, &fakeWorkflow{}), Config{Repos: []string{"octo/old"}}); err != nil {
		t.Fatal(err)
	}
	if len(before.queued) != 0 || len(before.rejected) != 1 {
		t.Fatalf("before the listing: queued %+v, rejected %+v", before.queued, before.rejected)
	}
	after := &fakeQueue{}
	if _, err := Poll(context.Background(), pollDeps(fakeSource{issues: issues}, after, &fakeWorkflow{}), Config{Repos: []string{"octo/old", "octo/new"}}); err != nil {
		t.Fatal(err)
	}
	if len(after.queued) != 1 || len(after.rejected) != 0 {
		t.Fatalf("after the listing: queued %+v, rejected %+v", after.queued, after.rejected)
	}
}

func TestPollAdmitsNothingAgainstAnAllowlistItCannotUse(t *testing.T) {
	for name, tt := range map[string]struct {
		cfg Config
		log string
	}{
		"an unreadable list": {Config{ReposErr: errFirestore}, "msg=allowlistUnreadable"},
		"an empty list":      {Config{}, "msg=allowlistEmpty"},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{
				candidates:     []Candidate{{RunID: "run-18", Repo: "octo/scratch", Size: "M", Priority: 2}},
				transientStops: []TransientStop{{RunID: "run-9", Class: runner.TransientModuleProxy}},
			}
			w := &fakeWorkflow{}
			var logs strings.Builder
			deps := pollDeps(fakeSource{issues: []Issue{admitted("size:S", "repo:octo/scratch"), admitted("size:S", "repo:someone/else")}}, q, w)
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			res, err := Poll(context.Background(), deps, tt.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if res.Seen != 2 || len(res.Enqueued) != 0 || len(res.Rejections) != 0 || len(q.rejected) != 0 || len(q.queued) != 0 {
				t.Fatalf("result %+v, queued %+v, rejected %+v: want nothing admitted or refused", res, q.queued, q.rejected)
			}
			if len(q.tryClaims) != 0 || len(w.claims) != 0 {
				t.Fatalf("claimed %+v, dispatched %+v, want no claim walk", q.tryClaims, w.claims)
			}
			if len(q.requeued) != 1 {
				t.Fatalf("requeued = %+v, want the other phases to run", q.requeued)
			}
			if !strings.Contains(logs.String(), tt.log) {
				t.Fatalf("log = %q, want %s", logs.String(), tt.log)
			}
		})
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

func TestPollRefreshesTheRankOfATicketTheQueueAlreadyHolds(t *testing.T) {
	q := &fakeQueue{holds: map[string]bool{"FRG-18": true}}
	issue := admitted("size:M", "repo:octo/scratch")
	issue.Priority = 1
	if _, err := Poll(context.Background(), pollDeps(fakeSource{issues: []Issue{issue}}, q, &fakeWorkflow{}), buildConfig); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.reprioritized, map[string]int{"FRG-18": 1}) {
		t.Fatalf("reprioritized = %v, want FRG-18 at rank 1", q.reprioritized)
	}
}

func TestPollDoesNotRefreshATicketItEnqueues(t *testing.T) {
	q := &fakeQueue{}
	deps := pollDeps(fakeSource{issues: []Issue{admitted("size:M", "repo:octo/scratch")}}, q, &fakeWorkflow{})
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.reprioritized) != 0 {
		t.Fatalf("reprioritized = %v, want none for a new ticket", q.reprioritized)
	}
}

func TestPollAdmitsPastAFailedRankRefresh(t *testing.T) {
	q := &fakeQueue{
		holds:       map[string]bool{"FRG-18": true},
		priorityErr: errFirestore,
		candidates:  []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Priority: 2}},
	}
	w := &fakeWorkflow{}
	var logs strings.Builder
	deps := pollDeps(fakeSource{issues: []Issue{admitted("size:M", "repo:octo/scratch")}}, q, w)
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatalf("err = %v, want the poll to continue past the failed refresh", err)
	}
	if !slices.Equal(res.Dispatched, []string{"FRG-18"}) {
		t.Fatalf("dispatched = %q, want the candidate admitted", res.Dispatched)
	}
	if !strings.Contains(logs.String(), "msg=priorityRefreshFailed") {
		t.Fatalf("log = %q, want priorityRefreshFailed", logs.String())
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

// A backlog costs a poll one read of the settled history, not one per candidate
// (AC1): the walk hands every candidate the same loader, which reads once.
func TestPollReadsTheSettledHistoryOnceHoweverManyCandidatesItWalks(t *testing.T) {
	ids := []string{"FRG-18", "FRG-19", "FRG-20", "FRG-21", "FRG-22", "FRG-23",
		"FRG-24", "FRG-25", "FRG-26", "FRG-27", "FRG-28", "FRG-29"}
	q := &fakeQueue{}
	for rank, id := range ids {
		q.candidates = append(q.candidates, Candidate{RunID: id, Repo: "octo/scratch", Size: "M", Priority: rank + 1})
	}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != len(ids) || len(res.Dispatched) != len(ids) {
		t.Fatalf("walked %d candidates and dispatched %d, want %d of each", len(q.tryClaims), len(res.Dispatched), len(ids))
	}
	if q.histories != 1 {
		t.Fatalf("the settled history was read %d times for %d candidates, want once", q.histories, len(ids))
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
// countingEstimator counts every read the poll makes of the estimator, per
// size, through a pointer so it survives the struct being copied into Deps.
type countingEstimator struct {
	fakeEstimator
	builds, plans map[string]int
}

func (e countingEstimator) Estimate(ctx context.Context, size string) (Estimate, error) {
	e.builds[size]++
	return e.fakeEstimator.Estimate(ctx, size)
}

func (e countingEstimator) EstimatePlan(ctx context.Context, size string) (Estimate, error) {
	e.plans[size]++
	return e.fakeEstimator.EstimatePlan(ctx, size)
}

func TestPollEstimatesEachSizeOnceHoweverManyCandidatesHoldIt(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "FRG-18", Repo: "octo/scratch", Size: "M", Priority: 1},
		{RunID: "FRG-19", Repo: "octo/scratch", Size: "M", Priority: 2},
		{RunID: "FRG-20", Repo: "octo/scratch", Size: "M", Priority: 3},
	}}
	est := countingEstimator{builds: map[string]int{}, plans: map[string]int{}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Estimator = est
	cfg := buildConfig
	cfg.TwoStage = true
	if _, err := Poll(context.Background(), deps, cfg); err != nil {
		t.Fatal(err)
	}
	if est.builds["M"] != 1 || est.plans["M"] != 1 {
		t.Fatalf("three M candidates read the estimator %d times for the build and %d for the plan, want once each", est.builds["M"], est.plans["M"])
	}
}

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

func TestPollDoesNotCountTheOpenPRARunSupersedesAgainstItsReplacement(t *testing.T) {
	for name, tt := range map[string]struct {
		pullOpen map[int]bool
		pullErr  error
		want     int
	}{
		"the superseded PR is open":        {pullOpen: map[int]bool{140: true}, want: 2},
		"the superseded PR already closed": {pullOpen: map[int]bool{140: false}, want: 3},
		"the superseded PR cannot be read": {pullErr: errFirestore, want: 3},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{
				{RunID: "run-a", Repo: "octo/a", Priority: 1, SupersededPR: 140},
				{RunID: "run-b", Repo: "octo/a", Priority: 2},
			}}
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			var asked []int
			deps.OpenPRs = fakeOpenPRs{open: map[string]int{"octo/a": 3}, pullOpen: tt.pullOpen, pullErr: tt.pullErr, pullCalls: &asked}
			if _, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a"}}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(asked, []int{140}) {
				t.Fatalf("PullOpen asked about %v, want only the superseded PR", asked)
			}
			if len(q.tryClaims) != 2 {
				t.Fatalf("claims = %d, want both candidates asked", len(q.tryClaims))
			}
			if got := q.tryClaims[0].facts.OpenPRs; got != tt.want {
				t.Fatalf("the replacement was admitted against %d open PRs, want %d", got, tt.want)
			}
			if got := q.tryClaims[1].facts.OpenPRs; got != 3 {
				t.Fatalf("a run replacing nothing was admitted against %d open PRs, want all 3", got)
			}
		})
	}
}

func TestPollNeverCountsBelowZeroForASupersedingRun(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1, SupersededPR: 140}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.OpenPRs = fakeOpenPRs{open: map[string]int{"octo/a": 0}, pullOpen: map[int]bool{140: true}}
	if _, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a"}}); err != nil {
		t.Fatal(err)
	}
	if got := q.tryClaims[0].facts.OpenPRs; got != 0 {
		t.Fatalf("open PRs = %d, want 0 when the listing shows none", got)
	}
}

func TestPollLeavesAnUnknownOpenPRCountAloneForASupersedingRun(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1, SupersededPR: 140}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.OpenPRs = fakeOpenPRs{err: errFirestore, pullOpen: map[int]bool{140: true}}
	if _, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a"}}); err != nil {
		t.Fatal(err)
	}
	if got := q.tryClaims[0].facts; got.OpenPRsKnown || got.OpenPRs != 0 {
		t.Fatalf("facts = %+v, want the unknown count left as it was", got)
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

func TestPollReadsTheOpenPRCountOfEveryRepositoryWhenNothingIsQueued(t *testing.T) {
	deps := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	calls := map[string]int{}
	deps.OpenPRs = fakeOpenPRs{calls: calls}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if calls["AlvinToh/Forge-Wingman"] != 1 || calls["octo/scratch"] != 1 {
		t.Fatalf("reads = %v, want one per listed repository", calls)
	}
}

func TestPollReadsEveryRepositoryAfterAnEarlyFailure(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	calls := map[string]int{}
	deps.OpenPRs = fakeOpenPRs{err: errFirestore, failOnly: "octo/a", calls: calls}
	if _, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a", "octo/b", "octo/c"}}); err != nil {
		t.Fatal(err)
	}
	if calls["octo/a"] != 1 || calls["octo/b"] != 1 || calls["octo/c"] != 1 {
		t.Fatalf("reads = %v, want every repository read once", calls)
	}
	if got := q.tryClaims[0].facts; got.OpenPRsKnown || got.OpenPRs != 0 {
		t.Fatalf("facts = %+v, want the count unknown while one read failed", got)
	}
}

func TestPollRecordsOneReachPerListedRepository(t *testing.T) {
	deps := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
	rec := &fakeReach{}
	deps.Reach = rec
	deps.OpenPRs = fakeOpenPRs{err: &APIError{Status: 404, message: "GitHub refused to list: 404 Not Found: gone"}, failOnly: "octo/b"}
	res, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a", "octo/b"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []RepoReach{
		{Repository: "octo/a", Reachable: true, CheckedAt: pollAt},
		{Repository: "octo/b", Reason: "not-found", Status: 404, CheckedAt: pollAt},
	}
	if !slices.Equal(rec.recorded, want) || !slices.Equal(res.Reach, want) {
		t.Fatalf("recorded %+v, result %+v, want %+v", rec.recorded, res.Reach, want)
	}
}

func TestPollRecordsNoReachWhenTheListIsEmptyOrUnreadable(t *testing.T) {
	for name, cfg := range map[string]Config{
		"empty":      {},
		"unreadable": {ReposErr: errFirestore},
	} {
		t.Run(name, func(t *testing.T) {
			deps := pollDeps(fakeSource{}, &fakeQueue{}, &fakeWorkflow{})
			rec := &fakeReach{}
			deps.Reach = rec
			if _, err := Poll(context.Background(), deps, cfg); err != nil {
				t.Fatal(err)
			}
			if rec.calls != 0 {
				t.Fatalf("the recorder was called %d times, want none", rec.calls)
			}
		})
	}
}

func TestPollNeitherStopsNorRefusesATicketWhenTheReachWriteFails(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
	var logs strings.Builder
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	deps.Reach = &fakeReach{err: errFirestore}
	res, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a"}})
	if err != nil {
		t.Fatalf("err = %v, want the poll to continue past the failed write", err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) || len(q.rejected) != 0 {
		t.Fatalf("dispatched %v, rejected %v, want the ticket dispatched", res.Dispatched, q.rejected)
	}
	if !strings.Contains(logs.String(), "level=WARN msg=reachRecordFailed") {
		t.Fatalf("logs:\n%s\nwant a reachRecordFailed warning", logs.String())
	}
}

func TestPollAdmitsTheSameWhetherOrNotReachIsRecorded(t *testing.T) {
	admitted := func(rec ReachRecorder) string {
		q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/a", Priority: 1}}}
		deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
		deps.Reach = rec
		deps.OpenPRs = fakeOpenPRs{open: map[string]int{"octo/a": 2}}
		if _, err := Poll(context.Background(), deps, Config{Repos: []string{"octo/a"}}); err != nil {
			t.Fatal(err)
		}
		f := q.tryClaims[0].facts
		return fmt.Sprint(len(q.tryClaims), f.OpenPRs, f.OpenPRsKnown, f.PeakHeld, f.BreakerTripped)
	}
	if with, without := admitted(&fakeReach{}), admitted(nil); with != without {
		t.Fatalf("admission with a recorder = %q, without = %q, want them equal", with, without)
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
	custom := Limits{PlatformCap: 4, LargeCap: 2, ReviewWIP: 6, RepoCap: 4}
	partial := Limits{ReviewWIP: 6}
	for name, tt := range map[string]struct {
		cfg  Limits
		want Limits
	}{
		"the zero value means the defaults": {want: DefaultLimits},
		"a configured value is kept":        {cfg: custom, want: custom},
		"a partial value defaults only the fields it leaves out": {cfg: partial,
			want: Limits{PlatformCap: DefaultLimits.PlatformCap, LargeCap: DefaultLimits.LargeCap, ReviewWIP: 6, RepoCap: DefaultLimits.RepoCap}},
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
	issue.BlockedByOpen = []string{"run-p"}
	if _, err := Poll(context.Background(), pollDeps(fakeSource{issues: []Issue{issue}}, q, &fakeWorkflow{}), buildConfig); err != nil {
		t.Fatal(err)
	}
	want := Relations{Known: true, BlockedBy: []string{"run-p"}, Blocks: []string{"run-x"}, BlockedByOpen: []string{"run-p"}}
	if got := q.tryClaims[0].facts.Relations; !got.Known || !slices.Equal(got.BlockedBy, want.BlockedBy) ||
		!slices.Equal(got.Blocks, want.Blocks) || !slices.Equal(got.BlockedByOpen, want.BlockedByOpen) {
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
	LastResortPlans: [][]string{{"p/free-a", "p/free-b"}},
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

// TestPollKeepsTheStageOfAPlanStageClaimOnTheFreeTier asserts a plan-stage
// claim that falls back to the free tier still carries the run's stage: the
// store books and settles a plan stage's reservation under its own key, so a
// claim that dropped the stage would book the build's and never be settled.
func TestPollKeepsTheStageOfAPlanStageClaimOnTheFreeTier(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}},
		bindings:   map[string]string{"run-a": "p-5h"},
	}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Estimator = fakeEstimator{
		bySize:     map[string]Estimate{"M": {ProviderCost: 2 * money.Dollar, Minutes: 30}},
		planBySize: map[string]Estimate{"M": {ProviderCost: money.Dollar, Minutes: 5}},
	}
	plan := paidPlan
	plan.TwoStage = true
	res, err := Poll(context.Background(), deps, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) || len(res.Deferrals) != 0 {
		t.Fatalf("dispatched %q, deferred %+v, want the run dispatched on the free tier", res.Dispatched, res.Deferrals)
	}
	free := q.tryClaims[len(q.tryClaims)-1]
	if len(q.tryClaims) != 2 || !free.cfg.ProviderFree || free.res.ProviderCost != 0 {
		t.Fatalf("claims %+v, want a second claim on the free tier", q.tryClaims)
	}
	if free.res.Stage != StagePlan {
		t.Fatalf("reservation = %+v, want the plan stage kept on the free tier", free.res)
	}
}

// TestPollWalksTheFreeTierPlansInOrder asserts a candidate whose own plan's
// ceiling binds, and whose own free tier cannot staff a run, is admitted on the
// next plan's free models — the move recorded on the claim's facts, so the run
// record names the plan it moved to (AC4).
func TestPollWalksTheFreeTierPlansInOrder(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		bindings:   map[string]string{"run-a": "p-5h"},
		// The first plan's free rung is over its cash ceiling; the second
		// plan's admits.
		freeModelBindings: map[string]string{"p/free-a": CeilingCash},
	}
	plan := paidPlan
	plan.LastResortPlans = [][]string{{"p/free-a", "p/free-b"}, {"q/free-a", "q/free-b"}}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Dispatched, []string{"run-a"}) || len(res.Deferrals) != 0 {
		t.Fatalf("dispatched %q, deferred %+v, want the run dispatched on the second plan's free tier", res.Dispatched, res.Deferrals)
	}
	// The paid claim, then the first rung's failed free claim, then the second
	// rung's.
	if len(q.tryClaims) != 3 {
		t.Fatalf("TryClaim ran %d times, want 3 (the walk continues past a rung whose own ceiling binds)", len(q.tryClaims))
	}
	last := q.tryClaims[2]
	if !last.cfg.ProviderFree {
		t.Fatalf("the admitting claim = %+v, want the free tier", last.cfg)
	}
	if runner.Provider(last.facts.Model) != "q" {
		t.Fatalf("facts.Model = %q, want the second plan's free model: the move must be recorded on the claim", last.facts.Model)
	}
	want := runner.ModelLabels{Build: "q/free-a", Review: "q/free-b", Plan: []string{"q/free-a", "q/free-b"}}
	if last.facts.LastResort == nil || !reflect.DeepEqual(*last.facts.LastResort, want) {
		t.Fatalf("facts.LastResort = %+v, want the second plan's free models", last.facts.LastResort)
	}
}

// TestPollReadsTheCandidatesOwnPlansHalt asserts the provider halt is keyed off
// each candidate's own plan, not the default model's, so a run that names
// another plan is judged against that plan's state.
func TestPollReadsTheCandidatesOwnPlansHalt(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Model: "q/x"}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Providers = fakeProviders{halted: map[string]bool{"q": true}}
	cfg := Config{Repos: buildConfig.Repos, Budget: buildConfig.Budget, Model: "p/paid"}
	if _, err := Poll(context.Background(), deps, cfg); err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || !q.tryClaims[0].facts.ProviderHalted {
		t.Fatalf("tryClaims = %+v, want the candidate's own plan read as halted", q.tryClaims)
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
	plan.LastResortPlans = [][]string{{"p/free-a"}}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "p-5h" {
		t.Fatalf("claims %d, deferrals %+v, want one claim deferred on the plan window", len(q.tryClaims), res.Deferrals)
	}
}

// TestPollSkipsAFreeTierPlanThatCannotStaffAReview: the order is walked rung by
// rung, and one whose plan has a single free model cannot staff both the build
// and its review — so it is skipped rather than claimed.
func TestPollSkipsAFreeTierPlanThatCannotStaffAReview(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "run-a"}},
		bindings:   map[string]string{"run-a": "p-5h"},
	}
	plan := paidPlan
	plan.LastResortPlans = [][]string{{"p/free-alone"}, {"p/free-a", "p/free-b"}}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 2 || len(res.Dispatched) != 1 {
		t.Fatalf("claims %d, dispatched %q, want the one-model rung skipped and the next one claimed",
			len(q.tryClaims), res.Dispatched)
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
	plan.LastResortPlans = [][]string{{"p/free-a"}}
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

// TestPollJudgesAFreeTierRungByItsOwnPlan: the halt and the verdict are read for
// the plan the run would land on, not for the plan that withheld it.
func TestPollJudgesAFreeTierRungByItsOwnPlan(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		bindings:   map[string]string{"run-a": "p-5h"},
	}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{}, q, w)
	deps.Providers = fakeProviders{halted: map[string]bool{"q": true}}
	deps.Plans = fakeVerdicts{verdicts: map[string]providers.Verdict{"p": providers.VerdictAllowed, "q": providers.VerdictRestricted}}
	plan := paidPlan
	plan.LastResortPlans = [][]string{{"q/free-a", "q/free-b"}}
	if _, err := Poll(context.Background(), deps, plan); err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 2 || q.tryClaims[0].facts.ProviderHalted || !q.tryClaims[1].facts.ProviderHalted {
		t.Fatalf("tryClaims = %+v, want the halt read for the paid plan, then for the free plan", q.tryClaims)
	}
	if len(w.claims) != 1 || w.claims[0].Verdict != providers.VerdictRestricted {
		t.Fatalf("claims = %+v, want the free plan's verdict on the claim", w.claims)
	}
}

func TestPollStopsWalkingTheFreeTierOnceARungAdmits(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		bindings:   map[string]string{"run-a": "p-5h"},
	}
	plan := paidPlan
	plan.LastResortPlans = [][]string{{"p/free-a", "p/free-b"}, {"q/free-a", "q/free-b"}}
	if _, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), plan); err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 2 || q.tryClaims[1].facts.Model != "p/free-a" {
		t.Fatalf("tryClaims = %+v, want the first rung claimed and the walk ended", q.tryClaims)
	}
}

func TestPollStopsWalkingTheFreeTierWhenAnotherPollClaimedTheRun(t *testing.T) {
	q := &fakeQueue{
		candidates:  []Candidate{{RunID: "run-a", Repo: "octo/scratch"}},
		bindings:    map[string]string{"run-a": "p-5h"},
		unavailable: map[string]bool{"run-a": true},
	}
	plan := paidPlan
	plan.LastResortPlans = [][]string{{"p/free-a", "p/free-b"}, {"q/free-a", "q/free-b"}}
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 2 || len(res.Dispatched) != 0 || len(res.Deferrals) != 0 {
		t.Fatalf("claims %d, dispatched %q, deferrals %+v, want one free claim and no admission matter", len(q.tryClaims), res.Dispatched, res.Deferrals)
	}
}

func TestPollDefersABuildStageRatherThanMovingItToTheFreeTier(t *testing.T) {
	q := &fakeQueue{
		candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M", Stage: StageBuild}},
		bindings:   map[string]string{"run-a": "p-5h"},
	}
	plan := paidPlan
	plan.TwoStage = true
	res, err := Poll(context.Background(), pollDeps(fakeSource{}, q, &fakeWorkflow{}), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.tryClaims) != 1 || len(res.Deferrals) != 1 || res.Deferrals[0].Ceiling != "p-5h" {
		t.Fatalf("claims %d, deferrals %+v, want one claim deferred on the plan window", len(q.tryClaims), res.Deferrals)
	}
}

// TestPollMovesTheClaimedTicketInProgress: the ticket a claimed run is building
// shows In Progress from the claim, and the state it held before it is recorded
// on the run, which a requeue or an abandon puts it back to.
func TestPollMovesTheClaimedTicketInProgress(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Size: "M"}}}
	tickets := &fakeTicketStates{prior: map[string]string{"FRG-18": "state-todo"}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.TicketStates = tickets
	res, err := Poll(context.Background(), deps, buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tickets.started, []string{"FRG-18"}) {
		t.Fatalf("started = %v, want the claimed ticket moved In Progress", tickets.started)
	}
	if got := q.ticketStates["FRG-18"]; got != "state-todo" {
		t.Fatalf("recorded state = %q, want the state the ticket held before the claim", got)
	}
	if len(res.Dispatched) != 1 || res.Dispatched[0] != "FRG-18" {
		t.Fatalf("dispatched %q, want the claimed run", res.Dispatched)
	}
}

// TestPollRecordsNoStateForATicketTheClaimDidNotMove: a ticket the claim left
// where it was — already In Progress, In Review, Done or Canceled — leaves the
// run nothing to put back.
func TestPollRecordsNoStateForATicketTheClaimDidNotMove(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Size: "M"}}}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.TicketStates = &fakeTicketStates{}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(q.ticketStates) != 0 {
		t.Fatalf("recorded %v, want nothing for a ticket no claim moved", q.ticketStates)
	}
}

// TestPollSucceedsWhenOneListedRepositoryCannotBeListed: one repository whose
// closed pull requests cannot be read neither fails the poll nor withholds a run.
func TestPollSucceedsWhenOneListedRepositoryCannotBeListed(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "ABC-18", Repo: "octo/scratch", Size: "M"}}}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{}, q, w)
	deps.PullRequests = &fakePullRequests{closedErr: errors.New("not found"), failClosed: "AlvinToh/Forge-Wingman"}
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatalf("err = %v, want the poll to succeed", err)
	}
	if len(w.claims) != 1 {
		t.Fatalf("dispatches = %+v, want the run started", w.claims)
	}
}

// TestPollDispatchesDespiteAFailedTicketMove: a failed Linear write is logged
// once, with the reason as its own field, and never withholds the run's start.
func TestPollDispatchesDespiteAFailedTicketMove(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{{RunID: "FRG-18", Repo: "octo/scratch", Size: "M"}}}
	w := &fakeWorkflow{}
	var logs bytes.Buffer
	deps := pollDeps(fakeSource{}, q, w)
	deps.TicketStates = &fakeTicketStates{startErr: errors.New("linear said no")}
	deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(w.claims) != 1 {
		t.Fatalf("dispatches = %+v, want the run started anyway", w.claims)
	}
	if got := strings.Count(logs.String(), "msg=ticketStateNotSet"); got != 1 {
		t.Fatalf("log = %q, want one ticketStateNotSet", logs.String())
	}
	if !strings.Contains(logs.String(), `err="linear said no"`) {
		t.Fatalf("log = %q, want the reason as its own field", logs.String())
	}
}
