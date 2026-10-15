package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// autonomyStore is the stored level with the measured evidence its advance
// conditions are checked against.
type autonomyStore interface {
	Evidence(ctx context.Context) (dispatcher.Evidence, error)
	Level(ctx context.Context) (dispatcher.Autonomy, error)
	Set(ctx context.Context, level dispatcher.Autonomy, at time.Time) error
	SetEarned(ctx context.Context, e dispatcher.Earned) error
	MarkStale(ctx context.Context) error
}

// historyStore is where an owner's change of level is recorded.
type historyStore interface {
	Append(ctx context.Context, c dispatcher.Change) error
}

// autonomy prints the autonomy level and the numbers each level's advance
// condition is measured from, recomputing and storing the earned-level snapshot,
// or with -level sets the level for the next claim.
// Its account check only guards against a run on the wrong account by mistake:
// Firestore IAM decides who may set it.
func autonomy(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("autonomy", flag.ContinueOnError)
	level := fs.Int("level", 0, "the level to set, 0 to 5; omitted, print the current level and its conditions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "level" })
	// Only a level that names none is refused here: a level's own conditions are
	// measured from the store, which the client below opens.
	if set {
		if err := dispatcher.Autonomy(*level).Known(); err != nil {
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
	return runAutonomy(ctx, logger, os.Stdout, store.NewAutonomies(fsc), store.NewAutonomyHistory(fsc), dispatcher.Autonomy(*level), set, time.Now())
}

// runAutonomy sets level when set is true, and otherwise prints the current one
// with what it allows, the trailing rate, and each level's advance condition
// with the numbers behind it. Either way it recomputes and stores the earned
// level. A level whose condition does not hold is refused with those numbers
// rather than written; a level with no condition is set even when the evidence
// cannot be read, and its history entry says so.
func runAutonomy(ctx context.Context, logger *slog.Logger, out io.Writer, s autonomyStore, h historyStore, level dispatcher.Autonomy, set bool, at time.Time) error {
	ev, evErr := s.Evidence(ctx)
	var earned dispatcher.Earned
	var recErr error
	if evErr == nil {
		earned, recErr = dispatcher.Recompute(ctx, s, ev, at)
	}
	if !set {
		return errors.Join(evErr, recErr, printIfRead(out, ev, evErr, recErr))
	}
	if level.AutoMerges() {
		if evErr != nil || recErr != nil {
			return errors.Join(evErr, recErr)
		}
		if err := level.Settable(ev); err != nil {
			return fmt.Errorf("-level: %w", err)
		}
	}
	numbers := earned.Numbers
	if evErr != nil {
		logger.Warn("autonomyEvidenceUnavailable", "err", evErr.Error())
		numbers = dispatcher.Numbers{Unavailable: true}
		ev.Level, _ = s.Level(ctx)
	} else if recErr != nil {
		logger.Warn("autonomyEarnedUnstored", "err", recErr.Error())
		_, numbers = dispatcher.EarnedLevel(ev)
	}
	if err := s.Set(ctx, level, at); err != nil {
		return err
	}
	logger.Info("autonomySet", "level", int(level))
	change := dispatcher.Change{From: ev.Level, To: level, Cause: dispatcher.ChangeOwnerSet, Numbers: numbers, At: at}
	if err := h.Append(ctx, change); err != nil {
		return fmt.Errorf("level set, but recording it failed: %w", err)
	}
	return nil
}

// printIfRead prints ev unless it or its recomputation failed.
func printIfRead(out io.Writer, ev dispatcher.Evidence, evErr, recErr error) error {
	if evErr != nil || recErr != nil {
		return nil
	}
	return printAutonomy(out, ev)
}

// printAutonomy writes the level and what it allows, the rate its conditions are
// measured from, and for each level whose advance condition is measured whether
// it holds, clause by clause with the numbers behind it.
func printAutonomy(out io.Writer, ev dispatcher.Evidence) error {
	trailing := ev.Window(dispatcher.TrailingPRs)
	lines := dispatcher.Lines(trailing)
	var report []string
	report = append(report, fmt.Sprintf("autonomy %d: %s", int(ev.Level), ev.Level.Describe()))
	report = append(report, fmt.Sprintf("trailing %d PRs: %.1f%% rewrite rate, %d owner lines of %d agent lines over %d PRs",
		dispatcher.TrailingPRs, lines.Rate()*100, lines.OwnerLines, lines.AgentLines, len(trailing)))
	for level := dispatcher.AutonomyAutoMerge; level <= dispatcher.AutonomyAutoMerge+1; level++ {
		conds := level.Advance(ev)
		if len(conds) == 0 {
			continue
		}
		held := "not held"
		if dispatcher.AllHeld(conds) {
			held = "held"
		}
		report = append(report, fmt.Sprintf("advance to level %d: %s", int(level), held))
		for _, c := range conds {
			report = append(report, "  "+c.String())
		}
	}
	report = append(report, fmt.Sprintf("levels 0 to %d: no condition to hold", int(dispatcher.AutonomyRolledBack)))
	_, err := fmt.Fprintln(out, strings.Join(report, "\n"))
	return err
}
