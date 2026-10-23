package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/alvintoh/forge-wingman/internal/dispatcher"
	"github.com/alvintoh/forge-wingman/internal/runner"
	"github.com/alvintoh/forge-wingman/internal/store"
)

type fakeNotices struct {
	raised []dispatcher.Notice
	err    error
}

func (n *fakeNotices) Raise(_ context.Context, notice dispatcher.Notice, _ time.Time) error {
	if n.err != nil {
		return n.err
	}
	n.raised = append(n.raised, notice)
	return nil
}

type fakeBreaker struct {
	tripped []string
	err     error
}

func (b *fakeBreaker) Trip(_ context.Context, runID, class string, _ time.Time) error {
	if b.err != nil {
		return b.err
	}
	b.tripped = append(b.tripped, runID+" "+class)
	return nil
}

func TestRaiseSystemicNoticesAndTripsByClass(t *testing.T) {
	const runURL = "https://github.com/o/r/actions/runs/7"
	for name, tt := range map[string]struct {
		reason        runner.StopReason
		raised, trips bool
	}{
		"an identity mismatch notifies and trips": {runner.StopIdentityMismatch, true, true},
		"a model outage notifies only":            {runner.StopModelUnavailable, true, false},
		"an agent failure does neither":           {runner.StopAgentExit, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			notices, breaker := &fakeNotices{}, &fakeBreaker{}
			rec := runner.Record{RunID: "ABC-1", StopReason: tt.reason, RunURL: runURL, TicketBody: "secret body"}
			raiseSystemic(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), notices, breaker, rec, "42-1", time.Now())
			if tt.raised != (len(notices.raised) == 1) || tt.trips != (len(breaker.tripped) == 1) {
				t.Fatalf("raised %v, tripped %v", notices.raised, breaker.tripped)
			}
			if tt.raised && notices.raised[0] != (dispatcher.Notice{ID: "ABC-1-42-1", Class: string(tt.reason), Link: runURL}) {
				t.Fatalf("notice = %+v", notices.raised[0])
			}
		})
	}
}

func TestRaiseSystemicTripsTheBreakerWhenTheNoticeFails(t *testing.T) {
	var log strings.Builder
	breaker := &fakeBreaker{}
	rec := runner.Record{RunID: "ABC-1", StopReason: runner.StopCredentialAbsent}
	raiseSystemic(context.Background(), slog.New(slog.NewTextHandler(&log, nil)), &fakeNotices{err: errors.New("firestore down")}, breaker, rec, "42-1", time.Now())
	if len(breaker.tripped) != 1 || !strings.Contains(log.String(), "noticeNotRaised") {
		t.Fatalf("tripped %v, log %q", breaker.tripped, log.String())
	}
}

func TestRaiseSystemicLogsABreakerItCouldNotTrip(t *testing.T) {
	var log strings.Builder
	rec := runner.Record{RunID: "ABC-1", StopReason: runner.StopAllowanceExhausted}
	raiseSystemic(context.Background(), slog.New(slog.NewTextHandler(&log, nil)), &fakeNotices{}, &fakeBreaker{err: errors.New("firestore down")}, rec, "42-1", time.Now())
	if !strings.Contains(log.String(), "breakerNotTripped") || strings.Contains(log.String(), "msg=breakerTripped") {
		t.Fatalf("log %q, want the failed trip reported and no trip claimed", log.String())
	}
}

func TestRaiseSystemicKeysANoticeByTheRunAloneForAnUnsafeAttemptID(t *testing.T) {
	notices := &fakeNotices{}
	rec := runner.Record{RunID: "ABC-1", StopReason: runner.StopIdentityMismatch}
	raiseSystemic(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), notices, &fakeBreaker{}, rec, "42/../1", time.Now())
	if len(notices.raised) != 1 || notices.raised[0].ID != "ABC-1" {
		t.Fatalf("raised %+v, want the run id alone", notices.raised)
	}
}

// failingRecords holds records it reads but cannot write.
type failingRecords struct{ recordReader }

func (failingRecords) PutRecord(context.Context, string, runner.Record) error {
	return errors.New("firestore refused the write")
}

type noLedger struct{}

func (noLedger) Settle(context.Context, string, *int) error { return nil }

func TestFinalizeRaisesASystemicStopWhoseRecordCannotBeWritten(t *testing.T) {
	notices, breaker := &fakeNotices{}, &fakeBreaker{}
	env := map[string]string{"WINGMAN_ACCOUNT": "work-account", "GITHUB_REPOSITORY_OWNER": "octo"}
	in := runner.FinalizeInput{RunID: "ABC-1", AttemptID: "42-1", Identity: runner.IdentityFromEnv(func(k string) string { return env[k] })}
	_, err := finalize(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		failingRecords{recordReader{"ABC-1": {RunID: "ABC-1"}}}, noLedger{}, nil, notices, breaker, in, time.Now())
	if err == nil {
		t.Fatal("the failed write was dropped")
	}
	if len(notices.raised) != 1 || len(breaker.tripped) != 1 {
		t.Fatalf("raised %v, tripped %v, want the identity stop raised and tripped", notices.raised, breaker.tripped)
	}
}

