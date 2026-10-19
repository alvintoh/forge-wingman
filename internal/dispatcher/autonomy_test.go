package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeAutonomies reports the level configured, or fails to read it, counts the
// reads made, and records every level a rollback set.
type fakeAutonomies struct {
	level  Autonomy
	err    error
	reads  int
	setErr error
	set    []Autonomy
}

func (a *fakeAutonomies) Level(context.Context) (Autonomy, error) {
	a.reads++
	return a.level, a.err
}

func (a *fakeAutonomies) Set(_ context.Context, level Autonomy, _ time.Time) error {
	if a.setErr != nil {
		return a.setErr
	}
	a.set = append(a.set, level)
	a.level = level
	return nil
}

// sample is one merged agent PR as a condition reads it: the lines it measured,
// and whether it auto-merged and was later reverted.
func sample(repo string, agentLines, ownerLines int64, autoMerged, reverted bool) Sample {
	return Sample{
		Repo:       repo,
		Rewrite:    Rewrite{AgentLines: agentLines, OwnerLines: ownerLines},
		AutoMerged: autoMerged,
		Reverted:   reverted,
	}
}

// samples is n merged auto-merged PRs of repo that each measured agentLines over
// ownerLines and were never reverted.
func samples(repo string, n int, agentLines, ownerLines int64) []Sample {
	out := make([]Sample, n)
	for i := range out {
		out[i] = sample(repo, agentLines, ownerLines, true, false)
	}
	return out
}

