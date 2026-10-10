package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/money"
	"github.com/alvintoh/forge-wingman/internal/providers"
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
		s.Branch = BranchName(testTicket.BranchSegment(), "1-1")
		s.Steps = []Step{{
			Phase: PhaseBuild, Round: 1, Model: "command-code/x",
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1),
			At:                finalizeNow.Add(-30 * time.Minute),
		}}
	}
	raw, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var seededAt = finalizeNow.Add(-2 * time.Hour)

// seeded is a store holding the record the dispatcher writes for testTicket.
func seeded(t *testing.T) fakeRecords {
	t.Helper()
	return fakeRecords{testRunID: NewRecord(testRunID, testTicket, seededAt)}
}

// fakeLedger is a Ledger that records every run it was asked to settle.
type fakeLedger struct {
	settled []string
	err     error
}

func (l *fakeLedger) Settle(_ context.Context, runID string) error {
	if l.err != nil {
		return l.err
	}
	l.settled = append(l.settled, runID)
	return nil
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
		{"built but the rebase conflicted", func(t *testing.T) string { return encoded(t, Summary{Outcome: OutcomeBuilt}) },
			FinalizeInput{RunResult: "success", PRResult: "failure", PRStopReason: string(StopRebaseConflict)},
			OutcomeInfraFailure, StopRebaseConflict, OutcomeBuilt},
		{"built but the PR job failed for an unrecognised reason", func(t *testing.T) string { return encoded(t, Summary{Outcome: OutcomeBuilt}) },
			FinalizeInput{RunResult: "success", PRResult: "failure", PRStopReason: "something-else"},
			OutcomeInfraFailure, StopPRJob, OutcomeBuilt},
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
		{"a workflow change at commit is kept", func(t *testing.T) string {
			return encoded(t, Summary{Outcome: OutcomeStopped, StopReason: StopWorkflowChange, Phase: PhaseCommit,
				Branch: BranchName(testTicket.BranchSegment(), "1-1"),
				Steps: []Step{{Phase: PhaseBuild, Round: 1, Model: "command-code/x",
					CompletionsObject: completionsObject("1-1", PhaseBuild, 1), At: finalizeNow.Add(-30 * time.Minute)}}})
		}, FinalizeInput{RunResult: "success", PRResult: "skipped"}, OutcomeStopped, StopWorkflowChange, OutcomeStopped},
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

			if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
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
			if got.RunID != testRunID || !reflect.DeepEqual(got.Ticket(), testTicket) {
				t.Fatalf("record lost the run or ticket: %+v", got)
			}
		})
	}
}

func TestFinalizeWritesTheSeededRecordKeepingItsTicketAndStart(t *testing.T) {
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, Summary{Outcome: OutcomeBuilt}),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	if len(store) != 1 {
		t.Fatalf("store holds %d records, want the seeded one only", len(store))
	}
	got := store[testRunID]
	if !reflect.DeepEqual(got.Ticket(), testTicket) || got.Branch != "wingman/abc-12-1-1" || got.Outcome != OutcomePROpened ||
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
			rec, err := Finalize(context.Background(), tt.store, &fakeLedger{}, nil, in, finalizeNow)
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

