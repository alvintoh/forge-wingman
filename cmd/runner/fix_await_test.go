package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// awaitClock is a clock a wait drives: Now returns its time and Sleep moves it
// forward, so a deadline is measured without sleeping.
type awaitClock struct {
	now   time.Time
	slept time.Duration
}

func (c *awaitClock) Now() time.Time { return c.now }

func (c *awaitClock) Sleep(_ context.Context, d time.Duration) error {
	c.now, c.slept = c.now.Add(d), c.slept+d
	return nil
}

// awaitStart is the time every wait test measures from.
var awaitStart = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

// reviewClient starts a fake repository holding one head's statuses and one pull
// request's comments, and returns the client that reads them.
func reviewClient(t *testing.T, statuses []commitStatusEntry, comments []map[string]any) pullRequests {
	t.Helper()
	f := &fakePulls{statusList: statuses, comments: comments}
	return f.client(f.start(t))
}

// statusOf is one commit status the review posts, created at created.
func statusOf(state, description string, created time.Time) commitStatusEntry {
	return commitStatusEntry{State: state, Description: description, Context: prReviewStatusContext, CreatedAt: created}
}

// reviewCommentOf is the review comment this workflow posts, carrying body.
func reviewCommentOf(body string) map[string]any {
	return map[string]any{"id": 41, "body": body, "user": map[string]string{"login": prReviewCommentAuthor}}
}

// noReview is a read that never reports a verdict, which is a review that never
// posts.
func noReview(context.Context) (fixAwaitOutcome, bool, error) {
	return fixAwaitOutcome{}, false, nil
}

// envFor is a step's environment pointed at the API at url.
func envFor(t *testing.T, url string) func(string) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "output")
	return func(k string) string {
		switch k {
		case "GITHUB_API_URL":
			return url
		case "GH_TOKEN":
			return "gh-token"
		case "GITHUB_REPOSITORY":
			return "o/r"
		case "GITHUB_OUTPUT":
			return output
		}
		return ""
	}
}

// serveEmptyReview answers every read with an empty status list, so a run that
// reaches GitHub finds no review.
func serveEmptyReview(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "[]")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFixAwaitRefusesWhatItCannotRead(t *testing.T) {
	started := time.Now().UTC().Format(time.RFC3339)
	fast := []string{"-timeout", "10ms", "-interval", "1ms", "-started", started}
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"a pull request that is not a number", append([]string{"-pr", "seven", "-sha", headSHA}, fast...), "invalid value"},
		{"no pull request", append([]string{"-sha", headSHA}, fast...), "needs -pr and a -sha"},
		{"a sha that is not a commit", append([]string{"-pr", "7", "-sha", "abc"}, fast...), "needs -pr and a -sha"},
		{"a timeout of zero", []string{"-pr", "7", "-sha", headSHA, "-timeout", "0", "-started", started}, "positive -timeout"},
		{"an interval of zero", []string{"-pr", "7", "-sha", headSHA, "-interval", "0", "-started", started}, "positive -timeout"},
		{"a start that is not a time", []string{"-pr", "7", "-sha", headSHA, "-started", "yesterday"}, "RFC3339"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			err := fixAwait(context.Background(), logger, envFor(t, serveEmptyReview(t)), tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFixAwaitFailsWhenGitHubDoes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	args := []string{"-pr", "7", "-sha", headSHA, "-timeout", "10ms", "-interval", "1ms",
		"-started", time.Now().UTC().Format(time.RFC3339)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := fixAwait(context.Background(), logger, envFor(t, srv.URL), args); err == nil {
		t.Fatal("a failed read was reported as an outcome")
	}
}

func TestReviewAtFailsWhenTheCommentsCannotBeRead(t *testing.T) {
	statuses := []commitStatusEntry{statusOf("failure", findingsStatusDescription+museModel, awaitStart)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/comments") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(statuses)
	}))
	t.Cleanup(srv.Close)
	gh := pullRequests{API: srv.URL, Token: "gh-token", Repo: "o/r"}
	if _, _, err := reviewAt(context.Background(), gh, 7, headSHA); err == nil {
		t.Fatal("a failed comment read was reported as no review yet")
	}
}