// joined is the samples of parts in the order the windows read them, newest
// first.
func joined(parts ...[]Sample) []Sample {
	var out []Sample
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

// scratch is the repository the condition tests measure in.
const scratch = "octo/scratch"

// cdOnly is the recorded CD flag for scratch alone.
var cdOnly = map[string]bool{scratch: true}

func TestAutonomyLevels(t *testing.T) {
	for level, want := range map[Autonomy]struct {
		valid, autoMerges bool
		settlable         error
	}{
		-1: {false, false, ErrUnknownAutonomy},
		0:  {true, false, nil},
		3:  {true, false, nil},
		4:  {true, true, ErrAutonomyNotEarned},
		5:  {true, true, ErrAutonomyNotEarned},
		6:  {false, false, ErrUnknownAutonomy},
	} {
		if got := level.Valid(); got != want.valid {
			t.Errorf("Autonomy(%d).Valid() = %v, want %v", level, got, want.valid)
		}
		if got := level.AutoMerges(); got != want.autoMerges {
			t.Errorf("Autonomy(%d).AutoMerges() = %v, want %v", level, got, want.autoMerges)
		}
		if err := level.Settable(Evidence{}); !errors.Is(err, want.settlable) || (err == nil) != (want.settlable == nil) {
			t.Errorf("Autonomy(%d).Settable() = %v, want %v", level, err, want.settlable)
		}
		if got := level.Describe(); (got != "") != want.valid {
			t.Errorf("Autonomy(%d).Describe() = %q, want a line only for a level that exists", level, got)
		}
	}
}

func TestAutonomyRefusalNamesTheAdvanceCondition(t *testing.T) {
	for level, want := range map[Autonomy]string{
		4: "level 4 needs level 3 held, a rewrite rate of 5% or less over 10 PRs, zero gate misses, and CD on the target repository",
		5: "level 5 needs level 4 held over 20 sampled PRs with zero reverts",
	} {
		if err := level.Settable(Evidence{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Autonomy(%d).Settable() = %v, want it to contain %q", level, err, want)
		}
	}
}

func TestSettableRefusesALevelWhoseOwnNumbersFail(t *testing.T) {
	for name, tt := range map[string]struct {
		level Autonomy
		ev    Evidence
		want  string
	}{
		"level 4 held": {
			4,
			Evidence{Level: 3, Trailing: samples(scratch, 10, 100, 0), CD: cdOnly},
			"",
		},
		"level 4 at the rate exactly": {
			// 5 owner lines of 100 agent lines is 5%, which the condition allows.
			4,
			Evidence{Level: 3, Trailing: samples(scratch, 10, 100, 5), CD: cdOnly},
			"",
		},
		"level 4 over the rate": {
			// 10 owner lines of 100 agent lines is 10%.
			4,
			Evidence{Level: 3, Trailing: samples(scratch, 10, 100, 10), CD: cdOnly},
			"a rewrite rate of 10.0% over 10 of 10 PRs, 5% or less",
		},
		"level 4 with fewer PRs than the window": {
			4,
			Evidence{Level: 3, Trailing: samples(scratch, 9, 100, 0), CD: cdOnly},
			"a rewrite rate of 0.0% over 9 of 10 PRs, 5% or less",
		},
		"level 4 with the level below unheld": {
			4,
			Evidence{Level: 2, Trailing: samples(scratch, 10, 100, 0), CD: cdOnly},
			"level 3 held (level 2 is set)",
		},
		"level 4 with a gate miss": {
			4,
			Evidence{Level: 3, Trailing: joined([]Sample{{Repo: scratch, GateMiss: true}}, samples(scratch, 9, 100, 0)), CD: cdOnly},
			"1 gate misses over 10 of 10 PRs, zero expected",
		},
		"level 4 with no CD recorded": {
			4,
			Evidence{Level: 3, Trailing: samples(scratch, 10, 100, 0)},
			"CD recorded for " + scratch,
		},
		"level 4 with CD recorded for another repository": {
			4,
			Evidence{Level: 3, Trailing: samples(scratch, 10, 100, 0), CD: map[string]bool{"octo/other": true}},
			"CD recorded for " + scratch,
		},
		"level 5 held": {
			5,
			Evidence{Level: 4, Trailing: samples(scratch, 20, 100, 0)},
			"",
		},
		"level 5 with the window merged by hand": {
			5,
			Evidence{Level: 4, Trailing: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, false, false)})},
			"19 auto-merged over 20 of 20 sampled PRs",
		},
		"level 4 with a sampled rate over the rollback rate": {
			// The newest 10 are clean, the older 10 sit at 25%: 12.5% sampled.
			4,
			Evidence{Level: 3, Trailing: joined(samples(scratch, 10, 100, 0), samples(scratch, 10, 100, 25)), CD: cdOnly},
			"no rollback condition holds, but sampled-rate does",
		},
		"level 4 with a revert among the sampled 20": {
			4,
			Evidence{Level: 3, Trailing: joined(samples(scratch, 10, 100, 0), samples(scratch, 9, 100, 0), []Sample{sample(scratch, 100, 0, true, true)}), CD: cdOnly},
			"no rollback condition holds, but reverted-auto-merge does",
		},
		"level 5 with a revert in the window": {
			5,
			Evidence{Level: 4, Trailing: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, true, true)})},
			"1 reverts over 20 of 20 sampled PRs, zero expected",
		},
		"level 5 with fewer PRs than the window": {
			5,
			Evidence{Level: 4, Trailing: samples(scratch, 19, 100, 0)},
			"0 reverts over 19 of 20 sampled PRs",
		},
		"level 5 with level 4 unheld": {
			5,
			Evidence{Level: 3, Trailing: samples(scratch, 20, 100, 0)},
			"level 4 held (level 3 is set)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := tt.level.Settable(tt.ev)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("Settable(%d) = %v, want it to hold", tt.level, err)
			case tt.want == "":
			case err == nil:
				t.Fatalf("Settable(%d) held, want a refusal naming %q", tt.level, tt.want)
			case !strings.Contains(err.Error(), tt.want):
				t.Fatalf("Settable(%d) = %v, want it to name %q", tt.level, err, tt.want)
			}
		})
	}
}

func TestSettableLiftsTheRefusalOnlyWhileEveryNumberHolds(t *testing.T) {
	// 2 owner lines of 1000 agent lines over the full window is 0.2%: level 4 is
	// earned, which the blanket refusal would still have refused.
	ev := Evidence{
		Level:    3,
		Trailing: joined([]Sample{sample(scratch, 100, 1, true, false), sample(scratch, 100, 1, true, false)}, samples(scratch, 8, 100, 0)),
		CD:       cdOnly,
	}
	if err := Autonomy(4).Settable(ev); err != nil {
		t.Fatalf("Settable(4) = %v, want the blanket refusal lifted while every clause holds", err)
	}
	// One PR rewritten to 100 owner lines takes the window to 10.1%.
	ev.Trailing[0] = sample(scratch, 100, 100, true, false)
	if err := Autonomy(4).Settable(ev); !errors.Is(err, ErrAutonomyNotEarned) {
		t.Fatalf("Settable(4) = %v, want a 10.1%% rate over the window to be refused", err)
	}
}

