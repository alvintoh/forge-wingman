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

// reviewFindingsBlockInstruction ends every review prompt: the exact format the
// agent's final message must end with, since nothing external defines a review
// artifact format.
const reviewFindingsBlockInstruction = "End your final response with a fenced code block named review-findings: " +
	"leave it empty if the diff satisfies the ticket, or list every finding worth fixing, one per line, if it does not:\n\n" +
	"```review-findings\n```"

// reviewFindingsInstruction tells the review agent the exact format its
// final message must end with, since nothing external defines a review
// artifact format.
const reviewFindingsInstruction = "Review the diff above against the ticket's acceptance criteria. Do not edit any " +
	"files or run any shell commands: this phase is review only.\n\n" + reviewFindingsBlockInstruction

// preOpenReviewInstruction is what the pre-open review pass asks for: the
// checks an interactive walkthrough makes by reading around the change, which
// the runner cannot make mechanically. It replaces reviewFindingsInstruction's
// shell refusal — searching the repository is how these are answered — while
// still forbidding edits, since a reviewer that changes what it reviews is
// reviewing its own work.
const preOpenReviewInstruction = "Review the diff above against the ticket's acceptance criteria, and the code " +
	"around it. Do not edit any files; read and search the repository as much as you need.\n\n" +
	"Besides the diff itself, report:\n\n" +
	"- anything outside the diff the change leaves wrong: a caller that still assumes the old behaviour, a test " +
	"name or doc that now describes something else, an assertion pinned to a value the change moved.\n" +
	"- code the diff adds after an early return, where the return skips it for the cases the ticket is about.\n" +
	"- every rule in force for this run — the conventions in your instructions — that the diff breaks.\n" +
	"- every fail-closed path the diff adds that no test exercises.\n\n" +
	reviewFindingsBlockInstruction

// ReviewPrompt is the review pass's prompt: the branch's diff, the ticket,
// then the review-findings format instruction.
func ReviewPrompt(diff string, t Ticket) string {
	return reviewPrompt(diff, t, reviewFindingsInstruction)
}

// PreOpenReviewPrompt is the pre-open review pass's prompt: ReviewPrompt's diff
// and ticket, then the checks the pre-PR loop has to ask for rather than make.
func PreOpenReviewPrompt(diff string, t Ticket) string {
	return reviewPrompt(diff, t, preOpenReviewInstruction)
}

// reviewPrompt is the diff under review and the ticket, then instruction.
func reviewPrompt(diff string, t Ticket, instruction string) string {
	return "## Diff under review\n\n```diff\n" + diff + "\n```\n\n## Ticket\n\n" + t.Text() + "\n\n" + instruction
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
