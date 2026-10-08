package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/linear"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	headSHA  = "0123456789abcdef0123456789abcdef01234567"
	movedSHA = "89abcdef0123456789abcdef0123456789abcdef"
	baseSHA  = "fedcba9876543210fedcba9876543210fedcba98"
)

// fakePulls serves one repository's pull request 7 and records every write.
type fakePulls struct {
	pull     map[string]any
	diff     string
	comments []map[string]any

	mu       sync.Mutex
	created  []string
	edited   map[string]string
	statuses map[string]commitStatus
	diffPath string
}

func (f *fakePulls) start(t *testing.T) func(string) string {
	t.Helper()
	f.edited, f.statuses = map[string]string{}, map[string]commitStatus{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/7":
			_ = json.NewEncoder(w).Encode(f.pull)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/o/r/compare/"):
			f.diffPath = r.URL.Path
			_, _ = io.WriteString(w, f.diff)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/7/comments":
			_ = json.NewEncoder(w).Encode(f.comments)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/7/comments":
			f.created = append(f.created, body["body"])
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/o/r/issues/comments/"):
			f.edited[strings.TrimPrefix(r.URL.Path, "/repos/o/r/issues/comments/")] = body["body"]
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/o/r/statuses/"):
			var s commitStatus
			_ = json.Unmarshal(raw, &s)
			f.statuses[strings.TrimPrefix(r.URL.Path, "/repos/o/r/statuses/")] = s
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"GITHUB_API_URL": srv.URL, "GH_TOKEN": "gh-token", "GITHUB_REPOSITORY": "o/r",
		"GITHUB_OUTPUT": filepath.Join(t.TempDir(), "output")}
	return func(k string) string { return env[k] }
}

func (f *fakePulls) client(getenv func(string) string) pullRequests {
	return pullRequests{API: getenv("GITHUB_API_URL"), Token: "gh-token", Repo: "o/r"}
}

func openPull(title, body, sha string, labels ...string) map[string]any {
	ls := []map[string]string{}
	for _, l := range labels {
		ls = append(ls, map[string]string{"name": l})
	}
	return map[string]any{"title": title, "body": body, "state": "open", "draft": false, "labels": ls,
		"head": map[string]string{"sha": sha}, "base": map[string]string{"sha": baseSHA}}
}

