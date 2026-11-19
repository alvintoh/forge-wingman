package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The chain follows first parents, so a commit a merge brought in beside them
// is left out though the listing holds it.
func TestGitHubWalksTheFirstParentsOfABranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/octo/scratch/commits" || r.URL.RawQuery != "sha=main&per_page=20" {
			t.Errorf("%s %s?%s, want main's commits listed", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `[
			{"sha":"c3","parents":[{"sha":"c2"},{"sha":"side"}]},
			{"sha":"side","parents":[{"sha":"c1"}]},
			{"sha":"c2","parents":[{"sha":"c1"}]},
			{"sha":"c1","parents":[{"sha":"c0"}]}
		]`)
	}))
	defer srv.Close()
	chain, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).FirstParents(context.Background(), "octo/scratch", "main")
	if err != nil {
		t.Fatal(err)
	}
	// c0 is past the listing, still named as c1's parent.
	if want := []string{"c3", "c2", "c1", "c0"}; strings.Join(chain, ",") != strings.Join(want, ",") {
		t.Fatalf("chain = %v, want %v", chain, want)
	}
}

func TestGitHubStopsTheFirstParentWalkAtItsLimit(t *testing.T) {
	var commits []string
	for i := 30; i > 0; i-- {
		commits = append(commits, fmt.Sprintf(`{"sha":"c%d","parents":[{"sha":"c%d"}]}`, i, i-1))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "["+strings.Join(commits, ",")+"]")
	}))
	defer srv.Close()
	chain, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).FirstParents(context.Background(), "octo/scratch", "main")
	if err != nil || len(chain) != mainWalkLimit || chain[0] != "c30" {
		t.Fatalf("chain = %v, %v, want %d commits from the head", chain, err, mainWalkLimit)
	}
}

func TestGitHubFailsABranchWithNoCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	if chain, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).FirstParents(context.Background(), "octo/scratch", "main"); err == nil {
		t.Fatalf("FirstParents = %v, want an error rather than no head", chain)
	}
}

func TestGitHubListsTheOpenPullRequests(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/scratch/pulls" {
			t.Errorf("path = %q, want the pull listing", r.URL.Path)
		}
		queries = append(queries, r.URL.RawQuery)
		_, _ = io.WriteString(w, `[
			{"number":12,"title":"Revert \"feat(x): add a file\"","body":"Reverts octo/scratch#7",
			 "head":{"ref":"revert-7-wingman/FRG-1-9-1","repo":{"full_name":"octo/scratch"}}},
			{"number":13,"title":"from a deleted fork","head":{"ref":"main","repo":null}}
		]`)
	}))
	defer srv.Close()
	open, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).OpenPulls(context.Background(), "octo/scratch")
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || queries[0] != "state=open&per_page=100&page=1" {
		t.Fatalf("queries = %v, want one page of the open listing", queries)
	}
	if len(open) != 2 || open[0].Number != 12 || open[0].Body != "Reverts octo/scratch#7" || open[0].Title != `Revert "feat(x): add a file"` ||
		open[0].Head != "revert-7-wingman/FRG-1-9-1" || open[0].HeadRepo != "octo/scratch" {
		t.Fatalf("open = %+v, want the revert with its title, body, branch and the repository the branch lives in", open)
	}
	if open[1].HeadRepo != "" {
		t.Fatalf("open = %+v, want no repository for a head whose repository is gone", open[1])
	}
}

func TestGitHubFailsWhenTheOpenPullRequestsOutrunTheirPages(t *testing.T) {
	page := "[" + strings.TrimSuffix(strings.Repeat(`{"number":1,"title":"x","head":{"ref":"a"}},`, pullsPerPage), ",") + "]"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, page)
	}))
	defer srv.Close()
	if _, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).OpenPulls(context.Background(), "octo/scratch"); err == nil {
		t.Fatal("err = nil, want a listing that may be short refused")
	}
}

// A listing reads the merge commit only of a merged PR: GitHub fills the field
// with a test merge before then.
func TestGitHubReadsTheMergeCommitOfAMergedPROnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[
			{"number":7,"node_id":"PR_kwDO7","merged_at":"2026-10-14T03:00:00Z","merge_commit_sha":"merge7","head":{"ref":"wingman/FRG-1-9-1"}},
			{"number":6,"node_id":"PR_kwDO6","merged_at":null,"merge_commit_sha":"testmerge6","head":{"ref":"wingman/FRG-1-8-1"}}
		]`)
	}))
	defer srv.Close()
	prs, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).Closed(context.Background(), "octo/scratch")
	if err != nil {
		t.Fatal(err)
	}
	if prs[0].MergeCommit != "merge7" || prs[0].NodeID != "PR_kwDO7" {
		t.Fatalf("merged = %+v, want its merge commit and GraphQL id", prs[0])
	}
	if prs[1].MergeCommit != "" {
		t.Fatalf("unmerged = %+v, want no merge commit", prs[1])
	}
}

func TestGitHubOpensARevertThroughTheGraphQLMutation(t *testing.T) {
	var path string
	var req struct {
		Query     string            `json:"query"`
		Variables map[string]string `json:"variables"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		_, _ = io.WriteString(w, `{"data":{"revertPullRequest":{"revertPullRequest":{"number":12}}}}`)
	}))
	defer srv.Close()
	n, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).RevertPull(context.Background(),
		ClosedPR{Number: 7, NodeID: "PR_kwDO7"}, `Revert "feat(x): add a file"`, "Reverts octo/scratch#7")
	if err != nil || n != 12 {
		t.Fatalf("RevertPull = %d, %v, want 12", n, err)
	}
	if path != "POST /graphql" || !strings.Contains(req.Query, "revertPullRequest(input: {pullRequestId: $id, title: $title, body: $body})") {
		t.Fatalf("%s with %q, want the revertPullRequest mutation", path, req.Query)
	}
	want := map[string]string{"id": "PR_kwDO7", "title": `Revert "feat(x): add a file"`, "body": "Reverts octo/scratch#7"}
	for k, v := range want {
		if req.Variables[k] != v {
			t.Fatalf("variables = %v, want %v", req.Variables, want)
		}
	}
}

func TestGitHubReportsARevertGitHubRefuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"revertPullRequest":null},"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by personal access token"}]}`)
	}))
	defer srv.Close()
	_, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).RevertPull(context.Background(),
		ClosedPR{Number: 7, NodeID: "PR_kwDO7"}, "t", "b")
	if err == nil || !strings.Contains(err.Error(), "Resource not accessible") {
		t.Fatalf("err = %v, want GitHub's reason", err)
	}
}

func TestGitHubRevertsNothingWithoutAGraphQLID(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	if _, err := (GitHub{Endpoint: srv.URL, Token: "t", Client: srv.Client()}).RevertPull(context.Background(),
		ClosedPR{Number: 7}, "t", "b"); err == nil || calls != 0 {
		t.Fatalf("err = %v after %d calls, want a refusal before any request", err, calls)
	}
}
