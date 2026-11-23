package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alvintoh/forge-wingman/internal/runner"
)

const specialistModel = "opencode-go/claude-haiku-5-5"

// noSpecialists is a pass over no area, which runs the general review alone.
var noSpecialists = runner.SpecialistPass{}

// promptAgent is smokeAgent that also records every prompt it is given.
type promptAgent struct {
	smokeAgent
	prompts *[]string
	models  *[]string
}

func (a promptAgent) WithModel(model string) runner.Agent { a.model = model; return a }

func (a promptAgent) Run(ctx context.Context, dir, session, prompt, rules string, stdout, stderr io.Writer) error {
	*a.prompts = append(*a.prompts, prompt)
	*a.models = append(*a.models, a.model)
	return a.smokeAgent.Run(ctx, dir, session, prompt, rules, stdout, stderr)
}

func newPromptAgent(runs map[string]smokeRun) (promptAgent, *[]string, *[]string) {
	smoke, _ := newSmokeAgent(runs)
	prompts, models := &[]string{}, &[]string{}
	return promptAgent{smokeAgent: smoke, prompts: prompts, models: models}, prompts, models
}

// reviewPass is a pass over the configured areas whose deadline is an hour away.
func reviewPass() runner.SpecialistPass {
	return runner.SpecialistPass{Areas: runner.ReviewAreas(), Deadline: time.Now().Add(time.Hour)}
}

// reviewOf writes a review context whose diff is diff, runs the review and returns
// the verdict file's bytes.
func reviewOf(t *testing.T, agent runner.Agent, diff string) []byte {
	t.Helper()
	c := runner.PRReviewContext{PR: 7, HeadSHA: headSHA, Ticket: runner.Ticket{ID: "ABC-12", Title: "Add x", Body: "AC1"}, Diff: diff}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	contextPath, out := filepath.Join(dir, "context.json"), filepath.Join(dir, "verdict.json")
	if err := os.WriteFile(contextPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runPRReview(context.Background(), quietLogger(), agent, []string{museModel}, time.Minute, contextPath, out, "", nil, reviewPass()); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

const dataDiff = "diff --git a/internal/store/x.go b/internal/store/x.go\n--- a/internal/store/x.go\n+++ b/internal/store/x.go\n@@ -1 +1 @@\n-a\n+b\n"

func TestRunPRReviewRunsNoSpecialistForADiffTouchingNoArea(t *testing.T) {
	agent, prompts, models := newPromptAgent(map[string]smokeRun{museModel: reviewReply(tokensUsed, "x is missing")})
	written := reviewOf(t, agent, "+x")
	if !slices.Equal(*models, []string{museModel}) || len(*prompts) != 1 {
		t.Fatalf("calls = %v, want the general model alone", *models)
	}
	if strings.Contains(string(written), "specialists") {
		t.Fatalf("verdict = %s, want no specialists key", written)
	}
}

func TestRunPRReviewRunsATouchedAreaAfterTheGeneralReview(t *testing.T) {
	agent, prompts, models := newPromptAgent(map[string]smokeRun{
		museModel:       reviewReply(tokensUsed, "x is missing"),
		specialistModel: reviewReply(tokensUsed, "a stored record has no value for the new field"),
	})
	written := reviewOf(t, agent, dataDiff)
	if !slices.Equal(*models, []string{museModel, specialistModel}) {
		t.Fatalf("calls = %v, want the general model then the data specialist's", *models)
	}
	area := runner.ReviewAreas()[0]
	if got := (*prompts)[1]; !strings.HasPrefix(got, "You are reviewing a pull request.") || !strings.Contains(got, area.Prompt) || !strings.Contains(got, dataDiff) {
		t.Fatalf("the specialist's prompt = %q", got)
	}
	v, err := runner.ParsePRVerdict(written)
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != "findings" || v.Findings != "x is missing" || len(v.Specialists) != 1 {
		t.Fatalf("verdict = %+v", v)
	}
	if s := v.Specialists[0]; s.Name != "data" || s.Outcome != runner.SpecialistReviewed || s.Model != specialistModel ||
		s.Findings != "a stored record has no value for the new field" || s.Usage.Input != 1000 {
		t.Fatalf("specialist = %+v", s)
	}
}

func TestRunPRReviewKeepsTheVerdictWhenASpecialistFails(t *testing.T) {
	agent, _, models := newPromptAgent(map[string]smokeRun{
		museModel:       reviewReply(tokensUsed, ""),
		specialistModel: func(context.Context, string, io.Writer, io.Writer) error { return errors.New("exit status 1") },
	})
	v, err := runner.ParsePRVerdict(reviewOf(t, agent, dataDiff))
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != "clean" || len(*models) != 2 {
		t.Fatalf("verdict = %+v after calls %v", v, *models)
	}
	if s := v.Specialists[0]; s.Outcome != runner.SpecialistNotReviewed || s.Reason != runner.SpecialistReasonFailed || len(s.Attempts) != 1 {
		t.Fatalf("specialist = %+v", s)
	}
}

func TestPRReviewRefusesASpecialistModelListThatIsNotUsable(t *testing.T) {
	build := runner.DefaultModel()
	area := func(models ...string) []runner.ReviewArea {
		return []runner.ReviewArea{{Name: "infra", Models: []string{specialistModel}}, {Name: "data", Models: models}}
	}
	for name, tt := range map[string]struct {
		areas []runner.ReviewArea
		want  string
	}{
		"a build model":     {area(specialistModel, build), "build model"},
		"a repeated model":  {area(specialistModel, specialistModel), "listed twice"},
		"a second provider": {area(specialistModel, "other/model"), "not a opencode-go model"},
	} {
		t.Run(name, func(t *testing.T) {
			err := prReviewWith(context.Background(), quietLogger(), func(string) string { return "" },
				[]string{"-context", "c", "-out", "v", "-models", museModel}, tt.areas, time.Now)
			if err == nil || !strings.Contains(err.Error(), `review area "data"`) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q naming the area", err, tt.want)
			}
		})
	}
}

// TestSpecialistDeadlineLeavesTheJobRoomForItsSetupAndUpload: the deadline counts
// from the runner's start, so the job's setup before it (about 6 minutes) and the
// artifact upload after it must fit in what the review job's own limit leaves.
// That a specialist cannot outrun the deadline is the runner package's worst-case test.
func TestSpecialistDeadlineLeavesTheJobRoomForItsSetupAndUpload(t *testing.T) {
	const setupAndUpload = 10 * time.Minute
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "pr-review.yml"))
	if err != nil {
		t.Fatal(err)
	}
	_, review, ok := strings.Cut(string(b), "\n  review:\n")
	if !ok {
		t.Fatal("pr-review.yml has no review job")
	}
	m := regexp.MustCompile(`timeout-minutes: (\d+)`).FindStringSubmatch(review)
	if m == nil {
		t.Fatal("the review job has no timeout-minutes")
	}
	minutes, _ := strconv.Atoi(m[1])
	if limit := time.Duration(minutes) * time.Minute; runner.PRReviewDeadline+setupAndUpload >= limit {
		t.Fatalf("deadline %s plus %s for setup and upload is not under the job's %s", runner.PRReviewDeadline, setupAndUpload, limit)
	}
}

