package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestRenderPromptParts(t *testing.T) {
	ticket := Ticket{ID: "t-1", Title: "feat: the thing", Size: "S", Body: "Do the thing."}

	t.Run("rules head first, ticket last", func(t *testing.T) {
		projection := "# Rules\n\nbe careful\n\n# The ticket\n\n" + ticketSentinel + "\n"
		rules, prompt, err := RenderPromptParts(projection, ticket)
		if err != nil {
			t.Fatal(err)
		}
		if want := "# Rules\n\nbe careful\n\n# The ticket\n\n"; rules != want {
			t.Fatalf("rules = %q, want the projection's head %q", rules, want)
		}
		if want := ticket.Text() + "\n"; prompt != want {
			t.Fatalf("prompt = %q, want the ticket %q", prompt, want)
		}
		if !strings.HasSuffix(strings.TrimSpace(rules+prompt), ticket.Body) {
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
			if _, _, err := RenderPromptParts(tt.projection, ticket); !errors.Is(err, errSentinel) {
				t.Fatalf("err = %v, want errSentinel", err)
			}
		})
	}
}
