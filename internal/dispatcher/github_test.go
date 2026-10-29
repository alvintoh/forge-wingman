package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGitHubStartsTheRunWorkflowInTheTicketRepository(t *testing.T) {
	var got struct {
		path   string
		auth   string
		accept string
		ver    string
		body   map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth, got.accept = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept")
		got.ver = r.Header.Get("X-GitHub-Api-Version")
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		// The endpoint answered 204 when it was added and answers 200 with the
		// run's URLs now; both mean the run started.
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"workflow_run_id":42,"run_url":"https://github.com/o/r/actions/runs/42"}`)
	}))
	defer srv.Close()

	claim := Claim{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1, Attempt: 2}
	if err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Workflow: "run.yml",
		Branch: "main", Client: srv.Client()}).Dispatch(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if got.path != "/repos/octo/scratch/actions/workflows/run.yml/dispatches" {
		t.Fatalf("path = %q", got.path)
	}
	if got.auth != "Bearer gh-token" || got.accept != "application/vnd.github+json" || got.ver == "" {
		t.Fatalf("headers = %+v", got)
	}
	// The model input is left to run.yml's own default rather than restated here.
	if got.body["ref"] != "main" {
		t.Fatalf("ref = %v", got.body["ref"])
	}
	inputs, _ := got.body["inputs"].(map[string]any)
	if len(inputs) != 2 || inputs["run_id"] != "FRG-18" || inputs["attempt"] != "2" {
		t.Fatalf("inputs = %v, want the run id and attempt=2", got.body["inputs"])
	}
}

func TestGitHubReportsARefusalOnOneLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "{\n  \"message\": \"Bad credentials\"\n}")
	}))
	defer srv.Close()

	err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Workflow: "run.yml", Branch: "main",
		Client: srv.Client()}).Dispatch(context.Background(), Claim{RunID: "FRG-18", Repo: "octo/scratch"})
	if err == nil {
		t.Fatal("dispatched with a token GitHub refused")
	}
	for _, want := range []string{"FRG-18", "octo/scratch", "401", "Bad credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("err = %q, want one line", err)
	}
}

func TestGitHubReadsARepositorysVisibility(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want bool
	}{
		{"a private repository", `{"private":true}`, true},
		{"a public repository", `{"private":false}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			got, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
				Private(context.Background(), "octo/scratch")
			if err != nil {
				t.Fatal(err)
			}
			if path != "/repos/octo/scratch" {
				t.Fatalf("path = %q", path)
			}
			if got != tt.want {
				t.Fatalf("private = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitHubReportsARefusalReadingVisibility(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()
	if _, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		Private(context.Background(), "octo/scratch"); err == nil {
		t.Fatal("Private succeeded against a 404")
	}
}

func TestSnippetTruncatesAndFlattens(t *testing.T) {
	if got := snippet([]byte("  a\r\nb  ")); got != "a b" {
		t.Fatalf("snippet = %q", got)
	}
	long := strings.Repeat("x", maxErrorBytes+10)
	if got := snippet([]byte(long)); len([]rune(got)) != maxErrorBytes+1 {
		t.Fatalf("snippet is %d runes, want the cap plus its ellipsis", len([]rune(got)))
	}
}

func TestGitHubCountsOnlyTheRunnersOpenPullRequests(t *testing.T) {
	var got struct{ path, state string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.state = r.URL.Path, r.URL.Query().Get("state")
		_, _ = io.WriteString(w, `[
			{"head":{"ref":"wingman/frg-18-1-1"}},
			{"head":{"ref":"feature/wingman/x"}},
			{"head":{"ref":"wingman/frg-19-1-1"}}
		]`)
	}))
	defer srv.Close()
	n, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch")
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/repos/octo/scratch/pulls" || got.state != "open" || n != 2 {
		t.Fatalf("path %q, state %q, count %d", got.path, got.state, n)
	}
}

func TestGitHubReportsARefusalCountingPullRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"Resource not accessible by personal access token"}`)
	}))
	defer srv.Close()
	_, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the refusal", err)
	}
}

func TestGitHubPullOpenReadsThePullRequestState(t *testing.T) {
	for state, want := range map[string]bool{"open": true, "closed": false} {
		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			_, _ = io.WriteString(w, `{"state":"`+state+`"}`)
		}))
		open, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
			PullOpen(context.Background(), "octo/scratch", 140)
		srv.Close()
		if err != nil || open != want || path != "/repos/octo/scratch/pulls/140" {
			t.Fatalf("state %q: open %v, path %q, err %v, want open %v", state, open, path, err, want)
		}
	}
}

func TestGitHubPullOpenReportsARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if open, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		PullOpen(context.Background(), "octo/scratch", 140); err == nil || open {
		t.Fatalf("open %v, err %v, want the refusal reported and the PR not read as open", open, err)
	}
}

func TestRunWorkflowSerialisesRunsByRunID(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^concurrency:\n\s+group:.*\$\{\{ inputs\.run_id \}\}`).Match(yml) {
		t.Fatal("run.yml's concurrency group does not name inputs.run_id, so two runs would share one")
	}
}

// pullsPageBody is one page of a pull-request listing: n PRs, the first agent
// of them built on the runner's branches.
func pullsPageBody(n, agent int) string {
	var items []string
	for i := range n {
		ref := "feature/x"
		if i < agent {
			ref = "wingman/x"
		}
		items = append(items, `{"head":{"ref":"`+ref+`"}}`)
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestGitHubCountsAgentPullRequestsAcrossPages(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		if r.URL.Query().Get("page") == "1" {
			_, _ = io.WriteString(w, pullsPageBody(pullsPerPage, 1))
			return
		}
		_, _ = io.WriteString(w, pullsPageBody(3, 2))
	}))
	defer srv.Close()
	n, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(pages) != 2 {
		t.Fatalf("count %d over pages %v, want 3 over two", n, pages)
	}
}

func TestGitHubFailsWhenThePullRequestsOutrunTheirPages(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pages++
		_, _ = io.WriteString(w, pullsPageBody(pullsPerPage, 1))
	}))
	defer srv.Close()
	n, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch")
	if err == nil || n != 0 {
		t.Fatalf("count %d, err %v, want no count that may be short", n, err)
	}
	if pages != maxPullsPages {
		t.Fatalf("read %d pages, want %d", pages, maxPullsPages)
	}
}

func TestGitHubReadsTheLastPageItWalks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == strconv.Itoa(maxPullsPages) {
			_, _ = io.WriteString(w, pullsPageBody(3, 2))
			return
		}
		_, _ = io.WriteString(w, pullsPageBody(pullsPerPage, 0))
	}))
	defer srv.Close()
	n, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch")
	if err != nil || n != 2 {
		t.Fatalf("count %d, err %v, want 2 from the last permitted page", n, err)
	}
}

func TestGitHubStopsAtAPageThatIsNotFull(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pages++
		_, _ = io.WriteString(w, pullsPageBody(pullsPerPage-1, pullsPerPage-1))
	}))
	defer srv.Close()
	n, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch")
	if err != nil || n != pullsPerPage-1 || pages != 1 {
		t.Fatalf("count %d over %d pages, err %v, want %d over one", n, pages, err, pullsPerPage-1)
	}
}

func TestGitHubReportsNoCountWhenALaterPageFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = io.WriteString(w, pullsPageBody(pullsPerPage, pullsPerPage))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	n, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch")
	if err == nil || n != 0 {
		t.Fatalf("count %d, err %v, want no partial count", n, err)
	}
}

func TestGitHubRefusesAPullRequestPageLargerThanItsBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "["+strings.Repeat(" ", maxPullsResponseBytes)+"]")
	}))
	defer srv.Close()
	if _, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch"); err == nil {
		t.Fatal("an oversized page was read whole")
	}
}

func TestGitHubDispatchesTheStageAPlanStageClaimNames(t *testing.T) {
	var inputs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		inputs, _ = body["inputs"].(map[string]any)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	claim := Claim{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1, Stage: StagePlan}
	if err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Workflow: "run.yml",
		Branch: "main", Client: srv.Client()}).Dispatch(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 3 || inputs["stage"] != StagePlan {
		t.Fatalf("inputs = %v, want the run id, attempt and stage=%s", inputs, StagePlan)
	}
}

func TestGitHubSendsNoStageForABuildStageClaim(t *testing.T) {
	var inputs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		inputs, _ = body["inputs"].(map[string]any)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	claim := Claim{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1, Stage: StageBuild}
	if err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Workflow: "run.yml",
		Branch: "main", Client: srv.Client()}).Dispatch(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 2 || inputs["run_id"] != "FRG-18" || inputs["attempt"] != "0" {
		t.Fatalf("inputs = %v, want the run id and attempt 0, no stage: run.yml's own default reads a missing stage as build", inputs)
	}
}

