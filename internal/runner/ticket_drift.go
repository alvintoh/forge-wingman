package runner

import "strings"

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

// TicketDriftLines returns the lines that differ between the ticket a run was
// claimed with and the description Linear holds now: each removed line prefixed
// "- " and each added one "+ ", in order, with the lines they share left out.
func TicketDriftLines(built, current string) []string {
	oldLines, newLines := splitLines(built), splitLines(current)
	if len(oldLines)*len(newLines) > maxDriftCells {
		return driftLinesBySet(oldLines, newLines)
	}
	n, m := len(oldLines), len(newLines)
	table := make([]int32, (n+1)*(m+1))
	at := func(i, j int) int32 { return table[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
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
		case oldLines[i] == newLines[j]:
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
func driftLinesBySet(oldLines, newLines []string) []string {
	counts := make(map[string]int, len(oldLines)+len(newLines))
	for _, line := range oldLines {
		counts[line]++
	}
	for _, line := range newLines {
		counts[line]--
	}
	var out []string
	for _, line := range oldLines {
		if counts[line] > 0 {
			out = append(out, "- "+line)
			counts[line]--
		}
	}
	for _, line := range newLines {
		if counts[line] < 0 {
			out = append(out, "+ "+line)
			counts[line]++
		}
	}
	return out
}

// splitLines splits a description into lines, normalising CRLF and dropping the
// newline that ends the last line, so a body with and without that terminator
// compares equal. An empty description is no lines.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