func TestSpecialistPassCountsTheDeadlineFromTheStartOfTheReview(t *testing.T) {
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	pass := specialistPass(runner.ReviewAreas(), started, func() time.Time { return started })
	if want := started.Add(25 * time.Minute); !pass.Deadline.Equal(want) || len(pass.Areas) != len(runner.ReviewAreas()) {
		t.Fatalf("deadline = %s, areas %d; want %s", pass.Deadline, len(pass.Areas), want)
	}
}

// TestRunPRReviewWritesTheVerdictWhenCancelledDuringASpecialist: a cancel that
// reaches a specialist still leaves the general verdict on disk, with the
// specialist not reviewed.
func TestRunPRReviewWritesTheVerdictWhenCancelledDuringASpecialist(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent, _, _ := newPromptAgent(map[string]smokeRun{
		museModel:       reviewReply(tokensUsed, "x is missing"),
		specialistModel: func(context.Context, string, io.Writer, io.Writer) error { cancel(); return errors.New("killed") },
	})
	c := runner.PRReviewContext{PR: 7, HeadSHA: headSHA, Ticket: runner.Ticket{ID: "ABC-12", Title: "Add x", Body: "AC1"}, Diff: dataDiff}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	contextPath, out := filepath.Join(dir, "context.json"), filepath.Join(dir, "verdict.json")
	if err := os.WriteFile(contextPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runPRReview(ctx, quietLogger(), agent, []string{museModel}, time.Minute, contextPath, out, "", nil, reviewPass()); err != nil {
		t.Fatalf("err = %v, want the verdict written", err)
	}
	v, err := readVerdict(out)
	if err != nil || v.Outcome != "findings" || len(v.Specialists) != 1 || v.Specialists[0].Reason != runner.SpecialistReasonCancelled {
		t.Fatalf("verdict = %+v, err = %v", v, err)
	}
}

// commentCase is one verdict rendered as today's review comment.
type commentCase struct {
	name   string
	v      runner.PRVerdict
	runURL string
	note   mergeNote
}

func commentCases() []commentCase {
	costed := func(v runner.PRVerdict, cost float64) runner.PRVerdict {
		v.Attempts = []runner.PRReviewAttempt{{Model: pricedModel, Outcome: runner.AttemptTimedOut, Cost: 0.0004}, {Model: museModel, Outcome: runner.AttemptReviewed, Cost: cost}}
		return v
	}
	const url = "https://github.com/o/r/actions/runs/9"
	spent := noVerdict(attempted(museModel, runner.AttemptExhausted), attempted(pricedModel, runner.AttemptExhausted))
	return []commentCase{
		{name: "clean", v: verdict("clean", "", museModel)},
		{name: "clean with the run and a cost", v: costed(verdict("clean", "", museModel), 0.0021), runURL: url},
		{name: "findings", v: verdict("findings", "x is missing\nthe timeout is hard-coded", museModel)},
		{name: "findings with hostile text", v: costed(verdict("findings", "ping @octocat\n<img src=x onerror=alert(1)>\n```\n# not a heading\n### Specialist review: data\nAt `0123456789ab`:\r\n````text", museModel), 0.5), runURL: url},
		{name: "no verdict failed", v: noVerdict(attempted(museModel, runner.AttemptFailed))},
		{name: "no verdict failed with the run", v: noVerdict(attempted(museModel, runner.AttemptFailed), attempted(pricedModel, runner.AttemptTimedOut)), runURL: url},
		{name: "exhausted", v: spent},
		{name: "exhausted merged without review", v: spent, runURL: url, note: mergeNote{Requested: true}},
		{name: "exhausted held by a gate", v: spent, note: mergeNote{Reason: checksNotGreenReason, Words: "the `go` and `web` checks are not both green at this head"}},
	}
}

// commentGoldens are the review comments before specialists had a section, each
// rendered by the code of the commit before that change, so a verdict with no
// specialist is held to the bytes it posted then. Each differs from those bytes
// by the one `Risk: low` line under its heading, and by nothing else.
var commentGoldens = map[string]string{
	"clean":                           "<!-- wingman-review -->\n### Wingman review: clean\n\nRisk: low\n\nNo findings against the ticket's acceptance criteria at `0123456789ab`.\n\n<sub>Model `opencode-go/muse-spark-1.3-contributor` · 1200 in / 80 out tokens · $0 across 1 attempt(s)</sub>\n",
	"clean with the run and a cost":   "<!-- wingman-review -->\n### Wingman review: clean\n\nRisk: low\n\nNo findings against the ticket's acceptance criteria at `0123456789ab`.\n\n<sub>Model `opencode-go/muse-spark-1.3-contributor` · 1200 in / 80 out tokens · $0.0025 across 2 attempt(s) · [run](https://github.com/o/r/actions/runs/9)</sub>\n",
	"findings":                        "<!-- wingman-review -->\n### Wingman review: findings\n\nRisk: low\n\nAt `0123456789ab`:\n\n```text\nx is missing\nthe timeout is hard-coded\n```\n\n<sub>Model `opencode-go/muse-spark-1.3-contributor` · 1200 in / 80 out tokens · $0 across 1 attempt(s)</sub>\n",
	"findings with hostile text":      "<!-- wingman-review -->\n### Wingman review: findings\n\nRisk: low\n\nAt `0123456789ab`:\n\n`````text\nping @\u200boctocat\n<img src=x onerror=alert(1)>\n```\n# not a heading\n### Specialist review: data\nAt `0123456789ab`:\n````text\n`````\n\n<sub>Model `opencode-go/muse-spark-1.3-contributor` · 1200 in / 80 out tokens · $0.5004 across 2 attempt(s) · [run](https://github.com/o/r/actions/runs/9)</sub>\n",
	"no verdict failed":               "<!-- wingman-review -->\n### Wingman review: no verdict: failed\n\nRisk: low\n\nNo review model returned a verdict at `0123456789ab`, so this pull request waits for the owner.\n\n- `opencode-go/muse-spark-1.3-contributor`: agent failed\n\n<sub>Model none · 0 in / 0 out tokens · $0 across 1 attempt(s)</sub>\n",
	"no verdict failed with the run":  "<!-- wingman-review -->\n### Wingman review: no verdict: failed\n\nRisk: low\n\nNo review model returned a verdict at `0123456789ab`, so this pull request waits for the owner.\n\n- `opencode-go/muse-spark-1.3-contributor`: agent failed\n- `opencode-go/deepseek-v4.1-flash`: timed out\n\n<sub>Model none · 0 in / 0 out tokens · $0 across 2 attempt(s) · [run](https://github.com/o/r/actions/runs/9)</sub>\n",
	"exhausted":                       "<!-- wingman-review -->\n### Wingman review: no verdict: allowance exhausted\n\nRisk: low\n\nNo review model returned a verdict at `0123456789ab`: every one reported its plan's allowance spent.\n\n- `opencode-go/muse-spark-1.3-contributor`: allowance exhausted\n- `opencode-go/deepseek-v4.1-flash`: allowance exhausted\n\n<sub>Model none · 0 in / 0 out tokens · $0 across 2 attempt(s)</sub>\n",
	"exhausted merged without review": "<!-- wingman-review -->\n### Wingman review: no verdict: allowance exhausted\n\nRisk: low\n\nNo review model returned a verdict at `0123456789ab`: every one reported its plan's allowance spent. This pull request is to be merged without review, because the `go` and `web` checks pass at this head.\n\n- `opencode-go/muse-spark-1.3-contributor`: allowance exhausted\n- `opencode-go/deepseek-v4.1-flash`: allowance exhausted\n\n<sub>Model none · 0 in / 0 out tokens · $0 across 2 attempt(s) · [run](https://github.com/o/r/actions/runs/9)</sub>\n",
	"exhausted held by a gate":        "<!-- wingman-review -->\n### Wingman review: no verdict: allowance exhausted\n\nRisk: low\n\nNo review model returned a verdict at `0123456789ab`: every one reported its plan's allowance spent. Auto-merge is not requested, so this pull request waits for the owner: the `go` and `web` checks are not both green at this head.\n\n- `opencode-go/muse-spark-1.3-contributor`: allowance exhausted\n- `opencode-go/deepseek-v4.1-flash`: allowance exhausted\n\n<sub>Model none · 0 in / 0 out tokens · $0 across 2 attempt(s)</sub>\n",
}

// planGoldens are what planPost made of the same verdicts before that change.
var planGoldens = map[string]struct {
	state, description string
	clean, exhausted   bool
}{
	"clean":                           {"success", "Clean: no findings from opencode-go/muse-spark-1.3-contributor", true, false},
	"clean with the run and a cost":   {"success", "Clean: no findings from opencode-go/muse-spark-1.3-contributor", true, false},
	"findings":                        {"failure", "Findings from opencode-go/muse-spark-1.3-contributor; see the review comment", false, false},
	"findings with hostile text":      {"failure", "Findings from opencode-go/muse-spark-1.3-contributor; see the review comment", false, false},
	"no verdict failed":               {"failure", "No verdict: failed; waits for the owner", false, false},
	"no verdict failed with the run":  {"failure", "No verdict: failed; waits for the owner", false, false},
	"exhausted":                       {"failure", "No verdict: allowance exhausted; waits for the owner", false, true},
	"exhausted merged without review": {"failure", "No verdict: allowance exhausted; waits for the owner", false, true},
	"exhausted held by a gate":        {"failure", "No verdict: allowance exhausted; waits for the owner", false, true},
}

// reviewedBy is a specialist that reviewed and reported findings, which may be
// empty.
func reviewedBy(name, findings string, cost float64) runner.SpecialistVerdict {
	return runner.SpecialistVerdict{Name: name, Outcome: runner.SpecialistReviewed, Findings: findings, Model: specialistModel, Priced: true,
		Usage: runner.Usage{Input: 1000, Output: 50}, Cost: cost,
		Attempts: []runner.PRReviewAttempt{{Model: specialistModel, Outcome: runner.AttemptReviewed, Cost: cost}}}
}

// missedBy is a specialist no model reviewed, for reason, after the given attempts.
func missedBy(name, reason string, attempts ...runner.PRReviewAttempt) runner.SpecialistVerdict {
	s := runner.SpecialistVerdict{Name: name, Outcome: runner.SpecialistNotReviewed, Reason: reason, Attempts: append([]runner.PRReviewAttempt{}, attempts...)}
	for _, a := range attempts {
		s.Cost += a.Cost
	}
	return s
}

// withSpecialists is v carrying specialists.
func withSpecialists(v runner.PRVerdict, specialists ...runner.SpecialistVerdict) runner.PRVerdict {
	v.Specialists = specialists
	return v
}

func TestAVerdictWithNoSpecialistPostsWhatItAlwaysDid(t *testing.T) {
	for _, c := range commentCases() {
		t.Run(c.name, func(t *testing.T) {
			want := commentGoldens[c.name]
			for label, v := range map[string]runner.PRVerdict{
				"none":          c.v,
				"an empty list": withSpecialists(c.v),
			} {
				if got := renderReviewComment(v, headSHA, c.runURL, c.note); got != want {
					t.Fatalf("with %s the comment is\n%q\nwant\n%q", label, got, want)
				}
			}
			plan, golden := planPost("", "success", c.v, nil, headSHA, c.runURL), planGoldens[c.name]
			if plan.status.State != golden.state || plan.status.Description != golden.description || plan.clean != golden.clean ||
				plan.exhausted != golden.exhausted {
				t.Fatalf("plan = %+v, want %+v", plan, golden)
			}
		})
	}
}

// TestPostIgnoresTheSpecialistsWithNoFindings: a low-risk specialist that found
// nothing adds its section and its cost to the comment and changes nothing else
// the post decides.
func TestPostIgnoresTheSpecialistsWithNoFindings(t *testing.T) {
	quiet := []runner.SpecialistVerdict{reviewedBy("data", "", 0.125)}
	for name, v := range map[string]runner.PRVerdict{
		"clean":      verdict("clean", "", museModel),
		"findings":   verdict("findings", "x is missing", museModel),
		"no verdict": noVerdict(attempted(museModel, runner.AttemptFailed)),
		"exhausted":  spentAllowance(),
	} {
		t.Run(name, func(t *testing.T) {
			got, want := planPost("", "success", withSpecialists(v, quiet...), nil, headSHA, ""), planPost("", "success", v, nil, headSHA, "")
			if got.status != want.status || got.clean != want.clean || got.exhausted != want.exhausted {
				t.Fatalf("plan = %+v, want %+v", got, want)
			}
			if !strings.Contains(got.comment, "### Specialist review: data\n") || got.comment == want.comment {
				t.Fatalf("comment = %q, want the sections added", got.comment)
			}
		})
	}
}

func TestRenderReviewCommentGivesEachSpecialistASectionInConfigurationOrder(t *testing.T) {
	v := withSpecialists(verdict("findings", "x is missing", museModel),
		missedBy("security", runner.SpecialistReasonTimedOut, runner.PRReviewAttempt{Model: specialistModel, Outcome: runner.AttemptTimedOut, Cost: 0.0625}),
		reviewedBy("data", "a stored record has no value for the new field\nthe index no longer serves the query", 0.125),
		missedBy("cloud", runner.SpecialistReasonDeadline),
		reviewedBy("infra", "", 0.25),
		reviewedBy("not-an-area", "left out", 1),
	)
	const want = "<!-- wingman-review -->\n" +
		"### Wingman review: findings\n\nRisk: high (findings from data; cloud ran in a high-risk area; security ran in a high-risk area; a specialist outside the review configuration ran)\n\nAt `0123456789ab`:\n\n```text\nx is missing\n```\n" +
		"\n### Specialist review: data\n\nAt `0123456789ab`:\n\n```text\na stored record has no value for the new field\nthe index no longer serves the query\n```\n" +
		"\n<sub>Model `" + specialistModel + "` · 1000 in / 50 out tokens · $0.125 across 1 attempt(s)</sub>\n" +
		"\n### Specialist review: infra\n\nNo findings in this area.\n" +
		"\n<sub>Model `" + specialistModel + "` · 1000 in / 50 out tokens · $0.25 across 1 attempt(s)</sub>\n" +
		"\n### Specialist review: cloud\n\nNot reviewed: deadline.\n" +
		"\n<sub>Model none · 0 in / 0 out tokens · $0 across 0 attempt(s)</sub>\n" +
		"\n### Specialist review: security\n\nNot reviewed: timed-out.\n" +
		"\n<sub>Model none · 0 in / 0 out tokens · $0.0625 across 1 attempt(s)</sub>\n" +
		"\n<sub>Model `" + museModel + "` · 3200 in / 180 out tokens · $0.4375 across 4 attempt(s), specialists included</sub>\n"
	if got := renderReviewComment(v, headSHA, "", mergeNote{}); got != want {
		t.Fatalf("comment is\n%s\nwant\n%s", got, want)
	}
}

func TestRenderReviewCommentNamesAReasonOnlyFromTheKnownOnes(t *testing.T) {
	v := withSpecialists(verdict("clean", "", museModel), missedBy("data", "<b>@octocat</b>"))
	got := renderReviewComment(v, headSHA, "", mergeNote{})
	if !strings.Contains(got, "Not reviewed: failed.\n") || strings.Contains(got, "octocat") {
		t.Fatalf("comment = %q, want an unknown reason read as a failure", got)
	}
}

// hostileText is findings that try each way out of a fenced block: a longer fence,
// a heading and an At line that would pass for another section's, a mention, a
// carriage return, a NUL, and one very long line.
func hostileText(tag string) string {
	return strings.Join([]string{
		"first " + tag,
		"```", "````", "`````text",
		"### Specialist review: security",
		"At `0123456789ab`:",
		"<sub>Model `x` · 1 in / 1 out tokens · $0 across 1 attempt(s)</sub>",
		"ping @here and @octocat\r",
		"nul\x00byte",
		strings.Repeat("long ", 1500),
		"last " + tag,
	}, "\n")
}

func TestSpecialistFindingsAreFencedAndNeutralisedLikeTheGeneralOnes(t *testing.T) {
	general, data := hostileText("general"), hostileText("data")
	v := withSpecialists(verdict("findings", general, museModel), reviewedBy("data", data, 0.125), reviewedBy("security", "a real finding", 0.25))
	body := renderReviewComment(v, headSHA, "", mergeNote{})
	if strings.Contains(body, "@here") || strings.Contains(body, "@octocat") || strings.Contains(body, "\r") {
		t.Fatalf("a mention or carriage return is live in the comment")
	}
	got, ok := parseReviewComment(body)
	if !ok {
		t.Fatal("the comment does not parse")
	}
	clean := func(s string) string { return strings.ReplaceAll(s, "\r", "") }
	want := []specialistFindings{{Name: "data", Findings: clean(data)}, {Name: "security", Findings: "a real finding"}}
	if got.SHA != headSHA[:12] || got.Findings != clean(general) || !slices.Equal(got.Specialists, want) {
		t.Fatalf("parse = %+v\nwant general %q and %+v", got, clean(general), want)
	}
}

func TestSpecialistFindingsCannotForgeASectionOrTheFooterInAGeneralBlock(t *testing.T) {
	general := hostileText("general")
	got, ok := parseReviewComment(renderReviewComment(verdict("findings", general, museModel), headSHA, "", mergeNote{}))
	if !ok || got.Findings != strings.ReplaceAll(general, "\r", "") || len(got.Specialists) != 0 {
		t.Fatalf("parse = %+v, ok = %v, want the whole block as the general findings and no section", got, ok)
	}
}

func TestParseReviewCommentRoundTripsTheSpecialists(t *testing.T) {
	v := withSpecialists(verdict("clean", "", museModel),
		reviewedBy("data", "d1\nping @octocat\n```go\nif x {\n```", 0.125),
		reviewedBy("infra", "", 0.25),
		missedBy("cloud", runner.SpecialistReasonDeadline),
		reviewedBy("security", "s1", 0.5))
	got, ok := parseReviewComment(renderReviewComment(v, headSHA, "", mergeNote{}))
	want := []specialistFindings{{Name: "data", Findings: "d1\nping @octocat\n```go\nif x {\n```"}, {Name: "security", Findings: "s1"}}
	if !ok || got.SHA != headSHA[:12] || got.Findings != "" || !slices.Equal(got.Specialists, want) {
		t.Fatalf("parse = %+v, ok = %v, want the head, no general findings and %+v", got, ok, want)
	}
}

func TestParseReviewCommentReadsNoHeadFromSpecialistsWithNoFindings(t *testing.T) {
	v := withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "", 0.125), missedBy("infra", runner.SpecialistReasonDeadline))
	if got, ok := parseReviewComment(renderReviewComment(v, headSHA, "", mergeNote{})); !ok || got.SHA != "" || got.Findings != "" || len(got.Specialists) != 0 {
		t.Fatalf("parse = %+v, ok = %v, want a comment naming no head", got, ok)
	}
}

