package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

const maxRebaseConflictRetries = 1

func record(ctx context.Context, logger *slog.Logger, getenv func(string) string, e env, args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run record to finalize")
	attemptID := fs.String("attempt-id", e.attemptID, "workflow attempt the build ran in")
	summary := fs.String("summary", "", "the build's summary, as JSON")
	unreadable := fs.Bool("summary-unreadable", false, "the summary could not be passed in")
	prURL := fs.String("pr-url", "", "URL of the opened PR")
	runResult := fs.String("run-result", "", "result of the run job")
	prResult := fs.String("pr-result", "", "result of the pr job")
	prStopReason := fs.String("pr-stop-reason", "", "the pr job's stop_reason output, empty when it pushed")
	prSeconds := fs.Int("pr-duration-s", 0, "wall-clock seconds the pr job took")
	checkReport := fs.String("failed-gate", "", "the check job's failed_gate output, empty when it did not run")
	runURL := fs.String("run-url", "", "URL of the workflow run")
	autoMerge := fs.Bool("auto-merge", false, "the pr job marked the PR eligible for auto-merge")
	claimAttempt := fs.String("attempt", "", "attempt the dispatcher claimed the run at, empty for a run dispatched without one")
	dispatcherURI := fs.String("dispatcher-uri", "", "the dispatcher job's run endpoint, woken once the record is written; empty skips the wake")
	if err := fs.Parse(args); err != nil {
		return err
	}
	attempt, err := parseClaimAttempt(*claimAttempt)
	if err != nil {
		return err
	}
	// started tells run.yml's fallback that the summary arrived, so a later failure
	// is not mistaken for one too large to pass in.
	if err := writeOutputs(e.output, map[string]string{"started": "true"}); err != nil {
		return err
	}
	*attemptID = buildAttempt(logger, *attemptID, e)

	fsc, err := recordsClient(ctx, e.project, *runID)
	if err != nil {
		return err
	}
	defer func() { _ = fsc.Close() }()

	records := store.NewRecords(fsc)
	queue := store.NewQueue(fsc)
	rec, err := finalize(ctx, logger, records, queue, records, store.NewNotices(fsc), store.NewBreaker(fsc), runner.FinalizeInput{
		RunID:             *runID,
		Identity:          e.identity,
		AttemptID:         *attemptID,
		Summary:           *summary,
		SummaryUnreadable: *unreadable,
		PRURL:             *prURL,
		RunResult:         *runResult,
		PRResult:          *prResult,
		PRStopReason:      *prStopReason,
		PRDuration:        time.Duration(*prSeconds) * time.Second,
		CheckReport:       *checkReport,
		FailedJobLog:      failedJobLogForRun(ctx, logger, getenv),
		RunURL:            *runURL,
		AutoMerge:         *autoMerge,
		Attempt:           attempt,
	}, time.Now())
	if err != nil {
		return err
	}
	defer wakeAfterRecord(ctx, logger, googleClient, *dispatcherURI, *runID)
	if rec.StopReason == runner.StopSummaryInvalid {
		logger.Warn("summaryRejected", "run", *runID, "err", rec.StopDetail)
	}
	logger.Info("recordFinalized", "run", *runID, "attempt", *attemptID, "outcome", string(rec.Outcome),
		"reason", string(rec.StopReason), "failedGate", rec.FailedGate,
		"timeOfUse", rec.TimeOfUse, "costDrift", rec.CostDrift)
	if rec.CostDriftUnread {
		logger.Warn("costDriftUnread", "run", *runID, "provider", rec.Plan)
	}
	if rec.Outcome == runner.OutcomeInfraFailure && rec.StopReason == runner.StopModelUnavailable && len(rec.Steps) > 0 {
		provider := runner.Provider(rec.Steps[len(rec.Steps)-1].Model)
		if perr := store.NewProviders(fsc).RecordInfraStop(ctx, provider, time.Now()); perr != nil {
			logger.Error("providerInfraStopNotRecorded", "run", *runID, "provider", provider, "err", perr.Error())
		}
	}
	if rec.Succeeded() && len(rec.Steps) > 0 {
		provider := runner.Provider(rec.Steps[len(rec.Steps)-1].Model)
		if perr := store.NewProviders(fsc).RecordSuccess(ctx, provider); perr != nil {
			logger.Error("providerSuccessNotRecorded", "run", *runID, "provider", provider, "err", perr.Error())
		}
	}
	if rec.StopReason == runner.StopRebaseConflict {
		res, err := queue.RequeueAfterConflict(ctx, *runID, time.Now(), maxRebaseConflictRetries)
		switch {
		case err == nil:
			logger.Info("rebaseConflictRequeued", "run", *runID, "attempt", res.Attempt)
			restoreTicketState(ctx, logger, e.project, *runID, res.TicketState)
			return nil
		case errors.Is(err, store.ErrRetriesExhausted):
			logger.Warn("rebaseConflictRetriesExhausted", "run", *runID, "retries", maxRebaseConflictRetries)
		case errors.Is(err, store.ErrRunAbandoned):
			logger.Warn("rebaseConflictOnAbandonedRun", "run", *runID)
		default:
			return err
		}
	}
	if !rec.Succeeded() {
		return errRunFailed
	}
	return nil
}

