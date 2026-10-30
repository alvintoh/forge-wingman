package runner

import (
	"slices"
	"testing"
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

// TestTheStageBudgetsAndTheReserveTotalTheRunCap keeps NFR-1's cap and the three
// shares drawn from it from drifting apart: the reserve is what the two stages
// must leave, so any change to one of them has to account for the others.
func TestTheStageBudgetsAndTheReserveTotalTheRunCap(t *testing.T) {
	if got := nfr1BuildBudget + nfr1ReviewBudget + nfr1CommitReserve; got != nfr1RunCap {
		t.Fatalf("the build, review and commit shares total %s, want the run cap %s", got, nfr1RunCap)
	}
}
