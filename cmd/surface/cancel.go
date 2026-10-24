package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// CancelRuns records the owner's cancel on a run, refusing one that no longer
// waits on the owner in the same write.
type CancelRuns interface {
	RecordCancel(ctx context.Context, id, by string, at time.Time) error
}

// cancelTimeout bounds one cancel request.
const cancelTimeout = 10 * time.Second

// The answers a cancel gives, matched as strings by the row's client.
const (
	statusCancelled        = "cancelled"
	statusAlreadyCancelled = "already-cancelled"
	statusAlreadyMerged    = "already-merged"
	// refusedNotWaiting answers a run whose PR does not wait on the owner, with
	// status 409.
	refusedNotWaiting = "not-waiting"
)

// cancelStatus is the cancel endpoint's answer: the run it cancelled, or one
// that had already stopped waiting on the owner.
type cancelStatus struct {
	Status string `json:"status"`
}

// cancelRefusal is the cancel endpoint's answer to a run it will not cancel.
type cancelRefusal struct {
	Reason string `json:"reason"`
}

// cancelRun serves POST /api/runs/{run}/cancel: it records that the owner
// cancelled the run, so the run's row leaves the inbox.
//
// The run comes from the path and the owner from the IAP assertion, never from
// the request body. The pull request the run opened is left as it is — not
// closed, commented on or marked — because the surface's token is read-only on
// pull requests (adr/0017).
func (d Deps) cancelRun(w http.ResponseWriter, r *http.Request) {
	owner := requestEmail(r)
	if owner == nil || *owner == "" {
		http.Error(w, "a cancel is made by a signed-in owner", http.StatusUnauthorized)
		return
	}
	if d.Cancels == nil {
		http.Error(w, "cancelling needs a run store", http.StatusServiceUnavailable)
		return
	}
	// A JSON body is what a page other than this one cannot send cross-origin
	// without a preflight, so it keeps a cross-site form post from cancelling a
	// run with the session cookie IAP establishes.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "a cancel is requested with a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cancelTimeout)
	defer cancel()
	run := r.PathValue("run")
	// Merged is answered before cancelled, whatever else the run carries: a merge
	// taken outside the surface, or after a cancel, is the later fact, and the run
	// history draws the run as merged. The store decides it in the write itself, so
	// a merge or a cancel landing meanwhile is seen.
	err := d.Cancels.RecordCancel(ctx, run, *owner, time.Now())
	switch {
	case err == nil:
		d.Logger.Info("ownerCancelledRun", "run", run, "by", *owner)
		writeJSON(w, http.StatusOK, cancelStatus{Status: statusCancelled})
	case errors.Is(err, runner.ErrRecordNotFound):
		http.Error(w, "no such run", http.StatusNotFound)
	case errors.Is(err, store.ErrRunMerged):
		d.Logger.Info("ownerCancelSkipped", "run", run, "reason", statusAlreadyMerged)
		writeJSON(w, http.StatusOK, cancelStatus{Status: statusAlreadyMerged})
	case errors.Is(err, store.ErrRunCancelled):
		d.Logger.Info("ownerCancelSkipped", "run", run, "reason", statusAlreadyCancelled)
		writeJSON(w, http.StatusOK, cancelStatus{Status: statusAlreadyCancelled})
	case errors.Is(err, store.ErrRunNotWaiting):
		// Only a run whose PR waits on the owner can be stopped here; an in-flight
		// run needs its workflow stopped, which no endpoint of this surface does.
		d.Logger.Info("ownerCancelRefused", "run", run, "reason", refusedNotWaiting)
		writeJSON(w, http.StatusConflict, cancelRefusal{Reason: refusedNotWaiting})
	default:
		d.Logger.Error("ownerCancelNotRecorded", "run", run, "err", err.Error())
		http.Error(w, "recording the cancel", http.StatusInternalServerError)
	}
}
