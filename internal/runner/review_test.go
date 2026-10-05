package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestReviewPromptCarriesTheDiffAndTheTicket(t *testing.T) {
	got := ReviewPrompt("diff --git a/x.go\n+x", testTicket)
	if !strings.Contains(got, "```diff\ndiff --git a/x.go\n+x\n```") || !strings.Contains(got, testTicket.Text()) ||
		!strings.HasSuffix(got, "```review-findings\n```") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestParseReviewFindings(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"clean", "```review-findings\n```", ""},
		{"one finding", "```review-findings\nthe retry never bounds its attempts\n```", "the retry never bounds its attempts"},
		{"only the last block counts", "```review-findings\nstale\n```\n\nmore reasoning\n\n```review-findings\n```", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReviewFindings(tt.text)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("findings = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseReviewFindingsRejectsAResponseWithNoBlock(t *testing.T) {
	if _, err := ParseReviewFindings("looks fine to me"); !errors.Is(err, errReviewFindings) {
		t.Fatalf("err = %v, want errReviewFindings", err)
	}
}
