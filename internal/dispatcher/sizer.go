package dispatcher

import (
	"fmt"
	"regexp"
	"strings"
)

// sizedBySignals is what sized a ticket that carried no size label: the
// deterministic signals in its own title and description.
const sizedBySignals = "deterministic"

// The acceptance-criteria counts the sizer reads a ticket's scope from. A
// ticket with no doubt signal is S up to smallACs criteria and M up to
// mediumACs; one with a single doubt is M up to smallACs. Anything else is L,
// and a ticket with more than maxACs criteria is above the ceiling.
const (
	smallACs  = 4
	mediumACs = 6
	maxACs    = 12
)

// signal is one named pattern the sizer counts a hit of.
type signal struct {
	name string
	re   *regexp.Regexp
}

var (
	// singleLayerLane is a title prefix naming one layer of the system; a slice
	// spans several and a title with no prefix names none, so either is a doubt.
	singleLayerLane = regexp.MustCompile(`(?i)^\s*\[(BE|FE|INFRA|OPS)\]`)

	// acHeading opens the acceptance criteria: a markdown heading, a line
	// opening in bold, or a line holding only the label. Its first group is the
	// heading's hashes, empty for the other two.
	acHeading = regexp.MustCompile(`(?i)^[ \t]*(#+)[ \t]*acceptance criteria\b|^[ \t]*\*\*[ \t]*acceptance criteria\b|^[ \t]*acceptance criteria[ \t]*(?:\([^)\n]*\))?[ \t]*:?[ \t]*$`)
	// mdHeading is any markdown heading; its group is the hashes.
	mdHeading = regexp.MustCompile(`^[ \t]*(#+)[ \t]`)
	// boldLine is a line opening in bold, which closes criteria that a bold or
	// bare label opened, unless it notes an amendment to them.
	boldLine  = regexp.MustCompile(`^[ \t]*\*\*[^*\n]+\*\*`)
	amendment = regexp.MustCompile(`(?i)^[ \t]*\*\*[ \t]*(?:updated|added|superseded|removed)\b`)
	// acItem is one numbered or bulleted criterion.
	acItem = regexp.MustCompile(`(?m)^ {0,3}(?:\d+[.)]|[-*+])[ \t]`)

	// riskSignals are the four kinds of change a mistake in costs most: the
	// stored schema, an API, authentication and secrets, and money. Each kind
	// the title or the criteria name is one doubt.
	riskSignals = []signal{
		{"schema", regexp.MustCompile(`(?i)\b(schema|migrations?|firestore|database|storage|index(es)?)\b`)},
		{"api", regexp.MustCompile(`(?i)\b(apis?|endpoints?|graphql|webhooks?)\b|\b(get|post|put|patch|delete) /`)},
		{"auth", regexp.MustCompile(`(?i)\b(auth|authn|authz|authenticat\w*|authori[sz]\w*|iap|oauth|credentials?|secrets?|permissions?|iam|signatures?|sign-in|isolat\w*|egress|sandbox\w*)\b`)},
		{"money", regexp.MustCompile(`(?i)\b(spend|costs?|prices?|pricing|budgets?|billing|ledger|usd|charged?)\b|\$\d`)},
	}

	// moduleSignals are the deployables and layers a criterion can name. Criteria
	// naming two or more of them are one doubt.
	moduleSignals = []signal{
		{"dispatcher", regexp.MustCompile(`(?i)\bdispatcher\b`)},
		{"runner", regexp.MustCompile(`(?i)\brunner\b`)},
		{"surface", regexp.MustCompile(`(?i)\bsurface\b`)},
		{"webhook", regexp.MustCompile(`(?i)\bwebhook\b`)},
		{"store", regexp.MustCompile(`(?i)\bstore\b`)},
		{"workflow", regexp.MustCompile(`(?i)\bworkflows?\b|\.github/`)},
		{"infra", regexp.MustCompile(`(?i)\b(opentofu|terraform|infra)\b`)},
		{"web", regexp.MustCompile(`(?i)\b(pages?|inbox|web)\b`)},
		{"container", regexp.MustCompile(`(?i)\bcontainers?\b`)},
	}
)

// signalSize is the size a ticket's title and description imply, and the
// signals that set it. Doubt always picks the larger size, so a ticket is S
// only when nothing argues for M, and M only when nothing argues for L: a
// ticket with no criteria to count is L, and one with more than maxACs is
// aboveCeiling.
func signalSize(title, body string) (size, reason string) {
	criteria, ok := acceptanceCriteria(body)
	n := len(acItem.FindAllStringIndex(criteria, -1))
	if !ok || n == 0 {
		return ceilingSize, "no acceptance criteria to count"
	}
	if n > maxACs {
		return aboveCeiling, fmt.Sprintf("%d acceptance criteria, over %d", n, maxACs)
	}
	doubts := 0
	reason = fmt.Sprintf("%d acceptance criteria", n)
	if !singleLayerLane.MatchString(title) {
		doubts++
		reason += "; no single-layer title prefix"
	}
	if risks := matching(riskSignals, title+"\n"+criteria); len(risks) > 0 {
		doubts += len(risks)
		reason += "; names " + strings.Join(risks, ", ")
	}
	if mods := matching(moduleSignals, criteria); len(mods) >= 2 {
		doubts++
		reason += "; criteria name " + strings.Join(mods, ", ")
	}
	switch {
	case n <= smallACs && doubts == 0:
		return "S", reason
	case n <= smallACs && doubts == 1, n <= mediumACs && doubts == 0:
		return "M", reason
	}
	return ceilingSize, reason
}

// acceptanceCriteria is the body's acceptance-criteria section, and whether it
// has one. A heading's section runs to the next heading of its level or above,
// so subheadings and bold notes inside it count; a bold or bare label's runs to
// the next heading or bold line that is not an amendment note.
func acceptanceCriteria(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		m := acHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		level, end := len(m[1]), len(lines)
		for j := i + 1; j < len(lines) && end == len(lines); j++ {
			if h := mdHeading.FindStringSubmatch(lines[j]); h != nil {
				if level == 0 || len(h[1]) <= level {
					end = j
				}
			} else if level == 0 && boldLine.MatchString(lines[j]) && !amendment.MatchString(lines[j]) {
				end = j
			}
		}
		return strings.Join(lines[i+1:end], "\n"), true
	}
	return "", false
}

// matching names the signals text hits, in the signals' own order.
func matching(signals []signal, text string) []string {
	var names []string
	for _, s := range signals {
		if s.re.MatchString(text) {
			names = append(names, s.name)
		}
	}
	return names
}
