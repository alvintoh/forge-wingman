package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/linear"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

const (
	headSHA  = "0123456789abcdef0123456789abcdef01234567"
	movedSHA = "89abcdef0123456789abcdef0123456789abcdef"
	baseSHA  = "fedcba9876543210fedcba9876543210fedcba98"
)

// fakePulls serves one repository's pull request 7 and records every write.
type fakePulls struct {
	pull         map[string]any
	diff         string
	comments     []map[string]any
	statusList   []commitStatusEntry
	checkRuns    []map[string]any
	checksStatus int
	pullStatus   int
	pullDelay    time.Duration

	mu       sync.Mutex
	created  []string
	edited   map[string]string
	statuses map[string]commitStatus
	diffPath string
	// auth is the Authorization header of every request, and statusQueries the
	// query string of every statuses read.
	auth          []string
	statusQueries []string
}

func (f *fakePulls) start(t *testing.T) func(string) string {
	t.Helper()
	f.edited, f.statuses = map[string]string{}, map[string]commitStatus{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/7":
			if f.pullDelay > 0 {
				time.Sleep(f.pullDelay)
			}
			if f.pullStatus != 0 {
				w.WriteHeader(f.pullStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(f.pull)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/o/r/compare/"):
			f.diffPath = r.URL.Path
			_, _ = io.WriteString(w, f.diff)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/7/comments":
			_ = json.NewEncoder(w).Encode(f.comments)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/check-runs"):
			if f.checksStatus != 0 {
				w.WriteHeader(f.checksStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(f.checkRuns), "check_runs": f.checkRuns})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "success", "total_count": 0, "statuses": []any{}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/o/r/commits/"):
			f.statusQueries = append(f.statusQueries, r.URL.RawQuery)
			_ = json.NewEncoder(w).Encode(f.statusList)
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
		"head": map[string]string{"sha": sha, "ref": "wingman/abc-12-1-1"}, "base": map[string]string{"sha": baseSHA}}
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

func TestWritePRReviewContextFailsWhenTheReviewAppSeesNoTeam(t *testing.T) {
	hidden := func(context.Context, string) (linear.Issue, error) { return linear.Issue{}, linear.ErrNoTeam }
	f := &fakePulls{pull: openPull("feat: ABC-12 add the secret widget", "", headSHA), diff: "+x"}
	getenv := f.start(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	err := writePRReviewContext(context.Background(), logger, f.client(getenv), hidden, 7, filepath.Join(t.TempDir(), "context.json"), getenv("GITHUB_OUTPUT"))
	if !errors.Is(err, errReviewAppNoTeam) {
		t.Fatalf("err = %v, want errReviewAppNoTeam", err)
	}
	for _, want := range []string{"the review app cannot see the team; is its Linear user suspended?", "infra/README.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to say %q", err, want)
		}
	}
	if got := readOutput(t, getenv); strings.Contains(got, "skip") {
		t.Errorf("output = %q, want no skip", got)
	}
	for _, secret := range []string{"gh-token", "secret widget"} {
		if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
			t.Errorf("the error or logs name %q: err %q, logs %q", secret, err, logs.String())
		}
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
		Attempts: []runner.PRReviewAttempt{{Model: museModel, Outcome: runner.AttemptReviewed}}}
}

// noVerdict is a review no model reached a verdict on, with each attempt's own outcome.
func noVerdict(attempts ...runner.PRReviewAttempt) runner.PRVerdict {
	return runner.PRVerdict{Outcome: runner.PRReviewNoVerdict, Attempts: attempts}
}

// attempted is one model's try at a review, which ended outcome.
func attempted(model, outcome string) runner.PRReviewAttempt {
	return runner.PRReviewAttempt{Model: model, Outcome: outcome}
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
		"a clean review":         {"", "success", verdict("clean", "", museModel), nil, "success", "### Wingman review: clean", true},
		"findings":               {"", "success", verdict("findings", "x is missing", museModel), nil, "failure", "### Wingman review: findings", false},
		"no verdict: failed":     {"", "success", noVerdict(attempted(museModel, runner.AttemptFailed)), nil, "failure", "### Wingman review: no verdict: failed", false},
		"an unreadable verdict":  {"", "success", verdict("clean", "", museModel), errors.New("bad"), "failure", "### Wingman review: no verdict: failed", false},
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

// TestPlanPostNamesTheNoVerdictCause: an allowance the reviewer reported spent
// reads differently from a failure, on the comment and the status alike.
func TestPlanPostNamesTheNoVerdictCause(t *testing.T) {
	spent := noVerdict(attempted(museModel, runner.AttemptExhausted), attempted(pricedModel, runner.AttemptExhausted))
	got := planPost("", "success", spent, nil, headSHA, "")
	if want := "### Wingman review: no verdict: allowance exhausted"; !strings.Contains(got.comment, want) ||
		!strings.HasPrefix(got.status.Description, "No verdict: allowance exhausted") {
		t.Fatalf("an exhausted allowance posted %+v, want %q on the comment and its status", got, want)
	}
	mixed := noVerdict(attempted(museModel, runner.AttemptExhausted), attempted(pricedModel, runner.AttemptTimedOut))
	got = planPost("", "success", mixed, nil, headSHA, "")
	if want := "### Wingman review: no verdict: failed"; !strings.Contains(got.comment, want) ||
		!strings.HasPrefix(got.status.Description, "No verdict: failed") {
		t.Fatalf("a partly exhausted review posted %+v, want %q on the comment and its status", got, want)
	}
}

func TestRenderReviewCommentNeutralisesTheFindings(t *testing.T) {
	findings := "ping @octocat\n<img src=x onerror=alert(1)>\n```\n# not a heading"
	got := renderReviewComment(verdict("findings", findings, museModel), headSHA, "", mergeNote{})
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
	if !strings.Contains(got, "Model `"+museModel+"` · 1200 in / 80 out tokens · $0 across 1 attempt(s)") {
		t.Errorf("the footer lacks the model and its cost: %q", got)
	}
}

func TestRenderReviewCommentNamesTheNoVerdictCause(t *testing.T) {
	spent := noVerdict(attempted(museModel, runner.AttemptExhausted), attempted(pricedModel, runner.AttemptExhausted))
	got := renderReviewComment(spent, headSHA, "", mergeNote{})
	if !strings.Contains(got, "### Wingman review: no verdict: allowance exhausted\n") ||
		!strings.Contains(got, "- `"+museModel+"`: allowance exhausted\n") ||
		!strings.Contains(got, "- `"+pricedModel+"`: allowance exhausted\n") {
		t.Fatalf("comment = %q, want the allowance and each model that reported it", got)
	}
	failed := noVerdict(attempted(museModel, runner.AttemptFailed))
	got = renderReviewComment(failed, headSHA, "", mergeNote{})
	if !strings.Contains(got, "### Wingman review: no verdict: failed\n") || strings.Contains(got, "allowance exhausted") {
		t.Fatalf("comment = %q, want a failure that names no allowance", got)
	}
}

func TestShortSHAKeepsAShortHeadWhole(t *testing.T) {
	if got := shortSHA(headSHA); got != headSHA[:12] {
		t.Fatalf("shortSHA = %q, want the first 12 characters", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Fatalf("shortSHA of a short head = %q, want it whole", got)
	}
}

func TestParseReviewCommentRoundTripsEveryComment(t *testing.T) {
	for _, v := range []runner.PRVerdict{
		verdict("clean", "", museModel),
		noVerdict(attempted(museModel, runner.AttemptFailed)),
		verdict("findings", "the retry never bounds `attempts`\nping @octocat\n```go\nif x {\n```\n# not a heading", museModel),
	} {
		t.Run(v.Outcome, func(t *testing.T) {
			got, ok := parseReviewComment(renderReviewComment(v, headSHA, "", mergeNote{}))
			if !ok {
				t.Fatal("the comment renderReviewComment wrote does not parse")
			}
			if v.Outcome != runner.PRReviewFindings {
				if got.SHA != "" || got.Findings != "" {
					t.Fatalf("parse = %+v, want no head and no findings", got)
				}
				return
			}
			if got.SHA != headSHA[:12] || got.Findings != v.Findings {
				t.Fatalf("parse = %+v, want %s and %q", got, headSHA[:12], v.Findings)
			}
		})
	}
}

func TestParseReviewCommentNamesTheHeadOfAnUnreadableBlock(t *testing.T) {
	body := prReviewMarker + "\n### Wingman review: findings\n\nAt `" + headSHA[:12] + "`:\n\n```text\nthe fence never closes\n"
	got, ok := parseReviewComment(body)
	if ok || got.SHA != headSHA[:12] {
		t.Fatalf("parse = %+v, ok = %v, want the head with an unreadable block", got, ok)
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
		"not a run's branch":           {eligible(func(p *pull) { p.Head.Ref = "feature/x" }), false},
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
		"clean, eligible and unchanged requests the merge": {verdict("clean", "", museModel), openPull("t", "", headSHA, autoMergeEligibleLabel), nil, "success", false, "merge=true", 1},
		"clean but not eligible does not":                  {verdict("clean", "", museModel), openPull("t", "", headSHA), nil, "success", false, "merge=false", 1},
		"clean after a push does not":                      {verdict("clean", "", museModel), openPull("t", "", movedSHA, autoMergeEligibleLabel), nil, "success", false, "merge=false", 1},
		"findings do not":                                  {verdict("findings", "x", museModel), openPull("t", "", headSHA, autoMergeEligibleLabel), nil, "failure", false, "merge=false", 1},
		"a second review edits its own comment":            {verdict("clean", "", museModel), openPull("t", "", headSHA), []map[string]any{forged, ours}, "success", true, "merge=false", 0},
		"another author's marker is not edited":            {verdict("clean", "", museModel), openPull("t", "", headSHA), []map[string]any{forged}, "success", false, "merge=false", 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: tt.pull, comments: tt.comments}
			getenv := f.start(t)
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, tt.v), "-context-result", "success",
				"-run-url", "https://github.com/o/r/actions/runs/9"}
			if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, recordNothing, args); err != nil {
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

// switchedOn reads the owner's settings as on with nothing escaped.
func switchedOn(context.Context) (store.AutoMergeSetting, error) {
	return store.AutoMergeSetting{Enabled: true}, nil
}

// rateUnder reads the trailing rewrite rate as at or under the guideline.
func rateUnder(context.Context) dispatcher.AutoMergeDecision {
	return dispatcher.AutoMergeDecision{WithinGuideline: true, Reason: dispatcher.AutoMergeReasonOn, PRs: dispatcher.TrailingPRs}
}

// rateAbove reads the trailing rewrite rate as over the guideline.
func rateAbove(context.Context) dispatcher.AutoMergeDecision {
	return dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonAboveGuideline, PRs: dispatcher.TrailingPRs, Rate: 0.34}
}

// rateUnread reads the trailing rewrite rate as evidence that could not be read.
func rateUnread(context.Context) dispatcher.AutoMergeDecision {
	return dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonEvidenceUnread}
}

// rateTooFew reads the trailing rewrite rate over a window too short to measure.
func rateTooFew(context.Context) dispatcher.AutoMergeDecision {
	return dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonTooFewMeasured, PRs: 3, Rate: 0.1}
}

