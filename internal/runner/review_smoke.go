package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// ReviewSmokeResult is what one model's review of the fixture diff produced and cost.
type ReviewSmokeResult struct {
	ReviewResult
	// Flagged is whether Findings mention the planted defect.
	Flagged bool
}

// ReviewSmoke writes a fixture with one planted defect into dir, runs the review
// prompt over its diff on model, and meters the run against the plan's rates.
// Its errors are Review's; Findings is cut to the start of the block.
func ReviewSmoke(ctx context.Context, agent Agent, model, dir string, secrets []string) (ReviewSmokeResult, error) {
	if err := os.WriteFile(filepath.Join(dir, reviewSmokeFile), []byte(reviewSmokeSource), 0o600); err != nil {
		return ReviewSmokeResult{}, fmt.Errorf("writing the fixture: %w", err)
	}
	review, err := Review(ctx, agent, model, dir, ReviewPrompt(reviewSmokeDiff(), reviewSmokeTicket), secrets)
	res := ReviewSmokeResult{ReviewResult: review}
	if err != nil {
		return res, err
	}
	res.Findings = truncate(review.Findings, smokeTextLimit)
	lower := strings.ToLower(review.Findings)
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
