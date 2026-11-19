package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/alvintoh/forge-wingman/internal/webhook"
)

var wakeTimeout = 30 * time.Second

// googleClient is the runner's own Google credentials, as the plan stage uses them to wake the dispatcher.
func googleClient(ctx context.Context) (*http.Client, error) {
	return google.DefaultClient(ctx, cloudPlatform)
}

// wakeAfterRecord starts a dispatcher execution so a finished run's slot is
// refilled without waiting for the schedule. It never returns an error: a
// failed wake is logged, and the next scheduled poll picks the slot up anyway.
// An empty uri sends nothing and builds no credentials.
func wakeAfterRecord(ctx context.Context, logger *slog.Logger, newClient func(context.Context) (*http.Client, error), requests webhook.WakeRequester, uri, runID string) {
	if uri == "" {
		logger.Info("dispatcherWakeSkipped", "run", runID)
		return
	}
	wctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	client, err := newClient(wctx)
	if err != nil {
		logger.Warn("dispatcherWakeFailed", "run", runID, "err", "google credentials: "+err.Error())
		return
	}
	if err := (webhook.Job{RunURI: uri, Client: client, Requests: requests}).Wake(wctx); err != nil {
		if !errors.Is(err, webhook.ErrWakeNotRecorded) {
			logger.Warn("dispatcherWakeFailed", "run", runID, "err", err.Error())
			return
		}
		logger.Warn("dispatcherWakeNotRecorded", "run", runID, "err", err.Error())
	}
	logger.Info("dispatcherWoken", "run", runID)
}

// waker starts one dispatcher execution.
type waker interface {
	Wake(ctx context.Context) error
}

// wakeAfterPlan wakes the dispatcher for a plan just settled. A wake whose
// execution started but whose record failed is logged and not returned: the
// plan is already settled, and the next tick polls anyway.
func wakeAfterPlan(ctx context.Context, logger *slog.Logger, job waker, runID string) error {
	err := job.Wake(ctx)
	if errors.Is(err, webhook.ErrWakeNotRecorded) {
		logger.Warn("dispatcherWakeNotRecorded", "run", runID, "err", err.Error())
		return nil
	}
	return err
}