// rateNoAgentLines reads the trailing rewrite rate over PRs carrying no agent lines.
func rateNoAgentLines(context.Context) dispatcher.AutoMergeDecision {
	return dispatcher.AutoMergeDecision{Reason: dispatcher.AutoMergeReasonNoAgentLines, PRs: dispatcher.TrailingPRs}
}

// spentAllowance is a review whose every model reported its plan's allowance
// spent, so it ends on no verdict of that cause.
func spentAllowance() runner.PRVerdict {
	return noVerdict(attempted(museModel, runner.AttemptExhausted), attempted(pricedModel, runner.AttemptExhausted))
}

// greenChecks are the go and web check runs of a head that passed both.
func greenChecks() []map[string]any {
	return []map[string]any{
		{"id": 1, "name": "go", "status": "completed", "conclusion": "success"},
		{"id": 2, "name": "web", "status": "completed", "conclusion": "success"},
	}
}

// An exhausted allowance on an eligible PR whose go and web checks pass at the
// reviewed head requests the merge, and says in the comment that it merged
// without a review.
func TestPRReviewPostRequestsTheMergeOnAnExhaustedAllowance(t *testing.T) {
	f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks()}
	getenv := f.start(t)
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, spentAllowance()), "-context-result", "success"}
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, recordNothing, args); err != nil {
		t.Fatal(err)
	}
	if got := readOutput(t, getenv); !strings.Contains(got, "merge=true") {
		t.Fatalf("output = %q, want the merge requested", got)
	}
	if len(f.created) != 1 || !strings.Contains(f.created[0], "merged without review") {
		t.Fatalf("comment = %v, want it to say the PR merged without a review", f.created)
	}
	if !strings.Contains(f.created[0], "### Wingman review: no verdict: allowance exhausted") {
		t.Fatalf("comment = %q, want the exhausted cause named", f.created[0])
	}
	if s := f.statuses[headSHA]; s.State != "failure" || s.Description != "No verdict: allowance exhausted; merges without review" {
		t.Fatalf("status = %+v, want the allowance named as merging without a review", s)
	}
}

