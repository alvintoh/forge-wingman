package dispatcher

import (
	"fmt"
	"strings"
	"time"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const (
	// sizePrefix and repoPrefix label a ticket for the dispatcher: a hand-set
	// size, and the repository its run is built in.
	sizePrefix = "size:"
	repoPrefix = "repo:"
	// sizedByLabel is what sized a ticket the dispatcher admitted, since a
	// size label is the owner's own sizing rather than the classifier's.
	sizedByLabel = "linear-label"
	// ceilingSize is the largest ticket the queue admits.
	ceilingSize = "L"
	// aboveCeiling is the size label naming the next ticket up, which one run
	// is not measured to carry.
	aboveCeiling = "XL"
)

// sizes are the ticket sizes one run is measured to carry, the same ladder
// runner.Ticket validates.
var sizes = map[string]bool{"S": true, "M": true, "L": true}

// build turns a delegated issue into a queued run, or names the refusal that
// keeps it out of the queue. An empty rejection reason means the issue is
// admissible. The repository is checked before the size, so a ticket the
// dispatcher may not dispatch into at all is refused for that rather than for a
// property the owner can only fix afterwards.
func build(issue Issue, c Config, at time.Time) (Queued, Rejection) {
	repo, reason, detail := repository(issue.Labels, c.Repos)
	if reason != "" {
		return Queued{}, rejection(issue, reason, detail, at)
	}
	size, reason, detail := sizeLabel(issue.Labels)
	if reason != "" {
		return Queued{}, rejection(issue, reason, detail, at)
	}
	rank, err := priorityRank(issue.Priority)
	if err != nil {
		return Queued{}, rejection(issue, RefusalTicketInvalid, err.Error(), at)
	}
	t := runner.Ticket{ID: issue.ID, Title: issue.Title, Body: issue.Body, Size: size, SizedBy: sizedByLabel}
	if err := t.Validate(); err != nil {
		return Queued{}, rejection(issue, RefusalTicketInvalid, err.Error(), at)
	}
	return Queued{RunID: issue.ID, Ticket: t, Repo: repo, Priority: rank, At: at}, Rejection{}
}

// rejection is the record kept against a ticket the dispatcher would not admit.
func rejection(issue Issue, reason Refusal, detail string, at time.Time) Rejection {
	return Rejection{Ticket: issue.ID, Reason: reason, Detail: detail, At: at}
}

// repository is the repository a repo: label names, spelled as the allowlist
// spells it, so a label's own casing cannot redirect a run.
func repository(labels, allow []string) (string, Refusal, string) {
	label, ok := labelValue(labels, repoPrefix)
	if !ok {
		return "", RefusalNoRepository, "no " + repoPrefix + " label"
	}
	for _, repo := range allow {
		if strings.EqualFold(repo, label) {
			return repo, "", ""
		}
	}
	return "", RefusalNotAllowlist, label + " is not an allowlisted repository"
}

// sizeLabel is the ticket's size, and the refusal when it carries no size
// label, one outside the ladder, or one naming a ticket above the ceiling.
func sizeLabel(labels []string) (string, Refusal, string) {
	label, ok := labelValue(labels, sizePrefix)
	if !ok {
		return "", RefusalNoSize, "no " + sizePrefix + " label"
	}
	switch s := strings.ToUpper(label); {
	case sizes[s]:
		return s, "", ""
	case s == aboveCeiling:
		return "", RefusalAboveCeiling, "size " + s + " is above " + ceilingSize
	}
	return "", RefusalSizeUnknown, "size " + label + " is not S, M or L"
}

// labelValue is the value of the first label carrying prefix. The first match
// decides, so a repeated label cannot move a ticket's size or destination.
func labelValue(labels []string, prefix string) (string, bool) {
	for _, label := range labels {
		if value, ok := strings.CutPrefix(label, prefix); ok && value != "" {
			return value, true
		}
	}
	return "", false
}

// noPriorityRank sorts an issue Linear holds at priority 0 behind every priority
// it does hold, which ORDER BY cannot do for a 0 in first position.
const noPriorityRank = 5

// priorityRank is the issue's Linear priority as the queue orders it: 1 is
// urgent and 4 low, and 0, meaning no priority, sorts last.
func priorityRank(priority int) (int, error) {
	switch priority {
	case 0:
		return noPriorityRank, nil
	case 1, 2, 3, 4:
		return priority, nil
	}
	return 0, fmt.Errorf("%w: Linear priority %d is not 0 to 4", runner.ErrTicketInvalid, priority)
}