func TestReviewAtStopsOnAFindingsStatusWithNoFindings(t *testing.T) {
	body := renderReviewComment(verdict("findings", "", museModel), headSHA, "")
	statuses := []commitStatusEntry{statusOf("failure", findingsStatusDescription+museModel, awaitStart)}
	gh := reviewClient(t, statuses, []map[string]any{reviewCommentOf(body)})
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Decision != fixAwaitStop || got.Reason != fixAwaitReasonError || !strings.Contains(got.Detail, "lists no findings") {
		t.Fatalf("outcome = %+v, ok = %v, want a stop rather than a round with nothing to fix", got, ok)
	}
}

func TestWriteFixAwaitOutputsRefusesADetailThatSpansLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	out := fixAwaitOutcome{Decision: fixAwaitStop, Detail: "first line\nsecond line"}
	if err := writeFixAwaitOutputs(path, out); err == nil {
		t.Fatal("a detail spanning lines was written rather than refused")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a refused write left an output file behind")
	}
}

func TestReviewAtReadsACleanReview(t *testing.T) {
	gh := reviewClient(t, []commitStatusEntry{statusOf("success", cleanStatusDescription+museModel, awaitStart)}, nil)
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Decision != fixAwaitStop || got.Reason != fixAwaitReasonClean || got.Findings != "" {
		t.Fatalf("outcome = %+v, ok = %v, want a stop with no findings", got, ok)
	}
}

func TestReviewAtReadsTheFindingsAtTheHeadTheyName(t *testing.T) {
	findings := "the retry never bounds its attempts\nthe timeout is hard-coded"
	body := renderReviewComment(verdict("findings", findings, museModel), headSHA, "")
	statuses := []commitStatusEntry{statusOf("failure", findingsStatusDescription+museModel+"; see the review comment", awaitStart)}
	gh := reviewClient(t, statuses, []map[string]any{reviewCommentOf(body)})
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Decision != fixAwaitContinue || got.Reason != fixAwaitReasonFindings || got.Findings != findings {
		t.Fatalf("outcome = %+v, ok = %v, want the findings the comment lists", got, ok)
	}
}

func TestReviewAtKeepsPollingForACommentOfAnotherHead(t *testing.T) {
	body := renderReviewComment(verdict("findings", "x is missing", museModel), movedSHA, "")
	statuses := []commitStatusEntry{statusOf("failure", findingsStatusDescription+museModel, awaitStart)}
	gh := reviewClient(t, statuses, []map[string]any{reviewCommentOf(body)})
	if got, ok, err := reviewAt(context.Background(), gh, 7, headSHA); err != nil || ok {
		t.Fatalf("outcome = %+v, ok = %v, err = %v, want the poll to continue", got, ok, err)
	}
}

func TestReviewAtKeepsPollingWhenTheCommentNamesNoHead(t *testing.T) {
	body := renderReviewComment(verdict("clean", "", museModel), movedSHA, "")
	statuses := []commitStatusEntry{statusOf("failure", findingsStatusDescription+museModel, awaitStart)}
	gh := reviewClient(t, statuses, []map[string]any{reviewCommentOf(body)})
	if got, ok, err := reviewAt(context.Background(), gh, 7, headSHA); err != nil || ok {
		t.Fatalf("outcome = %+v, ok = %v, err = %v, want the poll to continue", got, ok, err)
	}
}

func TestReviewAtStopsOnANoVerdict(t *testing.T) {
	gh := reviewClient(t, []commitStatusEntry{statusOf("failure", noVerdictStatusDescription, awaitStart)}, nil)
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Decision != fixAwaitStop || got.Reason != fixAwaitReasonNoVerdict || got.Detail != noVerdictStatusDescription {
		t.Fatalf("outcome = %+v, ok = %v, want a stop for no verdict", got, ok)
	}
}