// TestFinalizeNeverSettlesARunThatNeverReachedAnAgent is the regression test
// for a stop that happens before any agent runs (identity/ticket/projection
// checks) being marked settled anyway, at zero cost — which would otherwise
// enter the estimator's mean as a genuine zero-cost sample and silently pull
// every future estimate of that size down.
func TestFinalizeNeverSettlesARunThatNeverReachedAnAgent(t *testing.T) {
	ticketless := Record{RunID: testRunID, TicketID: "ABC-12", TicketTitle: "a title and nothing else"}
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", RunResult: "skipped", PRResult: "skipped"}
	rec, err := Finalize(context.Background(), fakeRecords{testRunID: ticketless}, &fakeLedger{}, nil, in, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if rec.StopReason != StopTicketMissing {
		t.Fatalf("stop reason = %q, want %q", rec.StopReason, StopTicketMissing)
	}
	if !rec.SettledAt.IsZero() {
		t.Fatalf("settled at %v, want zero — no agent ran, so no cost could have been incurred", rec.SettledAt)
	}
}

func TestFinalizeFailsWhenTheRecordCannotBeRead(t *testing.T) {
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1"}
	if _, err := Finalize(context.Background(), unreadableRecords{}, &fakeLedger{}, nil, in, finalizeNow); err == nil {
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
			if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
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
		Outcome: OutcomeBuilt,
		Phase:   PhaseCommit,
		Steps: []Step{{
			Phase:             PhaseBuild,
			Round:             1,
			Model:             "command-code/x",
			Harness:           "omp",
			Tokens:            Usage{Input: 10, Output: 2, CacheRead: 90, Cost: 0.5, Steps: 1},
			DurationMS:        1200,
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1),
			At:                finalizeNow.Add(-5 * time.Minute),
		}},
		EditedFiles:    []string{"version.go"},
		OutOfPlanFiles: []string{"version.go"},
		DiffLines:      DiffLines{Added: 3},
		DurationsMS:    map[string]int64{"build": 1200},
		RuleStackSHA:   testSHA,
		Branch:         BranchName(testTicket.BranchSegment(), "1-1"),
		CommitSubject:  "feat(x): ABC-12 add a file",
		CommitBody:     "- add version.go",
		PRSummary:      "Adds a file.",
		StartedAt:      started,
	}
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum), RunResult: "success",
		PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	got := store[testRunID]
	if len(got.Steps) != 1 || got.Steps[0] != sum.Steps[0] || got.Tokens != sum.Steps[0].Tokens ||
		got.DiffLines != sum.DiffLines || got.RuleStackSHA != testSHA || got.Branch != sum.Branch ||
		got.DurationsMS["build"] != 1200 || !got.StartedAt.Equal(seededAt) || len(got.EditedFiles) != 1 ||
		got.Phase != PhasePR || got.CommitSubject != sum.CommitSubject || got.CommitBody != sum.CommitBody ||
		got.PRSummary != sum.PRSummary || len(got.OutOfPlanFiles) != 1 || got.OutOfPlanFiles[0] != "version.go" {
		t.Fatalf("record = %+v", got)
	}
}

func TestFinalizeSumsTokensAcrossSteps(t *testing.T) {
	sum := Summary{
		Outcome: OutcomeBuilt,
		Phase:   PhaseCommit,
		Branch:  BranchName(testTicket.BranchSegment(), "1-1"),
		Steps: []Step{
			{Phase: PhaseBuild, Round: 1, Model: "command-code/x", Tokens: Usage{Input: 10, Output: 2, Steps: 1},
				CompletionsObject: completionsObject("1-1", PhaseBuild, 1), At: finalizeNow.Add(-30 * time.Minute)},
			{Phase: PhaseBuild, Round: 2, Model: "command-code/x", Tokens: Usage{Input: 5, Output: 1, Steps: 1},
				CompletionsObject: completionsObject("1-1", PhaseBuild, 2), At: finalizeNow.Add(-20 * time.Minute)},
		},
	}
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	got := store[testRunID]
	if want := (Usage{Input: 15, Output: 3, Steps: 2}); got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, want)
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
		if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, s.in, finalizeNow); err != nil {
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
	rec, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow)
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
	if err != nil || !reflect.DeepEqual(got.Ticket(), testTicket) {
		t.Fatalf("ticket %+v, err %v", got.Ticket(), err)
	}
	var stopped *StopError
	if _, err := ReadRun(context.Background(), store, "another-run"); !errors.As(err, &stopped) ||
		stopped.Reason != StopRecordMissing {
		t.Fatalf("err = %v, want a record-missing stop", err)
	}
}