// The request is made only where every gate allows it: an ineligible PR, a head
// that moved, and checks that are failing, pending or absent each request
// nothing, and the comment names the gate that held it.
func TestPRReviewPostDoesNotRequestTheMergeOnAnExhaustedAllowance(t *testing.T) {
	failing := []map[string]any{
		{"id": 1, "name": "go", "status": "completed", "conclusion": "success"},
		{"id": 2, "name": "web", "status": "completed", "conclusion": "failure"},
	}
	pending := []map[string]any{
		{"id": 1, "name": "go", "status": "completed", "conclusion": "success"},
		{"id": 2, "name": "web", "status": "in_progress", "conclusion": ""},
	}
	goFailing := []map[string]any{
		{"id": 1, "name": "go", "status": "completed", "conclusion": "failure"},
		{"id": 2, "name": "web", "status": "completed", "conclusion": "success"},
	}
	goPending := []map[string]any{
		{"id": 1, "name": "go", "status": "in_progress", "conclusion": ""},
		{"id": 2, "name": "web", "status": "completed", "conclusion": "success"},
	}
	for name, tt := range map[string]struct {
		pull      map[string]any
		checkRuns []map[string]any
		wantWords string
	}{
		"without the label":     {openPull("t", "", headSHA), greenChecks(), "not eligible for auto-merge"},
		"after a push":          {openPull("t", "", movedSHA, autoMergeEligibleLabel), greenChecks(), "not eligible for auto-merge"},
		"a failing check":       {openPull("t", "", headSHA, autoMergeEligibleLabel), failing, "not both green"},
		"a check still running": {openPull("t", "", headSHA, autoMergeEligibleLabel), pending, "not both green"},
		"go failing alone":      {openPull("t", "", headSHA, autoMergeEligibleLabel), goFailing, "not both green"},
		"go still running":      {openPull("t", "", headSHA, autoMergeEligibleLabel), goPending, "not both green"},
		"no check reported":     {openPull("t", "", headSHA, autoMergeEligibleLabel), nil, "not both green"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: tt.pull, checkRuns: tt.checkRuns}
			getenv := f.start(t)
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, spentAllowance()), "-context-result", "success"}
			if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, recordNothing, args); err != nil {
				t.Fatal(err)
			}
			if got := readOutput(t, getenv); !strings.Contains(got, "merge=false") {
				t.Fatalf("output = %q, want no merge requested", got)
			}
			if len(f.created) != 1 || !strings.Contains(f.created[0], tt.wantWords) {
				t.Fatalf("comment = %v, want %q", f.created, tt.wantWords)
			}
			if s := f.statuses[headSHA]; s.Description != "No verdict: allowance exhausted; waits for the owner" {
				t.Fatalf("status = %+v, want the allowance named as waiting for the owner", s)
			}
		})
	}
}