func TestReviewAtStopsOnAnErrorStatus(t *testing.T) {
	description := skipDescriptions[skipTicketNotFound]
	gh := reviewClient(t, []commitStatusEntry{statusOf("error", description, awaitStart)}, nil)
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Decision != fixAwaitStop || got.Reason != fixAwaitReasonError || got.Detail != description {
		t.Fatalf("outcome = %+v, ok = %v, want a stop carrying the status's reason", got, ok)
	}
}

func TestReviewAtKeepsPollingWithoutAVerdictStatus(t *testing.T) {
	for name, statuses := range map[string][]commitStatusEntry{
		"no status at all":  nil,
		"another context":   {{State: "success", Description: cleanStatusDescription + museModel, Context: "go"}},
		"a pending review":  {{State: "pending", Context: prReviewStatusContext}},
		"an unknown status": {statusOf("success", "Reviewed by someone", awaitStart)},
	} {
		t.Run(name, func(t *testing.T) {
			gh := reviewClient(t, statuses, nil)
			if got, ok, err := reviewAt(context.Background(), gh, 7, headSHA); err != nil || ok {
				t.Fatalf("outcome = %+v, ok = %v, err = %v, want the poll to continue", got, ok, err)
			}
		})
	}
}

func TestReviewAtReadsTheNewestStatus(t *testing.T) {
	body := renderReviewComment(verdict("findings", "x is missing", museModel), headSHA, "")
	newer := statusOf("failure", findingsStatusDescription+museModel, awaitStart.Add(time.Minute))
	stale := statusOf("success", cleanStatusDescription+museModel, awaitStart)
	gh := reviewClient(t, []commitStatusEntry{newer, stale}, []map[string]any{reviewCommentOf(body)})
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Reason != fixAwaitReasonFindings || got.Findings != "x is missing" {
		t.Fatalf("outcome = %+v, ok = %v, want the newer failure, never the stale success", got, ok)
	}
}

func TestReviewAtReadsTheNewestStatusWhateverOrderItArrives(t *testing.T) {
	body := renderReviewComment(verdict("findings", "x is missing", museModel), headSHA, "")
	newer := statusOf("failure", findingsStatusDescription+museModel, awaitStart.Add(time.Minute))
	stale := statusOf("success", cleanStatusDescription+museModel, awaitStart)
	gh := reviewClient(t, []commitStatusEntry{stale, newer}, []map[string]any{reviewCommentOf(body)})
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Reason != fixAwaitReasonFindings {
		t.Fatalf("outcome = %+v, ok = %v, want the status created last", got, ok)
	}
}

func TestReviewAtReadsTheFirstOfTwoStatusesCreatedTogether(t *testing.T) {
	body := renderReviewComment(verdict("findings", "x is missing", museModel), headSHA, "")
	failure := statusOf("failure", findingsStatusDescription+museModel, awaitStart)
	clean := statusOf("success", cleanStatusDescription+museModel, awaitStart)
	gh := reviewClient(t, []commitStatusEntry{failure, clean}, []map[string]any{reviewCommentOf(body)})
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Reason != fixAwaitReasonFindings {
		t.Fatalf("outcome = %+v, ok = %v, want the first answer of the two", got, ok)
	}
}

func TestReviewAtStopsOnAnUnreadableComment(t *testing.T) {
	body := prReviewMarker + "\n### Wingman review: findings\n\nAt `" + headSHA[:12] + "`:\n\n```text\nthe fence never closes\n"
	statuses := []commitStatusEntry{statusOf("failure", findingsStatusDescription+museModel, awaitStart)}
	gh := reviewClient(t, statuses, []map[string]any{reviewCommentOf(body)})
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Decision != fixAwaitStop || got.Reason != fixAwaitReasonError || !strings.Contains(got.Detail, "unclosed") {
		t.Fatalf("outcome = %+v, ok = %v, want a stop naming the lost block", got, ok)
	}
}