// withoutClosingFence drops the fence that closes the first block after marker.
func withoutClosingFence(t *testing.T, body, marker string) string {
	t.Helper()
	at := strings.Index(body, marker)
	closing := strings.Index(body[at:], "\n```\n")
	if at < 0 || closing < 0 {
		t.Fatalf("no closed block after %q in %q", marker, body)
	}
	return body[:at+closing] + body[at+closing+len("\n```"):]
}

func TestAnUnclosedSpecialistBlockMakesTheCommentUnreadable(t *testing.T) {
	v := withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "d1", 0.125), reviewedBy("security", "s1", 0.25))
	body := renderReviewComment(v, headSHA, "", mergeNote{})
	for _, marker := range []string{"### Specialist review: data", "### Specialist review: security"} {
		got, ok := parseReviewComment(withoutClosingFence(t, body, marker))
		if ok || got.SHA != headSHA[:12] {
			t.Fatalf("an unclosed block after %q parsed as %+v, ok = %v, want the head and unreadable", marker, got, ok)
		}
	}
	general := renderReviewComment(withSpecialists(verdict("findings", "g1", museModel), reviewedBy("data", "d1", 0.125)), headSHA, "", mergeNote{})
	if _, ok := parseReviewComment(withoutClosingFence(t, general, "Wingman review: findings")); ok {
		t.Fatal("an unclosed general block with a section after it parsed as readable")
	}
}

