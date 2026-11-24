package main

import (
	"context"
	"log/slog"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// prReviewRecordTimeout bounds the record's write, well inside the post job's
// own limit, so a slow database cannot hold the job's later steps.
const prReviewRecordTimeout = 15 * time.Second

// reviewRecorder stores the record of a posted review.
type reviewRecorder func(ctx context.Context, rec store.ReviewRecord) error

// storedReviewRecorder writes the record to Firestore with the job's own
// credentials, opening the client for the one write.
func storedReviewRecorder(getenv func(string) string) reviewRecorder {
	return func(ctx context.Context, rec store.ReviewRecord) error {
		return withStore(ctx, getenv, openProjectStore, func(fsc *firestore.Client) error {
			return store.NewPRReviews(fsc).Write(ctx, rec)
		})
	}
}

// recordReview stores the record of the review of head sha of pull request pr
// and logs a write that fails: the record is evidence about a review that is
// already posted, so it never changes what was posted or decided. A nil record
// stores nothing.
func recordReview(ctx context.Context, logger *slog.Logger, record reviewRecorder, pr int, sha string, v runner.PRVerdict) {
	if record == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, prReviewRecordTimeout)
	defer cancel()
	if err := record(ctx, reviewRecordOf(pr, sha, v)); err != nil {
		logger.Warn("prReviewRecordUnwritten", "pr", pr, "detail", err.Error())
	}
}

// reviewRecordOf is the record of a verdict: the general review's calls, then
// each configured specialist's in configuration order, with the total the
// comment's footer prints. It reads no finding text.
func reviewRecordOf(pr int, sha string, v runner.PRVerdict) store.ReviewRecord {
	calls := reviewCalls(store.ReviewRoleGeneral, v.Attempts, v.Usage)
	names := []string{}
	for _, s := range configuredSpecialists(v) {
		names = append(names, s.Name)
		calls = append(calls, reviewCalls(s.Name, s.Attempts, s.Usage)...)
	}
	_, total, _ := reviewTotals(v)
	return store.ReviewRecord{PR: pr, HeadSHA: sha, Risk: v.Risk().Level, Specialists: names, Calls: calls, Total: total}
}

// reviewCalls is the calls of one role: each attempt with its model, outcome and
// dollar figure. The tokens are the answering model's, so they sit on the attempt
// that was reviewed; an attempt that failed used none that were counted.
func reviewCalls(role string, attempts []runner.PRReviewAttempt, answered runner.Usage) []store.ReviewCall {
	calls := make([]store.ReviewCall, len(attempts))
	for i, a := range attempts {
		calls[i] = store.ReviewCall{Role: role, Model: a.Model, Outcome: a.Outcome, Cost: a.Cost}
		if a.Outcome == runner.AttemptReviewed {
			calls[i].Input, calls[i].Output = answered.Input, answered.Output
		}
	}
	return calls
}