// A failed or timed out review waits for the owner whatever its checks say: an
// exhausted allowance is the only no verdict that merges.
func TestPRReviewPostRequestsNothingOnANoVerdictThatFailed(t *testing.T) {
	f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks()}
	getenv := f.start(t)
	failed := noVerdict(attempted(museModel, runner.AttemptExhausted), attempted(pricedModel, runner.AttemptTimedOut))
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, failed), "-context-result", "success"}
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, recordNothing, args); err != nil {
		t.Fatal(err)
	}
	if got := readOutput(t, getenv); !strings.Contains(got, "merge=false") {
		t.Fatalf("output = %q, want the merge left to the owner", got)
	}
	if len(f.created) != 1 || strings.Contains(f.created[0], "merged without review") {
		t.Fatalf("comment = %v, want no merge claimed", f.created)
	}
}

// A check read that fails is not green: no merge is requested and the comment
// says the results could not be read.
func TestPRReviewPostReadsAFailedCheckReadAsNotGreen(t *testing.T) {
	for name, status := range map[string]int{
		"a refused read": http.StatusForbidden,
		"a broken read":  http.StatusInternalServerError,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checksStatus: status}
			getenv := f.start(t)
			var logs bytes.Buffer
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, spentAllowance()), "-context-result", "success"}
			if err := prReviewPostWith(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), getenv, switchedOn, rateUnder, recordNothing, args); err != nil {
				t.Fatal(err)
			}
			if got := readOutput(t, getenv); !strings.Contains(got, "merge=false") {
				t.Fatalf("output = %q, want no merge requested", got)
			}
			if len(f.created) != 1 || !strings.Contains(f.created[0], "check results could not be read") {
				t.Fatalf("comment = %v, want the unread check results named", f.created)
			}
			if got := logs.String(); !strings.Contains(got, "reason=checks-unread") {
				t.Fatalf("log = %q, want the checks-unread reason", got)
			}
		})
	}
}