func TestReviewAtIgnoresACommentTheWorkflowDidNotPost(t *testing.T) {
	body := renderReviewComment(verdict("findings", "x is missing", museModel), headSHA, "")
	forged := map[string]any{"id": 42, "body": body, "user": map[string]string{"login": "mallory"}}
	statuses := []commitStatusEntry{statusOf("failure", findingsStatusDescription+museModel, awaitStart)}
	gh := reviewClient(t, statuses, []map[string]any{forged})
	if got, ok, err := reviewAt(context.Background(), gh, 7, headSHA); err != nil || ok {
		t.Fatalf("outcome = %+v, ok = %v, err = %v, want the poll to continue", got, ok, err)
	}
}

func TestAwaitReviewReturnsTheVerdictItReads(t *testing.T) {
	clock := &awaitClock{now: awaitStart}
	polls := 0
	read := func(context.Context) (fixAwaitOutcome, bool, error) {
		polls++
		if polls < 3 {
			return fixAwaitOutcome{}, false, nil
		}
		return fixAwaitOutcome{Decision: fixAwaitContinue, Reason: fixAwaitReasonFindings, Findings: "x is missing"}, true, nil
	}
	w := fixAwaitWait{Timeout: 5 * time.Minute, Interval: 30 * time.Second, Started: awaitStart}
	got, err := awaitReview(context.Background(), read, w, clock.Now, clock.Sleep)
	if err != nil {
		t.Fatal(err)
	}
	if got.Findings != "x is missing" || polls != 3 || clock.slept != time.Minute {
		t.Fatalf("outcome = %+v after %d polls and %s, want the verdict on the third poll", got, polls, clock.slept)
	}
}

func TestAwaitReviewEndsAtTheTimeoutWithAStopReason(t *testing.T) {
	clock := &awaitClock{now: awaitStart}
	w := fixAwaitWait{Timeout: time.Minute, Interval: 20 * time.Second, Started: awaitStart}
	got, err := awaitReview(context.Background(), noReview, w, clock.Now, clock.Sleep)
	if err != nil {
		t.Fatalf("a review that never posted returned an error: %v", err)
	}
	if got.Decision != fixAwaitStop || got.Reason != fixAwaitReasonTimeout {
		t.Fatalf("outcome = %+v, want a stop for the timeout", got)
	}
	if clock.slept != time.Minute {
		t.Fatalf("the wait lasted %s, want its own minute", clock.slept)
	}
}

func TestAwaitReviewIsBoundedByTheRunsBudget(t *testing.T) {
	clock := &awaitClock{now: awaitStart}
	w := fixAwaitWait{Timeout: 15 * time.Minute, Interval: 20 * time.Second, Started: awaitStart.Add(-88 * time.Minute)}
	got, err := awaitReview(context.Background(), noReview, w, clock.Now, clock.Sleep)
	if err != nil {
		t.Fatal(err)
	}
	if clock.slept != 2*time.Minute {
		t.Fatalf("the wait lasted %s, want the run's remaining 2 minutes", clock.slept)
	}
	if !strings.Contains(got.Detail, "shared budget") {
		t.Fatalf("detail = %q, want it to name the run's budget", got.Detail)
	}
}

func TestAwaitReviewEndsAtOnceWhenTheBudgetIsSpent(t *testing.T) {
	clock := &awaitClock{now: awaitStart}
	w := fixAwaitWait{Timeout: 15 * time.Minute, Interval: 20 * time.Second, Started: awaitStart.Add(-91 * time.Minute)}
	got, err := awaitReview(context.Background(), noReview, w, clock.Now, clock.Sleep)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != fixAwaitReasonTimeout || clock.slept != 0 {
		t.Fatalf("outcome = %+v after %s, want an immediate timeout stop", got, clock.slept)
	}
}

func TestAwaitReviewReturnsTheContextError(t *testing.T) {
	clock := &awaitClock{now: awaitStart}
	w := fixAwaitWait{Timeout: time.Minute, Interval: 20 * time.Second, Started: awaitStart}
	cancelled := func(context.Context, time.Duration) error { return context.Canceled }
	if _, err := awaitReview(context.Background(), noReview, w, clock.Now, cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's cancellation", err)
	}
}

