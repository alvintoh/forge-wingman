package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/store"
)

// resetBreaker clears the breaker a systemic stop tripped, admitting dispatch
// again — a manual operator action, never automatic. Its account check only
// guards against a run on the wrong account by mistake: Firestore IAM decides
// who may clear the breaker.
func resetBreaker(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("reset-breaker", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	if e.project == "" {
		return errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, e.project)
	if err != nil {
		return fmt.Errorf("firestore client: %w", err)
	}
	defer func() { _ = fsc.Close() }()
	if err := store.NewBreaker(fsc).Reset(ctx); err != nil {
		return err
	}
	logger.Info("breakerReset")
	return nil
}
