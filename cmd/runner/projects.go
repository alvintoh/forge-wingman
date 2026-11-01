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

// phaseStore is the owner's pause on each listed project the pause subcommands
// write.
type phaseStore interface {
	Set(ctx context.Context, repository string, paused bool, at time.Time) error
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

// projectStop pauses a project, so the next poll withholds its new claims.
func projectStop(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	return setProjectPause(ctx, logger, e, args, "project-stop", true)
}

// projectResume clears a project's pause.
func projectResume(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	return setProjectPause(ctx, logger, e, args, "project-resume", false)
}

// openPhases opens the owner database's pause store and the function that closes
// it; a test replaces it to fail the open.
var openPhases = func(ctx context.Context, project string) (phaseStore, func() error, error) {
	fsc, err := firestore.NewClientWithDatabase(ctx, project, store.OwnerDatabase)
	if err != nil {
		return nil, nil, err
	}
	return store.NewPhases(fsc), fsc.Close, nil
}

// setProjectPause writes the project's pause for a stop or a resume. Repeating
// one is not an error: the write sets a value and does not toggle it. Its
// account check only guards against a run on the wrong account by mistake:
// Firestore IAM decides who may write (adr/0019).
func setProjectPause(ctx context.Context, logger *slog.Logger, e env, args []string, command string, paused bool) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
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
	phases, closePhases, err := openPhases(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = closePhases() }()
	return runProjectPause(ctx, logger, phases, *repo, paused, time.Now())
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

// runProjectPause records the project's pause and says what it set.
func runProjectPause(ctx context.Context, logger *slog.Logger, s phaseStore, repository string, paused bool, at time.Time) error {
	if err := s.Set(ctx, repository, paused, at); err != nil {
		return err
	}
	logger.Info("projectPauseSet", "repo", repository, "paused", paused)
	return nil
}
