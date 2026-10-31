package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// evidenceHead is the sha the fake pull request's head is at.
const evidenceHead = "0123456789abcdef0123456789abcdef01234567"

// fakeEvidence serves one repository's pull request 7, its git objects and the
// evidence branch, and records every write the pr step makes.
type fakeEvidence struct {
	pullBody string
	headSHA  string
	refs     []gitRef

	mu         sync.Mutex
	treePaths  []string
	baseTrees  []string
	refMoves   int
	refWrite   string
	body       string
	commitRead []string
	requests   []string

	// refConflicts is how many ref creates or updates answer a conflict before
	// one succeeds, as a second run committing at the same moment does.
	refConflicts int
	// refStatus is the status a conflicting ref answers; 409 when unset.
	refStatus int
	// nextTip is the tip the branch reads as once the first conflict moved it.
	nextTip string
	// fail names one request this fake answers with a server error, as
	// "GET /repos/o/r/pulls/7": this run's own failure, which no retry repeats.
	fail string
}

// moveTipOnConflict is another run's push landing first: the branch moves on,
// so the retry reads a tip whose tree already holds that run's folder.
func (f *fakeEvidence) moveTipOnConflict() {
	if f.nextTip == "" {
		return
	}
	next := gitRef{Ref: "refs/heads/" + runner.EvidenceBranch}
	next.Object.SHA = f.nextTip
	f.refs = []gitRef{next}
	f.nextTip = ""
}

// refLost is the answer another run's commit earns: the status GitHub gives a
// create or update that lost, and the tip that run left behind.
func (f *fakeEvidence) refLost(w http.ResponseWriter) {
	f.refConflicts--
	f.moveTipOnConflict()
	status := f.refStatus
	if status == 0 {
		status = http.StatusConflict
	}
	w.WriteHeader(status)
}

// countHits is how many times the fake served the request want names.
func countHits(requests []string, want string) int {
	hits := 0
	for _, r := range requests {
		if r == want {
			hits++
		}
	}
	return hits
}

