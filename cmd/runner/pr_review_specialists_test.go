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

// TestPostIgnoresTheSpecialistsOfAVerdict: the comment and the status are the
// general review's alone for now, so a verdict that carries specialists posts
// exactly what the same verdict without them does.
func TestPostIgnoresTheSpecialistsOfAVerdict(t *testing.T) {
	specialists := []runner.SpecialistVerdict{
		{Name: "data", Outcome: runner.SpecialistReviewed, Model: specialistModel, Findings: "a finding\n```\n@someone",
			Attempts: []runner.PRReviewAttempt{attempted(specialistModel, runner.AttemptReviewed)}},
		{Name: "infra", Outcome: runner.SpecialistNotReviewed, Reason: runner.SpecialistReasonDeadline, Attempts: []runner.PRReviewAttempt{}},
	}
	for name, v := range map[string]runner.PRVerdict{
		"clean":      verdict("clean", "", museModel),
		"findings":   verdict("findings", "x is missing", museModel),
		"no verdict": noVerdict(attempted(museModel, runner.AttemptFailed)),
		"exhausted":  spentAllowance(),
	} {
		t.Run(name, func(t *testing.T) {
			with := v
			with.Specialists = specialists
			if got, want := renderReviewComment(with, headSHA, "", mergeNote{}), renderReviewComment(v, headSHA, "", mergeNote{}); got != want {
				t.Fatalf("comment differs:\n%s\nwant\n%s", got, want)
			}
			if got, want := planPost("", "success", with, nil, headSHA, ""), planPost("", "success", v, nil, headSHA, ""); got != want {
				t.Fatalf("plan = %+v, want %+v", got, want)
			}
			if got, want := verdictFooter(with), verdictFooter(v); got != want {
				t.Fatalf("footer = %q, want %q", got, want)
			}
		})
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
