package dispatcher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubStartsTheRunWorkflowInTheTicketRepository(t *testing.T) {
	var got struct {
		path   string
		auth   string
		accept string
		ver    string
		body   map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth, got.accept = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept")
		got.ver = r.Header.Get("X-GitHub-Api-Version")
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		// The endpoint answered 204 when it was added and answers 200 with the
		// run's URLs now; both mean the run started.
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"workflow_run_id":42,"run_url":"https://github.com/o/r/actions/runs/42"}`)
	}))
	defer srv.Close()

	claim := Claim{RunID: "FRG-18", Repo: "octo/scratch", Priority: 1}
	if err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Workflow: "run.yml",
		Branch: "main", Client: srv.Client()}).Dispatch(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if got.path != "/repos/octo/scratch/actions/workflows/run.yml/dispatches" {
		t.Fatalf("path = %q", got.path)
	}
	if got.auth != "Bearer gh-token" || got.accept != "application/vnd.github+json" || got.ver == "" {
		t.Fatalf("headers = %+v", got)
	}
	// The model input is left to run.yml's own default rather than restated here.
	if got.body["ref"] != "main" {
		t.Fatalf("ref = %v", got.body["ref"])
	}
	inputs, _ := got.body["inputs"].(map[string]any)
	if len(inputs) != 1 || inputs["run_id"] != "FRG-18" {
		t.Fatalf("inputs = %v", got.body["inputs"])
	}
}

func TestGitHubReportsARefusalOnOneLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "{\n  \"message\": \"Bad credentials\"\n}")
	}))
	defer srv.Close()

	err := (GitHub{Endpoint: srv.URL, Token: "gh-token", Workflow: "run.yml", Branch: "main",
		Client: srv.Client()}).Dispatch(context.Background(), Claim{RunID: "FRG-18", Repo: "octo/scratch"})
	if err == nil {
		t.Fatal("dispatched with a token GitHub refused")
	}
	for _, want := range []string{"FRG-18", "octo/scratch", "401", "Bad credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("err = %q, want one line", err)
	}
}

func TestSnippetTruncatesAndFlattens(t *testing.T) {
	if got := snippet([]byte("  a\r\nb  ")); got != "a b" {
		t.Fatalf("snippet = %q", got)
	}
	long := strings.Repeat("x", maxErrorBytes+10)
	if got := snippet([]byte(long)); len([]rune(got)) != maxErrorBytes+1 {
		t.Fatalf("snippet is %d runes, want the cap plus its ellipsis", len([]rune(got)))
	}
}