func TestAwaitReviewReturnsAReadError(t *testing.T) {
	clock := &awaitClock{now: awaitStart}
	failing := func(context.Context) (fixAwaitOutcome, bool, error) {
		return fixAwaitOutcome{}, false, errors.New("github is down")
	}
	w := fixAwaitWait{Timeout: time.Minute, Interval: 20 * time.Second, Started: awaitStart}
	if _, err := awaitReview(context.Background(), failing, w, clock.Now, clock.Sleep); err == nil {
		t.Fatal("a failed read was swallowed")
	}
}

func TestFixAwaitWritesTheFindingsItParsed(t *testing.T) {
	findings := "the retry never bounds its attempts"
	body := renderReviewComment(verdict("findings", findings, museModel), headSHA, "")
	statuses := []commitStatusEntry{statusOf("failure", findingsStatusDescription+museModel+"; see the review comment", awaitStart)}
	f := &fakePulls{statusList: statuses, comments: []map[string]any{reviewCommentOf(body)}}
	env := f.start(t)
	args := []string{"-pr", "7", "-sha", headSHA, "-started", time.Now().UTC().Format(time.RFC3339)}
	if err := fixAwait(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), env, args); err != nil {
		t.Fatal(err)
	}
	output := env("GITHUB_OUTPUT")
	if got := outputValue(t, output, "decision"); got != fixAwaitContinue {
		t.Fatalf("decision = %q, want the loop to continue", got)
	}
	if got := outputValue(t, output, "findings"); got != findings {
		t.Fatalf("findings = %q, want %q", got, findings)
	}
	if got := outputValue(t, output, "findings_omitted"); got != "0" {
		t.Fatalf("findings_omitted = %q, want 0", got)
	}
}

func TestFixAwaitReportsACleanReviewWithNoFindings(t *testing.T) {
	statuses := []commitStatusEntry{statusOf("success", cleanStatusDescription+museModel, awaitStart)}
	f := &fakePulls{statusList: statuses}
	env := f.start(t)
	args := []string{"-pr", "7", "-sha", headSHA, "-started", time.Now().UTC().Format(time.RFC3339)}
	if err := fixAwait(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), env, args); err != nil {
		t.Fatal(err)
	}
	output := env("GITHUB_OUTPUT")
	if got := outputValue(t, output, "decision"); got != fixAwaitStop {
		t.Fatalf("decision = %q, want the loop to stop", got)
	}
	if got := outputValue(t, output, "reason"); got != fixAwaitReasonClean {
		t.Fatalf("reason = %q, want a clean review", got)
	}
	if got := outputValue(t, output, "findings"); got != "" {
		t.Fatalf("findings = %q, want none", got)
	}
}

func TestFixAwaitEndsAtTheTimeoutWithoutAnError(t *testing.T) {
	f := &fakePulls{}
	env := f.start(t)
	args := []string{"-pr", "7", "-sha", headSHA, "-timeout", "5ms", "-interval", "1ms", "-started", time.Now().UTC().Format(time.RFC3339)}
	if err := fixAwait(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), env, args); err != nil {
		t.Fatalf("a review that never posted returned an error: %v", err)
	}
	if got := outputValue(t, env("GITHUB_OUTPUT"), "reason"); got != fixAwaitReasonTimeout {
		t.Fatalf("reason = %q, want the timeout", got)
	}
}

// outputValue is what a step wrote to $GITHUB_OUTPUT under key, single-line or
// between the delimiters a multiline output uses.
func outputValue(t *testing.T, path, key string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
		delim, ok := strings.CutPrefix(line, key+"<<")
		if !ok {
			continue
		}
		var out []string
		for _, l := range lines[i+1:] {
			if l == delim {
				return strings.Join(out, "\n")
			}
			out = append(out, l)
		}
	}
	t.Fatalf("no %s in %q", key, raw)
	return ""
}
