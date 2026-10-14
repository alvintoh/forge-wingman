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

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// autonomyStore is the stored autonomy level the subcommand reads and sets.
type autonomyStore interface {
	Level(ctx context.Context) (dispatcher.Autonomy, error)
	Set(ctx context.Context, level dispatcher.Autonomy, at time.Time) error
}

// autonomy prints the autonomy level, or with -level sets it for the next
// claim. Its account check only guards against a run on the wrong account by
// mistake: Firestore IAM decides who may set it.
func autonomy(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("autonomy", flag.ContinueOnError)
	level := fs.Int("level", 0, "the level to set, 0 to 5; omitted, print the current level")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "level" })
	if set {
		if err := dispatcher.Autonomy(*level).Settable(); err != nil {
			return fmt.Errorf("-level: %w", err)
		}
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	return runAutonomy(ctx, logger, os.Stdout, store.NewAutonomies(fsc), dispatcher.Autonomy(*level), set, time.Now())
}

// runAutonomy sets level when set is true, and otherwise prints the current one
// with what it allows.
func runAutonomy(ctx context.Context, logger *slog.Logger, out io.Writer, s autonomyStore, level dispatcher.Autonomy, set bool, at time.Time) error {
	if !set {
		current, err := s.Level(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "autonomy %d: %s\n", int(current), current.Describe())
		return err
	}
	if err := s.Set(ctx, level, at); err != nil {
		return err
	}
	logger.Info("autonomySet", "level", int(level))
	return nil
}