func TestPlanPostNamesTheSpecialistsThatReportedFindings(t *testing.T) {
	long := "opencode-go/" + strings.Repeat("m", 150)
	every := []runner.SpecialistVerdict{reviewedBy("data", "d", 0.1), reviewedBy("infra", "i", 0.1), reviewedBy("cloud", "c", 0.1), reviewedBy("security", "s", 0.1)}
	for name, tt := range map[string]struct {
		v                  runner.PRVerdict
		state, description string
		clean, held        bool
	}{
		"a clean review with findings from two": {withSpecialists(verdict("clean", "", museModel), reviewedBy("security", "s", 0.1), reviewedBy("data", "d", 0.1)),
			"failure", "Findings from data, security; see the review comment", false, true},
		"general findings and a specialist's": {withSpecialists(verdict("findings", "g", museModel), reviewedBy("infra", "i", 0.1)),
			"failure", "Findings from " + museModel + " and infra; see the review comment", false, true},
		"a clean review and a specialist that found none": {withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "  \n", 0.1)),
			"success", "Clean: no findings from " + museModel, true, false},
		"a long model name": {withSpecialists(verdict("findings", "g", long), every...),
			"failure", "", false, true},
	} {
		t.Run(name, func(t *testing.T) {
			got := planPost("", "success", tt.v, nil, headSHA, "")
			if got.status.State != tt.state || got.clean != tt.clean || len(got.status.Description) > maxStatusDescription {
				t.Fatalf("plan = %+v", got)
			}
			if tt.description != "" && got.status.Description != tt.description {
				t.Fatalf("description = %q, want %q", got.status.Description, tt.description)
			}
			if tt.held && !strings.HasPrefix(got.status.Description, findingsStatusDescription) {
				t.Fatalf("description = %q, want fix-await's findings prefix", got.status.Description)
			}
		})
	}
}

