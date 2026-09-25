package runner

import (
	"context"
	"testing"
	"time"
)

var finalizeNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

func encoded(t *testing.T, s Summary) string {
	t.Helper()
	if s.StartedAt.IsZero() {
		s.StartedAt = finalizeNow.Add(-time.Hour)
	}
	if s.Phase == "" {
		s.Phase = PhaseCommit
	}
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFinalize(t *testing.T) {
	tests := []struct {
		name        string
		summary     func(t *testing.T) string
		in          FinalizeInput
		wantOutcome Outcome
		wantReason  StopReason
		wantBuild   Outcome
	}{
		{"built and PR opened", func(t *testing.T) string { return encoded(t, Summary{Outcome: OutcomeBuilt}) },
			FinalizeInput{PRURL: "https://github.com/o/r/pull/1", RunResult: "success", PRResult: "success"},
			OutcomePROpened, "", OutcomeBuilt},
		{"built but the PR job failed", func(t *testing.T) string { return encoded(t, Summary{Outcome: OutcomeBuilt}) },
			FinalizeInput{RunResult: "success", PRResult: "failure"}, OutcomeInfraFailure, StopPRJob, OutcomeBuilt},
		{"a stop is kept", func(t *testing.T) string {
			return encoded(t, Summary{Outcome: OutcomeStopped, StopReason: StopProjectionMissing, Phase: PhaseProjection})
		}, FinalizeInput{RunResult: "failure", PRResult: "skipped"}, OutcomeStopped, StopProjectionMissing, OutcomeStopped},
		{"no changes is kept", func(t *testing.T) string { return encoded(t, Summary{Outcome: OutcomeNoChanges}) },
			FinalizeInput{RunResult: "success", PRResult: "skipped"}, OutcomeNoChanges, "", OutcomeNoChanges},
		{"no summary", func(*testing.T) string { return "" },
			FinalizeInput{RunResult: "failure", PRResult: "skipped"}, OutcomeInfraFailure, StopNoBuildRecord, OutcomeInfraFailure},
		{"a stop reported by a model job that succeeded", func(t *testing.T) string {
			return encoded(t, Summary{Outcome: OutcomeStopped, StopReason: StopProjectionMissing, Phase: PhaseProjection})
		}, FinalizeInput{RunResult: "success", PRResult: "skipped"}, OutcomeStopped, StopProjectionMissing, OutcomeStopped},
		{"an invalid summary", func(*testing.T) string { return `{"outcome":"pr-opened"}` },
			FinalizeInput{RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"},
			OutcomeInfraFailure, StopSummaryInvalid, OutcomeInfraFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := fakeRecords{}
			in := tt.in
			in.RecordID = "1-1"
			in.Ticket = Tracer
			in.Summary = tt.summary(t)
			in.PRDuration = 3 * time.Second

			if _, err := Finalize(context.Background(), store, in, finalizeNow); err != nil {
				t.Fatal(err)
			}
			got := store["1-1"]
			if got.Outcome != tt.wantOutcome || got.StopReason != tt.wantReason || got.BuildOutcome != tt.wantBuild {
				t.Fatalf("outcome = %s/%s (build %s), want %s/%s (build %s)",
					got.Outcome, got.StopReason, got.BuildOutcome, tt.wantOutcome, tt.wantReason, tt.wantBuild)
			}
			if got.JobResults["run"] != in.RunResult || got.JobResults["pr"] != in.PRResult {
				t.Fatalf("job results = %v", got.JobResults)
			}
			if got.DurationsMS["pr"] != 3000 || !got.UpdatedAt.Equal(finalizeNow) || got.PRURL != in.PRURL {
				t.Fatalf("record = %+v", got)
			}
			if got.RunID != "1-1" || got.TicketID != Tracer.ID || got.SizedBy != Tracer.SizedBy {
				t.Fatalf("record lost the run or ticket: %+v", got)
			}
		})
	}
}

func TestFinalizeWritesTheWholeRecordFromTheSummary(t *testing.T) {
	started := finalizeNow.Add(-10 * time.Minute)
	sum := Summary{
		Outcome:           OutcomeBuilt,
		Phase:             PhaseCommit,
		Model:             "opencode/big-pickle",
		Tokens:            Usage{Input: 10, Output: 2, CacheRead: 90, Cost: 0.5, Steps: 1},
		EditedFiles:       []string{"version.go"},
		DiffLines:         DiffLines{Added: 3},
		CompletionsObject: "completions/1-1.jsonl",
		DurationsMS:       map[string]int64{"build": 1200},
		RuleStackSHA:      testSHA,
		Branch:            BranchName(Tracer.ID, "1-1"),
		StartedAt:         started,
	}
	store := fakeRecords{}
	in := FinalizeInput{RecordID: "1-1", Ticket: Tracer, Summary: encoded(t, sum), RunResult: "success",
		PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	got := store["1-1"]
	if got.Models["build"] != sum.Model || got.Tokens != sum.Tokens || got.DiffLines != sum.DiffLines ||
		got.CompletionsObject != sum.CompletionsObject || got.RuleStackSHA != testSHA || got.Branch != sum.Branch ||
		got.DurationsMS["build"] != 1200 || !got.StartedAt.Equal(started) || len(got.EditedFiles) != 1 ||
		got.Phase != PhasePR {
		t.Fatalf("record = %+v", got)
	}
}

func TestFinalizeConvergesWhenTheFailedJobIsRerun(t *testing.T) {
	store := fakeRecords{}
	raw := encoded(t, Summary{Outcome: OutcomeBuilt})
	steps := []struct {
		in          FinalizeInput
		wantOutcome Outcome
		wantReason  StopReason
	}{
		{FinalizeInput{RunResult: "success", PRResult: "failure"}, OutcomeInfraFailure, StopPRJob},
		{FinalizeInput{RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/2"}, OutcomePROpened, ""},
		{FinalizeInput{RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/2"}, OutcomePROpened, ""},
	}
	for i, s := range steps {
		s.in.RecordID = "1-1"
		s.in.Ticket = Tracer
		s.in.Summary = raw
		if _, err := Finalize(context.Background(), store, s.in, finalizeNow); err != nil {
			t.Fatal(err)
		}
		got := store["1-1"]
		if got.Outcome != s.wantOutcome || got.StopReason != s.wantReason || got.BuildOutcome != OutcomeBuilt {
			t.Fatalf("step %d: %s/%s (build %s), want %s/%s", i, got.Outcome, got.StopReason, got.BuildOutcome,
				s.wantOutcome, s.wantReason)
		}
	}
}

func TestFinalizeRecordsAnUnreadableSummary(t *testing.T) {
	store := fakeRecords{}
	in := FinalizeInput{RecordID: "1-1", Ticket: Tracer, SummaryUnreadable: true, RunResult: "success", PRResult: "skipped"}
	rec, err := Finalize(context.Background(), store, in, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := store["1-1"]; got.Outcome != OutcomeInfraFailure || got.StopReason != StopSummaryUnreadable {
		t.Fatalf("record = %s/%s", got.Outcome, got.StopReason)
	}
	if rec.Succeeded() {
		t.Fatal("an unreadable summary counted as success")
	}
}

func TestRecordSucceeded(t *testing.T) {
	for o, want := range map[Outcome]bool{
		OutcomePROpened: true, OutcomeNoChanges: true, OutcomeBuilt: false,
		OutcomeStopped: false, OutcomeAgentFailed: false, OutcomeInfraFailure: false,
	} {
		if got := (Record{Outcome: o}).Succeeded(); got != want {
			t.Errorf("%s: %v, want %v", o, got, want)
		}
	}
}
