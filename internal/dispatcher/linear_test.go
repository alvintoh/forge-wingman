package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	if got := issues[0].BlockerStates; len(got) != 2 || got["run-started"] != "started" || got["run-unread"] != "" {
		t.Fatalf("BlockerStates = %v, want the state type of each open blocker", got)
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

// TestOnlyTheLinearClientHoldsTheDelegateMutation: no non-test source file of the
// package but the Linear client carries the GraphQL input that sets an issue's
// delegate, so a delegation goes through DelegateIssue, which only the owner's
// start request calls.
func TestOnlyTheLinearClientHoldsTheDelegateMutation(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "linear.go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "delegateId") {
				t.Errorf("%s sets a Linear delegate: %s", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
}

// TestPollWritesNothingToLinearWhenNothingIsClaimed: a poll with nothing to claim
// only reads the issues delegated to the agent.
func TestPollWritesNothingToLinearWhenNothingIsClaimed(t *testing.T) {
	srv, seen := queryServer(t, linearReply("agent-1", false, ""))
	source := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1", Client: srv.Client()}
	if _, err := Poll(context.Background(), pollDeps(source, &fakeQueue{}, &fakeWorkflow{}), buildConfig); err != nil {
		t.Fatal(err)
	}
	if len(*seen) == 0 {
		t.Fatal("the poll made no Linear request")
	}
	for _, call := range *seen {
		if strings.Contains(call.query, "mutation") || strings.Contains(call.query, "delegateId") {
			t.Errorf("the poll wrote to Linear: %s", call.query)
		}
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

// ticketReply is one ticket as the state query's reply carries it: its own
// Linear id, the state it holds, the manual order a state change rewrites, and
// its team's own workflow states.
func ticketReply(uuid, stateID, stateName, stateType string, order float64, team ...map[string]any) map[string]any {
	issue := map[string]any{
		"id":        uuid,
		"state":     map[string]any{"id": stateID, "name": stateName, "type": stateType},
		"sortOrder": order,
		"team":      map[string]any{"states": map[string]any{"nodes": team}},
	}
	return map[string]any{"data": map[string]any{"issue": issue}}
}

// stateNode is one workflow state of a ticket's team.
func stateNode(id, name, kind string) map[string]any {
	return map[string]any{"id": id, "name": name, "type": kind}
}

// setStateReply is what the move's mutation answers: the state the ticket reads
// back as, and the order it reads back at.
func setStateReply(success bool, stateID string, order float64) map[string]any {
	issue := map[string]any{"state": map[string]any{"id": stateID}, "sortOrder": order}
	return map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": success, "issue": issue}}}
}

// teamStates is a team's workflow as the ticket reply carries it: In Review is a
// started state beside In Progress, so a move has to tell them apart by name.
func teamStates() []map[string]any {
	return []map[string]any{
		stateNode("state-backlog", "Backlog", "backlog"),
		stateNode("state-todo", "Todo", "unstarted"),
		stateNode("state-progress", "In Progress", "started"),
		stateNode("state-review", "In Review", "started"),
		stateNode("state-done", "Done", "completed"),
		stateNode("state-canceled", "Canceled", "canceled"),
	}
}

// TestLinearStartTicketMovesAnUnstartedTicketToItsTeamsInProgressState: the
// state id is resolved from the ticket's own team, never carried as a constant,
// and the manual order a state change rewrites is written with the move and read
// back.
func TestLinearStartTicketMovesAnUnstartedTicketToItsTeamsInProgressState(t *testing.T) {
	srv, seen := queryServer(t,
		ticketReply("uuid-18", "state-todo", "Todo", "unstarted", 12.5, teamStates()...),
		setStateReply(true, "state-progress", 12.5))
	prior, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.StartTicket(context.Background(), "FRG-18")
	if err != nil {
		t.Fatal(err)
	}
	if prior != "state-todo" {
		t.Fatalf("prior = %q, want the state the ticket held before the move", prior)
	}
	if len(*seen) != 2 {
		t.Fatalf("requests = %+v, want the read and the move", *seen)
	}
	for _, field := range []string{"team {", "states {", "sortOrder"} {
		if !strings.Contains((*seen)[0].query, field) {
			t.Errorf("the state query does not ask for %s: %s", field, (*seen)[0].query)
		}
	}
	if got := (*seen)[1].variables; got["id"] != "uuid-18" || got["state"] != "state-progress" || got["order"] != 12.5 {
		t.Fatalf("variables = %v, want the ticket's own id, its team's In Progress state and its order", got)
	}
	for _, field := range []string{"stateId: $state", "sortOrder: $order", "$order: Float!"} {
		if !strings.Contains((*seen)[1].query, field) {
			t.Errorf("the move does not carry %s: %s", field, (*seen)[1].query)
		}
	}
}

// TestLinearStartTicketLeavesATicketThatIsNotUnstartedAlone: In Review is a
// started state beside In Progress, and a ticket holding one — or a finished or
// canceled one — is not touched.
func TestLinearStartTicketLeavesATicketThatIsNotUnstartedAlone(t *testing.T) {
	for name, held := range map[string]struct{ id, stateName, kind string }{
		"in progress": {"state-progress", "In Progress", "started"},
		"in review":   {"state-review", "In Review", "started"},
		"done":        {"state-done", "Done", "completed"},
		"canceled":    {"state-canceled", "Canceled", "canceled"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, seen := queryServer(t, ticketReply("uuid-18", held.id, held.stateName, held.kind, 1, teamStates()...))
			prior, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
				Client: srv.Client()}.StartTicket(context.Background(), "FRG-18")
			if err != nil {
				t.Fatal(err)
			}
			if prior != "" {
				t.Fatalf("prior = %q, want no state reported for a ticket the claim did not move", prior)
			}
			if len(*seen) != 1 {
				t.Fatalf("requests = %+v, want the read alone", *seen)
			}
		})
	}
}

// TestLinearStartTicketReportsAMoveLinearDidNotTake: a reply reading the ticket
// back anywhere but where it was put — or at another order — is not a move the
// dispatcher may count as made.
func TestLinearStartTicketReportsAMoveLinearDidNotTake(t *testing.T) {
	for name, reply := range map[string]map[string]any{
		"the move was refused":   setStateReply(false, "state-progress", 12.5),
		"the state is unchanged": setStateReply(true, "state-todo", 12.5),
		"the order moved":        setStateReply(true, "state-progress", 99),
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := queryServer(t,
				ticketReply("uuid-18", "state-todo", "Todo", "unstarted", 12.5, teamStates()...), reply)
			prior, err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
				Client: srv.Client()}.StartTicket(context.Background(), "FRG-18")
			if err == nil || prior != "" {
				t.Fatalf("prior = %q, err = %v, want the move Linear did not take reported", prior, err)
			}
		})
	}
}

