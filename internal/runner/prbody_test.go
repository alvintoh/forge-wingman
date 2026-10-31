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
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**Ticket:** ref ABC-12\n",
		"-->\n`ABC-12`: " + testTicket.Title + "\n",
		"| Check | What it proves | Result |\n|---|---|---|\n| `make check` |",
		"the check job reports every gate passing, by the branch's own config: not proof | ✅ |",
		"## Screenshots (fixture data)\nNo screenshots: this run captured none.\n",
		"## Notes\nBuilt unattended by forge-wingman. Run: " + testRunURL,
		"```\n" + testTicket.Body + "\n```\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"<TEAM-n>", "closes", "AC1 · <short name>", "<other check>", "FRONTEND changes only", "| ❌ |"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body keeps %q:\n%s", unwanted, body)
		}
	}
}

func TestPRBodyNamesTheReportedFailedGate(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "test", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "| `make check` · test | the check job reports the test gate failing, by the branch's own config: not proof | ❌ |"
	if !strings.Contains(body, want) {
		t.Fatalf("body does not name the gate:\n%s", body)
	}
}

func TestPRBodyNamesTheLoopDetailInTheNotes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "checks: vet still failing after 3 round(s)", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## Notes\nBuilt unattended by forge-wingman. Run: "+testRunURL+
		"\n\n**Pre-PR loop:** kept this a draft — checks\\: vet still failing after 3 round\\(s\\)") {
		t.Fatalf("body does not name the loop detail:\n%s", body)
	}
}

func TestPRBodyOmitsTheLoopDetailWhenClean(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
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
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "FRG-79", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "\n\n**Concurrent run:** kept this a draft — `FRG-79`"+
		" plans to write some of these files\n") {
		t.Fatalf("body does not name the concurrent run:\n%s", body)
	}
}

func TestPRBodyNamesTheOutOfPlanFilesInTheNotes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", []string{"a.go", "b.go"}, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## Notes\nBuilt unattended by forge-wingman. Run: "+testRunURL+
		"\n\n**Edited outside the plan:** `a.go`, `b.go`") {
		t.Fatalf("body does not name the out-of-plan files:\n%s", body)
	}
}

