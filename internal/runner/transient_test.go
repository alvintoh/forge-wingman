package runner

import (
	"context"
	"slices"
	"testing"
	"time"
)

// proxyReadLog is a Go module proxy read error as the checks' own step prints
// one, which both a stop's own detail and the loop's give-up detail carry.
const proxyReadLog = "go mod download: reading https://proxy.golang.org/github.com/x/y/@v/v1.0.0.zip: 502 Bad Gateway"

func TestClassifyTransientNamesEachListedClass(t *testing.T) {
	for _, tt := range []struct {
		name string
		log  string
		want TransientClass
	}{
		{
			"a module proxy read error",
			"go: github.com/x/y@v1.0.0: reading https://proxy.golang.org/github.com/x/y/@v/v1.0.0.mod: 500 Internal Server Error (500)",
			TransientModuleProxy,
		},
		{
			"a checksum database read that also timed out, which is the proxy's class",
			"verifying go.sum: reading https://sum.golang.org/lookup/github.com/x/y@v1.0.0: dial tcp 1.2.3.4:443: i/o timeout",
			TransientModuleProxy,
		},
		{
			"a network timeout",
			"dial tcp 1.2.3.4:443: i/o timeout",
			TransientNetwork,
		},
		{
			"a host the runner could not resolve",
			"fatal: unable to access 'https://github.com/o/r.git/': Could not resolve host: github.com",
			TransientNetwork,
		},
		{
			"a lost runner",
			"##[error]The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.",
			TransientLostRunner,
		},
		{
			"a runner that lost the server",
			"##[error]Lost communication with the server. Verify the machine is running and has a healthy network connection.",
			TransientLostRunner,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyTransient(tt.log); got != tt.want {
				t.Fatalf("ClassifyTransient(%q) = %q, want %q", tt.log, got, tt.want)
			}
		})
	}
}

// TestClassifyTransientNamesNoClassForAFailureTheListDoesNotCarry is the
// classifier's half of the rule that only a listed fault is ever requeued.
func TestClassifyTransientNamesNoClassForAFailureTheListDoesNotCarry(t *testing.T) {
	for _, log := range []string{
		"",
		"agent exceeded 50m0s",
		"agent-exit: exit status 1",
		"plan-invalid: plan names no files block",
		"checks: test still failing after 3 round(s)\n\n--- FAIL: TestAdd (0.00s)",
		"stopped (workflow-change): the run touched .github/workflows/ci.yml",
	} {
		if got := ClassifyTransient(log); got != "" {
			t.Errorf("ClassifyTransient(%q) = %q, want no class", log, got)
		}
	}
}

func TestTransientClassesListsEveryClassTheClassifierNames(t *testing.T) {
	classes := TransientClasses()
	for _, want := range []TransientClass{TransientModuleProxy, TransientNetwork, TransientLostRunner} {
		if !slices.Contains(classes, want) {
			t.Fatalf("TransientClasses() = %v, want %q among them", classes, want)
		}
	}
}

// TestFinalizeRecordsTheClassTheFailingStepNames has the runner read the log a
// failing step left and record the class a poll requeues on, and holds it to
// leaving every other ending unclassified.
func TestFinalizeRecordsTheClassTheFailingStepNames(t *testing.T) {
	branch := BranchName(testTicket.BranchSegment(), "1-1")
	step := []Step{{Phase: PhaseBuild, Round: 1, Model: "command-code/x", CompletionsObject: completionsObject("1-1", PhaseBuild, 1), At: finalizeNow.Add(-30 * time.Minute)}}
	gaveUp := "checks: test still failing after 3 round(s)\n\n" + proxyReadLog
	for _, tt := range []struct {
		name string
		sum  Summary
		in   FinalizeInput
		want TransientClass
	}{
		{
			"a checks step that could not read the module proxy",
			Summary{Outcome: OutcomeInfraFailure, StopReason: StopChecksRun, Phase: PhaseBuild, Branch: branch, StopDetail: proxyReadLog},
			FinalizeInput{},
			TransientModuleProxy,
		},
		{
			"a worktree step that could not resolve the host",
			Summary{Outcome: OutcomeInfraFailure, StopReason: StopWorktree, Phase: PhaseWorktree, StopDetail: "fatal: unable to access 'https://github.com/o/r.git/': Could not resolve host: github.com"},
			FinalizeInput{},
			TransientNetwork,
		},
		{
			"a failing gate the loop gave up on",
			Summary{Outcome: OutcomeInfraFailure, StopReason: StopChecksRun, Phase: PhaseBuild, Branch: branch, LoopDetail: gaveUp},
			FinalizeInput{},
			TransientModuleProxy,
		},
		{
			"an agent timeout is left to the owner",
			Summary{Outcome: OutcomeAgentFailed, StopReason: StopAgentTimeout, Phase: PhaseBuild, Branch: branch, StopDetail: "agent exceeded 50m0s", Steps: step},
			FinalizeInput{},
			"",
		},
		{
			"a plan the agent could not produce is left to the owner",
			Summary{Outcome: OutcomeStopped, StopReason: StopPlanInvalid, Phase: PhasePlan, Branch: branch, StopDetail: "plan-invalid: plan names no files block"},
			FinalizeInput{},
			"",
		},
		{
			"a run that delivered its PR is never classified",
			Summary{Outcome: OutcomeBuilt, Phase: PhaseCommit, Branch: branch, Steps: step, LoopDetail: gaveUp},
			FinalizeInput{PRURL: "https://github.com/o/r/pull/1", RunResult: "success", PRResult: "success"},
			"",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := seeded(t)
			in := tt.in
			in.RunID, in.AttemptID, in.Identity = testRunID, "1-1", testIdentity
			in.Summary = encoded(t, tt.sum)

			if _, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow); err != nil {
				t.Fatal(err)
			}
			if got := store[testRunID].Transient; got != tt.want {
				t.Fatalf("the stored record carries class %q, want %q", got, tt.want)
			}
		})
	}
}