func TestFinalizeSettlesTheLedgerWithTheRunsActualCost(t *testing.T) {
	store := seeded(t)
	rec := store[testRunID]
	rec.Private = true
	store[testRunID] = rec

	sum := Summary{
		Outcome: OutcomeBuilt,
		Phase:   PhaseCommit,
		Branch:  BranchName(testTicket.BranchSegment(), "1-1"),
		Steps: []Step{{
			Phase: PhaseBuild, Round: 1, Model: "command-code/x",
			Tokens:            Usage{Input: 10, Output: 2, Cost: 0.123456, Steps: 1},
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1),
			At:                finalizeNow.Add(-30 * time.Minute),
		}},
		DurationsMS: map[string]int64{"build": 90_000}, // 1.5 minutes, rounds up to 2
	}
	ledger := &fakeLedger{}
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, ledger, nil, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	got := store[testRunID]
	if !got.SettledAt.Equal(finalizeNow) {
		t.Fatalf("settled at %v, want %v", got.SettledAt, finalizeNow)
	}
	if got.SettledProviderCostMicros != 123456 {
		t.Fatalf("settled cost = %d micros, want 123456", got.SettledProviderCostMicros)
	}
	if got.SettledRunnerMinutes != 2 {
		t.Fatalf("settled minutes = %d, want 2 (90s rounds up)", got.SettledRunnerMinutes)
	}
	if !got.Private {
		t.Fatal("private was not carried from the existing record")
	}
	if !slices.Equal(ledger.settled, []string{testRunID}) {
		t.Fatalf("ledger.Settle was called with %v, want [%s]", ledger.settled, testRunID)
	}
}

func TestBillableMinutesLeavesOutTheChecksThePhasesAlreadyHold(t *testing.T) {
	if got := BillableMinutes(map[string]int64{"build": 90_000, "checks": 60_000}); got != 2 {
		t.Fatalf("billable minutes = %d, want 2 from the build's 90s alone", got)
	}
}

func TestFinalizeZeroesSettledRunnerMinutesForAPublicTarget(t *testing.T) {
	store := seeded(t) // Private defaults to false
	sum := Summary{
		Outcome: OutcomeBuilt, Phase: PhaseCommit, Branch: BranchName(testTicket.BranchSegment(), "1-1"),
		Steps: []Step{{Phase: PhaseBuild, Round: 1, Model: "command-code/x", Tokens: Usage{Cost: 1},
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1), At: finalizeNow.Add(-30 * time.Minute)}},
		DurationsMS: map[string]int64{"build": 120_000},
	}
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	if got := store[testRunID].SettledRunnerMinutes; got != 0 {
		t.Fatalf("settled runner minutes = %d, want 0 for a public target", got)
	}
}

