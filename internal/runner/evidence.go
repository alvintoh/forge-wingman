package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	// EvidenceBranch is the branch of the run's own repository the screenshots
	// are published on, one folder per pull request.
	EvidenceBranch = "evidence"
	// shotsManifest is the file inside the shots artifact that lists each image.
	shotsManifest = "shots.json"
)

// ErrEvidencePath reports a screenshots path that would land outside the pull
// request's own folder on the evidence branch.
var ErrEvidencePath = errors.New("screenshot path is outside the pull request's folder")

// ErrShotsManifest reports a shots artifact the run cannot publish.
var ErrShotsManifest = errors.New("the shots artifact is not publishable")

// EvidenceDir is the evidence branch folder one pull request's screenshots live in.
func EvidenceDir(pr int) string { return "pr-" + strconv.Itoa(pr) }

// EvidencePath is the evidence branch path the image named name is published at
// inside pr's own folder. A name that would land outside it is refused, so a
// publish only ever writes under pr-<n>/.
func EvidencePath(pr int, name string) (string, error) {
	clean := path.Clean(name)
	if pr <= 0 || clean != name || clean == "." || clean == ".." ||
		path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", ErrEvidencePath, name)
	}
	return EvidenceDir(pr) + "/" + clean, nil
}

// shot is one entry of the shots artifact's manifest.
type shot struct {
	Route string `json:"route"`
	Band  string `json:"band"`
	File  string `json:"file"`
}

// shotManifest is the shots artifact's manifest, one entry per image captured.
type shotManifest struct {
	Shots []shot `json:"shots"`
}

// readShots reads the shots artifact in dir for pull request pr. A run with no
// artifact and an artifact with no manifest are no shots, not a failure; a
// manifest that is malformed or names an image outside the folder is.
func readShots(dir string, pr int) ([]Screenshot, error) {
	if dir == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, shotsManifest))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the shots manifest: %w", err)
	}
	var m shotManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrShotsManifest, err)
	}
	seen := map[string]bool{}
	shots := make([]Screenshot, 0, len(m.Shots))
	for _, s := range m.Shots {
		if s.Route == "" {
			return nil, fmt.Errorf("%w: a shot names no route", ErrShotsManifest)
		}
		if !slices.Contains(screenshotBands, s.Band) {
			return nil, fmt.Errorf("%w: band %q is not one of %v", ErrShotsManifest, s.Band, screenshotBands)
		}
		p, err := EvidencePath(pr, s.File)
		if err != nil {
			return nil, err
		}
		key := s.Route + "\x00" + s.Band
		if seen[key] {
			return nil, fmt.Errorf("%w: %s at %s is listed twice", ErrShotsManifest, s.Route, s.Band)
		}
		seen[key] = true
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(s.File))); err != nil {
			return nil, fmt.Errorf("reading the shots artifact: %w", err)
		}
		shots = append(shots, Screenshot{Route: s.Route, Band: s.Band, Path: p, file: s.File})
	}
	return shots, nil
}

// Evidence is the screenshots one run publishes for its pull request.
type Evidence struct {
	// PR is the pull request the shots belong to; they land under pr-<n>/.
	PR int
	// Dir is the shots artifact directory. Empty means the run captured none.
	Dir string
	// Remote is the git remote the evidence branch is pushed to.
	Remote string
	// Token is the run's GitHub App installation token.
	Token string
}

// PublishEvidence copies the shots e.Dir's manifest lists onto the evidence
// branch under e's pull request folder, replacing that folder's previous
// contents, and returns them for the body's section. A run that captured no
// shot publishes nothing and returns none.
func PublishEvidence(ctx context.Context, e Evidence) ([]Screenshot, error) {
	shots, err := readShots(e.Dir, e.PR)
	if err != nil || len(shots) == 0 {
		return nil, err
	}
	if e.Remote == "" {
		return nil, errors.New("no remote to publish the screenshots on")
	}
	dir, err := os.MkdirTemp("", "wingman-evidence-")
	if err != nil {
		return nil, fmt.Errorf("making the evidence staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := e.push(ctx, dir, shots); err != nil {
		return nil, err
	}
	return shots, nil
}

// push writes the shots under the pull request's folder in dir and pushes that
// branch to the remote, replacing the folder's previous contents.
func (e Evidence) push(ctx context.Context, dir string, shots []Screenshot) error {
	for _, args := range [][]string{
		{"init", "-q", "-b", EvidenceBranch},
		{"remote", "add", "origin", e.Remote},
	} {
		if _, err := e.git(ctx, dir, args...); err != nil {
			return err
		}
	}
	exists, err := e.branchExists(ctx, dir)
	if err != nil {
		return err
	}
	if exists {
		ref := "refs/remotes/origin/" + EvidenceBranch
		if _, err := e.git(ctx, dir, "fetch", "-q", "--depth=1", "origin", "+refs/heads/"+EvidenceBranch+":"+ref); err != nil {
			return err
		}
		if _, err := e.git(ctx, dir, "reset", "-q", "--hard", ref); err != nil {
			return err
		}
	}
	folder := EvidenceDir(e.PR)
	if err := os.RemoveAll(filepath.Join(dir, folder)); err != nil {
		return fmt.Errorf("clearing the pull request's evidence folder: %w", err)
	}
	for _, s := range shots {
		dest := filepath.Join(dir, filepath.FromSlash(s.Path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("writing the screenshots: %w", err)
		}
		raw, err := os.ReadFile(filepath.Join(e.Dir, filepath.FromSlash(s.file)))
		if err != nil {
			return fmt.Errorf("reading the screenshots: %w", err)
		}
		if err := os.WriteFile(dest, raw, 0o644); err != nil {
			return fmt.Errorf("writing the screenshots: %w", err)
		}
	}
	if _, err := e.git(ctx, dir, "add", "--", folder); err != nil {
		return err
	}
	status, err := e.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(status)) == "" {
		// The same shots are already published, so there is nothing to push.
		return nil
	}
	for _, args := range [][]string{
		{"commit", "-q", "-m", "evidence: " + folder},
		{"push", "-q", "origin", "refs/heads/" + EvidenceBranch + ":refs/heads/" + EvidenceBranch},
	} {
		if _, err := e.git(ctx, dir, args...); err != nil {
			return err
		}
	}
	return nil
}

// branchExists reports whether the evidence branch exists on the remote.
func (e Evidence) branchExists(ctx context.Context, dir string) (bool, error) {
	_, err := e.git(ctx, dir, "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+EvidenceBranch)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 2:
		return false, nil
	}
	return false, err
}

// git runs one git command in dir with the run's App token, so the remote the
// publish reads and writes is reached with that token and no other credential.
// The token rides in a header rather than the remote's URL, so a failed call's
// output cannot carry it.
func (e Evidence) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if e.Token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + e.Token))
		args = append([]string{"-c", "http.extraHeader=Authorization: Basic " + auth}, args...)
	}
	return runGit(ctx, "git", dir, nil, args...)
}