func TestGitHubMeasuresARunsLinesFromTheFixtureCommits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octo/scratch/pulls/7/commits":
			_, _ = io.WriteString(w, `[
				{"sha":"aaaa","author":{"login":"github-actions[bot]"},"commit":{"author":{"name":"github-actions[bot]","email":"41898282+github-actions[bot]@users.noreply.github.com"}}},
				{"sha":"bbbb","author":{"login":"alvintoh"},"commit":{"author":{"name":"github-actions[bot]","email":"41898282+github-actions[bot]@users.noreply.github.com"}}},
				{"sha":"cccc","author":null,"commit":{"author":{"name":"github-actions[bot]","email":"41898282+github-actions[bot]@users.noreply.github.com"}}}
			]`)
		case "/repos/octo/scratch/commits/aaaa":
			_, _ = io.WriteString(w, `{"stats":{"additions":120,"deletions":8,"total":128}}`)
		case "/repos/octo/scratch/commits/bbbb":
			_, _ = io.WriteString(w, `{"stats":{"additions":3,"deletions":1,"total":4}}`)
		case "/repos/octo/scratch/commits/cccc":
			_, _ = io.WriteString(w, `{"stats":{"additions":2,"deletions":0,"total":2}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	commits, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		Commits(context.Background(), "octo/scratch", 7)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := RewriteOf(commits), (Rewrite{AgentLines: 128, OwnerLines: 6, UnlabelledLines: 6}); got != want {
		t.Fatalf("RewriteOf = %+v over %+v, want %+v", got, commits, want)
	}
}

func TestGitHubReadsACommitGitHubReportsNoStatsForAsNoLines(t *testing.T) {
	// GitHub answers null stats for a commit of more than 300 files, which counts
	// as no lines rather than withholding the measurement.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/octo/scratch/pulls/7/commits" {
			_, _ = io.WriteString(w, `[{"sha":"aaaa","commit":{"author":{"name":"the runner","email":"runner@example.com"}}}]`)
			return
		}
		_, _ = io.WriteString(w, `{"stats":null,"files":[]}`)
	}))
	defer srv.Close()

	commits, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		Commits(context.Background(), "octo/scratch", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].Lines() != 0 || !commits[0].Unmeasured {
		t.Fatalf("commits = %+v, want one unmeasured commit of no lines", commits)
	}
}

func TestGitHubListsTheClosedPullRequestsNewestFirst(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `[
			{"number":9,"title":"Revert \"feat(x): add a file\"","body":"Reverts octo/scratch#7",
			 "merged_at":"2026-10-14T04:00:00Z","head":{"ref":"revert-7-wingman/FRG-1-9-1"}},
			{"number":7,"title":"feat(x): add a file","body":"","merged_at":"2026-10-14T03:00:00Z",
			 "html_url":"https://github.com/octo/scratch/pull/7",
			 "head":{"ref":"wingman/FRG-1-9-1"}},
			{"number":6,"title":"a PR nobody merged","body":"","merged_at":null,"head":{"ref":"wingman/FRG-1-8-1"}}
		]`)
	}))
	defer srv.Close()

	prs, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		Closed(context.Background(), "octo/scratch")
	if err != nil {
		t.Fatal(err)
	}
	if query != "state=closed&sort=updated&direction=desc&per_page=100" {
		t.Fatalf("query = %q, want the closed listing most recently updated first", query)
	}
	if len(prs) != 3 {
		t.Fatalf("closed = %+v, want three", prs)
	}
	if got := prs[0]; got.Number != 9 || got.Head != "revert-7-wingman/FRG-1-9-1" ||
		!got.MergedAt.Equal(time.Date(2026, 10, 14, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("first = %+v, want the revert with its head and merge time", got)
	}
	if got := prs[1]; got.Title != "feat(x): add a file" || got.Head != "wingman/FRG-1-9-1" ||
		got.URL != "https://github.com/octo/scratch/pull/7" {
		t.Fatalf("second = %+v, want the run's own PR with its page", got)
	}
	if got := prs[2]; !got.MergedAt.IsZero() {
		t.Fatalf("third = %+v, want a PR that closed unmerged to carry no merge time", got)
	}
}

func TestGitHubReadsOnePullRequestFromItsOwnReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/scratch/pulls/7" {
			t.Errorf("path = %q, want the pull request", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"number":7,"title":"feat(x): add a file","body":"",
			"merged_at":"2026-10-14T03:00:00Z","head":{"ref":"wingman/FRG-1-9-1"}}`)
	}))
	defer srv.Close()

	pr, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		Pull(context.Background(), "octo/scratch", 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || pr.Head != "wingman/FRG-1-9-1" || pr.MergedAt.IsZero() {
		t.Fatalf("Pull = %+v, want the run's PR and its merge time", pr)
	}
}