// recordPRFailure seeds a claimed run whose build finished and whose PR job
// stopped for prStopReason, runs record against it, and returns the record
// job's error beside the run's stored row.
func recordPRFailure(t *testing.T, prior map[string]any, prStopReason string) (map[string]any, error) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	runID := "record-conflict-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() {
		_, _ = client.Collection("runs").Doc(runID).Delete(context.Background())
		_ = client.Close()
	})
	ticket := runner.Ticket{ID: "ABC-1", Title: "t", Size: "S", Body: "b"}
	if err := store.NewRecords(client).PutRecord(ctx, runID, runner.NewRecord(runID, ticket, time.Now())); err != nil {
		t.Fatal(err)
	}
	row := map[string]any{"state": "claimed", "claimed_at": time.Now()}
	maps.Copy(row, prior)
	if _, err := client.Collection("runs").Doc(runID).Set(ctx, row, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	summary, err := runner.Summary{
		Outcome: runner.OutcomeBuilt, Phase: runner.PhaseCommit, StartedAt: time.Now().Add(-time.Hour),
		Branch: runner.BranchName(ticket.BranchSegment(), "42-1"),
		Steps: []runner.Step{{Phase: runner.PhaseBuild, Round: 1, Model: "p/m", Tokens: runner.Usage{Cost: 0.5, Steps: 1},
			CompletionsObject: "completions/42-1-build-1.jsonl", At: time.Now().Add(-30 * time.Minute)}},
	}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	getenv := map[string]string{"GOOGLE_CLOUD_PROJECT": "p", "GITHUB_RUN_ID": "42", "GITHUB_RUN_ATTEMPT": "1",
		"WINGMAN_ACCOUNT": "octo", "GITHUB_REPOSITORY_OWNER": "octo"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recordErr := run(ctx, logger, []string{"record", "-run-id", runID, "-summary", summary,
		"-run-result", jobSuccess, "-pr-result", "failure", "-pr-stop-reason", prStopReason},
		func(k string) string { return getenv[k] })
	snap, err := client.Collection("runs").Doc(runID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return snap.Data(), recordErr
}

func TestRecordRequeuesARunWhoseRebaseConflicted(t *testing.T) {
	row, err := recordPRFailure(t, nil, string(runner.StopRebaseConflict))
	if err != nil {
		t.Fatalf("err = %v, want a requeued run not reported as a failed one", err)
	}
	if row["state"] != "queued" || row["attempt"] != int64(1) {
		t.Fatalf("state/attempt = %v/%v, want queued/1", row["state"], row["attempt"])
	}
	if row["prior_settled_provider_cost_micros"] != int64(500_000) {
		t.Fatalf("prior settled cost = %v, want the failed attempt's 500000 carried", row["prior_settled_provider_cost_micros"])
	}
	if _, ok := row["outcome"]; ok {
		t.Fatalf("outcome = %v survived the requeue", row["outcome"])
	}
}

func TestRecordFailsARunWhoseRebaseConflictedAfterItsRetry(t *testing.T) {
	row, err := recordPRFailure(t, map[string]any{"attempt": int64(1)}, string(runner.StopRebaseConflict))
	if !errors.Is(err, errRunFailed) {
		t.Fatalf("err = %v, want errRunFailed", err)
	}
	if row["state"] != "claimed" || row["outcome"] != string(runner.OutcomeInfraFailure) || row["stop_reason"] != string(runner.StopRebaseConflict) {
		t.Fatalf("state/outcome/stop_reason = %v/%v/%v, want claimed/infra-failure/rebase-conflict", row["state"], row["outcome"], row["stop_reason"])
	}
}

func TestRecordFailsAnAbandonedRunWhoseRebaseConflictedWithoutRequeueingIt(t *testing.T) {
	row, err := recordPRFailure(t, map[string]any{"state": "abandoned"}, string(runner.StopRebaseConflict))
	if !errors.Is(err, errRunFailed) {
		t.Fatalf("err = %v, want errRunFailed", err)
	}
	if row["state"] != "abandoned" || row["stop_reason"] != string(runner.StopRebaseConflict) {
		t.Fatalf("state/stop_reason = %v/%v, want abandoned/rebase-conflict", row["state"], row["stop_reason"])
	}
	if _, ok := row["attempt"]; ok {
		t.Fatalf("attempt = %v, want the run left for an operator's requeue", row["attempt"])
	}
}

func TestRecordLeavesAFailedPRJobThatIsNotAConflictToFail(t *testing.T) {
	row, err := recordPRFailure(t, nil, "")
	if !errors.Is(err, errRunFailed) {
		t.Fatalf("err = %v, want errRunFailed", err)
	}
	if row["state"] != "claimed" || row["stop_reason"] != string(runner.StopPRJob) {
		t.Fatalf("state/stop_reason = %v/%v, want claimed/%s", row["state"], row["stop_reason"], runner.StopPRJob)
	}
}

func TestParseClaimAttempt(t *testing.T) {
	if got, err := parseClaimAttempt(""); got != nil || err != nil {
		t.Fatalf("empty = %v, %v, want nil: a run dispatched without an attempt", got, err)
	}
	if got, err := parseClaimAttempt("0"); err != nil || got == nil || *got != 0 {
		t.Fatalf("0 = %v, %v, want attempt 0, which is a real attempt", got, err)
	}
	if got, err := parseClaimAttempt("2"); err != nil || got == nil || *got != 2 {
		t.Fatalf("2 = %v, %v, want attempt 2", got, err)
	}
	for _, bad := range []string{"-1", "x", "1.5", " 1", "2147483648"} {
		if _, err := parseClaimAttempt(bad); err == nil {
			t.Fatalf("%q parsed, want an error", bad)
		}
	}
}