func (f *fakeEvidence) start(t *testing.T) func(string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		hit := r.Method + " " + r.URL.Path
		f.requests = append(f.requests, hit)
		if f.fail == hit {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/7":
			head := f.headSHA
			if head == "" {
				head = evidenceHead
			}
			_, _ = fmt.Fprintf(w, `{"title":"feat(web): ABC-1 add the widget","body":%q,"state":"open",`+
				`"head":{"sha":%q},"base":{"sha":"fedcba9876543210fedcba9876543210fedcba98"}}`, f.pullBody, head)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/git/matching-refs/heads/evidence":
			_ = json.NewEncoder(w).Encode(f.refs)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/o/r/git/commits/"):
			sha := strings.TrimPrefix(r.URL.Path, "/repos/o/r/git/commits/")
			f.commitRead = append(f.commitRead, sha)
			_, _ = io.WriteString(w, `{"tree":{"sha":"tree-`+sha+`"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/git/blobs":
			_, _ = io.WriteString(w, `{"sha":"blob"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/git/trees":
			var tree struct {
				BaseTree string `json:"base_tree"`
				Tree     []struct {
					Path string `json:"path"`
				} `json:"tree"`
			}
			_ = json.Unmarshal(raw, &tree)
			f.baseTrees = append(f.baseTrees, tree.BaseTree)
			for _, e := range tree.Tree {
				f.treePaths = append(f.treePaths, e.Path)
			}
			_, _ = io.WriteString(w, `{"sha":"tree"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/git/commits":
			_, _ = io.WriteString(w, `{"sha":"commit"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/git/refs":
			f.refMoves++
			if f.refConflicts > 0 {
				f.refLost(w)
				return
			}
			f.refWrite = "create"
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/git/refs/heads/evidence":
			f.refMoves++
			if f.refConflicts > 0 {
				f.refLost(w)
				return
			}
			f.refWrite = "move"
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/pulls/7":
			var body map[string]string
			_ = json.Unmarshal(raw, &body)
			f.body = body["body"]
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"GITHUB_API_URL": srv.URL, "GH_TOKEN": "app-token", "GITHUB_REPOSITORY": "o/r"}
	return func(k string) string { return env[k] }
}

// evidenceShots writes the shots artifact a capture produced: one PNG per
// viewport per route, at <viewport>/<route>.png.
func evidenceShots(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("png"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestPRPushesTheShotsAndShowsThemInTheBody is the whole step: the PNGs land on
// the evidence branch under the pull request's own folder, and its body gains the
// table of routes at the head sha they were shot at.
func TestPRPushesTheShotsAndShowsThemInTheBody(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Summary\n\nAdds the widget.\n\n## Screenshots (fixture data)\n" +
		"No screenshots: this run captured none.\n\n## Notes\n\nBuilt unattended.\n"}
	getenv := f.start(t)
	shots := evidenceShots(t, "s/index.png", "m/index.png", "l/index.png", "xl/index.png", "s/checkout.png")
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", shots}); err != nil {
		t.Fatal(err)
	}
	prefix := "pr-7/" + evidenceHead + "/"
	wantPaths := []string{prefix + "s/index.png", prefix + "m/index.png", prefix + "l/index.png",
		prefix + "xl/index.png", prefix + "s/checkout.png"}
	if !slices.Equal(f.treePaths, wantPaths) {
		t.Fatalf("pushed %v, want %v", f.treePaths, wantPaths)
	}
	raw := "https://raw.githubusercontent.com/o/r/evidence/" + prefix
	for _, want := range []string{
		"## Screenshots (fixture data) — shot at `" + evidenceHead + "`",
		"| Route | s | m | l | xl |",
		"| `/` | <img width=\"200\" alt=\"/ at s\" src=\"" + raw + "s/index.png\">",
		"| `/checkout` | <img width=\"200\" alt=\"/checkout at s\" src=\"" + raw + "s/checkout.png\"> | — | — | — |",
		"## Summary", "## Notes",
	} {
		if !strings.Contains(f.body, want) {
			t.Errorf("body lacks %q:\n%s", want, f.body)
		}
	}
	if f.refWrite != "create" {
		t.Fatalf("ref = %q, want create: the branch did not exist yet", f.refWrite)
	}
}

// TestPRReplacesTheSectionForANewSHA is the second push to a pull request: the
// body keeps one screenshots section, naming the sha it was shot at now.
func TestPRReplacesTheSectionForANewSHA(t *testing.T) {
	const old = "fedcba9876543210fedcba9876543210fedcba98"
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data) — shot at `" + old + "`\n\n" +
		"| Route | s | m | l | xl |\n|---|---|---|---|---|\n" +
		"| `/` | <img width=\"200\" alt=\"/ at s\" src=\"https://raw.githubusercontent.com/o/r/evidence/pr-7/" + old + "/s/index.png\"> | — | — | — |\n\n" +
		"## Notes\nBuilt unattended.\n"}
	getenv := f.start(t)
	shots := evidenceShots(t, "s/index.png")
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", shots}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(f.body, "## Screenshots (fixture data)"); n != 1 {
		t.Fatalf("body holds %d screenshots sections, want 1:\n%s", n, f.body)
	}
	if strings.Contains(f.body, old) || !strings.Contains(f.body, evidenceHead) {
		t.Fatalf("body does not name the sha it was shot at:\n%s", f.body)
	}
	if !strings.Contains(f.body, "\n\n## Notes\nBuilt unattended.") {
		t.Fatalf("the section was not replaced in place:\n%s", f.body)
	}
}

// TestPRWithoutAShotsArtifactLeavesTheBodyAlone is the run that captured none:
// pr-meta's own line stands, nothing is pushed, and the step still succeeds.
func TestPRWithoutAShotsArtifactLeavesTheBodyAlone(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n\n## Notes\n"}
	getenv := f.start(t)
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7"}); err != nil {
		t.Fatal(err)
	}
	if f.body != "" || f.refWrite != "" || len(f.treePaths) != 0 {
		t.Fatalf("body = %q, ref = %q, paths = %v: want nothing written", f.body, f.refWrite, f.treePaths)
	}
}

// TestPRRefusesAShotOutsideAViewportsFolder is an artifact the step cannot read
// as evidence: it fails rather than push a body that silently drops a shot.
func TestPRRefusesAShotOutsideAViewportsFolder(t *testing.T) {
	f := &fakeEvidence{}
	getenv := f.start(t)
	shots := evidenceShots(t, "wide/home.png")
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", shots}); err == nil {
		t.Fatal("err = nil, want the artifact refused")
	}
}

