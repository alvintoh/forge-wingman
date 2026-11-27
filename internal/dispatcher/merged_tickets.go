package dispatcher

import "strings"

// mergedTickets returns the ids of the issues Linear holds open whose id is a
// whole word in the title of a merged pull request that is not a revert.
func mergedTickets(issues []Issue, prs []ClosedPR) []string {
	var titles []string
	for _, pr := range prs {
		if !pr.MergedAt.IsZero() && !revertTitle.MatchString(pr.Title) {
			titles = append(titles, pr.Title)
		}
	}
	var out []string
	for _, issue := range issues {
		if issue.StateType == stateTypeCompleted || issue.StateType == stateTypeCanceled {
			continue
		}
		for _, title := range titles {
			if hasWord(title, issue.ID) {
				out = append(out, issue.ID)
				break
			}
		}
	}
	return out
}

// hasWord reports whether word occurs in text bounded by characters an
// identifier cannot contain.
func hasWord(text, word string) bool {
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], word)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(word)
		if (start == 0 || !identifierByte(text[start-1])) && (end == len(text) || !identifierByte(text[end])) {
			return true
		}
		from = start + 1
	}
	return false
}

func identifierByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '-' || b == '_'
}
