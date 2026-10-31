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
	refs     []gitRef

	mu         sync.Mutex
	treePaths  []string
	baseTrees  []string
	parents    []string
	refMoves   int
	refWrite   string
	refUpdate  map[string]any
	body       string
	commitRead []string
	requests   []string
	auth       []string

	// refConflicts is how many ref writes lose to another run before one succeeds.
	refConflicts int
	// refStatus is the status a lost ref write answers, 409 when unset.
	refStatus int
	// nextTip is the tip the branch reads as once the first conflict moved it.
	nextTip string
	// fail is one request, as "GET /repos/o/r/pulls/7", answered with failStatus.
	fail string
	// failStatus is the status fail answers, 500 when unset.
	failStatus int
}

// moveTipOnConflict moves the branch on, as another run's push landing first does.
func (f *fakeEvidence) moveTipOnConflict() {
	if f.nextTip == "" {
		return
	}
	next := gitRef{Ref: "refs/heads/" + runner.EvidenceBranch}
	next.Object.SHA = f.nextTip
	f.refs = []gitRef{next}
	f.nextTip = ""
}

// refLost answers a ref write that lost, and moves the branch on.
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
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if f.fail == hit {
			status := f.failStatus
			if status == 0 {
				status = http.StatusInternalServerError
			}
			w.WriteHeader(status)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/7":
			_, _ = fmt.Fprintf(w, `{"title":"feat(web): ABC-1 add the widget","body":%q,"state":"open",`+
				`"head":{"sha":%q},"base":{"sha":"fedcba9876543210fedcba9876543210fedcba98"}}`, f.pullBody, evidenceHead)
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
			var commit struct {
				Parents json.RawMessage `json:"parents"`
			}
			_ = json.Unmarshal(raw, &commit)
			f.parents = append(f.parents, string(commit.Parents))
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
			_ = json.Unmarshal(raw, &f.refUpdate)
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
	env := map[string]string{"GITHUB_API_URL": srv.URL, "GH_TOKEN": "app-token", "GITHUB_TOKEN": "other-token",
		"GITHUB_REPOSITORY": "o/r"}
	return func(k string) string { return env[k] }
}

// evidenceShots writes the shots artifact a capture produced, one PNG per
// viewport per route.
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

// sparseFile writes a file of size bytes without holding them.
func sparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

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

func TestPRRefusesAShotOutsideAViewportsFolder(t *testing.T) {
	f := &fakeEvidence{}
	getenv := f.start(t)
	shots := evidenceShots(t, "wide/home.png")
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", shots}); err == nil {
		t.Fatal("err = nil, want the artifact refused")
	}
}

// The commit is laid over the branch's own tree, so other pull requests' folders survive.
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

// The retry reads the branch as it stands, so its tree is laid over the other run's tip.
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

func TestPRDoesNotRetryAFailureOfItsOwn(t *testing.T) {
	for _, tt := range []struct {
		name   string
		fail   string
		status int
		tip    string
	}{
		{name: "the tip read", fail: "GET /repos/o/r/git/matching-refs/heads/evidence"},
		{name: "the base tree", fail: "GET /repos/o/r/git/commits/c0ffee", tip: "c0ffee"},
		{name: "a blob", fail: "POST /repos/o/r/git/blobs"},
		{name: "the tree", fail: "POST /repos/o/r/git/trees"},
		{name: "the commit", fail: "POST /repos/o/r/git/commits"},
		{name: "a ref create", fail: "POST /repos/o/r/git/refs"},
		{name: "a ref update", fail: "PATCH /repos/o/r/git/refs/heads/evidence", status: http.StatusForbidden, tip: "c0ffee"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
			f.fail, f.failStatus = tt.fail, tt.status
			if tt.tip != "" {
				ref := gitRef{Ref: "refs/heads/" + runner.EvidenceBranch}
				ref.Object.SHA = tt.tip
				f.refs = []gitRef{ref}
			}
			getenv := f.start(t)
			err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")})
			if err == nil || errors.Is(err, errEvidenceRefLost) {
				t.Fatalf("err = %v, want this run's own failure reported", err)
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

// GitHub answers a create for an existing branch with 422, which retries as an update.
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

func TestPRStopsWhenEveryRefAttemptLoses(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	f.refConflicts, f.nextTip = 10, "c0ffee"
	getenv := f.start(t)
	err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")})
	if !errors.Is(err, errEvidenceRefLost) {
		t.Fatalf("err = %v, want the lost race named", err)
	}
	if f.refMoves != 3 {
		t.Fatalf("ref updates = %d, want 3", f.refMoves)
	}
	if f.body != "" {
		t.Fatalf("body = %q, want no edit after a failed push", f.body)
	}
}

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

func TestPRRefusesAnOversizeScreenshotBeforePushingAny(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	getenv := f.start(t)
	dir := evidenceShots(t, "s/a.png")
	sparseFile(t, filepath.Join(dir, "s", "z.png"), 8388609)
	err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", dir})
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("err = %v, want the oversize screenshot refused", err)
	}
	if blobs := countHits(f.requests, "POST /repos/o/r/git/blobs"); blobs != 0 || f.refMoves != 0 || f.body != "" {
		t.Fatalf("blobs = %d, ref updates = %d, body = %q, want nothing pushed", blobs, f.refMoves, f.body)
	}
}

func TestPRRefusesASymlinkedScreenshot(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	getenv := f.start(t)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := evidenceShots(t, "s/index.png")
	if err := os.Symlink(secret, filepath.Join(dir, "s", "home.png")); err != nil {
		t.Fatal(err)
	}
	err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", dir})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want the symlink refused", err)
	}
	if blobs := countHits(f.requests, "POST /repos/o/r/git/blobs"); blobs != 0 || f.refMoves != 0 || f.body != "" {
		t.Fatalf("blobs = %d, ref updates = %d, body = %q, want nothing pushed", blobs, f.refMoves, f.body)
	}
}

func TestPRLeavesTheBodyAloneForANameItCannotShow(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	getenv := f.start(t)
	shots := evidenceShots(t, "s/x\n## Notes\nINJECTED.png")
	err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", shots})
	if err == nil || !strings.Contains(err.Error(), "cannot show") {
		t.Fatalf("err = %v, want the name refused", err)
	}
	if blobs := countHits(f.requests, "POST /repos/o/r/git/blobs"); blobs != 0 || f.refMoves != 0 || f.body != "" {
		t.Fatalf("blobs = %d, ref updates = %d, body = %q, want nothing pushed", blobs, f.refMoves, f.body)
	}
}

func TestCreateBlobBoundsWhatItReads(t *testing.T) {
	for _, tt := range []struct {
		name    string
		size    int64
		refused bool
	}{
		{name: "at the limit", size: 8388608},
		{name: "one byte over", size: 8388609, refused: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEvidence{}
			getenv := f.start(t)
			gh := pullRequests{API: getenv("GITHUB_API_URL"), Token: "t", Repo: "o/r"}
			path := filepath.Join(t.TempDir(), "big.png")
			sparseFile(t, path, tt.size)
			sha, err := gh.createBlob(context.Background(), path)
			blobs := countHits(f.requests, "POST /repos/o/r/git/blobs")
			if tt.refused {
				if err == nil || !strings.Contains(err.Error(), "screenshot limit") || blobs != 0 {
					t.Fatalf("sha = %q, err = %v, blobs = %d, want the file refused unsent", sha, err, blobs)
				}
				return
			}
			if err != nil || sha != "blob" || blobs != 1 {
				t.Fatalf("sha = %q, err = %v, blobs = %d, want the file stored", sha, err, blobs)
			}
		})
	}
}

