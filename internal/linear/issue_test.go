package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestIssueReportsAMissingIssue(t *testing.T) {
	for name, reply := range map[string]string{
		"null":                 `{"data":{"issue":null}}`,
		"no data in the reply": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Client{Endpoint: serve(t, 200, reply), Token: "token-1"}.Issue(context.Background(), "ABC-12")
			if !errors.Is(err, ErrIssueNotFound) {
				t.Fatalf("err = %v, want ErrIssueNotFound", err)
			}
		})
	}
}

func TestIssuePassesARefusalThrough(t *testing.T) {
	_, err := Client{Endpoint: serve(t, 401, `{}`), Token: "token-1"}.Issue(context.Background(), "ABC-12")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}
