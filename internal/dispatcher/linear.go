package dispatcher

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/linear"
)

// blocksRelation is the type of a Linear issue relation whose issue blocks its
// related issue.
const blocksRelation = "blocks"

// The Linear state types that mean an issue is finished: the poll does not act
// on one, and a blocker in one no longer holds what it blocks. Types rather
// than names are read, since the completed and canceled names differ per team.
const (
	stateTypeCompleted = "completed"
	stateTypeCanceled  = "canceled"
)

// doneStateTypes is stateTypeCompleted and stateTypeCanceled as the GraphQL
// list the delegated filter excludes.
const doneStateTypes = `"` + stateTypeCompleted + `", "` + stateTypeCanceled + `"`

// linearPage is how many issues one page asks for. A workspace holds far more
// issues than one workspace's delegated to a single agent, so the pages are
// walked rather than the first one taken as all of them.
const linearPage = 50

// inProgressStateName is the name of the state a claim moves a ticket into. The
// state's id is read from the ticket's own team, so a team's workflow decides
// it; the name is the one Linear's default workflows give that state.
const inProgressStateName = "In Progress"

// delegatedQuery reads the token's own identity beside the issues delegated to
// it, so a poll cannot act for an agent other than the configured one. An issue
// blocking this one carries its own state type, so a blocker still open can be
// told from a finished one.
const delegatedQuery = `query DelegatedIssues($delegate: ID!, $first: Int, $after: String) {
  viewer {
    id
  }
  issues(
    filter: { delegate: { id: { eq: $delegate } }, state: { type: { nin: [` + doneStateTypes + `] } } }
    first: $first
    after: $after
  ) {
    nodes {
      identifier
      url
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
            state {
              type
            }
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

// openQuery reads the token's own identity beside every open issue, with the
// delegate and the milestone a poll needs to choose the tickets it takes on
// itself: an issue already delegated still counts toward the milestone being
// worked through.
const openQuery = `query OpenIssues($first: Int, $after: String) {
  viewer {
    id
  }
  issues(
    filter: { state: { type: { nin: [` + doneStateTypes + `] } } }
    first: $first
    after: $after
  ) {
    nodes {
      id
      identifier
      url
      title
      description
      priority
      labels {
        nodes {
          name
        }
      }
      state {
        type
      }
      delegate {
        id
      }
      projectMilestone {
        id
        sortOrder
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}`

// delegateMutation delegates one issue to the agent, and reads the delegate
// back: the reply is the only evidence the delegation landed.
const delegateMutation = `mutation DelegateIssue($id: String!, $delegate: String!) {
  issueUpdate(id: $id, input: { delegateId: $delegate }) {
    success
    issue {
      delegate {
        id
      }
    }
  }
}`

// ticketStateQuery reads one ticket's own id, its state, the manual order a
// state change rewrites, and its team's workflow states — the last so the In
// Progress state's id is resolved per team rather than carried as a constant.
const ticketStateQuery = `query TicketState($id: String!) {
  issue(id: $id) {
    id
    state {
      id
      name
      type
    }
    sortOrder
    team {
      states {
        nodes {
          id
          name
          type
        }
      }
    }
  }
}`

// setTicketStateMutation moves one ticket to a state, writing its manual order
// back in the same update since a state change rewrites it. The reply carries
// the state and the order read back, so the move is only counted where the
// ticket is found where it was put.
const setTicketStateMutation = `mutation SetTicketState($id: String!, $state: String!, $order: Float!) {
  issueUpdate(id: $id, input: { stateId: $state, sortOrder: $order }) {
    success
    issue {
      state {
        id
      }
      sortOrder
    }
  }
}`

// Linear reads the issues delegated to one Linear agent, and delegates the ones
// the product takes on itself.
type Linear struct {
	Endpoint string
	Tokens   linear.TokenSource
	// Delegate is the agent the poll acts for, the id Linear's viewer query
	// must return for every page, and the delegate a delegation sets.
	Delegate string
	Client   *http.Client
	Page     int
}

// linearState is one issue's workflow state: the type tells a blocker still
// open from a finished one, and the id and name are what a claim's move reads —
// the id it names a team's state by, and the name it resolves that state with.
type linearState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// linearTicket is one ticket as the state query carries it: Linear's own id for
// it, which the move names, the state it holds, the manual order a state change
// rewrites, and its team's workflow states.
type linearTicket struct {
	ID        string      `json:"id"`
	State     linearState `json:"state"`
	SortOrder float64     `json:"sortOrder"`
	Team      struct {
		States struct {
			Nodes []linearState `json:"nodes"`
		} `json:"states"`
	} `json:"team"`
}

// linearMilestone is the project milestone a reply carries for one issue.
type linearMilestone struct {
	ID        string  `json:"id"`
	SortOrder float64 `json:"sortOrder"`
}

// linearDelegate is the agent a reply names an issue's delegate as.
type linearDelegate struct {
	ID string `json:"id"`
}

// linearIssue is one issue as a reply carries it: the fields the delegated query
// asks for, and the ones the open query adds, which a reply without them leaves
// zero.
type linearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	Labels      struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	State     linearState      `json:"state"`
	Delegate  *linearDelegate  `json:"delegate"`
	Milestone *linearMilestone `json:"projectMilestone"`
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
				Identifier string      `json:"identifier"`
				State      linearState `json:"state"`
			} `json:"issue"`
		} `json:"nodes"`
	} `json:"inverseRelations"`
}

// linearResponse is one GraphQL reply's data: the viewer proves which agent the
// token reads as, and the issues are one page of what the query asked for.
type linearResponse struct {
	Data struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
		Issues struct {
			Nodes    []linearIssue `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `json:"issues"`
	} `json:"data"`
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
		if err := l.checkViewer(resp.Data.Viewer.ID); err != nil {
			return nil, err
		}
		for _, node := range resp.Data.Issues.Nodes {
			issues = append(issues, node.issue())
		}
		if !resp.Data.Issues.PageInfo.HasNextPage {
			return issues, nil
		}
		cursor = resp.Data.Issues.PageInfo.EndCursor
	}
}

