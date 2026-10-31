package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// maxEvidenceFileBytes bounds one screenshot read from the shots artifact: the
	// pull request body shows it at 200px, so a larger file is a capture bug, not
	// evidence worth pushing.
	maxEvidenceFileBytes = 8 << 20
	// evidencePushAttempts bounds how often a commit is retried when another run's
	// commit moved the evidence branch between reading it and moving it on.
	evidencePushAttempts = 3
)

// errEvidenceRefLost reports the evidence branch moving between reading it and
// moving it on, which a rebuild over the branch as it stands then survives. Any
// other failure is this run's own and is not retried.
var errEvidenceRefLost = errors.New("the evidence branch moved under the push")

// prEvidence pushes a run's screenshots to the evidence branch and shows them in
// the pull request body. A run that captured none leaves the body as pr-meta
// worded it and still succeeds: the diff is the deliverable, the pictures are
// not.
//
// The shots artifact is only produced for a front-end change, so its presence is
// what marks this pull request as one, and the images are of fixture data.
func prEvidence(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	number := fs.Int("pr", 0, "the pull request the screenshots belong to")
	shots := fs.String("shots", "", "the directory the shots artifact was downloaded to, empty when the run captured none")
	repository := fs.String("repository", getenv("GITHUB_REPOSITORY"), "the repository the run builds in, as owner/name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *number <= 0 {
		return errors.New("pr needs -pr")
	}
	if *repository == "" {
		return errors.New("pr needs -repository")
	}
	gh := pullRequests{API: getenv("GITHUB_API_URL"), Token: getenv("GH_TOKEN"), Repo: *repository,
		Client: &http.Client{Timeout: prReviewRequestTimeout}}
	return publishScreenshots(ctx, logger, gh, *number, *shots)
}

// publishScreenshots pushes the shots artifact's PNGs under the pull request's
// folder on the evidence branch and replaces the body's screenshots section with
// them, naming the head sha they were shot at.
func publishScreenshots(ctx context.Context, logger *slog.Logger, gh pullRequests, number int, dir string) error {
	shots, err := runner.ReadShots(dir)
	if err != nil {
		return fmt.Errorf("reading the shots artifact: %w", err)
	}
	if len(shots) == 0 {
		logger.Info("prScreenshotsNone", "pr", number)
		return nil
	}
	p, err := gh.pull(ctx, number)
	if err != nil {
		return fmt.Errorf("reading pull request %d: %w", number, err)
	}
	ev, err := runner.PlaceShots(shots, number, p.Head.SHA)
	if err != nil {
		return fmt.Errorf("placing the screenshots at the head sha: %w", err)
	}
	if err := gh.pushEvidence(ctx, ev, number); err != nil {
		return fmt.Errorf("pushing the screenshots: %w", err)
	}
	section := runner.Screenshots(ev, gh.Repo)
	body, err := runner.ReplaceScreenshots(p.Body, section)
	if errors.Is(err, runner.ErrNoScreenshotsSection) {
		// A pull request opened before the section existed: seed it, so the body
		// gains the shots rather than the step failing.
		logger.Info("prScreenshotsSectionSeeded", "pr", number)
		body, err = runner.AppendScreenshots(p.Body, section), nil
	}
	if err != nil {
		return fmt.Errorf("rendering the screenshots section: %w", err)
	}
	if err := gh.editBody(ctx, number, body); err != nil {
		return fmt.Errorf("editing pull request %d: %w", number, err)
	}
	logger.Info("prScreenshotsPushed", "pr", number, "sha", ev.SHA, "files", len(ev.Files))
	return nil
}

// editBody replaces the pull request's body.
func (g pullRequests) editBody(ctx context.Context, number int, body string) error {
	return g.call(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", g.Repo, number),
		"", map[string]string{"body": body}, maxPullBytes, nil)
}

// pushEvidence commits ev's files to the evidence branch: one blob each, then a
// tree over the branch's own, then the commit the ref moves to. The first push
// starts the branch, so a repository that never captured a screenshot gains one
// commit holding only what the pull requests pushed.
func (g pullRequests) pushEvidence(ctx context.Context, ev runner.Evidence, number int) error {
	entries := make([]treeEntry, 0, len(ev.Files))
	for _, f := range ev.Files {
		sha, err := g.createBlob(ctx, f.Source)
		if err != nil {
			return fmt.Errorf("storing the screenshot %s: %w", f.Source, err)
		}
		entries = append(entries, treeEntry{Path: f.Path, Mode: "100644", Type: "blob", SHA: sha})
	}
	message := fmt.Sprintf("chore(evidence): add %s screenshots at %s", runner.EvidenceFolder(number), ev.SHA)
	// A second run committing to the branch between this read and the ref
	// update makes the update a lost race, not a failure: the commit is rebuilt
	// over the branch as it stands then. Any other failure is this run's own and
	// is not retried.
	var err error
	for range evidencePushAttempts {
		err = g.commitEvidence(ctx, entries, message)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errEvidenceRefLost) {
			break
		}
	}
	return fmt.Errorf("writing the commit: %w", err)
}

