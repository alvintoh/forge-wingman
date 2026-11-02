package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// runSuccessWindowSpan is the span each of the two windows the rate compares
	// reaches: four weeks.
	runSuccessWindowSpan = 28 * 24 * time.Hour

	// runSuccessMinRuns is the fewest runs a window states a rate over.
	runSuccessMinRuns = 20

	// runSuccessLimit is the most runs the read returns.
	runSuccessLimit = 200

	// failureChecksFailing is the failure class of a run whose pull request's checks failed.
	failureChecksFailing = "checks-failing"

	// failureAbandoned is the failure class of a run swept as never reporting back.
	failureAbandoned = stateAbandoned
)

// RunSuccess is the run success rate: the share of runs that opened a pull
// request whose required checks passed, over the trailing four weeks and the
// four weeks before (adr/0022). Every figure is read from the runs' own rows, as
// adr/0016 reads the autonomy figures.
type RunSuccess struct {
	// Recent is the four weeks to now, Previous the four weeks before those.
	Recent   RunWindow
	Previous RunWindow
	// Failure is the most common way a run failed in Recent, nil when none did.
	Failure *FailureClass
}

// RunWindow is one window's census: the runs that passed and the runs counted.
type RunWindow struct {
	Passed int
	Runs   int
	// Rate is Passed/Runs, nil while Runs is under runSuccessMinRuns.
	Rate *float64
}

// FailureClass is one way runs failed, with how many failed that way.
type FailureClass struct {
	Class string
	Count int
}

// runResult is one run's line in the rate: when it started, how it ended, what
// its pull request's checks settled at, and its stored state.
type runResult struct {
	StartedAt    time.Time
	Outcome      runner.Outcome
	ChecksResult string
	StopReason   runner.StopReason
	State        string
	Cancelled    bool
}

// RunSuccess returns the run success rate over the two windows ending at now. It
// is one rows-only query of the runs started since the earlier window opened,
// newest start first and capped, projected to the fields the count reads, and no
// GitHub call: a run's checks result is already on its row (adr/0022).
func (r *Records) RunSuccess(ctx context.Context, now time.Time) (RunSuccess, error) {
	since := now.Add(-2 * runSuccessWindowSpan)
	iter := r.client.Collection(runsCollection).
		Where(startedAtField, ">=", since).
		OrderBy(startedAtField, firestore.Desc).
		Limit(runSuccessLimit).
		// cancelled_by_owner_at is read beside the counted fields because a cancel
		// writes nothing else: without it a run the owner cancelled cannot be told
		// from one that failed.
		Select(checksResultField, outcomeField, startedAtField, stopReasonField, cancelledByOwnerAtField, stateField).
		Documents(ctx)
	defer iter.Stop()
	var rows []runResult
	var read int
	var oldest time.Time
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return RunSuccess{}, fmt.Errorf("reading the runs started since %s: %w",
				since.UTC().Format(time.RFC3339), err)
		}
		row := snap.Data()
		started, _ := row[startedAtField].(time.Time)
		read++
		oldest = started
		if !hasEnded(row) && str(row, stateField) != stateAbandoned {
			continue
		}
		result := runResult{
			Outcome:      runner.Outcome(str(row, outcomeField)),
			ChecksResult: str(row, checksResultField),
			StopReason:   runner.StopReason(str(row, stopReasonField)),
			State:        str(row, stateField),
			StartedAt:    started,
			Cancelled:    cancelledRow(row),
		}
		rows = append(rows, result)
	}
	if read == runSuccessLimit {
		r.logger.InfoContext(ctx, "runSuccessCapped", "limit", runSuccessLimit, "oldest", oldest)
	}
	return countRunSuccess(rows, now), nil
}

// countRunSuccess counts the two windows over the rows of runs that finished or
// were abandoned, an abandoned run counting as one that did not pass. A
// run the owner cancelled counts in neither; a run that opened a pull request
// counts on its checks result, and one whose result is still pending, or can
// never be read, counts in neither the pass nor the denominator.
func countRunSuccess(rows []runResult, now time.Time) RunSuccess {
	recentFrom := now.Add(-runSuccessWindowSpan)
	previousFrom := now.Add(-2 * runSuccessWindowSpan)
	var out RunSuccess
	failures := map[string]int{}
	for _, row := range rows {
		if row.StartedAt.Before(previousFrom) || row.StartedAt.After(now) {
			continue
		}
		recent := !row.StartedAt.Before(recentFrom)
		window := &out.Previous
		if recent {
			window = &out.Recent
		}
		passed := false
		switch {
		case row.Cancelled:
			continue
		case row.Outcome == runner.OutcomePROpened:
			switch row.ChecksResult {
			case dispatcher.CheckGreen:
				passed = true
				window.Passed++
			case dispatcher.CheckFailing:
			default:
				// Still pending, or a pull request no result can be read for:
				// counted in neither until it settles.
				continue
			}
		}
		window.Runs++
		if recent && !passed {
			failures[failureClass(row)]++
		}
	}
	out.Recent.Rate = rateOf(out.Recent)
	out.Previous.Rate = rateOf(out.Previous)
	out.Failure = mostCommon(failures)
	return out
}

// failureClass names how row failed: its pull request's checks failing, else the
// reason that stopped it, else its abandonment, else the outcome it ended with.
func failureClass(row runResult) string {
	switch {
	case row.Outcome == runner.OutcomePROpened:
		return failureChecksFailing
	case row.StopReason != "":
		return string(row.StopReason)
	case row.State == stateAbandoned:
		return failureAbandoned
	default:
		return string(row.Outcome)
	}
}

// rateOf is a window's pass rate, nil while the window holds fewer runs than a
// rate is stated over.
func rateOf(w RunWindow) *float64 {
	if w.Runs < runSuccessMinRuns {
		return nil
	}
	rate := float64(w.Passed) / float64(w.Runs)
	return &rate
}

// mostCommon is the class with the most failures, nil when none failed. A tie
// goes to the class that sorts first, so the answer does not move between two
// reads of the same rows.
func mostCommon(failures map[string]int) *FailureClass {
	var top *FailureClass
	for class, n := range failures {
		if top == nil {
			top = &FailureClass{Class: class, Count: n}
			continue
		}
		if n > top.Count || (n == top.Count && class < top.Class) {
			top = &FailureClass{Class: class, Count: n}
		}
	}
	return top
}