func readOutput(t *testing.T, getenv func(string) string) string {
	t.Helper()
	b, err := os.ReadFile(getenv("GITHUB_OUTPUT"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func TestWritePRReviewContext(t *testing.T) {
	found := func(context.Context, string) (linear.Issue, error) {
		return linear.Issue{Identifier: "ABC-12", Title: "Add x", Description: "AC1: x exists.", Labels: []string{"size:S"}}, nil
	}
	missing := func(context.Context, string) (linear.Issue, error) { return linear.Issue{}, linear.ErrIssueNotFound }
	for name, tt := range map[string]struct {
		title, diff string
		issue       func(context.Context, string) (linear.Issue, error)
		wantOutput  string
	}{
		"a ticket in the title":   {"feat(x): ABC-12 add x", "+x", found, "skip=false"},
		"no ticket id":            {"tidy the docs", "+x", found, "skip_reason=no-ticket-id"},
		"a ticket Linear lacks":   {"feat: ABC-12 add x", "+x", missing, "skip_reason=ticket-not-found"},
		"a diff over the ceiling": {"feat: ABC-12 add x", strings.Repeat("x", runner.MaxPRReviewDiffBytes+1), found, "skip_reason=diff-too-large"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: openPull(tt.title, "", headSHA), diff: tt.diff}
			getenv := f.start(t)
			out := filepath.Join(t.TempDir(), "context.json")
			if err := writePRReviewContext(context.Background(), quietLogger(), f.client(getenv), tt.issue, 7, out, getenv("GITHUB_OUTPUT")); err != nil {
				t.Fatal(err)
			}
			if got := readOutput(t, getenv); !strings.Contains(got, tt.wantOutput) {
				t.Fatalf("output = %q, want %q", got, tt.wantOutput)
			}
			if tt.wantOutput != "skip=false" {
				return
			}
			raw, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			c, err := runner.ParsePRReviewContext(raw)
			if err != nil || c.HeadSHA != headSHA || c.Ticket.Size != "S" || c.Diff != "+x" || f.diffPath != "/repos/o/r/compare/"+baseSHA+"..."+headSHA {
				t.Fatalf("context = %+v, diff path %q, err = %v", c, f.diffPath, err)
			}
		})
	}
}

func TestIssueWithMintsReadsAndRevokes(t *testing.T) {
	var mu sync.Mutex
	var revoked []string
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = r.ParseForm()
		if r.URL.Path == "/token" {
			if got := r.PostForm.Get("scope"); got != "read" {
				t.Errorf("scope = %q, want read", got)
			}
			_, _ = io.WriteString(w, `{"access_token":"read-token"}`)
			return
		}
		revoked = append(revoked, r.PostForm.Get("token"))
	}))
	t.Cleanup(oauth.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"issue":{"identifier":"ABC-12","title":"Add x","description":"d","labels":{"nodes":[]}}}}`)
	}))
	t.Cleanup(api.Close)
	creds := linear.Credentials{ClientID: "id", ClientSecret: "secret", Scope: linear.ReadScope, TokenURL: oauth.URL + "/token", RevokeURL: oauth.URL + "/revoke"}
	issue, err := issueWith(context.Background(), quietLogger(), linear.NewPollSource(creds), nil, api.URL, "ABC-12")
	if err != nil || issue.Title != "Add x" {
		t.Fatalf("issue = %+v, err = %v", issue, err)
	}
	if len(revoked) != 1 || revoked[0] != "read-token" {
		t.Fatalf("revoked %q, want the read token", revoked)
	}
}

func TestReadIssueAsReadsTheReviewAppsSecrets(t *testing.T) {
	var asked []string
	read := func(_ context.Context, name string) (string, error) {
		asked = append(asked, name)
		return "", errors.New("denied")
	}
	if _, err := readIssueAs(context.Background(), quietLogger(), read, nil, "ABC-12"); err == nil {
		t.Fatal("read an issue with no credentials")
	}
	if len(asked) != 1 || asked[0] != "linear-review-client-id" {
		t.Fatalf("asked for %q, want the review app's client id", asked)
	}
}

func verdict(outcome, findings, model string) runner.PRVerdict {
	return runner.PRVerdict{Outcome: outcome, Findings: findings, Model: model, Usage: runner.Usage{Input: 1200, Output: 80},
		Attempts: []runner.PRReviewAttempt{{Model: freeModel, Outcome: runner.AttemptReviewed}}}
}

func TestPlanPost(t *testing.T) {
	const runURL = "https://github.com/o/r/actions/runs/9"
	for name, tt := range map[string]struct {
		skip, contextResult string
		v                   runner.PRVerdict
		verdictErr          error
		wantState           string
		wantComment         string
		wantClean           bool
	}{
		"a clean review":         {"", "success", verdict("clean", "", freeModel), nil, "success", "### Wingman review: clean", true},
		"findings":               {"", "success", verdict("findings", "x is missing", freeModel), nil, "failure", "### Wingman review: findings", false},
		"no verdict":             {"", "success", verdict("no-verdict", "", ""), nil, "failure", "### Wingman review: no verdict", false},
		"an unreadable verdict":  {"", "success", verdict("clean", "", freeModel), errors.New("bad"), "failure", "### Wingman review: no verdict", false},
		"no ticket id":           {"no-ticket-id", "success", runner.PRVerdict{}, nil, "error", "", false},
		"an unknown skip reason": {"<b>", "success", runner.PRVerdict{}, nil, "error", "", false},
		"a failed context":       {"", "failure", runner.PRVerdict{}, nil, "error", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got := planPost(tt.skip, tt.contextResult, tt.v, tt.verdictErr, headSHA, runURL)
			if got.status.State != tt.wantState || got.status.Context != "wingman-review" || got.status.TargetURL != runURL ||
				len(got.status.Description) > 140 || got.clean != tt.wantClean {
				t.Fatalf("plan = %+v", got)
			}
			if (tt.wantComment == "") != (got.comment == "") || !strings.Contains(got.comment, tt.wantComment) {
				t.Fatalf("comment = %q, want %q", got.comment, tt.wantComment)
			}
		})
	}
}

func TestRenderReviewCommentNeutralisesTheFindings(t *testing.T) {
	findings := "ping @octocat\n<img src=x onerror=alert(1)>\n```\n# not a heading"
	got := renderReviewComment(verdict("findings", findings, freeModel), headSHA, "")
	if !strings.HasPrefix(got, "<!-- wingman-review -->\n") {
		t.Fatalf("comment lacks the marker: %q", got)
	}
	if strings.Contains(got, "@octocat") || !strings.Contains(got, "@\u200boctocat") {
		t.Errorf("the mention is live: %q", got)
	}
	open := strings.Index(got, "````text\n")
	end := strings.LastIndex(got, "\n````\n")
	if open < 0 || end < open {
		t.Fatalf("the findings are not fenced longer than their own backticks: %q", got)
	}
	if html := strings.Index(got, "<img"); html < open || html > end {
		t.Errorf("the HTML is outside the fence: %q", got)
	}
	if !strings.Contains(got, "Model `"+freeModel+"` · 1200 in / 80 out tokens · $0 across 1 attempt(s)") {
		t.Errorf("the footer lacks the model and its cost: %q", got)
	}
}

