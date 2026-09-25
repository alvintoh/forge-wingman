package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestTicketValidateRejects(t *testing.T) {
	for name, mutate := range map[string]func(*Ticket){
		"no id":                  func(tk *Ticket) { tk.ID = "" },
		"an id with a slash":     func(tk *Ticket) { tk.ID = "ABC/12" },
		"an id with a space":     func(tk *Ticket) { tk.ID = "ABC 12" },
		"an id with a dot":       func(tk *Ticket) { tk.ID = "ABC.12" },
		"a leading hyphen":       func(tk *Ticket) { tk.ID = "-ABC-12" },
		"a trailing hyphen":      func(tk *Ticket) { tk.ID = "ABC-12-" },
		"a double hyphen":        func(tk *Ticket) { tk.ID = "ABC--12" },
		"an id over the limit":   func(tk *Ticket) { tk.ID = strings.Repeat("a", maxTicketIDBytes+1) },
		"no title":               func(tk *Ticket) { tk.Title = " " },
		"a title over two lines": func(tk *Ticket) { tk.Title = "a\nb" },
		"a title over the limit": func(tk *Ticket) { tk.Title = strings.Repeat("a", maxTicketTitleBytes+1) },
		"an unknown size":        func(tk *Ticket) { tk.Size = "XL" },
		"a sized-by over lines":  func(tk *Ticket) { tk.SizedBy = "a\nb" },
		"a sized-by over the limit": func(tk *Ticket) {
			tk.SizedBy = strings.Repeat("a", maxSizedByBytes+1)
		},
		"no body":                 func(tk *Ticket) { tk.Body = "\n" },
		"a body over the limit":   func(tk *Ticket) { tk.Body = strings.Repeat("a", maxTicketBodyBytes+1) },
		"a body that is not UTF8": func(tk *Ticket) { tk.Body = "a\xffb" },
	} {
		t.Run(name, func(t *testing.T) {
			tk := testTicket
			mutate(&tk)
			if err := tk.Validate(); !errors.Is(err, ErrTicketInvalid) {
				t.Fatalf("err = %v, want ErrTicketInvalid", err)
			}
		})
	}
}

func TestTicketBranchSegmentIsTheLowercaseID(t *testing.T) {
	if got := (Ticket{ID: "ABC-12"}).BranchSegment(); got != "abc-12" {
		t.Fatalf("segment = %q", got)
	}
}

func TestTicketTextCarriesTheTitle(t *testing.T) {
	if got := testTicket.Text(); got != "## ABC-12: feat(x): add a file (size S)\n\nAdd a file." {
		t.Fatalf("text = %q", got)
	}
}

func TestTicketValidateBoundsTheEncodedTicket(t *testing.T) {
	tk := testTicket
	tk.Body = strings.Repeat("\x01", maxTicketBodyBytes)
	if len(tk.Body) > maxTicketBodyBytes {
		t.Fatal("body over the raw limit")
	}
	if err := tk.Validate(); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("err = %v, want the encoded length refused", err)
	}
}

func TestParseTicketRoundTripsEncode(t *testing.T) {
	tk := testTicket
	tk.Body = "Line one.\nLine two, with <b>&</b>."
	raw, err := tk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(raw, "\r\n") || !strings.Contains(raw, "<b>&</b>") {
		t.Fatalf("encoded ticket spans lines: %q", raw)
	}
	got, err := ParseTicket(raw)
	if err != nil || got != tk {
		t.Fatalf("ticket %+v, err %v", got, err)
	}
	for name, raw := range map[string]string{
		"empty":         "",
		"unknown field": strings.Replace(raw, `{"id"`, `{"extra":1,"id"`, 1),
		"trailing data": raw + raw,
		"invalid":       `{"id":"ABC-12"}`,
	} {
		if _, err := ParseTicket(raw); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: err = %v, want ErrTicketInvalid", name, err)
		}
	}
}
