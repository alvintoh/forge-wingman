package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// maxJobLogTailBytes is how much of a failing job's own log is kept: a step
	// prints what it stopped on at the log's end, and the classifier only looks
	// for markers in it.
	maxJobLogTailBytes = 64 << 10
	// maxJobLogBytes is how much of one job's log is read before that tail is
	// taken. A longer log is left out rather than classified from its middle:
	// its own tail is out of reach, and the words of some earlier, recovered
	// failure in its middle name a fault that did not stop the job.
	maxJobLogBytes = 16 << 20
	// maxJobListBytes bounds one page of a run's jobs.
	maxJobListBytes = 1 << 20
	// jobsPerPage is what the jobs API serves in one page, so a run of this many
	// jobs or fewer is one request.
	jobsPerPage = 100
	// recordJobPrefix is how the record job's own name begins, the run id
	// following the space. Its log is where this read reports itself, so it is
	// left out; the plan stage's job is named "record-plan-stage …", which no
	// space follows.
	recordJobPrefix = "record "
	// jobLogRequestTimeout bounds one job-log read, well inside the record job's
	// own ten minutes.
	jobLogRequestTimeout = 30 * time.Second
)

// jobs is a run's jobs as GitHub's API lists them.
type jobs struct {
	Jobs []job `json:"jobs"`
}

// job is one job of a run: which job it is, what it is called, and how it ended.
type job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// failedJobLog is the tail of each failed job's own log, joined by a newline in
// the order GitHub lists the jobs: the words a job stopped on, which the run's
// stop detail and loop detail only summarise. Two logs are left out: the record
// job's own, which is where this read reports itself, and any log too long to
// read whole, of which only its middle would be at hand. A run none of whose
// jobs failed is "".
func (g pullRequests) failedJobLog(ctx context.Context, runID int64, limit int) (string, error) {
	var listed jobs
	path := fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?filter=latest&per_page=%d", g.Repo, runID, jobsPerPage)
	if err := g.call(ctx, http.MethodGet, path, "", nil, maxJobListBytes, &listed); err != nil {
		return "", err
	}
	var tails []string
	for _, j := range listed.Jobs {
		if j.Conclusion != "failure" || strings.HasPrefix(j.Name, recordJobPrefix) {
			continue
		}
		// The extra byte is what tells a log that ends inside this read from one
		// cut at it: only a longer log loses its own tail.
		var raw []byte
		if err := g.call(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", g.Repo, j.ID), "", nil, maxJobLogBytes+1, &raw); err != nil {
			return "", err
		}
		if len(raw) > maxJobLogBytes {
			continue
		}
		tails = append(tails, logTail(raw, limit))
	}
	return strings.Join(tails, "\n"), nil
}

// logTail is the last limit bytes of a job's log, with any byte an earlier cut
// split left over dropped, so the tail is valid UTF-8 on its own.
func logTail(log []byte, limit int) string {
	if len(log) > limit {
		log = log[len(log)-limit:]
	}
	return strings.ToValidUTF8(string(log), "")
}

// failedJobLogForRun is the failing jobs' own log tails for the run this record
// job finalizes. A workflow that handed it no token or repository to read them
// with, and a log it cannot read, is reported and leaves the log empty: the
// record never fails because the words that classify the run could not be read.
func failedJobLogForRun(ctx context.Context, logger *slog.Logger, getenv func(string) string) string {
	token, repo, runID := getenv("GH_TOKEN"), getenv("GITHUB_REPOSITORY"), getenv("GITHUB_RUN_ID")
	if token == "" || repo == "" || runID == "" {
		logger.Warn("failedJobLogUnread", "reason", "not-configured")
		return ""
	}
	run, err := strconv.ParseInt(runID, 10, 64)
	if err != nil {
		logger.Warn("failedJobLogUnread", "reason", "run-id-unreadable", "run", runID)
		return ""
	}
	gh := pullRequests{API: getenv("GITHUB_API_URL"), Token: token, Repo: repo, Client: &http.Client{Timeout: jobLogRequestTimeout}}
	log, err := gh.failedJobLog(ctx, run, maxJobLogTailBytes)
	if err != nil {
		logger.Warn("failedJobLogUnread", "reason", "read-failed", "run", runID, "err", err.Error())
		return ""
	}
	return log
}