func TestPRReviewPostDoesNotRequestTheMergeWhileASpecialistHasFindings(t *testing.T) {
	eligible := openPull("t", "", headSHA, autoMergeEligibleLabel)
	for name, tt := range map[string]struct {
		v                     runner.PRVerdict
		wantState, wantMerge  string
		wantComment, wantDesc string
	}{
		"clean with specialist findings": {withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "d1", 0.125)), "failure", "merge=false",
			"### Specialist review: data\n\nAt `" + headSHA[:12] + "`:", "Findings from data; see the review comment"},
		"clean with a specialist that found none": {withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "", 0.125)), "success", "merge=true",
			"No findings in this area.", "Clean: no findings from " + museModel},
		"exhausted with specialist findings": {withSpecialists(spentAllowance(), reviewedBy("data", "d1", 0.125)), "failure", "merge=false",
			"Auto-merge is not requested, so this pull request waits for the owner: a specialist reported findings.", "No verdict: allowance exhausted; waits for the owner"},
		"exhausted with a specialist that found none": {withSpecialists(spentAllowance(), reviewedBy("data", "", 0.125)), "failure", "merge=true",
			"merged without review", "No verdict: allowance exhausted; merges without review"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakePulls{pull: eligible, checkRuns: greenChecks()}
			getenv := f.start(t)
			args := []string{"-pr", "7", "-reviewed-sha", headSHA, "-verdict", writeVerdict(t, tt.v), "-context-result", "success"}
			if err := prReviewPostWith(context.Background(), quietLogger(), getenv, switchedOn, rateUnder, args); err != nil {
				t.Fatal(err)
			}
			if got := readOutput(t, getenv); !strings.Contains(got, tt.wantMerge) {
				t.Fatalf("output = %q, want %q", got, tt.wantMerge)
			}
			if s := f.statuses[headSHA]; s.State != tt.wantState || s.Description != tt.wantDesc {
				t.Fatalf("status = %+v, want %s %q", s, tt.wantState, tt.wantDesc)
			}
			if len(f.created) != 1 || !strings.Contains(f.created[0], tt.wantComment) {
				t.Fatalf("comment = %v, want it to hold %q", f.created, tt.wantComment)
			}
		})
	}
}

