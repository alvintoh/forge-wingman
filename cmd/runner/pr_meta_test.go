package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
)

// prRecord is a run record as pr-meta reads it: the ticket, and none of the
// build's fields, which only the record job writes after the PR opens.
var prRecord = runner.Record{TicketID: "ABC-1", TicketTitle: "[BE] Add the widget", Size: "M", SizedBy: "test", TicketBody: "Add it."}

func prTemplate(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join("..", "..", runner.DefaultPRTemplate))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func encodedSummary(t *testing.T, attemptID string, now time.Time, edits ...func(*runner.Summary)) string {
	tk := prRecord.Ticket()
	sum := runner.Summary{
		Outcome: runner.OutcomeBuilt,
		Phase:   runner.PhaseCommit,
		Steps: []runner.Step{{Phase: runner.PhaseBuild, Round: 1, Model: "command-code/x",
			CompletionsObject: "completions/" + attemptID + "-build-1.jsonl"}},
		EditedFiles:    []string{"widget.go", "extra.go"},
		OutOfPlanFiles: []string{"extra.go"},
		Branch:         runner.BranchName(tk.BranchSegment(), attemptID),
		CommitSubject:  "feat(runner): ABC-1 add the widget",
		PRSummary:      "Adds the widget the runner needs.",
		StartedAt:      now.Add(-time.Hour),
	}
	for _, edit := range edits {
		edit(&sum)
	}
	raw, err := sum.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAutoMerge(t *testing.T) {
	// run-0 falls in the review sample and run-abc-12 does not.
	const unsampled, sampled = "run-abc-12", "run-0"
	for name, tt := range map[string]struct {
		switchValue, runID, size, failedGate string
		edit                                 func(*runner.Summary)
		check                                prCheck
		want                                 bool
	}{
		"a ready M run requests it":        {"on", unsampled, "M", "", nil, prCheck{}, true},
		"a ready S run requests it":        {"on", unsampled, "S", "", nil, prCheck{}, true},
		"an L run does not":                {"on", unsampled, "L", "", nil, prCheck{}, false},
		"a sampled run does not":           {"on", sampled, "M", "", nil, prCheck{}, false},
		"a draft does not":                 {"on", unsampled, "M", "", func(s *runner.Summary) { s.Ready = false }, prCheck{}, false},
		"an out-of-plan edit does not":     {"on", unsampled, "M", "", nil, prCheck{OutOfPlan: []string{"x.go"}}, false},
		"a concurrent run's plan does not": {"on", unsampled, "S", "", nil, prCheck{Overlap: "M-2"}, false},
		"an unread bundle does not":        {"on", unsampled, "M", "", nil, prCheck{Unread: true}, false},
		"a workflow change does not":       {"on", unsampled, "M", "", func(s *runner.Summary) { s.StopReason = runner.StopWorkflowChange }, prCheck{}, false},
		"a failed check gate does not":     {"on", unsampled, "M", "test", nil, prCheck{}, false},
		"the switch unset does not":        {"", unsampled, "M", "", nil, prCheck{}, false},
		"any value but on does not":        {"true", unsampled, "M", "", nil, prCheck{}, false},
		"the switch in capitals does not":  {"ON", unsampled, "M", "", nil, prCheck{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
			if tt.edit != nil {
				tt.edit(&sum)
			}
			tk := prRecord.Ticket()
			tk.Size = tt.size
			if got := autoMerge(tt.switchValue, tt.runID, tk, sum, tt.failedGate, tt.check); got != tt.want {
				t.Fatalf("autoMerge = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRefuseWorkflowPush(t *testing.T) {
	now := time.Now()
	stopped := func(s *runner.Summary) {
		s.Outcome, s.StopReason = runner.OutcomeStopped, runner.StopWorkflowChange
		s.StopDetail = "touches files under .github/workflows/, which the run cannot push: .github/workflows/ci.yml"
	}
	for name, tt := range map[string]struct {
		raw     string
		refused bool
	}{
		"a workflow change is refused": {encodedSummary(t, "42-1", now, stopped), true},
		"a built summary is not":       {encodedSummary(t, "42-1", now), false},
	} {
		t.Run(name, func(t *testing.T) {
			var log strings.Builder
			logger := slog.New(slog.NewTextHandler(&log, nil))
			err := refuseWorkflowPush(logger, buildSummary(logger, tt.raw, "42-1", prRecord.Ticket(), now))
			if tt.refused != errors.Is(err, errRunFailed) {
				t.Fatalf("err = %v, want refused %v", err, tt.refused)
			}
			if tt.refused != strings.Contains(log.String(), "msg=pushRefused") ||
				tt.refused != strings.Contains(log.String(), ".github/workflows/ci.yml") {
				t.Fatalf("log = %q", log.String())
			}
		})
	}
}

func TestRenderPRFallsBackToTheTicketWithoutAUsableSummary(t *testing.T) {
	now := time.Now()
	for name, tt := range map[string]struct {
		raw     string
		wantLog bool
	}{
		"empty":           {"", false},
		"another attempt": {encodedSummary(t, "42-2", now), true},
		"malformed":       {"{", true},
	} {
		var log strings.Builder
		logger := slog.New(slog.NewTextHandler(&log, nil))
		sum := buildSummary(logger, tt.raw, "42-1", prRecord.Ticket(), now)
		title, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", "", nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if title != "ABC-1 [BE] Add the widget" || !strings.Contains(body, "`ABC-1`: [BE] Add the widget") ||
			strings.Contains(body, "outside the plan") {
			t.Errorf("%s: title %q, want the ticket's subject and summary and no out-of-plan line:\n%s", name, title, body)
		}
		if got := strings.Contains(log.String(), "summaryRejected"); got != tt.wantLog {
			t.Errorf("%s: logged summaryRejected = %v, want %v: %q", name, got, tt.wantLog, log.String())
		}
	}
}

// TestRenderPRNamesWhatPRMetaComputedNotWhatTheModelClaimed is the AC11
// regression: the summary above claims `extra.go` is out of plan, and the branch
// read out of its bundle says nothing is, so the body names what pr-meta
// computed. The model job's own list is the claim under test.
func TestRenderPRNamesWhatPRMetaComputedNotWhatTheModelClaimed(t *testing.T) {
	now := time.Now()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum := buildSummary(logger, encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	planned := []string{"widget.go", "extra.go"}
	check := prCheck{
		Changed:   planned,
		OutOfPlan: outOfPlan(runner.PlanRecord{Files: planned}, planned),
		Overlap:   "M-2",
	}
	title, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", check.Overlap, check.OutOfPlan)
	if err != nil {
		t.Fatal(err)
	}
	if title != "feat(runner): ABC-1 add the widget" {
		t.Fatalf("title = %q, want the build's committed subject", title)
	}
	if !strings.Contains(body, "\nAdds the widget the runner needs\\.\n") {
		t.Fatalf("body lacks the build's summary:\n%s", body)
	}
	if strings.Contains(body, "outside the plan") {
		t.Errorf("body repeats the model job's out-of-plan claim:\n%s", body)
	}
	if !strings.Contains(body, "**Concurrent run:** kept this a draft — `M-2`") {
		t.Fatalf("body does not name the run whose plan these files touch:\n%s", body)
	}
}

func TestPRTitlePrefersTheCommittedSubject(t *testing.T) {
	tk := runner.Ticket{ID: "ABC-1", Title: "[BE] Add the widget"}
	if got := prTitle(runner.Summary{}, tk); got != "ABC-1 [BE] Add the widget" {
		t.Fatalf("title without a committed subject = %q, want the ticket's subject", got)
	}
	sum := runner.Summary{CommitSubject: "feat(runner): ABC-1 add the widget"}
	if got := prTitle(sum, tk); got != "feat(runner): ABC-1 add the widget" {
		t.Fatalf("title = %q, want the committed subject", got)
	}
}

func TestPlanOverlapNamesTheRunWhosePlanTheseFilesTouch(t *testing.T) {
	flight := []dispatcher.InFlight{
		{Ticket: "M-1", Repo: "octo/repo", Size: "M", Stage: dispatcher.StageBuild,
			Files: []string{"internal/runner/build.go", "cmd/runner/main.go"}},
		{Ticket: "L-1", Repo: "octo/other", Size: "L", Files: []string{"web/src/app.go"}},
		// A run whose plan has not settled writes an unknown set, not an empty
		// one, so it can touch nothing it could be named for.
		{Ticket: "M-2", Repo: "octo/repo", Size: "M", Stage: dispatcher.StagePlan},
		{Ticket: "M-3", Repo: "octo/repo", Size: "M", Files: []string{"web/src/app.go"}},
	}
	for name, tt := range map[string]struct {
		size, repository string
		changed          []string
		want             string
	}{
		"a planned file":                {"S", "octo/repo", []string{"cmd/runner/main.go"}, "M-1"},
		"only the first match is named": {"S", "octo/repo", []string{"internal/runner/build.go"}, "M-1"},
		"nothing planned is touched":    {"S", "octo/repo", []string{"docs/readme.md"}, ""},
		"another repository's plan":     {"S", "octo/repo", []string{"web/src/app.go"}, "M-3"},
		"no changed files":              {"S", "octo/repo", nil, ""},
		"an unplanned run is not named": {"S", "octo/repo", []string{"a.go"}, ""},
		"an M run asks nothing":         {"M", "octo/repo", []string{"cmd/runner/main.go"}, ""},
		"an L run asks nothing":         {"L", "octo/repo", []string{"cmd/runner/main.go"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := planOverlap(tt.size, tt.repository, tt.changed, flight); got != tt.want {
				t.Fatalf("planOverlap = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOutOfPlanNeedsARecordedPlan is what keeps this dormant until the plan
// stage is switched on: a run no plan stage recorded — an S ticket, and every
// run until then — has no trusted list to be outside of.
func TestOutOfPlanNeedsARecordedPlan(t *testing.T) {
	changed := []string{"a.go", "b.go"}
	if got := outOfPlan(runner.PlanRecord{}, changed); got != nil {
		t.Fatalf("outOfPlan with no recorded plan = %q, want nothing", got)
	}
	if got := outOfPlan(runner.PlanRecord{Files: []string{"a.go"}}, changed); !slices.Equal(got, []string{"b.go"}) {
		t.Fatalf("outOfPlan = %q, want the unplanned file", got)
	}
}

func TestDraftPR(t *testing.T) {
	for name, tt := range map[string]struct {
		sum   runner.Summary
		check prCheck
		want  bool
	}{
		"a ready run in plan":     {runner.Summary{Ready: true}, prCheck{Changed: []string{"a.go"}}, false},
		"an unready run":          {runner.Summary{}, prCheck{}, true},
		"an out-of-plan edit":     {runner.Summary{Ready: true}, prCheck{OutOfPlan: []string{"b.go"}}, true},
		"a concurrent run's plan": {runner.Summary{Ready: true}, prCheck{Overlap: "M-1"}, true},
		"an unread write set":     {runner.Summary{Ready: true}, prCheck{Unread: true}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := draftPR(tt.sum, tt.check); got != tt.want {
				t.Fatalf("draftPR = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunWorkflowReadsTheBranchOutOfTheBundle is the credential-free design:
// the pr job has not pushed the branch yet, so pr-meta reads the write set out
// of the bundle the model job uploaded, diffed against the main it checked out.
func TestRunWorkflowReadsTheBranchOutOfTheBundle(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	_, job, _ := strings.Cut(string(yml), "\n  pr-meta:\n")
	for _, want := range []string{
		"          fetch-depth: 0\n",
		"          name: bundle\n",
		`-bundle "$RUNNER_TEMP/wingman.bundle" -branch "$BRANCH" -repository "$REPOSITORY"`,
		"BRANCH: ${{ needs.model.outputs.branch }}",
		"REPOSITORY: ${{ github.repository }}",
		"draft: ${{ steps.meta.outputs.draft }}",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("run.yml's pr-meta job lacks %q", want)
		}
	}
}

// TestRunWorkflowOpensTheDraftPRMetasDecisionNames is the S-overlap draft: the
// pr job opens a draft from pr-meta's own decision, which the model job's ready
// output could otherwise override.
func TestRunWorkflowOpensTheDraftPRMetasDecisionNames(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(yml)
	if !strings.Contains(s, "DRAFT: ${{ needs.pr-meta.outputs.draft }}") ||
		!strings.Contains(s, `[[ "$DRAFT" != "true" ]] && draft=()`) {
		t.Errorf("run.yml's pr job does not open a draft from pr-meta's decision:\n%s", openStep(s))
	}
	if strings.Contains(s, "needs.model.outputs.ready") {
		t.Error("run.yml still reads the model job's own ready output; pr-meta decides the draft")
	}
}

// openStep is the pr job's open step, for an assertion message that shows it.
func openStep(yml string) string {
	_, after, _ := strings.Cut(yml, "\n  pr:\n")
	_, step, _ := strings.Cut(after, "\n      - id: open\n")
	step, _, _ = strings.Cut(step, "\n\n")
	return step
}
