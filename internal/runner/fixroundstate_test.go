package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixStateJSON is a state record encoded the way the round that writes it does.
func fixStateJSON(t *testing.T, s FixRoundsState) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// findingArray is n distinct finding lines, as a record encodes them.
func findingArray(t *testing.T, n int) string {
	t.Helper()
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("finding %d leaves the retry unbounded", i)
	}
	b, err := json.Marshal(lines)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// declineArray is n distinct declines, as a record encodes them.
func declineArray(t *testing.T, n int) string {
	t.Helper()
	declined := make([]DeclinedFinding, n)
	for i := range declined {
		declined[i] = DeclinedFinding{Finding: fmt.Sprintf("finding %d leaves the retry unbounded", i), Reason: "the caller owns it"}
	}
	b, err := json.Marshal(declined)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestParseFixRoundsStateRoundTripsWhatARoundWrote is AC5's read half: every
// field the round wrote comes back, and the next round is given the open
// findings minus the declined ones.
func TestParseFixRoundsStateRoundTripsWhatARoundWrote(t *testing.T) {
	started := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)
	want := FixRoundsState{
		Round:     2,
		Open:      []string{findingRetry, findingTimeout, findingSwallow},
		Declined:  []DeclinedFinding{{Finding: findingSwallow, Reason: "reported upstream"}},
		StartedAt: started,
		Session:   "ses_1",
		Calls:     2,
	}

	got, err := ParseFixRoundsState(fixStateJSON(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Open, want.Open) || !slices.Equal(got.Declined, want.Declined) {
		t.Fatalf("state = %+v, want the findings and declines it was written with", got)
	}
	if got.Round != want.Round || got.Calls != want.Calls || got.Session != want.Session || !got.StartedAt.Equal(started) {
		t.Fatalf("state = %+v, want round, calls, session and start time carried", got)
	}
	if issued := got.Issued(); !slices.Equal(issued, []string{findingRetry, findingTimeout}) {
		t.Fatalf("issued = %q, want the open findings minus the declined one", issued)
	}
}

// TestParseFixRoundsStateAcceptsTheCapsRoundAndRejectsTheNext is AC6: the cap of
// rounds is a state the loop can write, and one past it is not.
func TestParseFixRoundsStateAcceptsTheCapsRoundAndRejectsTheNext(t *testing.T) {
	started := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)
	for round := 0; round <= DefaultReviewFixRounds; round++ {
		if _, err := ParseFixRoundsState(fixStateJSON(t, FixRoundsState{Round: round, StartedAt: started})); err != nil {
			t.Fatalf("round %d was rejected: %v", round, err)
		}
	}
	over := FixRoundsState{Round: DefaultReviewFixRounds + 1, StartedAt: started}
	if _, err := ParseFixRoundsState(fixStateJSON(t, over)); !errors.Is(err, ErrFixRoundsState) {
		t.Fatalf("err = %v, want round %d rejected", err, over.Round)
	}
}

// TestParseFixRoundsStateRejectsRecordsTheLoopCouldNotWrite is AC5's other half:
// a record over a cap, or one holding a field this runner does not know, is
// rejected rather than trimmed.
func TestParseFixRoundsStateRejectsRecordsTheLoopCouldNotWrite(t *testing.T) {
	const started = `"started_at":"2026-11-13T04:00:00Z"`
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"no record at all", "", "no record"},
		{"a field this runner does not know", `{"round":1,"open":["a finding"],"resolved":2,` + started + `}`, "decoding"},
		{"a round past the cap", fmt.Sprintf(`{"round":%d,%s}`, DefaultReviewFixRounds+1, started), "round 4"},
		{"a negative round", `{"round":-1,` + started + `}`, "round -1"},
		{"a negative agent-call count", `{"round":1,"calls":-1,` + started + `}`, "agent calls"},
		{"no start time", `{"round":1,"open":["a finding"]}`, "start time"},
		{"a blank open finding", `{"round":1,"open":["  "],` + started + `}`, "blank"},
		{
			"an open finding past its line cap",
			fmt.Sprintf(`{"round":1,"open":[%q],%s}`, strings.Repeat("x", maxStateFindingBytes+1), started),
			"open finding is",
		},
		{
			"more open findings than a review may make",
			fmt.Sprintf(`{"round":1,"open":%s,%s}`, findingArray(t, maxReviewFindings+1), started),
			"open findings",
		},
		{
			"more declines than a review may make",
			fmt.Sprintf(`{"round":1,"declined":%s,%s}`, declineArray(t, maxReviewFindings+1), started),
			"declined findings",
		},
		{
			"a declined finding past its line cap",
			fmt.Sprintf(`{"round":1,"declined":[{"finding":%q,"reason":"r"}],%s}`, strings.Repeat("x", maxStateFindingBytes+1), started),
			"declined finding",
		},
		{"a record with data after it", `{"round":1,` + started + `}{"round":2}`, "trailing data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFixRoundsState(tt.raw)
			if !errors.Is(err, ErrFixRoundsState) {
				t.Fatalf("err = %v, want the record rejected", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to name %q", err, tt.want)
			}
		})
	}
}
