package runner

import (
	"errors"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"
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

// screenshotsHeading heads the section every rendered body carries, so the
// section is found by its heading whatever it holds.
const screenshotsHeading = "## Screenshots (fixture data)"

// noScreenshots is the one line a body carries for a run that captured none.
const noScreenshots = "No screenshots: this run captured none."

// shotWidthPx is the width every screenshot is shown at in the table.
const shotWidthPx = 200

// errTemplate reports a pull request template missing a line PRBody fills in.
var errTemplate = errors.New("pull request template lacks the ticket line, summary, verification table or notes")

// ErrNoScreenshotsSection reports a pull request body with no screenshots
// section to replace, which a body written before the section existed has.
var ErrNoScreenshotsSection = errors.New("pull request body has no screenshots section")

// PRBody renders the pull request template for a run of ticket t: `ref` to the
// ticket, prSummary as the summary (or `ID`: title when it is empty), the
// check job's report in place of the example rows, and runURL and the ticket's
// body, fenced, in the notes. The screenshots section becomes the one line a
// run that captured none says, which the pr step replaces with the shots it
// pushed. failedGate is empty when the check reported a pass. loopDetail is the
// pre-PR loop's report of why the PR opened as a draft (FR-5), empty when it
// did not. overlap names the in-flight run whose plan these files touch, empty
// when none does. outOfPlan are the edited files the run's plan did not name,
// which also keep it a draft. frontEnd names what held a front-end change for
// the owner, empty when nothing did. noPlan is set for a run no plan stage
// recorded a write set for, which waits for the owner rather than merging
// itself. review is what the pre-open review found and how the run left it,
// listed in the notes and empty for a run whose review found nothing.
// secondRound is what the review's one extra fix round cost, nil for a run that
// never took one. provisional are the decisions auto-pilot took the recommended
// option on, empty for a run that took none. supersedes is the number of the PR
// an owner's choice replaced, zero when none, and retried says that choice was a
// retry with a note.
func PRBody(template string, t Ticket, prSummary, failedGate, runURL, loopDetail, overlap string, outOfPlan []string, writeSetUnread bool, frontEnd []string, noPlan bool, review []ReviewFinding, secondRound *FixRoundCost, provisional []Provisional, supersedes int, retried bool) (string, error) {
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
		case section == templateScreens && line == templateScreens:
			out = append(out, screenshotsHeading, noScreenshots, "")
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
			if secondRound != nil {
				notes = append(notes, "", secondRoundNote(secondRound))
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

// Screenshots renders the pull request body's screenshots section: the heading
// naming the sha the shots were taken at, then one row per route with a 200px
// image per viewport, each at its raw URL on the evidence branch. A route the
// run captured at only some viewports leaves the others empty.
func Screenshots(ev Evidence, repository string) string {
	var b strings.Builder
	b.WriteString(screenshotsHeading + " — shot at `" + ev.SHA + "`\n\n")
	b.WriteString("| Route | " + strings.Join(ShotWidths, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(ShotWidths)+1) + "\n")
	for _, row := range screenshotRows(ev.Files) {
		b.WriteString("| " + codeSpan(row.route))
		for _, width := range ShotWidths {
			b.WriteString(" | ")
			if file, ok := row.shots[width]; ok {
				b.WriteString(screenshot(file, repository))
			} else {
				b.WriteString("—")
			}
		}
		b.WriteString(" |\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// screenshotRow is one route's row of the screenshots section: the page it
// shows, and each viewport it was captured at.
type screenshotRow struct {
	route string
	shots map[string]EvidenceFile
}

// screenshotRows groups files into one row per route, ordered by route. Every
// file PlaceShots places has its own route and viewport, so no width is written
// twice.
func screenshotRows(files []EvidenceFile) []screenshotRow {
	var rows []screenshotRow
	byRoute := map[string]int{}
	for _, f := range files {
		i, ok := byRoute[f.Route]
		if !ok {
			i = len(rows)
			byRoute[f.Route] = i
			rows = append(rows, screenshotRow{route: f.Route, shots: map[string]EvidenceFile{}})
		}
		rows[i].shots[f.Width] = f
	}
	slices.SortFunc(rows, func(a, b screenshotRow) int { return strings.Compare(a.route, b.route) })
	return rows
}

// screenshot renders one image of file, shown at its URL on the evidence
// branch.
func screenshot(file EvidenceFile, repository string) string {
	return fmt.Sprintf("<img width=\"%d\" alt=\"%s\" src=\"%s\">", shotWidthPx,
		html.EscapeString(file.Route+" at "+file.Width), EvidenceURL(repository, file.Path))
}

// ReplaceScreenshots replaces the body's screenshots section with section, the
// run's shots at the sha they were taken at: the section is edited in place on
// every push, so a body never grows a second copy of it. A body with no section
// is refused rather than appended to, and a copy the ticket body quotes inside
// its fence is not the section.
func ReplaceScreenshots(body, section string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	fenced := fenceMask(lines)
	start := -1
	for i, line := range lines {
		if !fenced[i] && strings.HasPrefix(line, screenshotsHeading) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", ErrNoScreenshotsSection
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if !fenced[i] && strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	out := make([]string, 0, len(lines)+len(section))
	out = append(out, lines[:start]...)
	out = append(out, strings.Split(section, "\n")...)
	out = append(out, "")
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n"), nil
}

// AppendScreenshots is body with section after the sections it already has: what
// a pull request written before the section existed gains, so its next push
// finds the section and replaces it in place.
func AppendScreenshots(body, section string) string {
	trimmed := strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if trimmed == "" {
		return section + "\n"
	}
	return trimmed + "\n\n" + section + "\n"
}

// fenceMask reports which lines of a body sit inside a code fence, the way the
// ticket's own copy is fenced in the notes, so a heading it quotes is not taken
// for the body's own.
func fenceMask(lines []string) []bool {
	mask := make([]bool, len(lines))
	fence := 0
	for i, line := range lines {
		ticks := leadingTicks(line)
		switch {
		case fence == 0 && ticks >= 3:
			fence, mask[i] = ticks, true
		case fence > 0:
			mask[i] = true
			if ticks >= fence {
				fence = 0
			}
		}
	}
	return mask
}

// leadingTicks counts the backticks a line opens a code fence with, after at
// most three spaces of indentation, and 0 for a line that opens none.
func leadingTicks(line string) int {
	indented := len(line) - len(strings.TrimLeft(line, " "))
	if indented > 3 {
		return 0
	}
	rest := line[indented:]
	return len(rest) - len(strings.TrimLeft(rest, "`"))
}

// supersedeReason is why the owner replaced a PR: a retry with a note, or a
// different option chosen on a decision.
func supersedeReason(retried bool) string {
	if retried {
		return "the owner retried this run with a note."
	}
	return "the owner chose a different option."
}

// secondRoundNote is the PR notes' report of the pre-open review's one extra
// fix round: the round it ran as, what that step spent and how long it took, so
// the owner prices the round from the run itself.
func secondRoundNote(c *FixRoundCost) string {
	took := (time.Duration(c.DurationMS) * time.Millisecond).Round(time.Second)
	return fmt.Sprintf("**Second fix round:** round %d cost %d input and %d output tokens over %s, inside the review stage's budget",
		c.Round, c.Tokens.Input, c.Tokens.Output, took)
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
