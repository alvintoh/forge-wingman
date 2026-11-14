package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

// fakeFixRecorder records what the record step writes, in order.
type fakeFixRecorder struct {
	calls     []string
	reason    runner.WaitReason
	detail    string
	merged    []runner.ReviewFixRound
	waitErr   error
	mergeErr  error
	waitRunID string
}

func (f *fakeFixRecorder) SetWaitReason(_ context.Context, id string, reason runner.WaitReason, detail string) error {
	f.calls = append(f.calls, "wait")
	f.waitRunID, f.reason, f.detail = id, reason, detail
	return f.waitErr
}

func (f *fakeFixRecorder) MergeFixRounds(_ context.Context, _ string, rounds []runner.ReviewFixRound) error {
	f.calls = append(f.calls, "merge")
	f.merged = rounds
	return f.mergeErr
}

func loopRound(n int, at time.Time) runner.ReviewFixRound {
	return runner.ReviewFixRound{
		Step: runner.Step{Phase: runner.PhaseFix, Round: n, Model: "p/m", At: at,
			CompletionsObject: "completions/42-1-fix-" + strconv.Itoa(n) + ".jsonl"},
		Findings: []string{"a finding"}, Reviewed: true, Resolved: 1,
	}
}

func loopState(rounds ...runner.ReviewFixRound) *runner.FixRoundsState {
	s := runner.NewFixRoundsState(len(rounds), len(rounds), time.Now(), "", nil, nil)
	for _, r := range rounds {
		s = s.WithRound(r)
	}
	return &s
}

func TestWriteFixLoopRecordsTheStopAndMergesTheRounds(t *testing.T) {
	now := time.Now()
	rec := &fakeFixRecorder{}
	in := fixLoopRecord{RunID: "run-1", AttemptID: "42-1", State: loopState(loopRound(1, now), loopRound(2, now)),
		End: runner.FixLoopEnd{Stop: runner.FixStopCap}}
	if err := writeFixLoop(context.Background(), quietLogger(), rec, in, now); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.calls, []string{"wait", "merge"}) || rec.waitRunID != "run-1" {
		t.Fatalf("calls %v for run %q, want the wait reason then the merge for run-1", rec.calls, rec.waitRunID)
	}
	if rec.reason != "fix-rounds-stopped" || rec.detail != "fix loop ended: round-cap" {
		t.Fatalf("wait = %q, %q", rec.reason, rec.detail)
	}
	if len(rec.merged) != 2 || rec.merged[1].Step.Round != 2 {
		t.Fatalf("merged %+v, want both rounds", rec.merged)
	}
}

func TestWriteFixLoopWritesNoWaitReasonForALoopThatDidNotStop(t *testing.T) {
	now := time.Now()
	rec := &fakeFixRecorder{}
	in := fixLoopRecord{RunID: "run-1", AttemptID: "42-1", State: loopState(loopRound(1, now)), End: runner.FixLoopEnd{AwaitReason: "clean"}}
	if err := writeFixLoop(context.Background(), quietLogger(), rec, in, now); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.calls, []string{"merge"}) {
		t.Fatalf("calls = %v, want the merge alone", rec.calls)
	}
}

