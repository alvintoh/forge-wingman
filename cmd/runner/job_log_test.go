package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// proxyReadLog is a Go module proxy read error as the failing step prints one,
// which is what the transient classifier names off a job's own log.
const proxyReadLog = "go mod download: reading https://proxy.golang.org/github.com/x/y/@v/v1.0.0.zip: 502 Bad Gateway"

// apiJob is one job as the run's jobs endpoint answers it. The tests spell the
// wire shape out rather than re-encoding the type the reader decodes into: a
// renamed JSON tag there would otherwise move both sides together and leave the
// reader seeing no failed job against the API itself.
func apiJob(id int64, name, conclusion string) map[string]any {
	return map[string]any{"id": id, "name": name, "conclusion": conclusion}
}

// fakeRun serves one run's job list and each job's own log as GitHub's API does:
// the job list at the run's jobs endpoint, and every log behind a redirect to a
// signed URL the body is served from.
type fakeRun struct {
	jobs []map[string]any
	logs map[int64]string
	// refused is the status a job's log endpoint answers with in place of its log.
	refused map[int64]int
	// listRefused is the status the jobs list answers with in place of its page.
	listRefused int
}

func (f *fakeRun) start(t *testing.T) func(string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gh-token" {
			t.Errorf("%s %s sent Authorization %q, want the run's token", r.Method, r.URL.Path, got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runs/7/jobs":
			if f.listRefused != 0 {
				w.WriteHeader(f.listRefused)
				return
			}
			if got := r.URL.Query().Get("filter"); got != "latest" {
				t.Errorf("the jobs list asked for filter %q, want latest so a rerun reads this attempt's jobs", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": f.jobs})
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/actions/jobs/"):
			id, ok := logJobID(r.URL.Path)
			if !ok {
				t.Errorf("job log path %q names no job", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if code, refused := f.refused[id]; refused {
				w.WriteHeader(code)
				return
			}
			http.Redirect(w, r, "/signed/"+strconv.FormatInt(id, 10), http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/signed/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/signed/"), 10, 64)
			_, _ = io.WriteString(w, f.logs[id])
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"GITHUB_API_URL": srv.URL, "GH_TOKEN": "gh-token", "GITHUB_REPOSITORY": "o/r", "GITHUB_RUN_ID": "7"}
	return func(k string) string { return env[k] }
}

// client is the API client the record job reads the run's jobs with.
func (f *fakeRun) client(getenv func(string) string) pullRequests {
	return pullRequests{API: getenv("GITHUB_API_URL"), Token: getenv("GH_TOKEN"), Repo: getenv("GITHUB_REPOSITORY")}
}

// logJobID is the job a job-log path names, and whether the path names one. It
// reports rather than fails, since its caller runs on the server's goroutine.
func logJobID(path string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/repos/o/r/actions/jobs/"), "/logs"), 10, 64)
	return id, err == nil
}

func TestFailedJobLogIsEveryFailedJobsOwnLog(t *testing.T) {
	check, pr := int64(11), int64(13)
	f := &fakeRun{
		jobs: []map[string]any{
			apiJob(check, "check ABC-1", "failure"),
			apiJob(12, "model ABC-1", "success"),
			apiJob(pr, "pr ABC-1", "failure"),
		},
		logs: map[int64]string{
			check: "step: go test\n" + proxyReadLog,
			pr:    "step: push\nfatal: unable to access 'https://github.com/o/r.git/': Could not resolve host: github.com",
		},
	}
	got, err := f.client(f.start(t)).failedJobLog(context.Background(), 7, maxJobLogTailBytes)
	if err != nil {
		t.Fatal(err)
	}
	if want := f.logs[check] + "\n" + f.logs[pr]; got != want {
		t.Fatalf("log = %q, want each failed job's own log joined: %q", got, want)
	}
}

// TestFailedJobLogIsEmptyForARunWhoseJobsAllSucceeded gives the successful job a
// log of its own, so reading it would be visible.
func TestFailedJobLogIsEmptyForARunWhoseJobsAllSucceeded(t *testing.T) {
	f := &fakeRun{jobs: []map[string]any{apiJob(11, "check ABC-1", "success")},
		logs: map[int64]string{11: proxyReadLog}}
	got, err := f.client(f.start(t)).failedJobLog(context.Background(), 7, maxJobLogTailBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("log = %q, want nothing read for a run with no failed job", got)
	}
}

func TestFailedJobLogIsEmptyForTheRecordJobsOwnLog(t *testing.T) {
	f := &fakeRun{jobs: []map[string]any{apiJob(21, "record ABC-1", "failure")},
		logs: map[int64]string{21: proxyReadLog}}
	got, err := f.client(f.start(t)).failedJobLog(context.Background(), 7, maxJobLogTailBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("log = %q, want the record job's own log left out", got)
	}
}

// TestFailedJobLogFailsWhenTheJobsListCannotBeRead keeps the reader reporting a
// jobs list it could not see — the answer a record job with no actions:read gets
// — rather than reading that as a run with no failed job.
func TestFailedJobLogFailsWhenTheJobsListCannotBeRead(t *testing.T) {
	f := &fakeRun{listRefused: http.StatusForbidden}
	if _, err := f.client(f.start(t)).failedJobLog(context.Background(), 7, maxJobLogTailBytes); err == nil {
		t.Fatal("a jobs list answered 403 read without an error")
	}
}

func TestFailedJobLogFailsWhenAJobsLogCannotBeRead(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			f := &fakeRun{
				jobs:    []map[string]any{apiJob(31, "check ABC-1", "failure")},
				refused: map[int64]int{31: code},
			}
			if _, err := f.client(f.start(t)).failedJobLog(context.Background(), 7, maxJobLogTailBytes); err == nil {
				t.Fatalf("a job log answered %d read without an error", code)
			}
		})
	}
}

func TestFailedJobLogKeepsABoundedTailOfALogItCut(t *testing.T) {
	// The cut lands inside a character, so the tail holds its bytes' worth of the
	// log's end and none of the character it split.
	f := &fakeRun{jobs: []map[string]any{apiJob(41, "check ABC-1", "failure")},
		logs: map[int64]string{41: strings.Repeat("a", 19) + "é" + strings.Repeat("b", 19)}}
	got, err := f.client(f.start(t)).failedJobLog(context.Background(), 7, 20)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("b", 19); got != want {
		t.Fatalf("tail = %q, want the log's last 19 bytes as valid UTF-8: %q", got, want)
	}
}

func TestFailedJobLogLeavesOutALogTooLongToReadWhole(t *testing.T) {
	// The log runs past the read, and the proxy error sits where the cut lands:
	// classifying from the middle would name a fault that did not stop the job.
	f := &fakeRun{jobs: []map[string]any{apiJob(51, "check ABC-1", "failure")},
		logs: map[int64]string{51: strings.Repeat("y", maxJobLogBytes-1000) + proxyReadLog + strings.Repeat("z", 1<<20)}}
	got, err := f.client(f.start(t)).failedJobLog(context.Background(), 7, maxJobLogTailBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("log = %q, want a log too long to hold its own tail left out", got)
	}
}

// TestFailedJobLogForRunReadsNothingWithoutTheRunItNeeds covers a workflow that
// hands the record job no token, and one whose run id is not a run's: neither
// reaches the API, and both leave the log empty rather than failing the record.
func TestFailedJobLogForRunReadsNothingWithoutTheRunItNeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("read %s %s without a token and a run to read", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	for name, broken := range map[string]map[string]string{
		"no token":                     {"GH_TOKEN": ""},
		"a run id that is not a run's": {"GITHUB_RUN_ID": "not-a-run"},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"GITHUB_API_URL": srv.URL, "GH_TOKEN": "gh-token", "GITHUB_REPOSITORY": "o/r", "GITHUB_RUN_ID": "7"}
			for k, v := range broken {
				env[k] = v
			}
			if got := failedJobLogForRun(context.Background(), quietLogger(), func(k string) string { return env[k] }); got != "" {
				t.Fatalf("log = %q, want nothing read without a token and a run to read", got)
			}
		})
	}
}

func TestFailedJobLogForRunIsTheFailingJobsOwnLog(t *testing.T) {
	f := &fakeRun{jobs: []map[string]any{apiJob(11, "check ABC-1", "failure")},
		logs: map[int64]string{11: proxyReadLog}}
	if got := failedJobLogForRun(context.Background(), quietLogger(), f.start(t)); got != proxyReadLog {
		t.Fatalf("log = %q, want the failing job's own log %q", got, proxyReadLog)
	}
}

// TestFailedJobLogForRunLeavesAnUnreadableLogEmpty is the record job's half of
// the rule that the words which classify a run can never fail its record: one
// unreadable log leaves the readable ones' words unread too, rather than half
// the run classified.
func TestFailedJobLogForRunLeavesAnUnreadableLogEmpty(t *testing.T) {
	f := &fakeRun{
		jobs:    []map[string]any{apiJob(11, "check ABC-1", "failure"), apiJob(12, "pr ABC-1", "failure")},
		logs:    map[int64]string{11: proxyReadLog},
		refused: map[int64]int{12: http.StatusForbidden},
	}
	if got := failedJobLogForRun(context.Background(), quietLogger(), f.start(t)); got != "" {
		t.Fatalf("log = %q, want an unreadable log left empty", got)
	}
}
