package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const fixPushBranchName = "wingman/abc-12-100-1"

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=" + runner.CommitAuthorName, "-c", "user.email=" + runner.CommitAuthorEmail}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pushSetup is a bare origin and a checkout at the expected head, with a bundle
// of one runner commit on it.
type pushSetup struct {
	origin, work, bundle, expected, tip string
}

func newPushSetup(t *testing.T, bBody string) pushSetup {
	t.Helper()
	root := t.TempDir()
	s := pushSetup{origin: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work")}
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", s.origin)
	gitIn(t, root, "init", "-q", "-b", fixPushBranchName, s.work)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(s.work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, s.work, "add", name)
		gitIn(t, s.work, "commit", "-qm", name)
	}
	write("a.go", "x\n")
	s.expected = gitIn(t, s.work, "rev-parse", "HEAD")
	gitIn(t, s.work, "remote", "add", "origin", s.origin)
	gitIn(t, s.work, "push", "-q", "origin", fixPushBranchName)
	write("b.go", bBody)
	s.tip = gitIn(t, s.work, "rev-parse", "HEAD")
	s.bundle = filepath.Join(root, "fix.bundle")
	gitIn(t, s.work, "bundle", "create", s.bundle, "^"+s.expected, fixPushBranchName)
	gitIn(t, s.work, "reset", "-q", "--hard", s.expected)
	return s
}

func (s pushSetup) args() []string {
	return []string{"fix-push", "-pr", "7", "-branch", fixPushBranchName, "-expected", s.expected,
		"-bundle", s.bundle, "-repo", s.work}
}

// pullAt serves pull request 7 at ref and sha, and returns the env pointing at it.
func pullAt(t *testing.T, ref, sha string) func(string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"head": map[string]string{"ref": ref, "sha": sha}})
	}))
	t.Cleanup(srv.Close)
	return envFor(t, srv.URL)
}

func runFixPush(t *testing.T, getenv func(string) string, args []string) error {
	t.Helper()
	return run(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), args, getenv)
}

func TestFixPushPushesWithNoCloudProjectAndReportsIt(t *testing.T) {
	s := newPushSetup(t, "x\n")
	getenv := pullAt(t, fixPushBranchName, s.expected)
	if err := runFixPush(t, getenv, s.args()); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, s.origin, "rev-parse", "refs/heads/"+fixPushBranchName); got != s.tip {
		t.Fatalf("origin at %s, want %s", got, s.tip)
	}
	out := readOutput(t, getenv)
	for _, want := range []string{"pushed=true\n", "sha=" + s.tip + "\n", "reason=\n", "detail<<"} {
		if !strings.Contains(out, want) {
			t.Fatalf("outputs %q lack %q", out, want)
		}
	}
}

func TestFixPushRefusalFailsTheStepAfterWritingItsOutputs(t *testing.T) {
	s := newPushSetup(t, "x\n")
	moved := strings.Repeat("b", 40)
	getenv := pullAt(t, fixPushBranchName, moved)
	err := runFixPush(t, getenv, s.args())
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("err = %v, want a failed step", err)
	}
	if got := gitIn(t, s.origin, "rev-parse", "refs/heads/"+fixPushBranchName); got != s.expected {
		t.Fatalf("origin moved to %s", got)
	}
	out := readOutput(t, getenv)
	for _, want := range []string{"pushed=false\n", "sha=\n", "reason=head-moved\n", moved} {
		if !strings.Contains(out, want) {
			t.Fatalf("outputs %q lack %q", out, want)
		}
	}
}

func TestFixPushNeedsAPullRequest(t *testing.T) {
	s := newPushSetup(t, "x\n")
	getenv := pullAt(t, fixPushBranchName, s.expected)
	err := runFixPush(t, getenv, append(s.args()[:1], s.args()[3:]...))
	if err == nil || !strings.Contains(err.Error(), "needs -pr") {
		t.Fatalf("err = %v, want it to name -pr", err)
	}
	if out := readOutput(t, getenv); !strings.Contains(out, "reason=error\n") {
		t.Fatalf("outputs %q lack the error reason", out)
	}
}

func TestFixPushRefusesTheSecretsTheJobHolds(t *testing.T) {
	plan := providers.Plans()[0]
	s := newPushSetup(t, "key = plan-key-value\n")
	getenv := pullAt(t, fixPushBranchName, s.expected)
	holding := func(k string) string {
		if k == providers.KeySecret(plan) {
			return "plan-key-value"
		}
		return getenv(k)
	}
	if err := runFixPush(t, holding, s.args()); err == nil {
		t.Fatal("a commit holding the job's secret was pushed")
	}
	if out := readOutput(t, getenv); !strings.Contains(out, "reason=secret\n") {
		t.Fatalf("outputs %q lack the secret reason", out)
	}
}

func TestFixPushRefusesTheJobsOwnGitHubToken(t *testing.T) {
	s := newPushSetup(t, "token = gh-token\n")
	getenv := pullAt(t, fixPushBranchName, s.expected)
	if err := runFixPush(t, getenv, s.args()); err == nil {
		t.Fatal("a commit holding the job's GitHub token was pushed")
	}
	if out := readOutput(t, getenv); !strings.Contains(out, "reason=secret\n") {
		t.Fatalf("outputs %q lack the secret reason", out)
	}
}

func TestFixPushLogsWhenNoSecretValueIsSet(t *testing.T) {
	s := newPushSetup(t, "x\n")
	getenv := pullAt(t, fixPushBranchName, s.expected)
	for _, tt := range []struct {
		name, token string
		wantLog     bool
	}{
		{"no token", "", true},
		{"a token", "gh-token", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			env := func(k string) string {
				if k == "GH_TOKEN" {
					return tt.token
				}
				return getenv(k)
			}
			_ = run(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), s.args(), env)
			if got := strings.Contains(logs.String(), "msg=fixPushSecretsUnset"); got != tt.wantLog {
				t.Fatalf("logged fixPushSecretsUnset = %v, want %v: %s", got, tt.wantLog, logs.String())
			}
		})
	}
}
