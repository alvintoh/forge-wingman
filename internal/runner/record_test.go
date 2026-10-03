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
			Phase: PhaseBuild, Round: 1, Model: "opencode/big-pickle",
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1),
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

			if _, err := Finalize(context.Background(), store, &fakeLedger{}, in, finalizeNow); err != nil {
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
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, in, finalizeNow); err != nil {
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
			rec, err := Finalize(context.Background(), tt.store, &fakeLedger{}, in, finalizeNow)
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
	rec, err := Finalize(context.Background(), fakeRecords{testRunID: ticketless}, &fakeLedger{}, in, finalizeNow)
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
	if _, err := Finalize(context.Background(), unreadableRecords{}, &fakeLedger{}, in, finalizeNow); err == nil {
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
			if _, err := Finalize(context.Background(), store, &fakeLedger{}, in, finalizeNow); err != nil {
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
			Model:             "opencode/big-pickle",
			Tokens:            Usage{Input: 10, Output: 2, CacheRead: 90, Cost: 0.5, Steps: 1},
			DurationMS:        1200,
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1),
		}},
		EditedFiles:  []string{"version.go"},
		DiffLines:    DiffLines{Added: 3},
		DurationsMS:  map[string]int64{"build": 1200},
		RuleStackSHA: testSHA,
		Branch:       BranchName(testTicket.BranchSegment(), "1-1"),
		StartedAt:    started,
	}
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum), RunResult: "success",
		PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	got := store[testRunID]
	if len(got.Steps) != 1 || got.Steps[0] != sum.Steps[0] || got.Tokens != sum.Steps[0].Tokens ||
		got.DiffLines != sum.DiffLines || got.RuleStackSHA != testSHA || got.Branch != sum.Branch ||
		got.DurationsMS["build"] != 1200 || !got.StartedAt.Equal(seededAt) || len(got.EditedFiles) != 1 ||
		got.Phase != PhasePR {
		t.Fatalf("record = %+v", got)
	}
}

func TestFinalizeSumsTokensAcrossSteps(t *testing.T) {
	sum := Summary{
		Outcome: OutcomeBuilt,
		Phase:   PhaseCommit,
		Branch:  BranchName(testTicket.BranchSegment(), "1-1"),
		Steps: []Step{
			{Phase: PhaseBuild, Round: 1, Model: "opencode/big-pickle", Tokens: Usage{Input: 10, Output: 2, Steps: 1},
				CompletionsObject: completionsObject("1-1", PhaseBuild, 1)},
			{Phase: PhaseBuild, Round: 2, Model: "opencode/big-pickle", Tokens: Usage{Input: 5, Output: 1, Steps: 1},
				CompletionsObject: completionsObject("1-1", PhaseBuild, 2)},
		},
	}
	store := seeded(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, in, finalizeNow); err != nil {
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
		if _, err := Finalize(context.Background(), store, &fakeLedger{}, s.in, finalizeNow); err != nil {
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
	rec, err := Finalize(context.Background(), store, &fakeLedger{}, in, finalizeNow)
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
			Phase: PhaseBuild, Round: 1, Model: "opencode/big-pickle",
			Tokens:            Usage{Input: 10, Output: 2, Cost: 0.123456, Steps: 1},
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1),
		}},
		DurationsMS: map[string]int64{"build": 90_000}, // 1.5 minutes, rounds up to 2
	}
	ledger := &fakeLedger{}
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, ledger, in, finalizeNow); err != nil {
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

func TestFinalizeZeroesSettledRunnerMinutesForAPublicTarget(t *testing.T) {
	store := seeded(t) // Private defaults to false
	sum := Summary{
		Outcome: OutcomeBuilt, Phase: PhaseCommit, Branch: BranchName(testTicket.BranchSegment(), "1-1"),
		Steps: []Step{{Phase: PhaseBuild, Round: 1, Model: "opencode/big-pickle", Tokens: Usage{Cost: 1},
			CompletionsObject: completionsObject("1-1", PhaseBuild, 1)}},
		DurationsMS: map[string]int64{"build": 120_000},
	}
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, sum),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, in, finalizeNow); err != nil {
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
	if _, err := Finalize(context.Background(), store, ledger, in, finalizeNow); err == nil {
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
			rec, err := Finalize(context.Background(), tt.store, &fakeLedger{}, in, finalizeNow)
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

func TestFinalizeCarriesTheProviderVerdictFromTheExistingRecord(t *testing.T) {
	store := seeded(t)
	rec := store[testRunID]
	rec.ProviderVerdict = providers.VerdictRestricted
	store[testRunID] = rec
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: "",
		RunResult: "failure", PRResult: "skipped"}
	if _, err := Finalize(context.Background(), store, &fakeLedger{}, in, finalizeNow); err != nil {
		t.Fatal(err)
	}
	if got := store[testRunID].ProviderVerdict; got != "restricted" {
		t.Fatalf("provider verdict = %q, want restricted carried from the record, not the summary", got)
	}
}
