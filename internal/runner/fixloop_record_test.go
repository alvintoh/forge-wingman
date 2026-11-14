package runner

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFixRoundsWaitNamesEverySpecificStop(t *testing.T) {
	tests := []struct {
		name string
		end  FixLoopEnd
		want string
	}{
		{"agent stop", FixLoopEnd{RoundReason: "agent-exit"}, "fix round stopped: agent-exit"},
		{"agent timeout", FixLoopEnd{RoundReason: "agent-timeout"}, "fix round stopped: agent-timeout"},
		{"agent no progress", FixLoopEnd{RoundReason: "agent-no-progress"}, "fix round stopped: agent-no-progress"},
		{"model unavailable", FixLoopEnd{RoundReason: "model-unavailable"}, "fix round stopped: model-unavailable"},
		{"commit refused", FixLoopEnd{RoundReason: "commit"}, "fix round stopped: commit"},
		{"git tampered", FixLoopEnd{RoundReason: "git-tampered"}, "fix round stopped: git-tampered"},
		{"head moved", FixLoopEnd{RoundReason: "head-moved"}, "fix round stopped: head-moved"},
		{"branch mismatch", FixLoopEnd{RoundReason: "branch-mismatch"}, "fix round stopped: branch-mismatch"},
		{"secret in branch", FixLoopEnd{RoundReason: "secret-in-branch"}, "fix round stopped: secret-in-branch"},
		{"workflow change", FixLoopEnd{RoundReason: "workflow-change"}, "fix round stopped: workflow-change"},
		{"run budget", FixLoopEnd{RoundReason: "run-budget"}, "fix round stopped: run-budget"},
		{"identity mismatch", FixLoopEnd{RoundReason: "identity-mismatch"}, "fix round stopped: identity-mismatch"},
		{"unclassified failure", FixLoopEnd{RoundReason: "error"}, "fix round stopped: error"},
		{"push bundle refs", FixLoopEnd{PushReason: "bundle-refs"}, "fix push refused: bundle-refs"},
		{"push wrong branch", FixLoopEnd{PushReason: "wrong-branch"}, "fix push refused: wrong-branch"},
		{"push not one commit", FixLoopEnd{PushReason: "not-one-commit"}, "fix push refused: not-one-commit"},
		{"push wrong parent", FixLoopEnd{PushReason: "wrong-parent"}, "fix push refused: wrong-parent"},
		{"push not runner", FixLoopEnd{PushReason: "not-runner"}, "fix push refused: not-runner"},
		{"push workflow file", FixLoopEnd{PushReason: "workflow-file"}, "fix push refused: workflow-file"},
		{"push secret", FixLoopEnd{PushReason: "secret"}, "fix push refused: secret"},
		{"push head moved", FixLoopEnd{PushReason: "head-moved"}, "fix push refused: head-moved"},
		{"push lease lost", FixLoopEnd{PushReason: "lease-lost"}, "fix push refused: lease-lost"},
		{"push reason not listed", FixLoopEnd{PushReason: "new-refusal"}, "fix push refused: new-refusal"},
		{"review timeout", FixLoopEnd{AwaitReason: "timeout"}, "review not posted: timeout"},
		{"review no verdict", FixLoopEnd{AwaitReason: "no-verdict"}, "review not posted: no-verdict"},
		{"review error", FixLoopEnd{AwaitReason: "error"}, "review not posted: error"},
		{"no change", FixLoopEnd{Stop: FixStopNoChange}, "fix loop ended: no-change"},
		{"unchanged", FixLoopEnd{Stop: FixStopUnchanged}, "fix loop ended: unchanged"},
		{"new findings", FixLoopEnd{Stop: FixStopNew}, "fix loop ended: new-findings"},
		{"declined", FixLoopEnd{Stop: FixStopDeclined}, "fix loop ended: declined"},
		{"budget", FixLoopEnd{Stop: FixStopBudget}, "fix loop ended: budget"},
		{"round cap", FixLoopEnd{Stop: FixStopCap}, "fix loop ended: round-cap"},
		{"round failure wins over a push refusal", FixLoopEnd{RoundReason: "commit", PushReason: "lease-lost", AwaitReason: "timeout", Stop: FixStopCap}, "fix round stopped: commit"},
		{"push refusal wins over a review that did not post", FixLoopEnd{PushReason: "lease-lost", AwaitReason: "timeout", Stop: FixStopCap}, "fix push refused: lease-lost"},
		{"unposted review wins over a loop stop", FixLoopEnd{AwaitReason: "timeout", Stop: FixStopCap}, "review not posted: timeout"},
		{"the job's own words on one line", FixLoopEnd{Stop: FixStopCap, RoundNote: "2 findings\nstill open"}, "fix loop ended: round-cap: 2 findings still open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, detail, ok := FixRoundsWait(tt.end)
			if !ok || reason != "fix-rounds-stopped" || detail != tt.want {
				t.Fatalf("FixRoundsWait = %q, %q, %v; want fix-rounds-stopped, %q, true", reason, detail, ok, tt.want)
			}
		})
	}
}

