package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alvintoh/forge-wingman/internal/providers"
)

const (
	flashModel = "p/flash"
	mimoModel  = "p/mimo"
)

// withLadder makes flashModel and then mimoModel plan p's escalation ladder
// until t ends, since the embedded configuration carries none; every other plan
// has no ladder.
func withLadder(t *testing.T) {
	t.Helper()
	saved := modelTier
	modelTier = func(model string) providers.Tier {
		switch {
		case model == flashModel:
			return providers.Tier{Next: mimoModel, Laddered: true}
		case model == mimoModel:
			return providers.Tier{Rung: 1, Laddered: true}
		case strings.HasPrefix(model, "p/"):
			return providers.Tier{Laddered: true}
		}
		return providers.Tier{}
	}
	t.Cleanup(func() { modelTier = saved })
}

// stoppedIn is a run that stopped with outcome and reason in phase, that
// phase's agent having started on model, carrying detail.
func stoppedIn(phase Phase, outcome Outcome, reason StopReason, model, detail string) Summary {
	return Summary{
		Outcome: outcome, StopReason: reason, StopDetail: detail, Phase: phase,
		Branch: BranchName(testTicket.BranchSegment(), "1-1"),
		Steps: []Step{{
			Phase: phase, Round: 1, Model: model, Tokens: Usage{Steps: 1},
			CompletionsObject: completionsObject("1-1", phase, 1),
			At:                finalizeNow.Add(-30 * time.Minute),
		}},
	}
}

// attemptRecords is a record store that also archives ended attempts.
type attemptRecords struct {
	fakeRecords
	attempts map[int]Record
	err      error
	// reads, when set, records each attempt asked for.
	reads *[]int
}

func (a attemptRecords) GetAttempt(_ context.Context, _ string, n int) (Record, error) {
	if a.reads != nil {
		*a.reads = append(*a.reads, n)
	}
	if a.err != nil {
		return Record{}, a.err
	}
	r, ok := a.attempts[n]
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	return r, nil
}