// TestPRMovesTheBranchItFinds is the second pull request to push: the commit is
// laid over the branch as it stands, so no other pull request's folder is
// dropped.
func TestPRMovesTheBranchItFinds(t *testing.T) {
	ref := gitRef{Ref: "refs/heads/" + runner.EvidenceBranch}
	ref.Object.SHA = "c0ffee"
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	f.refs = []gitRef{ref}
	getenv := f.start(t)
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err != nil {
		t.Fatal(err)
	}
	if f.refWrite != "move" {
		t.Fatalf("ref = %q, want move: the branch already existed", f.refWrite)
	}
	if want := []string{"tree-c0ffee"}; !slices.Equal(f.baseTrees, want) {
		t.Fatalf("base_tree = %v, want the branch's own tree, %v", f.baseTrees, want)
	}
}

// TestPRRebuildsItsCommitWhenAnotherRunMovesTheBranch is the ref race: the first
// update loses to a run that committed first, the retry reads the branch as it
// stands then, and its tree is laid over that tip's own, so the other pull
// request's folder is still there.
func TestPRRebuildsItsCommitWhenAnotherRunMovesTheBranch(t *testing.T) {
	ref := gitRef{Ref: "refs/heads/" + runner.EvidenceBranch}
	ref.Object.SHA = "c0ffee"
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	f.refConflicts, f.nextTip = 1, "c0ffee2"
	f.refs = []gitRef{ref}
	getenv := f.start(t)
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err != nil {
		t.Fatal(err)
	}
	if f.refMoves != 2 {
		t.Fatalf("ref updates = %d, want 2: the lost race is retried once", f.refMoves)
	}
	if want := []string{"tree-c0ffee", "tree-c0ffee2"}; !slices.Equal(f.baseTrees, want) {
		t.Fatalf("base_tree = %v, want the branch's own tree each time, %v", f.baseTrees, want)
	}
	if want := []string{"c0ffee", "c0ffee2"}; !slices.Equal(f.commitRead, want) {
		t.Fatalf("tip read = %v, want the branch re-read after the conflict, %v", f.commitRead, want)
	}
}

// TestPRDoesNotRetryAFailureOfItsOwn pins the retry to the ref race: every other
// hop is this run's own failure, so it is tried once and reported.
func TestPRDoesNotRetryAFailureOfItsOwn(t *testing.T) {
	for _, tt := range []struct {
		name string
		fail string
		tip  string
	}{
		{name: "the tip read", fail: "GET /repos/o/r/git/matching-refs/heads/evidence"},
		{name: "the base tree", fail: "GET /repos/o/r/git/commits/c0ffee", tip: "c0ffee"},
		{name: "a blob", fail: "POST /repos/o/r/git/blobs"},
		{name: "the tree", fail: "POST /repos/o/r/git/trees"},
		{name: "the commit", fail: "POST /repos/o/r/git/commits"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
			f.fail = tt.fail
			if tt.tip != "" {
				ref := gitRef{Ref: "refs/heads/" + runner.EvidenceBranch}
				ref.Object.SHA = tt.tip
				f.refs = []gitRef{ref}
			}
			getenv := f.start(t)
			if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err == nil {
				t.Fatal("err = nil, want the failure reported")
			}
			if hits := countHits(f.requests, tt.fail); hits != 1 {
				t.Fatalf("%s was tried %d times, want once", tt.fail, hits)
			}
			if f.refMoves != 0 || f.body != "" {
				t.Fatalf("ref updates = %d, body = %q, want nothing pushed", f.refMoves, f.body)
			}
		})
	}
}

// TestPRRebuildsTheCommitWhenTheBranchIsCreatedUnderIt is the create race: the
// tip read finds no branch, another run creates it, and the 422 a create earns
// for an existing branch is retried as an update over that run's tip.
func TestPRRebuildsTheCommitWhenTheBranchIsCreatedUnderIt(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	f.refConflicts, f.nextTip, f.refStatus = 1, "c0ffee", http.StatusUnprocessableEntity
	getenv := f.start(t)
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err != nil {
		t.Fatal(err)
	}
	if f.refWrite != "move" {
		t.Fatalf("ref = %q, want move: the branch existed by the retry", f.refWrite)
	}
	if f.refMoves != 2 {
		t.Fatalf("ref updates = %d, want 2: the create and the update it became", f.refMoves)
	}
	if want := []string{"", "tree-c0ffee"}; !slices.Equal(f.baseTrees, want) {
		t.Fatalf("base_tree = %v, want the attempt over no branch then the creator's, %v", f.baseTrees, want)
	}
}

