package linear

import (
	"context"
	"errors"
	"fmt"
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

// viewerTeamsQuery asks whether the caller belongs to any team.
const viewerTeamsQuery = `query ViewerTeams {
  viewer {
    teams(first: 1) {
      nodes {
        id
      }
    }
  }
}`

var (
	// ErrIssueNotFound reports an identifier Linear has no issue for.
	ErrIssueNotFound = errors.New("linear: no such issue")
	// ErrNoTeam reports a caller that belongs to no team, so every issue
	// reads as missing to it: an app user a workspace admin has suspended.
	ErrNoTeam = errors.New("linear: the caller belongs to no team")
)

// Issue is the part of a Linear issue a reviewer reads.
type Issue struct {
	Identifier  string
	Title       string
	Description string
	Labels      []string
}

// Issue reads the issue identifier names. When Linear reports no such issue,
// it asks whether the caller belongs to any team, and returns ErrNoTeam when
// it does not, since then the issue may exist and only be hidden.
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
	err := c.Do(ctx, issueQuery, map[string]string{"id": identifier}, &data)
	if err != nil && !entityNotFound(err) {
		return Issue{}, err
	}
	if data.Issue == nil {
		return Issue{}, c.missingIssue(ctx)
	}
	issue := Issue{Identifier: data.Issue.Identifier, Title: data.Issue.Title, Description: data.Issue.Description}
	for _, l := range data.Issue.Labels.Nodes {
		issue.Labels = append(issue.Labels, l.Name)
	}
	return issue, nil
}

// missingIssue tells an issue Linear lacks from one its caller cannot see.
func (c Client) missingIssue(ctx context.Context) error {
	var data struct {
		Viewer *struct {
			Teams struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"teams"`
		} `json:"viewer"`
	}
	if err := c.Do(ctx, viewerTeamsQuery, map[string]string{}, &data); err != nil {
		return fmt.Errorf("reading the caller's teams after a missing issue: %w", err)
	}
	if data.Viewer == nil || len(data.Viewer.Teams.Nodes) == 0 {
		return ErrNoTeam
	}
	return ErrIssueNotFound
}
