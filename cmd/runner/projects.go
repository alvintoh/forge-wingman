package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/store"
)

// projectStore is the owner's project allowlist the subcommands write.
type projectStore interface {
	Add(ctx context.Context, linearProject, repository string, at time.Time) error
	Remove(ctx context.Context, repository string) (bool, error)
}

// projectAdd lists a project and its repository in the owner database. Its
// account check only guards against a run on the wrong account by mistake:
// Firestore IAM decides who may write (adr/0019).
func projectAdd(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("project-add", flag.ContinueOnError)
	linearProject := fs.String("linear-project", "", "the project's Linear project name")
	repo := fs.String("repo", "", "the project's repository, as owner/name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if *linearProject == "" {
		return errors.New("-linear-project is required: the project's Linear project name")
	}
	if !store.ValidRepository(*repo) {
		return fmt.Errorf("-repo %q is not owner/name", *repo)
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClientWithDatabase(ctx, e.project, store.OwnerDatabase)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	return runProjectAdd(ctx, logger, store.NewProjects(fsc), *linearProject, *repo, time.Now())
}

// projectRemove unlists a project by its repository. Its account check only
// guards against a run on the wrong account by mistake: Firestore IAM decides
// who may write (adr/0019).
func projectRemove(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("project-remove", flag.ContinueOnError)
	repo := fs.String("repo", "", "the project's repository, as owner/name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if !store.ValidRepository(*repo) {
		return fmt.Errorf("-repo %q is not owner/name", *repo)
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClientWithDatabase(ctx, e.project, store.OwnerDatabase)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	return runProjectRemove(ctx, logger, store.NewProjects(fsc), *repo)
}

// runProjectAdd lists the project and reports what it stored.
func runProjectAdd(ctx context.Context, logger *slog.Logger, s projectStore, linearProject, repository string, at time.Time) error {
	if err := s.Add(ctx, linearProject, repository, at); err != nil {
		return err
	}
	logger.Info("projectAdded", "linear_project", linearProject, "repo", repository)
	return nil
}

// runProjectRemove unlists the repository and says what it did: it reports the
// remove, and says so when no listed project named the repository.
func runProjectRemove(ctx context.Context, logger *slog.Logger, s projectStore, repository string) error {
	removed, err := s.Remove(ctx, repository)
	if err != nil {
		return err
	}
	if !removed {
		logger.Info("projectNotListed", "repo", repository)
		return nil
	}
	logger.Info("projectRemoved", "repo", repository)
	return nil
}
