package runner

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func validSummary() Summary {
	return Summary{
		Outcome: OutcomeBuilt,
		Phase:   PhaseCommit,
		Steps: []Step{{
			Phase:             PhaseBuild,
			Round:             1,
			Model:             "command-code/x",
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1),
			At:                finalizeNow.Add(-30 * time.Minute),
		}},
		EditedFiles:  []string{"version.go"},
		DurationsMS:  map[string]int64{"build": 10},
		RuleStackSHA: testSHA,
		Branch:       BranchName(testTicket.BranchSegment(), "1-1"),
		StartedAt:    finalizeNow.Add(-time.Hour),
	}
}

func TestParseSummaryAcceptsWhatABuildReports(t *testing.T) {
	raw, err := validSummary().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err != nil {
		t.Fatal(err)
	}
}

func TestParseSummaryAcceptsTheChecksDuration(t *testing.T) {
	s := validSummary()
	s.DurationsMS["checks"] = 4000
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err != nil {
		t.Fatal(err)
	}
}

// TestParseSummaryKeepsThePhasesCacheFigures asserts a phase's first-request
// fresh and cached tokens and its cached share survive the summary round trip
// (AC1).
func TestParseSummaryKeepsThePhasesCacheFigures(t *testing.T) {
	s := validSummary()
	s.Cache = map[string]PhaseCache{"build": {FirstFresh: 10, FirstCached: 90, CachedShare: 0.9}}
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSummary(raw, "1-1", testTicket, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if want := (PhaseCache{FirstFresh: 10, FirstCached: 90, CachedShare: 0.9}); got.Cache["build"] != want {
		t.Fatalf("cache = %+v, want %+v", got.Cache, want)
	}
}

func TestParseSummaryRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Summary)
	}{
		{"an outcome only the record job derives", func(s *Summary) { s.Outcome = OutcomePROpened }},
		{"an unknown outcome", func(s *Summary) { s.Outcome = "shipped" }},
		{"a stop reason only the record job derives", func(s *Summary) {
			s.Outcome, s.StopReason = OutcomeInfraFailure, StopNoBuildRecord
		}},
		{"an unknown stop reason", func(s *Summary) { s.Outcome, s.StopReason = OutcomeStopped, "tired" }},
		{"a built run with a stop reason", func(s *Summary) { s.StopReason = StopCommit }},
		{"a stop without a reason", func(s *Summary) { s.Outcome = OutcomeStopped }},
		{"the pr phase", func(s *Summary) { s.Phase = PhasePR }},
		{"no phase", func(s *Summary) { s.Phase = "" }},
		{"a malformed model", func(s *Summary) { s.Steps[0].Model = "big pickle" }},
		{"a step naming a harness this run cannot route to", func(s *Summary) { s.Steps[0].Harness = "no-such-harness" }},
		{"negative tokens", func(s *Summary) { s.Steps[0].Tokens.Input = -1 }},
		{"a negative cost", func(s *Summary) { s.Steps[0].Tokens.Cost = -0.1 }},
		{"negative diff lines", func(s *Summary) { s.DiffLines.Removed = -1 }},
		{"too many diff lines", func(s *Summary) { s.DiffLines.Added = maxCount + 1 }},
		{"an empty edited file", func(s *Summary) { s.EditedFiles = []string{""} }},
		{"an edited file with a NUL", func(s *Summary) { s.EditedFiles = []string{"a\x00b"} }},
		{"an absolute edited file", func(s *Summary) { s.EditedFiles = []string{"/etc/passwd"} }},
		{"an edited file above the repository", func(s *Summary) { s.EditedFiles = []string{"../x"} }},
		{"an edited file through a parent segment", func(s *Summary) { s.EditedFiles = []string{"a/../b"} }},
		{"an edited file ending in a parent segment", func(s *Summary) { s.EditedFiles = []string{"a/.."} }},
		{"an absolute read file", func(s *Summary) { s.ReadFiles = []string{"/etc/passwd"} }},
		{"a read file through a parent segment", func(s *Summary) { s.ReadFiles = []string{"a/../b"} }},
		{"a negative peak context", func(s *Summary) { s.PeakContextTokens = -1 }},
		{"a peak context over the ceiling", func(s *Summary) { s.PeakContextTokens = maxCount + 1 }},
		{"a start time over a week old", func(s *Summary) { s.StartedAt = finalizeNow.Add(-8 * 24 * time.Hour) }},
		{"another run's completions", func(s *Summary) { s.Steps[0].CompletionsObject = "completions/1-2-build-1.jsonl" }},
		{"a completions path escape", func(s *Summary) { s.Steps[0].CompletionsObject = "completions/../x.jsonl" }},
		{"a step wearing another step's completions name", func(s *Summary) {
			s.Steps[0].CompletionsObject = completionsObject("1-1", PhaseBuild, 2)
		}},
		{"another run's branch", func(s *Summary) { s.Branch = "wingman/abc-12-9-1" }},
		{"a duration for another phase", func(s *Summary) { s.DurationsMS = map[string]int64{"pr": 1} }},
		{"a negative duration", func(s *Summary) { s.DurationsMS = map[string]int64{"build": -1} }},
		{"cache figures for something that is not a build phase", func(s *Summary) {
			s.Cache = map[string]PhaseCache{"pr": {FirstFresh: 1}}
		}},
		{"a negative first-request token count", func(s *Summary) {
			s.Cache = map[string]PhaseCache{"build": {FirstFresh: -1}}
		}},
		{"a first-request token count over the ceiling", func(s *Summary) {
			s.Cache = map[string]PhaseCache{"build": {FirstCached: maxCount + 1}}
		}},
		{"a cached share above one", func(s *Summary) {
			s.Cache = map[string]PhaseCache{"build": {CachedShare: 1.5}}
		}},
		{"a negative cached share", func(s *Summary) {
			s.Cache = map[string]PhaseCache{"build": {CachedShare: -0.1}}
		}},
		{"a sha that is not one", func(s *Summary) { s.RuleStackSHA = "main" }},
		{"a commit subject over two lines", func(s *Summary) { s.CommitSubject = "feat: ABC-12 x\nmore" }},
		{"a PR summary over two lines", func(s *Summary) { s.PRSummary = "Adds it.\nmore" }},
		{"no start time", func(s *Summary) { s.StartedAt = time.Time{} }},
		{"a start time in the future", func(s *Summary) { s.StartedAt = finalizeNow.Add(time.Hour) }},
		{"a step with no start time", func(s *Summary) { s.Steps[0].At = time.Time{} }},
		{"a step starting before the run", func(s *Summary) { s.Steps[0].At = s.StartedAt.Add(-time.Hour) }},
		{"a step starting in the future", func(s *Summary) { s.Steps[0].At = finalizeNow.Add(time.Hour) }},
		{"a built run with no branch", func(s *Summary) { s.Branch = "" }},
		{"a built run with no model", func(s *Summary) { s.Steps[0].Model = "" }},
		{"a built run with no completions", func(s *Summary) { s.Steps[0].CompletionsObject = "" }},
		{"a built run with no build step", func(s *Summary) { s.Steps = nil }},
		{"an agent failure with no completions", func(s *Summary) {
			s.Outcome, s.StopReason, s.Phase = OutcomeAgentFailed, StopAgentExit, PhaseBuild
			s.Steps[0].CompletionsObject = ""
		}},
		{"a plan stop with no branch", func(s *Summary) {
			s.Outcome, s.StopReason, s.Phase, s.Branch = OutcomeStopped, StopPlanInvalid, PhasePlan, ""
		}},
		{"a plan agent failure with a build-phase step", func(s *Summary) {
			s.Outcome, s.StopReason, s.Phase = OutcomeAgentFailed, StopAgentExit, PhasePlan
		}},
		{"a budget stop with no build step", func(s *Summary) {
			s.Outcome, s.StopReason, s.Phase, s.Steps = OutcomeBudgetStop, StopAllowanceExhausted, PhaseBuild, nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSummary()
			tt.mutate(&s)
			raw, err := s.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
}

func TestParseSummaryRejectsMalformedJSON(t *testing.T) {
	good, err := validSummary().Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"not json":      "outcome=built",
		"unknown field": strings.Replace(good, `{"outcome"`, `{"record_id":"x","outcome"`, 1),
		"trailing data": good + good,
		"oversized":     strings.Repeat(" ", maxSummaryBytes+1),
		"too many edited files": strings.Replace(good, `"edited_files":["version.go"]`,
			`"edited_files":[`+strings.Repeat(`"a",`, maxEditedFiles)+`"a"]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestParseSummaryCapsAndCleansStrings(t *testing.T) {
	s := validSummary()
	s.Outcome, s.StopReason = OutcomeInfraFailure, StopCommit
	s.StopDetail = strings.Repeat("x", 3*stopDetailLimit) + "\xff"
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSummary(raw, "1-1", testTicket, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.StopDetail) > stopDetailLimit+len("…") || !utf8.ValidString(got.StopDetail) {
		t.Fatalf("stop detail of %d bytes, valid %v", len(got.StopDetail), utf8.ValidString(got.StopDetail))
	}
}

func TestParseSummaryCapsTheCommitFields(t *testing.T) {
	s := validSummary()
	s.CommitSubject = strings.Repeat("s", 2*stopDetailLimit)
	s.PRSummary = strings.Repeat("p", 2*stopDetailLimit)
	s.CommitBody = strings.Repeat("b", maxTicketBodyBytes+1)
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSummary(raw, "1-1", testTicket, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CommitSubject) > stopDetailLimit+len("…") || len(got.PRSummary) > stopDetailLimit+len("…") ||
		len(got.CommitBody) > maxTicketBodyBytes+len("…") {
		t.Fatalf("commit fields of %d, %d and %d bytes", len(got.CommitSubject), len(got.PRSummary), len(got.CommitBody))
	}
}

func TestParseSummaryCapsEveryFilePath(t *testing.T) {
	s := validSummary()
	long := strings.Repeat("a", 3*maxPathBytes)
	s.EditedFiles = []string{long}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSummary(string(b), "1-1", testTicket, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got.EditedFiles {
		if len(f) > maxPathBytes+len("…") {
			t.Fatalf("file path of %d bytes survived the cap", len(f))
		}
	}
}

func TestEncodeFitsTheLimitAndStaysOnOneLine(t *testing.T) {
	s := validSummary()
	s.EditedFiles = make([]string, 5*maxEditedFiles)
	for i := range s.EditedFiles {
		s.EditedFiles[i] = strings.Repeat("\n", 2*maxPathBytes)
	}
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxSummaryBytes || strings.ContainsAny(raw, "\r\n") {
		t.Fatalf("summary of %d bytes, multiline %v", len(raw), strings.ContainsAny(raw, "\r\n"))
	}
	var encoded Summary
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		t.Fatal(err)
	}
	for _, f := range encoded.EditedFiles {
		if len(f) > maxPathBytes+len("…") {
			t.Fatalf("encoded a file path of %d bytes", len(f))
		}
	}
	if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err != nil {
		t.Fatal(err)
	}
}

func TestParseSummaryAcceptsEveryBuildEnding(t *testing.T) {
	for e := range buildEndings {
		s := Summary{Outcome: e.outcome, StopReason: e.reason, Phase: e.phase, StartedAt: finalizeNow}
		if e.phase == PhasePlan || e.phase == PhaseBuild || e.phase == PhaseReview || e.phase == PhaseCommit {
			s.Branch = BranchName(testTicket.BranchSegment(), "1-1")
		}
		agentPhase := e.phase
		if e.phase == PhaseCommit {
			agentPhase = PhaseBuild
		}
		if e.phase == PhaseCommit || e.outcome == OutcomeAgentFailed || e.outcome == OutcomeBudgetStop {
			s.Steps = []Step{{
				Phase: agentPhase, Round: 1, Model: "command-code/x",
				CompletionsObject: completionsObject("1-1", agentPhase, 1),
				At:                finalizeNow.Add(-time.Minute),
			}}
		}
		raw, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err != nil {
			t.Errorf("%s/%s at %q: %v", e.outcome, e.reason, e.phase, err)
		}
	}
}

func TestParseSummaryRejectsEndingsABuildCannotReach(t *testing.T) {
	for _, e := range []ending{
		{OutcomeStopped, StopProjectionMissing, PhaseCommit},
		{OutcomeInfraFailure, StopProjectionMissing, PhaseProjection},
		{OutcomeAgentFailed, StopAgentExit, PhaseCommit},
		{OutcomeAgentFailed, StopCommit, PhaseCommit},
		{OutcomeInfraFailure, "", PhaseCommit},
		{OutcomeBuilt, "", PhaseBuild},
		{OutcomeNoChanges, "", PhaseProjection},
		{OutcomeInfraFailure, StopSetup, PhaseProjection},
		{OutcomeInfraFailure, StopPanic, ""},
		{OutcomeStopped, StopWorktree, PhaseWorktree},
		// A review-phase failure no longer ends a build at PhaseReview: it
		// forces a draft and the build proceeds to PhaseCommit instead (FR-5).
		{OutcomeStopped, StopReviewInvalid, PhaseReview},
		{OutcomeInfraFailure, StopChecksRun, PhaseReview},
		// An out-of-plan build commits a draft rather than stopping.
		{OutcomeStopped, "out-of-plan", PhaseCommit},
	} {
		s := Summary{Outcome: e.outcome, StopReason: e.reason, Phase: e.phase, StartedAt: finalizeNow}
		raw, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err == nil {
			t.Errorf("accepted %s/%s at %q", e.outcome, e.reason, e.phase)
		}
	}
}

func TestParseSummaryAcceptsSeveralStepsEachUnderItsOwnCompletionsName(t *testing.T) {
	s := validSummary()
	s.Steps = append(s.Steps, Step{
		Phase: PhaseBuild, Round: 2, Model: "command-code/x",
		CompletionsObject: completionsObject("1-1", PhaseBuild, 2),
		At:                finalizeNow.Add(-29 * time.Minute),
	})
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSummary(raw, "1-1", testTicket, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 2 || got.Steps[0].CompletionsObject == got.Steps[1].CompletionsObject {
		t.Fatalf("steps = %+v", got.Steps)
	}
}

func TestEncodeDropsTheOldestStepsWhenEditedFilesAloneIsNotEnough(t *testing.T) {
	s := validSummary()
	s.EditedFiles = nil
	for i := range 700 {
		s.Steps = append(s.Steps, Step{
			Phase: PhaseBuild, Round: i + 2, Model: "command-code/x",
			CompletionsObject: completionsObject("1-1", PhaseBuild, i+2),
		})
	}
	want := len(s.Steps)

	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxSummaryBytes {
		t.Fatalf("summary is %d bytes, over %d", len(raw), maxSummaryBytes)
	}
	var got Summary
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) == 0 || len(got.Steps) >= want {
		t.Fatalf("kept %d of %d steps, want some trimmed", len(got.Steps), want)
	}
	if got.Steps[0].Round != s.Steps[want-len(got.Steps)].Round {
		t.Fatalf("kept steps do not start where the oldest were dropped: first round %d", got.Steps[0].Round)
	}
}

func TestStepRejectsAModelTooLongToRecord(t *testing.T) {
	step := Step{Phase: PhaseBuild, Round: 1, Model: overLongModel, CompletionsObject: completionsObject("1-1", PhaseBuild, 1)}
	if err := step.validate("1-1"); err == nil {
		t.Fatal("a step recorded a model past the length limit")
	}
	step.Model = "p/" + strings.Repeat("a", maxModelBytes-2)
	if err := step.validate("1-1"); err != nil {
		t.Fatal(err)
	}
}

// TestParseSummaryKeepsTheAgentsReadsAndPeakContext asserts the files an agent
// read and the largest context a request read survive the summary round trip.
func TestParseSummaryKeepsTheAgentsReadsAndPeakContext(t *testing.T) {
	s := validSummary()
	s.ReadFiles = []string{"a.go", "b.go"}
	s.PeakContextTokens = 480000
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSummary(raw, "1-1", testTicket, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.ReadFiles, s.ReadFiles) || got.PeakContextTokens != s.PeakContextTokens {
		t.Fatalf("read files %q, peak context %d, want %q and %d",
			got.ReadFiles, got.PeakContextTokens, s.ReadFiles, s.PeakContextTokens)
	}
}

// TestParseSummaryRejectsTooManyReadFiles covers a summary that did not come
// through Encode, which is what caps a build's own list: a run reports the
// files it read, never an unbounded pile of them.
func TestParseSummaryRejectsTooManyReadFiles(t *testing.T) {
	s := validSummary()
	s.ReadFiles = make([]string, maxEditedFiles+1)
	for i := range s.ReadFiles {
		s.ReadFiles[i] = "a.go"
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSummary(string(raw), "1-1", testTicket, finalizeNow); err == nil {
		t.Fatal("a summary carrying more read files than a run can report was accepted")
	}
}

// TestEncodeTrimsFilesReadBeforeSteps keeps the run's own steps, which say what
// the agent did, over a list of read files too large to carry whole.
func TestEncodeTrimsFilesReadBeforeSteps(t *testing.T) {
	s := validSummary()
	s.EditedFiles = nil
	s.ReadFiles = make([]string, maxEditedFiles)
	for i := range s.ReadFiles {
		s.ReadFiles[i] = strings.Repeat("a", maxPathBytes)
	}
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxSummaryBytes {
		t.Fatalf("summary is %d bytes, over %d", len(raw), maxSummaryBytes)
	}
	var got Summary
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != len(s.Steps) {
		t.Fatalf("kept %d of %d steps, want every step kept", len(got.Steps), len(s.Steps))
	}
	if len(got.ReadFiles) == 0 || len(got.ReadFiles) >= maxEditedFiles {
		t.Fatalf("kept %d read files, want some trimmed", len(got.ReadFiles))
	}
}

// TestEncodeDropsReadFilesBeforeEditedFiles asserts an oversize summary gives up the read list first.
func TestEncodeDropsReadFilesBeforeEditedFiles(t *testing.T) {
	s := validSummary()
	long := strings.Repeat("d/", 200)
	for i := range maxEditedFiles {
		s.ReadFiles = append(s.ReadFiles, fmt.Sprintf("%sr%03d.go", long, i))
	}
	for i := range 40 {
		s.EditedFiles = append(s.EditedFiles, fmt.Sprintf("%se%03d.go", long, i))
	}
	s.EditedFiles = append(s.EditedFiles, "version.go")
	edited := len(s.EditedFiles)
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxSummaryBytes {
		t.Fatalf("encoded %d bytes, over %d", len(raw), maxSummaryBytes)
	}
	var got Summary
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.ReadFiles) >= maxEditedFiles {
		t.Fatalf("read files = %d, want some dropped", len(got.ReadFiles))
	}
	if len(got.EditedFiles) != edited {
		t.Fatalf("edited files = %d, want all %d kept while read files could still go", len(got.EditedFiles), edited)
	}
}

// TestMergePathsDropsPathsOutsideTheRepository asserts absolute and escaping paths are left out.
func TestMergePathsDropsPathsOutsideTheRepository(t *testing.T) {
	got := mergePaths([]string{"a.go"}, []string{"/etc/passwd", "../x.go", "", "b.go", "a.go"})
	if want := []string{"a.go", "b.go"}; !slices.Equal(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}
