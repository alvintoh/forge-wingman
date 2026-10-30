package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// prRequestTimeout bounds one GitHub request the pr step makes.
const prRequestTimeout = 30 * time.Second

// prStep publishes a run's screenshots on the evidence branch and writes them
// into its pull request's body: the section the shots render, replaced in place
// where the body already has one. A run that captured none — no front-end
// paths, or a capture that failed — says so in one line and still succeeds, so
// a PR without a picture never stops its run.
func prStep(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	number := fs.Int("pr", 0, "the pull request to publish the screenshots on")
	sha := fs.String("sha", "", "the head sha the run's shots were taken at")
	shots := fs.String("shots", "", "the shots artifact directory; empty when the run captured none")
	remote := fs.String("remote", "", "the git remote to push to; empty means the repository's own")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *number <= 0 || !shaPattern.MatchString(*sha) {
		return errors.New("pr needs -pr and a -sha")
	}
	repo := getenv("GITHUB_REPOSITORY")
	if repo == "" {
		return errors.New("missing environment: GITHUB_REPOSITORY")
	}
	token := getenv("GH_TOKEN")
	if token == "" {
		return errors.New("missing environment: GH_TOKEN")
	}
	if *remote == "" {
		*remote = "https://github.com/" + repo + ".git"
	}
	published, err := runner.PublishEvidence(ctx, runner.Evidence{
		PR: *number, Dir: *shots, Remote: *remote, Token: token,
	})
	if err != nil {
		return fmt.Errorf("publishing the screenshots for #%d: %w", *number, err)
	}
	gh := pullRequests{API: getenv("GITHUB_API_URL"), Token: token, Repo: repo,
		Client: &http.Client{Timeout: prRequestTimeout}}
	p, err := gh.pull(ctx, *number)
	if err != nil {
		return err
	}
	body := runner.WithScreenshots(p.Body, runner.ScreenshotsSection(repo, *sha, published))
	if err := gh.editPull(ctx, *number, body); err != nil {
		return err
	}
	logger.Info("screenshotsPublished", "pr", *number, "shots", len(published), "sha", *sha)
	return nil
}
