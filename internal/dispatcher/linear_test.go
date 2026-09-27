package dispatcher

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

// linearServer answers the delegated query with the pages it is given, and
// records the variables each request carried.
func linearServer(t *testing.T, pages []map[string]any) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/graphql"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		if !strings.Contains(req.Query, "delegate: { id: { eq: $delegate } }") {
			t.Errorf("the query does not filter by delegate: %s", req.Query)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer linear-token" {
			t.Errorf("Authorization = %q", got)
		}
		seen = append(seen, req.Variables)
		page := pages[0]
		if len(pages) > 1 {
			pages = pages[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(page); err != nil {
			t.Errorf("encoding the reply: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// linearURL is where a Linear client of the test's own points.
func linearURL(srv *httptest.Server) string {
	return srv.URL + "/graphql"
}

func linearReply(viewer string, hasNext bool, cursor string, nodes ...map[string]any) map[string]any {
	return map[string]any{"data": map[string]any{
		"viewer": map[string]any{"id": viewer},
		"issues": map[string]any{
			"nodes":    nodes,
			"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
		},
	}}
}

func linearNode(id string, priority int, labels ...string) map[string]any {
	var names []map[string]any
	for _, l := range labels {
		names = append(names, map[string]any{"name": l})
	}
	return map[string]any{
		"identifier":  id,
		"title":       "feat(dispatcher): poll Linear",
		"description": "Enqueue delegated tickets.",
		"priority":    priority,
		"labels":      map[string]any{"nodes": names},
	}
}

func TestLinearReadsEveryDelegatedPage(t *testing.T) {
	srv, seen := linearServer(t, []map[string]any{
		linearReply("agent-1", true, "cursor-1", linearNode("FRG-18", 2, "size:M")),
		linearReply("agent-1", false, "", linearNode("FRG-19", 0, "size:L")),
	})
	issues, err := Linear{Endpoint: linearURL(srv), Token: "Bearer linear-token", Delegate: "agent-1",
		Client: srv.Client()}.Delegated(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].ID != "FRG-18" || issues[1].Priority != 0 {
		t.Fatalf("issues = %+v", issues)
	}
	if !slices.Equal(issues[0].Labels, []string{"size:M"}) || issues[0].Body == "" {
		t.Fatalf("issue = %+v", issues[0])
	}
	if len(*seen) != 2 || (*seen)[0]["after"] != nil || (*seen)[1]["after"] != "cursor-1" {
		t.Fatalf("pages asked with %v", *seen)
	}
}

func TestLinearRefusesATokenReadingAsAnotherAgent(t *testing.T) {
	srv, _ := linearServer(t, []map[string]any{linearReply("agent-2", false, "")})
	_, err := Linear{Endpoint: linearURL(srv), Token: "Bearer linear-token", Delegate: "agent-1",
		Client: srv.Client()}.Delegated(context.Background())
	if !errors.Is(err, ErrDelegateMismatch) {
		t.Fatalf("err = %v, want a delegate mismatch", err)
	}
}

func TestLinearReportsAQueryItCannotRun(t *testing.T) {
	for name, reply := range map[string]map[string]any{
		"errors beside the data":  {"errors": []map[string]any{{"message": "Invalid delegate id"}}},
		"a status that is not OK": nil,
		"a body that is not JSON": nil,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				switch name {
				case "a status that is not OK":
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, "unauthorized")
				case "a body that is not JSON":
					_, _ = io.WriteString(w, "<html>gateway</html>")
				default:
					_ = json.NewEncoder(w).Encode(reply)
				}
			}))
			defer srv.Close()
			_, err := Linear{Endpoint: linearURL(srv), Token: "t", Delegate: "agent-1",
				Client: srv.Client()}.Delegated(context.Background())
			if err == nil {
				t.Fatal("admitted a reply it cannot use")
			}
		})
	}
}
