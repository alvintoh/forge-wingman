package runner

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// shotsDir writes the shots artifact a capture of routes produced: one PNG per
// viewport per route, at <viewport>/<route>.png.
func shotsDir(t *testing.T, files ...string) string {
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

func TestReadShotsReadsOneRoutePerViewport(t *testing.T) {
	dir := shotsDir(t, "s/index.png", "m/index.png", "s/checkout/start.png", "README.md")
	shots, err := ReadShots(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range shots {
		got = append(got, s.Route+" "+s.Width+" "+s.Rel)
	}
	want := []string{"/ s s/index.png", "/ m m/index.png", "/checkout/start s s/checkout/start.png"}
	if !slices.Equal(got, want) {
		t.Fatalf("shots = %v, want %v", got, want)
	}
}

func TestReadShotsRefusesAPNGOutsideAViewportsFolder(t *testing.T) {
	dir := shotsDir(t, "wide/home.png")
	if _, err := ReadShots(dir); !errors.Is(err, errShotsLayout) {
		t.Fatalf("err = %v, want errShotsLayout: a screenshot would be left out of the body", err)
	}
}

func TestReadShotsOfNoArtifactIsNone(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "absent"), shotsDir(t, "notes.md")} {
		shots, err := ReadShots(dir)
		if err != nil || len(shots) != 0 {
			t.Errorf("%q: shots = %v, err = %v, want none and no error", dir, shots, err)
		}
	}
}

func TestEvidencePathKeepsEveryShotUnderThePRsFolder(t *testing.T) {
	got, err := EvidencePath(7, testSHA, "s/checkout/start.png")
	if err != nil {
		t.Fatal(err)
	}
	if want := "pr-7/" + testSHA + "/s/checkout/start.png"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestEvidencePathRefusesAPathOutsideThePRsFolder(t *testing.T) {
	for _, tt := range [][2]string{
		{"an upward step", "../s/home.png"},
		{"a step out of the middle", "s/../../home.png"},
		{"an absolute path", "/s/home.png"},
		{"the folder itself", "."},
		{"one step up alone", ".."},
		{"nothing at all", ""},
		{"a windows separator", `s\home.png`},
	} {
		if got, err := EvidencePath(7, testSHA, tt[1]); !errors.Is(err, errEvidencePath) {
			t.Errorf("%s: path = %q, err = %v, want the push refused", tt[0], got, err)
		}
	}
}

func TestEvidencePathRefusesAShaThatIsNotOne(t *testing.T) {
	if _, err := EvidencePath(7, "../../notes", "s/home.png"); !errors.Is(err, errEvidencePath) {
		t.Fatalf("err = %v, want the sha refused", err)
	}
}

func TestEvidencePathRefusesAPullRequestNumberThatIsNotOne(t *testing.T) {
	for _, number := range []int{0, -1} {
		if got, err := EvidencePath(number, testSHA, "s/home.png"); !errors.Is(err, errEvidencePR) {
			t.Errorf("pull request %d: path = %q, err = %v, want refused", number, got, err)
		}
	}
}

func TestPlaceShotsRefusesAShotAtNoPullRequest(t *testing.T) {
	shots := []Shot{{Route: "/home", Width: "s", File: "a", Rel: "s/home.png"}}
	ev, err := PlaceShots(shots, 0, testSHA)
	if !errors.Is(err, errEvidencePR) {
		t.Fatalf("err = %v, want errEvidencePR", err)
	}
	if len(ev.Files) != 0 {
		t.Fatalf("placed %d files, want none for a refused set", len(ev.Files))
	}
}

func TestPlaceShotsRefusesAHeadThatIsNotASha(t *testing.T) {
	shots := []Shot{{Route: "/home", Width: "s", File: "a", Rel: "s/home.png"}}
	ev, err := PlaceShots(shots, 7, "HEAD")
	if !errors.Is(err, errEvidencePath) {
		t.Fatalf("err = %v, want the head refused", err)
	}
	if len(ev.Files) != 0 {
		t.Fatalf("placed %d files, want none for a refused set", len(ev.Files))
	}
}

func TestPlaceShotsRefusesTheWholeSetForOneBadShot(t *testing.T) {
	shots := []Shot{
		{Route: "/home", Width: "s", File: "a", Rel: "s/home.png"},
		{Route: "/home", Width: "m", File: "b", Rel: "../m/home.png"},
	}
	ev, err := PlaceShots(shots, 7, testSHA)
	if !errors.Is(err, errEvidencePath) {
		t.Fatalf("err = %v, want errEvidencePath", err)
	}
	if len(ev.Files) != 0 {
		t.Fatalf("placed %d files, want none for a refused set", len(ev.Files))
	}
}

func TestPlaceShotsRefusesTwoShotsForOneCell(t *testing.T) {
	shots := []Shot{
		{Route: "/foo", Width: "s", File: "a", Rel: "s/foo.png"},
		{Route: "/foo", Width: "s", File: "b", Rel: "s/foo/index.png"},
	}
	ev, err := PlaceShots(shots, 7, testSHA)
	if !errors.Is(err, errShotCell) {
		t.Fatalf("err = %v, want errShotCell: a cell holds one image", err)
	}
	if len(ev.Files) != 0 {
		t.Fatalf("placed %d files, want none for a refused set", len(ev.Files))
	}
}

func TestEvidenceURLIsTheBranchesRawForm(t *testing.T) {
	got := EvidenceURL("o/r", "pr-7/"+testSHA+"/s/home.png")
	want := "https://raw.githubusercontent.com/o/r/evidence/pr-7/" + testSHA + "/s/home.png"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, "/home.png") {
		t.Fatalf("url = %q, want the file's own name", got)
	}
}