func TestGitHubFollowsTheCommitPagesAndFailsClosedAtTheCap(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/octo/scratch/commits/") {
			_, _ = io.WriteString(w, `{"stats":{"additions":1,"deletions":0}}`)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/octo/scratch/pulls/7/commits?per_page=100&page=%d>; rel="next"`, srv.URL, page+1))
		_, _ = fmt.Fprintf(w, `[{"sha":"s%d","author":{"login":"alvintoh"},"commit":{"author":{}}}]`, page)
	}))
	defer srv.Close()
	commits, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).Commits(context.Background(), "octo/scratch", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != maxListPages+1 || !commits[len(commits)-1].Unmeasured {
		t.Fatalf("got %d commits, want %d pages read and an unmeasured commit marking the cut", len(commits), maxListPages)
	}
	if RewriteOf(commits).UnmeasuredCommits != 1 {
		t.Fatal("want the cut to leave the PR unmeasured")
	}
}

func TestGitHubReadsEveryClosedPageAndFlagsATruncatedListing(t *testing.T) {
	var srv *httptest.Server
	pages := 2
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		if page < pages {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/octo/scratch/pulls?state=closed&page=%d>; rel="next", <%s/x>; rel="last"`, srv.URL, page+1, srv.URL))
		}
		_, _ = fmt.Fprintf(w, `[{"number":%d,"title":"t","body":"","merged_at":null,"head":{"ref":"b"}}]`, page)
	}))
	defer srv.Close()
	g := GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}
	prs, err := g.Closed(context.Background(), "octo/scratch")
	if err != nil || len(prs) != 2 || prs[1].Number != 2 {
		t.Fatalf("Closed = %+v, %v, want both pages read", prs, err)
	}
	pages = 1000
	prs, err = g.Closed(context.Background(), "octo/scratch")
	if !errors.Is(err, ErrClosedTruncated) || len(prs) != maxListPages {
		t.Fatalf("Closed = %d PRs, %v, want the cap reported with the PRs read", len(prs), err)
	}
}

func TestGitHubDoesNotFollowANextLinkToAnotherHost(t *testing.T) {
	other := 0
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		other++
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "<"+elsewhere.URL+"/pulls?page=2>; rel=\"next\"")
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	if _, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).Closed(context.Background(), "octo/scratch"); err == nil {
		t.Fatal("want a next link off the API host refused")
	}
	if other != 0 {
		t.Fatalf("the other host was asked %d times, want the token kept from it", other)
	}
}

func TestGitHubFailsWhenALaterPageCannotBeRead(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/octo/scratch/pulls?page=2>; rel="next"`, srv.URL))
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	if _, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).Closed(context.Background(), "octo/scratch"); err == nil {
		t.Fatal("want a page that cannot be read to fail the listing, not undercount it")
	}
}

func TestGitHubRefusalCarriesItsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).OpenAgentPRs(context.Background(), "octo/scratch")
	var api *APIError
	if !errors.As(err, &api) || api.Status != 404 {
		t.Fatalf("err = %v, want an APIError with status 404", err)
	}
}

func TestGitHubMergesSquashGuardedByTheHeadSHA(t *testing.T) {
	var method, path string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		_, _ = io.WriteString(w, `{"sha":"def456","merged":true}`)
	}))
	defer srv.Close()

	sha, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).Merge(context.Background(), "octo/scratch", 7, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/repos/octo/scratch/pulls/7/merge" {
		t.Fatalf("request = %s %s", method, path)
	}
	if len(body) != 2 || body["merge_method"] != "squash" || body["sha"] != "abc123" {
		t.Fatalf("body = %v, want a squash guarded by sha abc123", body)
	}
	if sha != "def456" {
		t.Fatalf("merge sha = %q, want def456", sha)
	}
}

func TestGitHubMergeReturnsGitHubsOwnReasonForAnUnmergeablePull(t *testing.T) {
	for status, reason := range map[int]string{
		http.StatusMethodNotAllowed: "Pull Request is not mergeable",
		http.StatusConflict:         "Head branch was modified. Review and try the merge again.",
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = fmt.Fprintf(w, `{"message":%q}`, reason)
			}))
			defer srv.Close()

			_, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).Merge(context.Background(), "octo/scratch", 7, "abc123")
			var refused *APIError
			if !errors.As(err, &refused) || refused.Status != status || refused.Reason != reason {
				t.Fatalf("err = %v, want a refusal %d saying %q", err, status, reason)
			}
		})
	}
}

func TestGitHubPullStateReadsOpenDraftMergedAndHead(t *testing.T) {
	for name, tt := range map[string]struct {
		body string
		want PullStatus
	}{
		"an open draft":   {`{"state":"open","draft":true,"merged":false,"head":{"sha":"abc123"}}`, PullStatus{Open: true, Draft: true, HeadSHA: "abc123"}},
		"a merged one":    {`{"state":"closed","draft":false,"merged":true,"head":{"sha":"abc123"}}`, PullStatus{Merged: true, HeadSHA: "abc123"}},
		"closed unmerged": {`{"state":"closed","draft":false,"merged":false,"head":{"sha":"abc123"}}`, PullStatus{HeadSHA: "abc123"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/octo/scratch/pulls/7" {
					t.Errorf("path = %q", r.URL.Path)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			got, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).PullState(context.Background(), "octo/scratch", 7)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("state = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestGitHubRefusalWithNoMessageKeepsTheBodyAsItsReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"errors":["no good"]}`)
	}))
	defer srv.Close()

	_, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).Merge(context.Background(), "octo/scratch", 7, "abc123")
	var refused *APIError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "no good") {
		t.Fatalf("err = %v, want a refusal whose reason carries the body", err)
	}
}