// An exhausted allowance requests nothing while the switch is off, an escaped PR
// holds auto-merge, the settings cannot be read, or the rewrite rate is over the
// guideline or cannot be measured at all, and the comment names the gate.
func TestPRReviewPostDeclinesTheMergeOnAnExhaustedAllowance(t *testing.T) {
	for name, tt := range map[string]struct {
		settings  autoMergeSettings
		rate      rewriteRate
		reason    string
		wantWords string
	}{
		"the switch is off": {func(context.Context) (store.AutoMergeSetting, error) {
			return store.AutoMergeSetting{Enabled: false}, nil
		}, rateUnder, dispatcher.AutoMergeReasonSwitchOff, "auto-merge switch is off"},
		"an escaped PR holds it": {func(context.Context) (store.AutoMergeSetting, error) {
			return store.AutoMergeSetting{Enabled: true,
				Escaped: dispatcher.MergeEscape{Repo: "o/r", PR: 5, By: 9, Reason: dispatcher.EscapeReverted}}, nil
		}, rateUnder, dispatcher.AutoMergeReasonEscaped, "auto-merge is off until you switch it on again"},
		"the settings cannot be read": {func(context.Context) (store.AutoMergeSetting, error) {
			return store.AutoMergeSetting{}, errors.New("permission denied")
		}, rateUnder, "settings-unread", "settings could not be read"},
		"the rate is over the guideline": {switchedOn, rateAbove, dispatcher.AutoMergeReasonAboveGuideline, "34.0% over the last 10 PRs a human merged, above the 20% guideline"},
		"the rate could not be read":     {switchedOn, rateUnread, dispatcher.AutoMergeReasonEvidenceUnread, "the measured merges could not be read"},
		"too few PRs are measured":       {switchedOn, rateTooFew, dispatcher.AutoMergeReasonTooFewMeasured, "3 of 10 PRs a human merged are measured"},
		"no agent lines in the window":   {switchedOn, rateNoAgentLines, dispatcher.AutoMergeReasonNoAgentLines, "the measured PRs carry no agent lines"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks()}
			getenv := f.start(t)
			var logs bytes.Buffer
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, spentAllowance()), "-context-result", "success"}
			if err := prReviewPostWith(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), getenv, tt.settings, tt.rate, recordNothing, args); err != nil {
				t.Fatal(err)
			}
			if got := readOutput(t, getenv); !strings.Contains(got, "merge=false") {
				t.Fatalf("output = %q, want no merge requested", got)
			}
			if len(f.created) != 1 || !strings.Contains(f.created[0], tt.wantWords) {
				t.Fatalf("comment = %v, want %q", f.created, tt.wantWords)
			}
			if got := logs.String(); !strings.Contains(got, "prReviewMergeDeclined") || !strings.Contains(got, "reason="+tt.reason) {
				t.Fatalf("log = %q, want the decline and reason %s", got, tt.reason)
			}
		})
	}
}

// A pull request that cannot be read requests no merge, and the review still
// posts: the comment is the deliverable, and the merge decision is secondary.
func TestPRReviewPostPostsTheReviewWhenThePullCannotBeRead(t *testing.T) {
	for name, tt := range map[string]struct {
		v         runner.PRVerdict
		wantWords string
	}{
		"a clean review": {verdict("clean", "", museModel), ""},
		"an exhausted allowance": {spentAllowance(),
			"Auto-merge is not requested, so this pull request waits for the owner: the pull request could not be read."},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks(),
				pullStatus: http.StatusInternalServerError}
			getenv := f.start(t)
			var logs bytes.Buffer
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, tt.v), "-context-result", "success"}
			if err := prReviewPostWith(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), getenv, switchedOn, rateUnder, recordNothing, args); err != nil {
				t.Fatal(err)
			}
			if got := readOutput(t, getenv); !strings.Contains(got, "merge=false") {
				t.Fatalf("output = %q, want no merge requested", got)
			}
			if len(f.created) != 1 || !strings.Contains(f.created[0], prReviewMarker) {
				t.Fatalf("comments = %v, want the review posted", f.created)
			}
			if len(f.statuses) != 1 {
				t.Fatalf("statuses = %+v, want the review's status posted", f.statuses)
			}
			if tt.wantWords != "" && !strings.Contains(f.created[0], tt.wantWords) {
				t.Fatalf("comment = %q, want %q", f.created[0], tt.wantWords)
			}
			if got := logs.String(); !strings.Contains(got, "reason=pull-unread") {
				t.Fatalf("log = %q, want the pull-unread decline", got)
			}
		})
	}
}