func TestMergeReady(t *testing.T) {
	eligible := func(edit func(*pull)) pull {
		var p pull
		raw, _ := json.Marshal(openPull("t", "", headSHA, "size:S", autoMergeEligibleLabel))
		_ = json.Unmarshal(raw, &p)
		if edit != nil {
			edit(&p)
		}
		return p
	}
	for name, tt := range map[string]struct {
		p    pull
		want bool
	}{
		"eligible, open and unchanged": {eligible(nil), true},
		"without the label":            {eligible(func(p *pull) { p.Labels = p.Labels[:1] }), false},
		"after a push":                 {eligible(func(p *pull) { p.Head.SHA = movedSHA }), false},
		"closed":                       {eligible(func(p *pull) { p.State = "closed" }), false},
		"back in draft":                {eligible(func(p *pull) { p.Draft = true }), false},
	} {
		if got := mergeReady(tt.p, headSHA); got != tt.want {
			t.Errorf("%s: mergeReady = %v, want %v", name, got, tt.want)
		}
	}
}

func writeVerdict(t *testing.T, v runner.PRVerdict) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "verdict.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPRReviewPost(t *testing.T) {
	ours := map[string]any{"id": 41, "body": prReviewMarker + "\nold", "user": map[string]string{"login": "github-actions[bot]"}}
	forged := map[string]any{"id": 42, "body": prReviewMarker + "\nforged", "user": map[string]string{"login": "mallory"}}
	for name, tt := range map[string]struct {
		v           runner.PRVerdict
		pull        map[string]any
		comments    []map[string]any
		wantState   string
		wantEdited  bool
		wantMerge   string
		wantCreated int
	}{
		"clean, eligible and unchanged requests the merge": {verdict("clean", "", freeModel), openPull("t", "", headSHA, autoMergeEligibleLabel), nil, "success", false, "merge=true", 1},
		"clean but not eligible does not":                  {verdict("clean", "", freeModel), openPull("t", "", headSHA), nil, "success", false, "merge=false", 1},
		"clean after a push does not":                      {verdict("clean", "", freeModel), openPull("t", "", movedSHA, autoMergeEligibleLabel), nil, "success", false, "merge=false", 1},
		"findings do not":                                  {verdict("findings", "x", freeModel), openPull("t", "", headSHA, autoMergeEligibleLabel), nil, "failure", false, "merge=false", 1},
		"a second review edits its own comment":            {verdict("clean", "", freeModel), openPull("t", "", headSHA), []map[string]any{forged, ours}, "success", true, "merge=false", 0},
		"another author's marker is not edited":            {verdict("clean", "", freeModel), openPull("t", "", headSHA), []map[string]any{forged}, "success", false, "merge=false", 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: tt.pull, comments: tt.comments}
			getenv := f.start(t)
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, tt.v), "-context-result", "success",
				"-run-url", "https://github.com/o/r/actions/runs/9"}
			if err := prReviewPost(context.Background(), quietLogger(), getenv, args); err != nil {
				t.Fatal(err)
			}
			if s, ok := f.statuses[headSHA]; !ok || s.State != tt.wantState || len(f.statuses) != 1 {
				t.Fatalf("statuses = %+v, want %s on the reviewed sha", f.statuses, tt.wantState)
			}
			if _, edited := f.edited["41"]; edited != tt.wantEdited || len(f.created) != tt.wantCreated || f.edited["42"] != "" {
				t.Fatalf("created %d, edited %v", len(f.created), f.edited)
			}
			if got := readOutput(t, getenv); !strings.Contains(got, tt.wantMerge) {
				t.Fatalf("output = %q, want %q", got, tt.wantMerge)
			}
		})
	}
}

