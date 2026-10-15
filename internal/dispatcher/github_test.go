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
	if got := prs[1]; got.Title != "feat(x): add a file" || got.Head != "wingman/FRG-1-9-1" {
		t.Fatalf("second = %+v, want the run's own PR", got)
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
