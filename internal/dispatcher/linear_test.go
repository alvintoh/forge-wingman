package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/linear"
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
		if !strings.Contains(req.Query, "$delegate: ID!") {
			t.Errorf("the query declares $delegate as something other than ID!, which Linear rejects for an id filter: %s", req.Query)
		}
		for _, field := range []string{" relations {", " inverseRelations {", " url\n", " state {"} {
			if !strings.Contains(req.Query, field) {
				t.Errorf("the query does not ask for%s: %s", field, req.Query)
			}
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
		"url":         "https://linear.app/w/issue/" + id,
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
	issues, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.Delegated(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].ID != "FRG-18" || issues[1].Priority != 0 {
		t.Fatalf("issues = %+v", issues)
	}
	if !slices.Equal(issues[0].Labels, []string{"size:M"}) || issues[0].Body == "" || issues[0].URL != "https://linear.app/w/issue/"+issues[0].ID {
		t.Fatalf("issue = %+v", issues[0])
	}
	if len(*seen) != 2 || (*seen)[0]["after"] != nil || (*seen)[1]["after"] != "cursor-1" {
		t.Fatalf("pages asked with %v", *seen)
	}
}

func TestLinearRefusesATokenReadingAsAnotherAgent(t *testing.T) {
	srv, _ := linearServer(t, []map[string]any{linearReply("agent-2", false, "")})
	_, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
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
			_, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("t"), Delegate: "agent-1",
				Client: srv.Client()}.Delegated(context.Background())
			if err == nil || errors.Is(err, ErrDelegateMismatch) {
				t.Fatalf("err = %v, want the query's own failure", err)
			}
		})
	}
}

