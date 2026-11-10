package runner

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// TestReviewRowsDoesNotCallARewordedFindingFixed covers a re-review that words
// the same finding differently: matched by text alone, the first wording would
// read as fixed beside a duplicate still open.
func TestReviewRowsDoesNotCallARewordedFindingFixed(t *testing.T) {
	guard := pendingFinding{key: "a.go\x00if x == nil", text: "guard at a.go:4 can be removed", stable: true}
	first := reviewPending("the retry never bounds its attempts")
	reworded := reviewPending("retries are unbounded")

	got := reviewRows([]fixRoundResult{{
		round:     2,
		issued:    append([]pendingFinding{guard}, first...),
		open:      reworded,
		rechecked: true,
	}})
	want := []ReviewFinding{
		{Finding: guard.text, Resolution: "fixed in round 2"},
		{Finding: "the retry never bounds its attempts", Resolution: "addressed in round 2; the re-review still reported findings"},
		{Finding: "retries are unbounded", Resolution: "still open"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

// TestReviewRowsCallsAFindingFixedWhenTheReReviewIsClean is the other side: a
// re-review that reports nothing of its own has fixed every earlier finding.
func TestReviewRowsCallsAFindingFixedWhenTheReReviewIsClean(t *testing.T) {
	got := reviewRows([]fixRoundResult{{
		round:     2,
		issued:    reviewPending("the retry never bounds its attempts"),
		rechecked: true,
	}})
	want := []ReviewFinding{{Finding: "the retry never bounds its attempts", Resolution: "fixed in round 2"}}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

// TestReviewRowsFollowsFindingsThroughBothFixRounds is the second round's own
// report: a finding the first re-check left open and the second fixed names the
// round that closed it, and one the second re-check still reports is marked
// open after that round.
func TestReviewRowsFollowsFindingsThroughBothFixRounds(t *testing.T) {
	guard := pendingFinding{key: "a.go\x00if x == nil", text: "guard at a.go:4 can be removed", stable: true}
	stubborn := reviewPending("the timeout is hard-coded")
	issued := []pendingFinding{guard, stubborn[0]}

	got := reviewRows([]fixRoundResult{
		{round: 2, issued: issued, open: issued, rechecked: true},
		{round: 3, issued: issued, open: stubborn, rechecked: true},
	})
	want := []ReviewFinding{
		{Finding: guard.text, Resolution: "fixed in round 3"},
		{Finding: "the timeout is hard-coded", Resolution: "still open after round 3"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

// TestReviewRowsNamesTheRoundForAFindingOnlyTheLastReCheckMade covers a finding
// the last re-check reports that no fix round was given: after two rounds it
// reads open after the last one, like the findings a round did try.
func TestReviewRowsNamesTheRoundForAFindingOnlyTheLastReCheckMade(t *testing.T) {
	tried := reviewPending("the timeout is hard-coded")
	newly := reviewPending("the retry never bounds its attempts")

	got := reviewRows([]fixRoundResult{
		{round: 2, issued: tried, open: tried, rechecked: true},
		{round: 3, issued: tried, open: append(slices.Clone(tried), newly...), rechecked: true},
	})
	want := []ReviewFinding{
		{Finding: "the timeout is hard-coded", Resolution: "still open after round 3"},
		{Finding: "the retry never bounds its attempts", Resolution: "still open after round 3"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

// TestTheStageBudgetsAndTheReserveTotalTheRunCap keeps NFR-1's cap and the three
// shares drawn from it from drifting apart: the reserve is what the two stages
// must leave, so any change to one of them has to account for the others. The
// review's floor is its share — what it gets when the build stage spent its own.
func TestTheStageBudgetsAndTheReserveTotalTheRunCap(t *testing.T) {
	if got := nfr1BuildBudget + nfr1ReviewFloor + nfr1CommitReserve; got != nfr1RunCap {
		t.Fatalf("the build, review and commit shares total %s, want the run cap %s", got, nfr1RunCap)
	}
}

// TestReviewBudgetIsTheCapLessWhatTheRunSpentAndTheReserve is AC1's arithmetic:
// the review's budget is what the run cap still leaves once the run's own time
// and the commit reserve are set aside, never below its floor.
func TestReviewBudgetIsTheCapLessWhatTheRunSpentAndTheReserve(t *testing.T) {
	started := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		elapsed time.Duration
		want    time.Duration
	}{
		{"a review beginning at once", 0, 85 * time.Minute},
		{"a build that finished 30 minutes in", 30 * time.Minute, 55 * time.Minute},
		{"a build that spent its whole budget", 60 * time.Minute, 25 * time.Minute},
		{"a run already past what the cap leaves", 85 * time.Minute, 25 * time.Minute},
	} {
		if got := reviewBudget(started, started.Add(tt.elapsed)); got != tt.want {
			t.Errorf("%s: review budget = %s, want %s", tt.name, got, tt.want)
		}
	}
}

// TestTheReviewFloorLeavesRoomToStartTheFirstPass holds the invariant the first
// pass relies on: it takes the stage's whole budget rather than the start-floor
// gate, so the floor must be at least the room a call needs or a stage could
// begin unable to start anything.
func TestTheReviewFloorLeavesRoomToStartTheFirstPass(t *testing.T) {
	if nfr1ReviewFloor < reviewStartRoom {
		t.Fatalf("the review floor %s leaves less than the %s a call needs to start", nfr1ReviewFloor, reviewStartRoom)
	}
}

// TestReviewCallTimeoutStartsNothingWithUnderThreeMinutesLeft is AC2's floor:
// three minutes left starts a call bounded by those three, two does not start
// one at all.
func TestReviewCallTimeoutStartsNothingWithUnderThreeMinutesLeft(t *testing.T) {
	started := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	budget := nfr1ReviewFloor
	for _, tt := range []struct {
		name       string
		configured time.Duration
		spent      time.Duration
		want       time.Duration
		wantOK     bool
	}{
		{"nothing spent", 0, 0, budget, true},
		{"a shorter agent timeout", 10 * time.Minute, 0, 10 * time.Minute, true},
		{"exactly the start room left", 0, budget - reviewStartRoom, 3 * time.Minute, true},
		{"under the start room left", 0, budget - reviewStartRoom + time.Minute, 0, false},
		{"the whole budget spent", 0, budget, 0, false},
	} {
		got, ok := roundTimeout(tt.configured, started, started.Add(tt.spent), budget, reviewStartRoom)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("%s: roundTimeout = %s, %v; want %s, %v", tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
}

// TestReviewCallErrorNamesTheBudgetForACallTheStageClamped is AC2's labelling: a
// review call that timed out once the stage's budget was spent reports the
// budget running out, and one that timed out with budget left keeps its timeout.
func TestReviewCallErrorNamesTheBudgetForACallTheStageClamped(t *testing.T) {
	timedOut := stopWith(OutcomeAgentFailed, StopAgentTimeout, errors.New("agent exceeded 3m0s"))
	started := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	budget := nfr1ReviewFloor

	got := reviewCallError(timedOut, started.Add(budget), started, budget)
	var s *StopError
	if !errors.As(got, &s) || s.Outcome != OutcomeStopped || s.Reason != StopRunBudget {
		t.Fatalf("call at the stage's end = %v, want the review stage's spent-budget stop", got)
	}
	if want := "the review stage's 25-minute budget was spent"; s.Err.Error() != want {
		t.Fatalf("detail = %q, want %q", s.Err.Error(), want)
	}
	if got := reviewCallError(timedOut, started.Add(budget-time.Second), started, budget); got != timedOut {
		t.Fatalf("call with budget left = %v, want it unchanged", got)
	}
	other := errors.New("exit status 1")
	if got := reviewCallError(other, started.Add(budget), started, budget); got != other {
		t.Fatalf("non-timeout error = %v, want it unchanged", got)
	}
}

func TestRunLimitMinutesByStageWord(t *testing.T) {
	for _, tt := range []struct {
		stage string
		want  int
	}{
		{RunStagePlanning, 60},
		{RunStageBuilding, 90},
		{"", 90},
		{"settled", 90},
	} {
		if got := RunLimitMinutes(tt.stage); got != tt.want {
			t.Errorf("RunLimitMinutes(%q) = %d, want %d", tt.stage, got, tt.want)
		}
	}
}

// TestRunLimitMinutesAreWholeMinutes keeps the minutes the surface shows exact:
// a budget with a stray second would be floored to a limit the runner never stops at.
func TestRunLimitMinutesAreWholeMinutes(t *testing.T) {
	for name, limit := range map[string]time.Duration{"nfr1RunCap": nfr1RunCap, "nfr1BuildBudget": nfr1BuildBudget} {
		if limit%time.Minute != 0 {
			t.Errorf("%s = %s, want a whole number of minutes", name, limit)
		}
	}
}