func TestPRMatchesTheEvidenceBranchExactly(t *testing.T) {
	decoy := gitRef{Ref: "refs/heads/evidence-x"}
	decoy.Object.SHA = "decoy"
	branch := gitRef{Ref: "refs/heads/evidence"}
	branch.Object.SHA = "c0ffee"
	for _, tt := range []struct {
		name  string
		refs  []gitRef
		write string
		base  []string
	}{
		{name: "a prefixed ref alone is no branch", refs: []gitRef{decoy}, write: "create", base: []string{""}},
		{name: "the branch beside a prefixed ref", refs: []gitRef{decoy, branch}, write: "move", base: []string{"tree-c0ffee"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
			f.refs = tt.refs
			getenv := f.start(t)
			if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err != nil {
				t.Fatal(err)
			}
			if f.refWrite != tt.write || !slices.Equal(f.baseTrees, tt.base) {
				t.Fatalf("ref = %q, base_tree = %v, want %q and %v", f.refWrite, f.baseTrees, tt.write, tt.base)
			}
		})
	}
}

func TestPRMovesTheBranchWithoutForce(t *testing.T) {
	branch := gitRef{Ref: "refs/heads/evidence"}
	branch.Object.SHA = "c0ffee"
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n", refs: []gitRef{branch}}
	getenv := f.start(t)
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err != nil {
		t.Fatal(err)
	}
	if force, ok := f.refUpdate["force"]; !ok || force != false {
		t.Fatalf("ref update = %v, want force false: a push that is not a fast forward must lose", f.refUpdate)
	}
}

func TestPRParentsTheCommitOnTheTipItRead(t *testing.T) {
	branch := gitRef{Ref: "refs/heads/evidence"}
	branch.Object.SHA = "c0ffee"
	for _, tt := range []struct {
		name string
		refs []gitRef
		want string
	}{
		{name: "an existing branch", refs: []gitRef{branch}, want: `["c0ffee"]`},
		{name: "a new branch", want: `[]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n", refs: tt.refs}
			getenv := f.start(t)
			if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err != nil {
				t.Fatal(err)
			}
			if want := []string{tt.want}; !slices.Equal(f.parents, want) {
				t.Fatalf("parents = %v, want %v", f.parents, want)
			}
		})
	}
}

func TestPRAuthenticatesWithGHToken(t *testing.T) {
	f := &fakeEvidence{pullBody: "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n"}
	getenv := f.start(t)
	if err := prEvidence(context.Background(), quietLogger(), getenv, []string{"-pr", "7", "-shots", evidenceShots(t, "s/index.png")}); err != nil {
		t.Fatal(err)
	}
	if len(f.auth) == 0 {
		t.Fatal("no request was made")
	}
	for _, got := range f.auth {
		if got != "Bearer app-token" {
			t.Fatalf("Authorization = %q, want the GH_TOKEN value", got)
		}
	}
}

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
