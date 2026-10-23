package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// MergeRuns reads the run a merge acts on and records the merge on it.
// RunRepository is the repository the dispatcher queued the run for, which the
// PR the run's job recorded must be in.
type MergeRuns interface {
	GetRecord(ctx context.Context, id string) (runner.Record, error)
	RunRepository(ctx context.Context, id string) (string, error)
	RecordMerge(ctx context.Context, id, sha string, at time.Time) error
}

// PRMerger reads where a pull request stands and merges it, under the surface's
// own GitHub token.
type PRMerger interface {
	PullState(ctx context.Context, repo string, number int) (dispatcher.PullStatus, error)
	ChecksGreen(ctx context.Context, repo, sha string) (bool, error)
	Merge(ctx context.Context, repo string, number int, sha string) (string, error)
}

// The reasons a merge is refused, each answered with status 409.
const (
	refusedChecksNotGreen = "checks-not-green"
	refusedDraft          = "draft"
	refusedGitHub         = "github-refused"
	refusedHeadMoved      = "head-moved"
	refusedNoPullRequest  = "no-pull-request"
	refusedNotOpen        = "not-open"
	refusedUnconfirmed    = "unconfirmed-decision"
)

// mergeStatus is the merge endpoint's answer to a merge it made or found made.
type mergeStatus struct {
	Status string `json:"status"`
	SHA    string `json:"sha,omitempty"`
}

// mergeRefusal is the merge endpoint's answer to a merge it will not make.
type mergeRefusal struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// mergeRun squash-merges the PR the run's row names, as the owner.
//
// The body names only the head the owner saw; the repository and PR come from
// the row. GitHub's own 405 and 409 reasons are passed through as a refusal.
func (d Deps) mergeRun(w http.ResponseWriter, r *http.Request) {
	if d.Merger == nil || d.Runs == nil {
		http.Error(w, "merging needs a run store and the merge token", http.StatusServiceUnavailable)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "a merge is requested with a JSON body", http.StatusUnsupportedMediaType)
		return
	}
	var body struct {
		ExpectedHeadSHA string `json:"expected_head_sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ExpectedHeadSHA == "" {
		http.Error(w, "the request body is not JSON naming the head commit it saw", http.StatusBadRequest)
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
		d.Logger.Error("ownerMergeRunNotRead", "run", run, "err", err.Error())
		http.Error(w, "reading the run", http.StatusInternalServerError)
		return
	}
	repo, number, err := parsePullURL(rec.PRURL)
	if err != nil {
		d.refuseMerge(w, run, refusedNoPullRequest, "")
		return
	}
	queuedFor, err := d.Runs.RunRepository(ctx, run)
	if err != nil {
		d.Logger.Error("ownerMergeRunNotRead", "run", run, "err", err.Error())
		http.Error(w, "reading the run", http.StatusInternalServerError)
		return
	}
	if !strings.EqualFold(repo, queuedFor) {
		d.refuseMerge(w, run, refusedNoPullRequest, "")
		return
	}
	state, err := d.Merger.PullState(ctx, repo, number)
	if err != nil {
		d.mergeFailed(w, run, repo, number, "reading the pull request", err)
		return
	}
	switch {
	case state.Merged:
		writeJSON(w, http.StatusOK, mergeStatus{Status: "already-merged"})
		return
	case !state.Open:
		d.refuseMerge(w, run, refusedNotOpen, "")
		return
	case state.Draft:
		d.refuseMerge(w, run, refusedDraft, "")
		return
	case state.HeadSHA != body.ExpectedHeadSHA:
		d.refuseMerge(w, run, refusedHeadMoved, "")
		return
	case rec.WaitReason == runner.WaitUnconfirmedDecision:
		d.refuseMerge(w, run, refusedUnconfirmed, "")
		return
	}
	green, err := d.Merger.ChecksGreen(ctx, repo, state.HeadSHA)
	if err != nil {
		d.mergeFailed(w, run, repo, number, "reading the checks", err)
		return
	}
	if !green {
		d.refuseMerge(w, run, refusedChecksNotGreen, "")
		return
	}
	sha, err := d.Merger.Merge(ctx, repo, number, state.HeadSHA)
	var refused *dispatcher.APIError
	if errors.As(err, &refused) && (refused.Status == http.StatusMethodNotAllowed || refused.Status == http.StatusConflict) {
		d.refuseMerge(w, run, refusedGitHub, refused.Reason)
		return
	}
	if err != nil {
		d.mergeFailed(w, run, repo, number, "merging the pull request", err)
		return
	}
	d.Logger.Info("ownerMerged", "run", run, "repo", repo, "number", number, "sha", sha)
	if err := d.Runs.RecordMerge(ctx, run, sha, time.Now()); err != nil {
		d.Logger.Error("ownerMergeNotRecorded", "run", run, "repo", repo, "number", number, "sha", sha, "err", err.Error())
	}
	writeJSON(w, http.StatusOK, mergeStatus{Status: "merged", SHA: sha})
}

// refuseMerge answers a merge it will not make with reason, and logs it.
func (d Deps) refuseMerge(w http.ResponseWriter, run, reason, detail string) {
	d.Logger.Info("ownerMergeRefused", "run", run, "reason", reason)
	writeJSON(w, http.StatusConflict, mergeRefusal{Reason: reason, Detail: detail})
}

// mergeFailed answers a merge that GitHub could not be asked about with a 502.
func (d Deps) mergeFailed(w http.ResponseWriter, run, repo string, number int, doing string, err error) {
	d.Logger.Error("ownerMergeFailed", "run", run, "repo", repo, "number", number, "doing", doing, "err", err.Error())
	http.Error(w, doing+" failed", http.StatusBadGateway)
}

// parsePullURL reads the repository and number out of a pull request page URL.
func parsePullURL(raw string) (repo string, number int, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" {
		return "", 0, fmt.Errorf("%q is not a github.com pull request URL", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return "", 0, fmt.Errorf("%q is not a github.com pull request URL", raw)
	}
	number, err = strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("%q names no pull request number", raw)
	}
	repo = parts[0] + "/" + parts[1]
	if !store.ValidRepository(repo) || slices.Contains(parts[:2], ".") || slices.Contains(parts[:2], "..") {
		return "", 0, fmt.Errorf("%q names no valid repository", raw)
	}
	return repo, number, nil
}