func TestGitHubChecksGreenNeedsEveryRunPassingAndAnyStatusSuccessful(t *testing.T) {
	const green = `{"total_count":1,"check_runs":[{"status":"completed","conclusion":"success"}]}`
	const noStatus = `{"state":"pending","total_count":0}`
	for _, tt := range []struct {
		name, runs, status string
		want, wantErr      bool
	}{
		{"all passing", `{"total_count":3,"check_runs":[{"conclusion":"success"},{"conclusion":"neutral"},{"conclusion":"skipped"}]}`, noStatus, true, false},
		{"one failed", `{"total_count":2,"check_runs":[{"conclusion":"success"},{"conclusion":"failure"}]}`, noStatus, false, false},
		{"one running", `{"total_count":2,"check_runs":[{"conclusion":"success"},{"conclusion":null}]}`, noStatus, false, false},
		{"none", `{"total_count":0,"check_runs":[]}`, noStatus, false, false},
		{"more than a page", `{"total_count":150,"check_runs":[{"conclusion":"success"}]}`, noStatus, false, true},
		{"status success", green, `{"state":"success","total_count":1}`, true, false},
		{"status failing", green, `{"state":"failure","total_count":1}`, false, false},
		{"status pending", green, `{"state":"pending","total_count":1}`, false, false},
		{"no statuses", green, noStatus, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/octo/scratch/commits/abc123/check-runs":
					_, _ = io.WriteString(w, tt.runs)
				case "/repos/octo/scratch/commits/abc123/status":
					_, _ = io.WriteString(w, tt.status)
				default:
					t.Errorf("path = %q", r.URL.Path)
				}
			}))
			defer srv.Close()

			got, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).ChecksGreen(context.Background(), "octo/scratch", "abc123")
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("green = %v, err = %v, want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestGitHubPullSummariesReadsEveryPullInOneGraphQLRequest(t *testing.T) {
	var calls int
	var path string
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		_, _ = io.WriteString(w, `{"data":{
		"r0":{"pullRequest":{"title":"feat: a","state":"OPEN","isDraft":false,"headRefOid":"abc123","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS","contexts":{"nodes":[
			{"__typename":"CheckRun","name":"go","status":"COMPLETED","conclusion":"SUCCESS"},
			{"__typename":"CheckRun","name":"web","status":"COMPLETED","conclusion":"SKIPPED"}]}}}}]}}},
		"r1":{"pullRequest":{"title":"fix: b","state":"OPEN","isDraft":true,"headRefOid":"def456","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE","contexts":{"nodes":[
			{"__typename":"CheckRun","name":"go","status":"COMPLETED","conclusion":"SUCCESS"},
			{"__typename":"CheckRun","name":"web","status":"COMPLETED","conclusion":"FAILURE"},
			{"__typename":"StatusContext","context":"lint","state":"ERROR"}]}}}}]}}},
		"r4":{"pullRequest":{"title":"feat: d","state":"MERGED","isDraft":false,"headRefOid":"9f9f9f","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING","contexts":{"nodes":[
			{"__typename":"CheckRun","name":"go","status":"IN_PROGRESS","conclusion":null},
			{"__typename":"StatusContext","context":"lint","state":"PENDING"},
			{"__typename":"CheckRun","name":"web","status":"COMPLETED","conclusion":"SUCCESS"}]}}}}]}}},
		"r2":{"pullRequest":{"title":"chore: c","state":"CLOSED","isDraft":false,"headRefOid":"0a0a0a","commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}},
		"r3":null}}`)
	}))
	defer srv.Close()

	refs := []PullRef{{"octo/scratch", 7}, {"octo/scratch", 8}, {"octo/other", 9}, {"octo/gone", 1}, {"octo/scratch", 10}}
	got, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).PullSummaries(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || path != "/graphql" {
		t.Fatalf("%d requests to %s, want one to /graphql", calls, path)
	}
	if req.Variables["o2"] != "octo" || req.Variables["n2"] != "other" || req.Variables["p2"] != float64(9) || strings.Contains(req.Query, "octo") {
		t.Fatalf("variables = %v, query %q, want the repository passed as variables", req.Variables, req.Query)
	}
	if !regexp.MustCompile(`pullRequest\(number: \$p0\) \{[^{]*\bstate\b`).MatchString(req.Query) {
		t.Fatalf("query %q, want the pull request's own state read", req.Query)
	}
	want := map[PullRef]PullSummary{
		{"octo/scratch", 7}:  {Title: "feat: a", State: "OPEN", HeadSHA: "abc123", Checks: CheckGreen, ChecksSummary: "go and web"},
		{"octo/scratch", 8}:  {Title: "fix: b", State: "OPEN", Draft: true, HeadSHA: "def456", Checks: CheckFailing, ChecksSummary: "web and lint"},
		{"octo/scratch", 10}: {Title: "feat: d", State: "MERGED", HeadSHA: "9f9f9f", Checks: CheckPending, ChecksSummary: "go and lint"},
		{"octo/other", 9}:    {Title: "chore: c", State: "CLOSED", HeadSHA: "0a0a0a", Checks: CheckPending, ChecksSummary: "no checks yet"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summaries = %+v, want %+v", got, want)
	}
}

func TestGitHubPullSummariesReportsAReadGitHubRefuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"Bad credentials"}]}`)
	}))
	defer srv.Close()
	_, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).PullSummaries(context.Background(), []PullRef{{"octo/scratch", 7}})
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("err = %v, want GitHub's reason", err)
	}
}

func TestGitHubPullSummariesRefusesMoreRefsThanOneReadHolds(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	refs := make([]PullRef, MaxSummaryRefs+1)
	for i := range refs {
		refs[i] = PullRef{"octo/scratch", i + 1}
	}
	_, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).PullSummaries(context.Background(), refs)
	if err == nil || !strings.Contains(err.Error(), "exceed") || calls != 0 {
		t.Fatalf("err = %v after %d requests, want a refusal before any request", err, calls)
	}
}

func TestRollupChecksMapsStatusStateAndDoesNotTrustAnUnknownOne(t *testing.T) {
	for state, want := range map[string]string{
		"SUCCESS":  CheckGreen,
		"PENDING":  CheckPending,
		"EXPECTED": CheckPending,
		"FAILURE":  CheckFailing,
		"ERROR":    CheckFailing,
		"":         CheckFailing,
	} {
		if got := rollupChecks(state); got != want {
			t.Errorf("rollupChecks(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestNamesInStateNamesACheckOnceAndCapsTheList(t *testing.T) {
	run := func(name, conclusion string) summaryContext {
		return summaryContext{Type: "CheckRun", Name: name, Status: "COMPLETED", Conclusion: conclusion}
	}
	for name, tt := range map[string]struct {
		contexts []summaryContext
		state    string
		want     string
	}{
		"a rerun check is named once":     {[]summaryContext{run("go", "FAILURE"), run("go", "FAILURE"), run("web", "FAILURE")}, CheckFailing, "go and web"},
		"more than four are counted":      {[]summaryContext{run("a", "SUCCESS"), run("b", "SUCCESS"), run("c", "SUCCESS"), run("d", "SUCCESS"), run("e", "SUCCESS"), run("f", "SUCCESS")}, CheckGreen, "a, b, c, d and 2 more"},
		"none in the state names nothing": {[]summaryContext{run("go", "SUCCESS")}, CheckFailing, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := namesInState(tt.contexts, tt.state); got != tt.want {
				t.Fatalf("namesInState = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGitHubListsThePullRequestsCommitsAcrossPages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/scratch/pulls/7/commits" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, `[{"sha":"cccc","author":null,"commit":{"message":"tidy","author":{"name":"the owner","email":"owner@example.com"}}}]`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/octo/scratch/pulls/7/commits?per_page=100&page=2>; rel="next"`, srv.URL))
		_, _ = io.WriteString(w, `[
			{"sha":"aaaa","author":{"login":"github-actions[bot]"},"commit":{"message":"feat: add","author":{"name":"github-actions[bot]","email":"bot@example.com"}}},
			{"sha":"bbbb","author":{"login":"alvintoh"},"commit":{"message":"fix: review","author":{"name":"the owner","email":"owner@example.com"}}}
		]`)
	}))
	defer srv.Close()

	commits, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).PullCommits(context.Background(), "octo/scratch", 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []PullCommit{
		{SHA: "aaaa", Login: "github-actions[bot]", Name: "github-actions[bot]", Email: "bot@example.com", Message: "feat: add"},
		{SHA: "bbbb", Login: "alvintoh", Name: "the owner", Email: "owner@example.com", Message: "fix: review"},
		{SHA: "cccc", Name: "the owner", Email: "owner@example.com", Message: "tidy"},
	}
	if !reflect.DeepEqual(commits, want) {
		t.Fatalf("commits = %+v, want %+v", commits, want)
	}
}