// Open returns every open issue, excluding the ones in a completed or canceled
// state. A poll walks them for the tickets it delegates to itself: the milestone
// each belongs to is what orders them, so a ticket already delegated is returned
// too. The token's own visibility bounds the read, as it bounds the delegated
// one, and it fails unless the token reads as the configured agent.
func (l Linear) Open(ctx context.Context) ([]Issue, error) {
	page := l.Page
	if page <= 0 {
		page = linearPage
	}
	var (
		issues []Issue
		cursor string
	)
	for {
		resp, err := l.openPage(ctx, cursor, page)
		if err != nil {
			return nil, err
		}
		if err := l.checkViewer(resp.Data.Viewer.ID); err != nil {
			return nil, err
		}
		for _, node := range resp.Data.Issues.Nodes {
			issues = append(issues, node.issue())
		}
		if !resp.Data.Issues.PageInfo.HasNextPage {
			return issues, nil
		}
		cursor = resp.Data.Issues.PageInfo.EndCursor
	}
}

// DelegateIssue sets issueID's delegate to the agent this client acts for, and
// fails unless Linear reports the issue delegated to that agent.
func (l Linear) DelegateIssue(ctx context.Context, issueID string) error {
	var resp struct {
		Data struct {
			IssueUpdate struct {
				Success bool `json:"success"`
				Issue   struct {
					Delegate *linearDelegate `json:"delegate"`
				} `json:"issue"`
			} `json:"issueUpdate"`
		} `json:"data"`
	}
	variables := struct {
		ID       string `json:"id"`
		Delegate string `json:"delegate"`
	}{ID: issueID, Delegate: l.Delegate}
	if err := l.request(ctx, delegateMutation, variables, &resp.Data); err != nil {
		return fmt.Errorf("delegating %s: %w", issueID, err)
	}
	delegate := resp.Data.IssueUpdate.Issue.Delegate
	if !resp.Data.IssueUpdate.Success || delegate == nil || delegate.ID != l.Delegate {
		return fmt.Errorf("delegating %s: Linear did not report it delegated to %s", issueID, l.Delegate)
	}
	return nil
}

// StartTicket sets a claimed ticket's state In Progress and reports the state
// it held before, which an undone claim restores the ticket to. Only a ticket
// nobody has begun is moved: one already started — In Review among them, which
// Linear's own pull-request automation sets — finished or canceled is left
// where it is. An empty report says the claim moved nothing, so a requeue or an
// abandon has nothing of its own to undo.
func (l Linear) StartTicket(ctx context.Context, ticketID string) (string, error) {
	ticket, err := l.ticketState(ctx, ticketID)
	if err != nil {
		return "", err
	}
	target, ok := inProgressState(ticket.Team.States.Nodes)
	if !ok || ticket.State.ID == target.ID || !slices.Contains(unstartedStates, ticket.State.Type) {
		return "", nil
	}
	if err := l.setTicketState(ctx, ticket.ID, target.ID, ticket.SortOrder); err != nil {
		return "", err
	}
	return ticket.State.ID, nil
}

// RestoreTicket returns a claimed ticket to stateID, the state it held before
// the claim, writing its manual order back as StartTicket does. A ticket that
// has left the In Progress state its claim set — whether Linear's own
// pull-request automation moved it or the owner did — is left where it is.
func (l Linear) RestoreTicket(ctx context.Context, ticketID, stateID string) error {
	if stateID == "" {
		return nil
	}
	ticket, err := l.ticketState(ctx, ticketID)
	if err != nil {
		return err
	}
	if current, ok := inProgressState(ticket.Team.States.Nodes); !ok || ticket.State.ID != current.ID {
		return nil
	}
	return l.setTicketState(ctx, ticket.ID, stateID, ticket.SortOrder)
}

// inProgressState is the team's In Progress state: the one named so, since a
// team's own states are the only authority on which id that is.
func inProgressState(states []linearState) (linearState, bool) {
	for _, state := range states {
		if strings.EqualFold(strings.TrimSpace(state.Name), inProgressStateName) {
			return state, true
		}
	}
	return linearState{}, false
}

