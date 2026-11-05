package dispatcher

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// criteria is a description whose acceptance criteria are the n lines given.
func criteria(lines ...string) string {
	var b strings.Builder
	b.WriteString("## Outcome\n\nThe thing works.\n\n## Acceptance criteria\n\n")
	for i, l := range lines {
		fmt.Fprintf(&b, "%d. %s\n", i+1, l)
	}
	b.WriteString("\n## Out of scope\n\nThe dispatcher, the runner and the store's schema.\n")
	return b.String()
}

// plain is n criteria naming no risk and no module.
func plain(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "The heading reads the chosen name."
	}
	return lines
}

func TestSignalSize(t *testing.T) {
	for name, tt := range map[string]struct {
		title, body string
		want        string
	}{
		"small and plain":                       {"[FE] Rename the heading", criteria(plain(4)...), "S"},
		"small with one doubt":                  {"[SLICE] Rename the heading", criteria(plain(4)...), "M"},
		"small with two doubts":                 {"[BE] Rename the heading", criteria(append(plain(3), "The endpoint charges the budget.")...), ceilingSize},
		"medium and plain":                      {"[BE] Rename the heading", criteria(plain(6)...), "M"},
		"medium with one doubt":                 {"[BE] Rename the heading", criteria(append(plain(5), "The webhook answers 200.")...), ceilingSize},
		"many criteria":                         {"[BE] Rename the heading", criteria(plain(7)...), ceilingSize},
		"a risk named only in the title":        {"[BE] Store the migration", criteria(plain(4)...), "M"},
		"criteria naming two modules":           {"[BE] Rename the heading", criteria(append(plain(3), "The dispatcher and the runner agree.")...), "M"},
		"criteria naming one module":            {"[BE] Rename the heading", criteria(append(plain(3), "The dispatcher logs it.")...), "S"},
		"a title with no prefix":                {"Rename the heading", criteria(plain(4)...), "M"},
		"no acceptance criteria":                {"[FE] Rename the heading", "## Outcome\n\nThe heading reads the chosen name.\n", ceilingSize},
		"a heading with no criteria under it":   {"[FE] Rename the heading", "## Acceptance criteria\n\nSee the drawing.\n", ceilingSize},
		"criteria over the ceiling":             {"[FE] Rename the heading", criteria(plain(maxACs + 1)...), aboveCeiling},
		"criteria at the ceiling":               {"[FE] Rename the heading", criteria(plain(maxACs)...), ceilingSize},
		"bold heading and bulleted criteria":    {"[FE] Rename the heading", "**Acceptance criteria**\n- one\n- two\n**Out of scope.** The schema.\n", "S"},
		"risk words after the criteria section": {"[FE] Rename the heading", criteria(plain(4)...) + "\n## Notes\n\nThe API, its auth and its cost.\n", "S"},
		"a bare label and plus bullets":         {"[FE] Rename the heading", "Acceptance criteria:\n+ one\n+ two\n+ three\n+ four\n+ five\n**Out of scope.** The schema.\n", "M"},
		"an amendment note inside the criteria": {"[FE] Rename the heading",
			"## Acceptance criteria\n\n1. a\n2. b\n3. c\n\n**Updated (2026-10-15):** four more.\n\n4. d\n5. e\n6. f\n7. g\n", ceilingSize},
		"subheadings inside the criteria": {"[FE] Rename the heading",
			"## Acceptance criteria\n\n1. a\n2. b\n\n### Edge cases\n\n3. c\n4. d\n5. e\n6. f\n7. g\n\n## Out of scope\n\n- x\n", ceilingSize},
		"a bold line inside heading criteria": {"[FE] Rename the heading",
			"## Acceptance criteria\n\n1. a\n2. b\n\n**Given** a signed-in owner:\n\n3. c\n4. d\n5. e\n6. f\n7. g\n", ceilingSize},
		"an amendment note inside bold criteria": {"[FE] Rename the heading",
			"**Acceptance criteria**\n- a\n- b\n**Added (2026-10-15):** five more.\n- c\n- d\n- e\n- f\n- g\n", ceilingSize},
		"a heading closing bold criteria": {"[FE] Rename the heading",
			"**Acceptance criteria**\n- one\n\n## Notes\n\nThe dispatcher and the runner.\n", "S"},
		"modules named only in the title": {"[BE] Move the runner into the dispatcher", criteria(plain(4)...), "S"},
		"prose naming the criteria before them": {"[FE] Rename the heading",
			"## Outcome\n\nAcceptance criteria follow the PRD:\n- a\n- b\n\n## Acceptance criteria\n\n" + strings.Repeat("1. a\n", 7), ceilingSize},
	} {
		t.Run(name, func(t *testing.T) {
			got, reason := signalSize(tt.title, tt.body)
			if got != tt.want {
				t.Fatalf("signalSize = %s (%s), want %s", got, reason, tt.want)
			}
			if reason == "" {
				t.Fatal("no reason recorded")
			}
		})
	}
}

// sizedTicket is one closed ticket the owner sized by hand.
type sizedTicket struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Size  string `json:"size"`
}

func TestSignalSizeNeverSizesAHandSizedLTicketSmaller(t *testing.T) {
	raw, err := os.ReadFile("testdata/sized_tickets.json")
	if err != nil {
		t.Fatal(err)
	}
	var tickets []sizedTicket
	if err := json.Unmarshal(raw, &tickets); err != nil {
		t.Fatal(err)
	}
	if len(tickets) == 0 {
		t.Fatal("the corpus is empty")
	}
	order := []string{"S", "M", "L", aboveCeiling}
	matrix := map[string]map[string]int{}
	agree := 0
	for _, tk := range tickets {
		got, reason := signalSize(tk.Title, tk.Body)
		if matrix[tk.Size] == nil {
			matrix[tk.Size] = map[string]int{}
		}
		matrix[tk.Size][got]++
		if got == tk.Size {
			agree++
		}
		if tk.Size == "L" && got != "L" {
			t.Errorf("%s is hand-sized L and sized %s (%s)", tk.ID, got, reason)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "hand \\ sizer %4s %4s %4s %4s\n", "S", "M", "L", "XL")
	for _, hand := range order[:3] {
		fmt.Fprintf(&b, "%-13s", hand)
		for _, got := range order {
			fmt.Fprintf(&b, " %4d", matrix[hand][got])
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "agree %d of %d", agree, len(tickets))
	t.Logf("confusion matrix, %d hand-sized tickets:\n%s", len(tickets), b.String())
}