func TestAdvanceNamesEveryNumberForLevelFour(t *testing.T) {
	// 6 owner lines of 1000 agent lines over 10 PRs is 0.6%.
	ev := Evidence{
		Level:    2,
		Trailing: joined([]Sample{sample(scratch, 100, 6, true, false)}, samples(scratch, 9, 100, 0)),
		CD:       cdOnly,
	}
	conds := Autonomy(4).Advance(ev)
	if len(conds) != 6 {
		t.Fatalf("Advance(4) = %d clauses, want 6: %+v", len(conds), conds)
	}
	for _, want := range []string{
		"level 3 held (level 2 is set)",
		"a rewrite rate of 0.6% over 10 of 10 PRs, 5% or less",
		"0 gate misses over 10 of 10 PRs, zero expected",
		"CD recorded for " + scratch,
		"no rollback condition holds",
		"0 PRs with unmeasured commits over 10 of 10 PRs, zero expected",
	} {
		found := false
		for _, c := range conds {
			found = found || strings.Contains(c.String(), want)
		}
		if !found {
			t.Errorf("Advance(4) = %+v, want a clause naming %q", conds, want)
		}
	}
	if AllHeld(conds) {
		t.Fatal("AllHeld() = true, want a level with a clause that does not hold to be unearned")
	}
	if conds[0].Held {
		t.Error("the level clause held, want level 2 set to fail the level-3 clause")
	}
	if !conds[1].Held || !conds[2].Held || !conds[3].Held || !conds[4].Held || !conds[5].Held {
		t.Errorf("Advance(4) = %+v, want the rate, gate and CD clauses to hold", conds)
	}
}

func TestRollbackForNamesEachCause(t *testing.T) {
	for name, tt := range map[string]struct {
		level Autonomy
		ev    Evidence
		want  Rollback
	}{
		"a reverted auto-merge": {
			4,
			Evidence{Level: 4, Trailing: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, true, true)})},
			RollbackReverted,
		},
		"a revert of a PR the owner merged by hand": {
			4,
			Evidence{Level: 4, Trailing: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, false, true)})},
			"",
		},
		"a sampled rate over 10%": {
			// 300 owner lines of 2000 agent lines over the sampled window is 15%,
			// while the trailing 10 hold no owner line at all.
			4,
			Evidence{Level: 4, Trailing: joined(samples(scratch, 10, 100, 0), []Sample{sample(scratch, 100, 300, true, false)}, samples(scratch, 9, 100, 0))},
			RollbackSampledRate,
		},
		"a sampled rate at 10% exactly": {
			// 200 owner lines of 2000 agent lines is exactly the sampled rate.
			4,
			Evidence{Level: 4, Trailing: joined(samples(scratch, 10, 100, 0), []Sample{sample(scratch, 100, 200, true, false)}, samples(scratch, 9, 100, 0))},
			"",
		},
		"a trailing-10 rate over the 20% ceiling": {
			// 50 owner lines of 100 agent lines over the trailing 10 is 50%, while
			// the sampled window's 1100 agent lines keep the sampled rate at 4.5%.
			4,
			Evidence{Level: 4, Trailing: joined(samples(scratch, 10, 10, 5), samples(scratch, 10, 100, 0))},
			RollbackCeiling,
		},
		"a trailing-10 rate at the ceiling exactly": {
			// 10 owner lines of 50 agent lines is exactly the 20% ceiling, which
			// the condition allows.
			4,
			Evidence{Level: 4, Trailing: joined(samples(scratch, 10, 5, 1), samples(scratch, 10, 100, 0))},
			"",
		},
		"a level that does not auto-merge": {
			3,
			Evidence{Level: 3, Trailing: joined(samples(scratch, 19, 100, 0), []Sample{sample(scratch, 100, 0, true, true)})},
			"",
		},
		"a level with nothing measured yet": {
			4, Evidence{Level: 4},
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := RollbackFor(tt.level, tt.ev); got != tt.want {
				t.Fatalf("RollbackFor(%d) = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}

func TestPollClaimsAtTheLevelItReadOnce(t *testing.T) {
	q := &fakeQueue{candidates: []Candidate{
		{RunID: "run-a", Repo: "octo/scratch", Size: "M"}, {RunID: "run-b", Repo: "octo/other", Size: "M"},
	}}
	autonomies := &fakeAutonomies{level: 3}
	deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
	deps.Autonomies = autonomies
	if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
		t.Fatal(err)
	}
	// The walk reads the level once for every claim it makes, and the rewrite
	// phase reads it once more to decide a rollback.
	if autonomies.reads != 2 || len(q.tryClaims) != 2 || q.tryClaims[0].facts.Autonomy != 3 || q.tryClaims[1].facts.Autonomy != 3 {
		t.Fatalf("reads = %d, tryClaims = %+v, want one read per claim and both claims at level 3", autonomies.reads, q.tryClaims)
	}
}

func TestPollClaimsAtLevelZeroWhenTheLevelCannotBeRead(t *testing.T) {
	for name, autonomies := range map[string]*fakeAutonomies{
		"the read fails":   {level: 3, err: errFirestore},
		"an unknown level": {level: 9},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{candidates: []Candidate{{RunID: "run-a", Repo: "octo/scratch", Size: "M"}}}
			var logs bytes.Buffer
			deps := pollDeps(fakeSource{}, q, &fakeWorkflow{})
			deps.Autonomies = autonomies
			deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			if _, err := Poll(context.Background(), deps, buildConfig); err != nil {
				t.Fatal(err)
			}
			if len(q.tryClaims) != 1 || q.tryClaims[0].facts.Autonomy != 0 {
				t.Fatalf("tryClaims = %+v, want the run claimed at level 0", q.tryClaims)
			}
			if !strings.Contains(logs.String(), "msg=autonomyUnread") {
				t.Fatalf("log = %q, want autonomyUnread", logs.String())
			}
			if len(autonomies.set) != 0 {
				t.Fatalf("set = %v, want no rollback from a level that counts as 0", autonomies.set)
			}
		})
	}
}

