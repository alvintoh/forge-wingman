package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
			CompletionsObject: "completions/" + attemptID + "-build-1.jsonl", At: now.Add(-30 * time.Minute)}},
		EditedFiles:   []string{"widget.go", "extra.go"},
		Branch:        runner.BranchName(tk.BranchSegment(), attemptID),
		CommitSubject: "feat(runner): ABC-1 add the widget",
		PRSummary:     "Adds the widget the runner needs.",
		StartedAt:     now.Add(-time.Hour),
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
		runID, size, failedGate string
		edit                    func(*runner.Summary)
		check                   prCheck
		want                    bool
	}{
		"a ready M run requests it":                    {unsampled, "M", "", nil, prCheck{Planned: true}, true},
		"an unplanned S run does not":                  {unsampled, "S", "", nil, prCheck{}, false},
		"a run with no recorded plan does not":         {unsampled, "M", "", nil, prCheck{}, false},
		"an L run does not":                            {unsampled, "L", "", nil, prCheck{Planned: true}, false},
		"a sampled run does not":                       {sampled, "M", "", nil, prCheck{Planned: true}, false},
		"a draft does not":                             {unsampled, "M", "", func(s *runner.Summary) { s.Ready = false }, prCheck{Planned: true}, false},
		"an out-of-plan edit does not":                 {unsampled, "M", "", nil, prCheck{Planned: true, OutOfPlan: []string{"x.go"}}, false},
		"a concurrent run's plan does not":             {unsampled, "S", "", nil, prCheck{Planned: true, Overlap: "M-2"}, false},
		"an unread bundle does not":                    {unsampled, "M", "", nil, prCheck{Planned: true, Unread: true}, false},
		"front-end paths do not":                       {unsampled, "M", "", nil, prCheck{Planned: true, FrontEnd: []string{"web/"}}, false},
		"the front-end label does not":                 {unsampled, "S", "", nil, prCheck{Planned: true, FrontEnd: []string{"area:fe"}}, false},
		"a workflow change does not":                   {unsampled, "M", "", func(s *runner.Summary) { s.StopReason = runner.StopWorkflowChange }, prCheck{Planned: true}, false},
		"a failed check gate does not":                 {unsampled, "M", "test", nil, prCheck{Planned: true}, false},
		"an unconfirmed provisional decision does not": {unsampled, "M", "", nil, prCheck{Planned: true, Provisional: []runner.Provisional{{Question: "Q?", Choice: "A"}}}, false},
		"unreadable provisional decisions do not":      {unsampled, "M", "", nil, prCheck{Planned: true, ProvisionalUnread: true}, false},
		"a confirmed provisional decision does":        {unsampled, "M", "", nil, prCheck{Planned: true, Provisional: []runner.Provisional{{Question: "Q?", Choice: "A", Confirmed: true}}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
			if tt.edit != nil {
				tt.edit(&sum)
			}
			tk := prRecord.Ticket()
			tk.Size = tt.size
			if got := autoMerge(true, dispatcher.AutonomyAutoMerge, tt.runID, tk, sum, tt.failedGate, tt.check); got != tt.want {
				t.Fatalf("autoMerge = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAutoMergeNeedsALevelThatAllowsIt(t *testing.T) {
	sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
	clear, held := prRecord.Ticket(), prRecord.Ticket()
	held.Size = "L"
	for level, allows := range map[dispatcher.Autonomy]bool{-1: false, 0: false, 1: false, 2: false, 3: false, 4: true, 5: true, 6: false} {
		t.Run(strconv.Itoa(int(level)), func(t *testing.T) {
			if got := autoMerge(true, level, "run-abc-12", clear, sum, "", prCheck{Planned: true}); got != allows {
				t.Errorf("a clear run: autoMerge = %v, want %v", got, allows)
			}
			if autoMerge(true, level, "run-abc-12", held, sum, "", prCheck{Planned: true}) {
				t.Error("a run needing the owner: autoMerge = true, want false at every level")
			}
		})
	}
}

func TestAutoMergeNeedsTheOwnersSwitchOn(t *testing.T) {
	sum := runner.Summary{Outcome: runner.OutcomeBuilt, Ready: true}
	clear := prRecord.Ticket()
	if autoMerge(false, dispatcher.AutonomyAutoMerge, "run-abc-12", clear, sum, "", prCheck{Planned: true}) {
		t.Error("switch off: autoMerge = true, want false for a run the rule would otherwise make eligible")
	}
	if !autoMerge(true, dispatcher.AutonomyAutoMerge, "run-abc-12", clear, sum, "", prCheck{Planned: true}) {
		t.Error("switch on: autoMerge = false, want true")
	}
}

func TestAutoMergeSwitchOnHoldsWhenTheSwitchCannotBeRead(t *testing.T) {
	for name, tt := range map[string]struct {
		store    *fakeAutoMergeSwitchStore
		want     bool
		wantWarn bool
	}{
		"on":         {&fakeAutoMergeSwitchStore{enabled: true}, true, false},
		"off":        {&fakeAutoMergeSwitchStore{}, false, false},
		"unreadable": {&fakeAutoMergeSwitchStore{enabled: true, readErr: errors.New("firestore is unreachable")}, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			var logs strings.Builder
			got := autoMergeSwitchOn(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), "run-abc-12", tt.store)
			if got != tt.want || strings.Contains(logs.String(), "msg=autoMergeSwitchUnread") != tt.wantWarn {
				t.Fatalf("on = %v, log %q, want on = %v and warned = %v", got, logs.String(), tt.want, tt.wantWarn)
			}
		})
	}
}

func TestPlanRecorded(t *testing.T) {
	for name, tt := range map[string]struct {
		plan runner.PlanRecord
		want bool
	}{
		"no plan":           {runner.PlanRecord{}, false},
		"an empty plan":     {runner.PlanRecord{Files: []string{}}, false},
		"a plan with files": {runner.PlanRecord{Files: []string{"a.go"}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := planRecorded(tt.plan); got != tt.want {
				t.Fatalf("planRecorded = %v, want %v", got, tt.want)
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
	wf := []string{".github/workflows/ci.yml"}
	for name, tt := range map[string]struct {
		raw     string
		files   []string
		refused bool
	}{
		"a workflow file in the write set is refused":        {encodedSummary(t, "42-1", now), wf, true},
		"a workflow change the build stopped for is refused": {encodedSummary(t, "42-1", now, stopped), nil, true},
		"a built summary is not":                             {encodedSummary(t, "42-1", now), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			var log strings.Builder
			logger := slog.New(slog.NewTextHandler(&log, nil))
			err := refuseWorkflowPush(logger, buildSummary(logger, tt.raw, "42-1", prRecord.Ticket(), now), tt.files)
			if tt.refused != errors.Is(err, errRunFailed) {
				t.Fatalf("err = %v, want refused %v", err, tt.refused)
			}
			if tt.refused && !strings.Contains(log.String(), "msg=pushRefused") {
				t.Fatalf("log = %q, want the refusal logged", log.String())
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
		title, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{})
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
	title, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", check)
	if err != nil {
		t.Fatal(err)
	}
	if title != "feat(runner): ABC-1 add the widget" {
		t.Fatalf("title = %q, want the build's committed subject", title)
	}
	if !strings.Contains(body, "\nAdds the widget the runner needs\\.\n") {
		t.Fatalf("body lacks the build's summary:\n%s", body)
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

// TestOutOfPlanNeedsARecordedPlan holds that a run no plan stage recorded has no
// trusted list to be outside of; autoMerge refuses such a run on its own.
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
		"no recorded plan":        {runner.Summary{Ready: true}, prCheck{}, false},
		"an unready run":          {runner.Summary{}, prCheck{}, true},
		"an out-of-plan edit":     {runner.Summary{Ready: true}, prCheck{OutOfPlan: []string{"b.go"}}, true},
		"a concurrent run's plan": {runner.Summary{Ready: true}, prCheck{Overlap: "M-1"}, true},
		"an unread write set":     {runner.Summary{Ready: true}, prCheck{Unread: true}, true},
		"a front-end change":      {runner.Summary{Ready: true}, prCheck{FrontEnd: []string{"web/"}}, false},
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

func TestPRBodyNamesAnUnreadWriteSetAsWhyThePRIsADraft(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "",
		prCheck{Unread: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Write set unread:**") {
		t.Fatalf("body = %q, want the unread write set named as why the PR is a draft", body)
	}
}

func TestRenderPRNamesAMissingPlan(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**No recorded plan:**") {
		t.Fatalf("body = %q, want the missing plan named", body)
	}
	_, body, err = renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "", prCheck{Planned: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "No recorded plan") {
		t.Fatalf("body names a missing plan the run has:\n%s", body)
	}
}

func TestDraftPRNamesEveryReasonTheWriteSetHoldsItBack(t *testing.T) {
	now := time.Now()
	ready := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	ready.Ready = true
	for name, tt := range map[string]struct {
		check prCheck
		want  bool
	}{
		"a clear write set":          {prCheck{}, false},
		"no recorded plan":           {prCheck{Planned: false}, false},
		"an unread write set":        {prCheck{Unread: true}, true},
		"an edit outside the plan":   {prCheck{OutOfPlan: []string{"a.go"}}, true},
		"a concurrent run's plan":    {prCheck{Overlap: "FRG-79"}, true},
		"a planned read that failed": {prCheck{Overlap: "", Unread: true}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := draftPR(ready, tt.check); got != tt.want {
				t.Fatalf("draftPR = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFrontEnd(t *testing.T) {
	const setting = `{"octo/repo":["web/","ui"],"octo/other":["docs/"]}`
	changed := []string{"internal/a.go", "web/src/app.ts", "uikit/x.ts"}
	for name, tt := range map[string]struct {
		labelled           bool
		repository, config string
		changed            []string
		want               []string
	}{
		"a file under a declared prefix":      {false, "octo/repo", setting, changed, []string{"web/"}},
		"a prefix without a trailing slash":   {false, "octo/repo", setting, []string{"ui/x.ts", "ui"}, []string{"ui"}},
		"a file named as the prefix":          {false, "octo/repo", setting, []string{"web"}, []string{"web/"}},
		"a sibling sharing the prefix's name": {false, "octo/repo", setting, []string{"uikit/x.ts"}, nil},
		"webhook is not under web":            {false, "octo/repo", setting, []string{"webhook/x.go"}, nil},
		"a prefix written ./web":              {false, "octo/repo", `{"octo/repo":["./web"]}`, []string{"web/a.ts"}, []string{"./web"}},
		"a rooted changed path":               {false, "octo/repo", setting, []string{"/web/a.ts"}, []string{"web/"}},
		"an empty prefix holds every change":  {false, "octo/repo", `{"octo/repo":[""]}`, []string{"a.go"}, []string{""}},
		"an empty prefix and no change":       {false, "octo/repo", `{"octo/repo":[""]}`, nil, nil},
		"the prefix's case is exact":          {false, "octo/repo", setting, []string{"Web/a.ts"}, nil},
		"an unset setting is not malformed":   {false, "octo/repo", "", []string{"web/a.ts"}, nil},
		"the repository's casing":             {false, "Octo/Repo", setting, changed, []string{"web/"}},
		"a prefix declared for another repo":  {false, "octo/other", setting, changed, nil},
		"no setting and no label":             {false, "octo/repo", "", changed, nil},
		"an empty prefix list":                {false, "octo/repo", `{"octo/repo":[]}`, changed, nil},
		"the label alone":                     {true, "octo/repo", "", []string{"a.go"}, []string{"area:fe"}},
		"the label and a prefix":              {true, "octo/repo", setting, changed, []string{"area:fe", "web/"}},
		"a setting that is not JSON holds":    {false, "octo/repo", "web/", []string{"a.go"}, []string{"WINGMAN_FRONT_END_PATHS"}},
		"no changed files and no label":       {false, "octo/repo", setting, nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if got := frontEnd(logger, tt.labelled, tt.repository, tt.config, tt.changed); !slices.Equal(got, tt.want) {
				t.Fatalf("frontEnd = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPRBodyNamesWhatHeldAFrontEndChange(t *testing.T) {
	now := time.Now()
	sum := buildSummary(slog.New(slog.NewTextHandler(io.Discard, nil)),
		encodedSummary(t, "42-1", now), "42-1", prRecord.Ticket(), now)
	_, body, err := renderPR(prTemplate(t), prRecord, sum, "", "https://example.test/run", "",
		prCheck{FrontEnd: []string{"area:fe", "web/"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "**Front-end change:** waits for the owner — matched `area:fe`, `web/`; run /fe-sweep before reading the diff") {
		t.Fatalf("body = %q, want the front-end marks named", body)
	}
}

func TestSupersededOutputIsEmptyUnlessAPRIsReplaced(t *testing.T) {
	if got := supersededOutput(141); got != "141" {
		t.Fatalf("supersededOutput(141) = %q, want 141", got)
	}
	if got := supersededOutput(0); got != "" {
		t.Fatalf("supersededOutput(0) = %q, want empty", got)
	}
}