func TestFixRoundsWaitNamesNothingForALoopThatDidNotStop(t *testing.T) {
	for _, end := range []FixLoopEnd{{}, {AwaitReason: "clean"}, {AwaitReason: "findings"}} {
		if reason, detail, ok := FixRoundsWait(end); ok || reason != "" || detail != "" {
			t.Errorf("FixRoundsWait(%+v) = %q, %q, %v; want nothing", end, reason, detail, ok)
		}
	}
}

func TestFixRoundsWaitAppendsTheStoppingJobsOwnNote(t *testing.T) {
	end := FixLoopEnd{RoundNote: "round words", PushNote: "push words", AwaitNote: "await words"}
	for _, tt := range []struct {
		name  string
		stop  func(*FixLoopEnd)
		wants string
	}{
		{"round", func(e *FixLoopEnd) { e.RoundReason = "commit" }, "fix round stopped: commit: round words"},
		{"push", func(e *FixLoopEnd) { e.PushReason = "lease-lost" }, "fix push refused: lease-lost: push words"},
		{"await", func(e *FixLoopEnd) { e.AwaitReason = "timeout" }, "review not posted: timeout: await words"},
		{"loop", func(e *FixLoopEnd) { e.Stop = FixStopCap }, "fix loop ended: round-cap: round words"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := end
			tt.stop(&e)
			if _, detail, _ := FixRoundsWait(e); detail != tt.wants {
				t.Fatalf("detail = %q, want %q", detail, tt.wants)
			}
		})
	}
}

func TestFixRoundsWaitCutsALongNote(t *testing.T) {
	_, detail, _ := FixRoundsWait(FixLoopEnd{Stop: FixStopCap, RoundNote: strings.Repeat("x", 3*stopDetailLimit)})
	if len(detail) > stopDetailLimit+len("…") || !strings.HasPrefix(detail, "fix loop ended: round-cap: xxx") {
		t.Fatalf("detail is %d bytes, want it cut to %d and still opening with the stop", len(detail), stopDetailLimit)
	}
}

