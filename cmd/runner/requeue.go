package main

import (
	"context"
	"flag"
	"log/slog"
	"time"

	"github.com/alvintoh/forge-wingman/internal/store"
)

// requeue returns a run that has ended to the queue, so its ticket is attempted
// again — a manual operator action, never automatic. Its account check only
// guards against a run on the wrong account by mistake: Firestore IAM decides
// who may requeue.
func requeue(ctx context.Context, logger *slog.Logger, e env, args []string) error {
	fs := flag.NewFlagSet("requeue", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run to return to the queue")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := e.identity.CheckAccount(); err != nil {
		return err
	}
	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()
	res, err := store.NewQueue(fsc).Requeue(ctx, *runID, time.Now())
	if err != nil {
		return err
	}
	logger.Info("runRequeued", "run", *runID, "attempt", res.Attempt, "state", res.State)
	restoreTicketState(ctx, logger, e.project, *runID, res.TicketState)
	return nil
}
