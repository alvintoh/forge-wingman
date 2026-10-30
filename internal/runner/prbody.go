package runner

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
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
	// screenshotHeading heads the body section the pr step writes with the
	// screenshots the run published on the evidence branch.
	screenshotHeading = "## Screenshots (fixture data)"
	// noShotsLine is the one line a section with no screenshots says instead.
	noShotsLine = "No screenshots: this run captured none."
)

// screenshotBands are the viewport bands every route is shot at, in the order
// the screenshots table's columns list them.
var screenshotBands = []string{"s", "m", "l", "xl"}

// errTemplate reports a pull request template missing a line PRBody fills in.
var errTemplate = errors.New("pull request template lacks the ticket line, summary, verification table or notes")

// PRBody renders the pull request template for a run of ticket t: `ref` to the
// ticket, prSummary as the summary (or `ID`: title when it is empty), the
// check job's report in place of the example rows, and runURL and the ticket's
// body, fenced, in the notes, dropping the screenshots section. failedGate is
// empty when the check reported a pass. loopDetail is the pre-PR loop's report
// of why the PR opened as a draft (FR-5), empty when it did not. overlap names
// the in-flight run whose plan these files touch, empty when none does.
// outOfPlan are the edited files the run's plan did not name, which also keep it
// a draft. frontEnd names what held a front-end change for the owner, empty when
// nothing did. noPlan is set for a run no plan stage recorded a write set for,
// which waits for the owner rather than merging itself. review is what the
// pre-open review found and how the run left it, listed in the notes and empty
// for a run whose review found nothing. provisional are the decisions auto-pilot
// took the recommended option on, empty for a run that took none. supersedes is
// the number of the PR an owner's choice replaced, zero when none, and retried
// says that choice was a retry with a note.
func PRBody(template string, t Ticket, prSummary, failedGate, runURL, loopDetail, overlap string, outOfPlan []string, writeSetUnread bool, frontEnd []string, noPlan bool, review []ReviewFinding, provisional []Provisional, supersedes int, retried bool) (string, error) {
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
			if writeSetUnread {
				notes = append(notes, "", "**Write set unread:** kept this a draft — the files this run changed could not be read from its bundle, so nothing about its write set is known")
			}
			if len(outOfPlan) > 0 {
				spans := make([]string, len(outOfPlan))
				for i, f := range outOfPlan {
					spans[i] = codeSpan(f)
				}
				notes = append(notes, "", "**Edited outside the plan:** "+strings.Join(spans, ", "))
			}
			if len(frontEnd) > 0 {
				spans := make([]string, len(frontEnd))
				for i, m := range frontEnd {
					spans[i] = codeSpan(m)
				}
				notes = append(notes, "", "**Front-end change:** waits for the owner — matched "+strings.Join(spans, ", ")+"; run /fe-sweep before reading the diff")
			}
			if noPlan {
				notes = append(notes, "", "**No recorded plan:** not eligible for auto-merge, so this waits for the owner — no plan stage recorded a write set for this run, so nothing it changed could be checked against one")
			}
			if supersedes > 0 {
				notes = append(notes, "", "Supersedes #"+strconv.Itoa(supersedes)+": "+supersedeReason(retried))
			}
			if len(provisional) > 0 {
				notes = append(notes, "", "**Provisional decisions:** auto-pilot took the recommended option on each, so this waits for the owner")
				for _, p := range provisional {
					notes = append(notes, "- "+plainText(p.Question)+" — took "+codeSpan(p.Choice)+", "+plainText(p.Cost))
				}
			}
			if len(review) > 0 {
				notes = append(notes, "", "**Pre-open review:** every finding it made, and how the run left it")
				for _, f := range review {
					notes = append(notes, "- "+plainText(f.Finding)+" — "+plainText(f.Resolution))
				}
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

// Screenshot is one image a run publishes for its pull request.
type Screenshot struct {
	// Route is the page the shot shows.
	Route string
	// Band is the viewport it was shot at, one of screenshotBands.
	Band string
	// Path is where it is published on the evidence branch.
	Path string

	// file is the image's name inside the shots artifact.
	file string
}

// ScreenshotsSection renders the body's screenshots section for shots published
// on repo's evidence branch at the head sha they were shot at: the heading with
// the sha beside it, then a table of routes by band whose cells are 200px
// images from that branch's raw URL. With no shots it is the one line saying
// the run captured none.
func ScreenshotsSection(repo, sha string, shots []Screenshot) string {
	if len(shots) == 0 {
		return screenshotHeading + "\n\n" + noShotsLine + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · `%s`\n\n", screenshotHeading, sha)
	b.WriteString("| Route | " + strings.Join(screenshotBands, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(screenshotBands)+1) + "\n")
	for _, row := range screenshotRows(repo, shots) {
		b.WriteString("| " + codeSpan(row.route) + " |")
		for _, band := range screenshotBands {
			b.WriteString(" " + row.cells[band] + " |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// WithScreenshots replaces the screenshots section in body with section, never
// adding a second one, so a push to the branch re-renders the body from the
// previous one rather than growing it. A body with no section gains it before
// the notes.
func WithScreenshots(body, section string) string {
	if strings.TrimSpace(body) == "" {
		return section
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, screenshotHeading) {
			start = i
			break
		}
	}
	if start < 0 {
		at := len(lines)
		for i, line := range lines {
			if line == templateNotes {
				at = i
				break
			}
		}
		out := make([]string, 0, len(lines)+4)
		out = append(out, lines[:at]...)
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, strings.Split(section, "\n")...)
		return strings.Join(append(out, lines[at:]...), "\n")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[:start]...)
	out = append(out, strings.Split(section, "\n")...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// screenshotRow is one route's images, keyed by band.
type screenshotRow struct {
	route string
	cells map[string]string
}

// screenshotRows groups shots into one row per route, routes in name order so a
// re-render of the same shots is identical.
func screenshotRows(repo string, shots []Screenshot) []screenshotRow {
	cells := map[string]map[string]string{}
	for _, s := range shots {
		if cells[s.Route] == nil {
			cells[s.Route] = map[string]string{}
		}
		cells[s.Route][s.Band] = screenshotCell(repo, s)
	}
	routes := make([]string, 0, len(cells))
	for route := range cells {
		routes = append(routes, route)
	}
	slices.Sort(routes)
	rows := make([]screenshotRow, len(routes))
	for i, route := range routes {
		rows[i] = screenshotRow{route: route, cells: cells[route]}
	}
	return rows
}

// screenshotCell renders one shot as a 200px image in a table cell.
func screenshotCell(repo string, s Screenshot) string {
	return `<img width="200" alt="` + attr(s.Route+" at "+s.Band) + `" src="` + attr(rawURL(repo, s.Path)) + `">`
}

// rawURL is p's raw URL on the evidence branch of repo.
func rawURL(repo, p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return "https://raw.githubusercontent.com/" + repo + "/" + EvidenceBranch + "/" + strings.Join(segments, "/")
}

// attrEscaper escapes the characters that would end an HTML attribute's value.
var attrEscaper = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")

// attr renders s inside a double-quoted HTML attribute.
func attr(s string) string { return attrEscaper.Replace(s) }

// supersedeReason is why the owner replaced a PR: a retry with a note, or a
// different option chosen on a decision.
func supersedeReason(retried bool) string {
	if retried {
		return "the owner retried this run with a note."
	}
	return "the owner chose a different option."
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
