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
		if _, err := ParseFixRoundsState(fixStateJSON(t, FixRoundsState{Round: round, Calls: round, StartedAt: started})); err != nil {
			t.Fatalf("round %d was rejected: %v", round, err)
		}
	}
	over := FixRoundsState{Round: DefaultReviewFixRounds + 1, Calls: DefaultReviewFixRounds + 1, StartedAt: started}
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
		{"a field this runner does not know", `{"round":1,"calls":1,"open":["a finding"],"resolved":2,` + started + `}`, "decoding"},
		{"a round past the cap", fmt.Sprintf(`{"round":%d,%s}`, DefaultReviewFixRounds+1, started), "round 4"},
		{"a negative round", `{"round":-1,` + started + `}`, "round -1"},
		{"a negative agent-call count", `{"round":1,"calls":-1,` + started + `}`, "agent calls"},
		{"fewer agent calls than rounds", `{"round":2,"calls":1,` + started + `}`, "1 agent calls for round 2"},
		{"more agent calls than any rounds could number", fmt.Sprintf(`{"round":3,"calls":%d,%s}`, DefaultReviewFixRounds*maxCallsPerFixRound+1, started), "agent calls for round 3"},
		{"a key spelled in another case", `{"Round":1,"calls":1,` + started + `}`, `key "Round"`},
		{"a key in capitals", `{"round":1,"calls":1,"OPEN":["a finding"],` + started + `}`, `key "OPEN"`},
		{"a newline in an open finding", `{"round":1,"calls":1,"open":["a\nb"],` + started + `}`, "control character"},
		{"a tab in a declined reason", `{"round":1,"calls":1,"declined":[{"finding":"f","reason":"a\tb"}],` + started + `}`, "control character"},
		{"a blank declined finding", `{"round":1,"calls":1,"declined":[{"finding":" ","reason":"r"}],` + started + `}`, "declined finding or its reason is blank"},
		{"a blank declined reason", `{"round":1,"calls":1,"declined":[{"finding":"f","reason":""}],` + started + `}`, "declined finding or its reason is blank"},
		{"no start time", `{"round":1,"calls":1,"open":["a finding"]}`, "start time"},
		{"a blank open finding", `{"round":1,"calls":1,"open":["  "],` + started + `}`, "blank"},
		{
			"an open finding past its line cap",
			fmt.Sprintf(`{"round":1,"calls":1,"open":[%q],%s}`, strings.Repeat("x", maxStateFindingBytes+1), started),
			"open finding is",
		},
		{
			"more open findings than a review may make",
			fmt.Sprintf(`{"round":1,"calls":1,"open":%s,%s}`, findingArray(t, maxReviewFindings+1), started),
			"open findings",
		},
		{
			"more declines than a review may make",
			fmt.Sprintf(`{"round":1,"calls":1,"declined":%s,%s}`, declineArray(t, maxReviewFindings+1), started),
			"declined findings",
		},
		{
			"a declined finding past its line cap",
			fmt.Sprintf(`{"round":1,"calls":1,"declined":[{"finding":%q,"reason":"r"}],%s}`, strings.Repeat("x", maxStateFindingBytes+1), started),
			"declined finding",
		},
		{"a record with data after it", `{"round":1,"calls":1,` + started + `}{"round":2}`, "trailing data"},
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

// TestNewFixRoundsStateWritesWhatParseAccepts: a round that gave 30 declines, 30
// open findings, a line over the cap and a line with a newline writes a record the
// next job reads, holding each list to the cap and each line to one short line.
func TestNewFixRoundsStateWritesWhatParseAccepts(t *testing.T) {
	var open []string
	var declined []DeclinedFinding
	for i := range 30 {
		open = append(open, fmt.Sprintf("finding %d leaves the retry unbounded", i))
		declined = append(declined, DeclinedFinding{Finding: fmt.Sprintf("declined %d", i), Reason: "the caller owns it"})
	}
	open[0] = strings.Repeat("x", 2*maxFindingBytes)
	open[1] = "first\nsecond"
	declined[0].Reason = ""
	started := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)

	got, err := ParseFixRoundsState(fixStateJSON(t, NewFixRoundsState(2, 3, started, "ses_1", open, declined)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Open) != maxReviewFindings || len(got.Declined) != maxReviewFindings {
		t.Fatalf("%d open and %d declined, want %d of each", len(got.Open), len(got.Declined), maxReviewFindings)
	}
	if len(got.Open[0]) != maxStateFindingBytes || got.Open[1] != "first second" {
		t.Fatalf("open lines = %q and %q, want the long one cut and the newline a space", got.Open[0], got.Open[1])
	}
	if got.Declined[0].Reason != noDeclineReason {
		t.Fatalf("declined reason = %q, want %q", got.Declined[0].Reason, noDeclineReason)
	}
	if got.Round != 2 || got.Calls != 3 || got.Session != "ses_1" || !got.StartedAt.Equal(started) {
		t.Fatalf("state = %+v, want round, calls, session and start carried", got)
	}
}