func TestFinalizeFailsWhenTheLedgerCannotBeSettled(t *testing.T) {
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, Summary{Outcome: OutcomeBuilt}),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	ledger := &fakeLedger{err: errors.New("firestore unreachable")}
	if _, err := Finalize(context.Background(), store, ledger, nil, in, finalizeNow); err == nil {
		t.Fatal("Finalize succeeded despite the ledger failing to settle")
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
			rec, err := Finalize(context.Background(), tt.store, &fakeLedger{}, nil, in, finalizeNow)
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

func TestFinalizeCarriesTheModelLabelsFromTheExistingRecord(t *testing.T) {
	store := seeded(t)
	rec := store[testRunID]
	rec.ModelLabels = ModelLabels{Build: "p/a", Review: "p/b", Plan: []string{"p/c", "p/d"}}
	rec.Plan, rec.LastResort, rec.LastResortModels = "p", true, ModelLabels{Build: "p/free"}
	store[testRunID] = rec
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: "",
		RunResult: "failure", PRResult: "skipped"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	if got := store[testRunID].ModelLabels; got.Build != "p/a" || got.Review != "p/b" || !slices.Equal(got.Plan, []string{"p/c", "p/d"}) {
		t.Fatalf("model labels = %+v, want the ticket's carried through finalize", got)
	}
	if got := store[testRunID]; got.Plan != "p" || !got.LastResort || got.LastResortModels.Build != "p/free" {
		t.Fatalf("plan %q, last resort %v, %+v, want the claim's carried through finalize", got.Plan, got.LastResort, got.LastResortModels)
	}
}

func TestFinalizeRecordsTheSampleAndTheAutoMergeRequest(t *testing.T) {
	for _, tt := range []struct {
		runID              string
		autoMerge, sampled bool
	}{
		{testRunID, true, false},
		{"run-0", false, true},
	} {
		store := fakeRecords{tt.runID: NewRecord(tt.runID, testTicket, seededAt)}
		in := FinalizeInput{Identity: testIdentity, RunID: tt.runID, AttemptID: "1-1", Summary: "",
			RunResult: "failure", PRResult: "skipped", AutoMerge: tt.autoMerge}
		if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
			t.Fatal(err)
		}
		if got := store[tt.runID]; got.Sampled != tt.sampled || got.AutoMerge != tt.autoMerge {
			t.Errorf("%s: sampled %v, auto-merge %v, want %v, %v", tt.runID, got.Sampled, got.AutoMerge, tt.sampled, tt.autoMerge)
		}
	}
}

func TestRunModelsAreTheFreeTiersOnlyOnAFreeTierClaim(t *testing.T) {
	named, free := ModelLabels{Build: "p/paid"}, ModelLabels{Build: "p/free"}
	if got := (Record{ModelLabels: named, LastResortModels: free}).RunModels(); got.Build != "p/paid" {
		t.Fatalf("RunModels = %+v, want the ticket's on a paid claim", got)
	}
	if got := (Record{ModelLabels: named, LastResort: true, LastResortModels: free}).RunModels(); got.Build != "p/free" {
		t.Fatalf("RunModels = %+v, want the free tier's in place of the ticket's", got)
	}
}

func TestTheRecordsTicketCarriesTheFreeModelsOnlyOnAFreeTierClaim(t *testing.T) {
	free := ModelLabels{Build: "p/free-a", Review: "p/free-b", Plan: []string{"p/free-a", "p/free-b"}}
	tk := (Record{TicketID: "ABC-12", TicketTitle: "t", Size: "S", TicketBody: "b",
		ModelLabels: ModelLabels{Build: "p/paid", Plan: []string{"p/paid-a", "p/paid-b"}},
		LastResort:  true, LastResortModels: free}).Ticket()
	if !slices.Equal(tk.FreeModels, free.Plan) {
		t.Fatalf("free models = %v, want the free tier's %v", tk.FreeModels, free.Plan)
	}
	paid := (Record{TicketID: "ABC-12", TicketTitle: "t", Size: "S", TicketBody: "b",
		ModelLabels: ModelLabels{Build: "p/paid", Plan: []string{"p/paid-a", "p/paid-b"}}}).Ticket()
	if len(paid.FreeModels) != 0 {
		t.Fatalf("free models = %v, want none on a paid claim", paid.FreeModels)
	}
}

func TestFinalizeCarriesTheProviderVerdictFromTheExistingRecord(t *testing.T) {
	store := seeded(t)
	rec := store[testRunID]
	rec.ProviderVerdict = providers.VerdictRestricted
	store[testRunID] = rec
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: "",
		RunResult: "failure", PRResult: "skipped"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	if got := store[testRunID].ProviderVerdict; got != "restricted" {
		t.Fatalf("provider verdict = %q, want restricted carried from the record, not the summary", got)
	}
}

func TestFinalizeRecordsTheRunURL(t *testing.T) {
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: "",
		RunResult: "failure", PRResult: "skipped", RunURL: "https://github.com/o/r/actions/runs/7"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	if got := store[testRunID].RunURL; got != "https://github.com/o/r/actions/runs/7" {
		t.Fatalf("run url = %q", got)
	}
}

