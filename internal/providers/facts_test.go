package providers

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestNewPageFactsCapsTheTextOnACharacterBoundary asserts a page longer than the
// cap is stored truncated and never split inside a character, since the store
// refuses a string that is not valid UTF-8.
func TestNewPageFactsCapsTheTextOnACharacterBoundary(t *testing.T) {
	text := strings.Repeat("日", MaxPageBytes)
	got := NewPageFacts("https://vendor.example/pricing", "#plans", text)
	if len(got.Text) >= len(text) {
		t.Fatalf("stored %d of %d bytes, want the text capped", len(got.Text), len(text))
	}
	if !strings.HasPrefix(text, got.Text) {
		t.Fatal("the stored text is not the page's own beginning")
	}
	if !utf8.ValidString(got.Text) {
		t.Fatalf("stored text ending %q, which is not valid UTF-8", got.Text[len(got.Text)-3:])
	}
	if got.URL != "https://vendor.example/pricing" || got.Selector != "#plans" {
		t.Fatalf("stored %+v, want the page and region it was read from", got)
	}
}

func TestParseProvenanceReadsTheLeaderboardsOwnWord(t *testing.T) {
	for _, tt := range []struct {
		word string
		want Provenance
	}{
		{"independent", ProvenanceIndependently},
		{"Independently-run", ProvenanceIndependently},
		{"vendor-reported", ProvenanceVendorReported},
		{"vendor", ProvenanceVendorReported},
		{"", ProvenanceUnknown},
		{"written by a human", ProvenanceUnknown},
	} {
		if got := ParseProvenance(tt.word); got != tt.want {
			t.Errorf("ParseProvenance(%q) = %q, want %q", tt.word, got, tt.want)
		}
	}
}