// The decision holds one window: a read that cannot finish inside it is a
// refusal, never a merge, and the reads after it are not attempted.
func TestExhaustedMergeDeclinesWhenTheDecisionWindowExpires(t *testing.T) {
	f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks(),
		pullDelay: 100 * time.Millisecond}
	getenv := f.start(t)
	reads := 0
	read := func(ctx context.Context) (store.AutoMergeSetting, error) {
		reads++
		return switchedOn(ctx)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	note := exhaustedMerge(ctx, f.client(getenv), 7, headSHA, read, rateUnder)
	if note.Requested || note.Reason != "pull-unread" {
		t.Fatalf("note = %+v, want a refusal on the pull request the window closed on", note)
	}
	if reads != 0 {
		t.Fatalf("settings read %d times after the window expired, want none", reads)
	}
}

// The decision's reads share one bounded window set by the post step itself: a
// settings read sees a deadline no further out than prReviewRequestTimeout.
func TestPRReviewPostBoundsTheExhaustedDecisionToOneWindow(t *testing.T) {
	f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel), checkRuns: greenChecks()}
	getenv := f.start(t)
	var remaining time.Duration
	var bounded bool
	read := func(ctx context.Context) (store.AutoMergeSetting, error) {
		var deadline time.Time
		if deadline, bounded = ctx.Deadline(); bounded {
			remaining = time.Until(deadline)
		}
		return switchedOn(ctx)
	}
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, spentAllowance()), "-context-result", "success"}
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, read, rateUnder, recordNothing, args); err != nil {
		t.Fatal(err)
	}
	if !bounded || remaining <= 0 || remaining > prReviewRequestTimeout {
		t.Fatalf("settings read saw bounded=%v, %s left, want a deadline within %s", bounded, remaining, prReviewRequestTimeout)
	}
}

// An owner's switch that is off is named even where the PR also lacks the
// eligibility label, because a switch turned off before the PR opened leaves no
// label and the missing label would hide the cause.
func TestPRReviewPostNamesTheSwitchBeforeTheMissingLabel(t *testing.T) {
	f := &fakePulls{pull: openPull("t", "", headSHA), checkRuns: greenChecks()}
	getenv := f.start(t)
	off := func(context.Context) (store.AutoMergeSetting, error) {
		return store.AutoMergeSetting{Enabled: false}, nil
	}
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, spentAllowance()), "-context-result", "success"}
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, off, rateUnder, recordNothing, args); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || !strings.Contains(f.created[0], "auto-merge switch is off") {
		t.Fatalf("comment = %v, want the switch named, not the missing label", f.created)
	}
}

// logDetail logs the evidence a decline carries, and its words where it carries
// none.
func TestMergeNoteLogDetail(t *testing.T) {
	for name, tt := range map[string]struct {
		note mergeNote
		want string
	}{
		"evidence behind the refusal": {mergeNote{Reason: "settings-unread", Words: "the settings could not be read", Detail: "permission denied"}, "permission denied"},
		"words alone":                 {mergeNote{Reason: "switch-off", Words: "the owner's auto-merge switch is off"}, "the owner's auto-merge switch is off"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tt.note.logDetail(); got != tt.want {
				t.Fatalf("logDetail = %q, want %q", got, tt.want)
			}
		})
	}
}

// The store helper refuses a job without a project before it opens a client, so
// a read cannot silently land in whatever project the credentials name.
func TestWithStoreRefusesAMissingProject(t *testing.T) {
	called := false
	open := func(context.Context, string) (*firestore.Client, error) {
		t.Fatal("a client was opened without a project")
		return nil, nil
	}
	err := withStore(context.Background(), func(string) string { return "" }, open, func(*firestore.Client) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("err = %v, want the missing environment named", err)
	}
	if called {
		t.Fatal("withStore ran its read without a project")
	}
}

