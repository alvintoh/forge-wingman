package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
)

const (
	defaultGitHubAPI = "https://api.github.com"
	maxPullBytes     = 1 << 20
	maxCommentsBytes = 4 << 20
	maxGitHubError   = 512
	commentsPerPage  = 100
	maxCommentPages  = 5
)

// pullRequests calls GitHub's REST API for the pull requests of one repository.
type pullRequests struct {
	// API is the REST root; empty means GitHub's own.
	API    string
	Token  string
	Repo   string
	Client *http.Client
}

// pull is the part of a pull request the review reads.
type pull struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

// hasLabel reports whether the pull request carries label.
func (p pull) hasLabel(label string) bool {
	for _, l := range p.Labels {
		if l.Name == label {
			return true
		}
	}
	return false
}

// comment is one issue comment on a pull request.
type comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (g pullRequests) pull(ctx context.Context, number int) (pull, error) {
	var p pull
	err := g.call(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", g.Repo, number), "", nil, maxPullBytes, &p)
	return p, err
}

// diff is the diff from base to head, at most limit bytes, and whether it was cut.
func (g pullRequests) diff(ctx context.Context, base, head string, limit int) (string, bool, error) {
	var raw []byte
	err := g.call(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/compare/%s...%s", g.Repo, base, head), "application/vnd.github.diff", nil, limit+1, &raw)
	if err != nil {
		return "", false, err
	}
	if len(raw) > limit {
		return "", true, nil
	}
	return string(raw), false, nil
}

// comments are the pull request's issue comments, up to maxCommentPages pages.
func (g pullRequests) comments(ctx context.Context, number int) ([]comment, error) {
	var all []comment
	for page := 1; page <= maxCommentPages; page++ {
		var batch []comment
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=%d&page=%d", g.Repo, number, commentsPerPage, page)
		if err := g.call(ctx, http.MethodGet, path, "", nil, maxCommentsBytes, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < commentsPerPage {
			break
		}
	}
	return all, nil
}

func (g pullRequests) createComment(ctx context.Context, number int, body string) error {
	return g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", g.Repo, number), "", map[string]string{"body": body}, maxPullBytes, nil)
}

func (g pullRequests) editComment(ctx context.Context, id int64, body string) error {
	return g.call(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", g.Repo, id), "", map[string]string{"body": body}, maxPullBytes, nil)
}

func (g pullRequests) setStatus(ctx context.Context, sha string, s commitStatus) error {
	return g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/statuses/%s", g.Repo, sha), "", s, maxPullBytes, nil)
}

// call sends one request and decodes a 2xx reply of at most limit bytes into
// out: raw bytes into a *[]byte, JSON into anything else, nothing when nil.
func (g pullRequests) call(ctx context.Context, method, path, accept string, body any, limit int, out any) error {
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding %s %s: %w", method, path, err)
		}
		reqBody = bytes.NewReader(raw)
	}
	api := g.API
	if api == "" {
		api = defaultGitHubAPI
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(api, "/")+path, reqBody)
	if err != nil {
		return fmt.Errorf("building %s %s: %w", method, path, err)
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("X-GitHub-Api-Version", dispatcher.GitHubAPIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)))
	if err != nil {
		return fmt.Errorf("reading %s %s: %w", method, path, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("GitHub answered %s %s with %s: %s", method, path, res.Status, cell(strings.ToValidUTF8(string(raw[:min(len(raw), maxGitHubError)]), "")))
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = raw
		return nil
	default:
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decoding %s %s: %w", method, path, err)
		}
		return nil
	}
}
