package runner

import (
	"context"
	"testing"
	"time"
)

func TestFinalize(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		existing    *Record
		in          FinalizeInput
		wantOutcome Outcome
		wantReason  StopReason
	}{
		{"built and PR opened", &Record{BuildOutcome: OutcomeBuilt, Outcome: OutcomeBuilt},
			FinalizeInput{PRURL: "https://github.com/o/r/pull/1", RunResult: "success", PRResult: "success"},
			OutcomePROpened, ""},
		{"built but the PR job failed", &Record{BuildOutcome: OutcomeBuilt, Outcome: OutcomeBuilt},
			FinalizeInput{RunResult: "success", PRResult: "failure"}, OutcomeInfraFailure, "pr-job"},
		{"a stop is kept", &Record{BuildOutcome: OutcomeStopped, Outcome: OutcomeStopped, StopReason: StopProjectionMissing},
			FinalizeInput{RunResult: "failure", PRResult: "skipped"}, OutcomeStopped, StopProjectionMissing},
		{"no changes is kept", &Record{BuildOutcome: OutcomeNoChanges, Outcome: OutcomeNoChanges},
			FinalizeInput{RunResult: "success", PRResult: "skipped"}, OutcomeNoChanges, ""},
		{"no build record", nil,
			FinalizeInput{RunResult: "failure", PRResult: "skipped"}, OutcomeInfraFailure, StopNoBuildRecord},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := fakeRecords{}
			if tt.existing != nil {
				store["1-1"] = *tt.existing
			}
			in := tt.in
			in.RecordID = "1-1"
			in.Ticket = Tracer
			in.PRDuration = 3 * time.Second

			if _, err := Finalize(context.Background(), store, in, now); err != nil {
				t.Fatal(err)
			}
			got := store["1-1"]
			if got.Outcome != tt.wantOutcome || got.StopReason != tt.wantReason {
				t.Fatalf("outcome = %s/%s, want %s/%s", got.Outcome, got.StopReason, tt.wantOutcome, tt.wantReason)
			}
			if got.JobResults["run"] != in.RunResult || got.JobResults["pr"] != in.PRResult {
				t.Fatalf("job results = %v", got.JobResults)
			}
			if got.DurationsMS["pr"] != 3000 || !got.UpdatedAt.Equal(now) || got.PRURL != in.PRURL {
				t.Fatalf("record = %+v", got)
			}
		})
	}
}

func TestFinalizeConvergesWhenTheFailedJobIsRerun(t *testing.T) {
	store := fakeRecords{"1-1": {BuildOutcome: OutcomeBuilt, Outcome: OutcomeBuilt}}
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	steps := []struct {
		in          FinalizeInput
		wantOutcome Outcome
		wantReason  StopReason
	}{
		{FinalizeInput{RunResult: "success", PRResult: "failure"}, OutcomeInfraFailure, "pr-job"},
		{FinalizeInput{RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/2"}, OutcomePROpened, ""},
		{FinalizeInput{RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/2"}, OutcomePROpened, ""},
	}
	for i, s := range steps {
		s.in.RecordID = "1-1"
		if _, err := Finalize(context.Background(), store, s.in, now); err != nil {
			t.Fatal(err)
		}
		got := store["1-1"]
		if got.Outcome != s.wantOutcome || got.StopReason != s.wantReason || got.BuildOutcome != OutcomeBuilt {
			t.Fatalf("step %d: %s/%s (build %s), want %s/%s", i, got.Outcome, got.StopReason, got.BuildOutcome,
				s.wantOutcome, s.wantReason)
		}
	}
}
