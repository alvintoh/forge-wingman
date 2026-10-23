package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

// runRecord reads run runID's record, failing the run when the record is
// missing or its ticket cannot be built.
func runRecord(ctx context.Context, logger *slog.Logger, r runner.RecordReader, runID string) (runner.Record, error) {
	rec, err := runner.ReadRun(ctx, r, runID)
	var stopped *runner.StopError
	if errors.As(err, &stopped) {
		logger.Error("ticketUnavailable", "run", runID, "reason", string(stopped.Reason),
			"err", stopped.Err.Error())
		return runner.Record{}, fmt.Errorf("%w: %s", errRunFailed, stopped.Reason)
	}
	if err != nil {
		return runner.Record{}, err
	}
	return rec, nil
}

// recordsClient opens Firestore for a subcommand that reads or writes record runID.
func recordsClient(ctx context.Context, project, runID string) (*firestore.Client, error) {
	if !runner.ValidRunID(runID) {
		return nil, fmt.Errorf("run id %q cannot name a record", runID)
	}
	if project == "" {
		return nil, errors.New("GOOGLE_CLOUD_PROJECT is not set")
	}
	fsc, err := firestore.NewClient(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("firestore client: %w", err)
	}
	return fsc, nil
}
