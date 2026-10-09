package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	githubEndpoint = "https://api.github.com"
	// GitHubAPIVersion is the REST version the workflow-dispatch documentation
	// is written against; GitHub ignores the header's version rather than the
	// shape of the request.
	GitHubAPIVersion = "2026-03-10"
	// maxErrorBytes caps how much of a refusal is kept for the log, since a
	// failure is reported by its message and not by the rest of the body.
	maxErrorBytes = 512
	// maxLinearResponseBytes bounds one Linear page: a page of a hundred
	// oversized descriptions is a ticket the query should not have asked for.
	maxLinearResponseBytes = 4 << 20
	// maxRepoResponseBytes bounds a "get a repository" reply: the field this
	// product reads from it is one boolean.
	maxRepoResponseBytes = 64 << 10
	// pullsPerPage is how many open PRs one listing page reads, and maxPullsPages
	// how many pages a count walks.
	pullsPerPage  = 100
	maxPullsPages = 5
	// maxPullsResponseBytes bounds one page of the open-PR listing.
	maxPullsResponseBytes = 4 << 20
)

// GitHub starts the run workflow in one repository.
type GitHub struct {
	Endpoint string
	// Token needs actions: write on every repository the allowlist names.
	Token string
	// Workflow is the run workflow's file name, which the dispatch endpoint
	// accepts in place of its numeric id.
	Workflow string
	// Branch is the ref the workflow is dispatched from: the runner's
	// Workload Identity Federation trusts main only, and run.yml's jobs are
	// gated on it.
	Branch string
	Client *http.Client
}

// Dispatch starts the run workflow for the claimed run in the repository the
// ticket's repo: label was allowlisted to. Any 2xx is a dispatch: GitHub
// documented 204 for this endpoint when it was added and answers 200 with the
// run's URLs now, and both mean the run was started.
func (g GitHub) Dispatch(ctx context.Context, c Claim) error {
	body, err := json.Marshal(map[string]any{
		"ref":    g.Branch,
		"inputs": map[string]string{"run_id": c.RunID},
	})
	if err != nil {
		return fmt.Errorf("encoding the dispatch request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/actions/workflows/%s/dispatches",
		g.endpoint(), c.Repo, url.PathEscape(g.Workflow))
	action := fmt.Sprintf("dispatch run %s in %s", c.RunID, c.Repo)
	res, err := g.do(ctx, http.MethodPost, endpoint, bytes.NewReader(body), action)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	return nil
}

// Private reports whether repo is a private repository, read live from
// GitHub's own API (FR-22) rather than a hand-maintained list, which goes
// stale the moment a repository's visibility changes.
func (g GitHub) Private(ctx context.Context, repo string) (bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s", g.endpoint(), repo)
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("read repository %s", repo))
	if err != nil {
		return false, err
	}
	defer func() { _ = res.Body.Close() }()
	var body struct {
		Private bool `json:"private"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxRepoResponseBytes)).Decode(&body); err != nil {
		return false, fmt.Errorf("decoding repository %s: %w", repo, err)
	}
	return body.Private, nil
}

// OpenAgentPRs counts the open pull requests in repo whose head branch is one
// of the runner's own. It needs pull_requests: read on every repository the
// allowlist names, and fails rather than report a count that may be short when
// the open PRs fill maxPullsPages pages.
func (g GitHub) OpenAgentPRs(ctx context.Context, repo string) (int, error) {
	n := 0
	for page := 1; page <= maxPullsPages; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/pulls?state=open&per_page=%d&page=%d", g.endpoint(), repo, pullsPerPage, page)
		heads, err := g.openPullHeads(ctx, endpoint, repo)
		if err != nil {
			return 0, err
		}
		for _, ref := range heads {
			if strings.HasPrefix(ref, runner.BranchPrefix) {
				n++
			}
		}
		if len(heads) < pullsPerPage {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s may hold more than %d open pull requests", repo, maxPullsPages*pullsPerPage)
}

// openPullHeads is the head branch of every PR on one page of the listing.
func (g GitHub) openPullHeads(ctx context.Context, endpoint, repo string) ([]string, error) {
	res, err := g.do(ctx, http.MethodGet, endpoint, nil, fmt.Sprintf("list open pull requests in %s", repo))
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	var pulls []struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxPullsResponseBytes)).Decode(&pulls); err != nil {
		return nil, fmt.Errorf("decoding the open pull requests of %s: %w", repo, err)
	}
	heads := make([]string, len(pulls))
	for i, p := range pulls {
		heads[i] = p.Head.Ref
	}
	return heads, nil
}

// do sends an authenticated GitHub API request and returns its response on
// any 2xx — the caller closes its body. reqBody may be nil for a GET. action
// names what the call was trying to do, for both the request-build error and
// a refusal's message.
func (g GitHub) do(ctx context.Context, method, endpoint string, reqBody io.Reader, action string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return nil, fmt.Errorf("building the request to %s: %w", action, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-GitHub-Api-Version", GitHubAPIVersion)
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBytes))
	return nil, fmt.Errorf("GitHub refused to %s: %s: %s", action, res.Status, snippet(raw))
}

func (g GitHub) endpoint() string {
	if g.Endpoint == "" {
		return githubEndpoint
	}
	return g.Endpoint
}

// snippet renders a reply's body for a log line, on one line and truncated.
func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > maxErrorBytes {
		s = s[:maxErrorBytes] + "…"
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
}