func TestValidateFixRoundHoldsARoundToASummarysLimits(t *testing.T) {
	good := validFixRound()
	if err := ValidateFixRound("1-1", good); err != nil {
		t.Fatalf("a valid round: %v", err)
	}
	tests := []struct {
		name string
		edit func(*ReviewFixRound)
	}{
		{"a model that is not provider/model", func(r *ReviewFixRound) { r.Step.Model = "x" }},
		{"a harness this run cannot route to", func(r *ReviewFixRound) { r.Step.Harness = "nope" }},
		{"another run's completions object", func(r *ReviewFixRound) { r.Step.CompletionsObject = "completions/1-2-fix-1.jsonl" }},
		{"counts with no completed re-review", func(r *ReviewFixRound) { r.Reviewed = false }},
		{"a negative new count", func(r *ReviewFixRound) { r.New = -1 }},
		{"a negative resolved count", func(r *ReviewFixRound) { r.Resolved = -1 }},
		{"a resolved count over the ceiling", func(r *ReviewFixRound) { r.Resolved = int(maxCount) + 1 }},
		{"a new count over the ceiling", func(r *ReviewFixRound) { r.New = int(maxCount) + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validFixRound()
			tt.edit(&r)
			if err := ValidateFixRound("1-1", r); err == nil {
				t.Fatal("ValidateFixRound accepted it")
			}
		})
	}
	unreviewed := validFixRound()
	unreviewed.Reviewed, unreviewed.Resolved = false, 0
	if err := ValidateFixRound("1-1", unreviewed); err != nil {
		t.Fatalf("a round that was not reviewed, with no counts: %v", err)
	}
}

func TestValidateMergedFixRoundRefusesAStartOutsideTheClocksWindow(t *testing.T) {
	if err := ValidateMergedFixRound("1-1", validFixRound(), finalizeNow); err != nil {
		t.Fatalf("a round started inside the window: %v", err)
	}
	tests := []struct {
		name string
		at   time.Time
	}{
		{"no start time", time.Time{}},
		{"a start time ahead of the clock", finalizeNow.Add(time.Hour)},
		{"a start time long before the clock", finalizeNow.Add(-maxSummaryAge - time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validFixRound()
			r.Step.At = tt.at
			if err := ValidateMergedFixRound("1-1", r, finalizeNow); err == nil {
				t.Fatal("ValidateMergedFixRound accepted it")
			}
		})
	}
	bad := validFixRound()
	bad.Step.Model = "x"
	if err := ValidateMergedFixRound("1-1", bad, finalizeNow); err == nil {
		t.Fatal("ValidateMergedFixRound accepted a round ValidateFixRound refuses")
	}
}

func TestParseSummaryAcceptsAFixRoundStartedOutsideTheClocksWindow(t *testing.T) {
	for _, at := range []time.Time{{}, finalizeNow.Add(time.Hour), finalizeNow.Add(-maxSummaryAge - time.Hour)} {
		s := validSummary()
		r := validFixRound()
		r.Step.At = at
		s.FixRounds = []ReviewFixRound{r}
		raw, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseSummary(raw, "1-1", testTicket, finalizeNow); err != nil {
			t.Fatalf("a summary's fix round started at %v: %v, want it accepted as before", at, err)
		}
	}
}

func fixRoundN(n int, cost float64) ReviewFixRound {
	r := validFixRound()
	r.Step.Round = n
	r.Step.CompletionsObject = completionsObject("1-1", PhaseFix, n)
	r.Step.Tokens = Usage{Input: 10, Cost: cost, Steps: 1}
	return r
}

func buildStep() Step {
	return Step{Phase: PhaseBuild, Round: 1, Model: "opencode-go/x", Tokens: Usage{Input: 100, Cost: 1, Steps: 1},
		CompletionsObject: completionsObject("1-1", PhaseBuild, 1), At: finalizeNow.Add(-30 * time.Minute)}
}

func TestWithFixRoundsMergesRoundsAndTheirSteps(t *testing.T) {
	rec := Record{Steps: []Step{buildStep()}, Tokens: Usage{Input: 100, Cost: 1, Steps: 1}}
	got, err := WithFixRounds(rec, []ReviewFixRound{fixRoundN(2, 0.5), fixRoundN(1, 0.25)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FixRounds) != 2 || got.FixRounds[0].Step.Round != 1 || got.FixRounds[1].Step.Round != 2 {
		t.Fatalf("rounds = %+v, want rounds 1 and 2 in that order", got.FixRounds)
	}
	if len(got.Steps) != 3 || got.Steps[0].Phase != PhaseBuild || got.Steps[1].Round != 1 || got.Steps[2].Round != 2 {
		t.Fatalf("steps = %+v, want the build step then the fix steps by round", got.Steps)
	}
	if want := (Usage{Input: 120, Cost: 1.75, Steps: 3}); got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v: the totals include the merged steps", got.Tokens, want)
	}
}