func TestVerdictFooterTotalsTheGeneralAttemptsAndEverySpecialistAttempt(t *testing.T) {
	v := verdict("clean", "", museModel)
	v.Attempts = []runner.PRReviewAttempt{{Model: pricedModel, Outcome: runner.AttemptTimedOut, Cost: 0.25}, {Model: museModel, Outcome: runner.AttemptReviewed, Cost: 0.5}}
	v = withSpecialists(v, reviewedBy("data", "", 0.125),
		missedBy("cloud", runner.SpecialistReasonTimedOut,
			runner.PRReviewAttempt{Model: specialistModel, Outcome: runner.AttemptTimedOut, Cost: 0.03125},
			runner.PRReviewAttempt{Model: specialistModel, Outcome: runner.AttemptTimedOut, Cost: 0.03125}))
	const want = "Model `" + museModel + "` · 2200 in / 130 out tokens · $0.9375 across 5 attempt(s), specialists included"
	if got := verdictFooter(v); got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
}

func TestReviewAtReadsTheGeneralAndTheSpecialistFindings(t *testing.T) {
	for name, tt := range map[string]struct {
		v    runner.PRVerdict
		want string
	}{
		"a clean general review": {withSpecialists(verdict("clean", "", museModel), reviewedBy("security", "s1\ns2", 0.1), reviewedBy("data", "d1", 0.1), reviewedBy("infra", "", 0.1)), "d1\ns1\ns2"},
		"general findings first": {withSpecialists(verdict("findings", "g1\ng2", museModel), reviewedBy("data", "d1", 0.1)), "g1\ng2\nd1"},
	} {
		t.Run(name, func(t *testing.T) {
			plan := planPost("", "success", tt.v, nil, headSHA, "")
			gh := reviewClient(t, []commitStatusEntry{statusOf(plan.status.State, plan.status.Description, awaitStart)}, []map[string]any{reviewCommentOf(plan.comment)})
			got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || got.Decision != fixAwaitContinue || got.Reason != fixAwaitReasonFindings || got.Findings != tt.want || got.Detail != plan.status.Description {
				t.Fatalf("outcome = %+v, ok = %v, want the findings %q", got, ok, tt.want)
			}
		})
	}
}