// TestPRStopsWhenEveryRefAttemptLoses is the retry's bound: consecutive lost
// races stop the step, and the body is left alone.
func TestPRStopsWhenEveryRefAttemptLoses(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	f.refConflicts, f.nextTip = evidencePushAttempts, "c0ffee"
	getenv := f.start(t)
	err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")})
	if !errors.Is(err, errEvidenceRefLost) {
		t.Fatalf("err = %v, want the lost race named", err)
	}
	if f.refMoves != evidencePushAttempts {
		t.Fatalf("ref updates = %d, want %d", f.refMoves, evidencePushAttempts)
	}
	if f.body != "" {
		t.Fatalf("body = %q, want no edit after a failed push", f.body)
	}
}

// TestPRStopsWhenThePullCannotBeRead leaves the branch and the body untouched:
// nothing is pushed before the head sha is read.
func TestPRStopsWhenThePullCannotBeRead(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	f.fail = "GET /repos/o/r/pulls/7"
	getenv := f.start(t)
	err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")})
	if err == nil || !strings.Contains(err.Error(), "reading pull request 7") {
		t.Fatalf("err = %v, want the unreadable pull named", err)
	}
	if f.refMoves != 0 || len(f.treePaths) != 0 || f.body != "" {
		t.Fatalf("ref updates = %d, paths = %v, body = %q: want nothing pushed", f.refMoves, f.treePaths, f.body)
	}
}

// TestPRReportsAnEditItCannotMake is a push that landed and a body edit that did
// not: the step names the edit and leaves the body as it was.
func TestPRReportsAnEditItCannotMake(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	f.fail = "PATCH /repos/o/r/pulls/7"
	getenv := f.start(t)
	err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")})
	if err == nil || !strings.Contains(err.Error(), "editing pull request 7") {
		t.Fatalf("err = %v, want the refused edit named", err)
	}
	if len(f.treePaths) == 0 {
		t.Fatal("nothing was pushed: the push runs before the edit")
	}
	if f.body != "" {
		t.Fatalf("body = %q, want the edit refused", f.body)
	}
}

// TestPRRefusesAScreenshotOverTheLimit is the size bound: the body shows a
// screenshot at 200px, so a file that large is a capture bug and is refused
// before anything is pushed.
func TestPRRefusesAScreenshotOverTheLimit(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	getenv := f.start(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "s"), 0o755); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxEvidenceFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "s", "home.png"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", dir})
	if err == nil || !strings.Contains(err.Error(), "screenshot limit") {
		t.Fatalf("err = %v, want the oversize screenshot refused", err)
	}
	if f.refMoves != 0 || f.body != "" {
		t.Fatalf("ref updates = %d, body = %q, want nothing pushed", f.refMoves, f.body)
	}
}

// TestPRSeedsASectionABodyPredates is a pull request opened before the section
// existed: its body gains the section rather than the step failing.
func TestPRSeedsASectionABodyPredates(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Summary\n\nAdds the widget.\n\n## Notes\n\nBuilt unattended.\n"}
	getenv := f.start(t)
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(f.body, "## Screenshots (fixture data)"); n != 1 {
		t.Fatalf("body holds %d screenshots sections, want 1:\n%s", n, f.body)
	}
	if !strings.Contains(f.body, "shot at `"+evidenceHead+"`") || !strings.Contains(f.body, "## Notes\n\nBuilt unattended.") {
		t.Fatalf("body does not keep its notes and gain the section:\n%s", f.body)
	}
}

// TestPRNeedsAPullRequestAndARepository covers the step's own usage guards: with
// no pull request, no repository, or a flag it does not know, it stops before it
// sends anything.
func TestPRNeedsAPullRequestAndARepository(t *testing.T) {
	unset := func(string) string { return "" }
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no pull request", args: []string{"-pr", "0"}, want: "pr needs -pr"},
		{name: "no repository", args: []string{"-pr", "7"}, want: "pr needs -repository"},
		{name: "unknown flag", args: []string{"-pr", "7", "-nope"}, want: "flag provided but not defined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := prEvidence(context.Background(), quietLogger(), unset, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
