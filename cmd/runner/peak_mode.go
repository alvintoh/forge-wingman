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

// peakModeStore is the stored peak mode the subcommand reads and sets.
type peakModeStore interface {
	Mode(ctx context.Context) (dispatcher.PeakMode, error)
	Set(ctx context.Context, mode dispatcher.PeakMode, at time.Time) error
}

// peakMode prints the peak mode, or with -mode sets it for the next poll. Its
// account check only guards against a run on the wrong account by mistake:
// Firestore IAM decides who may set it.
func peakMode(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("peak-mode", flag.ContinueOnError)
	mode := fs.String("mode", "", "off-peak-only or always; omitted, print the current mode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if *mode != "" && !dispatcher.PeakMode(*mode).Valid() {
		return fmt.Errorf("-mode %q is not %s or %s", *mode, dispatcher.PeakOffPeakOnly, dispatcher.PeakAlways)
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	return runPeakMode(ctx, logger, os.Stdout, store.NewPeakModes(fsc), dispatcher.PeakMode(*mode), time.Now())
}

// runPeakMode sets mode when one is named, and otherwise prints the current one.
func runPeakMode(ctx context.Context, logger *slog.Logger, out io.Writer, s peakModeStore, mode dispatcher.PeakMode, at time.Time) error {
	if mode == "" {
		current, err := s.Mode(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, current)
		return err
	}
	if err := s.Set(ctx, mode, at); err != nil {
		return err
	}
	logger.Info("peakModeSet", "mode", string(mode))
	return nil
}
