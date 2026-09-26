package runner

import (
	"errors"
	"strings"
)

// DefaultPRTemplate is the pull request template the pr job renders.
const DefaultPRTemplate = ".github/pull_request_template.md"

const (
	templateTicketLine   = "**Ticket:** ref <TEAM-n>"
	templateSummary      = "## Summary"
	templateVerification = "## Verification"
	templateTableRule    = "|---|---|---|"
	templateScreens      = "## Screenshots"
	templateNotes        = "## Notes"
)

// errTemplate reports a pull request template missing a line PRBody fills in.
var errTemplate = errors.New("pull request template lacks the ticket line, summary, verification table or notes")

// PRBody renders the pull request template for a run of ticket t: `ref` to the
// ticket, its title as the summary, the check job's report in place of the example
// rows, and runURL and the ticket's body, fenced, in the notes, dropping the
// screenshots section. failedGate is empty when the check reported a pass.
func PRBody(template string, t Ticket, failedGate, runURL string) (string, error) {
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
			out = append(out, line, "`"+t.ID+"`: "+t.Title)
			filled[templateSummary] = true
		case line == templateTableRule:
			out = append(out, line, gateRow(failedGate))
			filled[templateTableRule] = true
		case strings.HasPrefix(line, "| ") && filled[templateTableRule] && section == templateVerification:
		case line == templateNotes:
			out = append(out, line, "Built unattended by forge-wingman. Run: "+runURL, "", "The ticket as built:", "",
				fence(t.Body), t.Body, fence(t.Body), "")
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