func TestPRBodyRendersOutOfPlanFilesAsInertCode(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", []string{"x\n\n@someone review", "a`b"}, false, nil, false, nil, nil, nil, 0, false)
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
	body, err := PRBody(repoTemplate(t), testTicket, summary, "", testRunURL, loop, "", nil, false, nil, false, nil, nil, nil, 0, false)
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
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, review, nil, nil, 0, false)
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

// TestPRBodyReportsTheSecondFixRoundsCost is the one extra round's price in
// the notes, so the owner sees what the round spent on the first run that takes
// one; a run that never took one says nothing about it.
func TestPRBodyReportsTheSecondFixRoundsCost(t *testing.T) {
	cost := &FixRoundCost{Round: 3, Tokens: Usage{Input: 40, Output: 10}, DurationMS: 372000}
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, cost, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "**Second fix round:** round 3 cost 40 input and 10 output tokens over 6m12s, inside the review stage's budget"; !strings.Contains(body, want) {
		t.Fatalf("body lacks %q:\n%s", want, body)
	}

	clean, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clean, "Second fix round") {
		t.Fatalf("body reports a second round nobody took:\n%s", clean)
	}
}

func TestPRBodyOmitsTheOutOfPlanLineWhenEmpty(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
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
	body, err := PRBody(repoTemplate(t), tk, "", "test", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
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
		if _, err := PRBody(broken, testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false); !errors.Is(err, errTemplate) {
			t.Errorf("%s: err = %v, want errTemplate", name, err)
		}
	}
}

func TestPRBodyWritesTheSummaryOnce(t *testing.T) {
	tmpl := "**Ticket:** closes <TEAM-n>\n\n## Summary\n<!-- a -->\n<!-- b -->\n\n## Verification\n" +
		"| Check | What it proves | Result |\n|---|---|---|\n| x | y | ✅ |\n\n## Notes\n"
	body, err := PRBody(tmpl, testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(body, testTicket.Title); n != 1 {
		t.Fatalf("title appears %d times, want 1:\n%s", n, body)
	}
}

func TestPRBodySummarisesWithTheBuildsSummaryWhenSet(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "Adds the widget the runner needs.", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "-->\nAdds the widget the runner needs\\.\n") || strings.Contains(body, "`ABC-12`: ") {
		t.Fatalf("body does not carry the build's summary as its own sentence:\n%s", body)
	}
}

func TestPRBodyNamesWhatHeldAFrontEndChange(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, []string{"area:fe", "web/"}, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Front-end change:** waits for the owner — matched `area:fe`, `web/`; run /fe-sweep before reading the diff\n") {
		t.Fatalf("body does not name the front-end marks:\n%s", body)
	}
}

func TestPRBodyOmitsTheFrontEndNoteWhenNothingHeldIt(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Front-end change") {
		t.Fatalf("body names a front-end hold nothing caused:\n%s", body)
	}
}

func TestPRBodyNamesAMissingPlanInTheNotes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, true, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**No recorded plan:** not eligible for auto-merge, so this waits for the owner") {
		t.Fatalf("body does not name the missing plan:\n%s", body)
	}
}

func TestPRBodyOmitsTheNoPlanNoteWhenPlanned(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
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
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, provisional, 0, false)
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
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Provisional decisions") {
		t.Fatalf("body names provisional decisions:\n%s", body)
	}
}

func TestPRBodyNamesTheSupersededPR(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 141, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Supersedes #141: the owner chose a different option.") {
		t.Fatalf("body does not name the superseded PR:\n%s", body)
	}
}

func TestPRBodyNamesARetryAsTheReasonItSupersedes(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 141, true)
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
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Supersedes") {
		t.Fatalf("body names a superseded PR for a retry that opened none:\n%s", body)
	}
}

func TestPRBodyNamesNoSupersededPRForAFirstAttempt(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Supersedes") {
		t.Fatalf("body names a superseded PR:\n%s", body)
	}
}

func TestPRBodySaysTheRunCapturedNoScreenshots(t *testing.T) {
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## Screenshots (fixture data)\nNo screenshots: this run captured none.\n\n## Notes\n") {
		t.Fatalf("body does not say the run captured none:\n%s", body)
	}
}

// evidenceOf is one screenshot at its place on the evidence branch.
func evidenceOf(route, width, p string) EvidenceFile {
	return EvidenceFile{Route: route, Width: width, Path: p}
}

func TestScreenshotsRendersOneImagePerViewport(t *testing.T) {
	ev := Evidence{SHA: testSHA, Files: []EvidenceFile{
		evidenceOf("/checkout", "s", "pr-7/"+testSHA+"/s/checkout.png"),
		evidenceOf("/home", "m", "pr-7/"+testSHA+"/m/index.png"),
		evidenceOf("/home", "s", "pr-7/"+testSHA+"/s/index.png"),
	}}
	raw := "https://raw.githubusercontent.com/o/r/evidence/pr-7/" + testSHA
	want := "## Screenshots (fixture data) — shot at `" + testSHA + "`\n\n" +
		"| Route | s | m | l | xl |\n|---|---|---|---|---|\n" +
		"| `/checkout` | <img width=\"200\" alt=\"/checkout at s\" src=\"" + raw + "/s/checkout.png\"> | — | — | — |\n" +
		"| `/home` | <img width=\"200\" alt=\"/home at s\" src=\"" + raw +
		"/s/index.png\"> | <img width=\"200\" alt=\"/home at m\" src=\"" + raw + "/m/index.png\"> | — | — |"
	if got := Screenshots(ev, "o/r"); got != want {
		t.Fatalf("section =\n%s\nwant:\n%s", got, want)
	}
}

func TestReplaceScreenshotsReplacesTheSectionForANewSHA(t *testing.T) {
	const first, second = "fedcba9876543210fedcba9876543210fedcba98", "89abcdef0123456789abcdef0123456789abcdef"
	body, err := PRBody(repoTemplate(t), testTicket, "", "", testRunURL, "", "", nil, false, nil, false, nil, nil, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{first, second} {
		ev := Evidence{SHA: sha, Files: []EvidenceFile{{Route: "/home", Width: "s", Path: "pr-7/" + sha + "/s/home.png"}}}
		if body, err = ReplaceScreenshots(body, Screenshots(ev, "o/r")); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(body, "## Screenshots (fixture data)"); n != 1 {
		t.Fatalf("body holds %d screenshots sections, want 1:\n%s", n, body)
	}
	if !strings.Contains(body, "shot at `"+second+"`") || strings.Contains(body, first) {
		t.Fatalf("body does not name the sha it was shot at:\n%s", body)
	}
	if !strings.Contains(body, "\n\n## Notes\nBuilt unattended") {
		t.Fatalf("replacing the section joined it to the notes:\n%s", body)
	}
}

func TestReplaceScreenshotsRefusesABodyWithNoSection(t *testing.T) {
	if _, err := ReplaceScreenshots("## Notes\nbuilt", "## Screenshots (fixture data)\nx"); !errors.Is(err, ErrNoScreenshotsSection) {
		t.Fatalf("err = %v, want ErrNoScreenshotsSection: a body must not gain an appended section", err)
	}
}

// The ticket body quotes the heading inside its fence in the notes.
func TestReplaceScreenshotsLeavesAFencedCopyAlone(t *testing.T) {
	body := "## Summary\n\nAdds it.\n\n## Screenshots (fixture data)\n" +
		"No screenshots: this run captured none.\n\n## Notes\n\nThe ticket as built:\n\n" +
		"```\n## Screenshots (fixture data)\nquoted\n```\n"
	section := "## Screenshots (fixture data) — shot at `" + testSHA + "`"
	got, err := ReplaceScreenshots(body, section)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "```\n## Screenshots (fixture data)\nquoted\n```") {
		t.Fatalf("the quoted copy did not survive verbatim:\n%s", got)
	}
	if n := strings.Count(got, "shot at `"+testSHA+"`"); n != 1 {
		t.Fatalf("body holds %d shot sections, want 1:\n%s", n, got)
	}
	if shot, notes := strings.Index(got, "shot at `"+testSHA+"`"), strings.Index(got, "## Notes"); shot > notes {
		t.Fatalf("the section landed after the notes:\n%s", got)
	}
}