func TestWithFixRoundsIsIdempotent(t *testing.T) {
	rec := Record{Steps: []Step{buildStep()}}
	incoming := []ReviewFixRound{fixRoundN(1, 0.25), fixRoundN(2, 0.5)}
	once, err := WithFixRounds(rec, incoming)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := WithFixRounds(once, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if len(twice.FixRounds) != 2 || len(twice.Steps) != 3 || twice.Tokens != once.Tokens {
		t.Fatalf("after a second merge: %d rounds, %d steps, tokens %+v; want 2, 3 and %+v", len(twice.FixRounds), len(twice.Steps), twice.Tokens, once.Tokens)
	}
}

func TestWithFixRoundsReplacesTheRoundUnderTheSameKey(t *testing.T) {
	first := fixRoundN(1, 0.25)
	rec, err := WithFixRounds(Record{}, []ReviewFixRound{first, fixRoundN(2, 0.5)})
	if err != nil {
		t.Fatal(err)
	}
	redone := fixRoundN(1, 0.75)
	redone.Commit = strings.Repeat("b", 40)
	got, err := WithFixRounds(rec, []ReviewFixRound{redone})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FixRounds) != 2 || got.FixRounds[0].Commit != redone.Commit || got.FixRounds[1].Step.Round != 2 {
		t.Fatalf("rounds = %+v, want round 1 replaced and round 2 kept", got.FixRounds)
	}
	if got.Tokens.Cost != 1.25 {
		t.Fatalf("cost = %v, want 1.25: the replaced step counts once", got.Tokens.Cost)
	}
}

