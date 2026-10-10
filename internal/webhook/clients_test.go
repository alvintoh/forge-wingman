package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/linear"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// linearServer answers every request with reply and keeps the last request body.
func linearServer(t *testing.T, status int, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestAcknowledgePostsAThought(t *testing.T) {
	srv, got := linearServer(t, 200, `{"data":{"agentActivityCreate":{"success":true}}}`)
	l := Linear{Endpoint: srv.URL, Tokens: heldToken("token-1")}
	if err := l.Acknowledge(context.Background(), "session-1"); err != nil {
		t.Fatal(err)
	}
	input := (*got)["variables"].(map[string]any)["input"].(map[string]any)
	content := input["content"].(map[string]any)
	if input["agentSessionId"] != "session-1" || content["type"] != "thought" {
		t.Errorf("input = %v", input)
	}
}

func TestLinearFailures(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		status      int
	}{
		{"no success", `{"data":{"agentActivityCreate":{"success":false}}}`, 200},
		{"graphql error beside data", `{"data":{"agentActivityCreate":{"success":true}},"errors":[{"message":"session not found"}]}`, 200},
		{"http error", `{}`, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := linearServer(t, tc.status, tc.reply)
			if err := (Linear{Endpoint: srv.URL, Tokens: heldToken("token-1")}).Acknowledge(context.Background(), "session-1"); err == nil {
				t.Fatal("acknowledged")
			}
		})
	}
}

// heldToken is a token that is never refreshed.
type heldToken string

func (h heldToken) Token(context.Context) (string, error) { return string(h), nil }

func (h heldToken) Refresh(context.Context, string) (string, error) {
	return "", errors.New("a held token is not refreshed")
}

func TestAcknowledgeRetriesWithAReMintedToken(t *testing.T) {
	var mints int
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mints++
		_, _ = fmt.Fprintf(w, `{"access_token":"token-%d"}`, mints)
	}))
	t.Cleanup(mint.Close)
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer token-2" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"errors":[{"message":"unauthorized"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"agentActivityCreate":{"success":true}}}`)
	}))
	t.Cleanup(srv.Close)
	tokens := linear.NewCachedSource(linear.Credentials{ClientID: "client-1", ClientSecret: "secret-1",
		TokenURL: mint.URL, HTTP: mint.Client()}, func() time.Time { return receivedAt })
	if err := (Linear{Endpoint: srv.URL, Tokens: tokens}).Acknowledge(context.Background(), "session-1"); err != nil {
		t.Fatal(err)
	}
	if mints != 2 || strings.Join(sent, ",") != "Bearer token-1,Bearer token-2" {
		t.Fatalf("%d mints, tokens sent %v", mints, sent)
	}
}

func TestWakeReportsAnUnreachableJob(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if err := (Job{RunURI: srv.URL + "/v2/jobs/d:run", Client: srv.Client()}).Wake(context.Background()); err == nil {
		t.Fatal("woke an unreachable job")
	}
}

func TestWake(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"accepted", 200, false},
		{"refused", 403, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var method, path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.Path
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"name":"operations/1"}`)
			}))
			t.Cleanup(srv.Close)
			err := Job{RunURI: srv.URL + "/v2/jobs/d:run", Client: srv.Client()}.Wake(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if method != http.MethodPost || !strings.HasSuffix(path, ":run") {
				t.Errorf("%s %s", method, path)
			}
		})
	}
}

// TestElicitAsksASelectionActivity covers AC2: one elicitation activity, the
// select signal, the recommendation first, each option carrying its cost.
func TestElicitAsksASelectionActivity(t *testing.T) {
	srv, got := linearServer(t, 200, `{"data":{"agentActivityCreate":{"success":true}}}`)
	l := Linear{Endpoint: srv.URL, Tokens: heldToken("token-1")}
	d := runner.Decision{
		Question: "Which flag should the run use?",
		Context:  []string{"line one", "line two"},
		Options: []runner.DecisionOption{
			{Label: "Reuse the existing flag", Value: "reuse", Cost: "no new config"},
			{Label: "Add a new flag", Value: "new", Cost: "one more env var"},
		},
	}
	if err := l.Elicit(context.Background(), "session-1", d); err != nil {
		t.Fatal(err)
	}
	input := (*got)["variables"].(map[string]any)["input"].(map[string]any)
	if input["agentSessionId"] != "session-1" {
		t.Errorf("agentSessionId = %v", input["agentSessionId"])
	}
	content := input["content"].(map[string]any)
	if content["type"] != "elicitation" || content["signal"] != "select" {
		t.Errorf("content = %v", content)
	}
	if content["body"] != "Which flag should the run use?\n\nline one\nline two" {
		t.Errorf("body = %q", content["body"])
	}
	options := content["signalMetadata"].(map[string]any)["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("options = %v", options)
	}
	first := options[0].(map[string]any)
	if first["label"] != "Reuse the existing flag — no new config" || first["value"] != "reuse" {
		t.Errorf("first option = %v", first)
	}
	second := options[1].(map[string]any)
	if second["label"] != "Add a new flag — one more env var" || second["value"] != "new" {
		t.Errorf("second option = %v", second)
	}
}

func TestElicitReportsNoSuccess(t *testing.T) {
	srv, _ := linearServer(t, 200, `{"data":{"agentActivityCreate":{"success":false}}}`)
	err := (Linear{Endpoint: srv.URL, Tokens: heldToken("token-1")}).Elicit(context.Background(), "session-1",
		runner.Decision{Question: "Q?"})
	if err == nil {
		t.Fatal("asked with no success reported")
	}
}