// commitEvidence writes entries as one commit on top of the evidence branch as
// it stands now, and moves the branch to it.
func (g pullRequests) commitEvidence(ctx context.Context, entries []treeEntry, message string) error {
	tip, err := g.evidenceTip(ctx)
	if err != nil {
		return err
	}
	tree := map[string]any{"tree": entries}
	parents := []string{}
	if tip != "" {
		base, err := g.evidenceBaseTree(ctx, tip)
		if err != nil {
			return err
		}
		tree["base_tree"] = base
		parents = append(parents, tip)
	}
	var created struct {
		SHA string `json:"sha"`
	}
	if err := g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/git/trees", g.Repo),
		"", tree, maxPullBytes, &created); err != nil {
		return err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/git/commits", g.Repo), "",
		map[string]any{"message": message, "tree": created.SHA, "parents": parents}, maxPullBytes, &commit); err != nil {
		return err
	}
	if tip == "" {
		if err := g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/git/refs", g.Repo), "",
			map[string]any{"ref": "refs/heads/" + runner.EvidenceBranch, "sha": commit.SHA}, maxPullBytes, nil); err != nil {
			return refRace(err)
		}
		return nil
	}
	if err := g.call(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/git/refs/heads/%s", g.Repo, runner.EvidenceBranch), "",
		map[string]any{"sha": commit.SHA, "force": false}, maxPullBytes, nil); err != nil {
		return refRace(err)
	}
	return nil
}

// refRace wraps a ref create or update that lost to another run's commit, which
// a rebuild over the branch as it stands then survives. GitHub answers a lost
// create with 422 (the branch already exists) and a lost update with 409 or 422
// (not a fast forward); any other failure is this run's own and is returned as
// such, so it is not retried.
func refRace(err error) error {
	var api *apiError
	if errors.As(err, &api) && (api.Code == http.StatusConflict || api.Code == http.StatusUnprocessableEntity) {
		return fmt.Errorf("%w: %w", errEvidenceRefLost, err)
	}
	return fmt.Errorf("moving the evidence branch: %w", err)
}

// gitRef is one of the refs matching-refs answers with.
type gitRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// evidenceTip is the evidence branch's head, empty when the branch does not
// exist yet. The matching form answers an empty list for a branch nobody has
// pushed, where the ref itself would answer 404.
func (g pullRequests) evidenceTip(ctx context.Context) (string, error) {
	var refs []gitRef
	path := fmt.Sprintf("/repos/%s/git/matching-refs/heads/%s", g.Repo, runner.EvidenceBranch)
	if err := g.call(ctx, http.MethodGet, path, "", nil, maxPullBytes, &refs); err != nil {
		return "", err
	}
	for _, r := range refs {
		if r.Ref == "refs/heads/"+runner.EvidenceBranch {
			return r.Object.SHA, nil
		}
	}
	return "", nil
}

// evidenceBaseTree is the tree of the commit at tip, which a new commit is laid
// over so the folders of every other pull request survive.
func (g pullRequests) evidenceBaseTree(ctx context.Context, tip string) (string, error) {
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	path := fmt.Sprintf("/repos/%s/git/commits/%s", g.Repo, tip)
	if err := g.call(ctx, http.MethodGet, path, "", nil, maxPullBytes, &commit); err != nil {
		return "", err
	}
	return commit.Tree.SHA, nil
}

// treeEntry is one path of a tree, as GitHub's git data API takes it.
type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// createBlob stores the file at path as a blob and returns its sha.
func (g pullRequests) createBlob(ctx context.Context, path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(raw) > maxEvidenceFileBytes {
		return "", fmt.Errorf("%s is %d bytes, over the %d-byte screenshot limit", path, len(raw), maxEvidenceFileBytes)
	}
	var blob struct {
		SHA string `json:"sha"`
	}
	body := map[string]string{"content": base64.StdEncoding.EncodeToString(raw), "encoding": "base64"}
	if err := g.call(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/git/blobs", g.Repo),
		"", body, maxPullBytes, &blob); err != nil {
		return "", err
	}
	return blob.SHA, nil
}
