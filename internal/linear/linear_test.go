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

// TestMissingEntities: only a refusal made wholly of not-found errors that each
// name a field is reported, with the data Linear returned beside them.
func TestMissingEntities(t *testing.T) {
	const notFound = `{"message":"Entity not found: Issue","path":["t1"],"extensions":{"code":"INPUT_ERROR"}}`
	for _, tc := range []struct {
		name, reply string
		status      int
		want        bool
	}{
		{"not found", `{"data":{"t0":{},"t1":null},"errors":[` + notFound + `]}`, 200, true},
		{"not found without a path", `{"data":{"t1":null},"errors":[{"message":"Entity not found: Issue"}]}`, 200, false},
		{"beside another error", `{"data":{"t1":null},"errors":[` + notFound + `,{"message":"rate limited","path":["t0"]}]}`, 200, false},
		{"unauthenticated", `{"errors":[` + strings.Replace(notFound, "INPUT_ERROR", authenticationError, 1) + `]}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Client{Endpoint: serve(t, tc.status, tc.reply), Token: "token-1"}.Do(context.Background(), "query", nil, &struct{}{})
			fields, data, ok := MissingEntities(err)
			if ok != tc.want {
				t.Fatalf("ok = %v for %v, want %v", ok, err, tc.want)
			}
			if tc.want && (len(fields) != 1 || fields[0] != "t1" || !strings.Contains(string(data), `"t0":{}`)) {
				t.Fatalf("fields = %v, data = %s, want t1 missing beside t0's data", fields, data)
			}
		})
	}
}

func TestSnippetCutsOnACharacterBoundary(t *testing.T) {
	got := snippet([]byte(strings.Repeat("é", maxErrorBytes)))
	if !utf8.ValidString(got) || len(got) > maxErrorBytes+len("…") {
		t.Fatalf("snippet is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}