// A client the job cannot open fails the read with that error, and nothing runs
// against a client it does not have.
func TestWithStoreWrapsAClientItCannotOpen(t *testing.T) {
	called := false
	open := func(context.Context, string) (*firestore.Client, error) {
		return nil, errors.New("could not find default credentials")
	}
	err := withStore(context.Background(), func(string) string { return "p" }, open, func(*firestore.Client) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "firestore client: could not find default credentials") {
		t.Fatalf("err = %v, want the client's error wrapped", err)
	}
	if called {
		t.Fatal("withStore ran its read without a client")
	}
}

// The rate the job should have read but could not is evidence that could not be
// read, which holds the merge rather than letting it through, and the cause is
// logged so a Firestore or IAM failure is diagnosable from the job's log.
func TestStoredRewriteRateReadsAMissingProjectAsEvidenceUnread(t *testing.T) {
	var logs bytes.Buffer
	read := storedRewriteRate(slog.New(slog.NewTextHandler(&logs, nil)), func(string) string { return "" })
	if got := read(context.Background()); got.Reason != dispatcher.AutoMergeReasonEvidenceUnread || got.WithinGuideline {
		t.Fatalf("rate = %+v, want the evidence-unread reason and no pass", got)
	}
	if got := logs.String(); !strings.Contains(got, "prReviewRateUnread") || !strings.Contains(got, "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("log = %q, want the unread rate and its cause", got)
	}
}

// A clean review of an eligible PR still requests nothing once the switch has
// been turned off, an escaped PR holds auto-merge or the settings cannot be read:
// the eligibility label was set before the review ran.
func TestPRReviewPostDeclinesTheMergeWhileTheSettingsHold(t *testing.T) {
	for name, tt := range map[string]struct {
		settings autoMergeSettings
		reason   string
	}{
		"the switch is off": {func(context.Context) (store.AutoMergeSetting, error) {
			return store.AutoMergeSetting{Enabled: false}, nil
		}, dispatcher.AutoMergeReasonSwitchOff},
		"an escaped PR holds it": {func(context.Context) (store.AutoMergeSetting, error) {
			return store.AutoMergeSetting{Enabled: true,
				Escaped: dispatcher.MergeEscape{Repo: "o/r", PR: 5, By: 9, Reason: dispatcher.EscapeReverted}}, nil
		}, dispatcher.AutoMergeReasonEscaped},
		"the settings cannot be read": {func(context.Context) (store.AutoMergeSetting, error) {
			return store.AutoMergeSetting{}, errors.New("permission denied")
		}, "settings-unread"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: openPull("t", "", headSHA, autoMergeEligibleLabel)}
			getenv := f.start(t)
			var logs bytes.Buffer
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, verdict("clean", "", museModel)), "-context-result", "success"}
			if err := prReviewPostWith(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), getenv, tt.settings, rateUnder, recordNothing, args); err != nil {
				t.Fatal(err)
			}
			if got := readOutput(t, getenv); !strings.Contains(got, "merge=false") {
				t.Fatalf("output = %q, want the merge declined", got)
			}
			if got := logs.String(); !strings.Contains(got, "prReviewMergeDeclined") || !strings.Contains(got, "reason="+tt.reason) {
				t.Fatalf("log = %q, want the decline and reason %s", got, tt.reason)
			}
			if s := f.statuses[headSHA]; s.State != "success" {
				t.Fatalf("status = %+v, want the clean review still posted", s)
			}
		})
	}
}

// Settings are read only for a review that would otherwise request the merge.
func TestPRReviewPostReadsTheSettingsOnlyForAMergeItWouldRequest(t *testing.T) {
	f := &fakePulls{pull: openPull("t", "", headSHA)}
	getenv := f.start(t)
	reads := 0
	counting := func(ctx context.Context) (store.AutoMergeSetting, error) {
		reads++
		return switchedOn(ctx)
	}
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, verdict("clean", "", museModel)), "-context-result", "success"}
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, counting, rateUnder, recordNothing, args); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatalf("reads = %d, want none for a PR that is not eligible", reads)
	}
}

