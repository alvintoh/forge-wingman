package runner

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxDriftCells bounds the line table TicketDriftLines builds. A description
// pair past it falls back to a membership difference, which quotes the same
// lines without the order-preserving walk.
const maxDriftCells = 4_000_000

// TicketDrift is what a run's PR notes report when its ticket changed after the
// run read it: the lines that differ, or Unread when the recorded change could
// not be read.
type TicketDrift struct {
	Lines  []string
	Unread bool
}

const (
	// maxDriftQuoteLines, maxDriftQuoteBytes and maxDriftLineBytes bound what a
	// PR's notes quote of a ticket change, so a rewritten ticket cannot push the
	// body past GitHub's 65,536 characters beside the copy the run was built from.
	maxDriftQuoteLines = 40
	maxDriftQuoteBytes = 8 << 10
	maxDriftLineBytes  = 240
)

// autolink and mdEscape are what Linear's re-rendering of a description
// changes without the author editing a word: a bare URL saved as <url>, and a
// markdown punctuation mark saved with a backslash before it.
var (
	autolink = regexp.MustCompile(`<(https?://[^>\s]+)>`)
	mdEscape = regexp.MustCompile("\\\\([\\\\`*_{}\\[\\]()#+\\-.!|<>~])")
)

// TicketChanged reports whether a ticket description differs from the copy a
// run was claimed with in any line a reader would see as changed. It is the one
// definition of "changed": the store stamps a run by it and the PR quotes what
// it finds, so a re-save that moves no word neither drafts a PR nor quotes an
// empty difference.
func TicketChanged(built, current string) bool {
	if built == current {
		return false
	}
	return len(TicketDriftLines(built, current)) > 0
}

// TicketDriftLines returns the lines that differ between the ticket a run was
// claimed with and the description Linear holds now: each removed line prefixed
// "- " and each added one "+ ", in order, with the lines they share left out.
// Lines compare after their line ending, trailing whitespace, autolink brackets
// and markdown escapes are normalised, and the quote shows each line as written.
func TicketDriftLines(built, current string) []string {
	oldLines, newLines := splitLines(built), splitLines(current)
	oldKeys, newKeys := lineKeys(oldLines), lineKeys(newLines)
	if len(oldLines)*len(newLines) > maxDriftCells {
		return driftLinesBySet(oldLines, newLines, oldKeys, newKeys)
	}
	n, m := len(oldLines), len(newLines)
	table := make([]int32, (n+1)*(m+1))
	at := func(i, j int) int32 { return table[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldKeys[i] == newKeys[j] {
				table[i*(m+1)+j] = at(i+1, j+1) + 1
				continue
			}
			table[i*(m+1)+j] = max(at(i+1, j), at(i, j+1))
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldKeys[i] == newKeys[j]:
			i, j = i+1, j+1
		case at(i+1, j) >= at(i, j+1):
			out = append(out, "- "+oldLines[i])
			i++
		default:
			out = append(out, "+ "+newLines[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+oldLines[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+newLines[j])
	}
	return out
}

// driftLinesBySet is the coarse difference the table's size cap falls back to:
// every line whose count in one description exceeds its count in the other.
func driftLinesBySet(oldLines, newLines, oldKeys, newKeys []string) []string {
	counts := make(map[string]int, len(oldLines)+len(newLines))
	for _, key := range oldKeys {
		counts[key]++
	}
	for _, key := range newKeys {
		counts[key]--
	}
	var out []string
	for i, line := range oldLines {
		if counts[oldKeys[i]] > 0 {
			out = append(out, "- "+line)
			counts[oldKeys[i]]--
		}
	}
	for j, line := range newLines {
		if counts[newKeys[j]] < 0 {
			out = append(out, "+ "+line)
			counts[newKeys[j]]++
		}
	}
	return out
}

// CapDriftLines bounds the lines a PR quotes: at most maxDriftQuoteLines lines
// and maxDriftQuoteBytes bytes in all, each cut to maxDriftLineBytes on a
// character boundary. It returns the lines to quote and how many it left out.
func CapDriftLines(lines []string) ([]string, int) {
	var kept []string
	used := 0
	for _, line := range lines {
		line = capLine(line)
		if len(kept) == maxDriftQuoteLines || used+len(line)+1 > maxDriftQuoteBytes {
			break
		}
		kept = append(kept, line)
		used += len(line) + 1
	}
	return kept, len(lines) - len(kept)
}

// capLine cuts line to maxDriftLineBytes, stepping back to the start of a
// character so the cut never leaves a broken one, and marks the cut with an
// ellipsis.
func capLine(line string) string {
	if len(line) <= maxDriftLineBytes {
		return line
	}
	cut := maxDriftLineBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + "…"
}

// lineKeys is each line's comparison form.
func lineKeys(lines []string) []string {
	keys := make([]string, len(lines))
	for i, line := range lines {
		keys[i] = normaliseLine(line)
	}
	return keys
}

// normaliseLine strips what a re-save changes without an edit: trailing
// whitespace, the angle brackets Linear puts around a bare URL, and the
// backslash it puts before markdown punctuation.
func normaliseLine(line string) string {
	line = strings.TrimRight(line, " \t")
	line = autolink.ReplaceAllString(line, "$1")
	return mdEscape.ReplaceAllString(line, "$1")
}

// splitLines splits a description into lines, normalising CRLF and dropping the
// newlines that end the last line, so a body with and without them compares
// equal. An empty description is no lines.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
