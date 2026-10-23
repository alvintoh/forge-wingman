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

// backfillQueuedAt stamps queued_at on the queued and planned runs written
// before the field existed — a manual operator action, safe to repeat. Its
// account check only guards against a run on the wrong account by mistake:
// Firestore IAM decides who may write.
func backfillQueuedAt(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("backfill-queued-at", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "count the runs it would stamp and write nothing")
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
	res, err := store.NewQueue(fsc).BackfillQueuedAt(ctx, *dryRun)
	if err != nil {
		return err
	}
	logger.Info("queuedAtBackfilled", "stamped", res.Stamped, "skipped", res.Skipped, "dryRun", *dryRun)
	return nil
}
