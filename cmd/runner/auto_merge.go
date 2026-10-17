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

const (
	modeOn  = "on"
	modeOff = "off"
)

// autoMergeSwitchStore is the stored auto-merge switch the subcommand reads and sets.
type autoMergeSwitchStore interface {
	Enabled(ctx context.Context) (bool, error)
	Set(ctx context.Context, enabled bool, at time.Time) error
}

// autoMergeSwitch prints the auto-merge switch, or with -mode sets it for the
// next pr-meta. Its account check only guards against a run on the wrong
// account by mistake: Firestore IAM decides who may set it.
func autoMergeSwitch(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("auto-merge", flag.ContinueOnError)
	mode := fs.String("mode", "", "on or off; omitted, print the current switch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q: use -mode %s or -mode %s", fs.Arg(0), modeOn, modeOff)
	}
	modeGiven := false
	fs.Visit(func(f *flag.Flag) { modeGiven = modeGiven || f.Name == "mode" })
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if modeGiven && *mode != modeOn && *mode != modeOff {
		return fmt.Errorf("-mode %q is not %s or %s", *mode, modeOn, modeOff)
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	return runAutoMergeSwitch(ctx, logger, os.Stdout, store.NewAutoMergeSwitches(fsc), *mode, time.Now())
}

// runAutoMergeSwitch sets the switch when mode names one, and otherwise prints the current one.
func runAutoMergeSwitch(ctx context.Context, logger *slog.Logger, out io.Writer, s autoMergeSwitchStore, mode string, at time.Time) error {
	if mode == "" {
		enabled, err := s.Enabled(ctx)
		if err != nil {
			return err
		}
		word := modeOff
		if enabled {
			word = modeOn
		}
		_, err = fmt.Fprintln(out, word)
		return err
	}
	if err := s.Set(ctx, mode == modeOn, at); err != nil {
		return err
	}
	logger.Info("autoMergeSwitchSet", "mode", mode)
	return nil
}
