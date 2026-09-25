package runner

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func validSummary() Summary {
	return Summary{
		Outcome:           OutcomeBuilt,
		Phase:             PhaseCommit,
		Model:             "opencode/big-pickle",
		EditedFiles:       []string{"version.go"},
		CompletionsObject: "completions/1-1.jsonl",
		DurationsMS:       map[string]int64{"build": 10},
		RuleStackSHA:      testSHA,
		Branch:            BranchName(testTicket.BranchSegment(), "1-1"),
		StartedAt:         finalizeNow.Add(-time.Hour),
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
		{"a malformed model", func(s *Summary) { s.Model = "big pickle" }},
		{"negative tokens", func(s *Summary) { s.Tokens.Input = -1 }},
		{"a negative cost", func(s *Summary) { s.Tokens.Cost = -0.1 }},
		{"negative diff lines", func(s *Summary) { s.DiffLines.Removed = -1 }},
		{"too many diff lines", func(s *Summary) { s.DiffLines.Added = maxCount + 1 }},
		{"an empty edited file", func(s *Summary) { s.EditedFiles = []string{""} }},
		{"an edited file with a NUL", func(s *Summary) { s.EditedFiles = []string{"a\x00b"} }},
		{"an absolute edited file", func(s *Summary) { s.EditedFiles = []string{"/etc/passwd"} }},
		{"an edited file above the repository", func(s *Summary) { s.EditedFiles = []string{"../x"} }},
		{"an edited file through a parent segment", func(s *Summary) { s.EditedFiles = []string{"a/../b"} }},
		{"an edited file ending in a parent segment", func(s *Summary) { s.EditedFiles = []string{"a/.."} }},
		{"a start time over a week old", func(s *Summary) { s.StartedAt = finalizeNow.Add(-8 * 24 * time.Hour) }},
		{"another run's completions", func(s *Summary) { s.CompletionsObject = "completions/1-2.jsonl" }},
		{"a completions path escape", func(s *Summary) { s.CompletionsObject = "completions/../x.jsonl" }},
		{"another run's branch", func(s *Summary) { s.Branch = "wingman/abc-12-9-1" }},
		{"a duration for another phase", func(s *Summary) { s.DurationsMS = map[string]int64{"pr": 1} }},
		{"a negative duration", func(s *Summary) { s.DurationsMS = map[string]int64{"build": -1} }},
		{"a sha that is not one", func(s *Summary) { s.RuleStackSHA = "main" }},
		{"no start time", func(s *Summary) { s.StartedAt = time.Time{} }},
		{"a start time in the future", func(s *Summary) { s.StartedAt = finalizeNow.Add(time.Hour) }},
		{"a built run with no branch", func(s *Summary) { s.Branch = "" }},
		{"a built run with no model", func(s *Summary) { s.Model = "" }},
		{"a built run with no completions", func(s *Summary) { s.CompletionsObject = "" }},
		{"an agent failure with no completions", func(s *Summary) {
			s.Outcome, s.StopReason, s.Phase, s.CompletionsObject = OutcomeAgentFailed, StopAgentExit, PhaseBuild, ""
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
	if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err != nil {
		t.Fatal(err)
	}
}

func TestParseSummaryAcceptsEveryBuildEnding(t *testing.T) {
	for e := range buildEndings {
		s := Summary{Outcome: e.outcome, StopReason: e.reason, Phase: e.phase, StartedAt: finalizeNow}
		if e.phase == PhaseBuild || e.phase == PhaseCommit {
			s.Branch, s.Model, s.CompletionsObject = BranchName(testTicket.BranchSegment(), "1-1"), "opencode/big-pickle", "completions/1-1.jsonl"
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