func TestPRReviewPostSetsAnErrorStatusForASkippedReview(t *testing.T) {
	f := &fakePulls{}
	getenv := f.start(t)
	args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-skip-reason", "no-ticket-id", "-context-result", "success"}
	if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, recordNothing, args); err != nil {
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
		museModel:   func(context.Context, string, io.Writer, io.Writer) error { return errors.New("exit status 1") },
		pricedModel: reviewReply(tokensUsed, "x is missing"),
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
	if err := runPRReview(context.Background(), quietLogger(), agent, []string{museModel, pricedModel}, time.Minute, contextPath, out, summary, nil, noSpecialists); err != nil {
		t.Fatal(err)
	}
	v, err := readVerdict(out)
	if err != nil || v.Outcome != "findings" || v.Model != pricedModel || len(v.Attempts) != 2 || v.Attempts[0].Outcome != "agent failed" {
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
	err := runPRReview(context.Background(), quietLogger(), agent, []string{museModel}, time.Minute, path, filepath.Join(t.TempDir(), "v"), "", nil, noSpecialists)
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
		"on:\n  pull_request_target:\n",
		"    types: [opened, ready_for_review, reopened, synchronize]\n",
		"(github.event.action != 'synchronize' || startsWith(github.event.pull_request.head.ref, 'wingman/'))",
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
		"post":    "    permissions:\n      checks: read\n      id-token: write\n      pull-requests: write\n      statuses: write\n    steps:",
	} {
		if !strings.Contains(jobs[job], want) {
			t.Errorf("the %s job's permissions are not %q", job, want)
		}
	}
	// The sign-in step must not stop the review being posted: it is the only step before the
	// build, so a failure there would otherwise skip the post and its status.
	_, afterAuth, _ := strings.Cut(jobs["post"], "      - id: auth\n")
	authStep, _, _ := strings.Cut(afterAuth, "\n      - ")
	for _, want := range []string{"continue-on-error: true", "workload_identity_provider: ${{ vars.GCP_WIF_PROVIDER }}"} {
		if !strings.Contains(authStep, want) {
			t.Errorf("the post job's sign-in step lacks %q", want)
		}
	}
	if strings.Contains(jobs["review"], "GH_TOKEN") || strings.Contains(jobs["review"], "google-github-actions/auth") {
		t.Error("the review job holds a GitHub or GCP credential")
	}
	if !strings.Contains(jobs["review"], "MODELS: ${{ vars.WINGMAN_PR_REVIEW_MODELS || ") {
		t.Error("the review job does not read its models from WINGMAN_PR_REVIEW_MODELS")
	}
	if !strings.Contains(jobs["review"], "OPENCODE_API_KEY: ${{ secrets.OPENCODE_API_KEY }}") || strings.Count(yml, "secrets.OPENCODE_API_KEY") != 1 {
		t.Error("the model key is not the review job's alone")
	}
	// The head's ref is read once, in the context job's condition, and never checked out.
	if strings.Count(yml, "github.event.pull_request.head.ref") != 1 ||
		strings.Count(jobs["context"], "startsWith(github.event.pull_request.head.ref, 'wingman/')") != 1 ||
		strings.Contains(yml, "\n          ref:") ||
		strings.Count(yml, "persist-credentials: false") != strings.Count(yml, "actions/checkout@") {
		t.Error("pr-review.yml checks out something other than the base branch, or keeps a checkout's token")
	}
	for _, want := range []string{
		"if: steps.post.outputs.merge == 'true'",
		"continue-on-error: true",
		`any(. == "go") and any(. == "web")`,
		`gh pr merge "$PR" --auto --squash --match-head-commit "$REVIEWED_SHA"`,
		"GH_TOKEN: ${{ steps.app-token.outputs.token }}",
		// The settings re-read signs in as the pr-review identity, and a failed sign-in must not
		// stop the review being posted.
		"google-github-actions/auth@",
		"service_account: ${{ vars.GCP_PR_REVIEW_SERVICE_ACCOUNT }}",
		"GOOGLE_CLOUD_PROJECT: ${{ steps.auth.outputs.project_id }}",
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

func TestUpsertCommentEditsTheMarkedCommentBehindAnUnmarkedOne(t *testing.T) {
	summary := map[string]any{"id": 40, "body": "CI summary", "user": map[string]string{"login": prReviewCommentAuthor}}
	f := &fakePulls{comments: []map[string]any{summary, reviewCommentOf(prReviewMarker + "\nold")}}
	gh := f.client(f.start(t))
	if err := upsertComment(context.Background(), gh, 7, "new"); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 || len(f.edited) != 1 || f.edited["41"] != "new" {
		t.Fatalf("created = %q, edited = %q, want only comment 41 edited", f.created, f.edited)
	}
}