// noticeRaiser and breakerTripper are what record writes for a systemic stop.
type noticeRaiser interface {
	Raise(ctx context.Context, n dispatcher.Notice, at time.Time) error
}

type breakerTripper interface {
	Trip(ctx context.Context, runID, class string, at time.Time) error
}

// finalize writes the run's outcome into its record and raises its systemic
// notice, even when the record or ledger write then fails, since the stop is
// known by then.
func finalize(ctx context.Context, logger *slog.Logger, records runner.RecordStore, ledger runner.Ledger,
	reader runner.ProviderCostReader, notices noticeRaiser, breaker breakerTripper, in runner.FinalizeInput, at time.Time) (runner.Record, error) {
	rec, err := runner.Finalize(ctx, records, ledger, reader, in, at)
	raiseSystemic(ctx, logger, notices, breaker, rec, in.AttemptID, at)
	return rec, err
}

// attemptIDPattern is what a workflow attempt id looks like: run, then attempt.
var attemptIDPattern = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,10}$`)

// raiseSystemic raises the notice for a systemic stop, linked to the run that
// recorded it, and trips the breaker when its class does. The notice is keyed
// by run and attempt, so a later attempt after a reset raises its own; an
// attempt id that cannot name a document falls back to the run alone. Both
// writes create only what is absent, so a record job run twice raises and
// trips once.
func raiseSystemic(ctx context.Context, logger *slog.Logger, notices noticeRaiser, breaker breakerTripper, rec runner.Record, attemptID string, at time.Time) {
	notify, trips := rec.Systemic()
	if !notify {
		return
	}
	class := string(rec.StopReason)
	id := rec.RunID + "-" + attemptID
	if !attemptIDPattern.MatchString(attemptID) {
		logger.Error("noticeAttemptInvalid", "run", rec.RunID, "length", len(attemptID))
		id = rec.RunID
	}
	if err := notices.Raise(ctx, dispatcher.Notice{ID: id, Class: class, Link: rec.RunURL}, at); err != nil {
		logger.Error("noticeNotRaised", "run", rec.RunID, "class", class, "err", err.Error())
	}
	if !trips {
		return
	}
	if err := breaker.Trip(ctx, rec.RunID, class, at); err != nil {
		logger.Error("breakerNotTripped", "run", rec.RunID, "class", class, "err", err.Error())
		return
	}
	logger.Warn("breakerTripped", "run", rec.RunID, "class", class)
}

// parseClaimAttempt reads the attempt a run was claimed at, which the workflow
// passes through empty for a run dispatched before it carried one: nil.
func parseClaimAttempt(s string) (*int, error) {
	if s == "" {
		return nil, nil
	}
	n, err := strconv.ParseUint(s, 10, 31)
	if err != nil {
		return nil, fmt.Errorf("-attempt %q is not a claim attempt: %w", s, err)
	}
	attempt := int(n)
	return &attempt, nil
}
