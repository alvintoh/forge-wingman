package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// sizeTicket prints the size the sizer gives the ticket in -ticket-file and the
// signals that set it, one per line, so an author can see both before the run
// exists. The file need carry no size, which is what this answers. A size label
// overrides the sizer, so a ticket the label sized says which label did, on a
// further line. It reads that file alone: no store and no Actions environment.
func sizeTicket(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("size", flag.ContinueOnError)
	ticketFile := fs.String("ticket-file", "", "path to the ticket file the ticket subcommand wrote")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ticketFile == "" {
		return errors.New("no -ticket-file path to size")
	}
	raw, err := os.ReadFile(*ticketFile)
	if err != nil {
		return fmt.Errorf("reading the ticket: %w", err)
	}
	t, err := runner.ParseTicketDraft(string(raw))
	if err != nil {
		return fmt.Errorf("reading the ticket %s: %w", *ticketFile, err)
	}
	size, reason := dispatcher.SignalSize(t.Title, t.Body)
	lines := []string{size, reason}
	if t.SizedBy == dispatcher.SizedByLabel && t.Size != size {
		lines = append(lines, "the "+dispatcher.SizePrefix+t.Size+" label sizes the run, not the sizer")
	}
	if _, err := fmt.Fprintln(out, strings.Join(lines, "\n")); err != nil {
		return fmt.Errorf("writing the size: %w", err)
	}
	return nil
}