func TestLinearReadsBlockingRelationsInBothDirections(t *testing.T) {
	node := linearNode("run-a", 2, "size:M")
	node["relations"] = map[string]any{"nodes": []map[string]any{
		{"type": "blocks", "relatedIssue": map[string]any{"identifier": "run-x"}},
		{"type": "related", "relatedIssue": map[string]any{"identifier": "run-y"}},
	}}
	node["inverseRelations"] = map[string]any{"nodes": []map[string]any{
		{"type": "blocks", "issue": map[string]any{"identifier": "run-p"}},
		{"type": "duplicate", "issue": map[string]any{"identifier": "run-q"}},
	}}
	srv, _ := linearServer(t, []map[string]any{linearReply("agent-1", false, "", node, linearNode("run-b", 0, "size:L"))})
	issues, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.Delegated(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(issues[0].Blocks, []string{"run-x"}) || !slices.Equal(issues[0].BlockedBy, []string{"run-p"}) {
		t.Fatalf("issue = %+v, want only the blocking relations kept", issues[0])
	}
	if len(issues[1].Blocks) != 0 || len(issues[1].BlockedBy) != 0 {
		t.Fatalf("issue = %+v, want no relations for an issue with none", issues[1])
	}
}

// TestLinearReadsWhetherABlockerIsDone is AC1, AC2 and AC4: a blocked-by issue
// in any state but completed or canceled still holds the ticket, one in those
// two does not, and one whose state the reply did not carry counts as open.
func TestLinearReadsWhetherABlockerIsDone(t *testing.T) {
	node := linearNode("run-a", 2, "size:M")
	node["inverseRelations"] = map[string]any{"nodes": []map[string]any{
		{"type": "blocks", "issue": map[string]any{"identifier": "run-started", "state": map[string]any{"type": "started"}}},
		{"type": "blocks", "issue": map[string]any{"identifier": "run-completed", "state": map[string]any{"type": "completed"}}},
		{"type": "blocks", "issue": map[string]any{"identifier": "run-canceled", "state": map[string]any{"type": "canceled"}}},
		{"type": "blocks", "issue": map[string]any{"identifier": "run-unread"}},
	}}
	srv, _ := linearServer(t, []map[string]any{linearReply("agent-1", false, "", node)})
	issues, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.Delegated(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run-started", "run-completed", "run-canceled", "run-unread"}
	if !slices.Equal(issues[0].BlockedBy, want) {
		t.Fatalf("BlockedBy = %v, want every blocking relation kept in %v", issues[0].BlockedBy, want)
	}
	wantOpen := []string{"run-started", "run-unread"}
	if !slices.Equal(issues[0].BlockedByOpen, wantOpen) {
		t.Fatalf("BlockedByOpen = %v, want %v", issues[0].BlockedByOpen, wantOpen)
	}
}

// queryServer answers each request with the next reply, recording every request's
// query, variables and token.
func queryServer(t *testing.T, replies ...map[string]any) (*httptest.Server, *[]queryCall) {
	t.Helper()
	var seen []queryCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		seen = append(seen, queryCall{query: req.Query, variables: req.Variables, auth: r.Header.Get("Authorization")})
		reply := replies[0]
		if len(replies) > 1 {
			replies = replies[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(reply); err != nil {
			t.Errorf("encoding the reply: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// queryCall is one request a Linear client made.
type queryCall struct {
	query     string
	variables map[string]any
	auth      string
}

// openReply is one page of the open issues query's reply.
func openReply(viewer string, hasNext bool, cursor string, nodes ...map[string]any) map[string]any {
	return map[string]any{"data": map[string]any{
		"viewer": map[string]any{"id": viewer},
		"issues": map[string]any{
			"nodes":    nodes,
			"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
		},
	}}
}

// openNode is one issue as the open query's reply carries it: a milestone and a
// delegate only where the test gives it one.
func openNode(id, uuid, milestone string, sortOrder float64, delegate string) map[string]any {
	node := map[string]any{
		"id":          uuid,
		"identifier":  id,
		"url":         "https://linear.app/w/issue/" + id,
		"title":       "[BE] add " + id,
		"description": "Add it.",
		"priority":    2,
		"state":       map[string]any{"type": "unstarted"},
		"labels":      map[string]any{"nodes": []map[string]any{{"name": "size:M"}, {"name": "repo:octo/scratch"}}},
	}
	if milestone != "" {
		node["projectMilestone"] = map[string]any{"id": milestone, "sortOrder": sortOrder}
	}
	if delegate != "" {
		node["delegate"] = map[string]any{"id": delegate}
	}
	return node
}

// delegateReply is what the delegate mutation answers.
func delegateReply(success bool, delegate string) map[string]any {
	issue := map[string]any{}
	if delegate != "" {
		issue["delegate"] = map[string]any{"id": delegate}
	}
	return map[string]any{"data": map[string]any{
		"issueUpdate": map[string]any{"success": success, "issue": issue},
	}}
}

// TestLinearReadsEveryOpenIssuePageWithWhatDelegatingNeeds: the open read carries
// the node id the delegation names, the milestone that orders the queue, and the
// delegate that marks a ticket already taken.
func TestLinearReadsEveryOpenIssuePageWithWhatDelegatingNeeds(t *testing.T) {
	srv, seen := queryServer(t,
		openReply("agent-1", true, "cursor-1", openNode("FRG-20", "uuid-20", "phase-1", 1, "agent-1")),
		openReply("agent-1", false, "", openNode("FRG-21", "uuid-21", "phase-2", 2, "")))
	issues, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %+v, want both pages", issues)
	}
	if first := issues[0]; first.ID != "FRG-20" || first.UUID != "uuid-20" || first.DelegatedTo != "agent-1" || first.StateType != "unstarted" ||
		first.Milestone != (Milestone{ID: "phase-1", SortOrder: 1}) {
		t.Fatalf("issue = %+v, want the id, the milestone and the delegate read", first)
	}
	if second := issues[1]; second.UUID != "uuid-21" || second.DelegatedTo != "" ||
		second.Milestone != (Milestone{ID: "phase-2", SortOrder: 2}) || len(second.Labels) != 2 {
		t.Fatalf("issue = %+v, want a ticket with no delegate read as free", second)
	}
	if len(*seen) != 2 || (*seen)[0].variables["after"] != nil || (*seen)[1].variables["after"] != "cursor-1" {
		t.Fatalf("pages asked with %v", *seen)
	}
	for _, field := range []string{"projectMilestone {", "state {", "sortOrder", "delegate {", "$first: Int"} {
		if !strings.Contains((*seen)[0].query, field) {
			t.Errorf("the open query does not ask for %s: %s", field, (*seen)[0].query)
		}
	}
}

func TestLinearOpenRefusesATokenReadingAsAnotherAgent(t *testing.T) {
	srv, _ := queryServer(t, openReply("agent-2", false, ""))
	_, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.Open(context.Background())
	if !errors.Is(err, ErrDelegateMismatch) {
		t.Fatalf("err = %v, want a delegate mismatch", err)
	}
}

// TestLinearDelegatesAnIssueToTheAgentItActsFor is the write: the mutation
// names the issue's own Linear id, not its identifier, and the configured
// delegate.
func TestLinearDelegatesAnIssueToTheAgentItActsFor(t *testing.T) {
	srv, seen := queryServer(t, delegateReply(true, "agent-1"))
	err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.DelegateIssue(context.Background(), "uuid-20")
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].auth != "Bearer linear-token" {
		t.Fatalf("requests = %+v, want one carrying the poll's token", *seen)
	}
	if got := (*seen)[0].variables; got["id"] != "uuid-20" || got["delegate"] != "agent-1" {
		t.Fatalf("variables = %v, want the issue's own id and the agent", got)
	}
	for _, field := range []string{"issueUpdate(", "delegateId: $delegate", "$delegate: String!", "success", "delegate {"} {
		if !strings.Contains((*seen)[0].query, field) {
			t.Errorf("the mutation does not carry %s: %s", field, (*seen)[0].query)
		}
	}
}

// TestLinearRefusesADelegationLinearDidNotTake: a reply that delegated the issue
// to some other agent is not a delegation the dispatcher may count.
func TestLinearRefusesADelegationLinearDidNotTake(t *testing.T) {
	srv, _ := queryServer(t, delegateReply(true, "agent-2"))
	err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.DelegateIssue(context.Background(), "uuid-20")
	if err == nil || !strings.Contains(err.Error(), "did not report it delegated to agent-1") {
		t.Fatalf("err = %v, want the reply read back before the delegation is reported", err)
	}
}

// TestLinearRefusesADelegationReplyThatNamesNoDelegate: a reply that says success
// without a delegate, or a delegate without success, is not a delegation.
func TestLinearRefusesADelegationReplyThatNamesNoDelegate(t *testing.T) {
	for name, reply := range map[string]map[string]any{
		"success with a null delegate": delegateReply(true, ""),
		"no success":                   delegateReply(false, "agent-1"),
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := queryServer(t, reply)
			err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
				Client: srv.Client()}.DelegateIssue(context.Background(), "uuid-20")
			if err == nil || !strings.Contains(err.Error(), "did not report it delegated") {
				t.Fatalf("err = %v, want the delegation refused", err)
			}
		})
	}
}

// TestLinearReportsTheDelegationLinearRefused: a GraphQL refusal is the query's
// own failure, and it names the issue it was asked to delegate.
func TestLinearReportsTheDelegationLinearRefused(t *testing.T) {
	srv, _ := queryServer(t, map[string]any{"errors": []map[string]any{
		{"message": "Cannot query field \"delegateId\" on type \"IssueUpdateInput\""},
	}})
	err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.DelegateIssue(context.Background(), "uuid-20")
	if err == nil || !strings.Contains(err.Error(), "delegateId") || !strings.Contains(err.Error(), "uuid-20") {
		t.Fatalf("err = %v, want Linear's own refusal beside the issue", err)
	}
}

// fixedToken is a token that is never refreshed.
type fixedToken string

func (f fixedToken) Token(context.Context) (string, error) { return string(f), nil }

func (f fixedToken) Refresh(context.Context, string) (string, error) {
	return "", errors.New("a fixed token is not refreshed")
}

// mintServer mints minted-1, minted-2, … or, with fail set, refuses every mint.
func mintServer(t *testing.T, fail bool) (linear.Credentials, *int) {
	t.Helper()
	var mints int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mints++
		_, _ = fmt.Fprintf(w, `{"access_token":"minted-%d"}`, mints)
	}))
	t.Cleanup(srv.Close)
	return linear.Credentials{ClientID: "client-1", ClientSecret: "secret-1", TokenURL: srv.URL, HTTP: srv.Client()}, &mints
}

