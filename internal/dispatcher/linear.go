package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// linearEndpoint is Linear's GraphQL API; the tests point Linear.Endpoint at a
// server of their own.
const linearEndpoint = "https://api.linear.app/graphql"

// blocksRelation is the type of a Linear issue relation whose issue blocks its
// related issue.
const blocksRelation = "blocks"

// linearPage is how many issues one page asks for. A workspace holds far more
// issues than one workspace's delegated to a single agent, so the pages are
// walked rather than the first one taken as all of them.
const linearPage = 50

// delegatedQuery reads the token's own identity beside the issues delegated to
// it, so a poll cannot act for an agent other than the configured one. State
// types rather than names are filtered: the completed and canceled names differ
// per team, the types do not.
const delegatedQuery = `query DelegatedIssues($delegate: String!, $first: Int, $after: String) {
  viewer {
    id
  }
  issues(
    filter: { delegate: { id: { eq: $delegate } }, state: { type: { nin: ["completed", "canceled"] } } }
    first: $first
    after: $after
  ) {
    nodes {
      identifier
      title
      description
      priority
      labels {
        nodes {
          name
        }
      }
      relations {
        nodes {
          type
          relatedIssue {
            identifier
          }
        }
      }
      inverseRelations {
        nodes {
          type
          issue {
            identifier
          }
        }
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}`

// Linear reads the issues delegated to one Linear agent.
type Linear struct {
	Endpoint string
	Token    string
	// Delegate is the agent the poll acts for, the id Linear's viewer query
	// must return for every page.
	Delegate string
	Client   *http.Client
	Page     int
}

// linearResponse is one GraphQL reply. Errors arrive beside the data, with a
// 200 status, so both are decoded together.
type linearResponse struct {
	Data struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
		Issues struct {
			Nodes []struct {
				Identifier  string `json:"identifier"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Priority    int    `json:"priority"`
				Labels      struct {
					Nodes []struct {
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"labels"`
				Relations struct {
					Nodes []struct {
						Type         string `json:"type"`
						RelatedIssue struct {
							Identifier string `json:"identifier"`
						} `json:"relatedIssue"`
					} `json:"nodes"`
				} `json:"relations"`
				InverseRelations struct {
					Nodes []struct {
						Type  string `json:"type"`
						Issue struct {
							Identifier string `json:"identifier"`
						} `json:"issue"`
					} `json:"nodes"`
				} `json:"inverseRelations"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `json:"issues"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Delegated returns every issue still delegated to the configured agent,
// excluding the ones in a completed or canceled state. It fails unless the token
// reads as the configured agent, since the filter asks the API about the
// delegate by id and would otherwise return another agent's issues.
func (l Linear) Delegated(ctx context.Context) ([]Issue, error) {
	page := l.Page
	if page <= 0 {
		page = linearPage
	}
	var (
		issues []Issue
		cursor string
	)
	for {
		resp, err := l.page(ctx, cursor, page)
		if err != nil {
			return nil, err
		}
		if resp.Data.Viewer.ID != l.Delegate {
			return nil, fmt.Errorf("%w: the Linear token reads as %s, not the configured delegate %s",
				ErrDelegateMismatch, resp.Data.Viewer.ID, l.Delegate)
		}
		for _, node := range resp.Data.Issues.Nodes {
			issue := Issue{
				ID:       node.Identifier,
				Title:    node.Title,
				Body:     node.Description,
				Priority: node.Priority,
			}
			for _, label := range node.Labels.Nodes {
				issue.Labels = append(issue.Labels, label.Name)
			}
			for _, rel := range node.Relations.Nodes {
				if rel.Type == blocksRelation {
					issue.Blocks = append(issue.Blocks, rel.RelatedIssue.Identifier)
				}
			}
			for _, rel := range node.InverseRelations.Nodes {
				if rel.Type == blocksRelation {
					issue.BlockedBy = append(issue.BlockedBy, rel.Issue.Identifier)
				}
			}
			issues = append(issues, issue)
		}
		if !resp.Data.Issues.PageInfo.HasNextPage {
			return issues, nil
		}
		cursor = resp.Data.Issues.PageInfo.EndCursor
	}
}

// page asks Linear for one page of the delegated issues. The cursor is omitted
// rather than sent empty on the first page, since "" is not a cursor Linear can
// resume from.
func (l Linear) page(ctx context.Context, after string, size int) (linearResponse, error) {
	var resp linearResponse
	variables := struct {
		Delegate string `json:"delegate"`
		First    int    `json:"first"`
		After    string `json:"after,omitempty"`
	}{Delegate: l.Delegate, First: size, After: after}
	body, err := json.Marshal(map[string]any{"query": delegatedQuery, "variables": variables})
	if err != nil {
		return resp, fmt.Errorf("encoding the Linear query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint(), bytes.NewReader(body))
	if err != nil {
		return resp, fmt.Errorf("building the Linear request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.Token)
	client := l.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return resp, fmt.Errorf("reading Linear: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxLinearResponseBytes+1))
	if err != nil {
		return resp, fmt.Errorf("reading Linear's reply: %w", err)
	}
	if len(raw) > maxLinearResponseBytes {
		return resp, fmt.Errorf("the reply is over %d bytes", maxLinearResponseBytes)
	}
	if res.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("Linear answered %s: %s", res.Status, snippet(raw))
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return resp, fmt.Errorf("decoding Linear's reply: %w", err)
	}
	if len(resp.Errors) > 0 {
		var msgs []string
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return resp, fmt.Errorf("Linear refused the query: %s", strings.Join(msgs, "; "))
	}
	return resp, nil
}

func (l Linear) endpoint() string {
	if l.Endpoint == "" {
		return linearEndpoint
	}
	return l.Endpoint
}