func TestReviewAtBindsASpecialistOnlyCommentToItsHead(t *testing.T) {
	plan := planPost("", "success", withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "d1", 0.1)), nil, movedSHA, "")
	gh := reviewClient(t, []commitStatusEntry{statusOf(plan.status.State, plan.status.Description, awaitStart)}, []map[string]any{reviewCommentOf(plan.comment)})
	if got, ok, err := reviewAt(context.Background(), gh, 7, headSHA); err != nil || ok {
		t.Fatalf("outcome = %+v, ok = %v, err = %v, want the poll to continue past another head's comment", got, ok, err)
	}
}

func TestReviewAtStopsOnAnUnclosedSpecialistBlock(t *testing.T) {
	plan := planPost("", "success", withSpecialists(verdict("clean", "", museModel), reviewedBy("data", "d1", 0.1)), nil, headSHA, "")
	body := withoutClosingFence(t, plan.comment, "### Specialist review: data")
	gh := reviewClient(t, []commitStatusEntry{statusOf(plan.status.State, plan.status.Description, awaitStart)}, []map[string]any{reviewCommentOf(body)})
	got, ok, err := reviewAt(context.Background(), gh, 7, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Decision != fixAwaitStop || got.Reason != fixAwaitReasonError || !strings.Contains(got.Detail, "unclosed findings fence") {
		t.Fatalf("outcome = %+v, ok = %v, want a stop on the unclosed fence", got, ok)
	}
}

// bulkFindings is n lines of findings, each carrying mentions that escaping widens.
func bulkFindings(tag string, n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = tag + " " + strconv.Itoa(i) + " ping @a @b @c @d @e @f @g @h"
	}
	return strings.Join(lines, "\n")
}

