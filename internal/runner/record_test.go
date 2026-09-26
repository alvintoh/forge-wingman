package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"
)

var finalizeNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

const testRunID = "run-abc-12"

func TestRunWorkflowRunsTheRunnersGates(t *testing.T) {
	yml, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	loop := regexp.MustCompile(`for g in ((?:"[a-z-]+:\$[A-Z]+" ?)+);`).FindSubmatch(yml)
	if loop == nil {
		t.Fatal("run.yml has no gate loop")
	}
	var got []string
	for _, m := range regexp.MustCompile(`"([a-z-]+):`).FindAllSubmatch(loop[1], -1) {
		got = append(got, string(m[1]))
	}
	if !slices.Equal(got, gates) {
		t.Fatalf("run.yml runs gates %v, want %v", got, gates)
	}
}

func encoded(t *testing.T, s Summary) string {
	t.Helper()
	if s.StartedAt.IsZero() {
		s.StartedAt = finalizeNow.Add(-time.Hour)
	}
	if s.Phase == "" && s.StopReason == "" {
		s.Phase = PhaseCommit
		s.Branch, s.Model, s.CompletionsObject = BranchName(testTicket.BranchSegment(), "1-1"), "opencode/big-pickle", "completions/1-1.jsonl"
	}
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var seededAt = finalizeNow.Add(-2 * time.Hour)

// seeded is a store holding the record Seed writes for testTicket.
func seeded(t *testing.T) fakeRecords {
	t.Helper()
	store := fakeRecords{}
	if err := Seed(context.Background(), store, testRunID, testTicket, seededAt); err != nil {
		t.Fatal(err)
	}
	return store
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
		{"an identity mismatch is kept", func(t *testing.T) string {
			return encoded(t, Summary{Outcome: OutcomeStopped, StopReason: StopIdentityMismatch})
		}, FinalizeInput{RunResult: "success", PRResult: "skipped"}, OutcomeStopped, StopIdentityMismatch, OutcomeStopped},
		{"an invalid summary", func(*testing.T) string { return `{"outcome":"pr-opened"}` },
			FinalizeInput{RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"},
			OutcomeInfraFailure, StopSummaryInvalid, OutcomeInfraFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := seeded(t)
			in := tt.in
			in.RunID, in.AttemptID = testRunID, "1-1"
			in.Identity = testIdentity
			in.Summary = tt.summary(t)
			in.PRDuration = 3 * time.Second

			if _, err := Finalize(context.Background(), store, in, finalizeNow); err != nil {
				t.Fatal(err)
			}
			got := store[testRunID]
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
			if got.RunID != testRunID || got.Ticket() != testTicket {
				t.Fatalf("record lost the run or ticket: %+v", got)
			}
		})
	}
}

func TestFinalizeWritesTheSeededRecordKeepingItsTicketAndStart(t *testing.T) {
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, Summary{Outcome: OutcomeBuilt}),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	if len(store) != 1 {
		t.Fatalf("store holds %d records, want the seeded one only", len(store))
	}
	got := store[testRunID]
	if got.Ticket() != testTicket || got.Branch != "wingman/abc-12-1-1" || got.Outcome != OutcomePROpened ||
		!got.StartedAt.Equal(seededAt) {
		t.Fatalf("record = %+v", got)
	}
}

func TestFinalizeRecordsWhyThereWasNoTicket(t *testing.T) {
	ticketless := Record{RunID: testRunID, TicketID: "ABC-12", TicketTitle: "a title and nothing else"}
	for _, tt := range []struct {
		name  string
		store fakeRecords
		want  StopReason
	}{
		{"no record", fakeRecords{}, StopRecordMissing},
		{"a record without a buildable ticket", fakeRecords{testRunID: ticketless}, StopTicketMissing},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", RunResult: "skipped", PRResult: "skipped"}
			rec, err := Finalize(context.Background(), tt.store, in, finalizeNow)
			if err != nil {
				t.Fatal(err)
			}
			got := tt.store[testRunID]
			if got.Outcome != OutcomeStopped || got.StopReason != tt.want || got.StopDetail == "" || rec.Succeeded() {
				t.Fatalf("record = %s/%s %q", got.Outcome, got.StopReason, got.StopDetail)
			}
			if tt.want == StopTicketMissing && (got.TicketID != ticketless.TicketID || got.TicketTitle != ticketless.TicketTitle) {
				t.Fatalf("the ticket as read was not kept: %+v", got)
			}
		})
	}
}

func TestFinalizeFailsWhenTheRecordCannotBeRead(t *testing.T) {
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1"}
	if _, err := Finalize(context.Background(), unreadableRecords{}, in, finalizeNow); err == nil {
		t.Fatal("Finalize wrote a record it could not read")
	}
}

type unreadableRecords struct{ fakeRecords }

func (unreadableRecords) GetRecord(context.Context, string) (Record, error) {
	return Record{}, errors.New("unavailable")
}

