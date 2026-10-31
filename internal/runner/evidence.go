package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// EvidenceBranch is the branch a run pushes a pull request's screenshots to.
// The App token that opens the pull request cannot upload to GitHub's
// attachment store, so the images live on a branch the token can push.
const EvidenceBranch = "evidence"

// MaxShotBytes is the largest screenshot a run may push.
const MaxShotBytes = 8 << 20

// shotWidths are the viewports a run captures, in the order the pull request
// body shows them.
var shotWidths = []string{"s", "m", "l", "xl"}

// errShotsLayout reports a shots artifact holding a PNG outside a viewport's
// folder, which would otherwise be dropped from the pull request body.
var errShotsLayout = errors.New("shots artifact holds a PNG outside a viewport's folder")

// errEvidencePath reports a screenshot whose place on the evidence branch would
// fall outside the pull request's own folder.
var errEvidencePath = errors.New("screenshot path is outside the pull request's folder")

// errShotFile reports a PNG in the shots artifact that is not a regular file,
// which a symlink would turn into a read of whatever it points at.
var errShotFile = errors.New("shots artifact holds a PNG that is not a regular file")

// errShotSize reports a screenshot over MaxShotBytes.
var errShotSize = errors.New("screenshot is over the size limit")

// errShotName reports a screenshot name the body's table cannot show as one cell.
var errShotName = errors.New("screenshot name holds a character the body cannot show")

// errShotCell reports two screenshots for one route and viewport, which the
// body's table has a single cell for.
var errShotCell = errors.New("two screenshots for one route and viewport")

// errEvidencePR reports a screenshot that names no pull request, which no folder
// on the evidence branch could hold.
var errEvidencePR = errors.New("screenshot belongs to no pull request")

// evidenceSHA is a commit sha as GitHub writes it, which is the folder a run's
// screenshots sit under.
var evidenceSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// EvidenceFolder is the folder on EvidenceBranch that pull request number's
// screenshots live under.
func EvidenceFolder(number int) string { return "pr-" + strconv.Itoa(number) }

// evidencePath is where the shots artifact's file at rel lives on EvidenceBranch
// for pull request number at sha: inside that pull request's own folder, never
// outside it. A number that is not a pull request's has no folder to hold it.
func evidencePath(number int, sha, rel string) (string, error) {
	if number <= 0 {
		return "", fmt.Errorf("%w: pull request %d", errEvidencePR, number)
	}
	clean := path.Clean(rel)
	if rel == "" || strings.ContainsRune(rel, '\\') || path.IsAbs(clean) ||
		clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", errEvidencePath, rel)
	}
	if !evidenceSHA.MatchString(sha) {
		return "", fmt.Errorf("%w: sha %q", errEvidencePath, sha)
	}
	return EvidenceFolder(number) + "/" + sha + "/" + clean, nil
}

// evidenceURL is the address the pull request body shows the file at p by: the
// raw form of EvidenceBranch in repository, which GitHub renders as an image.
func evidenceURL(repository, p string) string {
	u := url.URL{Scheme: "https", Host: "raw.githubusercontent.com",
		Path: "/" + repository + "/" + EvidenceBranch + "/" + p}
	return u.String()
}

// Shot is one screenshot in the artifact a run's capture produced: the page it
// shows, the viewport it was taken at, and the file itself.
type Shot struct {
	// Route is the page it shows, as "/" for the site's root.
	Route string
	// Width is the viewport it was taken at, one of shotWidths.
	Width string
	// File is where the artifact holds it.
	File string
	// Rel is its path in the artifact, "<width>/<route>.png".
	Rel string
}

// ReadShots reads the shots artifact at dir: one PNG per route per viewport,
// under a folder named for the viewport, where a file named index is its
// folder's page. A directory holding no PNG is a run that captured none. Any
// other file is ignored, and a PNG under no viewport's folder is refused rather
// than silently left out of the pull request body.
//
// A PNG that is not a regular file, or is over MaxShotBytes, refuses the whole
// set before anything is read.
func ReadShots(dir string) ([]Shot, error) {
	if dir == "" {
		return nil, nil
	}
	var shots []Shot
	err := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(p), ".png") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%w: %q", errShotFile, rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxShotBytes {
			return fmt.Errorf("%w: %q is %d bytes, over %d", errShotSize, rel, info.Size(), MaxShotBytes)
		}
		width, page, ok := strings.Cut(rel, "/")
		if !ok || !slices.Contains(shotWidths, width) {
			return fmt.Errorf("%w: %s", errShotsLayout, rel)
		}
		shots = append(shots, Shot{Route: shotRoute(page), Width: width, File: p, Rel: rel})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(shots, func(a, b Shot) int {
		if byRoute := strings.Compare(a.Route, b.Route); byRoute != 0 {
			return byRoute
		}
		return slices.Index(shotWidths, a.Width) - slices.Index(shotWidths, b.Width)
	})
	return shots, nil
}

// shotRoute is the page a shot at page in the artifact pictures: its folder's
// own page when the file is named index, and its own path otherwise.
func shotRoute(page string) string {
	route := strings.TrimSuffix(page, path.Ext(page))
	route = strings.TrimSuffix(route, "/index")
	if route == "index" {
		route = ""
	}
	return "/" + route
}

// Evidence is a run's screenshots at the places they are pushed for one pull
// request: the sha they were shot at, and each file with the page and viewport
// it shows.
type Evidence struct {
	SHA   string
	Files []EvidenceFile
}

// EvidenceFile is one screenshot at its place on EvidenceBranch.
type EvidenceFile struct {
	// Route is the page it shows.
	Route string
	// Width is the viewport it was taken at.
	Width string
	// Source is the file in the shots artifact.
	Source string
	// Path is where it goes on EvidenceBranch.
	Path string
}

// PlaceShots places shots at their place on EvidenceBranch for pull request
// number at sha. A shot that would land outside the pull request's folder, one
// whose name the body's table cannot show, or a second shot for a cell the table
// has one of, refuses the whole set: nothing is pushed for a partial answer, and
// no screenshot is silently dropped.
func PlaceShots(shots []Shot, number int, sha string) (Evidence, error) {
	ev := Evidence{SHA: sha, Files: make([]EvidenceFile, 0, len(shots))}
	cell := make(map[string]bool, len(shots))
	for _, s := range shots {
		if strings.IndexFunc(s.Rel, unshowable) >= 0 {
			return Evidence{}, fmt.Errorf("%w: %q", errShotName, s.Rel)
		}
		p, err := evidencePath(number, sha, s.Rel)
		if err != nil {
			return Evidence{}, err
		}
		key := s.Route + " " + s.Width
		if cell[key] {
			return Evidence{}, fmt.Errorf("%w: %s at %s (%s)", errShotCell, s.Route, s.Width, s.Rel)
		}
		cell[key] = true
		ev.Files = append(ev.Files, EvidenceFile{Route: s.Route, Width: s.Width, Source: s.File, Path: p})
	}
	return ev, nil
}

// unshowable reports a character a name cannot carry into the body: a control
// character, or one that ends a table cell, a code span or an attribute.
func unshowable(r rune) bool {
	return unicode.IsControl(r) || strings.ContainsRune("|`<>", r)
}