func finalizeStop(t *testing.T, store RecordStore, attempt int, s Summary) Record {
	t.Helper()
	withLadder(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", Summary: encoded(t, s),
		RunResult: "failure", PRResult: "skipped", Attempt: &attempt}
	rec, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestFinalizeNamesTheNextTierWhenTheBuildAgentFails(t *testing.T) {
	rec := finalizeStop(t, seeded(t), 0, stoppedIn(PhaseBuild, OutcomeAgentFailed, StopAgentExit, flashModel, "agent exited 1"))
	want := "Next model tier: " + mimoModel + ". A `model:" + mimoModel +
		"` label on the ticket chooses it, and a retry then runs once on that tier.\n\nagent exited 1"
	if rec.StopDetail != want {
		t.Fatalf("stop detail = %q, want %q", rec.StopDetail, want)
	}
	if rec.Outcome != OutcomeAgentFailed || rec.StopReason != StopAgentExit {
		t.Fatalf("outcome = %s/%s, want the build's agent-failed/agent-exit", rec.Outcome, rec.StopReason)
	}
}

func TestFinalizeSaysNoTierIsConfiguredAboveAModelOffTheLadder(t *testing.T) {
	rec := finalizeStop(t, seeded(t), 0, stoppedIn(PhaseBuild, OutcomeAgentFailed, StopAgentTimeout, "p/x", ""))
	if want := "No model tier is configured above p/x."; rec.StopDetail != want {
		t.Fatalf("stop detail = %q, want %q", rec.StopDetail, want)
	}
}

func TestFinalizeLeavesTheDetailAsItIsWhenThePlanHasNoLadder(t *testing.T) {
	rec := finalizeStop(t, seeded(t), 0, stoppedIn(PhaseBuild, OutcomeAgentFailed, StopAgentExit, "q/x", "agent exited 1"))
	if rec.StopDetail != "agent exited 1" {
		t.Fatalf("stop detail = %q, want the build's own", rec.StopDetail)
	}
}

func TestFinalizeNamesNoTierForAStopThatIsNotABuildAgentFailure(t *testing.T) {
	for name, s := range map[string]Summary{
		"an exhausted allowance": stoppedIn(PhaseBuild, OutcomeBudgetStop, StopAllowanceExhausted, flashModel, "out"),
		"an unavailable model":   stoppedIn(PhaseBuild, OutcomeInfraFailure, StopModelUnavailable, flashModel, "out"),
		"checks that cannot run": stoppedIn(PhaseBuild, OutcomeInfraFailure, StopChecksRun, flashModel, "out"),
		"a formatter gate":       stoppedIn(PhaseBuild, OutcomeStopped, StopFormatterOutsideDiff, flashModel, "out"),
		"a spent build budget":   stoppedIn(PhaseBuild, OutcomeStopped, StopRunBudget, flashModel, "out"),
		"a plan agent failure":   stoppedIn(PhasePlan, OutcomeAgentFailed, StopAgentExit, flashModel, "out"),
	} {
		t.Run(name, func(t *testing.T) {
			if rec := finalizeStop(t, seeded(t), 0, s); rec.StopDetail != "out" {
				t.Fatalf("stop detail = %q, want the build's own %q", rec.StopDetail, "out")
			}
		})
	}
}

func TestFinalizeEndsAnEscalatedAttemptNamingTheOneBeforeIt(t *testing.T) {
	spent := "Escalation spent: attempt 1 failed its build on the escalated tier " + mimoModel + ", so the run ends here."
	earlier := Record{Outcome: OutcomeAgentFailed, StopReason: StopAgentExit,
		Steps: []Step{{Phase: PhasePlan, Model: "p/plan"}, {Phase: PhaseBuild, Model: flashModel}}}
	for name, tc := range map[string]struct {
		store RecordStore
		want  string
	}{
		"archived": {attemptRecords{fakeRecords: seeded(t), attempts: map[int]Record{0: earlier}},
			spent + " Attempt 0 ended agent-failed (agent-exit) on " + flashModel + "."},
		"archived with no reason or build": {attemptRecords{fakeRecords: seeded(t), attempts: map[int]Record{0: {Outcome: OutcomeAgentFailed}}},
			spent + " Attempt 0 ended agent-failed."},
		"never archived":  {attemptRecords{fakeRecords: seeded(t)}, spent},
		"unreadable":      {attemptRecords{fakeRecords: seeded(t), err: errors.New("down")}, spent + " Attempt 0 could not be read."},
		"no archive kept": {seeded(t), spent},
	} {
		t.Run(name, func(t *testing.T) {
			rec := finalizeStop(t, tc.store, 1, stoppedIn(PhaseBuild, OutcomeAgentFailed, StopAgentNoProgress, mimoModel, ""))
			if rec.StopDetail != tc.want {
				t.Fatalf("stop detail = %q, want %q", rec.StopDetail, tc.want)
			}
		})
	}
}

func TestFinalizeKeepsTheTierWhenTheBuildsDetailFillsTheLimit(t *testing.T) {
	rec := finalizeStop(t, seeded(t), 0, stoppedIn(PhaseBuild, OutcomeAgentFailed, StopAgentExit, flashModel,
		strings.Repeat("x", stopDetailLimit)))
	if !strings.HasPrefix(rec.StopDetail, "Next model tier: "+mimoModel) || len(rec.StopDetail) > stopDetailLimit+len("…") {
		t.Fatalf("stop detail = %.80q… (%d bytes), want the tier first within the limit", rec.StopDetail, len(rec.StopDetail))
	}
}

func TestFinalizeNamesNoTierForATransientAgentFailure(t *testing.T) {
	detail := strings.Repeat("x", stopDetailLimit-len("dial tcp: i/o timeout")) + "dial tcp: i/o timeout"
	rec := finalizeStop(t, seeded(t), 0, stoppedIn(PhaseBuild, OutcomeAgentFailed, StopAgentExit, flashModel, detail))
	if rec.Transient != TransientNetwork {
		t.Fatalf("transient = %q, want %q from the build's own detail", rec.Transient, TransientNetwork)
	}
	if rec.StopDetail != detail {
		t.Fatalf("stop detail = %.80q… (%d bytes), want the build's own, naming no tier", rec.StopDetail, len(rec.StopDetail))
	}
}

func TestFinalizeNamesTheRetriedAttemptPastAnAutomaticRequeue(t *testing.T) {
	retried := Record{Outcome: OutcomeAgentFailed, StopReason: StopAgentExit, Steps: []Step{{Phase: PhaseBuild, Model: flashModel}}}
	store := attemptRecords{fakeRecords: seeded(t), attempts: map[int]Record{0: retried}}
	rec := finalizeStop(t, store, 2, stoppedIn(PhaseBuild, OutcomeAgentFailed, StopAgentExit, mimoModel, ""))
	want := "Escalation spent: attempt 2 failed its build on the escalated tier " + mimoModel +
		", so the run ends here. Attempt 0 ended agent-failed (agent-exit) on " + flashModel + "."
	if rec.StopDetail != want {
		t.Fatalf("stop detail = %q, want %q", rec.StopDetail, want)
	}
}

func TestFinalizeReadsNoArchiveForAFailureBelowTheTopTier(t *testing.T) {
	var reads []int
	store := attemptRecords{fakeRecords: seeded(t), reads: &reads}
	finalizeStop(t, store, 1, stoppedIn(PhaseBuild, OutcomeAgentFailed, StopAgentExit, flashModel, ""))
	if len(reads) != 0 {
		t.Fatalf("archived attempts read = %v, want none for a failure that names the next tier", reads)
	}
}