// gatedServer answers the pages in order, refusing with a 401 each request
// refuse picks by its index and token; it records every Authorization header.
func gatedServer(t *testing.T, refuse func(n int, token string) bool, pages ...map[string]any) (*httptest.Server, *[]string) {
	t.Helper()
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		sent = append(sent, auth)
		if refuse != nil && refuse(len(sent)-1, strings.TrimPrefix(auth, "Bearer ")) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"errors":[{"message":"Authentication required","extensions":{"code":"AUTHENTICATION_ERROR"}}]}`)
			return
		}
		page := pages[0]
		pages = pages[1:]
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

func TestLinearMintsOncePerPollAndRefreshesARefusedTokenOnce(t *testing.T) {
	creds, mints := mintServer(t, false)
	expiresOnThirdPage := func(n int, token string) bool { return n >= 2 && token == "minted-1" }
	srv, sent := gatedServer(t, expiresOnThirdPage,
		linearReply("agent-1", true, "cursor-1", linearNode("ABC-1", 2, "size:M")),
		linearReply("agent-1", true, "cursor-2", linearNode("ABC-2", 2, "size:M")),
		linearReply("agent-1", false, "", linearNode("ABC-3", 2, "size:M")))
	l := Linear{Endpoint: srv.URL, Tokens: linear.NewPollSource(creds), Delegate: "agent-1", Client: srv.Client(), Page: 1}
	issues, err := l.Delegated(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := "Bearer minted-1,Bearer minted-1,Bearer minted-1,Bearer minted-2"
	if len(issues) != 3 || *mints != 2 || strings.Join(*sent, ",") != want {
		t.Fatalf("%d issues, %d mints, sent %v", len(issues), *mints, *sent)
	}
}

func TestPollAdmitsNothingWithoutAnAcceptedToken(t *testing.T) {
	page := func(cursor string) map[string]any {
		return linearReply("agent-1", cursor != "", cursor, linearNode("ABC-1", 2, "size:M", "repo:octo/scratch"))
	}
	for _, tc := range []struct {
		name      string
		failMint  bool
		refuse    func(n int, token string) bool
		wantMints int
		wantSent  int
	}{
		{name: "the mint fails", failMint: true},
		{name: "the refreshed token is refused too", refuse: func(int, string) bool { return true },
			wantMints: 2, wantSent: 2},
		{name: "a second refusal in the same poll", refuse: func(n int, token string) bool {
			return (n == 1 && token == "minted-1") || (n == 3 && token == "minted-2")
		}, wantMints: 2, wantSent: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds, mints := mintServer(t, tc.failMint)
			srv, sent := gatedServer(t, tc.refuse, page("cursor-1"), page("cursor-2"), page("cursor-3"), page(""))
			source := Linear{Endpoint: srv.URL, Tokens: linear.NewPollSource(creds), Delegate: "agent-1",
				Client: srv.Client(), Page: 1}
			q := &fakeQueue{}
			w := &fakeWorkflow{}
			if _, err := Poll(context.Background(), pollDeps(source, q, w), buildConfig); err == nil {
				t.Fatal("polled without an accepted token")
			}
			if len(q.queued) != 0 || len(q.rejected) != 0 || len(w.claims) != 0 {
				t.Fatalf("queued %v, rejected %v, dispatched %v", q.queued, q.rejected, w.claims)
			}
			if *mints != tc.wantMints || len(*sent) != tc.wantSent {
				t.Fatalf("%d mints, %d queries", *mints, len(*sent))
			}
		})
	}
}