// TestLinearRestoreTicketPutsAStartedTicketBackWhereItWas: the restore writes the
// state the claim recorded, with the order the ticket holds now.
func TestLinearRestoreTicketPutsAStartedTicketBackWhereItWas(t *testing.T) {
	srv, seen := queryServer(t,
		ticketReply("uuid-18", "state-progress", "In Progress", "started", 3, teamStates()...),
		setStateReply(true, "state-todo", 3))
	err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.RestoreTicket(context.Background(), "FRG-18", "state-todo")
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 {
		t.Fatalf("requests = %+v, want the read and the move", *seen)
	}
	if got := (*seen)[1].variables; got["state"] != "state-todo" || got["order"] != 3.0 {
		t.Fatalf("variables = %v, want the recorded state at the order the ticket holds", got)
	}
}

// TestLinearRestoreTicketLeavesATicketThatMovedOnAlone: a ticket Linear's own
// pull-request automation took to In Review, or the owner moved by hand, is not
// dragged back to where the claim found it.
func TestLinearRestoreTicketLeavesATicketThatMovedOnAlone(t *testing.T) {
	srv, seen := queryServer(t, ticketReply("uuid-18", "state-review", "In Review", "started", 3, teamStates()...))
	err := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Delegate: "agent-1",
		Client: srv.Client()}.RestoreTicket(context.Background(), "FRG-18", "state-todo")
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("requests = %+v, want the read alone", *seen)
	}
}

// TestLinearRestoreTicketWritesNothingWithNoStateToPutBack: a claim that moved
// nothing has nothing to undo, so no request is made at all.
func TestLinearRestoreTicketWritesNothingWithNoStateToPutBack(t *testing.T) {
	srv, seen := queryServer(t)
	client := Linear{Endpoint: linearURL(srv), Tokens: fixedToken("linear-token"), Client: srv.Client()}
	if err := client.RestoreTicket(context.Background(), "FRG-18", ""); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 0 {
		t.Fatalf("requests = %+v, want nothing written for a claim that moved nothing", *seen)
	}
}