// fakeTrailing is a ProviderCostReader that answers with a fixed mean and
// sample, recording what it was asked.
type fakeTrailing struct {
	mean   money.Micros
	sample int
	err    error
	calls  []trailingCall
}

// trailingCall is one ProviderTrailingCost call's arguments.
type trailingCall struct {
	plan    string
	since   time.Time
	exclude string
}

func (f *fakeTrailing) ProviderTrailingCost(_ context.Context, plan string, since time.Time, excludeRunID string) (money.Micros, int, error) {
	f.calls = append(f.calls, trailingCall{plan: plan, since: since, exclude: excludeRunID})
	if f.err != nil {
		return 0, 0, f.err
	}
	return f.mean, f.sample, nil
}

// settledOn is a built run whose one step, on model, cost cost.
func settledOn(model string, cost float64) Summary {
	return Summary{
		Outcome: OutcomeBuilt,
		Phase:   PhaseCommit,
		Branch:  BranchName(testTicket.BranchSegment(), "1-1"),
		Steps: []Step{{
			Phase: PhaseBuild, Round: 1, Model: model, Tokens: Usage{Cost: cost, Steps: 1},
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1),
			At:                finalizeNow.Add(-30 * time.Minute),
		}},
	}
}

// seededOn is seeded, with the run's provider plan named.
func seededOn(t *testing.T, plan string) fakeRecords {
	t.Helper()
	store := seeded(t)
	rec := store[testRunID]
	rec.Plan = plan
	store[testRunID] = rec
	return store
}