func TestReplaceScreenshotsSkipsAFencedHeadingBeforeTheSection(t *testing.T) {
	body := "## Summary\n\n```\n## Screenshots (fixture data)\nquoted\n```\n\n" +
		"## Screenshots (fixture data)\nNo screenshots: this run captured none.\n\n## Notes\nbuilt\n"
	got, err := ReplaceScreenshots(body, "SECTION")
	if err != nil {
		t.Fatal(err)
	}
	want := "## Summary\n\n```\n## Screenshots (fixture data)\nquoted\n```\n\nSECTION\n\n## Notes\nbuilt\n"
	if got != want {
		t.Fatalf("body =\n%s\nwant:\n%s", got, want)
	}
}

func TestReplaceScreenshotsEndsAtTheNextHeadingOutsideAFence(t *testing.T) {
	body := "## Summary\n\n## Screenshots (fixture data)\nold\n\n```\n## Quoted\n```\n\n## Notes\nbuilt\n"
	got, err := ReplaceScreenshots(body, "SECTION")
	if err != nil {
		t.Fatal(err)
	}
	want := "## Summary\n\nSECTION\n\n## Notes\nbuilt\n"
	if got != want {
		t.Fatalf("body =\n%s\nwant:\n%s", got, want)
	}
}

func TestAppendScreenshotsSeedsTheSectionABodyLacks(t *testing.T) {
	body := "## Summary\n\nAdds it.\n\n## Notes\n\nBuilt unattended.\n"
	section := "## Screenshots (fixture data) — shot at `" + testSHA + "`"
	seeded := AppendScreenshots(body, section)
	if !strings.Contains(seeded, "## Notes\n\nBuilt unattended.\n\n"+section) {
		t.Fatalf("seeded body does not keep its notes and carry the section:\n%s", seeded)
	}
	if _, err := ReplaceScreenshots(seeded, section); err != nil {
		t.Fatalf("seeded body has no section to replace: %v", err)
	}
}

func TestAppendScreenshotsSeedsAnEmptyBody(t *testing.T) {
	section := "## Screenshots (fixture data) — shot at `" + testSHA + "`"
	if got, want := AppendScreenshots("", section), section+"\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
