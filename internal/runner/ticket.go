package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxTicketIDBytes    = 64
	maxTicketTitleBytes = 256
	maxSizedByBytes     = 64
	maxTicketBodyBytes  = 32 << 10
	// maxEncodedTicketBytes keeps the model job's ticket input under Linux's 128 KiB
	// limit on one environment string, however much the body's escaping grows it.
	maxEncodedTicketBytes = 96 << 10
)

// ErrTicketInvalid reports a ticket the runner cannot build.
var ErrTicketInvalid = errors.New("ticket is invalid")

// ticketIDPattern admits the ids whose lowercase form is a branch segment
// run.yml's pr job accepts: alphanumeric words joined by single hyphens.
var ticketIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

var ticketSizes = map[string]bool{"S": true, "M": true, "L": true}

// Ticket is one unit of work handed to the build agent.
type Ticket struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Size    string `json:"size"`
	SizedBy string `json:"sized_by,omitempty"`
	Body    string `json:"body"`
}

// Validate reports whether the ticket has an id, a one-line title, a size of S, M
// or L and a body, each within its limit.
func (t Ticket) Validate() error {
	switch {
	case len(t.ID) > maxTicketIDBytes || !ticketIDPattern.MatchString(t.ID):
		return fmt.Errorf("%w: id %q is not a branch-safe ticket id", ErrTicketInvalid, truncate(t.ID, logErrorLimit))
	case strings.TrimSpace(t.Title) == "" || len(t.Title) > maxTicketTitleBytes || !printable(t.Title):
		return fmt.Errorf("%w: title is empty, over %d bytes or not one printable line", ErrTicketInvalid, maxTicketTitleBytes)
	case !ticketSizes[t.Size]:
		return fmt.Errorf("%w: size %q is not S, M or L", ErrTicketInvalid, truncate(t.Size, logErrorLimit))
	case len(t.SizedBy) > maxSizedByBytes || !printable(t.SizedBy):
		return fmt.Errorf("%w: sized-by is over %d bytes or not one printable line", ErrTicketInvalid, maxSizedByBytes)
	case strings.TrimSpace(t.Body) == "" || len(t.Body) > maxTicketBodyBytes || !utf8.ValidString(t.Body):
		return fmt.Errorf("%w: body is empty, over %d bytes or not UTF-8", ErrTicketInvalid, maxTicketBodyBytes)
	}
	if raw, err := t.Encode(); err != nil || len(raw) > maxEncodedTicketBytes {
		return fmt.Errorf("%w: encoded ticket is over %d bytes", ErrTicketInvalid, maxEncodedTicketBytes)
	}
	return nil
}

func printable(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// BranchSegment is the ticket id as the run's branch name carries it.
func (t Ticket) BranchSegment() string {
	return strings.ToLower(t.ID)
}

// Text renders the ticket as it appears at the end of the prompt.
func (t Ticket) Text() string {
	return "## " + t.ID + ": " + t.Title + " (size " + t.Size + ")\n\n" + t.Body
}

// Encode renders the ticket as one line of JSON, leaving HTML characters unescaped.
func (t Ticket) Encode() (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(t); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// ParseTicket decodes a ticket encoded by Encode and validates it.
func ParseTicket(raw string) (Ticket, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var t Ticket
	if err := dec.Decode(&t); err != nil {
		return Ticket{}, fmt.Errorf("%w: decoding: %w", ErrTicketInvalid, err)
	}
	if dec.More() {
		return Ticket{}, fmt.Errorf("%w: trailing data", ErrTicketInvalid)
	}
	if err := t.Validate(); err != nil {
		return Ticket{}, err
	}
	return t, nil
}
