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
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
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
	body, err := PRBody(repoTemplate(t), testTicket, "", "test", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "| `make check` · test | the check job reports the test gate failing, by the branch's own config: not proof | ❌ |"
	if !strings.Contains(body, want) {
		t.Fatalf("body does not name the gate:\n%s", body)
	}
}

func TestPRBodyNamesTheLoopDetailInTheNotes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "checks: vet still failing after 3 round(s)", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## Notes\nBuilt unattended by forge-wingman. Run: "+testRunURL+
		"\n\n**Pre-PR loop:** kept this a draft — checks\\: vet still failing after 3 round\\(s\\)") {
		t.Fatalf("body does not name the loop detail:\n%s", body)
	}
}

func TestPRBodyOmitsTheLoopDetailWhenClean(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Pre-PR loop") {
		t.Fatalf("body names a loop detail nobody reported:\n%s", body)
	}
}

// TestPRBodyNamesTheRunWhosePlanTheseFilesTouch covers the S-overlap draft's
// own wording: the draft has to name the run it waits for, or the owner cannot
// tell which two pull requests to read together.
func TestPRBodyNamesTheRunWhosePlanTheseFilesTouch(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "FRG-79", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "\n\n**Concurrent run:** kept this a draft — `FRG-79`"+
		" plans to write some of these files\n") {
		t.Fatalf("body does not name the concurrent run:\n%s", body)
	}
}

func TestPRBodyNamesTheOutOfPlanFilesInTheNotes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", []string{"a.go", "b.go"}, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## Notes\nBuilt unattended by forge-wingman. Run: "+testRunURL+
		"\n\n**Edited outside the plan:** `a.go`, `b.go`") {
		t.Fatalf("body does not name the out-of-plan files:\n%s", body)
	}
}

func TestPRBodyRendersOutOfPlanFilesAsInertCode(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", []string{"x\n\n@someone review", "a`b"}, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Edited outside the plan:** `x\\n\\n@someone review`, ``a`b``\n") {
		t.Fatalf("out-of-plan files not rendered as single-line code spans:\n%s", body)
	}
}

func TestPRBodyRendersModelWrittenTextLiterally(t *testing.T) {
	summary := "Adds it. @someone ![x](https://evil.example/?d=1)"
	loop := "review asks @org/team to see [here](https://evil.example)\n# Heading"
	body, err := PRBody(repoTemplate(t), testTicket, summary, "", testRunURL, loop, "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, live := range []string{"@someone", "@org/team", "![", "](", "\n# Heading"} {
		if strings.Contains(body, live) {
			t.Errorf("model-written %q is still live markdown", live)
		}
	}
	for _, want := range []string{`\@` + "\u200bsomeone", `\!\[x\]\(https\:`, `\# Heading`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks the escaped %q", want)
		}
	}
}

// TestPRBodyListsThePreOpenReviewFindings is what a reader of the pull request
// sees of the pre-open review: each finding it made and how the run left it,
// rendered so neither can act as markdown.
func TestPRBodyListsThePreOpenReviewFindings(t *testing.T) {
	review := []ReviewFinding{
		{Finding: "guard at a.go:4 can be removed and every test in . still passes", Resolution: "fixed in round 2"},
		{Finding: "the retry never bounds its attempts @team", Resolution: "still open"},
	}
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, review, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**Pre-open review:** every finding it made, and how the run left it",
		"- guard at a\\.go\\:4 can be removed and every test in \\. still passes — fixed in round 2",
		"- the retry never bounds its attempts \\@\u200bteam — still open",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestPRBodyOmitsTheOutOfPlanLineWhenEmpty(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
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
	body, err := PRBody(repoTemplate(t), tk, "", "test", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
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
		if _, err := PRBody(broken, testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false); !errors.Is(err, errTemplate) {
			t.Errorf("%s: err = %v, want errTemplate", name, err)
		}
	}
}

func TestPRBodyWritesTheSummaryOnce(t *testing.T) {
	tmpl := "**Ticket:** closes <TEAM-n>\n\n## Summary\n<!-- a -->\n<!-- b -->\n\n## Verification\n" +
		"| Check | What it proves | Result |\n|---|---|---|\n| x | y | ✅ |\n\n## Notes\n"
	body, err := PRBody(tmpl, testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(body, testTicket.Title); n != 1 {
		t.Fatalf("title appears %d times, want 1:\n%s", n, body)
	}
}

func TestPRBodySummarisesWithTheBuildsSummaryWhenSet(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "Adds the widget the runner needs.", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "-->\nAdds the widget the runner needs\\.\n") || strings.Contains(body, "`ABC-12`: ") {
		t.Fatalf("body does not carry the build's summary as its own sentence:\n%s", body)
	}
}

func TestPRBodyNamesWhatHeldAFrontEndChange(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, []string{"area:fe", "web/"}, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Front-end change:** waits for the owner — matched `area:fe`, `web/`; run /fe-sweep before reading the diff\n") {
		t.Fatalf("body does not name the front-end marks:\n%s", body)
	}
}

func TestPRBodyOmitsTheFrontEndNoteWhenNothingHeldIt(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Front-end change") {
		t.Fatalf("body names a front-end hold nothing caused:\n%s", body)
	}
}

func TestPRBodyNamesAMissingPlanInTheNotes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, true, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**No recorded plan:** not eligible for auto-merge, so this waits for the owner") {
		t.Fatalf("body does not name the missing plan:\n%s", body)
	}
}

func TestPRBodyOmitsTheNoPlanNoteWhenPlanned(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "No recorded plan") {
		t.Fatalf("body names a missing plan nothing lacked:\n%s", body)
	}
}

func TestPRBodyListsEachProvisionalDecision(t *testing.T) {
	provisional := []Provisional{
		{Question: "Which flag?", Choice: "Reuse the flag", Cost: "no new config"},
		{Question: "Where @someone?", Choice: "a`b", Cost: "one more env var"},
	}
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, provisional, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**Provisional decisions:** auto-pilot took the recommended option on each, so this waits for the owner\n",
		"- Which flag\\? — took `Reuse the flag`, no new config\n",
		"- Where \\@\u200bsomeone\\? — took ``a`b``, one more env var\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestPRBodyOmitsProvisionalDecisionsWhenNoneWereTaken(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Provisional decisions") {
		t.Fatalf("body names provisional decisions:\n%s", body)
	}
}

func TestPRBodyNamesTheSupersededPR(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 141, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Supersedes #141: the owner chose a different option.") {
		t.Fatalf("body does not name the superseded PR:\n%s", body)
	}
}

func TestPRBodyNamesARetryAsTheReasonItSupersedes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 141, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Supersedes #141: the owner retried this run with a note.") {
		t.Fatalf("body does not name the retry:\n%s", body)
	}
	if strings.Contains(body, "chose a different option") {
		t.Fatalf("body names an option choice for a retry:\n%s", body)
	}
}

func TestPRBodyNamesNoRetryWithoutASupersededPR(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Supersedes") {
		t.Fatalf("body names a superseded PR for a retry that opened none:\n%s", body)
	}
}

func TestPRBodyNamesNoSupersededPRForAFirstAttempt(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Supersedes") {
		t.Fatalf("body names a superseded PR:\n%s", body)
	}
}
