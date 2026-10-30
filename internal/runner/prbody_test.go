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

// TestScreenshotsSectionRendersTheRoutesByBand is AC1's table: a row per route,
// the s, m, l and xl columns in that order, and a 200px image in each cell whose
// source is the shot's raw URL on the evidence branch.
func TestScreenshotsSectionRendersTheRoutesByBand(t *testing.T) {
	shots := []Screenshot{
		{Route: "/checkout", Band: "s", Path: "pr-12/checkout-s.png"},
		{Route: "/checkout", Band: "xl", Path: "pr-12/checkout-xl.png"},
		{Route: "/", Band: "m", Path: "pr-12/root-m.png"},
	}
	section := ScreenshotsSection("o/r", "abc1234", shots)
	sCell := `<img width="200" alt="/checkout at s" src="https://raw.githubusercontent.com/o/r/evidence/pr-12/checkout-s.png">`
	xlCell := `<img width="200" alt="/checkout at xl" src="https://raw.githubusercontent.com/o/r/evidence/pr-12/checkout-xl.png">`
	mCell := `<img width="200" alt="/ at m" src="https://raw.githubusercontent.com/o/r/evidence/pr-12/root-m.png">`
	for _, want := range []string{
		"## Screenshots (fixture data) · `abc1234`\n\n",
		"| Route | s | m | l | xl |\n|---|---|---|---|---|\n",
		"| `/checkout` | " + sCell + " |  |  | " + xlCell + " |\n",
		"| `/` |  | " + mCell + " |  |  |\n",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("section lacks %q:\n%s", want, section)
		}
	}
	if n := strings.Count(section, `<img width="200"`); n != len(shots) {
		t.Errorf("section holds %d images, want %d:\n%s", n, len(shots), section)
	}
	if strings.Index(section, "| `/` |") > strings.Index(section, "| `/checkout` |") {
		t.Errorf("section does not order the routes by name:\n%s", section)
	}
}

// TestScreenshotsSectionSaysTheRunCapturedNone is AC3: a run with no artifact
// still gets a section, saying so in one line rather than an empty table.
func TestScreenshotsSectionSaysTheRunCapturedNone(t *testing.T) {
	want := "## Screenshots (fixture data)\n\nNo screenshots: this run captured none.\n"
	if got := ScreenshotsSection("o/r", "abc1234", nil); got != want {
		t.Fatalf("section = %q, want %q", got, want)
	}
}

// TestWithScreenshotsReplacesTheSectionInPlace is AC2: a push re-renders the
// body for the new sha, and the section it finds is replaced rather than a
// second one added.
func TestWithScreenshotsReplacesTheSectionInPlace(t *testing.T) {
	body := "## Summary\n\nAdds the widget.\n\n" +
		"## Screenshots (fixture data) · `old1234`\n\n" +
		"| Route | s | m | l | xl |\n|---|---|---|---|---|\n" +
		"| `/stale` | ![stale](https://example.invalid/stale.png) |  |  |  |\n\n" +
		"## Notes\n\nBuilt unattended.\n"
	section := ScreenshotsSection("o/r", "new5678", []Screenshot{{Route: "/checkout", Band: "s", Path: "pr-12/checkout-s.png"}})
	got := WithScreenshots(body, section)
	if n := strings.Count(got, "## Screenshots"); n != 1 {
		t.Fatalf("body holds %d screenshots sections, want 1:\n%s", n, got)
	}
	for _, unwanted := range []string{"old1234", "stale"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("body keeps the previous section's %q:\n%s", unwanted, got)
		}
	}
	for _, want := range []string{
		"## Summary\n\nAdds the widget.",
		"## Screenshots (fixture data) · `new5678`",
		"pr-12/checkout-s.png",
		"## Notes\n\nBuilt unattended.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("body lacks %q:\n%s", want, got)
		}
	}
}

func TestWithScreenshotsInsertsTheSectionBeforeTheNotes(t *testing.T) {
	body := WithScreenshots("## Summary\n\nAdds the widget.\n\n## Notes\n\nBuilt unattended.\n",
		ScreenshotsSection("o/r", "abc1234", []Screenshot{{Route: "/checkout", Band: "s", Path: "pr-12/checkout-s.png"}}))
	if n := strings.Count(body, "## Screenshots"); n != 1 {
		t.Fatalf("body holds %d screenshots sections, want 1:\n%s", n, body)
	}
	if strings.Index(body, "## Screenshots") > strings.Index(body, "## Notes") {
		t.Fatalf("body puts the section after the notes:\n%s", body)
	}
	if !strings.HasSuffix(body, "Built unattended.\n") {
		t.Fatalf("body changes the notes:\n%s", body)
	}
}
