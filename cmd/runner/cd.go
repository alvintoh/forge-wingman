package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/store"
)

// cdStore is the recorded CD flag the subcommand reads and sets.
type cdStore interface {
	Deploying(ctx context.Context, repo string) (bool, error)
	Set(ctx context.Context, repo string, deploying bool, at time.Time) error
}

// cd prints whether a target repository is recorded as deploying on merge, or
// with -deploying records it: the flag a level-4 advance condition requires, and
// the product cannot read for itself, since the dispatcher holds no repository
// access. Its account check only guards against a run on the wrong account by
// mistake: Firestore IAM decides who may set it.
func cd(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("cd", flag.ContinueOnError)
	repo := fs.String("repo", "", "the target repository, as owner/name")
	deploying := fs.Bool("deploying", false, "record that a merge to the repository deploys; -deploying=false records that it does not")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("-repo is required: the target repository, as owner/name")
	}
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "deploying" })
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	return runCD(ctx, logger, os.Stdout, store.NewCDs(fsc), *repo, *deploying, set, time.Now())
}

// runCD records repo's CD flag when a value is named, and otherwise prints it.
func runCD(ctx context.Context, logger *slog.Logger, out io.Writer, s cdStore, repo string, deploying, set bool, at time.Time) error {
	if set {
		if err := s.Set(ctx, repo, deploying, at); err != nil {
			return err
		}
		logger.Info("cdRecorded", "repo", repo, "deploying", deploying)
		return nil
	}
	current, err := s.Deploying(ctx, repo)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s deploys on merge: %t\n", repo, current)
	return err
}
