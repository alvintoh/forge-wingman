package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/store"
)

const (
	modeOn  = "on"
	modeOff = "off"
)

// autoMergeSwitchStore is the stored auto-merge settings the subcommand reads and sets.
type autoMergeSwitchStore interface {
	Read(ctx context.Context) (store.AutoMergeSetting, error)
	SetEnabled(ctx context.Context, enabled bool, at time.Time) error
	SetBar(ctx context.Context, percent int, at time.Time) error
}

// autoMergeSwitch prints the auto-merge settings, or with -mode and -bar sets one
// or both for the next pr-meta. Its account check only guards against a run on
// the wrong account by mistake: Firestore IAM decides who may set it.
func autoMergeSwitch(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("auto-merge", flag.ContinueOnError)
	mode := fs.String("mode", "", "on or off; omitted, the switch is left as it is")
	bar := fs.Int("bar", 0, "the rewrite rate's bar as a percentage from 1 to 100; omitted, the bar is left as it is")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q: use -mode %s or -mode %s, and/or -bar <percent>", fs.Arg(0), modeOn, modeOff)
	}
	modeGiven, barGiven := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mode":
			modeGiven = true
		case "bar":
			barGiven = true
		}
	})
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if modeGiven && *mode != modeOn && *mode != modeOff {
		return fmt.Errorf("-mode %q is not %s or %s", *mode, modeOn, modeOff)
	}
	if barGiven && (*bar < store.MinBarPercent || *bar > store.MaxBarPercent) {
		return fmt.Errorf("-bar %d is not a whole number from %d to %d", *bar, store.MinBarPercent, store.MaxBarPercent)
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	return runAutoMergeSwitch(ctx, logger, os.Stdout, store.NewAutoMergeSwitches(fsc), *mode, modeGiven, *bar, barGiven, time.Now())
}

// runAutoMergeSwitch sets the switch and the bar each is given, and otherwise
// prints the settings as stored.
func runAutoMergeSwitch(ctx context.Context, logger *slog.Logger, out io.Writer, s autoMergeSwitchStore, mode string, modeGiven bool, bar int, barGiven bool, at time.Time) error {
	if !modeGiven && !barGiven {
		setting, err := s.Read(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, autoMergeSettingWords(setting))
		return err
	}
	if modeGiven {
		enabled := mode == modeOn
		if err := s.SetEnabled(ctx, enabled, at); err != nil {
			return err
		}
		logger.Info("autoMergeSwitchSet", "mode", mode)
	}
	if barGiven {
		if err := s.SetBar(ctx, bar, at); err != nil {
			return err
		}
		logger.Info("autoMergeBarSet", "bar", strconv.Itoa(bar)+"%")
	}
	return nil
}

// autoMergeSettingWords prints the settings as the command's one line: the
// switch, the bar, and the escaped PR while one holds auto-merge off.
func autoMergeSettingWords(s store.AutoMergeSetting) string {
	word := modeOff
	if s.Enabled {
		word = modeOn
	}
	line := fmt.Sprintf("%s, bar %d%%", word, s.BarPercent)
	if s.Escaped.PR > 0 {
		line += fmt.Sprintf(", escaped %s#%d until switched on again", s.Escaped.Repo, s.Escaped.PR)
	}
	return line
}
