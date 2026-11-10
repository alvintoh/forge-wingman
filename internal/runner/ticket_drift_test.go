package runner

import (
	"slices"
	"strings"
	"testing"
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