func TestWithFixRoundsKeysOnPhaseAsWellAsRound(t *testing.T) {
	buildRound := validFixRound()
	buildRound.Step.Phase, buildRound.Step.CompletionsObject = PhaseBuild, completionsObject("1-1", PhaseBuild, 1)
	got, err := WithFixRounds(Record{FixRounds: []ReviewFixRound{buildRound}, Steps: []Step{buildRound.Step}}, []ReviewFixRound{fixRoundN(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FixRounds) != 2 || len(got.Steps) != 2 {
		t.Fatalf("%d rounds and %d steps, want 2 and 2: a build round 1 and a fix round 1 are different rounds", len(got.FixRounds), len(got.Steps))
	}
}

func TestWithFixRoundsRederivesTheSettledCostOfASettledRecord(t *testing.T) {
	rec := Record{Steps: []Step{buildStep()}, SettledAt: finalizeNow, PriorSettledProviderCostMicros: 2_000_000, SettledProviderCostMicros: 3_000_000}
	got, err := WithFixRounds(rec, []ReviewFixRound{fixRoundN(1, 0.5)})
	if err != nil {
		t.Fatal(err)
	}
	if got.SettledProviderCostMicros != 3_500_000 {
		t.Fatalf("settled cost = %d, want 3500000: the build's 1.00 and the round's 0.50 over the 2.00 earlier attempts settled", got.SettledProviderCostMicros)
	}
}

func TestWithFixRoundsLeavesAnUnsettledRecordUnsettled(t *testing.T) {
	got, err := WithFixRounds(Record{}, []ReviewFixRound{fixRoundN(1, 0.5)})
	if err != nil {
		t.Fatal(err)
	}
	if got.SettledProviderCostMicros != 0 || !got.SettledAt.IsZero() {
		t.Fatalf("settled %d at %v, want a record Finalize has not settled left so", got.SettledProviderCostMicros, got.SettledAt)
	}
}

func TestWithFixRoundsRefusesWhatItCannotKeyOrPlace(t *testing.T) {
	notFix := fixRoundN(1, 0)
	notFix.Step.Phase = PhaseBuild
	tests := []struct {
		name     string
		incoming []ReviewFixRound
		want     error
	}{
		{"a round on another phase", []ReviewFixRound{notFix}, ErrFixRoundNotOnFixPhase},
		{"the same round twice", []ReviewFixRound{fixRoundN(1, 0), fixRoundN(1, 0)}, nil},
		{"round zero", []ReviewFixRound{fixRoundN(0, 0)}, nil},
		{"a round past the calls a loop numbers", []ReviewFixRound{fixRoundN(DefaultReviewFixRounds*maxCallsPerFixRound+1, 0)}, nil},
		{"more rounds than a loop makes", []ReviewFixRound{fixRoundN(1, 0), fixRoundN(2, 0), fixRoundN(3, 0), fixRoundN(4, 0)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := WithFixRounds(Record{}, tt.incoming)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("err = %v, want a refusal (%v)", err, tt.want)
			}
		})
	}
}

func mergedFixRecord(t *testing.T) fakeRecords {
	t.Helper()
	store := seeded(t)
	merged, err := WithFixRounds(store[testRunID], []ReviewFixRound{fixRoundN(1, 0.25), fixRoundN(2, 0.5)})
	if err != nil {
		t.Fatal(err)
	}
	store[testRunID] = merged
	return store
}

func TestFinalizeKeepsTheMergedFixRoundsAndTheirStepsOnARerun(t *testing.T) {
	store := mergedFixRecord(t)
	rec := finalizeRun(t, store, nil, encoded(t, validSummary()), finalizeNow)
	if len(rec.FixRounds) != 2 || rec.FixRounds[1].Step.Round != 2 {
		t.Fatalf("rounds = %+v, want both merged rounds kept", rec.FixRounds)
	}
	fixSteps := 0
	for _, st := range rec.Steps {
		if st.Phase == PhaseFix {
			fixSteps++
		}
	}
	if fixSteps != 2 || rec.Tokens.Input != 20 || rec.Tokens.Cost != 0.75 {
		t.Fatalf("%d fix steps, tokens %+v; want 2 fix steps and their 20 input tokens and 0.75 cost in the totals", fixSteps, rec.Tokens)
	}
	if want := int64(750_000); int64(rec.SettledProviderCostMicros) != want {
		t.Fatalf("settled cost = %d, want %d: it includes the merged rounds", rec.SettledProviderCostMicros, want)
	}
	if got := store[testRunID]; len(got.FixRounds) != 2 || len(got.Steps) != len(rec.Steps) {
		t.Fatalf("stored %d rounds and %d steps, want what Finalize returned", len(got.FixRounds), len(got.Steps))
	}
}

func TestFinalizeKeepsTheMergedFixRoundsOfARunWhoseSummaryIsLost(t *testing.T) {
	store := mergedFixRecord(t)
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: "1-1", SummaryUnreadable: true, RunResult: "failure"}
	rec, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.FixRounds) != 2 || len(rec.Steps) != 2 {
		t.Fatalf("%d rounds and %d steps, want the merged 2 and 2 kept beside a stop", len(rec.FixRounds), len(rec.Steps))
	}
}

