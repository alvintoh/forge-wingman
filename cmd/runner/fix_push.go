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

// fixPushTimeout bounds the whole push: the checks, the head read and git.
const fixPushTimeout = 5 * time.Minute

// fixPushReasonError is the reason reported for a failure that is not a refusal.
const fixPushReasonError = "error"

// fixPush pushes a fix round's bundle onto the pull request's branch when it
// passes runner.FixPush's checks, and reports pushed, sha, reason and detail to
// the step. It needs no cloud project: the pull request's head is read from
// GitHub and the push uses the checkout's own credential. A refusal fails the
// step after its outputs are written.
func fixPush(ctx context.Context, logger *slog.Logger, getenv func(string) string, args []string) error {
	fs := flag.NewFlagSet("fix-push", flag.ContinueOnError)
	number := fs.Int("pr", 0, "the pull request the round was made on")
	branch := fs.String("branch", "", "the pull request's branch")
	expected := fs.String("expected", "", "the head sha the round was made on")
	bundle := fs.String("bundle", "", "the round's bundle")
	repo := fs.String("repo", ".", "the checkout to push from")
	remote := fs.String("remote", "origin", "the remote to push to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	output := getenv("GITHUB_OUTPUT")
	if *number <= 0 {
		return finishFixPush(logger, output, "", errors.New("fix-push needs -pr"))
	}
	gh := pullRequests{API: getenv("GITHUB_API_URL"), Token: getenv("GH_TOKEN"), Repo: getenv("GITHUB_REPOSITORY"),
		Client: &http.Client{Timeout: prReviewRequestTimeout}}
	ctx, cancel := context.WithTimeout(ctx, fixPushTimeout)
	defer cancel()
	sha, err := runner.FixPush(ctx, runner.FixPushInput{
		Repo:     *repo,
		Bundle:   *bundle,
		Branch:   *branch,
		Expected: *expected,
		Remote:   *remote,
		Secrets:  runner.HarnessSecrets(getenv),
		Head: func(ctx context.Context) (runner.FixPushHead, error) {
			p, err := gh.pull(ctx, *number)
			return runner.FixPushHead{Ref: p.Head.Ref, SHA: p.Head.SHA}, err
		},
	})
	return finishFixPush(logger, output, sha, err)
}

// finishFixPush reports a push to the step that reads it: pushed, the sha when
// pushed, and the reason and detail when it was not, the reason empty when it
// was. A refused or failed push is a failed step, logged once here.
func finishFixPush(logger *slog.Logger, output, sha string, err error) error {
	reason, detail := "", ""
	if err != nil {
		reason, detail = fixPushReasonError, err.Error()
		var refusal *runner.FixPushRefusal
		if errors.As(err, &refusal) {
			reason, detail = string(refusal.Reason), refusal.Detail
		}
		logger.Error("fixPushNotPushed", "reason", reason, "err", err)
	} else {
		logger.Info("fixPushPushed", "sha", sha)
	}
	if werr := writeOutputs(output, map[string]string{
		"pushed": fmt.Sprint(err == nil),
		"sha":    sha,
		"reason": reason,
	}); werr != nil {
		return errors.Join(err, werr)
	}
	if werr := writeMultilineOutput(output, "detail", detail); werr != nil {
		return errors.Join(err, werr)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errRunFailed, err)
	}
	return nil
}
