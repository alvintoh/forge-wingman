package runner

import (
	"errors"
	"regexp"
	"strings"
)

// reviewFindingsInstruction tells the review agent the exact format its
// final message must end with, since nothing external defines a review
// artifact format.
const reviewFindingsInstruction = "Review the diff above against the ticket's acceptance criteria. Do not edit any " +
	"files or run any shell commands: this phase is review only.\n\n" +
	"End your final response with a fenced code block named review-findings: leave it empty if the diff " +
	"satisfies the ticket, or list every finding worth fixing, one per line, if it does not:\n\n" +
	"```review-findings\n```"

// ReviewPrompt is the review pass's prompt: the branch's diff, the ticket,
// then the review-findings format instruction.
func ReviewPrompt(diff string, t Ticket) string {
	return "## Diff under review\n\n```diff\n" + diff + "\n```\n\n## Ticket\n\n" + t.Text() + "\n\n" + reviewFindingsInstruction
}

// errReviewFindings reports a review agent's final message that carries no
// valid review-findings block.
var errReviewFindings = errors.New("review agent's response carries no valid review-findings block")

var reviewFindingsBlock = regexp.MustCompile("(?s)```review-findings\\s*\\n(.*?)```")

// ParseReviewFindings reads the review agent's final message for the last
// review-findings block and returns its content, trimmed — empty when the
// review found nothing worth fixing.
func ParseReviewFindings(text string) (string, error) {
	matches := reviewFindingsBlock.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", errReviewFindings
	}
	return strings.TrimSpace(matches[len(matches)-1][1]), nil
}
