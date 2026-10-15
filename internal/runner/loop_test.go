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

	got := reviewRows(append([]pendingFinding{guard}, first...), reworded, 2)
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
	got := reviewRows(reviewPending("the retry never bounds its attempts"), nil, 2)
	want := []ReviewFinding{{Finding: "the retry never bounds its attempts", Resolution: "fixed in round 2"}}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}