// TestAWorstCaseReviewStaysUnderTheCommentLimit: the general findings at their
// cap and every specialist at its own, escaped, are more than GitHub takes; the
// general block stays whole and each specialist's is cut at a line with a note.
func TestAWorstCaseReviewStaysUnderTheCommentLimit(t *testing.T) {
	general := bulkFindings("general", 730)
	if len(general) > 32<<10 {
		t.Fatalf("the general findings are %d bytes, over the cap the verdict reader allows", len(general))
	}
	var specialists []runner.SpecialistVerdict
	raw := map[string]string{}
	for _, area := range runner.ReviewAreas() {
		raw[area.Name] = bulkFindings(area.Name, 190)
		if len(raw[area.Name]) > 8<<10 {
			t.Fatalf("%s findings are %d bytes, over the specialist cap", area.Name, len(raw[area.Name]))
		}
		specialists = append(specialists, reviewedBy(area.Name, raw[area.Name], 0.125))
	}
	body := renderReviewComment(withSpecialists(verdict("findings", general, museModel), specialists...), headSHA, "https://github.com/o/r/actions/runs/9", mergeNote{})
	if n := utf8.RuneCountInString(body); n > maxReviewCommentChars {
		t.Fatalf("the comment is %d characters, over the %d budget", n, maxReviewCommentChars)
	}
	got, ok := parseReviewComment(body)
	if !ok || got.Findings != general || len(got.Specialists) != len(specialists) {
		t.Fatalf("parse ok = %v, general intact = %v, %d sections", ok, got.Findings == general, len(got.Specialists))
	}
	for _, s := range got.Specialists {
		full := raw[s.Name]
		if s.Findings == "" || !strings.HasPrefix(full, s.Findings) || (len(s.Findings) < len(full) && full[len(s.Findings)] != '\n') {
			t.Fatalf("%s findings are %d of %d bytes and not a whole-line prefix", s.Name, len(s.Findings), len(full))
		}
	}
	if n := strings.Count(body, specialistFindingsCutNote); n != len(specialists) {
		t.Fatalf("%d cut notes, want one for each of the %d specialists", n, len(specialists))
	}
}

func TestASpecialistWithRoomForNoWholeLineKeepsPartOfTheFirst(t *testing.T) {
	long := strings.Repeat("word ", 400)
	s := reviewedBy("data", long+"\nsecond line", 0.125)
	section := renderSpecialist(s, headSHA, 700)
	if utf8.RuneCountInString(section) > 700 || !strings.Contains(section, specialistFindingsCutNote) {
		t.Fatalf("section of %d characters = %q", utf8.RuneCountInString(section), section)
	}
	parsed, ok := parseReviewComment("<!-- wingman-review -->\n### Wingman review: clean\n" + section)
	if !ok || parsed.Specialists[0].Findings == "" || !strings.HasPrefix(long, parsed.Specialists[0].Findings) {
		t.Fatalf("parse = %+v, ok = %v, want a prefix of the first line", parsed, ok)
	}
}

func TestASpecialistWithNoRoomSaysItsFindingsAreNotShown(t *testing.T) {
	section := renderSpecialist(reviewedBy("data", "d1", 0.125), headSHA, 0)
	if !strings.Contains(section, specialistFindingsOmittedNote) || strings.Contains(section, "```") || strings.Contains(section, "d1") {
		t.Fatalf("section = %q, want the omitted note and no block", section)
	}
}

func TestVerdictFooterRoundsASumOfCostsToMillionthsOfADollar(t *testing.T) {
	v := verdict("clean", "", museModel)
	v.Attempts = []runner.PRReviewAttempt{{Model: museModel, Outcome: runner.AttemptTimedOut, Cost: 0.1}, {Model: museModel, Outcome: runner.AttemptReviewed, Cost: 0.2}}
	if got, want := verdictFooter(v), "Model `"+museModel+"` · 1200 in / 80 out tokens · $0.30000000000000004 across 2 attempt(s)"; got != want {
		t.Fatalf("a review with no specialist prints %q, want %q as it always did", got, want)
	}
	v = withSpecialists(v, reviewedBy("data", "", 0))
	if got, want := verdictFooter(v), "Model `"+museModel+"` · 2200 in / 130 out tokens · $0.3 across 3 attempt(s), specialists included"; got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
}

// TestALookAlikeHeadingOutsideAFenceOpensNoSection: only a line that is wholly a
// specialist heading opens a section, so a line that carries one with text
// before or after it, and a block under it, stay in the general review's lines.
func TestALookAlikeHeadingOutsideAFenceOpensNoSection(t *testing.T) {
	at := "At `" + headSHA[:12] + "`:\n\n"
	body := prReviewMarker + "\n### Wingman review: findings\n\n" + at + "```text\ng1\n```\n" +
		"\n### Specialist review: data and more\n\n" + at + "```text\nafter\n```\n" +
		"\n> ### Specialist review: security\n\n" + at + "```text\nquoted\n```\n"
	got, ok := parseReviewComment(body)
	if !ok || got.Findings != "g1" || len(got.Specialists) != 0 {
		t.Fatalf("parse = %+v, ok = %v, want the general findings alone and no section", got, ok)
	}
}
