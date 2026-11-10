package runner

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTicketDriftLinesQuotesOnlyTheLinesThatDiffer(t *testing.T) {
	built := "## Problem\n\nA widget.\n\n## Acceptance criteria\n\n1. It works.\n"
	current := "## Problem\n\nA widget.\n\n## Acceptance criteria\n\n1. It works.\n2. It is fast.\n"
	want := []string{"+ 2. It is fast."}
	if got := TicketDriftLines(built, current); !slices.Equal(got, want) {
		t.Fatalf("TicketDriftLines = %q, want %q", got, want)
	}
}

func TestTicketDriftLinesQuotesARemovedAndAChangedLine(t *testing.T) {
	want := []string{"- b", "+ B"}
	if got := TicketDriftLines("a\nb\nc", "a\nB\nc"); !slices.Equal(got, want) {
		t.Fatalf("TicketDriftLines = %q, want %q", got, want)
	}
}

func TestTicketDriftLinesNormalisesCRLF(t *testing.T) {
	if got := TicketDriftLines("a\r\nb", "a\nb"); len(got) != 0 {
		t.Fatalf("TicketDriftLines = %q, want no difference for a line-ending change", got)
	}
}

func TestTicketDriftLinesIgnoresATrailingNewline(t *testing.T) {
	if got := TicketDriftLines("a\n", "a"); len(got) != 0 {
		t.Fatalf("TicketDriftLines = %q, want no difference for a trailing newline", got)
	}
}

func TestTicketDriftLinesHandlesAnEmptyDescription(t *testing.T) {
	if got := TicketDriftLines("", "a"); !slices.Equal(got, []string{"+ a"}) {
		t.Fatalf("TicketDriftLines = %q, want the added line", got)
	}
	if got := TicketDriftLines("a", ""); !slices.Equal(got, []string{"- a"}) {
		t.Fatalf("TicketDriftLines = %q, want the removed line", got)
	}
	if got := TicketDriftLines("", ""); len(got) != 0 {
		t.Fatalf("TicketDriftLines = %q, want no difference for two empty descriptions", got)
	}
}

func TestTicketDriftLinesFallsBackPastTheTableCap(t *testing.T) {
	built := strings.Repeat("old\n", 2001)
	current := strings.Repeat("new\n", 2001)
	got := TicketDriftLines(built, current)
	if len(got) != 4002 {
		t.Fatalf("TicketDriftLines returned %d lines, want the 2001 removed and 2001 added lines", len(got))
	}
	if got[0] != "- old" || got[len(got)-1] != "+ new" {
		t.Fatalf("TicketDriftLines = first %q last %q, want the removed and added lines", got[0], got[len(got)-1])
	}
}

// A description as Linear re-saves it: an autolinked URL, an escaped markdown
// mark, trailing spaces and CRLF, with no word edited.
const (
	authoredTicket = "## Acceptance criteria\n\n1. Read https://example.com/docs and the *bold* rule.\n2. It is fast.\n"
	reSavedTicket  = "## Acceptance criteria\n\n1. Read <https://example.com/docs> and the \\*bold\\* rule.  \r\n2. It is fast.\r\n\r\n"
)

func TestTicketChangedIgnoresLinearsReRendering(t *testing.T) {
	if TicketChanged(authoredTicket, reSavedTicket) {
		t.Fatalf("TicketChanged = true, want false for a re-save that edits no word; lines %q",
			TicketDriftLines(authoredTicket, reSavedTicket))
	}
}

func TestTicketChangedSeesARealCriterionChangeThroughReRendering(t *testing.T) {
	edited := strings.Replace(reSavedTicket, "It is fast.", "It is fast and cached.", 1)
	if !TicketChanged(authoredTicket, edited) {
		t.Fatal("TicketChanged = false, want true for a changed criterion")
	}
	want := []string{"- 2. It is fast.", "+ 2. It is fast and cached."}
	if got := TicketDriftLines(authoredTicket, edited); !slices.Equal(got, want) {
		t.Fatalf("TicketDriftLines = %q, want %q: the quote shows each line as written", got, want)
	}
}

func TestCapDriftLinesKeepsAShortDifferenceWhole(t *testing.T) {
	in := []string{"- a", "+ b"}
	got, omitted := CapDriftLines(in)
	if !slices.Equal(got, in) || omitted != 0 {
		t.Fatalf("CapDriftLines = %q, %d, want the lines unchanged", got, omitted)
	}
}

func TestCapDriftLinesBoundsTheLineCount(t *testing.T) {
	in := make([]string, maxDriftQuoteLines+7)
	for i := range in {
		in[i] = "+ x"
	}
	got, omitted := CapDriftLines(in)
	if len(got) != maxDriftQuoteLines || omitted != 7 {
		t.Fatalf("CapDriftLines kept %d and omitted %d, want %d and 7", len(got), omitted, maxDriftQuoteLines)
	}
}

func TestCapDriftLinesBoundsTheBytes(t *testing.T) {
	in := make([]string, maxDriftQuoteLines)
	for i := range in {
		in[i] = "+ " + strings.Repeat("y", maxDriftLineBytes)
	}
	got, omitted := CapDriftLines(in)
	total := 0
	for _, l := range got {
		total += len(l) + 1
	}
	if total > maxDriftQuoteBytes || omitted == 0 {
		t.Fatalf("CapDriftLines quoted %d bytes and omitted %d, want at most %d bytes and some omitted", total, omitted, maxDriftQuoteBytes)
	}
}

func TestCapDriftLinesCutsALongLineOnACharacterBoundary(t *testing.T) {
	// The three-byte prefix puts every "é" on an odd offset, so the cap falls
	// inside one.
	got, _ := CapDriftLines([]string{"+ x" + strings.Repeat("é", maxDriftLineBytes)})
	if !utf8.ValidString(got[0]) || !strings.HasSuffix(got[0], "…") || len(got[0]) > maxDriftLineBytes+len("…") {
		t.Fatalf("CapDriftLines = %q (%d bytes), want a valid line cut to the cap and marked", got[0], len(got[0]))
	}
}
