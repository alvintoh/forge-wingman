package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"regexp"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/store"
)

// enableProvider clears a halted provider's stop count, admitting it to
// dispatch again (AC5) — a manual operator action, never automatic.
func enableProvider(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("enable-provider", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider to admit again")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if !providerPattern.MatchString(*provider) {
		return fmt.Errorf("provider %q is not a provider name", *provider)
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	if err := store.NewProviders(fsc).ClearHalt(ctx, *provider); err != nil {
		return err
	}
	logger.Info("providerEnabled", "provider", *provider)
	return nil
}

// providerPattern is what runner.Provider extracts from a model string: the
// same character class modelPattern requires of a model's provider segment.
var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
