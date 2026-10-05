package dispatcher

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

// fakeVerdicts reports the verdict configured for each provider.
type fakeVerdicts struct {
	verdicts map[string]providers.Verdict
	err      error
}

func (v fakeVerdicts) Verdict(_ context.Context, provider string) (providers.Verdict, error) {
	if v.err != nil {
		return "", v.err
	}
	return v.verdicts[provider], nil
}

var verdictConfig = Config{Repos: buildConfig.Repos, Budget: buildConfig.Budget, Model: "command-code/x"}

// fakeRecorder keeps each verdict it is asked to write.
type fakeRecorder struct {
	runs     []string
	verdicts []providers.Verdict
	err      error
}

func (r *fakeRecorder) RecordVerdict(_ context.Context, runID string, v providers.Verdict) error {
	r.runs = append(r.runs, runID)
	r.verdicts = append(r.verdicts, v)
	return r.err
}

func verdictPoll(t *testing.T, plans ProviderVerdicts, logs *bytes.Buffer) (Result, *fakeWorkflow, error) {
	t.Helper()
	return recordedPoll(t, plans, nil, logs)
}

func recordedPoll(t *testing.T, plans ProviderVerdicts, rec VerdictRecorder, logs *bytes.Buffer) (Result, *fakeWorkflow, error) {
	t.Helper()
	q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Priority: 1}}}
	w := &fakeWorkflow{}
	deps := pollDeps(fakeSource{}, q, w)
	deps.Plans = plans
	deps.Verdicts = rec
	if logs != nil {
		deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	res, err := Poll(context.Background(), deps, verdictConfig)
	return res, w, err
}

func TestPollClaimsTheRunWithTheConfiguredProvidersVerdict(t *testing.T) {
	plans := fakeVerdicts{verdicts: map[string]providers.Verdict{
		"command-code": providers.VerdictRestricted, "openrouter": providers.VerdictAllowed,
	}}
	_, w, err := verdictPoll(t, plans, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.claims) != 1 || w.claims[0].Verdict != "restricted" {
		t.Fatalf("claims = %+v, want the command-code verdict restricted", w.claims)
	}
}

func TestPollRecordsUnknownAndStillDispatchesWhenTheVerdictLookupFails(t *testing.T) {
	var logs bytes.Buffer
	res, w, err := verdictPoll(t, fakeVerdicts{err: errFirestore}, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Dispatched) != 1 || len(w.claims) != 1 || w.claims[0].Verdict != "unknown" {
		t.Fatalf("dispatched %v, claims %+v, want the run started with verdict unknown", res.Dispatched, w.claims)
	}
	if !strings.Contains(logs.String(), "providerVerdictLookupFailed") {
		t.Fatalf("logs = %s, want the failed lookup logged", logs.String())
	}
}

func TestPollLeavesTheVerdictEmptyWhenNoVerdictSourceIsWired(t *testing.T) {
	res, w, err := verdictPoll(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Dispatched) != 1 || w.claims[0].Verdict != "" {
		t.Fatalf("dispatched %v, claims %+v, want a run with no verdict", res.Dispatched, w.claims)
	}
}

func TestPollLogsTheVerdictOnTheDispatchedRun(t *testing.T) {
	var logs bytes.Buffer
	plans := fakeVerdicts{verdicts: map[string]providers.Verdict{"command-code": providers.VerdictAllowed}}
	if _, _, err := verdictPoll(t, plans, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "runDispatched") || !strings.Contains(logs.String(), "verdict=allowed") {
		t.Fatalf("logs = %s, want runDispatched carrying verdict=allowed", logs.String())
	}
}

func TestPollWritesTheVerdictOntoTheRunBeforeDispatching(t *testing.T) {
	rec := &fakeRecorder{}
	plans := fakeVerdicts{verdicts: map[string]providers.Verdict{"command-code": providers.VerdictRestricted}}
	if _, _, err := recordedPoll(t, plans, rec, nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.runs) != 1 || rec.runs[0] != "run-a" || rec.verdicts[0] != "restricted" {
		t.Fatalf("recorded runs %v verdicts %v, want run-a restricted", rec.runs, rec.verdicts)
	}
}

func TestPollStillDispatchesAndLogsWhenTheVerdictWriteFails(t *testing.T) {
	var logs bytes.Buffer
	rec := &fakeRecorder{err: errFirestore}
	plans := fakeVerdicts{verdicts: map[string]providers.Verdict{"command-code": providers.VerdictAllowed}}
	res, w, err := recordedPoll(t, plans, rec, &logs)
	if err != nil || len(res.Dispatched) != 1 || len(w.claims) != 1 {
		t.Fatalf("dispatched %v, claims %v, err %v, want the run started anyway", res.Dispatched, w.claims, err)
	}
	if !strings.Contains(logs.String(), "verdictRecordFailed") || !strings.Contains(logs.String(), "run=run-a") {
		t.Fatalf("logs = %s, want verdictRecordFailed naming the run", logs.String())
	}
}

func TestPollWritesNothingWhenNoRecorderIsWired(t *testing.T) {
	plans := fakeVerdicts{verdicts: map[string]providers.Verdict{"command-code": providers.VerdictAllowed}}
	res, _, err := recordedPoll(t, plans, nil, nil)
	if err != nil || len(res.Dispatched) != 1 {
		t.Fatalf("dispatched %v, err %v", res.Dispatched, err)
	}
}

func TestPollWritesNoVerdictWhenNoneWasLookedUp(t *testing.T) {
	rec := &fakeRecorder{}
	if _, _, err := recordedPoll(t, nil, rec, nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.runs) != 0 {
		t.Fatalf("recorded %v, want no write for an empty verdict", rec.runs)
	}
}
