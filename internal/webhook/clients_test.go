package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/linear"
)

// linearServer answers every request with reply and keeps the last request body.
func linearServer(t *testing.T, status int, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token-1" {
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
	l := Linear{Endpoint: srv.URL, Token: fixedSecret("token-1")}
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
			if err := (Linear{Endpoint: srv.URL, Token: fixedSecret("token-1")}).Acknowledge(context.Background(), "session-1"); err == nil {
				t.Fatal("acknowledged")
			}
		})
	}
}

// rotatingServer refuses every token but "token-2", the way Linear refuses a
// token that has expired.
func rotatingServer(t *testing.T, refusal string, status int) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "token-2" {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, refusal)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"agentActivityCreate":{"success":true}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestAcknowledgeRereadsARefusedToken(t *testing.T) {
	for _, tc := range []struct {
		name, refusal string
		status        int
	}{
		{"http 401", `{"errors":[{"message":"unauthorized"}]}`, 401},
		{"graphql authentication error", `{"errors":[{"message":"Authentication required","extensions":{"code":"AUTHENTICATION_ERROR"}}]}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, seen := rotatingServer(t, tc.refusal, tc.status)
			var reads int
			token := NewSecret("linear-token", "token-1", func(context.Context) (string, error) {
				reads++
				return "token-2", nil
			}, func() time.Time { return receivedAt }, slog.New(slog.DiscardHandler))
			l := Linear{Endpoint: srv.URL, Token: token}
			if err := l.Acknowledge(context.Background(), "session-1"); err != nil {
				t.Fatal(err)
			}
			if reads != 1 || strings.Join(*seen, ",") != "token-1,token-2" {
				t.Fatalf("reads %d, tokens sent %v", reads, *seen)
			}
		})
	}
}

func TestAcknowledgeDoesNotRereadInsideTheWindow(t *testing.T) {
	srv, seen := rotatingServer(t, `{}`, 401)
	now := receivedAt
	var reads int
	token := NewSecret("linear-token", "token-1", func(context.Context) (string, error) {
		reads++
		return "token-1", nil
	}, func() time.Time { return now }, slog.New(slog.DiscardHandler))
	l := Linear{Endpoint: srv.URL, Token: token}
	for range 2 {
		if err := l.Acknowledge(context.Background(), "session-1"); !errors.Is(err, linear.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
		now = now.Add(reloadEvery - time.Second)
	}
	if reads != 1 || len(*seen) != 2 {
		t.Fatalf("reads %d, requests %d: want one re-read and no retry with the same token", reads, len(*seen))
	}
}

func TestAnotherRefusalIsNotRetried(t *testing.T) {
	srv, seen := rotatingServer(t, `{"errors":[{"message":"session not found"}]}`, 200)
	var reads int
	token := NewSecret("linear-token", "token-1", func(context.Context) (string, error) {
		reads++
		return "token-2", nil
	}, func() time.Time { return receivedAt }, slog.New(slog.DiscardHandler))
	if err := (Linear{Endpoint: srv.URL, Token: token}).Acknowledge(context.Background(), "session-1"); err == nil {
		t.Fatal("acknowledged")
	}
	if reads != 0 || len(*seen) != 1 {
		t.Fatalf("reads %d, requests %d", reads, len(*seen))
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
