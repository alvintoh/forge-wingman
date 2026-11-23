package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// maxReviewCommentChars is the budget the specialist sections are cut to fit.
	// GitHub refuses a body over 65,536 characters; the headroom leaves room for
	// what the sections' own measuring does not count. It covers the specialist
	// sections only: the general block is written first and never measured, so a
	// general review large enough to pass the limit by itself still does.
	maxReviewCommentChars = 60000
	// specialistFindingsCutNote follows a specialist's findings block that was
	// shortened to keep the comment inside maxReviewCommentChars. It sits outside
	// the fence, so a reader never takes it for a finding.
	specialistFindingsCutNote = "Findings cut here: the comment is at GitHub's size limit."
	// specialistFindingsOmittedNote stands for a block with no room left at all.
	specialistFindingsOmittedNote = "Findings not shown: the comment is at GitHub's size limit."
	// specialistFindingsHoldReason is the gate that keeps an allowance-exhausted
	// review from requesting the merge once a specialist reported findings.
	specialistFindingsHoldReason = "specialist-findings"
)

// specialistHeadingLine is the heading renderSpecialist opens a section with, and
// the only line parseReviewComment reads a section from.
var specialistHeadingLine = regexp.MustCompile("^### Specialist review: ([a-z][a-z0-9-]*)$")

// specialistReasonTokens are the reasons a not-reviewed specialist states; any
// other reads as a failure rather than reaching a comment as free text.
var specialistReasonTokens = map[string]bool{
	runner.SpecialistReasonCancelled:  true,
	runner.SpecialistReasonDeadline:   true,
	runner.SpecialistReasonExhausted:  true,
	runner.SpecialistReasonTimedOut:   true,
	runner.SpecialistReasonUnreadable: true,
	runner.SpecialistReasonFailed:     true,
}

// specialistFindings is what one section of a review comment holds.
type specialistFindings struct {
	Name     string
	Findings string
}

// configuredSpecialists is v's specialists in the order the review areas are
// configured, which is the order their sections render in. An entry naming no
// configured area is left out.
func configuredSpecialists(v runner.PRVerdict) []runner.SpecialistVerdict {
	var out []runner.SpecialistVerdict
	for _, area := range runner.ReviewAreas() {
		for _, s := range v.Specialists {
			if s.Name == area.Name {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// hasFindings reports whether a specialist reviewed and found something.
func hasFindings(s runner.SpecialistVerdict) bool {
	return s.Outcome == runner.SpecialistReviewed && strings.TrimSpace(s.Findings) != ""
}

// specialistsWithFindings is the names of the specialists that reported
// findings, in configuration order.
func specialistsWithFindings(v runner.PRVerdict) []string {
	var names []string
	for _, s := range configuredSpecialists(v) {
		if hasFindings(s) {
			names = append(names, s.Name)
		}
	}
	return names
}

// renderSpecialists is the sections of v's specialists, one after another, that
// fit in room characters: each takes a fair share of what the sections before it
// left, so one verbose specialist cannot starve the rest.
func renderSpecialists(v runner.PRVerdict, sha string, room int) string {
	specialists := configuredSpecialists(v)
	var b strings.Builder
	for i, s := range specialists {
		section := renderSpecialist(s, sha, room/(len(specialists)-i))
		b.WriteString(section)
		room = max(0, room-utf8.RuneCountInString(section))
	}
	return b.String()
}

// renderSpecialist is one specialist's section: a heading, then its findings in
// the same fenced and mention-neutralised block the general findings use, or the
// words for no findings or for a review that did not happen, then its figures.
// Findings that would take the section past room are cut at a line, with a note.
func renderSpecialist(s runner.SpecialistVerdict, sha string, room int) string {
	figures := "\n<sub>" + usageLine(s.Model, s.Usage, s.Cost, len(s.Attempts)) + "</sub>\n"
	heading := "\n" + "### Specialist review: " + s.Name + "\n\n"
	switch {
	case s.Outcome != runner.SpecialistReviewed:
		reason := s.Reason
		if !specialistReasonTokens[reason] {
			reason = runner.SpecialistReasonFailed
		}
		return heading + "Not reviewed: " + reason + ".\n" + figures
	case !hasFindings(s):
		return heading + "No findings in this area.\n" + figures
	}
	section := func(raw string, note string) string {
		body := "At `" + shortSHA(sha) + "`:\n\n" + fenced(neutralise(raw)) + "\n"
		if note != "" {
			body += "\n" + note + "\n"
		}
		return heading + body + figures
	}
	fits := func(raw, note string) bool { return utf8.RuneCountInString(section(raw, note)) <= room }
	if fits(s.Findings, "") {
		return section(s.Findings, "")
	}
	lines := strings.Split(s.Findings, "\n")
	if k := largestFit(len(lines)-1, func(k int) bool { return fits(strings.Join(lines[:k], "\n"), specialistFindingsCutNote) }); k > 0 {
		return section(strings.Join(lines[:k], "\n"), specialistFindingsCutNote)
	}
	first := []rune(lines[0])
	if m := largestFit(len(first), func(m int) bool { return fits(string(first[:m]), specialistFindingsCutNote) }); m > 0 {
		return section(string(first[:m]), specialistFindingsCutNote)
	}
	return heading + specialistFindingsOmittedNote + "\n" + figures
}

// largestFit is the largest k in [1, n] for which fits holds, or 0 when none
// does. It reads fits as monotone, which a longer prefix of findings is except
// where a backtick run lengthens the fence, so the k it returns always fits.
func largestFit(n int, fits func(k int) bool) int {
	return sort.Search(n, func(i int) bool { return !fits(i + 1) })
}

// usageLine names a model, its tokens and the cost of the attempts that reached
// it, as the review comment's footer and each specialist's figures print them.
func usageLine(model string, usage runner.Usage, cost float64, attempts int) string {
	name := "none"
	if model != "" {
		name = "`" + model + "`"
	}
	return fmt.Sprintf("Model %s · %d in / %d out tokens · $%s across %d attempt(s)",
		name, usage.Input, usage.Output, strconv.FormatFloat(cost, 'f', -1, 64), attempts)
}

// reviewSection is the lines of one specialist's section of a review comment.
type reviewSection struct {
	name  string
	lines []string
	// start is the index of the line after the section's heading.
	start int
}

// readReviewSections splits the lines of a review comment at its specialist
// headings, reading only the headings outside a fence: findings are untrusted,
// and a heading inside one must not open a section. general holds the lines
// before the first heading and each section the lines from its heading to the
// next.
func readReviewSections(lines []string) (general []string, sections []reviewSection) {
	general = lines
	fence := ""
	for i, line := range lines {
		if fence != "" {
			if line == fence {
				fence = ""
			}
			continue
		}
		if m := reviewFenceLine.FindStringSubmatch(line); m != nil {
			fence = m[1]
			continue
		}
		m := specialistHeadingLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if len(sections) == 0 {
			general = lines[:i]
		} else {
			sections[len(sections)-1].lines = lines[sections[len(sections)-1].start:i]
		}
		sections = append(sections, reviewSection{name: m[1], start: i + 1})
	}
	if n := len(sections); n > 0 {
		sections[n-1].lines = lines[sections[n-1].start:]
	}
	return general, sections
}