func TestAdvanceFourAgreesWithRollback(t *testing.T) {
	// A level the owner may set is never one the next poll takes back.
	for name, trailing := range map[string][]Sample{
		"sampled rate over":    joined(samples(scratch, 10, 100, 0), samples(scratch, 10, 100, 25)),
		"revert in the window": joined(samples(scratch, 10, 100, 0), []Sample{sample(scratch, 100, 0, true, true)}),
		"clean":                samples(scratch, 20, 100, 0),
	} {
		t.Run(name, func(t *testing.T) {
			ev := Evidence{Level: 3, Trailing: trailing, CD: cdOnly}
			settable := Autonomy(4).Settable(ev) == nil
			rolledBack := RollbackFor(4, ev) != ""
			if settable && rolledBack {
				t.Fatalf("level 4 is settable yet RollbackFor = %q", RollbackFor(4, ev))
			}
		})
	}
}

func TestMisidentifiedAuthorIsNotEarnableAndRollsBack(t *testing.T) {
	// Owner lines with no agent lines read as an unbounded rate, not a clean one.
	trailing := samples(scratch, 10, 0, 50)
	ev := Evidence{Level: 3, Trailing: trailing, CD: cdOnly}
	if err := Autonomy(4).Settable(ev); !errors.Is(err, ErrAutonomyNotEarned) {
		t.Fatalf("Settable(4) = %v, want a window with no agent lines refused", err)
	}
	if got := RollbackFor(4, ev); got == "" {
		t.Fatal("RollbackFor = \"\", want an unbounded rate to roll back")
	}
}

func TestClausesRefuseAShortOrEmptyWindow(t *testing.T) {
	if c := noGateMisses(samples(scratch, 9, 100, 0), 10); c.Held {
		t.Errorf("noGateMisses held over 9 of 10 PRs: %+v", c)
	}
	if c := noGateMisses(samples(scratch, 10, 100, 0), 10); !c.Held {
		t.Errorf("noGateMisses not held over 10 of 10 clean PRs: %+v", c)
	}
	if c := cdRecorded(nil, cdOnly); c.Held {
		t.Errorf("cdRecorded held over no PRs: %+v", c)
	}
}
