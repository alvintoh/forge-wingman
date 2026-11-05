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
	// runSuccessMinRuns is the fewest runs a window states a rate over; the web's RUN_SUCCESS_MIN_RUNS repeats it.
	runSuccessMinRuns = 20

	// runSuccessReadLimit is the most rows the read scans for its two windows: runs in
	// flight, cancelled or awaiting their checks sit among the counted ones and are
	// skipped, so it is set well above the 60 counted runs two windows of 30 hold.
	runSuccessReadLimit = 200

	// failureChecksFailing is the failure class of a run whose pull request's checks failed.
	failureChecksFailing = "checks-failing"

	// failureAbandoned is the failure class of a run swept as never reporting back.
	failureAbandoned = stateAbandoned
)

// RunSuccess is the run success rate: the share of runs that opened a pull
// request whose required checks passed, over the newest counted runs and the
// same number before them (adr/0022). Every figure is read from the runs' own
// rows, as adr/0016 reads the autonomy figures.
type RunSuccess struct {
	// Recent is the newest window of counted runs, Previous the window before them.
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

// runResult is one finished run's line in the rate: how it ended, what its pull
// request's checks settled at, and its stored state.
type runResult struct {
	Outcome      runner.Outcome
	ChecksResult string
	StopReason   runner.StopReason
	State        string
	Cancelled    bool
}

// counted reports whether the run counts in a window. A run the owner cancelled
// counts in neither; a run that opened a pull request counts once its checks
// result is green or failing, and one still pending, or that can never be read,
// counts in neither until it settles.
func (row runResult) counted() bool {
	if row.Cancelled {
		return false
	}
	if row.Outcome != runner.OutcomePROpened {
		return true
	}
	return row.ChecksResult == dispatcher.CheckGreen || row.ChecksResult == dispatcher.CheckFailing
}

// RunSuccess returns the run success rate over the newest size counted runs and
// the size before them, so a stretch without runs leaves it where it was. It is
// one rows-only read of the runs newest start first, projected to the fields the
// count reads and stopped once both windows are full, and no GitHub call: a
// run's checks result is already on its row (adr/0022).
func (r *Records) RunSuccess(ctx context.Context, size int) (RunSuccess, error) {
	iter := r.client.Collection(runsCollection).
		OrderBy(startedAtField, firestore.Desc).
		Limit(runSuccessReadLimit).
		// cancelled_by_owner_at is read beside the counted fields because a cancel
		// writes nothing else: without it a run the owner cancelled cannot be told
		// from one that failed.
		Select(checksResultField, outcomeField, startedAtField, stopReasonField, cancelledByOwnerAtField, stateField).
		Documents(ctx)
	defer iter.Stop()
	var rows []runResult
	var read, counted int
	var oldest time.Time
	for counted < 2*size {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return RunSuccess{}, fmt.Errorf("reading the newest runs: %w", err)
		}
		row := snap.Data()
		read++
		oldest, _ = row[startedAtField].(time.Time)
		if !hasEnded(row) && str(row, stateField) != stateAbandoned {
			continue
		}
		result := runResult{
			Outcome:      runner.Outcome(str(row, outcomeField)),
			ChecksResult: str(row, checksResultField),
			StopReason:   runner.StopReason(str(row, stopReasonField)),
			State:        str(row, stateField),
			Cancelled:    cancelledRow(row),
		}
		if result.counted() {
			counted++
		}
		rows = append(rows, result)
	}
	if read == runSuccessReadLimit && counted < 2*size {
		r.logger.InfoContext(ctx, "runSuccessCapped", "limit", runSuccessReadLimit, "counted", counted, "oldest", oldest)
	}
	return countRunSuccess(rows, size), nil
}

// countRunSuccess counts the two windows over the rows of runs that finished or
// were abandoned, newest first: the first size counted runs are Recent, the next
// size Previous, and the rest neither. An abandoned run counts as one that did
// not pass.
func countRunSuccess(rows []runResult, size int) RunSuccess {
	var out RunSuccess
	failures := map[string]int{}
	n := 0
	for _, row := range rows {
		if !row.counted() {
			continue
		}
		if n == 2*size {
			break
		}
		recent := n < size
		n++
		window := &out.Previous
		if recent {
			window = &out.Recent
		}
		window.Runs++
		if row.Outcome == runner.OutcomePROpened && row.ChecksResult == dispatcher.CheckGreen {
			window.Passed++
		} else if recent {
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