// finalizeRun is Finalize with the identity, run and attempt every test here
// names, so only the case under test is spelled out.
func finalizeRun(t *testing.T, store fakeRecords, reader ProviderCostReader, raw string, now time.Time) Record {
	t.Helper()
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: raw,
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	rec, err := Finalize(context.Background(), store, &fakeLedger{}, reader, in, now)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestFinalizeFlagsARunCostingWellAboveItsProvidersTrailingAverage(t *testing.T) {
	store := seededOn(t, "command-code")
	reader := &fakeTrailing{mean: money.Dollar, sample: 10}

	rec := finalizeRun(t, store, reader, encoded(t, settledOn("command-code/x", 2)), finalizeNow)
	want := "cost $2.0000 is 100% above the $1.0000 average of 10 of this provider's runs"
	if rec.CostDrift != want || store[testRunID].CostDrift != want {
		t.Fatalf("cost drift = %q (record %q), want %q", rec.CostDrift, store[testRunID].CostDrift, want)
	}
	if len(reader.calls) != 1 {
		t.Fatalf("the provider was asked %d times, want once", len(reader.calls))
	}
	call := reader.calls[0]
	if call.plan != "command-code" || call.exclude != testRunID || !call.since.Equal(finalizeNow.Add(-TrailingWindow)) {
		t.Fatalf("asked for %+v, want this run's plan over the trailing window with the run itself left out", call)
	}
}

// TestFinalizeFlagsOnlyARunPastTheDriftThreshold pins the 25% band itself, at
// its own boundary: a run exactly a quarter above the average is not flagged,
// and one cent of a micro past it is.
func TestFinalizeFlagsOnlyARunPastTheDriftThreshold(t *testing.T) {
	for name, tt := range map[string]struct {
		cost      float64
		wantDrift bool
	}{
		"well above the average": {2, true},
		"just past the band":     {1.2501, true},
		"exactly at the band":    {1.25, false},
		"inside the band":        {1.1, false},
		"below the average":      {0.5, false},
	} {
		t.Run(name, func(t *testing.T) {
			store := seededOn(t, "command-code")
			reader := &fakeTrailing{mean: money.Dollar, sample: 10}

			rec := finalizeRun(t, store, reader, encoded(t, settledOn("command-code/x", tt.cost)), finalizeNow)
			if got := rec.CostDrift != ""; got != tt.wantDrift {
				t.Fatalf("cost drift = %q at $%v against a $1 average, want flagged %v", rec.CostDrift, tt.cost, tt.wantDrift)
			}
			if store[testRunID].CostDrift != rec.CostDrift {
				t.Fatalf("recorded %q, want the returned %q", store[testRunID].CostDrift, rec.CostDrift)
			}
		})
	}
}

func TestFinalizeLeavesTheDriftFlagEmptyWithoutEnoughOfTheProvidersHistory(t *testing.T) {
	store := seededOn(t, "command-code")
	reader := &fakeTrailing{mean: money.Dollar, sample: minDriftSample - 1}

	rec := finalizeRun(t, store, reader, encoded(t, settledOn("command-code/x", 10)), finalizeNow)
	if rec.CostDrift != "" {
		t.Fatalf("cost drift = %q over %d settled runs, want none under %d", rec.CostDrift, reader.sample, minDriftSample)
	}
	if len(reader.calls) != 1 {
		t.Fatalf("the provider was asked %d times, want the mean to be what withheld the flag", len(reader.calls))
	}
}

func TestFinalizeLeavesTheDriftFlagEmptyWithoutAProviderToAsk(t *testing.T) {
	for name, tt := range map[string]struct {
		plan      string
		trail     *fakeTrailing
		wantAsked bool
	}{
		"no reader":                     {plan: "command-code"},
		"a history that cannot be read": {plan: "command-code", trail: &fakeTrailing{err: errors.New("firestore unreachable")}, wantAsked: true},
		"no provider on the record":     {trail: &fakeTrailing{mean: money.Dollar, sample: 10}},
	} {
		t.Run(name, func(t *testing.T) {
			store := seededOn(t, tt.plan)
			var reader ProviderCostReader
			if tt.trail != nil {
				reader = tt.trail
			}

			rec := finalizeRun(t, store, reader, encoded(t, settledOn("command-code/x", 10)), finalizeNow)
			if rec.CostDrift != "" {
				t.Fatalf("cost drift = %q, want none", rec.CostDrift)
			}
			if !rec.SettledAt.Equal(finalizeNow) {
				t.Fatalf("settled at %v, want the run still settled with the comparison withheld", rec.SettledAt)
			}
			if tt.trail != nil && (len(tt.trail.calls) > 0) != tt.wantAsked {
				t.Fatalf("the provider was asked %d times, want asked %v", len(tt.trail.calls), tt.wantAsked)
			}
		})
	}
}

func TestFinalizeNeverComparesARunThatNeverReachedAnAgent(t *testing.T) {
	store := seededOn(t, "command-code")
	reader := &fakeTrailing{mean: money.Dollar, sample: 10}

	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", RunResult: "failure", PRResult: "skipped"}
	rec, err := Finalize(context.Background(), store, &fakeLedger{}, reader, in, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CostDrift != "" || len(reader.calls) != 0 {
		t.Fatalf("cost drift = %q over %d reads, want a run with no cost left out of the comparison", rec.CostDrift, len(reader.calls))
	}
}

func TestFinalizeTagsTheRunWithTheTimeOfUseItsStepsFellIn(t *testing.T) {
	const priced, free = "command-code/deepseek/deepseek-v4.1-flash", "command-code/poolside/laguna-s-2.1-free"
	// A Monday, inside the priced card's own 01:00-04:00 UTC window.
	peak := time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
	// The same Monday, outside every window.
	offPeak := time.Date(2026, 9, 28, 5, 0, 0, 0, time.UTC)
	for name, tt := range map[string]struct {
		model string
		at    time.Time
		want  string
	}{
		"a step priced at peak":                  {priced, peak, TimeOfUsePeak},
		"a step priced off peak":                 {priced, offPeak, TimeOfUseOffPeak},
		"a plan that does not price by the hour": {free, peak, ""},
	} {
		t.Run(name, func(t *testing.T) {
			started, now := tt.at.Add(-time.Hour), tt.at.Add(time.Hour)
			store := fakeRecords{testRunID: NewRecord(testRunID, testTicket, started)}
			sum := Summary{
				Outcome: OutcomeBuilt, Phase: PhaseCommit, Branch: BranchName(testTicket.BranchSegment(), "1-1"),
				Steps: []Step{{
					Phase: PhaseBuild, Round: 1, Model: tt.model, Tokens: Usage{Cost: 1, Steps: 1},
					CompletionsObject: completionsObject("1-1", PhaseBuild, 1), At: tt.at,
				}},
				StartedAt: started,
			}

			rec := finalizeRun(t, store, nil, encoded(t, sum), now)
			if rec.TimeOfUse != tt.want {
				t.Fatalf("time of use = %q, want %q", rec.TimeOfUse, tt.want)
			}
			if store[testRunID].TimeOfUse != tt.want {
				t.Fatalf("recorded %q, want %q", store[testRunID].TimeOfUse, tt.want)
			}
		})
	}
}

func TestTimeOfUseIsTheBucketTheRunsOwnStepsFellIn(t *testing.T) {
	const priced = "command-code/deepseek/deepseek-v4.1-flash"
	peak := time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)    // a Monday, inside the card's 01:00-04:00 window
	offPeak := time.Date(2026, 9, 28, 5, 0, 0, 0, time.UTC) // the same Monday, outside every window
	weekend := time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC) // a Saturday, inside the hours but never at peak
	for name, tt := range map[string]struct {
		steps []Step
		want  string
	}{
		"a step priced at peak":                       {[]Step{{Model: priced, At: peak}}, TimeOfUsePeak},
		"a step priced off peak":                      {[]Step{{Model: priced, At: offPeak}}, TimeOfUseOffPeak},
		"a weekend step inside the hours":             {[]Step{{Model: priced, At: weekend}}, TimeOfUseOffPeak},
		"any peak step wins over the rest":            {[]Step{{Model: priced, At: offPeak}, {Model: priced, At: peak}}, TimeOfUsePeak},
		"a plan that does not price by the hour":      {[]Step{{Model: "command-code/poolside/laguna-s-2.1-free", At: peak}}, ""},
		"a model no plan prices":                      {[]Step{{Model: "nowhere/nothing", At: peak}}, ""},
		"a step whose time is unknown prices at peak": {[]Step{{Model: priced}}, TimeOfUsePeak},
		"no steps at all":                             {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := timeOfUse(tt.steps); got != tt.want {
				t.Fatalf("time of use = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSystemicClassifiesEveryStopReason reads every StopReason record.go
// declares, so a new one cannot go unclassified.
func TestSystemicClassifiesEveryStopReason(t *testing.T) {
	src, err := os.ReadFile("record.go")
	if err != nil {
		t.Fatal(err)
	}
	want := map[StopReason][2]bool{
		"credential-absent":   {true, true},
		"identity-mismatch":   {true, true},
		"allowance-exhausted": {true, true},
		"model-unavailable":   {true, false},
	}
	declared := regexp.MustCompile(`Stop\w+\s+StopReason = "([a-z-]+)"`).FindAllSubmatch(src, -1)
	if len(declared) < len(want) {
		t.Fatalf("found %d stop reasons in record.go", len(declared))
	}
	for _, m := range declared {
		reason := StopReason(m[1])
		notify, trips := Record{StopReason: reason}.Systemic()
		if w := want[reason]; notify != w[0] || trips != w[1] {
			t.Errorf("%s: notify %v, trips %v; want %v, %v", reason, notify, trips, w[0], w[1])
		}
	}
	if notify, trips := (Record{}).Systemic(); notify || trips {
		t.Fatal("a run with no stop notified")
	}
}
