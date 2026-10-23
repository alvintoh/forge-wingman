package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// RunRetries retries a stopped run with the owner's note, for the owner named.
type RunRetries interface {
	RetryWithNote(ctx context.Context, runID string, req store.RetryRequest, at time.Time) (store.RetryResult, error)
}

// The reasons a retry is refused, each answered with status 409. The PR ones
// beside merge's own: a ready PR is merged instead, and a merged or closed one
// is not the attempt's to replace.
const (
	refusedAbandoned    = "abandoned"
	refusedAlreadyQueue = "already-queued"
	refusedAttemptMoved = "attempt-moved"
	refusedInFlight     = "in-flight"
	refusedMerged       = "merged"
	refusedNoChanges    = "no-changes"
	refusedPRClosed     = "pr-closed"
	refusedPRMerged     = "pr-merged"
	refusedPRReady      = "pr-ready"
	refusedWaiting      = "waiting"
	// refusedNoMergeToken answers, with 503, a retry of a run with a PR on a
	// surface that cannot read GitHub.
	refusedNoMergeToken = "no-merge-token"

	// maxRetryBodyBytes bounds a retry's body: a note of runner.MaxNoteBytes, JSON-escaped.
	maxRetryBodyBytes = 8 * runner.MaxNoteBytes
)

// retryStatus is the retry endpoint's answer to a retry it made or found made:
// the attempt the run is queued as, from 0 as the inbox's attempt is.
type retryStatus struct {
	Status  string `json:"status"`
	Attempt int    `json:"attempt"`
}

// retryRefusals maps what the store refuses a retry for to the reason it answers with.
var retryRefusals = []struct {
	err    error
	reason string
}{
	{store.ErrAttemptMoved, refusedAttemptMoved},
	{store.ErrRunInFlight, refusedInFlight},
	{store.ErrRunWaiting, refusedWaiting},
	{store.ErrRunDispatchable, refusedAlreadyQueue},
	{store.ErrRunAbandoned, refusedAbandoned},
	{store.ErrRunMerged, refusedMerged},
	{store.ErrRunNoChanges, refusedNoChanges},
}

// retryRun queues the run again for the owner's note, as the signed-in owner.
//
// The body names only the note and the attempt the owner saw. Who retried comes
// from the IAP identity, and which PR the new attempt replaces from GitHub's
// reading of the one the row names: an open draft is replaced, a ready one is
// merged instead, and a run with no PR has none to read.
func (d Deps) retryRun(w http.ResponseWriter, r *http.Request) {
	if d.Runs == nil || d.Retries == nil {
		http.Error(w, "retrying needs a run store", http.StatusServiceUnavailable)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "a retry is requested with a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	actor := requestEmail(r)
	if actor == nil || *actor == "" {
		http.Error(w, "this service is reached through IAP", http.StatusUnauthorized)
		return
	}
	var body struct {
		Note            *string `json:"note"`
		ExpectedAttempt *int    `json:"expected_attempt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRetryBodyBytes)).Decode(&body); err != nil ||
		body.Note == nil || body.ExpectedAttempt == nil || *body.ExpectedAttempt < 0 {
		http.Error(w, "the request body is not JSON naming a note and the attempt it saw", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(*body.Note)
	if note == "" || !runner.ValidNote(note) {
		http.Error(w, "the note is blank or too long to carry", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mergeTimeout)
	defer cancel()
	run := r.PathValue("run")
	rec, err := d.Runs.GetRecord(ctx, run)
	if errors.Is(err, runner.ErrRecordNotFound) {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}
	if err != nil {
		d.Logger.Error("ownerRetryRunNotRead", "run", run, "err", err.Error())
		http.Error(w, "reading the run", http.StatusInternalServerError)
		return
	}
	supersedes := 0
	if rec.PRURL != "" {
		var ok bool
		if supersedes, ok = d.draftToSupersede(ctx, w, run, rec.PRURL); !ok {
			return
		}
	}
	res, err := d.Retries.RetryWithNote(ctx, run,
		store.RetryRequest{Note: note, Actor: *actor, ExpectedAttempt: *body.ExpectedAttempt, SupersedePR: supersedes}, time.Now())
	if errors.Is(err, runner.ErrRecordNotFound) {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}
	for _, refusal := range retryRefusals {
		if errors.Is(err, refusal.err) {
			d.refuseRetry(w, run, refusal.reason)
			return
		}
	}
	if err != nil {
		d.Logger.Error("ownerRetryFailed", "run", run, "err", err.Error())
		http.Error(w, "retrying the run", http.StatusInternalServerError)
		return
	}
	outcome := "queued"
	if res.Already {
		outcome = "already-retried"
	}
	d.Logger.Info("ownerRetried", "run", run, "attempt", res.Attempt, "status", outcome, "supersedes", supersedes)
	writeJSON(w, http.StatusOK, retryStatus{Status: outcome, Attempt: res.Attempt})
}

// draftToSupersede reads the PR a run's row names and returns its number when it
// is an open draft, which the retry replaces. Any other state, or a PR that is
// not the run's own or cannot be read, has been answered by the time it reports
// false.
func (d Deps) draftToSupersede(ctx context.Context, w http.ResponseWriter, run, prURL string) (number int, ok bool) {
	if d.Merger == nil {
		d.Logger.Info("ownerRetryRefused", "run", run, "reason", refusedNoMergeToken)
		writeJSON(w, http.StatusServiceUnavailable, mergeRefusal{Reason: refusedNoMergeToken})
		return 0, false
	}
	repo, number, err := parsePullURL(prURL)
	if err != nil {
		d.refuseRetry(w, run, refusedNoPullRequest)
		return 0, false
	}
	queuedFor, err := d.Runs.RunRepository(ctx, run)
	if err != nil {
		d.Logger.Error("ownerRetryRunNotRead", "run", run, "err", err.Error())
		http.Error(w, "reading the run", http.StatusInternalServerError)
		return 0, false
	}
	if !strings.EqualFold(repo, queuedFor) {
		d.refuseRetry(w, run, refusedNoPullRequest)
		return 0, false
	}
	state, err := d.Merger.PullState(ctx, repo, number)
	if err != nil {
		d.Logger.Error("ownerRetryFailed", "run", run, "repo", repo, "number", number, "doing", "reading the pull request", "err", err.Error())
		http.Error(w, "reading the pull request failed", http.StatusBadGateway)
		return 0, false
	}
	switch {
	case state.Merged:
		d.refuseRetry(w, run, refusedPRMerged)
	case !state.Open:
		d.refuseRetry(w, run, refusedPRClosed)
	case !state.Draft:
		d.refuseRetry(w, run, refusedPRReady)
	default:
		return number, true
	}
	return 0, false
}

// refuseRetry answers a retry it will not make with reason, and logs it.
func (d Deps) refuseRetry(w http.ResponseWriter, run, reason string) {
	d.Logger.Info("ownerRetryRefused", "run", run, "reason", reason)
	writeJSON(w, http.StatusConflict, mergeRefusal{Reason: reason})
}
