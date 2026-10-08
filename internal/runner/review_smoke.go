package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

const (
	reviewSmokeFile   = "percent.go"
	reviewSmokeSource = "package stats\n\n" +
		"// Percent is part as a whole-number percentage of whole.\n" +
		"func Percent(part, whole int) int {\n" +
		"\treturn part * 100 / whole\n" +
		"}\n"
)

// reviewSmokeTicket asks for a guard reviewSmokeSource lacks: it divides by whole unchecked.
var reviewSmokeTicket = Ticket{
	ID:    "SMOKE-1",
	Title: "Add stats.Percent",
	Size:  "S",
	Body: "Add `Percent(part, whole int) int` to package stats, returning part as a whole-number percentage of whole.\n\n" +
		"Acceptance criteria:\n\n1. Percent(1, 4) returns 25.\n2. Percent(n, 0) returns 0 rather than panicking.",
}

// reviewSmokeDefectMarkers are words a finding about the unchecked division would carry.
var reviewSmokeDefectMarkers = []string{"zero", "divi", "panic"}

// ErrReviewSmokeOutput reports a review agent's final message that carries no readable review-findings block.
var ErrReviewSmokeOutput = errors.New("findings unreadable")

// ReviewSmokeResult is what one model's review of the fixture diff produced and cost.
type ReviewSmokeResult struct {
	Model string
	// Priced is whether the plan has a rate card for Model, so Cost is the runner's own figure.
	Priced bool
	Cost   float64
	// Findings is the start of the review-findings block's content, empty for a clean review.
	Findings string
	// Flagged is whether Findings mention the planted defect.
	Flagged bool
	// Text is the start of the agent's final message.
	Text string
}

// ReviewSmoke writes a fixture with one planted defect into dir, runs the review
// prompt over its diff on model, and meters the run against the plan's rates.
//
// A run that fails, or outlives ctx, is an error; its message carries the end of
// the agent's stderr with every secret redacted. A final message with no
// review-findings block is ErrReviewSmokeOutput, returned with the metered result.
func ReviewSmoke(ctx context.Context, agent Agent, model, dir string, secrets []string) (ReviewSmokeResult, error) {
	if err := os.WriteFile(filepath.Join(dir, reviewSmokeFile), []byte(reviewSmokeSource), 0o600); err != nil {
		return ReviewSmokeResult{}, fmt.Errorf("writing the fixture: %w", err)
	}
	var out, errOut bytes.Buffer
	prompt := ReviewPrompt(reviewSmokeDiff(), reviewSmokeTicket)
	if err := bindModel(agent, model).Run(ctx, dir, "", prompt, "", &out, &errOut); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ReviewSmokeResult{}, fmt.Errorf("review agent on %s: %w", model, ctxErr)
		}
		errOut.WriteString("\n" + err.Error())
		return ReviewSmokeResult{}, fmt.Errorf("review agent on %s: %s", model, redactedTail(errOut.Bytes(), smokeTextLimit, secrets))
	}
	usage, err := meterUsage(bytes.NewReader(out.Bytes()), model, providers.RatesFor)
	if err != nil {
		return ReviewSmokeResult{}, fmt.Errorf("metering %s: %w", model, err)
	}
	_, priced := providers.RatesFor(model)
	text, _ := FinalText(bytes.NewReader(out.Bytes()))
	res := ReviewSmokeResult{Model: model, Priced: priced, Cost: usage.Cost, Text: truncate(strings.TrimSpace(text), smokeTextLimit)}
	findings, err := ParseReviewFindings(text)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrReviewSmokeOutput, err)
	}
	res.Findings = truncate(findings, smokeTextLimit)
	lower := strings.ToLower(findings)
	for _, m := range reviewSmokeDefectMarkers {
		res.Flagged = res.Flagged || strings.Contains(lower, m)
	}
	return res, nil
}

// reviewSmokeDiff is the diff that adds reviewSmokeFile as a new file.
func reviewSmokeDiff() string {
	lines := strings.Split(strings.TrimSuffix(reviewSmokeSource, "\n"), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%[1]s b/%[1]s\nnew file mode 100644\n--- /dev/null\n+++ b/%[1]s\n@@ -0,0 +1,%[2]d @@\n",
		reviewSmokeFile, len(lines))
	for _, l := range lines {
		b.WriteString("+" + l + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
