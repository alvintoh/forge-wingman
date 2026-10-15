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

// TestPreOpenReviewPromptAsksForWhatAWalkthroughChecks is what the pre-open
// review pass is held to: the checks the runner cannot make mechanically, which
// a walkthrough makes by reading around the change.
func TestPreOpenReviewPromptAsksForWhatAWalkthroughChecks(t *testing.T) {
	got := PreOpenReviewPrompt("diff --git a/x.go\n+x", testTicket)
	if !strings.Contains(got, "```diff\ndiff --git a/x.go\n+x\n```") || !strings.Contains(got, testTicket.Text()) {
		t.Fatalf("prompt = %q, want the diff and the ticket", got)
	}
	for _, want := range []string{
		"caller", "early return", "rule in force", "fail-closed path", "```review-findings\n```",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt = %q, want it to ask for %q", got, want)
		}
	}
	if strings.Contains(got, "run any shell commands") {
		t.Fatalf("prompt = %q, want the review able to read and search the repository", got)
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