func TestFinalizeRecordsTheFailedGateOfAnOpenedPR(t *testing.T) {
	for report, want := range map[string]string{
		"":              "",
		"none":          "",
		"test":          "test",
		"gofmt":         "gofmt",
		"unfinished":    gateUnnamed,
		"rm -rf":        gateUnnamed,
		"golangci-lint": "golangci-lint",
	} {
		t.Run(report, func(t *testing.T) {
			store := seeded(t)
			in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, Summary{Outcome: OutcomeBuilt}),
				RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1", CheckReport: report}
			if _, err := Finalize(context.Background(), store, in, finalizeNow); err != nil {
				t.Fatal(err)
			}
			got := store[testRunID]
			if got.FailedGate != want || got.Outcome != OutcomePROpened {
				t.Fatalf("failed gate %q, outcome %s; want %q, pr-opened", got.FailedGate, got.Outcome, want)
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
		Branch:            BranchName(testTicket.BranchSegment(), "1-1"),
		StartedAt:         started,
	}
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum), RunResult: "success",
		PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	got := store[testRunID]
	if got.Models["build"] != sum.Model || got.Tokens != sum.Tokens || got.DiffLines != sum.DiffLines ||
		got.CompletionsObject != sum.CompletionsObject || got.RuleStackSHA != testSHA || got.Branch != sum.Branch ||
		got.DurationsMS["build"] != 1200 || !got.StartedAt.Equal(seededAt) || len(got.EditedFiles) != 1 ||
		got.Phase != PhasePR {
		t.Fatalf("record = %+v", got)
	}
}

func TestFinalizeConvergesWhenTheFailedJobIsRerun(t *testing.T) {
	store := seeded(t)
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
		s.in.RunID, s.in.AttemptID, s.in.Identity = testRunID, "1-1", testIdentity
		s.in.Summary = raw
		if _, err := Finalize(context.Background(), store, s.in, finalizeNow); err != nil {
			t.Fatal(err)
		}
		got := store[testRunID]
		if got.Outcome != s.wantOutcome || got.StopReason != s.wantReason || got.BuildOutcome != OutcomeBuilt {
			t.Fatalf("step %d: %s/%s (build %s), want %s/%s", i, got.Outcome, got.StopReason, got.BuildOutcome,
				s.wantOutcome, s.wantReason)
		}
	}
}

func TestFinalizeRecordsAnUnreadableSummary(t *testing.T) {
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", SummaryUnreadable: true, RunResult: "success", PRResult: "skipped"}
	rec, err := Finalize(context.Background(), store, in, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := store[testRunID]; got.Outcome != OutcomeInfraFailure || got.StopReason != StopSummaryUnreadable {
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

func TestReadRun(t *testing.T) {
	store := seeded(t)
	got, err := ReadRun(context.Background(), store, testRunID)
	if err != nil || got.Ticket() != testTicket {
		t.Fatalf("ticket %+v, err %v", got.Ticket(), err)
	}
	var stopped *StopError
	if _, err := ReadRun(context.Background(), store, "another-run"); !errors.As(err, &stopped) ||
		stopped.Reason != StopRecordMissing {
		t.Fatalf("err = %v, want a record-missing stop", err)
	}
}

func TestSeedRefuses(t *testing.T) {
	store := seeded(t)
	for name, tt := range map[string]struct {
		runID  string
		ticket Ticket
	}{
		"an existing record":    {testRunID, testTicket},
		"an unbuildable ticket": {"run-2", Ticket{ID: "ABC-13"}},
		"a run id with a slash": {"runs/x", testTicket},
		"an empty run id":       {"", testTicket},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Seed(context.Background(), store, tt.runID, tt.ticket, finalizeNow); err == nil {
				t.Fatal("seeded")
			}
		})
	}
	if len(store) != 1 || store[testRunID].Ticket() != testTicket {
		t.Fatalf("store = %+v", store)
	}
}

func TestFinalizeRecordsAnIdentityMismatchAheadOfEverythingElse(t *testing.T) {
	for _, tt := range []struct {
		name  string
		store fakeRecords
		in    FinalizeInput
	}{
		{"the model job never ran", seeded(t), FinalizeInput{RunResult: "skipped", PRResult: "skipped"}},
		{"no record either", fakeRecords{}, FinalizeInput{RunResult: "skipped", PRResult: "skipped"}},
		{"a build that opened a PR", seeded(t), FinalizeInput{Summary: encoded(t, Summary{Outcome: OutcomeBuilt}),
			RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.RunID, in.AttemptID = testRunID, "1-1"
			in.Identity = Identity{Account: "work-account", Owner: "octo"}
			rec, err := Finalize(context.Background(), tt.store, in, finalizeNow)
			if err != nil {
				t.Fatal(err)
			}
			got := tt.store[testRunID]
			if got.Outcome != OutcomeStopped || got.StopReason != StopIdentityMismatch || rec.Succeeded() {
				t.Fatalf("record = %s/%s", got.Outcome, got.StopReason)
			}
		})
	}
}