func TestPRReviewPostSetsAnErrorStatusForASkippedReview(t *testing.T) {
	f := &fakePulls{}
	getenv := f.start(t)
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-skip-reason", "no-ticket-id", "-context-result", "success"}
	if err := prReviewPost(context.Background(), quietLogger(), getenv, args); err != nil {
		t.Fatal(err)
	}
	if s := f.statuses[headSHA]; s.State != "error" || !strings.Contains(s.Description, "no ticket id") || len(f.created) != 0 {
		t.Fatalf("status = %+v, comments %d", s, len(f.created))
	}
	if got := readOutput(t, getenv); !strings.Contains(got, "merge=false") {
		t.Fatalf("output = %q", got)
	}
}

func TestPRReviewPostRefusesAMalformedSHA(t *testing.T) {
	f := &fakePulls{}
	getenv := f.start(t)
	if err := prReviewPost(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-reviewed-sha", "main"}); err == nil {
		t.Fatal("posted a status on a ref that is not a sha")
	}
}

func TestRunPRReviewWritesTheVerdict(t *testing.T) {
	agent, _ := newSmokeAgent(map[string]smokeRun{
		freeModel: func(context.Context, string, io.Writer, io.Writer) error { return errors.New("exit status 1") },
		otherFree: reviewReply(tokensUsed, "x is missing"),
	})
	c := runner.PRReviewContext{PR: 7, HeadSHA: headSHA, Ticket: runner.Ticket{ID: "ABC-12", Title: "Add x", Body: "AC1"}, Diff: "+x"}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	contextPath, out, summary := filepath.Join(dir, "context.json"), filepath.Join(dir, "verdict.json"), filepath.Join(dir, "summary")
	if err := os.WriteFile(contextPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runPRReview(context.Background(), quietLogger(), agent, []string{freeModel, otherFree}, time.Minute, contextPath, out, summary, nil); err != nil {
		t.Fatal(err)
	}
	v, err := readVerdict(out)
	if err != nil || v.Outcome != "findings" || v.Model != otherFree || len(v.Attempts) != 2 || v.Attempts[0].Outcome != "agent failed" {
		t.Fatalf("verdict = %+v, err = %v", v, err)
	}
	if got := readSummary(t, summary); !strings.Contains(got, "Wingman review: findings.") {
		t.Fatalf("summary = %q", got)
	}
}

func TestRunPRReviewRefusesAMalformedContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.json")
	if err := os.WriteFile(path, []byte(`{"pr":7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	agent, dirs := newSmokeAgent(nil)
	err := runPRReview(context.Background(), quietLogger(), agent, []string{freeModel}, time.Minute, path, filepath.Join(t.TempDir(), "v"), "", nil)
	if !errors.Is(err, runner.ErrPRReviewInvalid) || len(*dirs) != 0 {
		t.Fatalf("err = %v, ran %d models", err, len(*dirs))
	}
}

func TestPRReviewRefusesAnEmptyModelList(t *testing.T) {
	err := prReview(context.Background(), quietLogger(), func(string) string { return "" }, []string{"-context", "c", "-out", "v", "-models", " "})
	if err == nil || !strings.Contains(err.Error(), "-models") {
		t.Fatalf("err = %v, want the empty list refused", err)
	}
}

func TestPRReviewRefusesTheBuildModel(t *testing.T) {
	err := prReview(context.Background(), quietLogger(), func(string) string { return "" },
		[]string{"-context", "c", "-out", "v", "-models", runner.DefaultModel()})
	if err == nil || !strings.Contains(err.Error(), "build model") {
		t.Fatalf("err = %v, want the build model refused", err)
	}
}

func TestPRReviewWorkflow(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "pr-review.yml"))
	if err != nil {
		t.Fatal(err)
	}
	yml := string(b)
	jobs := map[string]string{}
	for _, name := range []string{"context", "review", "post"} {
		_, rest, ok := strings.Cut(yml, "\n  "+name+":\n")
		if !ok {
			t.Fatalf("pr-review.yml has no %s job", name)
		}
		if i := strings.Index(rest, "\n  # "); i >= 0 {
			rest = rest[:i]
		}
		jobs[name] = rest
	}
	for _, want := range []string{
		"on:\n  pull_request_target:\n    types: [opened, ready_for_review, reopened]\n",
		"\npermissions: {}\n",
		"github.event.pull_request.draft == false",
		"github.event.pull_request.head.repo.full_name == github.repository",
		"github.event.pull_request.base.ref == 'main'",
		"cancel-in-progress: true",
	} {
		if !strings.Contains(yml, want) {
			t.Errorf("pr-review.yml lacks %q", want)
		}
	}
	for job, want := range map[string]string{
		"context": "    permissions:\n      id-token: write\n      pull-requests: read\n    outputs:",
		"review":  "    permissions: {}\n",
		"post":    "    permissions:\n      pull-requests: write\n      statuses: write\n    steps:",
	} {
		if !strings.Contains(jobs[job], want) {
			t.Errorf("the %s job's permissions are not %q", job, want)
		}
	}
	if strings.Contains(jobs["review"], "GH_TOKEN") || strings.Contains(jobs["review"], "google-github-actions/auth") {
		t.Error("the review job holds a GitHub or GCP credential")
	}
	if !strings.Contains(jobs["review"], "MODELS: ${{ vars.WINGMAN_PR_REVIEW_MODELS || ") {
		t.Error("the review job does not read its models from WINGMAN_PR_REVIEW_MODELS")
	}
	if !strings.Contains(jobs["review"], "COMMANDCODE_API_KEY: ${{ secrets.COMMANDCODE_API_KEY }}") || strings.Count(yml, "secrets.COMMANDCODE_API_KEY") != 1 {
		t.Error("the model key is not the review job's alone")
	}
	if strings.Contains(yml, "github.event.pull_request.head.ref") || strings.Contains(yml, "\n          ref:") ||
		strings.Count(yml, "persist-credentials: false") != strings.Count(yml, "actions/checkout@") {
		t.Error("pr-review.yml checks out something other than the base branch, or keeps a checkout's token")
	}
	for _, want := range []string{
		"if: steps.post.outputs.merge == 'true'",
		"continue-on-error: true",
		`any(. == "go") and any(. == "web")`,
		`gh pr merge "$PR" --auto --squash --match-head-commit "$REVIEWED_SHA"`,
		"GH_TOKEN: ${{ steps.app-token.outputs.token }}",
	} {
		if !strings.Contains(jobs["post"], want) {
			t.Errorf("the post job lacks %q", want)
		}
	}
}

func TestFencedIsOneLongerThanTheLongestBacktickRun(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       "```",
		"`a` `b` `c`": "```",
		"x ```` y":    "`````",
	} {
		if got, _, _ := strings.Cut(fenced(in), "text"); got != want {
			t.Errorf("fenced(%q) opens with %q, want %q", in, got, want)
		}
	}
}
