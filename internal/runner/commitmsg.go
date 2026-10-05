package runner

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxCommitSubjectRunes is the conventional-commit subject length the block must keep to.
const maxCommitSubjectRunes = 72

// commitMessageInstruction tells the build agent the block its final message
// ends with, from which the run takes its commit message and PR summary.
var commitMessageInstruction = fmt.Sprintf("End your final response with exactly one fenced code block giving the commit "+
	"message for the whole change so far: a conventional-commit subject of at most %d characters that names the "+
	"ticket's id, a blank line, one sentence summarising the change for the pull request, a blank line, then one "+
	"`- ` bullet per thing that changed:\n\n"+
	"```commit-message\nfeat(runner): ABC-1 add the widget\n\nOne sentence summarising the change.\n\n"+
	"- first thing that changed\n- second thing that changed\n```", maxCommitSubjectRunes)

// withCommitInstruction is prompt followed by the commit-message format instruction.
func withCommitInstruction(prompt string) string {
	return prompt + "\n\n" + commitMessageInstruction
}

// errCommitMessage reports a build agent's final message that carries no valid
// commit-message block.
var errCommitMessage = errors.New("build agent's response carries no valid commit-message block")

var commitMessageBlock = regexp.MustCompile("(?s)```commit-message\\s*\\n(.*?)```")

// CommitMessage is the text a run commits with and opens its PR under.
type CommitMessage struct {
	// Subject is the commit subject and PR title.
	Subject string
	// Summary is the PR's one-line summary.
	Summary string
	// Body is the commit message after its subject.
	Body string
}

// Text is the full commit message: the subject, a blank line, then the body.
func (m CommitMessage) Text() string {
	return m.Subject + "\n\n" + m.Body
}

// ParseCommitMessage reads text for its last commit-message block and returns
// it when the subject is a conventional-commit subject of at most 72
// characters naming ticketID, a summary follows it and at least one `- `
// bullet follows that.
func ParseCommitMessage(text, ticketID string) (CommitMessage, error) {
	matches := commitMessageBlock.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return CommitMessage{}, errCommitMessage
	}
	paras := paragraphs(matches[len(matches)-1][1])
	if len(paras) < 3 {
		return CommitMessage{}, fmt.Errorf("%w: want a subject, a summary and bullets", errCommitMessage)
	}
	subject, summary := paras[0], strings.Join(strings.Fields(paras[1]), " ")
	switch {
	case strings.Contains(subject, "\n") || !printable(subject):
		return CommitMessage{}, fmt.Errorf("%w: subject is not one printable line", errCommitMessage)
	case !conventionalPrefix.MatchString(subject):
		return CommitMessage{}, fmt.Errorf("%w: subject has no conventional-commit prefix", errCommitMessage)
	case !strings.Contains(subject, ticketID):
		return CommitMessage{}, fmt.Errorf("%w: subject does not name %s", errCommitMessage, ticketID)
	case utf8.RuneCountInString(subject) > maxCommitSubjectRunes:
		return CommitMessage{}, fmt.Errorf("%w: subject is over %d characters", errCommitMessage, maxCommitSubjectRunes)
	case strings.HasPrefix(summary, "- ") || !printable(summary):
		return CommitMessage{}, fmt.Errorf("%w: no summary sentence before the bullets", errCommitMessage)
	}
	body := strings.Join(paras[2:], "\n\n")
	if !slices.ContainsFunc(strings.Split(body, "\n"), func(l string) bool { return strings.HasPrefix(l, "- ") }) {
		return CommitMessage{}, fmt.Errorf("%w: no `- ` bullet", errCommitMessage)
	}
	return CommitMessage{Subject: subject, Summary: summary, Body: body}, nil
}

// paragraphs splits s on blank lines into its trimmed, non-empty paragraphs.
func paragraphs(s string) []string {
	var out, cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

// adopt replaces m with text's commit-message block when that block is valid.
func (m *CommitMessage) adopt(text, ticketID string) {
	if parsed, err := ParseCommitMessage(text, ticketID); err == nil {
		*m = parsed
	}
}

var titleTag = regexp.MustCompile(`^\[([A-Za-z]+)\]\s*`)

// laneTypes maps a ticket title's lane tag to its conventional-commit type;
// an absent or unlisted tag is a feat.
var laneTypes = map[string]string{"INFRA": "chore", "OPS": "chore"}

// FallbackCommitMessage is the commit message when the build agent gave no
// valid block: the type from the title's lane tag, the scope from the
// packages files touch most, the title without its tag as the summary, and
// the ticket's body. A title already carrying a conventional-commit prefix
// keeps Subject's form.
func FallbackCommitMessage(t Ticket, files []string) CommitMessage {
	if conventionalPrefix.MatchString(t.Title) {
		return CommitMessage{Subject: t.Subject(), Summary: t.Title, Body: t.Body}
	}
	summary := t.Title
	commitType := "feat"
	if m := titleTag.FindStringSubmatch(t.Title); m != nil {
		summary = t.Title[len(m[0]):]
		if lt, ok := laneTypes[strings.ToUpper(m[1])]; ok {
			commitType = lt
		}
	}
	if summary == "" {
		summary = t.Title
	}
	prefix := commitType
	if scope := topPackage(files); scope != "" {
		prefix += "(" + scope + ")"
	}
	return CommitMessage{Subject: prefix + ": " + t.ID + " " + lowerFirst(summary), Summary: summary, Body: t.Body}
}

// topPackage is the package under internal/ or cmd/ that files touch most,
// the alphabetically first on a tie, or empty when none is under either.
func topPackage(files []string) string {
	counts := map[string]int{}
	for _, f := range files {
		parts := strings.Split(f, "/")
		if len(parts) >= 3 && (parts[0] == "internal" || parts[0] == "cmd") {
			counts[parts[1]]++
		}
	}
	best := ""
	for pkg, n := range counts {
		if n > counts[best] || (n == counts[best] && pkg < best) {
			best = pkg
		}
	}
	return best
}

func lowerFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[size:]
}
