package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildPrompt(t *testing.T) {
	ticket := Ticket{ID: "t-1", Title: "feat: the thing", Size: "S", Body: "Do the thing."}

	t.Run("projection verbatim first, ticket last", func(t *testing.T) {
		projection := "# Rules\n\nbe careful\n\n# The ticket\n\n" + ticketSentinel + "\n"
		got, err := BuildPrompt(projection, ticket)
		if err != nil {
			t.Fatal(err)
		}
		want := "# Rules\n\nbe careful\n\n# The ticket\n\n## t-1: feat: the thing (size S)\n\nDo the thing.\n"
		if got != want {
			t.Fatalf("prompt = %q, want %q", got, want)
		}
		if !strings.HasPrefix(got, projection[:strings.Index(projection, ticketSentinel)]) {
			t.Fatal("projection is not the prompt's prefix")
		}
		if !strings.HasSuffix(strings.TrimSpace(got), ticket.Body) {
			t.Fatal("ticket is not last")
		}
	})

	for _, tt := range []struct {
		name       string
		projection string
	}{
		{"no sentinel", "# Rules\n"},
		{"nothing before the sentinel", ticketSentinel + "\n"},
		{"only whitespace before the sentinel", " \n\t" + ticketSentinel + "\n"},
		{"two sentinels", ticketSentinel + "\n" + ticketSentinel + "\n"},
		{"content after the sentinel", "# Rules\n" + ticketSentinel + "\nmore rules\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BuildPrompt(tt.projection, ticket); !errors.Is(err, errSentinel) {
				t.Fatalf("err = %v, want errSentinel", err)
			}
		})
	}
}
