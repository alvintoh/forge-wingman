package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// testSha is the head sha the shots in these tests were taken at.
const testSha = "0123456789abcdef0123456789abcdef01234567"

// evidenceRemote is a bare repository whose evidence branch holds files, keyed
// by their path on the branch.
func evidenceRemote(t *testing.T, files map[string]string) string {
	t.Helper()
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	remote := filepath.Join(t.TempDir(), "evidence.git")
	git(filepath.Dir(remote), "init", "-q", "--bare", remote)
	seed := t.TempDir()
	git(seed, "init", "-q", "-b", "evidence")
	for name, content := range files {
		p := filepath.Join(seed, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(seed, "add", "-A")
	git(seed, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "seed")
	git(seed, "remote", "add", "origin", remote)
	git(seed, "push", "-q", "origin", "evidence:evidence")
	return remote
}

// evidencePaths is every path the evidence branch holds in remote.
func evidencePaths(t *testing.T, remote string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", "evidence")
	cmd.Dir = remote
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-tree: %v", err)
	}
	return strings.Fields(string(out))
}

// shotsDir writes a shots artifact holding manifest and the named images.
func shotsDir(t *testing.T, manifest string, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "shots.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// prServer serves one pull request: its current body, which each edit replaces,
// and a reader for the bodies its edits carried.
func prServer(t *testing.T, number int, body string) (url string, patched func() []string) {
	t.Helper()
	current, edits := body, []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := fmt.Sprintf("/repos/o/r/pulls/%d", number)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == path:
			_ = json.NewEncoder(w).Encode(map[string]string{"body": current})
		case r.Method == http.MethodPatch && r.URL.Path == path:
			var b struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
				t.Errorf("decoding the body: %v", err)
			}
			current = b.Body
			edits = append(edits, b.Body)
			_, _ = io.WriteString(w, "{}")
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []string { return edits }
}

func prEnv(api string) func(string) string {
	env := map[string]string{"GITHUB_API_URL": api, "GH_TOKEN": "app-token", "GITHUB_REPOSITORY": "o/r"}
	return func(k string) string { return env[k] }
}

// TestPRPublishesTheShotsIntoTheBodyAndTheEvidenceBranch is AC1 and AC4: the pr
// step pushes the shots under pr-<n>/ on the evidence branch and edits the body
// to show them, leaving the rest of the body as it was.
func TestPRPublishesTheShotsIntoTheBodyAndTheEvidenceBranch(t *testing.T) {
	remote := evidenceRemote(t, map[string]string{"pr-12/old.png": "old", "pr-3/keep.png": "keep"})
	dir := shotsDir(t, `{"shots":[{"route":"/checkout","band":"s","file":"checkout-s.png"}]}`, "checkout-s.png")
	url, patched := prServer(t, 12, "## Summary\n\nAdds the widget.\n\n## Notes\n\nBuilt unattended.\n")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := run(context.Background(), logger,
		[]string{"pr", "-pr", "12", "-sha", testSha, "-shots", dir, "-remote", remote}, prEnv(url))
	if err != nil {
		t.Fatal(err)
	}
	if edits := patched(); len(edits) != 1 {
		t.Fatalf("edited the body %d times, want 1", len(edits))
	} else {
		for _, want := range []string{
			"## Screenshots (fixture data) · `" + testSha + "`",
			`<img width="200" alt="/checkout at s" src="https://raw.githubusercontent.com/o/r/evidence/pr-12/checkout-s.png">`,
			"## Summary\n\nAdds the widget.",
			"## Notes\n\nBuilt unattended.\n",
		} {
			if !strings.Contains(edits[0], want) {
				t.Errorf("body lacks %q:\n%s", want, edits[0])
			}
		}
	}
	if got := evidencePaths(t, remote); !slices.Equal(got, []string{"pr-12/checkout-s.png", "pr-3/keep.png"}) {
		t.Fatalf("evidence branch holds %v", got)
	}
}

// TestPRSaysSoInOneLineWhenTheRunCapturedNone is AC3: no artifact writes one
// line into the body and the step still succeeds, so the run does not stop.
func TestPRSaysSoInOneLineWhenTheRunCapturedNone(t *testing.T) {
	url, patched := prServer(t, 12, "## Summary\n\nAdds the widget.\n\n## Notes\n\nBuilt unattended.\n")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(context.Background(), logger, []string{"pr", "-pr", "12", "-sha", testSha}, prEnv(url)); err != nil {
		t.Fatal(err)
	}
	edits := patched()
	if len(edits) != 1 {
		t.Fatalf("edited the body %d times, want 1", len(edits))
	}
	want := "## Screenshots (fixture data)\n\nNo screenshots: this run captured none.\n"
	if !strings.Contains(edits[0], want) {
		t.Fatalf("body lacks %q:\n%s", want, edits[0])
	}
}

// TestPRReplacesTheSectionOnASecondPush is AC2 end to end: a push at a new sha
// re-renders the body, and the section the first push wrote is replaced rather
// than a second one added.
func TestPRReplacesTheSectionOnASecondPush(t *testing.T) {
	remote := evidenceRemote(t, nil)
	first := shotsDir(t, `{"shots":[{"route":"/checkout","band":"s","file":"checkout-s.png"}]}`, "checkout-s.png")
	second := shotsDir(t, `{"shots":[{"route":"/checkout","band":"m","file":"checkout-m.png"}]}`, "checkout-m.png")
	url, patched := prServer(t, 12, "## Summary\n\nAdds the widget.\n\n## Notes\n\nBuilt unattended.\n")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	newSha := strings.Repeat("b", 40)
	for _, push := range []struct{ sha, shots string }{{testSha, first}, {newSha, second}} {
		args := []string{"pr", "-pr", "12", "-sha", push.sha, "-shots", push.shots, "-remote", remote}
		if err := run(context.Background(), logger, args, prEnv(url)); err != nil {
			t.Fatal(err)
		}
	}
	edits := patched()
	if len(edits) != 2 {
		t.Fatalf("edited the body %d times, want 2", len(edits))
	}
	body := edits[1]
	if n := strings.Count(body, "## Screenshots"); n != 1 {
		t.Fatalf("body holds %d screenshots sections, want 1:\n%s", n, body)
	}
	if !strings.Contains(body, "· `"+newSha+"`") || strings.Contains(body, testSha) {
		t.Fatalf("body does not name the new sha only:\n%s", body)
	}
	if !strings.Contains(body, "pr-12/checkout-m.png") || strings.Contains(body, "checkout-s.png") {
		t.Fatalf("body still shows the previous push's shots:\n%s", body)
	}
}

// TestPRStopsOnWhatItCannotPublish is the regression for every guard the command
// checks before it publishes: each case fails on its own check, so deleting that
// check turns exactly one case red instead of leaving them all green.
func TestPRStopsOnWhatItCannotPublish(t *testing.T) {
	escapes := shotsDir(t, `{"shots":[{"route":"/x","band":"s","file":"../evil.png"}]}`, "evil.png")
	// A case that gets past its own guard answers here rather than at GitHub's
	// API, so a missing guard fails on its assertion and not on the network.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"body":"## Notes\n\nBuilt.\n"}`)
	}))
	t.Cleanup(api.Close)
	environment := func(drop ...string) map[string]string {
		out := map[string]string{"GITHUB_REPOSITORY": "o/r", "GH_TOKEN": "app-token", "GITHUB_API_URL": api.URL}
		for _, name := range drop {
			delete(out, name)
		}
		return out
	}
	cases := []struct {
		name   string
		env    map[string]string
		args   []string
		want   string
		reason error
	}{
		{
			"an unknown flag", environment(), []string{"pr", "-nope"},
			"flag provided but not defined: -nope", nil,
		},
		{
			"no pull request", environment(), []string{"pr", "-sha", testSha},
			"pr needs -pr and a -sha", nil,
		},
		{
			"no head sha", environment(), []string{"pr", "-pr", "12"},
			"pr needs -pr and a -sha", nil,
		},
		{
			"no repository", environment("GITHUB_REPOSITORY"),
			[]string{"pr", "-pr", "12", "-sha", testSha},
			"missing environment: GITHUB_REPOSITORY", nil,
		},
		{
			"no token", environment("GH_TOKEN"),
			[]string{"pr", "-pr", "12", "-sha", testSha},
			"missing environment: GH_TOKEN", nil,
		},
		{
			"a shot outside the folder", environment(),
			[]string{"pr", "-pr", "12", "-sha", testSha, "-shots", escapes},
			"publishing the screenshots for #12", runner.ErrEvidencePath,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			err := run(context.Background(), logger, tt.args, func(k string) string { return tt.env[k] })
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run = %v, want an error naming %q", err, tt.want)
			}
			if tt.reason != nil && !errors.Is(err, tt.reason) {
				t.Fatalf("run = %v, want it to wrap %v", err, tt.reason)
			}
		})
	}
}
