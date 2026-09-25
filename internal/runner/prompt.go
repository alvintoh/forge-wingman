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

// BuildPrompt returns the projection verbatim with its sentinel replaced by the ticket.
func BuildPrompt(projection string, t Ticket) (string, error) {
	head, tail, found := strings.Cut(projection, ticketSentinel)
	if !found || strings.TrimSpace(head) == "" || strings.TrimSpace(tail) != "" {
		return "", errSentinel
	}
	return head + t.Text() + tail, nil
}
