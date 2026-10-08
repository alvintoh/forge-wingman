package linear

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func serve(t *testing.T, status int, reply string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Errorf("Authorization = %q, want the token as a bearer token", got)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestDoDecodesTheData(t *testing.T) {
	var out struct {
		Issue struct {
			Identifier string `json:"identifier"`
		} `json:"issue"`
	}
	c := Client{Endpoint: serve(t, 200, `{"data":{"issue":{"identifier":"ABC-1"}}}`), Token: "token-1"}
	if err := c.Do(context.Background(), "query", nil, &out); err != nil || out.Issue.Identifier != "ABC-1" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

func TestDoFails(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want string
		status, max       int
	}{
		{"errors beside data", `{"data":{"issue":null},"errors":[{"message":"no access"}]}`, "the Linear API refused the query: no access", 200, 0},
		{"refused status", "  denied\n ", "the Linear API answered 403 Forbidden: denied", 403, 0},
		{"undecodable reply", `{"data":`, "decoding Linear's reply: unexpected end of JSON input", 200, 0},
		{"oversized reply", `{"data":{}}`, "the reply is over 4 bytes", 200, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Client{Endpoint: serve(t, tc.status, tc.reply), Token: "token-1", MaxReply: tc.max}
			var out struct{}
			if err := c.Do(context.Background(), "query", nil, &out); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDoMarksARefusedToken(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		status      int
		want        bool
	}{
		{"http 401", `{"errors":[{"message":"unauthorized"}]}`, 401, true},
		{"authentication error code", `{"errors":[{"message":"Authentication required","extensions":{"code":"AUTHENTICATION_ERROR"}}]}`, 400, true},
		{"another refusal", `{"errors":[{"message":"no access"}]}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Client{Endpoint: serve(t, tc.status, tc.reply), Token: "token-1"}.Do(context.Background(), "query", nil, &struct{}{})
			if err == nil || errors.Is(err, ErrUnauthenticated) != tc.want {
				t.Fatalf("err = %v, want unauthenticated %v", err, tc.want)
			}
		})
	}
}

func TestDoWithoutDataLeavesOutAlone(t *testing.T) {
	out := struct{ Kept string }{Kept: "kept"}
	if err := (Client{Endpoint: serve(t, 200, `{}`), Token: "token-1"}).Do(context.Background(), "query", nil, &out); err != nil || out.Kept != "kept" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoDefaultsToLinearsEndpoint(t *testing.T) {
	var got string
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		got = r.URL.String()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{}}`))}, nil
	})}
	if err := (Client{Token: "token-1", HTTP: client}).Do(context.Background(), "query", nil, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if got != "https://api.linear.app/graphql" {
		t.Fatalf("posted to %q", got)
	}
}

func TestSnippetCutsOnACharacterBoundary(t *testing.T) {
	got := snippet([]byte(strings.Repeat("é", maxErrorBytes)))
	if !utf8.ValidString(got) || len(got) > maxErrorBytes+len("…") {
		t.Fatalf("snippet is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}
