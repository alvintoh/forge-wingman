package linear

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestIssueReadsTheIssueByItsIdentifier(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		asked = req.Variables.ID
		_, _ = w.Write([]byte(`{"data":{"issue":{"identifier":"ABC-12","title":"Add x","description":"AC1","labels":{"nodes":[{"name":"size:S"}]}}}}`))
	}))
	t.Cleanup(srv.Close)
	issue, err := Client{Endpoint: srv.URL, Token: "token-1"}.Issue(context.Background(), "ABC-12")
	if err != nil {
		t.Fatal(err)
	}
	if asked != "ABC-12" || issue.Title != "Add x" || issue.Description != "AC1" || !slices.Equal(issue.Labels, []string{"size:S"}) {
		t.Fatalf("asked %q, issue = %+v", asked, issue)
	}
}

// serveIssue answers the issue query with issueReply and the viewer's teams
// query with teamsReply, counting the teams queries.
func serveIssue(t *testing.T, issueReply, teamsReply string) (string, *int) {
	t.Helper()
	teamsAsked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		if strings.HasPrefix(req.Query, "query ViewerTeams") {
			teamsAsked++
			_, _ = io.WriteString(w, teamsReply)
			return
		}
		_, _ = io.WriteString(w, issueReply)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &teamsAsked
}

const (
	entityNotFoundReply = `{"errors":[{"message":"Entity not found: Issue","extensions":{"code":"INVALID_INPUT"}}],"data":null}`
	oneTeamReply        = `{"data":{"viewer":{"teams":{"nodes":[{"id":"team-1"}]}}}}`
	noTeamReply         = `{"data":{"viewer":{"teams":{"nodes":[]}}}}`
)

func TestIssueReportsAMissingIssue(t *testing.T) {
	for name, reply := range map[string]string{
		"null":                 `{"data":{"issue":null}}`,
		"no data in the reply": `{}`,
		"entity not found":     entityNotFoundReply,
	} {
		t.Run(name, func(t *testing.T) {
			endpoint, _ := serveIssue(t, reply, oneTeamReply)
			_, err := Client{Endpoint: endpoint, Token: "token-1"}.Issue(context.Background(), "ABC-12")
			if !errors.Is(err, ErrIssueNotFound) {
				t.Fatalf("err = %v, want ErrIssueNotFound", err)
			}
		})
	}
}

func TestIssueReportsACallerInNoTeam(t *testing.T) {
	for name, teams := range map[string]string{
		"no team":   noTeamReply,
		"no viewer": `{"data":{"viewer":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			endpoint, _ := serveIssue(t, entityNotFoundReply, teams)
			_, err := Client{Endpoint: endpoint, Token: "token-1"}.Issue(context.Background(), "ABC-12")
			if !errors.Is(err, ErrNoTeam) || errors.Is(err, ErrIssueNotFound) {
				t.Fatalf("err = %v, want ErrNoTeam alone", err)
			}
			if strings.Contains(err.Error(), "token-1") {
				t.Fatalf("err = %q names the token", err)
			}
		})
	}
}

func TestIssueFailsWhenTheTeamsCannotBeRead(t *testing.T) {
	endpoint, _ := serveIssue(t, entityNotFoundReply, `{"errors":[{"message":"rate limited"}]}`)
	_, err := Client{Endpoint: endpoint, Token: "token-1"}.Issue(context.Background(), "ABC-12")
	if err == nil || errors.Is(err, ErrIssueNotFound) || errors.Is(err, ErrNoTeam) {
		t.Fatalf("err = %v, want the teams query's failure", err)
	}
}

func TestIssuePassesAnotherRefusalThroughWithoutAskingForTeams(t *testing.T) {
	endpoint, teamsAsked := serveIssue(t, `{"errors":[{"message":"Argument Validation Error"}]}`, noTeamReply)
	_, err := Client{Endpoint: endpoint, Token: "token-1"}.Issue(context.Background(), "ABC-12")
	if err == nil || errors.Is(err, ErrIssueNotFound) || errors.Is(err, ErrNoTeam) || *teamsAsked != 0 {
		t.Fatalf("err = %v after %d teams queries, want the refusal and none", err, *teamsAsked)
	}
}

func TestIssuePassesARefusalThrough(t *testing.T) {
	_, err := Client{Endpoint: serve(t, 401, `{}`), Token: "token-1"}.Issue(context.Background(), "ABC-12")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}