func TestMergeAfterARerunOfTheRecordStepRestoresTheRounds(t *testing.T) {
	store := seeded(t)
	rec := finalizeRun(t, store, nil, encoded(t, validSummary()), finalizeNow)
	if len(rec.FixRounds) != 0 {
		t.Fatalf("rounds = %+v before any merge", rec.FixRounds)
	}
	got, err := WithFixRounds(rec, []ReviewFixRound{fixRoundN(1, 0.25), fixRoundN(2, 0.5)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FixRounds) != 2 || len(got.Steps) != len(rec.Steps)+2 {
		t.Fatalf("%d rounds and %d steps, want the 2 rounds and their steps added to the build's", len(got.FixRounds), len(got.Steps))
	}
}

func TestFinalizeCarriesOnlyTheFixPhaseRoundsOfTheExistingRecord(t *testing.T) {
	store := mergedFixRecord(t)
	rec := store[testRunID]
	buildRound := validFixRound()
	buildRound.Step.Phase, buildRound.Step.CompletionsObject = PhaseBuild, completionsObject("1-1", PhaseBuild, 1)
	rec.FixRounds = append(rec.FixRounds, buildRound)
	store[testRunID] = rec
	got := finalizeRun(t, store, nil, encoded(t, validSummary()), finalizeNow)
	if len(got.FixRounds) != 2 {
		t.Fatalf("rounds = %+v, want the two fix-phase rounds and not the build's own, which its summary rebuilds", got.FixRounds)
	}
}

// builtAttempt is a built run of attempt whose one build step cost cost.
func builtAttempt(attempt string, cost float64) Summary {
	return Summary{
		Outcome: OutcomeBuilt, Phase: PhaseCommit, Branch: BranchName(testTicket.BranchSegment(), attempt),
		Steps: []Step{{Phase: PhaseBuild, Round: 1, Model: "opencode-go/x", Tokens: Usage{Cost: cost, Steps: 1},
			CompletionsObject: completionsObject(attempt, PhaseBuild, 1), At: finalizeNow.Add(-30 * time.Minute)}},
	}
}

func finalizeAttempt(t *testing.T, store fakeRecords, attempt string, sum Summary) Record {
	t.Helper()
	in := FinalizeInput{Identity: testIdentity, RunID: testRunID, AttemptID: attempt, Summary: encoded(t, sum),
		RunResult: "success", PRResult: "success", PRURL: "https://github.com/o/r/pull/1"}
	rec, err := Finalize(context.Background(), store, &fakeLedger{}, nil, in, finalizeNow)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestARetryDoesNotCountTheEarlierAttemptsFixRoundsTwice(t *testing.T) {
	store := seeded(t)
	first := finalizeAttempt(t, store, "1-1", builtAttempt("1-1", 0))
	merged, err := WithFixRounds(first, []ReviewFixRound{fixRoundN(1, 0.25), fixRoundN(2, 0.5)})
	if err != nil {
		t.Fatal(err)
	}
	if merged.SettledProviderCostMicros != 750_000 {
		t.Fatalf("first attempt settled %d, want 750000", merged.SettledProviderCostMicros)
	}
	// A retry moves what the row settled into the prior total, as the requeue does.
	merged.PriorSettledProviderCostMicros = merged.SettledProviderCostMicros
	store[testRunID] = merged

	got := finalizeAttempt(t, store, "1-2", builtAttempt("1-2", 0.1))
	if got.SettledProviderCostMicros != 850_000 {
		t.Fatalf("settled %d, want 850000: the first attempt's 750000 once and the retry's own 100000", got.SettledProviderCostMicros)
	}
	if len(got.FixRounds) != 0 {
		t.Fatalf("rounds = %+v, want none of the earlier attempt's on the retry", got.FixRounds)
	}
	for _, st := range got.Steps {
		if st.Phase == PhaseFix {
			t.Fatalf("steps hold the earlier attempt's fix step %+v", st)
		}
	}
}

func TestWithFixRoundsKeysOnTheAttempt(t *testing.T) {
	retry := fixRoundN(1, 0.5)
	retry.Step.CompletionsObject = completionsObject("1-2", PhaseFix, 1)
	rec, err := WithFixRounds(Record{}, []ReviewFixRound{fixRoundN(1, 0.25)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := WithFixRounds(rec, []ReviewFixRound{retry})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FixRounds) != 2 || got.Tokens.Cost != 0.75 {
		t.Fatalf("%d rounds, cost %v; want both attempts' round 1 and 0.75", len(got.FixRounds), got.Tokens.Cost)
	}
}

func TestWithFixRoundsRefusesAMergePastTheAttemptsRoundCap(t *testing.T) {
	rec, err := WithFixRounds(Record{}, []ReviewFixRound{fixRoundN(1, 0), fixRoundN(2, 0), fixRoundN(3, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WithFixRounds(rec, []ReviewFixRound{fixRoundN(5, 0)}); !errors.Is(err, ErrFixRoundsOverCap) {
		t.Fatalf("err = %v, want ErrFixRoundsOverCap", err)
	}
	if _, err := WithFixRounds(rec, []ReviewFixRound{fixRoundN(3, 0)}); err != nil {
		t.Fatalf("merging a held round again: %v", err)
	}
}

func TestFinalizeCarriesEveryRoundOfItsAttemptWhateverTheirCount(t *testing.T) {
	for _, n := range []int{3, 4} {
		store := seeded(t)
		var rounds []ReviewFixRound
		for i := 1; i <= n; i++ {
			rounds = append(rounds, fixRoundN(i, 0))
		}
		store[testRunID] = mergeFixRounds(store[testRunID], rounds)
		got := finalizeAttempt(t, store, "1-1", builtAttempt("1-1", 0))
		if len(got.FixRounds) != n {
			t.Fatalf("%d rounds held: Finalize kept %d, want %d", n, len(got.FixRounds), n)
		}
	}
}

func TestWithFixRoundsRederivesTheTimeOfUseOfASettledRecord(t *testing.T) {
	round := fixRoundN(1, 0.5)
	round.Step.Model = "opencode-go/deepseek-v4.1-flash"
	// A Monday, inside that model's own 01:00-04:00 UTC peak window.
	round.Step.At = time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
	got, err := WithFixRounds(Record{SettledAt: finalizeNow, TimeOfUse: "off-peak"}, []ReviewFixRound{round})
	if err != nil {
		t.Fatal(err)
	}
	if got.TimeOfUse != "peak" {
		t.Fatalf("time of use = %q, want peak: the merged step ran at peak", got.TimeOfUse)
	}
}

func TestFixRoundsWaitKeepsEachInputToOneKnownLine(t *testing.T) {
	tests := []struct {
		name string
		end  FixLoopEnd
		want string
	}{
		{"a round reason that is not a stop reason", FixLoopEnd{RoundReason: "agent-failed\nINJECTED"}, "fix round stopped: error"},
		{"a stop that is not a fix stop", FixLoopEnd{Stop: "nope\nINJECTED"}, "fix loop ended: unknown"},
		{"a push reason across lines", FixLoopEnd{PushReason: "lease-lost\nINJECTED"}, "fix push refused: lease-lost INJECTED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, detail, _ := FixRoundsWait(tt.end); detail != tt.want {
				t.Fatalf("detail = %q, want %q", detail, tt.want)
			}
		})
	}
	_, detail, _ := FixRoundsWait(FixLoopEnd{PushReason: strings.Repeat("x", 3*stopDetailLimit)})
	if len(detail) > stopDetailLimit+len("…") {
		t.Fatalf("detail is %d bytes, want it cut to %d", len(detail), stopDetailLimit)
	}
}

func TestStopReasonsListsEveryStopReasonRecordDeclares(t *testing.T) {
	src, err := os.ReadFile("record.go")
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`Stop\w+\s+StopReason = "([a-z-]+)"`).FindAllSubmatch(src, -1)
	if len(declared) != len(stopReasons) {
		t.Fatalf("record.go declares %d stop reasons, the list holds %d", len(declared), len(stopReasons))
	}
	for _, m := range declared {
		if !slices.Contains(stopReasons, string(m[1])) {
			t.Errorf("%s is not listed", m[1])
		}
	}
}
