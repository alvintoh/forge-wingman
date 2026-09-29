package dispatcher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
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

	claim := Claim{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1}
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
	if len(inputs) != 1 || inputs["run_id"] != "FRG-18" {
		t.Fatalf("inputs = %v", got.body["inputs"])
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, pullsPageBody(pullsPerPage, 1))
	}))
	defer srv.Close()
	n, err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Client: srv.Client()}).
		OpenAgentPRs(context.Background(), "octo/scratch")
	if err == nil || n != 0 {
		t.Fatalf("count %d, err %v, want no count that may be short", n, err)
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