func TestGitHubPullCommitsFailsAtTheCeilingGitHubLists(t *testing.T) {
	for _, tt := range []struct {
		total   int
		wantErr bool
	}{{pullCommitsCeiling - 1, false}, {pullCommitsCeiling, true}} {
		t.Run(strconv.Itoa(tt.total), func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				page = max(page, 1)
				first, last := (page-1)*100, min(page*100, tt.total)
				if last < tt.total {
					w.Header().Set("Link", fmt.Sprintf(`<%s/repos/octo/scratch/pulls/7/commits?per_page=100&page=%d>; rel="next"`, srv.URL, page+1))
				}
				var items []string
				for i := first; i < last; i++ {
					items = append(items, fmt.Sprintf(`{"sha":"s%d","author":{"login":"alvintoh"},"commit":{"author":{}}}`, i))
				}
				_, _ = io.WriteString(w, "["+strings.Join(items, ",")+"]")
			}))
			defer srv.Close()
			commits, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).PullCommits(context.Background(), "octo/scratch", 7)
			if (err != nil) != tt.wantErr || (err == nil && len(commits) != tt.total) {
				t.Fatalf("got %d commits, %v, want %d commits and error %v", len(commits), err, tt.total, tt.wantErr)
			}
		})
	}
}

func TestGitHubReadsTheStatusChecksTheBranchRulesRequire(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       []string
		wantErr    bool
	}{
		{"two rulesets naming one context twice", `[
			{"type":"pull_request","parameters":{}},
			{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"go"},{"context":"web"}]}},
			{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"web"},{"context":"lint"}]}}
		]`, []string{"go", "web", "lint"}, false},
		{"rules that require no check", `[{"type":"pull_request","parameters":{}}]`, nil, true},
		{"another rule type naming a context", `[{"type":"workflows","parameters":{"required_status_checks":[{"context":"go"}]}}]`, nil, true},
		{"a rule with an empty list", `[{"type":"required_status_checks","parameters":{"required_status_checks":[]}}]`, nil, true},
		{"no rules", `[]`, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/octo/scratch/rules/branches/main" {
					t.Errorf("path = %q", r.URL.Path)
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			got, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).RequiredChecks(context.Background(), "octo/scratch", "main")
			if (err != nil) != tt.wantErr || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, %v, want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestGitHubChecksOutcomeWaitsForEveryRequiredCheck(t *testing.T) {
	const noStatus = `{"total_count":0,"statuses":[]}`
	for _, tt := range []struct {
		name, runs, status string
		required           []string
		want               string
	}{
		{"every check green", `{"total_count":2,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"success"},
			{"id":2,"name":"web","status":"completed","conclusion":"skipped"}]}`, noStatus, []string{"go", "web"}, CheckGreen},
		{"a failing run no rule requires is ignored", `{"total_count":2,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"success"},
			{"id":2,"name":"docs","status":"completed","conclusion":"failure"}]}`, noStatus, []string{"go"}, CheckGreen},
		{"one failing", `{"total_count":2,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"failure"},
			{"id":2,"name":"web","status":"completed","conclusion":"success"}]}`, noStatus, []string{"go", "web"}, CheckFailing},
		{"one missing", `{"total_count":1,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"success"}]}`, noStatus, []string{"go", "web"}, CheckPending},
		{"one in progress", `{"total_count":2,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"success"},
			{"id":2,"name":"web","status":"in_progress","conclusion":null}]}`, noStatus, []string{"go", "web"}, CheckPending},
		{"pending wins over failing", `{"total_count":2,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"failure"},
			{"id":2,"name":"web","status":"queued","conclusion":null}]}`, noStatus, []string{"go", "web"}, CheckPending},
		{"a rerun that passed after a failure", `{"total_count":2,"check_runs":[
			{"id":9,"name":"go","status":"completed","conclusion":"success"},
			{"id":4,"name":"go","status":"completed","conclusion":"failure"}]}`, noStatus, []string{"go"}, CheckGreen},
		{"a neutral conclusion is not a failure", `{"total_count":1,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"neutral"}]}`, noStatus, []string{"go"}, CheckGreen},
		{"a context with a run and a status takes the run", `{"total_count":1,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"success"}]}`,
			`{"total_count":2,"statuses":[{"context":"go","state":"failure"},{"context":"deploy","state":"success"}]}`, []string{"go", "deploy"}, CheckGreen},
		{"a legacy status for a context with no run", `{"total_count":1,"check_runs":[
			{"id":1,"name":"go","status":"completed","conclusion":"success"}]}`,
			`{"total_count":1,"statuses":[{"context":"deploy","state":"success"}]}`, []string{"go", "deploy"}, CheckGreen},
		{"a failing legacy status", `{"total_count":0,"check_runs":[]}`,
			`{"total_count":1,"statuses":[{"context":"deploy","state":"failure"}]}`, []string{"deploy"}, CheckFailing},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/octo/scratch/commits/abc123/check-runs":
					_, _ = io.WriteString(w, tt.runs)
				case "/repos/octo/scratch/commits/abc123/status":
					_, _ = io.WriteString(w, tt.status)
				default:
					t.Errorf("path = %q", r.URL.Path)
				}
			}))
			defer srv.Close()
			got, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).ChecksOutcome(context.Background(), "octo/scratch", "abc123", tt.required)
			if err != nil || got != tt.want {
				t.Fatalf("outcome = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestGitHubChecksOutcomeRefusesToCallNoRequiredChecksGreen(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	got, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).ChecksOutcome(context.Background(), "octo/scratch", "abc123", nil)
	if err == nil || got != "" || calls != 0 {
		t.Fatalf("got %q, %v after %d calls, want an error before any read", got, err, calls)
	}
}

func TestGitHubChecksOutcomeReadsStatusesOnlyForContextsWithNoRun(t *testing.T) {
	const runs = `{"total_count":1,"check_runs":[{"id":1,"name":"go","status":"completed","conclusion":"success"}]}`
	for _, tt := range []struct {
		name     string
		required []string
		wantErr  bool
		want     string
	}{
		{"every context has a run", []string{"go"}, false, CheckGreen},
		{"a context has none", []string{"go", "deploy"}, true, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/check-runs") {
					_, _ = io.WriteString(w, runs)
					return
				}
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer srv.Close()
			got, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).ChecksOutcome(context.Background(), "octo/scratch", "abc123", tt.required)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v, want %q, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestGitHubChecksOutcomeFailsWhenAReadHoldsMoreThanItsPage(t *testing.T) {
	for name, tt := range map[string]struct{ runs, status string }{
		"more runs than the page": {`{"total_count":150,"check_runs":[{"id":1,"name":"go","status":"completed","conclusion":"success"}]}`,
			`{"total_count":0,"statuses":[]}`},
		"more statuses than the page": {`{"total_count":0,"check_runs":[]}`,
			`{"total_count":150,"statuses":[{"context":"deploy","state":"success"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/check-runs") {
					_, _ = io.WriteString(w, tt.runs)
					return
				}
				_, _ = io.WriteString(w, tt.status)
			}))
			defer srv.Close()
			got, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).ChecksOutcome(context.Background(), "octo/scratch", "abc123", []string{"go", "deploy"})
			if err == nil || got != "" {
				t.Fatalf("got %q, %v, want an error and no outcome", got, err)
			}
		})
	}
}
