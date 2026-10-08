package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
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

// ErrReviewOutput reports a review agent's final message that carries no readable review-findings block.
var ErrReviewOutput = errors.New("findings unreadable")

// ReviewResult is what one model's review produced and cost.
type ReviewResult struct {
	Model string
	// Priced is whether the plan has a rate card for Model, so Cost is the runner's own figure.
	Priced bool
	Usage
	// Findings is the review-findings block's content, empty for a clean review.
	Findings string
	// Text is the start of the agent's final message.
	Text string
}

// Review runs prompt on model in dir and meters the run against the plan's rates.
//
// A run that fails, or outlives ctx, is an error; its message carries the end of
// the agent's stderr with every secret redacted. A final message with no
// review-findings block is ErrReviewOutput, returned with the metered result.
func Review(ctx context.Context, agent Agent, model, dir, prompt string, secrets []string) (ReviewResult, error) {
	var out, errOut bytes.Buffer
	if err := bindModel(agent, model).Run(ctx, dir, "", prompt, "", &out, &errOut); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ReviewResult{}, fmt.Errorf("review agent on %s: %w", model, ctxErr)
		}
		errOut.WriteString("\n" + err.Error())
		return ReviewResult{}, fmt.Errorf("review agent on %s: %s", model, redactedTail(errOut.Bytes(), smokeTextLimit, secrets))
	}
	usage, err := meterUsage(bytes.NewReader(out.Bytes()), model, providers.RatesFor)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("metering %s: %w", model, err)
	}
	_, priced := providers.RatesFor(model)
	text, _ := FinalText(bytes.NewReader(out.Bytes()))
	res := ReviewResult{Model: model, Priced: priced, Usage: usage, Text: truncate(strings.TrimSpace(text), smokeTextLimit)}
	findings, err := ParseReviewFindings(text)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrReviewOutput, err)
	}
	res.Findings = findings
	return res, nil
}
