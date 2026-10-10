package runner

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultPRTemplate is the pull request template the pr job renders.
const DefaultPRTemplate = ".github/pull_request_template.md"

const (
	templateTicketLine   = "**Ticket:** closes <TEAM-n>"
	templateSummary      = "## Summary"
	templateVerification = "## Verification"
	templateTableRule    = "|---|---|---|"
	templateScreens      = "## Screenshots"
	templateNotes        = "## Notes"
)

// errTemplate reports a pull request template missing a line PRBody fills in.
var errTemplate = errors.New("pull request template lacks the ticket line, summary, verification table or notes")

// PRBody renders the pull request template for a run of ticket t: `ref` to the
// ticket, prSummary as the summary (or `ID`: title when it is empty), the
// check job's report in place of the example rows, and runURL and the ticket's
// body, fenced, in the notes, dropping the screenshots section. failedGate is
// empty when the check reported a pass. loopDetail is the pre-PR loop's report
// of why the PR opened as a draft (FR-5), empty when it did not, and overlap
// names the in-flight run whose plan these files touch, empty when none does.
// outOfPlan are the edited files the run's plan did not name, which also keep it
// a draft.
func PRBody(template string, t Ticket, prSummary, failedGate, runURL, loopDetail, overlap string, outOfPlan []string) (string, error) {
	summary := plainText(prSummary)
	if summary == "" {
		summary = "`" + t.ID + "`: " + t.Title
	}
	lines := strings.Split(strings.ReplaceAll(template, "\r\n", "\n"), "\n")
	var out []string
	var section string
	filled := map[string]bool{}
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") {
			section = strings.TrimSpace(line)
		}
		switch {
		case strings.HasPrefix(line, templateTicketLine):
			out = append(out, "**Ticket:** ref "+t.ID)
			filled[templateTicketLine] = true
		case section == templateScreens:
		case section == templateSummary && commentEnds(lines, i):
			out = append(out, line, summary)
			filled[templateSummary] = true
		case line == templateTableRule:
			out = append(out, line, gateRow(failedGate))
			filled[templateTableRule] = true
		case strings.HasPrefix(line, "| ") && filled[templateTableRule] && section == templateVerification:
		case line == templateNotes:
			notes := []string{line, "Built unattended by forge-wingman. Run: " + runURL}
			if loopDetail != "" {
				notes = append(notes, "", "**Pre-PR loop:** kept this a draft — "+plainText(loopDetail))
			}
			if overlap != "" {
				notes = append(notes, "", "**Concurrent run:** kept this a draft — "+
					codeSpan(overlap)+" plans to write some of these files")
			}
			if len(outOfPlan) > 0 {
				spans := make([]string, len(outOfPlan))
				for i, f := range outOfPlan {
					spans[i] = codeSpan(f)
				}
				notes = append(notes, "", "**Edited outside the plan:** "+strings.Join(spans, ", "))
			}
			notes = append(notes, "", "The ticket as built:", "", fence(t.Body), t.Body, fence(t.Body), "")
			out = append(out, notes...)
			filled[templateNotes] = true
		default:
			out = append(out, line)
		}
	}
	if len(filled) != 4 {
		return "", errTemplate
	}
	return strings.Join(out, "\n"), nil
}

// commentEnds reports whether line i closes the first HTML comment of its section.
func commentEnds(lines []string, i int) bool {
	if !strings.HasSuffix(strings.TrimSpace(lines[i]), "-->") {
		return false
	}
	for j := i - 1; j >= 0 && !strings.HasPrefix(lines[j], "## "); j-- {
		if strings.HasSuffix(strings.TrimSpace(lines[j]), "-->") {
			return false
		}
	}
	return true
}

// plainText renders model-written text literally: every ASCII punctuation mark
// is backslash-escaped, so it forms no image, link, heading or emphasis, and
// every @ is followed by a zero-width space, so it notifies no one.
func plainText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf && (unicode.IsPunct(r) || unicode.IsSymbol(r)) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
		if r == '@' {
			b.WriteRune('\u200b')
		}
	}
	return b.String()
}

// codeSpan renders a build-chosen path as one inline code span, escaped so a
// newline cannot end the line, so the path cannot add a mention, link or heading.
func codeSpan(s string) string {
	q := strconv.Quote(s)
	q = q[1 : len(q)-1]
	longest, run := 0, 0
	for _, r := range q {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	tick := strings.Repeat("`", longest+1)
	if strings.HasPrefix(q, "`") || strings.HasSuffix(q, "`") {
		q = " " + q + " "
	}
	return tick + q + tick
}

// fence is a code fence longer than any run of backticks in s.
func fence(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func gateRow(failedGate string) string {
	if failedGate == "" {
		return "| `make check` | the check job reports every gate passing, by the branch's own config: not proof | ✅ |"
	}
	return "| `make check` · " + failedGate + " | the check job reports the " + failedGate +
		" gate failing, by the branch's own config: not proof | ❌ |"
}
