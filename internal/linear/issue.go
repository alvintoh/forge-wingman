package linear

import (
	"context"
	"errors"
)

// issueQuery reads one issue by its identifier, such as ABC-12.
const issueQuery = `query Issue($id: String!) {
  issue(id: $id) {
    identifier
    title
    description
    labels {
      nodes {
        name
      }
    }
  }
}`

// ErrIssueNotFound reports an identifier Linear has no issue for.
var ErrIssueNotFound = errors.New("linear: no such issue")

// Issue is the part of a Linear issue a reviewer reads.
type Issue struct {
	Identifier  string
	Title       string
	Description string
	Labels      []string
}

// Issue reads the issue identifier names.
func (c Client) Issue(ctx context.Context, identifier string) (Issue, error) {
	var data struct {
		Issue *struct {
			Identifier  string `json:"identifier"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Labels      struct {
				Nodes []struct {
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"issue"`
	}
	if err := c.Do(ctx, issueQuery, map[string]string{"id": identifier}, &data); err != nil {
		return Issue{}, err
	}
	if data.Issue == nil {
		return Issue{}, ErrIssueNotFound
	}
	issue := Issue{Identifier: data.Issue.Identifier, Title: data.Issue.Title, Description: data.Issue.Description}
	for _, l := range data.Issue.Labels.Nodes {
		issue.Labels = append(issue.Labels, l.Name)
	}
	return issue, nil
}
