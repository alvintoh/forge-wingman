package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// sevenCriteria is the description whose seven plain criteria size a ticket L.
var sevenCriteria = "## Acceptance criteria\n\n" + strings.Repeat("1. The heading reads the chosen name.\n", 7)

// ticketFile writes tk as the ticket subcommand does and returns its path.
func ticketFile(t *testing.T, tk runner.Ticket) string {
	t.Helper()
	raw, err := tk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ticket.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSizePrintsTheSizersSizeNotTheTicketsOwn pins the file's own size against
// the sizer's: the ticket is filed as S and its seven criteria size it L.
func TestSizePrintsTheSizersSizeNotTheTicketsOwn(t *testing.T) {
	tk := runner.Ticket{ID: "FRG-91", Title: "[BE] Move the store", Size: "S", Body: sevenCriteria}
	var out bytes.Buffer
	if err := sizeTicket(&out, []string{"-ticket-file", ticketFile(t, tk)}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "L\n7 acceptance criteria\n"; got != want {
		t.Fatalf("size printed %q, want %q", got, want)
	}
}

// TestSizeNamesTheSizeLabelThatOverridesTheSizer covers the ticket a label
// sized: the admission path uses the label over the sizer, so the command says
// which label did.
func TestSizeNamesTheSizeLabelThatOverridesTheSizer(t *testing.T) {
	tk := runner.Ticket{ID: "FRG-91", Title: "[BE] Move the store", Size: "M", SizedBy: dispatcher.SizedByLabel, Body: sevenCriteria}
	var out bytes.Buffer
	if err := sizeTicket(&out, []string{"-ticket-file", ticketFile(t, tk)}); err != nil {
		t.Fatal(err)
	}
	want := "L\n7 acceptance criteria\nthe size:M label sizes the run, not the sizer\n"
	if got := out.String(); got != want {
		t.Fatalf("size printed %q, want %q", got, want)
	}
}

// TestSizePrintsLAndTheSizersReasonWithNoAcceptanceCriteria covers the ticket
// the sizer has nothing to count, which it sizes L.
func TestSizePrintsLAndTheSizersReasonWithNoAcceptanceCriteria(t *testing.T) {
	tk := runner.Ticket{ID: "FRG-92", Title: "[FE] Rename the heading", Size: "S", Body: "## Outcome\n\nThe heading reads the chosen name.\n"}
	var out bytes.Buffer
	if err := sizeTicket(&out, []string{"-ticket-file", ticketFile(t, tk)}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "L\nno acceptance criteria to count\n"; got != want {
		t.Fatalf("size printed %q, want %q", got, want)
	}
}

// TestSizeAcceptsATicketWithNoSizeYet covers the file an author writes before
// delegating: the size is the answer this command gives, so the file need not
// carry one.
func TestSizeAcceptsATicketWithNoSizeYet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticket.json")
	raw := `{"id":"FRG-94","title":"[FE] Rename the heading","body":"## Acceptance criteria\n\n1. One.\n2. Two.\n"}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := sizeTicket(&out, []string{"-ticket-file", path}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "S\n2 acceptance criteria\n"; got != want {
		t.Fatalf("size printed %q, want %q", got, want)
	}
}

// TestSizeFailsNamingWhatItCannotRead covers each way the ticket file is
// unusable, each naming what was wrong with it.
func TestSizeFailsNamingWhatItCannotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	unparsable := filepath.Join(t.TempDir(), "unparsable.json")
	if err := os.WriteFile(unparsable, []byte(`{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	// An invalid size is not a failure here, since the size is what the command
	// answers and the file need not carry one; an empty body still is.
	invalid := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"id":"FRG-95","title":"t","size":"S","body":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	type failure struct {
		name     string
		args     []string
		want     string
		notExist bool
	}
	for _, tt := range []failure{
		{"an undefined flag", []string{"-nope"}, "nope", false},
		{"no path given", nil, "no -ticket-file path", false},
		{"a file that is missing", []string{"-ticket-file", missing}, missing, true},
		{"a file that does not parse", []string{"-ticket-file", unparsable}, unparsable, false},
		{"a ticket that is invalid", []string{"-ticket-file", invalid}, invalid, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := sizeTicket(&out, tt.args)
			if err == nil {
				t.Fatalf("size succeeded and printed %q", out.String())
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to name %s", err, tt.want)
			}
			if tt.notExist && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("err = %v, want it to report the file is absent", err)
			}
			if code := exitCode(err); code == 0 {
				t.Fatalf("exit code = 0 for %v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("size printed %q on a failure", out.String())
			}
		})
	}
}

// brokenWriter fails every write, so the verdict cannot reach the caller.
type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("no space") }

// TestSizeFailsWhenItCannotWriteItsVerdict covers a stdout that fails, which
// must not read as a printed size.
func TestSizeFailsWhenItCannotWriteItsVerdict(t *testing.T) {
	tk := runner.Ticket{ID: "FRG-96", Title: "[FE] Rename the heading", Size: "S", Body: "## Acceptance criteria\n\n1. One.\n"}
	err := sizeTicket(brokenWriter{}, []string{"-ticket-file", ticketFile(t, tk)})
	if err == nil || !strings.Contains(err.Error(), "writing the size") {
		t.Fatalf("err = %v, want a write failure", err)
	}
}

// TestSizeReadsOnlyTheFileItIsGivenAndWritesNothing runs the command with no
// Actions environment, failing the test if it reads one, then checks the ticket
// file is untouched and nothing else appeared beside it.
func TestSizeReadsOnlyTheFileItIsGivenAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	tk := runner.Ticket{ID: "FRG-93", Title: "[FE] Rename the heading", Size: "S", Body: "## Acceptance criteria\n\n1. One.\n2. Two.\n"}
	raw, err := tk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ticket.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	getenv := func(k string) string {
		t.Fatalf("size read the environment variable %s", k)
		return ""
	}
	if err := run(context.Background(), logger, []string{"size", "-ticket-file", path}, getenv); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != raw {
		t.Fatalf("the ticket file changed to %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "ticket.json" {
		t.Fatalf("the directory holds %d entries, want the ticket alone", len(entries))
	}
}
