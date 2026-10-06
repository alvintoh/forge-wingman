package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRunURL = "https://github.com/o/r/actions/runs/42"

func repoTemplate(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", DefaultPRTemplate))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPRBodyRendersTheRepositoryTemplate(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**Ticket:** ref ABC-12\n",
		"-->\n`ABC-12`: " + testTicket.Title + "\n",
		"| Check | What it proves | Result |\n|---|---|---|\n| `make check` |",
		"the check job reports every gate passing, by the branch's own config: not proof | ✅ |",
		"## Notes\nBuilt unattended by forge-wingman. Run: " + testRunURL,
		"```\n" + testTicket.Body + "\n```\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"<TEAM-n>", "closes", "AC1 · <short name>", "<other check>", "## Screenshots", "| ❌ |"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body keeps %q:\n%s", unwanted, body)
		}
	}
}

func TestPRBodyNamesTheReportedFailedGate(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "test", testRunURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "| `make check` · test | the check job reports the test gate failing, by the branch's own config: not proof | ❌ |"
	if !strings.Contains(body, want) {
		t.Fatalf("body does not name the gate:\n%s", body)
	}
}

func TestPRBodyNamesTheLoopDetailInTheNotes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "checks: vet still failing after 3 round(s)", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## Notes\nBuilt unattended by forge-wingman. Run: "+testRunURL+
		"\n\n**Pre-PR loop:** kept this a draft — checks: vet still failing after 3 round(s)") {
		t.Fatalf("body does not name the loop detail:\n%s", body)
	}
}

func TestPRBodyOmitsTheLoopDetailWhenClean(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Pre-PR loop") {
		t.Fatalf("body names a loop detail nobody reported:\n%s", body)
	}
}

func TestPRBodyNamesTheOutOfPlanFilesInTheNotes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", []string{"a.go", "b.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## Notes\nBuilt unattended by forge-wingman. Run: "+testRunURL+
		"\n\n**Edited outside the plan:** a.go, b.go") {
		t.Fatalf("body does not name the out-of-plan files:\n%s", body)
	}
}

func TestPRBodyOmitsTheOutOfPlanLineWhenEmpty(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "outside the plan") {
		t.Fatalf("body names out-of-plan files nobody reported:\n%s", body)
	}
}

func TestPRBodyFencesTheTicketBodyAfterTheGateRow(t *testing.T) {
	tk := testTicket
	tk.Body = "<!-- hide the rest\n```\n`````\nstill fenced"
	body, err := PRBody(repoTemplate(t), tk, "", "test", testRunURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "``````\n"+tk.Body+"\n``````\n") {
		t.Fatalf("body is not fenced longer than its backtick runs:\n%s", body)
	}
	if gate, ticket := strings.Index(body, "| ❌ |"), strings.Index(body, "<!-- hide"); gate < 0 || gate > ticket {
		t.Fatalf("the gate row does not precede the ticket body:\n%s", body)
	}
}

func TestPRBodyRefusesATemplateItCannotFill(t *testing.T) {
	template := repoTemplate(t)
	for name, broken := range map[string]string{
		"no ticket line": strings.Replace(template, "**Ticket:** closes <TEAM-n>", "**Ticket:**", 1),
		"no summary":     strings.Replace(template, "## Summary", "## About", 1),
		"no table":       strings.Replace(template, "|---|---|---|", "", 1),
		"no notes":       strings.Replace(template, "## Notes", "## Other", 1),
	} {
		if _, err := PRBody(broken, testTicket, "", "", testRunURL, "", nil); !errors.Is(err, errTemplate) {
			t.Errorf("%s: err = %v, want errTemplate", name, err)
		}
	}
}

func TestPRBodyWritesTheSummaryOnce(t *testing.T) {
	tmpl := "**Ticket:** closes <TEAM-n>\n\n## Summary\n<!-- a -->\n<!-- b -->\n\n## Verification\n" +
		"| Check | What it proves | Result |\n|---|---|---|\n| x | y | ✅ |\n\n## Notes\n"
	body, err := PRBody(tmpl, testTicket, "", "", testRunURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(body, testTicket.Title); n != 1 {
		t.Fatalf("title appears %d times, want 1:\n%s", n, body)
	}
}

func TestPRBodySummarisesWithTheBuildsSummaryWhenSet(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "Adds the widget the runner needs.", "", testRunURL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "-->\nAdds the widget the runner needs.\n") || strings.Contains(body, "`ABC-12`: ") {
		t.Fatalf("body does not carry the build's summary as its own sentence:\n%s", body)
	}
}
