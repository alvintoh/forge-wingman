package runner

import (
	"errors"
	"strings"
)

// ticketSentinel is TICKET_SENTINEL in tools/projection/gen_projection.py.
const ticketSentinel = "<<<TICKET>>>"

// errSentinel reports a projection that is empty before its ticket placeholder, or
// whose placeholder is missing, repeated, or not last.
var errSentinel = errors.New("projection must be rules followed by exactly one trailing ticket sentinel")

// RenderPromptParts splits a projection into its rules head and the ticket text
// its sentinel stands for. A harness writes the rules head to its memory file,
// ahead of the per-run context block, and sends only the ticket as the prompt.
func RenderPromptParts(projection string, t Ticket) (rules, ticket string, err error) {
	head, tail, found := strings.Cut(projection, ticketSentinel)
	if !found || strings.TrimSpace(head) == "" || strings.TrimSpace(tail) != "" {
		return "", "", errSentinel
	}
	return head, t.Text() + tail, nil
}