func TestWriteFixLoopMergesNothingWhenNoRoundRan(t *testing.T) {
	rec := &fakeFixRecorder{}
	for _, state := range []*runner.FixRoundsState{nil, loopState()} {
		in := fixLoopRecord{RunID: "run-1", AttemptID: "42-1", State: state, End: runner.FixLoopEnd{Stop: runner.FixStopBudget}}
		if err := writeFixLoop(context.Background(), quietLogger(), rec, in, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(rec.calls, []string{"wait", "wait"}) {
		t.Fatalf("calls = %v, want the wait reason twice and no merge", rec.calls)
	}
}

func TestWriteFixLoopRefusesARoundThatIsNotThisRunsAndStillRecordsTheStop(t *testing.T) {
	now := time.Now()
	foreign := loopRound(1, now)
	foreign.Step.CompletionsObject = "completions/42-2-fix-1.jsonl"
	rec := &fakeFixRecorder{}
	in := fixLoopRecord{RunID: "run-1", AttemptID: "42-1", State: loopState(foreign), End: runner.FixLoopEnd{PushReason: "lease-lost"}}
	err := writeFixLoop(context.Background(), quietLogger(), rec, in, now)
	if err == nil || !strings.Contains(err.Error(), "fix round 1") {
		t.Fatalf("err = %v, want the round refused", err)
	}
	if !slices.Equal(rec.calls, []string{"wait"}) || rec.detail != "fix push refused: lease-lost" {
		t.Fatalf("calls %v, detail %q; want the stop recorded and no merge", rec.calls, rec.detail)
	}
}

func TestWriteFixLoopRefusesARoundFromTheFuture(t *testing.T) {
	now := time.Now()
	rec := &fakeFixRecorder{}
	in := fixLoopRecord{RunID: "run-1", AttemptID: "42-1", State: loopState(loopRound(1, now.Add(time.Hour)))}
	if err := writeFixLoop(context.Background(), quietLogger(), rec, in, now); err == nil || len(rec.calls) != 0 {
		t.Fatalf("err = %v, calls = %v; want the round refused before any write", err, rec.calls)
	}
}

func TestWriteFixLoopStopsAtTheFirstWriteThatFails(t *testing.T) {
	now := time.Now()
	boom := errors.New("boom")
	rec := &fakeFixRecorder{waitErr: boom}
	in := fixLoopRecord{RunID: "run-1", AttemptID: "42-1", State: loopState(loopRound(1, now)), End: runner.FixLoopEnd{Stop: runner.FixStopCap}}
	if err := writeFixLoop(context.Background(), quietLogger(), rec, in, now); !errors.Is(err, boom) || len(rec.calls) != 1 {
		t.Fatalf("err = %v, calls = %v; want the wait failure and no merge", err, rec.calls)
	}
	rec = &fakeFixRecorder{mergeErr: boom}
	if err := writeFixLoop(context.Background(), quietLogger(), rec, in, now); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the merge failure", err)
	}
}

func writeState(t *testing.T, s *runner.FixRoundsState) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fix-state.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadFixStateParsesWhatTheLastRoundWrote(t *testing.T) {
	got, err := readFixState(writeState(t, loopState(loopRound(1, time.Now()))))
	if err != nil || len(got.Rounds) != 1 || got.Round != 1 {
		t.Fatalf("state = %+v, err = %v, want the one round", got, err)
	}
}

func TestReadFixStateRefusesWhatItCannotTrust(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.json")
	valid, err := os.ReadFile(writeState(t, loopState()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, append(valid, strings.Repeat(" ", maxFixStateBytes)...), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"round":0,"started_at":"2026-11-14T00:00:00Z","extra":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing": filepath.Join(dir, "none.json"), "over the size cap": big, "a field the runner does not write": unknown} {
		if _, err := readFixState(path); err == nil {
			t.Errorf("%s: readFixState accepted it", name)
		}
	}
}

func recordFixEnv(output string, extra map[string]string) func(string) string {
	base := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo", "GITHUB_OUTPUT": output}
	return func(k string) string {
		if v, ok := extra[k]; ok {
			return v
		}
		return base[k]
	}
}

func TestRunDispatchesRecordFixRoundsAndNeedsTheRunsEnvironment(t *testing.T) {
	err := run(context.Background(), quietLogger(), []string{"record-fix-rounds"}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "missing environment") {
		t.Fatalf("err = %v, want the missing environment named", err)
	}
	err = run(context.Background(), quietLogger(), []string{"record-fix-rounds"}, recordFixEnv("", nil))
	if err == nil || !strings.Contains(err.Error(), "-run-id") {
		t.Fatalf("err = %v, want the missing -run-id named", err)
	}
}

func TestParseRecordFixRoundsReadsEachJobsOutput(t *testing.T) {
	e, err := loadEnv(recordFixEnv("", nil))
	if err != nil {
		t.Fatal(err)
	}
	in, state, err := parseRecordFixRounds(quietLogger(), e, []string{"-run-id", "run-1", "-state", "s.json",
		"-round-reason", "commit", "-round-stop", "round-cap", "-round-detail", "rd",
		"-push-reason", "lease-lost", "-push-detail", "pd", "-await-reason", "timeout", "-await-detail", "ad"})
	if err != nil {
		t.Fatal(err)
	}
	want := fixLoopRecord{RunID: "run-1", AttemptID: "42-1", End: runner.FixLoopEnd{
		RoundReason: "commit", Stop: runner.FixStopCap, RoundNote: "rd", PushReason: "lease-lost", PushNote: "pd", AwaitReason: "timeout", AwaitNote: "ad"}}
	if state != "s.json" || in != want {
		t.Fatalf("state %q, record %+v; want s.json and %+v", state, in, want)
	}
}

func TestRecordFixRoundsRefusesAnotherAccountsIdentity(t *testing.T) {
	err := run(context.Background(), quietLogger(), []string{"record-fix-rounds", "-run-id", "run-1"},
		recordFixEnv("", map[string]string{"WINGMAN_ACCOUNT": "mallory"}))
	if err == nil || strings.Contains(err.Error(), "-run-id") || strings.Contains(err.Error(), "firestore") {
		t.Fatalf("err = %v, want the identity refused before anything is read or written", err)
	}
}

func TestRecordFixRoundsRefusesAnUnreadableStateBeforeWriting(t *testing.T) {
	err := run(context.Background(), quietLogger(), []string{"record-fix-rounds", "-run-id", "run-1", "-state", filepath.Join(t.TempDir(), "none.json")},
		recordFixEnv("", nil))
	if err == nil || !strings.Contains(err.Error(), "fix-round state") {
		t.Fatalf("err = %v, want the state refused", err)
	}
}

func TestRecordFixRoundsWritesTheStopAndMergesTheRoundsAgainstTheEmulator(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "record-fix-rounds-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("runs").Doc(runID).Delete(context.Background())
		_ = client.Close()
	})
	records := store.NewRecords(client)
	seed := runner.NewRecord(runID, runner.Ticket{ID: "ABC-12"}, time.Now().Add(-time.Hour))
	seed.SettledAt = time.Now()
	if err := records.PutRecord(ctx, runID, seed); err != nil {
		t.Fatal(err)
	}
	round := loopRound(1, time.Now())
	round.Step.Tokens = runner.Usage{Input: 10, Steps: 1}
	state := writeState(t, loopState(round))
	args := []string{"record-fix-rounds", "-run-id", runID, "-state", state, "-round-stop", "round-cap"}
	for range 2 {
		if err := run(ctx, quietLogger(), args, recordFixEnv("", nil)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := records.GetRecord(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WaitReason != "fix-rounds-stopped" || got.WaitDetail != "fix loop ended: round-cap" {
		t.Fatalf("wait = %q, %q", got.WaitReason, got.WaitDetail)
	}
	if len(got.FixRounds) != 1 || len(got.Steps) != 1 || got.Tokens.Input != 10 {
		t.Fatalf("%d rounds, %d steps, tokens %+v after running twice; want 1, 1 and 10 input", len(got.FixRounds), len(got.Steps), got.Tokens)
	}
}
