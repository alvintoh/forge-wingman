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
)

const (
	githubEndpoint = "https://api.github.com"
	// githubAPIVersion is the REST version the workflow-dispatch documentation
	// is written against; GitHub ignores the header's version rather than the
	// shape of the request.
	githubAPIVersion = "2026-03-10"
	// maxErrorBytes caps how much of a refusal is kept for the log, since a
	// failure is reported by its message and not by the rest of the body.
	maxErrorBytes = 512
	// maxLinearResponseBytes bounds one Linear page: a page of a hundred
	// oversized descriptions is a ticket the query should not have asked for.
	maxLinearResponseBytes = 4 << 20
	// maxRepoResponseBytes bounds a "get a repository" reply: the field this
	// product reads from it is one boolean.
	maxRepoResponseBytes = 64 << 10
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building the dispatch request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("dispatching run %s in %s: %w", c.RunID, c.Repo, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBytes))
	return fmt.Errorf("GitHub refused to dispatch run %s in %s: %s: %s",
		c.RunID, c.Repo, res.Status, snippet(raw))
}

// Private reports whether repo is a private repository, read live from
// GitHub's own API (FR-22) rather than a hand-maintained list, which goes
// stale the moment a repository's visibility changes.
func (g GitHub) Private(ctx context.Context, repo string) (bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s", g.endpoint(), repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("building the repository request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("reading repository %s: %w", repo, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBytes))
		return false, fmt.Errorf("GitHub refused to read repository %s: %s: %s", repo, res.Status, snippet(raw))
	}
	var body struct {
		Private bool `json:"private"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxRepoResponseBytes)).Decode(&body); err != nil {
		return false, fmt.Errorf("decoding repository %s: %w", repo, err)
	}
	return body.Private, nil
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
