package dispatcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
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

// fakeQueue is the store a poll writes, with the outcome each call is to have.
type fakeQueue struct {
	queued     []Queued
	holds      map[string]bool
	rejected   []Rejection
	released   []string
	claim      Claim
	claimed    bool
	claimErr   error
	enqueueErr error
	rejectErr  error
	releaseErr error
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

func (q *fakeQueue) Claim(context.Context, time.Time) (Claim, bool, error) {
	if q.claimErr != nil {
		return Claim{}, false, q.claimErr
	}
	return q.claim, q.claimed, nil
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

func pollDeps(source Source, q *fakeQueue, w *fakeWorkflow) Deps {
	return Deps{
		Source: source, Queue: q, Workflow: w,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return pollAt },
	}
}

func TestPollAdmitsWhatItCanAndRefusesWhatItCannot(t *testing.T) {
	claim := Claim{RunID: "FRG-18", Repo: "octo/scratch", Priority: 2}
	q := &fakeQueue{claim: claim, claimed: true}
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
	if res.Dispatched != "FRG-18" || !slices.Equal(w.claims, []Claim{claim}) {
		t.Fatalf("dispatched %q, %+v", res.Dispatched, w.claims)
	}
}

func TestPollLeavesATicketTheQueueAlreadyHolds(t *testing.T) {
	q := &fakeQueue{holds: map[string]bool{"FRG-18": true}}
	res, err := Poll(context.Background(), pollDeps(fakeSource{issues: []Issue{
		admitted("size:M", "repo:octo/scratch"),
	}}, q, &fakeWorkflow{}), buildConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Enqueued) != 0 || len(q.queued) != 0 || len(q.rejected) != 0 {
		t.Fatalf("result %+v, queue %+v", res, q)
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
	q := &fakeQueue{claim: Claim{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1}, claimed: true}
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
	q := &fakeQueue{claim: Claim{RunID: "FRG-18"}, claimed: true, releaseErr: releaseErr}
	_, err := Poll(context.Background(), pollDeps(fakeSource{}, q,
		&fakeWorkflow{err: dispatchErr}), buildConfig)
	if !errors.Is(err, dispatchErr) || !errors.Is(err, releaseErr) {
		t.Fatalf("err = %v, want both the dispatch and the release failure", err)
	}
}

func TestPollFailsOnTheStore(t *testing.T) {
	for name, tt := range map[string]struct {
		queue  *fakeQueue
		source fakeSource
		issues []Issue
	}{
		"the source cannot be read":   {queue: &fakeQueue{}, source: fakeSource{err: errFirestore}},
		"the queue cannot be claimed": {queue: &fakeQueue{claimErr: errFirestore}},
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

func reasons(rejections []Rejection) []Refusal {
	var out []Refusal
	for _, r := range rejections {
		out = append(out, r.Reason)
	}
	return out
}