// ticketState reads the ticket's own id, its state, its manual order and its
// team's workflow states.
func (l Linear) ticketState(ctx context.Context, ticketID string) (linearTicket, error) {
	var resp struct {
		Data struct {
			Issue *linearTicket `json:"issue"`
		} `json:"data"`
	}
	if err := l.request(ctx, ticketStateQuery, map[string]string{"id": ticketID}, &resp.Data); err != nil {
		return linearTicket{}, fmt.Errorf("reading the state of %s: %w", ticketID, err)
	}
	if resp.Data.Issue == nil {
		return linearTicket{}, fmt.Errorf("reading the state of %s: Linear has no such ticket", ticketID)
	}
	return *resp.Data.Issue, nil
}

// setTicketState moves the ticket to stateID, writing order back with it, and
// fails unless Linear reports the ticket in that state, at that order.
func (l Linear) setTicketState(ctx context.Context, issueID, stateID string, order float64) error {
	var resp struct {
		Data struct {
			IssueUpdate struct {
				Success bool `json:"success"`
				Issue   struct {
					State     linearState `json:"state"`
					SortOrder float64     `json:"sortOrder"`
				} `json:"issue"`
			} `json:"issueUpdate"`
		} `json:"data"`
	}
	variables := struct {
		ID    string  `json:"id"`
		State string  `json:"state"`
		Order float64 `json:"order"`
	}{ID: issueID, State: stateID, Order: order}
	if err := l.request(ctx, setTicketStateMutation, variables, &resp.Data); err != nil {
		return fmt.Errorf("moving %s to state %s: %w", issueID, stateID, err)
	}
	update := resp.Data.IssueUpdate
	if !update.Success || update.Issue.State.ID != stateID || update.Issue.SortOrder != order {
		return fmt.Errorf("moving %s to state %s: Linear reported %s at order %v, want %s at %v",
			issueID, stateID, update.Issue.State.ID, update.Issue.SortOrder, stateID, order)
	}
	return nil
}

// issue is the issue one reply's node names.
func (n linearIssue) issue() Issue {
	issue := Issue{
		ID:        n.Identifier,
		URL:       n.URL,
		Title:     n.Title,
		Body:      n.Description,
		Priority:  n.Priority,
		UUID:      n.ID,
		StateType: n.State.Type,
	}
	if n.Delegate != nil {
		issue.DelegatedTo = n.Delegate.ID
	}
	if n.Milestone != nil {
		issue.Milestone = Milestone{ID: n.Milestone.ID, SortOrder: n.Milestone.SortOrder}
	}
	for _, label := range n.Labels.Nodes {
		issue.Labels = append(issue.Labels, label.Name)
	}
	for _, rel := range n.Relations.Nodes {
		if rel.Type == blocksRelation {
			issue.Blocks = append(issue.Blocks, rel.RelatedIssue.Identifier)
		}
	}
	for _, rel := range n.InverseRelations.Nodes {
		if rel.Type == blocksRelation {
			issue.BlockedBy = append(issue.BlockedBy, rel.Issue.Identifier)
			if blockerOpen(rel.Issue.State.Type) {
				issue.BlockedByOpen = append(issue.BlockedByOpen, rel.Issue.Identifier)
				if issue.BlockerStates == nil {
					issue.BlockerStates = map[string]string{}
				}
				issue.BlockerStates[rel.Issue.Identifier] = rel.Issue.State.Type
			}
		}
	}
	return issue
}

// blockerOpen reports whether a blocked-by issue Linear relates to a ticket
// still holds it: every state type but the two that mean finished, and a state
// the reply did not carry, which counts as open.
func blockerOpen(stateType string) bool {
	return stateType != stateTypeCompleted && stateType != stateTypeCanceled
}

// checkViewer fails unless the viewer a reply carried is the configured
// delegate, which is what proves the token reads as that agent.
func (l Linear) checkViewer(id string) error {
	if id == l.Delegate {
		return nil
	}
	return fmt.Errorf("%w: the Linear token reads as %s, not the configured delegate %s",
		ErrDelegateMismatch, id, l.Delegate)
}

// request posts one operation with the poll's token, minting a successor when
// Linear refuses it.
func (l Linear) request(ctx context.Context, query string, variables, out any) error {
	return linear.DoRefreshing(ctx, l.Tokens, func(token string) error {
		api := linear.Client{Endpoint: l.Endpoint, Token: token, HTTP: l.Client, MaxReply: maxLinearResponseBytes}
		return api.Do(ctx, query, variables, out)
	})
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
	err := l.request(ctx, delegatedQuery, variables, &resp.Data)
	return resp, err
}

// openPage asks Linear for one page of the open issues, with the cursor omitted
// where page omits it.
func (l Linear) openPage(ctx context.Context, after string, size int) (linearResponse, error) {
	var resp linearResponse
	variables := struct {
		First int    `json:"first"`
		After string `json:"after,omitempty"`
	}{First: size, After: after}
	err := l.request(ctx, openQuery, variables, &resp.Data)
	return resp, err
}
